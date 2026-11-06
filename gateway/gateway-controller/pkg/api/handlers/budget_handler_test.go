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

package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/storage"
)

func TestBudgetManagementCRUD(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := storage.NewStorage(storage.BackendConfig{
		Type: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "gateway.db"), GatewayID: "gw-1",
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &APIServer{db: db, gatewayID: "gw-1", logger: logger}
	doc := models.BudgetDocument{
		APIVersion: models.BudgetAPIVersion, Kind: "Budget",
		Metadata: models.BudgetMetadata{Name: "team-monthly"},
		Spec: models.BudgetSpec{DisplayName: "Team monthly", Limit: models.BudgetLimit{
			Amount: "100.00", Period: "month", Action: "block",
		}},
	}
	request := func(method, path string, body any) *http.Request {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	create := httptest.NewRecorder()
	s.CreateBudget(create, request(http.MethodPost, "/budgets", doc))
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", create.Code, create.Body.String())
	}
	get := httptest.NewRecorder()
	s.GetBudget(get, request(http.MethodGet, "/budgets/team-monthly", nil), "team-monthly")
	if get.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", get.Code, get.Body.String())
	}
	list := httptest.NewRecorder()
	s.ListBudgets(list, request(http.MethodGet, "/budgets", nil))
	var result struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &result); err != nil || result.Count != 1 {
		t.Fatalf("list = %d %s: %v", list.Code, list.Body.String(), err)
	}
	doc.Spec.Limit.Amount = "150.00"
	doc.Spec.Status = "inactive"
	update := httptest.NewRecorder()
	s.UpdateBudget(update, request(http.MethodPut, "/budgets/team-monthly", doc), "team-monthly")
	if update.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", update.Code, update.Body.String())
	}
	doc.Spec.Limit.Period = "week"
	immutable := httptest.NewRecorder()
	s.UpdateBudget(immutable, request(http.MethodPut, "/budgets/team-monthly", doc), "team-monthly")
	if immutable.Code != http.StatusConflict {
		t.Fatalf("period edit = %d: %s", immutable.Code, immutable.Body.String())
	}
	deleted := httptest.NewRecorder()
	s.DeleteBudget(deleted, request(http.MethodDelete, "/budgets/team-monthly", nil), "team-monthly")
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete = %d: %s", deleted.Code, deleted.Body.String())
	}
	missing := httptest.NewRecorder()
	s.GetBudget(missing, request(http.MethodGet, "/budgets/team-monthly", nil), "team-monthly")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d", missing.Code)
	}
}
