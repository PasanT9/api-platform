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

package models

import "time"

// BudgetDocument is the gateway's declarative budget configuration.
type BudgetDocument struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Kind       string         `json:"kind" yaml:"kind"`
	Metadata   BudgetMetadata `json:"metadata" yaml:"metadata"`
	Spec       BudgetSpec     `json:"spec" yaml:"spec"`
}

type BudgetMetadata struct {
	Name string `json:"name" yaml:"name"`
}

type BudgetSpec struct {
	DisplayName string           `json:"displayName" yaml:"displayName"`
	Description string           `json:"description,omitempty" yaml:"description,omitempty"`
	Status      string           `json:"status,omitempty" yaml:"status,omitempty"`
	Limit       BudgetLimit      `json:"limit" yaml:"limit"`
	Warnings    *BudgetWarnings  `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Resource    *BudgetResource  `json:"resource,omitempty" yaml:"resource,omitempty"`
	Consumers   []BudgetConsumer `json:"consumers,omitempty" yaml:"consumers,omitempty"`
	Exemptions  []BudgetConsumer `json:"exemptions,omitempty" yaml:"exemptions,omitempty"`
}

type BudgetLimit struct {
	Amount   string `json:"amount" yaml:"amount"`
	Currency string `json:"currency,omitempty" yaml:"currency,omitempty"`
	Period   string `json:"period" yaml:"period"`
	Action   string `json:"action" yaml:"action"`
}

type BudgetWarnings struct {
	Thresholds []int    `json:"thresholds" yaml:"thresholds"`
	Channels   []string `json:"channels,omitempty" yaml:"channels,omitempty"`
}

type BudgetResource struct {
	Kind       string `json:"kind" yaml:"kind"`
	Allocation string `json:"allocation,omitempty" yaml:"allocation,omitempty"`
	ID         string `json:"id,omitempty" yaml:"id,omitempty"`
}

type BudgetConsumer struct {
	Type       string          `json:"type" yaml:"type"`
	Allocation string          `json:"allocation,omitempty" yaml:"allocation,omitempty"`
	ID         string          `json:"id,omitempty" yaml:"id,omitempty"`
	Owner      *BudgetKeyOwner `json:"owner,omitempty" yaml:"owner,omitempty"`
	Condition  string          `json:"condition,omitempty" yaml:"condition,omitempty"`
	Key        string          `json:"key,omitempty" yaml:"key,omitempty"`
}

// BudgetKeyOwner identifies the API that owns a named key.
type BudgetKeyOwner struct {
	Kind string `json:"kind" yaml:"kind"`
	ID   string `json:"id" yaml:"id"`
}

type StoredBudget struct {
	UUID         string
	GatewayID    string
	Handle       string
	Name         string
	Version      string
	Revision     int64
	DataVersion  string
	Status       string
	Origin       Origin
	ResourceKind string
	ResourceUUID string
	Document     BudgetDocument
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedBy    string
	UpdatedAt    time.Time
}
