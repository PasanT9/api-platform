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

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"cel.dev/cel-go/cel"
)

const BudgetAPIVersion = "gateway.api-platform.wso2.com/v1"

var budgetAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

// NormalizeAndValidateBudget fills defaults and checks the standalone v1 contract.
// The caller must separately resolve named resources and API keys in storage.
func NormalizeAndValidateBudget(b *BudgetDocument) error {
	if b.APIVersion != BudgetAPIVersion || b.Kind != "Budget" {
		return fmt.Errorf("apiVersion must be %q and kind must be Budget", BudgetAPIVersion)
	}
	if b.Metadata.Name == "" || len(b.Metadata.Name) > 255 || strings.IndexFunc(b.Metadata.Name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '}'
	}) >= 0 {
		return fmt.Errorf("metadata.name must be a nonempty handle without whitespace, slash, or braces")
	}
	if len(b.Spec.DisplayName) == 0 || len(b.Spec.DisplayName) > 255 || len(b.Spec.Description) > 1023 {
		return fmt.Errorf("spec.displayName must have 1–255 characters and description at most 1023")
	}
	if b.Spec.Status == "" {
		b.Spec.Status = "active"
	}
	if b.Spec.Status != "active" && b.Spec.Status != "inactive" {
		return fmt.Errorf("spec.status must be active or inactive")
	}
	if b.Spec.Limit.Currency == "" {
		b.Spec.Limit.Currency = "USD"
	}
	if b.Spec.Limit.Currency != "USD" || (b.Spec.Limit.Period != "day" && b.Spec.Limit.Period != "week" && b.Spec.Limit.Period != "month") ||
		(b.Spec.Limit.Action != "block" && b.Spec.Limit.Action != "allow") {
		return fmt.Errorf("spec.limit requires USD, a day/week/month period, and a block/allow action")
	}
	if !budgetAmountPattern.MatchString(b.Spec.Limit.Amount) {
		return fmt.Errorf("spec.limit.amount must be a positive decimal string with at most two places")
	}
	whole, fraction, _ := strings.Cut(b.Spec.Limit.Amount, ".")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	if len(fraction) == 1 {
		fraction += "0"
	}
	if fraction == "" {
		fraction = "00"
	}
	cents, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil || cents <= 0 || cents > 922337203685 {
		return fmt.Errorf("spec.limit.amount is outside the supported range")
	}
	if b.Spec.Warnings != nil {
		seen := map[int]bool{}
		for _, t := range b.Spec.Warnings.Thresholds {
			if t < 1 || t > 1000 || seen[t] {
				return fmt.Errorf("warnings.thresholds must contain distinct whole percentages in 1–1000")
			}
			seen[t] = true
		}
		channelSeen := map[string]bool{}
		for _, channel := range b.Spec.Warnings.Channels {
			if strings.TrimSpace(channel) == "" || channelSeen[channel] {
				return fmt.Errorf("warnings.channels must contain distinct nonempty names")
			}
			channelSeen[channel] = true
		}
	}
	if b.Spec.Resource != nil {
		r := b.Spec.Resource
		if r.Kind != "LlmProvider" && r.Kind != "LlmProxy" {
			return fmt.Errorf("resource.kind must be LlmProvider or LlmProxy")
		}
		if r.Allocation == "" {
			r.Allocation = "shared"
		}
		if r.Allocation != "shared" && r.Allocation != "separate" {
			return fmt.Errorf("resource.allocation must be shared or separate")
		}
		if r.ID != "" && r.Allocation != "shared" {
			return fmt.Errorf("a named resource must use shared allocation")
		}
	}
	seenConsumerTypes := map[string]bool{}
	for i := range b.Spec.Consumers {
		c := &b.Spec.Consumers[i]
		if err := validateBudgetConsumer(c, false); err != nil {
			return fmt.Errorf("consumers[%d]: %w", i, err)
		}
		if c.Type != "attribute" {
			if seenConsumerTypes[c.Type] {
				return fmt.Errorf("consumer type %q appears more than once", c.Type)
			}
			seenConsumerTypes[c.Type] = true
		}
	}
	for i := range b.Spec.Exemptions {
		if err := validateBudgetConsumer(&b.Spec.Exemptions[i], true); err != nil {
			return fmt.Errorf("exemptions[%d]: %w", i, err)
		}
	}
	return nil
}

