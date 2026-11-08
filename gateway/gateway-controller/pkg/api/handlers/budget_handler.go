/*
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/wso2/api-platform/common/authenticators"
	api "github.com/wso2/api-platform/gateway/gateway-controller/pkg/api/management"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/api/middleware"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/storage"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/utils"
	"github.com/wso2/api-platform/httpkit/httputil"
	"gopkg.in/yaml.v3"
)

const maxBudgetBodyBytes = 1 << 20

type budgetResourceResponse struct {
	models.BudgetDocument
	Status struct {
		ID        string    `json:"id"`
		CreatedAt time.Time `json:"createdAt"`
		UpdatedAt time.Time `json:"updatedAt"`
	} `json:"status"`
}

func budgetResponse(b *models.StoredBudget) budgetResourceResponse {
	r := budgetResourceResponse{BudgetDocument: b.Document}
	r.Status.ID = b.Handle
	r.Status.CreatedAt = b.CreatedAt
	r.Status.UpdatedAt = b.UpdatedAt
	return r
}

func (s *APIServer) budgetStore() (storage.BudgetStorage, bool) {
	store, ok := s.db.(storage.BudgetStorage)
	return store, ok
}

func budgetError(w http.ResponseWriter, code int, message string) {
	httputil.WriteJSON(w, code, api.ErrorResponse{Status: "error", Message: message})
}

func decodeBudget(r *http.Request, w http.ResponseWriter) (*models.BudgetDocument, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBudgetBodyBytes)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	var b models.BudgetDocument
	switch mediaType {
	case "application/json":
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&b); err != nil {
			return nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("request body must contain exactly one document")
		}
	case "application/yaml", "application/x-yaml", "text/yaml":
		decoder := yaml.NewDecoder(r.Body)
		decoder.KnownFields(true)
		if err := decoder.Decode(&b); err != nil {
			return nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("request body must contain exactly one document")
		}
	default:
		return nil, fmt.Errorf("Content-Type must be application/json or application/yaml")
	}
	if err := models.NormalizeAndValidateBudget(&b); err != nil {
		return nil, err
	}
	return &b, nil
}

func budgetActor(r *http.Request) string {
	if ac, ok := authenticators.GetAuthContext(r); ok && ac.Authenticated {
		return ac.UserID
	}
	return "system"
}

func (s *APIServer) publishBudgetChange(r *http.Request, action string, b *models.StoredBudget) {
	utils.PublishBudgetChange(s.lazyResourceManager, s.eventHub, s.gatewayID, action, b, middleware.GetCorrelationID(r), s.logger)
}

// controlPlaneOwned refuses gateway API changes to a budget the control plane
// manages; the control plane's next event or sync would overwrite them.
func controlPlaneOwned(w http.ResponseWriter, b *models.StoredBudget) bool {
	if b.Origin != models.OriginControlPlane {
		return false
	}
	budgetError(w, http.StatusConflict, "Budget is managed by the control plane; change it there")
	return true
}

func (s *APIServer) CreateBudget(w http.ResponseWriter, r *http.Request) {
	store, ok := s.budgetStore()
	if !ok {
		budgetError(w, http.StatusInternalServerError, "Budget storage unavailable")
		return
	}
	doc, err := decodeBudget(r, w)
	if err != nil {
		budgetError(w, http.StatusBadRequest, err.Error())
		return
	}
	b := &models.StoredBudget{
		UUID: uuid.NewString(), Handle: doc.Metadata.Name, Name: doc.Spec.DisplayName,
		Version: "v1.0", DataVersion: "1.0", Status: doc.Spec.Status,
		Origin: models.OriginGatewayAPI, Document: *doc,
		CreatedBy: budgetActor(r), UpdatedBy: budgetActor(r),
	}
	if doc.Spec.Resource != nil {
		b.ResourceKind = doc.Spec.Resource.Kind
	}
	if err := store.CreateBudget(b); err != nil {
		switch {
		case storage.IsNotFoundError(err):
			budgetError(w, http.StatusBadRequest, err.Error())
		case storage.IsConflictError(err):
			budgetError(w, http.StatusConflict, err.Error())
		default:
			budgetError(w, http.StatusInternalServerError, "Failed to create budget")
		}
		return
	}
	s.publishBudgetChange(r, "CREATE", b)
	httputil.WriteJSON(w, http.StatusCreated, budgetResponse(b))
}

func (s *APIServer) ListBudgets(w http.ResponseWriter, r *http.Request) {
	store, ok := s.budgetStore()
	if !ok {
		budgetError(w, http.StatusInternalServerError, "Budget storage unavailable")
		return
	}
	budgets, err := store.ListBudgets()
	if err != nil {
		budgetError(w, http.StatusInternalServerError, "Failed to list budgets")
		return
	}
	items := make([]budgetResourceResponse, 0, len(budgets))
	for _, b := range budgets {
		items = append(items, budgetResponse(b))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"status": "success", "count": len(items), "budgets": items})
}

func (s *APIServer) GetBudget(w http.ResponseWriter, r *http.Request, id string) {
	store, ok := s.budgetStore()
	if !ok {
		budgetError(w, http.StatusInternalServerError, "Budget storage unavailable")
		return
	}
	b, err := store.GetBudget(id)
	if storage.IsNotFoundError(err) {
		budgetError(w, http.StatusNotFound, "Budget not found")
		return
	}
	if err != nil {
		budgetError(w, http.StatusInternalServerError, "Failed to get budget")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, budgetResponse(b))
}

func (s *APIServer) UpdateBudget(w http.ResponseWriter, r *http.Request, id string) {
	store, ok := s.budgetStore()
	if !ok {
		budgetError(w, http.StatusInternalServerError, "Budget storage unavailable")
		return
	}
	current, err := store.GetBudget(id)
	if storage.IsNotFoundError(err) {
		budgetError(w, http.StatusNotFound, "Budget not found")
		return
	}
	if err != nil {
		budgetError(w, http.StatusInternalServerError, "Failed to get budget")
		return
	}
	if controlPlaneOwned(w, current) {
		return
	}
	doc, err := decodeBudget(r, w)
	if err != nil {
		budgetError(w, http.StatusBadRequest, err.Error())
		return
	}
	if doc.Metadata.Name != id {
		budgetError(w, http.StatusConflict, "metadata.name cannot change")
		return
	}
	if err := models.ValidateBudgetUpdate(current.Document, *doc); err != nil {
		budgetError(w, http.StatusConflict, err.Error())
		return
	}
	current.Document = *doc
	current.UpdatedBy = budgetActor(r)
	if err := store.UpdateBudget(current); err != nil {
		if storage.IsConflictError(err) {
			budgetError(w, http.StatusConflict, "Budget changed concurrently")
		} else if storage.IsNotFoundError(err) {
			budgetError(w, http.StatusBadRequest, err.Error())
		} else {
			budgetError(w, http.StatusInternalServerError, "Failed to update budget")
		}
		return
	}
	s.publishBudgetChange(r, "UPDATE", current)
	httputil.WriteJSON(w, http.StatusOK, budgetResponse(current))
}

func (s *APIServer) DeleteBudget(w http.ResponseWriter, r *http.Request, id string) {
	store, ok := s.budgetStore()
	if !ok {
		budgetError(w, http.StatusInternalServerError, "Budget storage unavailable")
		return
	}
	current, err := store.GetBudget(id)
	if storage.IsNotFoundError(err) {
		budgetError(w, http.StatusNotFound, "Budget not found")
		return
	}
	if err != nil {
		budgetError(w, http.StatusInternalServerError, "Failed to get budget")
		return
	}
	if controlPlaneOwned(w, current) {
		return
	}
	if err := store.DeleteBudget(id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			budgetError(w, http.StatusNotFound, "Budget not found")
		} else {
			budgetError(w, http.StatusInternalServerError, "Failed to delete budget")
		}
		return
	}
	s.publishBudgetChange(r, "DELETE", current)
	w.WriteHeader(http.StatusNoContent)
}
