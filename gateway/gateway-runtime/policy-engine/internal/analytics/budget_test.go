package analytics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBudgetRequestMetadata(t *testing.T) {
	raw := `[{"budgetUuid":"budget-2","handle":"team-monthly","allowanceKey":"project=demo"},
	         {"budgetUuid":"budget-1","handle":"agent-daily","allowanceKey":"shared"},
	         {"budgetUuid":"budget-3","handle":"org-ceiling","allowanceKey":""}]`
	result := budgetRequestMetadata(raw, "0.01604", "calculated", true)
	require.Equal(t, []string{"agent-daily", "org-ceiling", "team-monthly"}, result["handles"])
	// Shared budgets add no allowance key; per-consumer ones are qualified by handle.
	require.Equal(t, []string{"team-monthly|project=demo"}, result["allowanceKeys"])
	require.Equal(t, "priced", result["costStatus"])
	require.InDelta(t, 0.01604, result["costUsd"], 1e-12)
	require.Len(t, result, 4, "only handles, allowanceKeys, costStatus and costUsd are sent")

	empty := budgetRequestMetadata("", "", "", false)
	require.Equal(t, []string{}, empty["handles"])
	require.Equal(t, []string{}, empty["allowanceKeys"])
	require.Equal(t, float64(0), empty["costUsd"], "always a number, never absent or a string")
	require.Equal(t, float64(0), budgetRequestMetadata("", "abc", "calculated", true)["costUsd"])
}

func TestAddBudgetOutcome(t *testing.T) {
	result := budgetRequestMetadata("", "", "", false)
	addBudgetOutcome(result, `["team-monthly","agent-daily"]`, `["agent-daily|shared|100"]`)
	require.Equal(t, []string{"agent-daily", "team-monthly"}, result["blockedBy"])
	require.Equal(t, []string{"agent-daily|shared|100"}, result["thresholdsCrossed"])

	quiet := budgetRequestMetadata("", "", "", false)
	addBudgetOutcome(quiet, "", "not json")
	_, blocked := quiet["blockedBy"]
	_, crossed := quiet["thresholdsCrossed"]
	require.False(t, blocked, "absent unless a budget refused the request")
	require.False(t, crossed)
}

func TestBudgetRequestMetadataCostStatus(t *testing.T) {
	cases := []struct {
		name, cost, status string
		usage              bool
		want               string
	}{
		{"unpriced model reports zero cost", "0.0000000000", "not_calculated", true, "missing_price"},
		{"failed request without usage", "0.0000000000", "not_calculated", false, "usage_unavailable"},
		{"no llm-cost policy", "", "", true, "missing_price"},
		{"no llm-cost policy and no usage", "", "", false, "usage_unavailable"},
		{"legacy cost without status", "0.002", "", true, "priced"},
		{"zero legacy cost without status", "0", "", true, "missing_price"},
		{"calculated free model", "0.0000000000", "calculated", true, "priced"},
		{"unparsable cost", "abc", "calculated", true, "usage_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, budgetRequestMetadata("", tc.cost, tc.status, tc.usage)["costStatus"])
		})
	}
}