func validateBudgetConsumer(c *BudgetConsumer, exemption bool) error {
	if c.Type != "project" && c.Type != "apiKey" && c.Type != "attribute" {
		return fmt.Errorf("type must be project, apiKey, or attribute")
	}
	if c.Type == "apiKey" && c.ID != "" && (c.Owner == nil || c.Owner.ID == "") {
		return fmt.Errorf("API key id requires an owning API")
	}
	if c.Owner != nil {
		if c.Type != "apiKey" || c.ID == "" || (c.Owner.Kind != "LlmProvider" && c.Owner.Kind != "LlmProxy") || c.Owner.ID == "" {
			return fmt.Errorf("owner must identify an LLM provider or proxy for an API key")
		}
	}
	if exemption {
		if c.Allocation != "" || c.Key != "" {
			return fmt.Errorf("exemptions cannot set allocation or key")
		}
		if c.Type == "attribute" {
			if c.ID != "" || c.Owner != nil || c.Condition == "" {
				return fmt.Errorf("attribute exemption requires condition and no target")
			}
			return compileBudgetCEL(c.Condition, true)
		}
		if c.ID == "" || c.Condition != "" {
			return fmt.Errorf("named exemption requires id and no condition")
		}
		return nil
	}
	if c.Allocation == "" {
		c.Allocation = "shared"
	}
	if c.Allocation != "shared" && c.Allocation != "separate" {
		return fmt.Errorf("allocation must be shared or separate")
	}
	if c.Type == "attribute" {
		if c.ID != "" || c.Owner != nil {
			return fmt.Errorf("attribute consumer cannot set a target")
		}
		if c.Allocation == "shared" && c.Condition != "" && c.Key == "" {
			return compileBudgetCEL(c.Condition, true)
		}
		if c.Allocation == "separate" && c.Key != "" && c.Condition == "" {
			return compileBudgetCEL(c.Key, false)
		}
		return fmt.Errorf("shared attribute needs condition; separate attribute needs key")
	}
	if c.Condition != "" || c.Key != "" {
		return fmt.Errorf("named consumer cannot set condition or key")
	}
	if c.Allocation == "shared" && c.ID == "" || c.Allocation == "separate" && c.ID != "" {
		return fmt.Errorf("shared consumer needs id; separate consumer cannot set a target")
	}
	return nil
}

func compileBudgetCEL(expression string, boolean bool) error {
	if len(expression) > 2048 {
		return fmt.Errorf("CEL expression is too long")
	}
	env, err := cel.NewEnv(cel.Variable("request", cel.DynType))
	if err != nil {
		return err
	}
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("invalid CEL expression: %w", issues.Err())
	}
	if boolean && ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return fmt.Errorf("CEL condition must return bool")
	}
	if !boolean && ast.OutputType() != cel.StringType && ast.OutputType() != cel.ListType(cel.StringType) && ast.OutputType() != cel.DynType {
		return fmt.Errorf("CEL key must return string or list of strings")
	}
	return nil
}

// ValidateBudgetUpdate enforces the immutable fields from the Budget design.
func ValidateBudgetUpdate(old, next BudgetDocument) error {
	if old.APIVersion != next.APIVersion || old.Kind != next.Kind || old.Metadata.Name != next.Metadata.Name ||
		old.Spec.Limit.Period != next.Spec.Limit.Period || old.Spec.Limit.Currency != next.Spec.Limit.Currency ||
		!reflect.DeepEqual(old.Spec.Resource, next.Spec.Resource) || !reflect.DeepEqual(old.Spec.Consumers, next.Spec.Consumers) {
		return fmt.Errorf("name, period, currency, resource, and consumers cannot change")
	}
	return nil
}
