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
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/lazyresourcexds"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/storage"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/utils"
)

func budgetTestClient(t *testing.T, serverURL string) (*Client, storage.BudgetStorage, *lazyresourcexds.LazyResourceStateManager) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := storage.NewStorage(storage.BackendConfig{
		Type: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "budgets.db"), GatewayID: "gw-1",
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	lazyStore := storage.NewLazyResourceStore(logger)
	lrm := lazyresourcexds.NewLazyResourceStateManager(lazyStore, lazyresourcexds.NewLazyResourceSnapshotManager(lazyStore, logger), logger)
	client := &Client{logger: logger, db: db, eventHub: &mockControlPlaneEventHub{}, gatewayID: "gw-1", lazyResourceManager: lrm}
	if serverURL != "" {
		client.apiUtilsService = utils.NewAPIUtilsService(utils.PlatformAPIConfig{BaseURL: serverURL, Token: "token"}, http.DefaultClient, logger)
	}
	return client, db.(storage.BudgetStorage), lrm
}

func cpBudget(id, handle string, revision int64, status string) utils.ControlPlaneBudget {
	return utils.ControlPlaneBudget{UUID: id, Revision: revision, Document: models.BudgetDocument{
		APIVersion: models.BudgetAPIVersion, Kind: "Budget",
		Metadata: models.BudgetMetadata{Name: handle},
		Spec: models.BudgetSpec{
			DisplayName: handle, Status: status,
			Limit:    models.BudgetLimit{Amount: "100.00", Period: "month", Action: "block"},
			Resource: &models.BudgetResource{Kind: "LlmProvider", ID: "openai"},
		},
	}}
}

func budgetEvent(t *testing.T, eventType string, b utils.ControlPlaneBudget) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(BudgetEvent{Type: eventType, Payload: b, CorrelationID: uuid.NewString()})
	require.NoError(t, err)
	var event map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &event))
	return event
}

func TestBudgetEventsKeepDisabledBudgets(t *testing.T) {
	client, store, lrm := budgetTestClient(t, "")
	id := uuid.NewString()

	client.handleBudgetSavedEvent(budgetEvent(t, "budget.created", cpBudget(id, "api-platform-openai", 1, "active")))
	require.Contains(t, lrm.GetResourcesByType("Budget"), id, "an active budget is enforced")

	client.handleBudgetSavedEvent(budgetEvent(t, "budget.updated", cpBudget(id, "api-platform-openai", 2, "inactive")))
	stored, err := store.GetBudgetByUUID(id)
	require.NoError(t, err, "a disabled budget stays stored")
	require.Equal(t, "inactive", stored.Status)
	require.NotContains(t, lrm.GetResourcesByType("Budget"), id, "a disabled budget is not enforced")

	client.handleBudgetSavedEvent(budgetEvent(t, "budget.updated", cpBudget(id, "api-platform-openai", 1, "active")))
	stored, _ = store.GetBudgetByUUID(id)
	require.Equal(t, "inactive", stored.Status, "an older event is ignored")

	client.handleBudgetSavedEvent(budgetEvent(t, "budget.updated", cpBudget(id, "api-platform-openai", 3, "active")))
	require.Contains(t, lrm.GetResourcesByType("Budget"), id, "re-enabling enforces it again")

	client.handleBudgetDeletedEvent(budgetEvent(t, "budget.deleted", cpBudget(id, "api-platform-openai", 4, "active")))
	_, err = store.GetBudgetByUUID(id)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.NotContains(t, lrm.GetResourcesByType("Budget"), id)
}

func TestSyncBudgetsSavesInactiveAndRemovesStaleControlPlaneBudgets(t *testing.T) {
	keptID, staleID, disabledID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/budgets", r.URL.Path)
		require.Equal(t, "token", r.Header.Get("api-key"))
		_ = json.NewEncoder(w).Encode([]utils.ControlPlaneBudget{
			cpBudget(keptID, "kept", 1, "active"),
			cpBudget(disabledID, "disabled", 5, "inactive"),
		})
	}))
	defer server.Close()
	client, store, lrm := budgetTestClient(t, server.URL)

	client.applyBudget(cpBudget(keptID, "kept", 1, "active"), "setup")
	client.applyBudget(cpBudget(staleID, "stale", 1, "active"), "setup")
	local := cpBudget(uuid.NewString(), "gateway-local", 0, "active").Document
	require.NoError(t, store.CreateBudget(&models.StoredBudget{UUID: uuid.NewString(), Handle: "gateway-local", Name: "Local",
		Version: "v1.0", DataVersion: "1.0", Status: "active", Origin: models.OriginGatewayAPI, Document: func() models.BudgetDocument {
			local.Spec.Resource = nil
			return local
		}()}))

	client.syncBudgets()

	_, err := store.GetBudgetByUUID(staleID)
	require.ErrorIs(t, err, storage.ErrNotFound, "a control-plane budget no longer listed is removed")
	disabled, err := store.GetBudgetByUUID(disabledID)
	require.NoError(t, err, "inactive budgets from the sync are stored")
	require.Equal(t, "inactive", disabled.Status)
	_, err = store.GetBudget("gateway-local")
	require.NoError(t, err, "budgets created on the gateway are left alone")
	resources := lrm.GetResourcesByType("Budget")
	require.Contains(t, resources, keptID)
	require.NotContains(t, resources, disabledID)
	require.NotContains(t, resources, staleID)
}
