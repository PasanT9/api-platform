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
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/api-platform/gateway/gateway-runtime/policy-engine/internal/constants"
	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

func testBudget(t *testing.T, id string, resource *runtimeBudgetResource, consumers, exemptions []runtimeBudgetConsumer) *runtimeBudget {
	t.Helper()
	b := &runtimeBudget{UUID: id, Handle: id, Revision: 1}
	b.Configuration.Spec.Status = "active"
	b.Configuration.Spec.Limit.Amount = "20.00"
	b.Configuration.Spec.Limit.Period = "day"
	b.Configuration.Spec.Limit.Action = "block"
	b.Configuration.Spec.Resource = resource
	b.Configuration.Spec.Consumers = consumers
	b.Configuration.Spec.Exemptions = exemptions
	require.NoError(t, b.compileConditions())
	return b
}

func testMatcher(budgets ...*runtimeBudget) *budgetMatcher {
	return &budgetMatcher{version: policy.GetLazyResourceStoreInstance().Version(), budgets: budgets}
}

func TestBudgetMatcherResourceOverlap(t *testing.T) {
	proxy := testBudget(t, "proxy-budget", &runtimeBudgetResource{Kind: "LlmProxy", ID: "proxy-handle"}, nil, nil)
	provider := testBudget(t, "provider-budget", &runtimeBudgetResource{Kind: "LlmProvider", ID: "provider-handle"}, nil, nil)
	org := testBudget(t, "org-budget", nil, nil, nil)
	m := testMatcher(proxy, provider, org)
	r := budgetRequest{APIKind: "LlmProxy", APIHandle: "proxy-handle", ProviderHandle: "provider-handle"}
	matches := m.match(r)
	require.Len(t, matches, 3)
	require.Equal(t, []string{"proxy-budget", "provider-budget", "org-budget"}, []string{matches[0].Handle, matches[1].Handle, matches[2].Handle})

	r.ProviderHandle = "provider-a"
	matches = m.match(r)
	require.Len(t, matches, 2, "proxy budget is independent of its underlying provider")
	require.Equal(t, "proxy-budget", matches[0].Handle)

	r.APIKind = "LlmProvider"
	r.APIHandle = "provider-handle"
	r.ProviderHandle = "provider-handle"
	matches = m.match(r)
	require.Len(t, matches, 2, "direct provider call matches provider and broad budgets")

	r.Loopback = true
	require.Empty(t, m.match(r), "internal provider hop must not count twice")
}

func TestBudgetMatcherConsumersAndExemptions(t *testing.T) {
	b := testBudget(t, "project-user", nil, []runtimeBudgetConsumer{
		{Type: "project", ID: "project-1"},
		{Type: "attribute", Allocation: "separate", Key: `request.Auth.Claims["email"]`},
	}, []runtimeBudgetConsumer{{Type: "attribute", Condition: `"finance" in request.Auth.Claims["groups"]`}})
	m := testMatcher(b)
	r := budgetRequest{
		APIKind: "LlmProxy", APIID: "proxy", ProjectID: "project-1",
		Auth: &policy.AuthContext{Authenticated: true, TypedProperties: map[string]any{
			"email": "someone@example.com", "groups": []string{"engineering"},
		}},
	}
	matches := m.match(r)
	require.Len(t, matches, 1)
	require.Equal(t, "attribute=someone%40example.com", matches[0].AllowanceKey)
	r.Auth.TypedProperties["groups"] = []string{"finance"}
	require.Empty(t, m.match(r), "matching exemption removes only this budget")
	r.Auth.TypedProperties["groups"] = []string{"engineering"}
	delete(r.Auth.TypedProperties, "email")
	require.Empty(t, m.match(r), "missing identity does not match")
	r.ProjectID = "other-project"
	require.Empty(t, m.match(r))
}

func TestBudgetMatcherMultiValueAndAPIKey(t *testing.T) {
	b := testBudget(t, "per-team-key", nil, []runtimeBudgetConsumer{
		{Type: "apiKey", ID: "key-name", Owner: &runtimeBudgetOwner{Kind: "LlmProvider", ID: "provider"}},
		{Type: "attribute", Allocation: "separate", Key: `request.Auth.Claims["groups"]`},
	}, nil)
	m := testMatcher(b)
	r := budgetRequest{
		APIKind: "LlmProvider", APIHandle: "provider", APIKeyName: "key-name", APIKeyOwnerKind: "LlmProvider", APIKeyOwnerHandle: "provider",
		Auth: &policy.AuthContext{Authenticated: true, TypedProperties: map[string]any{
			"groups": []string{"AI", "R&D", "AI"},
		}},
	}
	matches := m.match(r)
	require.Len(t, matches, 2)
	require.Equal(t, "attribute=AI", matches[0].AllowanceKey)
	require.Equal(t, "attribute=R%26D", matches[1].AllowanceKey)
	r.APIKeyName = "another-key"
	require.Empty(t, m.match(r))
	r.APIKeyName = "key-name"
	r.APIKeyOwnerHandle = "another-provider"
	require.Empty(t, m.match(r), "same key name under another owner must not match")
}

