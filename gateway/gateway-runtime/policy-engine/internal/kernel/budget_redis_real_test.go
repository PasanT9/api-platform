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

// These tests run budget spend against a real Redis or Valkey, to check what an
// in-process fake cannot: the real Lua engine. They are skipped unless
// BUDGET_TEST_REDIS_ADDR is set, for example:
//
//	docker run -d --name budget-test-valkey -p 127.0.0.1:16379:6379 valkey/valkey:8.1-alpine
//	BUDGET_TEST_REDIS_ADDR=127.0.0.1:16379 go test ./internal/kernel/ -run Real
//
// Every test uses its own key prefix and deletes its keys afterwards.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func realRedis(t *testing.T) (string, *redis.Client, string) {
	t.Helper()
	addr := os.Getenv("BUDGET_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set BUDGET_TEST_REDIS_ADDR to run against a real Redis or Valkey")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, client.Ping(context.Background()).Err())
	prefix := fmt.Sprintf("budget-test:%s:%d:", t.Name(), time.Now().UnixNano())
	t.Cleanup(func() {
		ctx := context.Background()
		keys, _ := client.Keys(ctx, prefix+"*").Result()
		if len(keys) > 0 {
			client.Del(ctx, keys...)
		}
		_ = client.Close()
	})
	return addr, client, prefix
}

func realGateway(t *testing.T, addr, prefix string) *redisSpendStore {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, PoolSize: 50,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	t.Cleanup(func() { _ = client.Close() })
	store := newRedisSpendStore(newMemorySpendStore(), client, prefix, time.Second)
	store.syncOnce(context.Background())
	require.Zero(t, store.unavailableFor())
	return store
}

func TestRealRedisChargesEveryAllowanceAtomically(t *testing.T) {
	addr, client, prefix := realRedis(t)
	gw := realGateway(t, addr, prefix)
	ctx := context.Background()
	charges := []spendCharge{{Key: "team", Limit: 1_000_000_000}, {Key: "user", Limit: 500_000_000}, {Key: "nolimit"}}

	results := gw.AddAll(500_000_000, charges)
	require.Equal(t, []spendResult{
		{Before: 0, After: 500_000_000, Exhausted: false},
		{Before: 0, After: 500_000_000, Exhausted: true},
		{Before: 0, After: 500_000_000, Exhausted: false},
	}, results)
	for _, c := range charges {
		v, err := client.Get(ctx, prefix+c.Key).Result()
		require.NoError(t, err)
		require.Equal(t, "500000000", v)
		ttl, err := client.TTL(ctx, prefix+c.Key).Result()
		require.NoError(t, err)
		require.Greater(t, ttl, 39*24*time.Hour)
	}
	// A second charge keeps the TTL and adds exactly.
	results = gw.AddAll(1, charges[:1])
	require.Equal(t, int64(500_000_001), results[0].After)
}

func TestRealRedisBadCounterChangesNothing(t *testing.T) {
	addr, client, prefix := realRedis(t)
	gw := realGateway(t, addr, prefix)
	ctx := context.Background()
	require.NoError(t, client.Set(ctx, prefix+"broken", "abc", 0).Err())
	require.NoError(t, client.Set(ctx, prefix+"good", "100", 0).Err())

	results := gw.AddAll(50, []spendCharge{{Key: "good"}, {Key: "broken"}})

	v, _ := client.Get(ctx, prefix+"good").Result()
	require.Equal(t, "100", v, "no partial write on the real engine")
	require.Equal(t, int64(50), results[0].After, "counted locally instead")
	require.Zero(t, gw.unavailableFor(), "an error reply does not mark Redis down")
}

func TestRealRedisExactAtLargeAmounts(t *testing.T) {
	addr, _, prefix := realRedis(t)
	gw := realGateway(t, addr, prefix)
	// $10 million limit: beyond 2^53 nano-USD, where Lua numbers lose precision.
	const limit = int64(10_000_000_000_000_000)
	results := gw.AddAll(limit-1, []spendCharge{{Key: "big", Limit: limit}})
	require.Equal(t, limit-1, results[0].After, "the total stays exact")
	require.False(t, results[0].Exhausted, "one nano-USD below the limit is not exhausted")
	results = gw.AddAll(1, []spendCharge{{Key: "big", Limit: limit}})
	require.Equal(t, limit, results[0].After)
	require.True(t, results[0].Exhausted)
}

func TestRealRedisConcurrentGatewaysAddUpExactly(t *testing.T) {
	addr, client, prefix := realRedis(t)
	a, b := realGateway(t, addr, prefix), realGateway(t, addr, prefix)
	const perGateway, workers = 200, 10
	charges := []spendCharge{{Key: "shared"}, {Key: "per-user-alice"}}

	var wg sync.WaitGroup
	for _, gw := range []*redisSpendStore{a, b} {
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(gw *redisSpendStore) {
				defer wg.Done()
				for i := 0; i < perGateway/workers; i++ {
					gw.AddAll(1_950_000, charges) // $0.00195
				}
			}(gw)
		}
	}
	wg.Wait()

	want := strconv.FormatInt(2*perGateway*1_950_000, 10)
	for _, c := range charges {
		v, err := client.Get(context.Background(), prefix+c.Key).Result()
		require.NoError(t, err)
		require.Equal(t, want, v, c.Key)
	}
	require.Empty(t, a.pending)
	require.Empty(t, b.pending)
	// After a sync both gateways see the shared total.
	a.syncOnce(context.Background())
	require.Equal(t, int64(2*perGateway*1_950_000), a.Spent("shared"))
}

func TestRealRedisCrossingsReportedOnceUnderConcurrency(t *testing.T) {
	addr, _, prefix := realRedis(t)
	gws := []*redisSpendStore{realGateway(t, addr, prefix), realGateway(t, addr, prefix), realGateway(t, addr, prefix)}
	withBudgetClock(t, time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	m := dailyBlock("concurrency-daily", "1.00", 25, 50, 75)

	var mu sync.Mutex
	var crossings []string
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ { // 60 x $0.02 = $1.20 across three gateways
		wg.Add(1)
		go func(gw *redisSpendStore) {
			defer wg.Done()
			ec := &PolicyExecutionContext{budgetMatches: []BudgetMatch{m}}
			ec.chargeBudgetsWith(gw, "0.02")
			mu.Lock()
			crossings = append(crossings, ec.budgetCrossings...)
			mu.Unlock()
		}(gws[i%3])
	}
	wg.Wait()
	require.ElementsMatch(t, []string{
		"concurrency-daily|attribute=alice%40wso2.com|25",
		"concurrency-daily|attribute=alice%40wso2.com|50",
		"concurrency-daily|attribute=alice%40wso2.com|75",
		"concurrency-daily|attribute=alice%40wso2.com|100",
	}, crossings, "each threshold once across all gateways")
}
