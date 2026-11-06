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
	"time"

	"github.com/stretchr/testify/require"
	policy "github.com/wso2/api-platform/sdk/core/policy/v1alpha2"
)

func TestBudgetUsageSnapshotShowsCurrentPeriodAllowances(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	budget := testBudget(t, "rnd-member-daily", nil, []runtimeBudgetConsumer{
		{Type: "attribute", Allocation: "separate", Key: `request.Auth.Claims["email"]`},
	}, nil)
	budget.Configuration.Spec.Limit.Amount = "0.02"
	budget.Configuration.Spec.Limit.Period = "day"
	budget.Configuration.Spec.Limit.Action = "block"
	encoded, err := json.Marshal(budget)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(encoded, &data))
	store := policy.GetLazyResourceStoreInstance()
	require.NoError(t, store.StoreResource(&policy.LazyResource{ID: budget.UUID, ResourceType: "Budget", Resource: data}))
	t.Cleanup(func() { require.NoError(t, store.RemoveResourceByIDAndType(budget.UUID, "Budget")) })

	alice := BudgetMatch{BudgetUUID: budget.UUID, AllowanceKey: "attribute=alice%40wso2.com", Period: "day"}
	bob := BudgetMatch{BudgetUUID: budget.UUID, AllowanceKey: "attribute=bob%40wso2.com", Period: "day"}
	start, _ := budgetPeriod("day", nowUTC())
	budgetSpend.Add(spendKey(alice, start), 21_000_000)
	budgetSpend.Add(spendKey(bob, start), 5_000_000)
	// Yesterday's spend is not part of the current period.
	budgetSpend.Add(spendKey(alice, start.AddDate(0, 0, -1)), 99_000_000)

	var usage *BudgetUsage
	for _, u := range BudgetUsageSnapshot() {
		if u.BudgetUUID == budget.UUID {
			u := u
			usage = &u
		}
	}
	require.NotNil(t, usage)
	require.Equal(t, "2026-11-05T00:00:00Z", usage.ResetsAt)
	require.Equal(t, []AllowanceUsage{
		{Allowance: "attribute=alice@wso2.com", SpentUSD: "0.021000000", RemainingUSD: "0.000000000", UsedPercent: "105.0", Exhausted: true},
		{Allowance: "attribute=bob@wso2.com", SpentUSD: "0.005000000", RemainingUSD: "0.015000000", UsedPercent: "25.0", Exhausted: false},
	}, usage.Allowances)
}

func TestReadableAllowance(t *testing.T) {
	require.Equal(t, "shared", readableAllowance(""))
	require.Equal(t, "project=api-platform|attribute=a+b@wso2.com",
		readableAllowance("project=api-platform|attribute=a%2Bb%40wso2.com"))
	require.Equal(t, "apiKey=LlmProxy:support-agent-openai:key+1", readableAllowance("apiKey=LlmProxy:support-agent-openai:key+1"))
}
