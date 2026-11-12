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
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// testGateway is one gateway's spend store connected to a shared Redis.
type testGateway struct {
	t     *testing.T
	store *redisSpendStore
}

func newTestGateway(t *testing.T, addr string) *testGateway {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1,
		DialTimeout: 200 * time.Millisecond, ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	store := newRedisSpendStore(newMemorySpendStore(), client, "budget:v1:", 200*time.Millisecond)
	gw := &testGateway{t: t, store: store}
	gw.sync()
	return gw
}

// on makes this gateway's store the one the kernel uses while fn runs.
func (g *testGateway) on(fn func()) {
	prev := budgetSpend
	budgetSpend = g.store
	defer func() { budgetSpend = prev }()
	fn()
}

func (g *testGateway) sync() { g.store.syncOnce(context.Background()) }

func (g *testGateway) charge(m BudgetMatch, cost string) []string {
	ec := &PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}
	g.on(func() {
		ec.checkBudgetAdmission()
		ec.chargeBudgets(cost)
	})
	return ec.budgetCrossings
}

func (g *testGateway) refused(m BudgetMatch) bool {
	var blocks []budgetBlock
	g.on(func() {
		blocks = (&PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}).checkBudgetAdmission()
	})
	return len(blocks) > 0
}

func TestRedisSharesSpendBetweenGateways(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a, b := newTestGateway(t, mr.Addr()), newTestGateway(t, mr.Addr())
	m := dailyBlock("support-agent-daily-cap", "1.00", 80)
	start, _ := budgetPeriod("day", nowUTC())
	key := spendKey(m, start)

	require.Empty(t, a.charge(m, "0.6"))
	require.False(t, b.refused(m), "b has not synced yet")

	// The 80% and 100% crossings happen on b, using the shared total.
	require.Equal(t, []string{
		"support-agent-daily-cap|attribute=alice%40wso2.com|80",
		"support-agent-daily-cap|attribute=alice%40wso2.com|100",
	}, b.charge(m, "0.6"))
	require.Equal(t, int64(1_200_000_000), b.store.Spent(key))

	// a learns at its next sync that the allowance is used up.
	require.False(t, a.refused(m))
	a.sync()
	require.True(t, a.refused(m))
	require.Equal(t, int64(1_200_000_000), a.store.Spent(key))

	stored, err := mr.Get("budget:v1:" + key)
	require.NoError(t, err)
	require.Equal(t, "1200000000", stored)
	require.Greater(t, mr.TTL("budget:v1:"+key), 30*24*time.Hour)
}

func TestRedisReportsEachCrossingOnceAcrossGateways(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a, b := newTestGateway(t, mr.Addr()), newTestGateway(t, mr.Addr())
	m := dailyBlock("project-x-daily", "1.00", 50)

	// Neither gateway syncs between charges: their local counters lag, but the
	// crossings still come from the shared total.
	var crossings []string
	for i := 0; i < 5; i++ {
		crossings = append(crossings, a.charge(m, "0.15")...)
		crossings = append(crossings, b.charge(m, "0.15")...)
	}
	require.Equal(t, []string{
		"project-x-daily|attribute=alice%40wso2.com|50",
		"project-x-daily|attribute=alice%40wso2.com|100",
	}, crossings)
}

func TestRedisOutageCountsLocallyAndCatchesUp(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a, b := newTestGateway(t, mr.Addr()), newTestGateway(t, mr.Addr())
	m := dailyBlock("project-x-daily", "1.00", 80)
	start, _ := budgetPeriod("day", nowUTC())
	key := spendKey(m, start)

	require.Empty(t, a.charge(m, "0.3"))

	mr.Close()
	// Each gateway keeps counting and reports crossings from what it sees.
	require.Empty(t, a.charge(m, "0.4"))
	require.Equal(t, int64(700_000_000), a.store.Spent(key))
	require.Empty(t, b.charge(m, "0.2"))
	a.sync()
	b.sync()

	require.NoError(t, mr.Restart())
	a.sync()
	b.sync()
	a.sync() // a pulls b's charge, sent after a's last pull

	stored, err := mr.Get("budget:v1:" + key)
	require.NoError(t, err)
	require.Equal(t, "900000000", stored, "every charge reaches Redis once")
	require.Equal(t, int64(900_000_000), a.store.Spent(key))
	require.Equal(t, int64(900_000_000), b.store.Spent(key))

	// Sending pending charges did not report crossings; the next charge does.
	require.Equal(t, []string{"project-x-daily|attribute=alice%40wso2.com|100"}, b.charge(m, "0.2"))
}

func TestRedisNotReachableAtStartup(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	a := newTestGateway(t, addr)
	m := dailyBlock("project-x-daily", "1.00")
	start, _ := budgetPeriod("day", nowUTC())

	require.Equal(t, []string{"project-x-daily|attribute=alice%40wso2.com|100"}, a.charge(m, "1.5"))
	require.True(t, a.refused(m), "admission works without Redis")

	require.NoError(t, mr.StartAddr(addr))
	a.sync()
	stored, err := mr.Get("budget:v1:" + spendKey(m, start))
	require.NoError(t, err)
	require.Equal(t, "1500000000", stored)
}