func TestBudgetLoopbackRequiresDirectLoopbackPeer(t *testing.T) {
	ec := &PolicyExecutionContext{
		sharedCtx: &policy.SharedContext{APIKind: policy.APIKindLlmProvider},
		requestHeaderCtx: &policy.RequestHeaderContext{Headers: policy.NewHeaders(map[string][]string{
			analyticsInternalLoopbackKey: {"true"},
		})},
	}
	ec.directRemoteAddress = "203.0.113.1:1234"
	require.False(t, isBudgetLoopback(ec), "caller can forge the marker")
	ec.directRemoteAddress = "127.0.0.1:1234"
	require.True(t, isBudgetLoopback(ec))
	ec.directRemoteAddress = ""
	require.False(t, isBudgetLoopback(ec), "missing peer must not suppress accounting")

	// The value the LLM proxy actually sends on its provider hop.
	ec.requestHeaderCtx = &policy.RequestHeaderContext{Headers: policy.NewHeaders(map[string][]string{
		analyticsInternalLoopbackKey: {"1"},
	})}
	ec.directRemoteAddress = "127.0.0.1:41000"
	require.True(t, isBudgetLoopback(ec), "the proxy's provider hop is not a second budgeted request")
	ec.requestHeaderCtx = &policy.RequestHeaderContext{Headers: policy.NewHeaders(map[string][]string{
		analyticsInternalLoopbackKey: {""},
	})}
	require.False(t, isBudgetLoopback(ec), "an empty marker is not a loopback")
}

func TestBudgetRequestUsesSelectedProviderAndValidatedKey(t *testing.T) {
	ec := &PolicyExecutionContext{
		sharedCtx: &policy.SharedContext{
			APIKind: policy.APIKindLlmProxy, APIId: "proxy-uuid", ProjectID: "project-uuid",
			AuthContext: &policy.AuthContext{Previous: &policy.AuthContext{
				Authenticated: true, APIKeyName: "key-name", APIKeyOwnerKind: "LlmProxy", APIKeyOwnerHandle: "proxy", AuthType: "apikey",
			}},
		},
		requestHeaderCtx:         &policy.RequestHeaderContext{Headers: policy.NewHeaders(nil)},
		providerHandleByUpstream: map[string]string{"alternate": "alternate"},
		dynamicMetadata: map[string]map[string]interface{}{
			constants.ExtProcFilterName: {constants.TargetUpstreamNameKey: "alternate"},
		},
	}
	r := ec.budgetRequest()
	require.Equal(t, "alternate", r.ProviderHandle)
	require.Equal(t, "key-name", r.APIKeyName)
	require.Equal(t, "apikey", r.Auth.AuthType)
	ec.dynamicMetadata[constants.ExtProcFilterName][constants.TargetUpstreamNameKey] = "unknown"
	require.Empty(t, ec.budgetRequest().ProviderHandle, "unknown explicit target must not use primary provider")
	delete(ec.dynamicMetadata[constants.ExtProcFilterName], constants.TargetUpstreamNameKey)
	ec.sharedCtx.Metadata = map[string]interface{}{"provider_name": "primary"}
	require.Equal(t, "primary", ec.budgetRequest().ProviderHandle)
	ec.sharedCtx.AuthContext = &policy.AuthContext{Authenticated: true, AuthType: "jwt", APIKeyID: "forged-key-id"}
	require.Empty(t, ec.budgetRequest().APIKeyName, "non-API-key contexts cannot supply the key identity")
}

func TestBudgetMatcherLoadsDistributedDefinition(t *testing.T) {
	budget := testBudget(t, "distributed-budget", &runtimeBudgetResource{Kind: "LlmProxy", Allocation: "shared", ID: "my-proxy"}, nil, nil)
	encoded, err := json.Marshal(budget)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(encoded, &data))
	store := policy.GetLazyResourceStoreInstance()
	require.NoError(t, store.StoreResource(&policy.LazyResource{
		ID: budget.UUID, ResourceType: "Budget", Resource: data,
	}))
	t.Cleanup(func() { require.NoError(t, store.RemoveResourceByIDAndType(budget.UUID, "Budget")) })
	m := &budgetMatcher{}
	require.Len(t, m.match(budgetRequest{APIKind: "LlmProxy", APIHandle: "my-proxy"}), 1)
	require.Empty(t, m.match(budgetRequest{APIKind: "LlmProxy", APIHandle: "another-proxy"}))
}
