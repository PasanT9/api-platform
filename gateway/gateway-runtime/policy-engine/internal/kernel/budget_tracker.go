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
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"

	"github.com/wso2/api-platform/gateway/gateway-runtime/policy-engine/internal/analytics"
)

// Spend tracking keeps each allowance's spend for the current period in this
// process's memory. It is exact for a single policy-engine instance; replicas keep
// separate counters, and a restart starts every period from zero. Both are accepted
// for this version. The spendStore interface is where a shared backend (Redis, or
// control-plane aggregation) plugs in later without touching matching or admission.

// spendStore holds spend in nano-USD per allowance-period key.
type spendStore interface {
	Spent(key string) int64
	// Add adds nanos and returns the spend before and after the addition.
	Add(key string, nanos int64) (before, after int64)
	// WithPrefix returns every key that starts with prefix and its spend.
	WithPrefix(prefix string) map[string]int64
}

type memorySpendStore struct {
	mu     sync.Mutex
	spends map[string]int64
}

func newMemorySpendStore() *memorySpendStore {
	return &memorySpendStore{spends: make(map[string]int64)}
}

func (s *memorySpendStore) Spent(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spends[key]
}

func (s *memorySpendStore) Add(key string, nanos int64) (int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.spends[key]
	after := before
	if nanos > 0 {
		if before > math.MaxInt64-nanos {
			after = math.MaxInt64
		} else {
			after = before + nanos
		}
		s.spends[key] = after
	}
	return before, after
}

func (s *memorySpendStore) WithPrefix(prefix string) map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64)
	for key, spent := range s.spends {
		if strings.HasPrefix(key, prefix) {
			out[key] = spent
		}
	}
	return out
}

var budgetSpend spendStore = newMemorySpendStore()

// nowUTC is replaceable in tests.
var nowUTC = func() time.Time { return time.Now().UTC() }

// budgetPeriod returns the UTC start of the period containing t and the start of
// the next one (when the allowance resets). Weeks start on Monday (ISO 8601).
func budgetPeriod(period string, t time.Time) (start, reset time.Time) {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "week":
		offset := (int(day.Weekday()) + 6) % 7 // Monday = 0
		start = day.AddDate(0, 0, -offset)
		return start, start.AddDate(0, 0, 7)
	case "month":
		start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	default: // day
		return day, day.AddDate(0, 0, 1)
	}
}

// spendKey identifies one allowance in one period. The budget UUID (not the
// revision) is used so that raising or lowering the limit keeps the spend.
func spendKey(m BudgetMatch, periodStart time.Time) string {
	return m.BudgetUUID + "/" + periodStart.Format("2006-01-02") + "/" + m.AllowanceKey
}

// amountNanos converts a decimal USD amount ("5.00") to nano-USD.
func amountNanos(amount string) (int64, bool) {
	value, ok := new(big.Rat).SetString(amount)
	if !ok || value.Sign() < 0 {
		return 0, false
	}
	nanos := new(big.Rat).Mul(value, big.NewRat(1_000_000_000, 1))
	whole := new(big.Int).Quo(nanos.Num(), nanos.Denom())
	if !whole.IsInt64() {
		return 0, false
	}
	return whole.Int64(), true
}

// budgetBlock describes one budget that refused a request.
type budgetBlock struct {
	Handle   string `json:"budget"`
	Limit    string `json:"limit"`
	Currency string `json:"currency"`
	Period   string `json:"period"`
	ResetsAt string `json:"resetsAt"`
	reset    time.Time
}

// checkBudgetAdmission records the admission period and returns every block
// budget whose allowance is already used up. Allow budgets never refuse.
func (ec *PolicyExecutionContext) checkBudgetAdmission() []budgetBlock {
	if ec == nil || len(ec.budgetMatches) == 0 {
		return nil
	}
	now := nowUTC()
	ec.budgetAdmittedAt = now
	var blocks []budgetBlock
	seen := map[string]bool{}
	for _, m := range ec.budgetMatches {
		if m.Action != "block" {
			continue
		}
		limit, ok := amountNanos(m.Amount)
		if !ok {
			continue
		}
		start, reset := budgetPeriod(m.Period, now)
		if budgetSpend.Spent(spendKey(m, start)) < limit || seen[m.BudgetUUID] {
			continue
		}
		seen[m.BudgetUUID] = true
		blocks = append(blocks, budgetBlock{
			Handle: m.Handle, Limit: m.Amount, Currency: "USD", Period: m.Period,
			ResetsAt: reset.Format(time.RFC3339), reset: reset,
		})
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Handle < blocks[j].Handle })
	return blocks
}

