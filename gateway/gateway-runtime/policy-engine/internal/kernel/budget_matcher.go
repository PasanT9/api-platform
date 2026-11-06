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

package kernel

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"github.com/wso2/api-platform/gateway/gateway-runtime/policy-engine/internal/constants"
	celcontext "github.com/wso2/api-platform/gateway/gateway-runtime/policy-engine/internal/pkg/cel"
	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

// BudgetMatch is the immutable definition and allowance identity selected for
// one admitted request. Charging and enforcement consume these in later phases.
type BudgetMatch struct {
	BudgetUUID   string `json:"budgetUuid"`
	Handle       string `json:"handle"`
	Revision     int64  `json:"revision"`
	AllowanceKey string `json:"allowanceKey"`
	Amount       string `json:"amount"`
	Period       string `json:"period"`
	Action       string `json:"action"`
	// Thresholds are the warning percentages; used for crossing detection only.
	Thresholds []int `json:"-"`
}

type runtimeBudget struct {
	UUID          string `json:"uuid"`
	Handle        string `json:"handle"`
	Revision      int64  `json:"revision"`
	ResourceKind  string `json:"resource_kind"`
	Configuration struct {
		Spec struct {
			Limit struct {
				Amount string `json:"amount"`
				Period string `json:"period"`
				Action string `json:"action"`
			} `json:"limit"`
			Warnings *struct {
				Thresholds []int `json:"thresholds"`
			} `json:"warnings"`
			Status     string                  `json:"status"`
			Resource   *runtimeBudgetResource  `json:"resource"`
			Consumers  []runtimeBudgetConsumer `json:"consumers"`
			Exemptions []runtimeBudgetConsumer `json:"exemptions"`
		} `json:"spec"`
	} `json:"configuration"`
	conditions map[string]cel.Program
}

type runtimeBudgetResource struct {
	Kind       string `json:"kind"`
	Allocation string `json:"allocation"`
	ID         string `json:"id"`
}

type runtimeBudgetConsumer struct {
	Type       string              `json:"type"`
	Allocation string              `json:"allocation"`
	ID         string              `json:"id"`
	Owner      *runtimeBudgetOwner `json:"owner"`
	Condition  string              `json:"condition"`
	Key        string              `json:"key"`
}

type runtimeBudgetOwner struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type budgetRequest struct {
	APIKind           string
	APIID             string
	APIHandle         string
	ProjectID         string
	ProviderHandle    string
	APIKeyName        string
	APIKeyOwnerKind   string
	APIKeyOwnerHandle string
	Headers           map[string][]string
	Path              string
	Method            string
	Auth              *policy.AuthContext
	Loopback          bool
}

type budgetMatcher struct {
	mu      sync.RWMutex
	version uint64
	budgets []*runtimeBudget
}

var activeBudgetMatcher budgetMatcher

// refreshBudgetMatches replaces the request's selection after every completed
// request-policy phase. Body routing can supersede a header-phase provider choice.
func (ec *PolicyExecutionContext) refreshBudgetMatches() {
	if ec == nil || ec.sharedCtx == nil || ec.requestHeaderCtx == nil {
		return
	}
	if len(activeBudgetMatcher.definitions()) == 0 {
		ec.budgetMatches = nil
		return
	}
	ec.budgetMatches = activeBudgetMatcher.match(ec.budgetRequest())
}

func (ec *PolicyExecutionContext) budgetRequest() budgetRequest {
	selectedProvider := ""
	if value, ok := ec.sharedCtx.Metadata["provider_name"].(string); ok {
		selectedProvider = value
	}
	if namespace := ec.dynamicMetadata[constants.ExtProcFilterName]; namespace != nil {
		if name, ok := namespace[constants.TargetUpstreamNameKey].(string); ok && name != "" {
			// An explicit route whose provider binding is unknown cannot inherit the
			// primary provider's budget.
			selectedProvider = ec.providerHandleByUpstream[name]
		}
	}
	if ec.sharedCtx.APIKind == policy.APIKindLlmProvider {
		if value, ok := ec.sharedCtx.Metadata["api_handle"].(string); ok {
			selectedProvider = value
		}
	}
	var keyName, keyOwnerKind, keyOwnerHandle string
	for auth := ec.sharedCtx.AuthContext; auth != nil; auth = auth.Previous {
		if auth.Authenticated && auth.AuthType == "apikey" && auth.APIKeyName != "" {
			keyName, keyOwnerKind, keyOwnerHandle = auth.APIKeyName, auth.APIKeyOwnerKind, auth.APIKeyOwnerHandle
			break
		}
	}
	apiHandle, _ := ec.sharedCtx.Metadata["api_handle"].(string)
	return budgetRequest{
		APIKind: string(ec.sharedCtx.APIKind), APIID: ec.sharedCtx.APIId,
		APIHandle: apiHandle, ProjectID: ec.sharedCtx.ProjectID, ProviderHandle: selectedProvider,
		APIKeyName: keyName, APIKeyOwnerKind: keyOwnerKind, APIKeyOwnerHandle: keyOwnerHandle,
		Headers: ec.requestHeaderCtx.Headers.GetAll(),
		Path:    ec.requestHeaderCtx.Path, Method: ec.requestHeaderCtx.Method,
		Auth:     firstAuthenticatedContext(ec.sharedCtx.AuthContext),
		Loopback: isBudgetLoopback(ec),
	}
}

