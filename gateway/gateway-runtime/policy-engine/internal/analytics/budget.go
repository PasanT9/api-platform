package analytics

import (
	"encoding/json"
	"math/big"
	"sort"
)

const (
	costStatusPriced           = "priced"
	costStatusMissingPrice     = "missing_price"
	costStatusUsageUnavailable = "usage_unavailable"

	// Values llm-cost writes to x-llm-cost-status.
	llmCostCalculated    = "calculated"
	llmCostNotCalculated = "not_calculated"

	// sharedAllowance is the allowance key of a shared (not per-consumer) budget.
	sharedAllowance = "shared"
)

type budgetMatchEvent struct {
	BudgetUUID   string `json:"budgetUuid"`
	Handle       string `json:"handle"`
	AllowanceKey string `json:"allowanceKey"`
}

// budgetRequestMetadata builds metadata.budget for one LLM request. It carries only
// what no other analytics field says:
//
//   - handles: the budgets this request counts toward. A string array, so an
//     Elasticsearch-backed tool (Moesif) counts the request once under each budget;
//     spend per budget = sum of the existing cost field grouped by handle.
//   - allowanceKeys: "<handle>|<key>" for budgets allocated per consumer, so spend
//     per consumer can be grouped. Shared budgets are fully described by handles.
//   - costStatus: tells an unpriced model (zero cost, usage present) from a priced
//     request, so budgets are never silently under-counted.
//   - costUsd: the request's cost as a JSON number. aiMetadata.llmCost carries the
//     same value, but an analytics tool fixes a field's type when it first indexes
//     it, and llmCost is already a string field in existing Moesif apps, so it
//     cannot be summed. Spend per budget needs a field that is always numeric.
//
// Project and provider are already in the event; budget settings (limit, period,
// action) belong to budget-state events, not to every request.
func budgetRequestMetadata(rawMatches, rawCost, costStatus string, hasUsage bool) map[string]any {
	nanos, _ := costNanoUSD(rawCost)
	result := map[string]any{
		"handles":       []string{},
		"allowanceKeys": []string{},
		"costStatus":    resolveCostStatus(rawCost, costStatus, hasUsage),
		"costUsd":       float64(nanos) / 1e9,
	}
	var matches []budgetMatchEvent
	if rawMatches == "" || json.Unmarshal([]byte(rawMatches), &matches) != nil {
		return result
	}
	handles := map[string]bool{}
	allowances := map[string]bool{}
	for _, match := range matches {
		if match.BudgetUUID == "" || match.Handle == "" {
			continue
		}
		handles[match.Handle] = true
		if match.AllowanceKey != "" && match.AllowanceKey != sharedAllowance {
			allowances[match.Handle+"|"+match.AllowanceKey] = true
		}
	}
	result["handles"] = sortedKeys(handles)
	result["allowanceKeys"] = sortedKeys(allowances)
	return result
}

// addBudgetOutcome adds what enforcement did, only when it did something:
//
//   - blockedBy: handles of the budgets that refused the request (HTTP 429).
//   - stateUnavailable: true when blockedBy is because shared spend could not be
//     read (HTTP 503), not because the budgets were used up.
//   - thresholdsCrossed: "<handle>|<allowance>|<percent>" for every warning threshold
//     (and 100%, exhausted) this request's cost crossed. Alerting and notifications
//     key on this; it appears on exactly one request per crossing.
func addBudgetOutcome(result map[string]any, rawBlocked, rawCrossings, rawStateUnavailable string) {
	if rawStateUnavailable == "true" {
		result["stateUnavailable"] = true
	}
	for field, raw := range map[string]string{"blockedBy": rawBlocked, "thresholdsCrossed": rawCrossings} {
		if raw == "" {
			continue
		}
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err == nil && len(values) > 0 {
			sort.Strings(values)
			result[field] = values
		}
	}
}

func resolveCostStatus(rawCost, costStatus string, hasUsage bool) string {
	nanos, parsed := int64(0), false
	if rawCost != "" {
		nanos, parsed = costNanoUSD(rawCost)
	}
	switch {
	case costStatus == llmCostCalculated && parsed:
		return costStatusPriced
	case costStatus == "" && parsed && nanos > 0:
		// Without llm-cost's status, a parsable non-zero cost is still a price.
		return costStatusPriced
	case (costStatus == llmCostNotCalculated || costStatus == "") && hasUsage:
		return costStatusMissingPrice
	}
	return costStatusUsageUnavailable
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func costNanoUSD(value string) (int64, bool) {
	cost, ok := new(big.Rat).SetString(value)
	if !ok || cost.Sign() < 0 {
		return 0, false
	}
	nanos := new(big.Rat).Mul(cost, big.NewRat(1_000_000_000, 1))
	whole := new(big.Int).Quo(nanos.Num(), nanos.Denom())
	if !whole.IsInt64() {
		return 0, false
	}
	return whole.Int64(), true
}