// enforceBudgets runs admission at the request's final request phase. It returns
// an immediate 429 response naming every blocking budget and when each resets,
// or nil when the request may proceed. phaseAnalytics is the analytics the
// current phase's policies produced, kept on the refusal so the request stays
// visible in traffic analytics.
func (ec *PolicyExecutionContext) enforceBudgets(phaseAnalytics map[string]any) *extprocv3.ProcessingResponse {
	blocks := ec.checkBudgetAdmission()
	if len(blocks) == 0 {
		return nil
	}
	ec.budgetBlocked = blocks
	// The request is admitted only when every blocking budget has reset.
	latest := blocks[0].reset
	for _, b := range blocks[1:] {
		if b.reset.After(latest) {
			latest = b.reset
		}
	}
	retryAfter := int64(math.Ceil(latest.Sub(nowUTC()).Seconds()))
	if retryAfter < 1 {
		retryAfter = 1
	}
	body, _ := json.Marshal(map[string]any{
		"code":    "BUDGET_EXCEEDED",
		"message": "This request exceeds one or more budgets. It can be retried after every listed budget resets.",
		"budgets": blocks,
	})
	slog.Info("Request refused by budget", "budgets", len(blocks), "retry_after_seconds", retryAfter)

	response := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extprocv3.ImmediateResponse{
				Status: &typev3.HttpStatus{Code: typev3.StatusCode_TooManyRequests},
				Headers: &extprocv3.HeaderMutation{SetHeaders: []*corev3.HeaderValueOption{
					{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("application/json")}},
					{Header: &corev3.HeaderValue{Key: "retry-after", RawValue: []byte(strconv.FormatInt(retryAfter, 10))}},
				}},
				Body: body,
			},
		},
	}
	analyticsData := copyAnalytics(ec.analyticsMetadata)
	for key, value := range phaseAnalytics {
		analyticsData[key] = value
	}
	// The collector system policy records the caller only in response phases, which a
	// refused request never reaches. Keep the refusal attributed to the caller.
	if _, set := analyticsData[analytics.UserIDMetadataKey]; !set && ec.sharedCtx != nil {
		if auth := firstAuthenticatedContext(ec.sharedCtx.AuthContext); auth != nil && auth.Subject != "" {
			analyticsData[analytics.UserIDMetadataKey] = auth.Subject
		}
	}
	if analyticsStruct, err := buildAnalyticsStruct(analyticsData, ec); err == nil {
		response.DynamicMetadata = buildDynamicMetadata(analyticsStruct, nil, nil)
	} else {
		slog.Error("Unable to attach analytics to a budget refusal", "error", err)
	}
	return response
}

func copyAnalytics(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// requestBodyPhaseFollows reports whether Envoy will deliver a buffered request-body
// callback after the request-headers response, so admission waits for the final
// match there. Streamed request bodies are admitted at the header phase.
func (ec *PolicyExecutionContext) requestBodyPhaseFollows() bool {
	return ec.policyChain != nil && ec.policyChain.RequiresRequestBody &&
		!ec.requestHasNoBody() && !ec.isStreamingRequest
}

// chargeBudgets adds the request's cost to every matched allowance, once, in the
// period in which the request was admitted (BR-9), and records every warning
// threshold (and 100%) the request crossed. A request without a price adds
// nothing; analytics flags it as missing_price.
func (ec *PolicyExecutionContext) chargeBudgets(rawCost string) {
	if ec == nil || ec.budgetCharged || len(ec.budgetMatches) == 0 || len(ec.budgetBlocked) > 0 {
		return
	}
	ec.budgetCharged = true
	nanos, ok := amountNanos(rawCost)
	if !ok || nanos == 0 {
		return
	}
	admitted := ec.budgetAdmittedAt
	if admitted.IsZero() {
		admitted = nowUTC()
	}
	for _, m := range ec.budgetMatches {
		start, _ := budgetPeriod(m.Period, admitted)
		before, after := budgetSpend.Add(spendKey(m, start), nanos)
		limit, ok := amountNanos(m.Amount)
		if !ok || limit == 0 {
			continue
		}
		for _, threshold := range crossingPoints(m.Thresholds) {
			mark := limitShare(limit, threshold)
			if before < mark && after >= mark {
				ec.budgetCrossings = append(ec.budgetCrossings, fmt.Sprintf("%s|%s|%d", m.Handle, allowanceOrShared(m.AllowanceKey), threshold))
				slog.Info("Budget threshold crossed", "budget", m.Handle, "allowance", allowanceOrShared(m.AllowanceKey),
					"threshold_percent", threshold, "period_start", start.Format("2006-01-02"))
			}
		}
	}
}

// crossingPoints is the budget's warning thresholds plus 100% (exhausted).
func crossingPoints(thresholds []int) []int {
	points := map[int]bool{100: true}
	for _, t := range thresholds {
		if t > 0 {
			points[t] = true
		}
	}
	out := make([]int, 0, len(points))
	for t := range points {
		out = append(out, t)
	}
	sort.Ints(out)
	return out
}

func limitShare(limit int64, percent int) int64 {
	share := new(big.Int).Mul(big.NewInt(limit), big.NewInt(int64(percent)))
	share.Quo(share, big.NewInt(100))
	if !share.IsInt64() {
		return math.MaxInt64
	}
	return share.Int64()
}

func allowanceOrShared(key string) string {
	if key == "" {
		return "shared"
	}
	return key
}
