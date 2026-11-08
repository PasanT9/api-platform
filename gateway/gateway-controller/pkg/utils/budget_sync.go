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

package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"log/slog"
	"time"

	"github.com/wso2/api-platform/common/eventhub"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/lazyresourcexds"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/storage"
)

// ControlPlaneBudget is a budget as the control plane sends it, in budget.*
// events and in the full sync on connect.
type ControlPlaneBudget struct {
	UUID     string                `json:"uuid"`
	Revision int64                 `json:"revision"`
	Document models.BudgetDocument `json:"document"`
}

// PublishBudgetChange makes the policy engine follow a saved budget and tells the
// controller's replicas to reload it. Inactive budgets stay stored but are
// withdrawn from the policy engine, so they match no traffic.
func PublishBudgetChange(lrm *lazyresourcexds.LazyResourceStateManager, hub eventhub.EventHub, gatewayID, action string,
	b *models.StoredBudget, correlationID string, logger *slog.Logger) {
	if lrm != nil {
		var err error
		if action == "DELETE" || b.Status != "active" {
			err = lrm.RemoveResourceByIDAndType(b.UUID, "Budget", correlationID)
		} else {
			err = lrm.StoreResource(BudgetLazyResource(b), correlationID)
		}
		if err != nil && logger != nil {
			logger.Error("Failed to sync budget lazy resource", "budget", b.Handle, "error", err)
		}
	}
	if hub != nil {
		payload, _ := json.Marshal(map[string]string{"uuid": b.UUID})
		event := eventhub.Event{
			GatewayID: gatewayID, OriginatedTimestamp: time.Now().UTC(), EventType: eventhub.EventTypeBudget,
			Action: action, EntityID: b.Handle, EventID: correlationID, EventData: string(payload),
		}
		if err := hub.PublishEvent(gatewayID, event); err != nil && logger != nil {
			logger.Error("Failed to publish budget event", "budget", b.Handle, "error", err)
		}
	}
}

// ApplyControlPlaneBudget validates a budget from the control plane and saves it.
// It returns the stored budget and whether it changed anything; a repeated or
// older revision changes nothing.
func ApplyControlPlaneBudget(store storage.BudgetStorage, in ControlPlaneBudget) (*models.StoredBudget, bool, error) {
	if in.UUID == "" {
		return nil, false, fmt.Errorf("budget from the control plane has no uuid")
	}
	doc := in.Document
	if err := models.NormalizeAndValidateBudget(&doc); err != nil {
		return nil, false, fmt.Errorf("invalid budget %s: %w", doc.Metadata.Name, err)
	}
	b := &models.StoredBudget{
		UUID: in.UUID, Handle: doc.Metadata.Name, Name: doc.Spec.DisplayName,
		Version: "v1.0", DataVersion: "1.0", Revision: in.Revision, Status: doc.Spec.Status,
		Origin: models.OriginControlPlane, Document: doc,
		CreatedBy: "control-plane", UpdatedBy: "control-plane",
	}
	if doc.Spec.Resource != nil {
		b.ResourceKind = doc.Spec.Resource.Kind
	}
	applied, err := store.UpsertControlPlaneBudget(b)
	return b, applied, err
}

// ErrBudgetsNotServed means the control plane has no budgets endpoint.
var ErrBudgetsNotServed = errors.New("control plane does not serve budgets")

// FetchBudgets returns every budget of the gateway's organization, active and
// inactive, for the full sync on connect and reconnect.
func (s *APIUtilsService) FetchBudgets() ([]ControlPlaneBudget, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.getBaseURL()+"/budgets", nil)
	if err != nil {
		return nil, fmt.Errorf("create budgets request: %w", err)
	}
	req.Header.Add("api-key", s.config.Token)
	req.Header.Add("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch budgets: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrBudgetsNotServed
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("budgets request failed with status %d: %s", resp.StatusCode, string(body))
	}
	var budgets []ControlPlaneBudget
	if err := json.NewDecoder(resp.Body).Decode(&budgets); err != nil {
		return nil, fmt.Errorf("decode budgets response: %w", err)
	}
	return budgets, nil
}
