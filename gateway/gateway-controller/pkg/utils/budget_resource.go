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
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/models"
	"github.com/wso2/api-platform/gateway/gateway-controller/pkg/storage"
)

// BudgetLazyResource is the runtime definition distributed to policy engines.
func BudgetLazyResource(b *models.StoredBudget) *storage.LazyResource {
	doc := b.Document
	if doc.Spec.Warnings != nil {
		warnings := *doc.Spec.Warnings
		warnings.Channels = nil // notification destinations stay in the control plane
		doc.Spec.Warnings = &warnings
	}
	return &storage.LazyResource{
		ID: b.UUID, ResourceType: "Budget",
		Resource: map[string]any{
			"uuid": b.UUID, "handle": b.Handle, "revision": b.Revision,
			"resource_kind": b.ResourceKind,
			"configuration": doc,
		},
	}
}
