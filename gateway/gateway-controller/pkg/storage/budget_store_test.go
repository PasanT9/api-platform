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

package storage

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
)

func testBudgetDocument() models.BudgetDocument {
	return models.BudgetDocument{
		APIVersion: models.BudgetAPIVersion,
		Kind:       "Budget",
		Metadata:   models.BudgetMetadata{Name: "team-monthly"},
		Spec: models.BudgetSpec{
			DisplayName: "Team monthly", Status: "active",
			Limit: models.BudgetLimit{Amount: "100.00", Currency: "USD", Period: "month", Action: "block"},
		},
	}
}

func TestBudgetStoreCRUDAndHistoricalBinding(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStorage(BackendConfig{Type: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "gateway.db"), GatewayID: "gw-1"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sqlStore := store.(*sqlStore)
	resourceUUID := uuid.NewString()
	insertResource := func(id string) {
		t.Helper()
		_, err := sqlStore.db.Exec(`INSERT INTO artifacts
            (uuid, gateway_id, display_name, version, kind, handle, desired_state, origin)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, "gw-1", "Proxy", "v1", "LlmProxy", "proxy-one", "undeployed", "gateway_api")
		if err != nil {
			t.Fatal(err)
		}
	}
	insertResource(resourceUUID)
	doc := testBudgetDocument()
	doc.Spec.Resource = &models.BudgetResource{Kind: "LlmProxy", ID: "proxy-one", Allocation: "shared"}
	b := &models.StoredBudget{
		UUID: uuid.NewString(), Handle: doc.Metadata.Name, Name: doc.Spec.DisplayName,
		Version: "v1.0", DataVersion: "1.0", Status: "active", Origin: models.OriginGatewayAPI,
		ResourceKind: "LlmProxy", Document: doc, CreatedBy: "admin", UpdatedBy: "admin",
	}
	if err := sqlStore.CreateBudget(b); err != nil {
		t.Fatal(err)
	}
	if b.ResourceUUID != resourceUUID || b.Revision != 1 {
		t.Fatalf("unexpected created binding or revision: %+v", b)
	}
	if err := sqlStore.CreateBudget(b); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create = %v, want conflict", err)
	}
	got, err := sqlStore.GetBudget(b.Handle)
	if err != nil {
		t.Fatal(err)
	}
	got.Document.Spec.Limit.Amount = "150.00"
	got.Document.Spec.Status = "inactive"
	if err := sqlStore.UpdateBudget(got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.Status != "inactive" {
		t.Fatalf("unexpected update: %+v", got)
	}
	stale := *b
	stale.Document.Spec.Limit.Amount = "200.00"
	if err := sqlStore.UpdateBudget(&stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v, want conflict", err)
	}
	if _, err := sqlStore.db.Exec(`DELETE FROM artifacts WHERE gateway_id = ? AND uuid = ?`, "gw-1", resourceUUID); err != nil {
		t.Fatal(err)
	}
	insertResource(uuid.NewString())
	got, err = sqlStore.GetBudget(b.Handle)
	if err != nil || got.ResourceUUID != resourceUUID {
		t.Fatalf("historical binding changed: budget=%+v err=%v", got, err)
	}
	items, err := sqlStore.ListBudgets()
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %v, %v", items, err)
	}
	if err := sqlStore.DeleteBudget(b.Handle); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlStore.GetBudget(b.Handle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v, want not found", err)
	}
}

func TestBudgetUpdateCanDisableAfterAPIKeyDeletion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewStorage(BackendConfig{Type: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "gateway.db"), GatewayID: "gw-1"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sqlStore := store.(*sqlStore)
	keyID := uuid.NewString()
	ownerID := uuid.NewString()
	_, err = sqlStore.db.Exec(`INSERT INTO artifacts
        (uuid, gateway_id, display_name, version, kind, handle, desired_state, origin)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, ownerID, "gw-1", "Provider", "v1", "LlmProvider", "provider-one", "undeployed", "gateway_api")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqlStore.db.Exec(`INSERT INTO api_keys
        (uuid, gateway_id, name, api_key, masked_api_key, artifact_uuid)
        VALUES (?, ?, ?, ?, ?, ?)`, keyID, "gw-1", "personal", "hash", "masked", ownerID)
	if err != nil {
		t.Fatal(err)
	}
	doc := testBudgetDocument()
	owner := &models.BudgetKeyOwner{Kind: "LlmProvider", ID: "provider-one"}
	doc.Spec.Consumers = []models.BudgetConsumer{{Type: "apiKey", ID: "personal", Owner: owner, Allocation: "shared"}}
	b := &models.StoredBudget{
		UUID: uuid.NewString(), Handle: doc.Metadata.Name, Name: doc.Spec.DisplayName,
		Version: "v1.0", DataVersion: "1.0", Status: "active", Origin: models.OriginGatewayAPI,
		Document: doc, CreatedBy: "admin", UpdatedBy: "admin",
	}
	if err := sqlStore.CreateBudget(b); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlStore.db.Exec(`DELETE FROM api_keys WHERE gateway_id = ? AND uuid = ?`, "gw-1", keyID); err != nil {
		t.Fatal(err)
	}
	got, err := sqlStore.GetBudget(b.Handle)
	if err != nil {
		t.Fatal(err)
	}
	got.Document.Spec.Status = "inactive"
	if err := sqlStore.UpdateBudget(got); err != nil {
		t.Fatalf("deactivation after key deletion: %v", err)
	}
	got.Document.Spec.Exemptions = []models.BudgetConsumer{{Type: "apiKey", ID: "missing-key", Owner: owner}}
	if err := sqlStore.UpdateBudget(got); !errors.Is(err, ErrNotFound) {
		t.Fatalf("new nonexistent exemption = %v, want not found", err)
	}
}
