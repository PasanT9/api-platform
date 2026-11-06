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

package eventlistener

import (
	"encoding/json"
	"errors"

	"github.com/wso2/api-platform/common/eventhub"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/storage"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/utils"
)

func (l *EventListener) processBudgetEvent(event eventhub.Event) {
	if l.lazyResourceManager == nil {
		return
	}
	store, ok := l.db.(storage.BudgetStorage)
	if !ok {
		l.logger.Error("Budget storage unavailable for replica sync")
		return
	}
	var data struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal([]byte(event.EventData), &data); err != nil || data.UUID == "" {
		l.logger.Error("Invalid budget event data", "budget", event.EntityID, "error", err)
		return
	}
	if event.Action == "DELETE" {
		if err := l.lazyResourceManager.RemoveResourceByIDAndType(data.UUID, "Budget", event.EventID); err != nil {
			l.logger.Error("Failed to remove deleted budget", "budget", event.EntityID, "error", err)
		}
		return
	}
	if event.Action != "CREATE" && event.Action != "UPDATE" {
		l.logger.Warn("Unknown budget event action", "action", event.Action)
		return
	}
	b, err := store.GetBudget(event.EntityID)
	if errors.Is(err, storage.ErrNotFound) {
		_ = l.lazyResourceManager.RemoveResourceByIDAndType(data.UUID, "Budget", event.EventID)
		return
	}
	if err != nil {
		l.logger.Error("Failed to load budget for replica sync", "budget", event.EntityID, "error", err)
		return
	}
	if b.UUID != data.UUID {
		_ = l.lazyResourceManager.RemoveResourceByIDAndType(data.UUID, "Budget", event.EventID)
		return
	}
	if b.Status != "active" {
		_ = l.lazyResourceManager.RemoveResourceByIDAndType(b.UUID, "Budget", event.EventID)
		return
	}
	if err := l.lazyResourceManager.StoreResource(utils.BudgetLazyResource(b), event.EventID); err != nil {
		l.logger.Error("Failed to sync budget lazy resource", "budget", b.Handle, "error", err)
	}
}

// reconcileBudgets repairs missed publication events and replica reconnects by
// comparing the durable SQL set with the in-memory xDS source of truth.
func (l *EventListener) reconcileBudgets() {
	if l.lazyResourceManager == nil {
		return
	}
	store, ok := l.db.(storage.BudgetStorage)
	if !ok {
		return
	}
	budgets, err := store.ListBudgets()
	if err != nil {
		l.logger.Error("Failed to reconcile budget definitions", "error", err)
		return
	}
	wanted := make(map[string]bool)
	current := l.lazyResourceManager.GetResourcesByType("Budget")
	for _, b := range budgets {
		if b.Status != "active" {
			continue
		}
		wanted[b.UUID] = true
		if existing, found := current[b.UUID]; found {
			if revision, ok := existing.Resource["revision"].(int64); ok && revision == b.Revision {
				continue
			}
		}
		if err := l.lazyResourceManager.StoreResource(utils.BudgetLazyResource(b), "reconcile"); err != nil {
			l.logger.Error("Failed to reconcile budget", "budget", b.Handle, "error", err)
		}
	}
	for id := range current {
		if !wanted[id] {
			if err := l.lazyResourceManager.RemoveResourceByIDAndType(id, "Budget", "reconcile"); err != nil {
				l.logger.Error("Failed to remove stale budget resource", "uuid", id, "error", err)
			}
		}
	}
}