func TestRedisChargesEveryAllowanceInOneScript(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a := newTestGateway(t, mr.Addr())
	start, _ := budgetPeriod("day", nowUTC())
	team, user := dailyBlock("rnd-team-daily", "1.00"), dailyBlock("developer-daily", "0.50")
	charges := []spendCharge{
		{Key: spendKey(team, start), Limit: 1_000_000_000},
		{Key: spendKey(user, start), Limit: 500_000_000},
		{Key: "no-limit"},
	}

	a.store.AddAll(100_000_000, charges) // loads the script
	calls := &countingHook{}
	a.store.client.AddHook(calls)
	results := a.store.AddAll(400_000_000, charges)
	require.Equal(t, int64(1), calls.n.Load(), "one Redis call charges every allowance")

	require.Equal(t, []spendResult{
		{Before: 100_000_000, After: 500_000_000, Exhausted: false},
		{Before: 100_000_000, After: 500_000_000, Exhausted: true},
		{Before: 100_000_000, After: 500_000_000, Exhausted: false},
	}, results)
	for _, c := range charges {
		stored, err := mr.Get("budget:v1:" + c.Key)
		require.NoError(t, err)
		require.Equal(t, "500000000", stored)
		require.Greater(t, mr.TTL("budget:v1:"+c.Key), 30*24*time.Hour)
	}
}

func TestRedisScriptChangesNothingWhenOneCounterIsBad(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a := newTestGateway(t, mr.Addr())
	require.NoError(t, mr.Set("budget:v1:broken", "not-a-number"))

	results := a.store.AddAll(300_000_000, []spendCharge{{Key: "good"}, {Key: "broken"}})

	require.False(t, mr.Exists("budget:v1:good"), "the good counter is not charged on its own")
	broken, err := mr.Get("budget:v1:broken")
	require.NoError(t, err)
	require.Equal(t, "not-a-number", broken)
	// The gateway still counts the request locally.
	require.Equal(t, int64(300_000_000), results[0].After)
	require.Equal(t, int64(300_000_000), a.store.Spent("good"))
}

func TestChargeBudgetsUsesOneStepForEveryMatch(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a := newTestGateway(t, mr.Addr())
	team, user := dailyBlock("rnd-team-daily", "1.00", 50), dailyBlock("developer-daily", "0.50")
	start, _ := budgetPeriod("day", nowUTC())

	var crossings []string
	a.on(func() {
		ec := &PolicyExecutionContext{budgetMatches: []BudgetMatch{team, user}}
		ec.chargeBudgets("0.6")
		crossings = ec.budgetCrossings
	})
	require.Equal(t, []string{
		"rnd-team-daily|attribute=alice%40wso2.com|50",
		"developer-daily|attribute=alice%40wso2.com|100",
	}, crossings)
	for _, m := range []BudgetMatch{team, user} {
		stored, err := mr.Get("budget:v1:" + spendKey(m, start))
		require.NoError(t, err)
		require.Equal(t, "600000000", stored)
	}
}

func TestRefuseWhenSpendIsStale(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a := newTestGateway(t, mr.Addr())
	mr.Close()
	block := dailyBlock("project-x-daily", "1.00")
	allow := dailyBlock("project-x-plan", "5.00")
	allow.Action = "allow"

	prev := budgetRefuseWhenStale
	t.Cleanup(func() { budgetRefuseWhenStale = prev })

	enforce := func(matches ...BudgetMatch) (*PolicyExecutionContext, int32) {
		ec := &PolicyExecutionContext{budgetMatches: matches}
		var status int32
		a.on(func() {
			if response := ec.enforceBudgets(nil); response != nil {
				status = int32(response.GetImmediateResponse().GetStatus().GetCode())
			}
		})
		return ec, status
	}

	// Redis has just become unreachable: the sync notices, but it is not stale yet.
	a.sync()
	require.Greater(t, a.store.unavailableFor(), time.Duration(0))
	budgetRefuseWhenStale = 5 * time.Second
	_, status := enforce(block)
	require.Zero(t, status)

	// Unreachable for longer than stale_after.
	a.store.downSince.Store(time.Now().Add(-10 * time.Second).UnixNano())
	ec, status := enforce(block, allow)
	require.Equal(t, int32(503), status)
	require.True(t, ec.budgetStateUnavailable)
	require.Len(t, ec.budgetBlocked, 1)
	require.Equal(t, "project-x-daily", ec.budgetBlocked[0].Handle)

	// Allow budgets are always served.
	_, status = enforce(allow)
	require.Zero(t, status)

	// "serve" keeps admitting from local counts.
	budgetRefuseWhenStale = 0
	_, status = enforce(block)
	require.Zero(t, status)

	// Once Redis is back, admission is normal again.
	budgetRefuseWhenStale = 5 * time.Second
	require.NoError(t, mr.Restart())
	require.Eventually(t, func() bool { a.sync(); return a.store.unavailableFor() == 0 },
		3*time.Second, 50*time.Millisecond)
	_, status = enforce(block)
	require.Zero(t, status)
}

// countingHook counts the commands a Redis client sends.
type countingHook struct{ n atomic.Int64 }

func (h *countingHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *countingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error { h.n.Add(1); return next(ctx, cmd) }
}
func (h *countingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { h.n.Add(int64(len(cmds))); return next(ctx, cmds) }
}

func TestRedisNewGatewayReadsSharedSpendOnFirstCheck(t *testing.T) {
	withBudgetClock(t, time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	a, b := newTestGateway(t, mr.Addr()), newTestGateway(t, mr.Addr())
	m := dailyBlock("support-agent-daily-cap", "1.00")

	a.charge(m, "1.2")
	// b has never checked this allowance and has not synced: it must still refuse.
	require.True(t, b.refused(m))

	// With Redis down, a gateway new to an allowance falls back to its own count.
	c := newTestGateway(t, mr.Addr())
	mr.Close()
	require.False(t, c.refused(m))
	require.Greater(t, c.store.unavailableFor(), time.Duration(0))
}
