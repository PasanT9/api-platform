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

	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
)

// withBudgetClock gives each test a fresh spend store and a fixed clock.
func withBudgetClock(t *testing.T, now time.Time) *time.Time {
	t.Helper()
	clock := now
	prevStore, prevNow := budgetSpend, nowUTC
	budgetSpend = newMemorySpendStore()
	nowUTC = func() time.Time { return clock }
	t.Cleanup(func() { budgetSpend, nowUTC = prevStore, prevNow })
	return &clock
}

func dailyBlock(handle string, amount string, thresholds ...int) BudgetMatch {
	return BudgetMatch{BudgetUUID: "uuid-" + handle, Handle: handle, AllowanceKey: "attribute=alice%40wso2.com",
		Amount: amount, Period: "day", Action: "block", Thresholds: thresholds}
}

func TestBudgetPeriod(t *testing.T) {
	at := time.Date(2026, 11, 4, 15, 30, 0, 0, time.UTC) // a Wednesday
	cases := map[string][2]string{
		"day":   {"2026-11-04", "2026-11-05"},
		"week":  {"2026-11-02", "2026-11-09"},
		"month": {"2026-11-01", "2026-12-01"},
	}
	for period, want := range cases {
		start, reset := budgetPeriod(period, at)
		require.Equal(t, want[0], start.Format("2006-01-02"), period)
		require.Equal(t, want[1], reset.Format("2006-01-02"), period)
	}
	// Sunday belongs to the week that started the previous Monday.
	start, _ := budgetPeriod("week", time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC))
	require.Equal(t, "2026-11-02", start.Format("2006-01-02"))
	// December rolls into January.
	_, reset := budgetPeriod("month", time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC))
	require.Equal(t, "2027-01-01", reset.Format("2006-01-02"))
}

func TestMemorySpendStore(t *testing.T) {
	store := newMemorySpendStore()
	before, after := store.Add("k", 5)
	require.Equal(t, int64(0), before)
	require.Equal(t, int64(5), after)
	_, after = store.Add("k", 0)
	require.Equal(t, int64(5), after, "zero cost changes nothing")
	require.Equal(t, int64(5), store.Spent("k"))
	require.Equal(t, int64(0), store.Spent("other"))
}

func TestBudgetAdmissionBlocksOnlyExhaustedBlockBudgets(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	daily := dailyBlock("api-platform-developer-allowance", "1.00")
	allow := daily
	allow.BudgetUUID, allow.Handle, allow.Action = "uuid-allow", "openai-monthly-spend", "allow"
	ec := &PolicyExecutionContext{budgetMatches: []BudgetMatch{daily, allow}}

	require.Empty(t, ec.checkBudgetAdmission(), "nothing spent yet")

	start, _ := budgetPeriod("day", nowUTC())
	budgetSpend.Add(spendKey(daily, start), 999_999_999)
	budgetSpend.Add(spendKey(allow, start), 5_000_000_000)
	require.Empty(t, ec.checkBudgetAdmission(), "below the limit; an allow budget over its limit never refuses")

	budgetSpend.Add(spendKey(daily, start), 1)
	blocks := ec.checkBudgetAdmission()
	require.Len(t, blocks, 1, "reaching the limit exactly blocks")
	require.Equal(t, "api-platform-developer-allowance", blocks[0].Handle)
	require.Equal(t, "2026-11-05T00:00:00Z", blocks[0].ResetsAt)
}

func TestBudgetAdmissionIsPerAllowance(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	alice := dailyBlock("api-platform-developer-allowance", "1.00")
	bob := alice
	bob.AllowanceKey = "attribute=bob%40wso2.com"
	start, _ := budgetPeriod("day", nowUTC())
	budgetSpend.Add(spendKey(alice, start), 1_000_000_000)

	require.Len(t, (&PolicyExecutionContext{budgetMatches: []BudgetMatch{alice}}).checkBudgetAdmission(), 1)
	require.Empty(t, (&PolicyExecutionContext{budgetMatches: []BudgetMatch{bob}}).checkBudgetAdmission(),
		"another developer's allowance is untouched")
}

