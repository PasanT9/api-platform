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
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"time"
)

// BudgetUsage is this policy engine's view of one active budget in its current
// period. Spend is the in-memory counter of this instance only.
type BudgetUsage struct {
	Budget      string           `json:"budget"`
	BudgetUUID  string           `json:"budgetUuid"`
	Revision    int64            `json:"revision"`
	Limit       string           `json:"limit"`
	Currency    string           `json:"currency"`
	Period      string           `json:"period"`
	Action      string           `json:"action"`
	PeriodStart string           `json:"periodStart"`
	ResetsAt    string           `json:"resetsAt"`
	Allowances  []AllowanceUsage `json:"allowances"`
}

// AllowanceUsage is the spend of one allowance (the shared pool, or one member of
// a separate allocation) in the current period.
type AllowanceUsage struct {
	// Allowance is the readable allowance identity ("shared" for a shared pool).
	Allowance    string `json:"allowance"`
	SpentUSD     string `json:"spentUsd"`
	RemainingUSD string `json:"remainingUsd"`
	UsedPercent  string `json:"usedPercent"`
	Exhausted    bool   `json:"exhausted"`
}

// BudgetUsageSnapshot lists every active budget and the allowances that have
// spent in the current period. Allowances that have not spent yet are not listed;
// a budget with none has an empty list.
func BudgetUsageSnapshot() []BudgetUsage {
	now := nowUTC()
	budgets := activeBudgetMatcher.definitions()
	out := make([]BudgetUsage, 0, len(budgets))
	for _, b := range budgets {
		limit := b.Configuration.Spec.Limit
		start, reset := budgetPeriod(limit.Period, now)
		usage := BudgetUsage{
			Budget: b.Handle, BudgetUUID: b.UUID, Revision: b.Revision,
			Limit: limit.Amount, Currency: "USD", Period: limit.Period, Action: limit.Action,
			PeriodStart: start.Format(time.RFC3339), ResetsAt: reset.Format(time.RFC3339),
			Allowances: []AllowanceUsage{},
		}
		limitNanos, _ := amountNanos(limit.Amount)
		prefix := b.UUID + "/" + start.Format("2006-01-02") + "/"
		for key, spent := range budgetSpend.WithPrefix(prefix) {
			remaining := limitNanos - spent
			if remaining < 0 {
				remaining = 0
			}
			percent := "0.0"
			if limitNanos > 0 {
				percent = new(big.Rat).SetFrac64(spent*100, limitNanos).FloatString(1)
			}
			usage.Allowances = append(usage.Allowances, AllowanceUsage{
				Allowance:    readableAllowance(strings.TrimPrefix(key, prefix)),
				SpentUSD:     nanosToUSD(spent),
				RemainingUSD: nanosToUSD(remaining),
				UsedPercent:  percent,
				Exhausted:    limitNanos > 0 && spent >= limitNanos,
			})
		}
		sort.Slice(usage.Allowances, func(i, j int) bool {
			return usage.Allowances[i].Allowance < usage.Allowances[j].Allowance
		})
		out = append(out, usage)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Budget < out[j].Budget })
	return out
}

// readableAllowance turns an internal allowance key into the form people read,
// for example "attribute=alice%40wso2.com" into "attribute=alice@wso2.com".
func readableAllowance(key string) string {
	if key == "" {
		return "shared"
	}
	parts := strings.Split(key, "|")
	for i, part := range parts {
		if value, ok := strings.CutPrefix(part, "attribute="); ok {
			if decoded, err := url.QueryUnescape(value); err == nil {
				parts[i] = "attribute=" + decoded
			}
		}
	}
	return strings.Join(parts, "|")
}

func nanosToUSD(nanos int64) string {
	return fmt.Sprintf("%d.%09d", nanos/1_000_000_000, nanos%1_000_000_000)
}