func firstAuthenticatedContext(auth *policy.AuthContext) *policy.AuthContext {
	for current := auth; current != nil; current = current.Previous {
		if current.Authenticated {
			return current
		}
	}
	return nil
}

func isBudgetLoopback(ec *PolicyExecutionContext) bool {
	if ec.sharedCtx.APIKind != policy.APIKindLlmProvider {
		return false
	}
	// The LLM proxy stamps "1" on its provider hop (gateway-controller
	// InternalLoopbackHeader); like the analytics system policy, any nonempty value
	// marks the hop. The peer check below keeps external callers from forging it.
	marker := ec.requestHeaderCtx.Headers.Get(analyticsInternalLoopbackKey)
	if len(marker) == 0 || marker[0] == "" {
		return false
	}
	address := ec.directRemoteAddress
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	ip := net.ParseIP(strings.Trim(address, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (m *budgetMatcher) definitions() []*runtimeBudget {
	store := policy.GetLazyResourceStoreInstance()
	version := store.Version()
	m.mu.RLock()
	if m.version == version {
		budgets := m.budgets
		m.mu.RUnlock()
		return budgets
	}
	m.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.version == version {
		return m.budgets
	}
	resources, err := store.GetResourcesByType("Budget")
	if err != nil {
		slog.Error("Unable to read budget definitions", "error", err)
		return m.budgets
	}
	budgets := make([]*runtimeBudget, 0, len(resources))
	for _, resource := range resources {
		encoded, err := json.Marshal(resource.Resource)
		if err != nil {
			slog.Error("Invalid budget definition", "id", resource.ID, "error", err)
			continue
		}
		var budget runtimeBudget
		if err = json.Unmarshal(encoded, &budget); err != nil {
			slog.Error("Invalid budget definition", "id", resource.ID, "error", err)
			continue
		}
		if budget.UUID == "" || budget.Configuration.Spec.Status != "active" {
			continue
		}
		if err = budget.compileConditions(); err != nil {
			slog.Error("Invalid budget conditions", "id", resource.ID, "error", err)
			continue
		}
		budgets = append(budgets, &budget)
	}
	sort.Slice(budgets, func(i, j int) bool { return budgets[i].UUID < budgets[j].UUID })
	m.budgets = budgets
	m.version = version
	return budgets
}

func (b *runtimeBudget) compileConditions() error {
	b.conditions = make(map[string]cel.Program)
	env, err := cel.NewEnv(cel.Variable("request", cel.DynType))
	if err != nil {
		return err
	}
	compile := func(expression string) error {
		if expression == "" || b.conditions[expression] != nil {
			return nil
		}
		ast, issues := env.Compile(expression)
		if issues != nil && issues.Err() != nil {
			return issues.Err()
		}
		program, err := env.Program(ast)
		if err == nil {
			b.conditions[expression] = program
		}
		return err
	}
	for _, consumer := range b.Configuration.Spec.Consumers {
		if err = compile(consumer.Condition); err != nil {
			return err
		}
		if err = compile(consumer.Key); err != nil {
			return err
		}
	}
	for _, exemption := range b.Configuration.Spec.Exemptions {
		if err = compile(exemption.Condition); err != nil {
			return err
		}
	}
	return nil
}

func (m *budgetMatcher) match(request budgetRequest) []BudgetMatch {
	if request.Loopback || (request.APIKind != "LlmProxy" && request.APIKind != "LlmProvider") {
		return nil
	}
	budgets := m.definitions()
	if len(budgets) == 0 {
		return nil
	}
	var matches []BudgetMatch
	activation := request.celActivation()
	for _, b := range budgets {
		keys, ok := b.matchResource(request)
		if !ok {
			continue
		}
		for _, consumer := range b.Configuration.Spec.Consumers {
			values := b.consumerValues(consumer, request, activation)
			if len(values) == 0 {
				ok = false
				break
			}
			keys = combineAllowanceKeys(keys, values)
		}
		if !ok {
			continue
		}
		for _, exemption := range b.Configuration.Spec.Exemptions {
			if len(b.consumerValues(exemption, request, activation)) > 0 {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		var thresholds []int
		if b.Configuration.Spec.Warnings != nil {
			thresholds = b.Configuration.Spec.Warnings.Thresholds
		}
		for _, key := range keys {
			matches = append(matches, BudgetMatch{
				BudgetUUID: b.UUID, Handle: b.Handle, Revision: b.Revision,
				AllowanceKey: key, Amount: b.Configuration.Spec.Limit.Amount,
				Period: b.Configuration.Spec.Limit.Period, Action: b.Configuration.Spec.Limit.Action,
				Thresholds: thresholds,
			})
		}
	}
	return matches
}

func (b *runtimeBudget) matchResource(r budgetRequest) ([]string, bool) {
	resource := b.Configuration.Spec.Resource
	if resource == nil {
		return []string{""}, true
	}
	var id string
	switch resource.Kind {
	case "LlmProxy":
		if r.APIKind == "LlmProxy" {
			id = r.APIHandle
		}
	case "LlmProvider":
		id = r.ProviderHandle
	default:
		return nil, false
	}
	if id == "" || (resource.ID != "" && resource.ID != id) {
		return nil, false
	}
	if resource.Allocation == "separate" {
		return []string{resource.Kind + "=" + id}, true
	}
	return []string{""}, true
}

func (b *runtimeBudget) consumerValues(c runtimeBudgetConsumer, r budgetRequest, activation map[string]any) []string {
	var value string
	switch c.Type {
	case "project":
		value = r.ProjectID
	case "apiKey":
		value = r.APIKeyName
		if value == "" || r.APIKeyOwnerKind == "" || r.APIKeyOwnerHandle == "" {
			return nil
		}
		if c.Owner != nil && (c.Owner.Kind != r.APIKeyOwnerKind || c.Owner.ID != r.APIKeyOwnerHandle) {
			return nil
		}
		value = r.APIKeyOwnerKind + ":" + r.APIKeyOwnerHandle + ":" + value
	case "attribute":
		if c.Condition != "" {
			if b.evalBool(c.Condition, activation) {
				return []string{""}
			}
			return nil
		}
		values := b.evalStrings(c.Key, activation)
		if c.Allocation != "separate" {
			return nil
		}
		out := make([]string, 0, len(values))
		for _, v := range values {
			out = append(out, "attribute="+url.QueryEscape(v))
		}
		return out
	default:
		return nil
	}
	if value == "" || (c.ID != "" && (c.Type != "apiKey" && c.ID != value || c.Type == "apiKey" && c.ID != r.APIKeyName)) {
		return nil
	}
	if c.Allocation == "separate" {
		return []string{c.Type + "=" + value}
	}
	return []string{""}
}

func (b *runtimeBudget) evalBool(expression string, activation map[string]any) bool {
	program := b.conditions[expression]
	if program == nil {
		return false
	}
	result, _, err := program.Eval(activation)
	if err != nil {
		return false
	}
	value, ok := result.Value().(bool)
	return ok && value
}

func (b *runtimeBudget) evalStrings(expression string, activation map[string]any) []string {
	program := b.conditions[expression]
	if program == nil {
		return nil
	}
	result, _, err := program.Eval(activation)
	if err != nil {
		return nil
	}
	var values []string
	switch v := result.Value().(type) {
	case string:
		values = []string{v}
	case []string:
		values = v
	case []any:
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil
			}
			values = append(values, text)
		}
	default:
		return nil
	}
	seen := map[string]bool{}
	var distinct []string
	for _, value := range values {
		if value != "" && !seen[value] {
			distinct = append(distinct, value)
			seen[value] = true
		}
	}
	sort.Strings(distinct)
	return distinct
}

func (r budgetRequest) celActivation() map[string]any {
	return map[string]any{"request": map[string]any{
		"Auth": celcontext.AuthToCEL(r.Auth), "Headers": r.Headers, "Path": r.Path, "Method": r.Method,
		"Metadata": map[string]any{},
	}}
}

func combineAllowanceKeys(prefixes, values []string) []string {
	out := make([]string, 0, len(prefixes)*len(values))
	for _, prefix := range prefixes {
		for _, value := range values {
			if prefix == "" {
				out = append(out, value)
			} else if value == "" {
				out = append(out, prefix)
			} else {
				out = append(out, fmt.Sprintf("%s|%s", prefix, value))
			}
		}
	}
	return out
}