func TestChargeBudgetsOnceInAdmissionPeriodWithCrossings(t *testing.T) {
	clock := withBudgetClock(t, time.Date(2026, 11, 4, 23, 59, 59, 0, time.UTC))
	m := dailyBlock("rnd-member-daily", "1.00", 50, 80)
	ec := &PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}
	require.Empty(t, ec.checkBudgetAdmission())
	admittedStart, _ := budgetPeriod("day", nowUTC())

	// The response completes after midnight: the cost still belongs to the admission day.
	*clock = clock.Add(2 * time.Second)
	ec.chargeBudgets("0.8500000000")
	ec.chargeBudgets("0.8500000000") // a later response phase must not charge again

	require.Equal(t, int64(850_000_000), budgetSpend.Spent(spendKey(m, admittedStart)))
	nextStart, _ := budgetPeriod("day", nowUTC())
	require.Equal(t, int64(0), budgetSpend.Spent(spendKey(m, nextStart)))
	require.Equal(t, []string{
		"rnd-member-daily|attribute=alice%40wso2.com|50",
		"rnd-member-daily|attribute=alice%40wso2.com|80",
	}, ec.budgetCrossings)
}

func TestChargeBudgetsReportsExhaustionOnce(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	m := dailyBlock("support-agent-daily-cap", "1.00")
	first := &PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}
	first.chargeBudgets("0.6")
	require.Empty(t, first.budgetCrossings)
	second := &PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}
	second.chargeBudgets("0.6")
	require.Equal(t, []string{"support-agent-daily-cap|attribute=alice%40wso2.com|100"}, second.budgetCrossings)
	third := &PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}
	third.chargeBudgets("0.6")
	require.Empty(t, third.budgetCrossings, "already past 100%")
}

func TestChargeBudgetsSkipsUnpricedAndRefusedRequests(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	m := dailyBlock("api-platform-developer-allowance", "1.00")
	start, _ := budgetPeriod("day", nowUTC())

	(&PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}).chargeBudgets("0.0000000000")
	(&PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}).chargeBudgets("")
	refused := &PolicyExecutionContext{budgetMatches: []BudgetMatch{m}, budgetBlocked: []budgetBlock{{Handle: m.Handle}}}
	refused.chargeBudgets("0.5")
	require.Equal(t, int64(0), budgetSpend.Spent(spendKey(m, start)))
}

func TestEnforceBudgetsRefusesWith429(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 4, 22, 0, 0, 0, time.UTC))
	daily := dailyBlock("api-platform-developer-allowance", "1.00")
	monthly := dailyBlock("api-platform-project-monthly", "2.00")
	monthly.Period, monthly.AllowanceKey = "month", ""
	start, _ := budgetPeriod("day", nowUTC())
	monthStart, _ := budgetPeriod("month", nowUTC())
	budgetSpend.Add(spendKey(daily, start), 1_000_000_000)
	budgetSpend.Add(spendKey(monthly, monthStart), 2_000_000_000)

	ec := &PolicyExecutionContext{budgetMatches: []BudgetMatch{daily, monthly}}
	response := ec.enforceBudgets(nil)
	require.NotNil(t, response)
	immediate := response.GetImmediateResponse()
	require.Equal(t, typev3.StatusCode_TooManyRequests, immediate.Status.Code)

	headers := map[string]string{}
	for _, h := range immediate.Headers.SetHeaders {
		headers[h.Header.Key] = string(h.Header.RawValue)
	}
	// Admitted again only when both have reset: the month resets on 1 December,
	// 26 days and 2 hours after 4 November 22:00.
	require.Equal(t, "2253600", headers["retry-after"])

	var body struct {
		Code    string        `json:"code"`
		Budgets []budgetBlock `json:"budgets"`
	}
	require.NoError(t, json.Unmarshal(immediate.Body, &body))
	require.Equal(t, "BUDGET_EXCEEDED", body.Code)
	require.Len(t, body.Budgets, 2)
	require.Equal(t, "api-platform-developer-allowance", body.Budgets[0].Handle)
	require.Equal(t, "2026-11-05T00:00:00Z", body.Budgets[0].ResetsAt)
	require.Equal(t, "2026-12-01T00:00:00Z", body.Budgets[1].ResetsAt)
	require.Len(t, ec.budgetBlocked, 2)
}

func TestEnforceBudgetsAllowsWhenNothingMatches(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	require.Nil(t, (&PolicyExecutionContext{}).enforceBudgets(nil))
}
