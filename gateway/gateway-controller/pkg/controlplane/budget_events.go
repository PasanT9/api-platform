/*
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

package controlplane

import (
	"errors"
	"log/slog"

	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/storage"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/utils"
)

// BudgetEvent is a budget.created, budget.updated or budget.deleted event from
// the control plane. Disabling and enabling arrive as budget.updated with the
// new status; only deleting a budget sends budget.deleted.
type BudgetEvent struct {
	Type          string                   `json:"type"`
	Payload       utils.ControlPlaneBudget `json:"payload"`
	Timestamp     string                   `json:"timestamp"`
	CorrelationID string                   `json:"correlationId"`
}

func (c *Client) budgetStorage() (storage.BudgetStorage, bool) {
	store, ok := c.db.(storage.BudgetStorage)
	return store, ok
}

// handleBudgetSavedEvent stores a created, updated, disabled or enabled budget.
func (c *Client) handleBudgetSavedEvent(event map[string]interface{}) {
	var e BudgetEvent
	if err := utils.MapToStruct(event, &e); err != nil {
		c.logger.Error("Failed to parse budget event", slog.Any("error", err))
		return
	}
	c.applyBudget(e.Payload, e.CorrelationID)
}

func (c *Client) applyBudget(in utils.ControlPlaneBudget, correlationID string) bool {
	store, ok := c.budgetStorage()
	if !ok {
		return false
	}
	logger := c.logger.With(slog.String("budget_uuid", in.UUID), slog.String("correlation_id", correlationID))
	b, applied, err := utils.ApplyControlPlaneBudget(store, in)
	if err != nil {
		logger.Error("Failed to save budget from the control plane", slog.Any("error", err))
		return false
	}
	if !applied {
		logger.Debug("Budget event is not newer than the stored budget; ignored", slog.Int64("revision", in.Revision))
		return true
	}
	action := "UPDATE"
	if in.Revision <= 1 {
		action = "CREATE"
	}
	utils.PublishBudgetChange(c.lazyResourceManager, c.eventHub, c.gatewayID, action, b, correlationID, logger)
	logger.Info("Budget saved from the control plane", slog.String("budget", b.Handle), slog.String("status", b.Status))
	return true
}

// handleBudgetDeletedEvent removes a deleted budget.
func (c *Client) handleBudgetDeletedEvent(event map[string]interface{}) {
	var e BudgetEvent
	if err := utils.MapToStruct(event, &e); err != nil {
		c.logger.Error("Failed to parse budget.deleted event", slog.Any("error", err))
		return
	}
	c.deleteBudget(e.Payload.UUID, e.CorrelationID)
}

func (c *Client) deleteBudget(id, correlationID string) {
	store, ok := c.budgetStorage()
	if !ok || id == "" {
		return
	}
	logger := c.logger.With(slog.String("budget_uuid", id), slog.String("correlation_id", correlationID))
	b, err := store.GetBudgetByUUID(id)
	if errors.Is(err, storage.ErrNotFound) {
		return
	}
	if err != nil {
		logger.Error("Failed to load budget for deletion", slog.Any("error", err))
		return
	}
	if b.Origin != models.OriginControlPlane {
		logger.Warn("Ignoring control plane delete of a budget created on this gateway", slog.String("budget", b.Handle))
		return
	}
	if err := store.DeleteBudgetByUUID(id); err != nil && !errors.Is(err, storage.ErrNotFound) {
		logger.Error("Failed to delete budget", slog.Any("error", err))
		return
	}
	utils.PublishBudgetChange(c.lazyResourceManager, c.eventHub, c.gatewayID, "DELETE", b, correlationID, logger)
	logger.Info("Budget deleted by the control plane", slog.String("budget", b.Handle))
}

// syncBudgets makes the stored control-plane budgets match the control plane on
// connect and reconnect: it saves newer ones, including inactive budgets, and
// removes those the control plane no longer has. Budgets created on this
// gateway's own API are left alone.
func (c *Client) syncBudgets() {
	if c.apiUtilsService == nil {
		return
	}
	store, ok := c.budgetStorage()
	if !ok {
		return
	}
	budgets, err := c.apiUtilsService.FetchBudgets()
	if errors.Is(err, utils.ErrBudgetsNotServed) {
		c.logger.Debug("Control plane does not serve budgets; skipping budget sync")
		return
	}
	if err != nil {
		c.logger.Warn("Failed to sync budgets from the control plane", slog.Any("error", err))
		return
	}
	fetched := make(map[string]bool, len(budgets))
	for _, b := range budgets {
		c.applyBudget(b, "budget-sync")
		fetched[b.UUID] = true // listed: keep it even if this save failed
	}
	local, err := store.ListBudgets()
	if err != nil {
		c.logger.Warn("Failed to list budgets for sync", slog.Any("error", err))
		return
	}
	for _, b := range local {
		if b.Origin == models.OriginControlPlane && !fetched[b.UUID] {
			c.deleteBudget(b.UUID, "budget-sync")
		}
	}
	c.logger.Info("Budget sync complete", slog.Int("total", len(budgets)))
}
