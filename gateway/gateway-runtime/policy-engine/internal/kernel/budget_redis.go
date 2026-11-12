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
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Budget spend shared through Redis.
//
// Redis only keeps the gateways' local counters in step; it changes nothing in
// matching, admission or charging. Every gateway still admits from its local
// counter. With Redis reachable, each charge is added to the shared counter as
// well, and the shared total comes back into the local counter. A background sync
// pulls the shared totals of every allowance this gateway has checked, so an
// allowance used up on one gateway is refused on the others within one sync
// interval.
//
// While Redis is reachable, threshold crossings come from the shared counter, so
// each crossing is reported once across all gateways. While it is not, the gateway
// counts locally, reports crossings from its local counter, and keeps the charges
// to send once Redis is back. Sending them does not report crossings again. A
// warning can therefore be sent twice around an outage; that is accepted.

// budgetSpendTTL keeps an allowance's shared counter beyond the longest period
// (a month), after which the key is no longer used.
const budgetSpendTTL = 40 * 24 * time.Hour

// budgetWatchIdle is how long an allowance stays in the sync after it was last
// checked or charged on this gateway.
const budgetWatchIdle = 10 * time.Minute

// budgetAddScript charges several allowances in one atomic step. KEYS are the
// allowance counters; ARGV[1] is the counter TTL in seconds, then one (nano-USD
// amount, limit) pair per key, with limit 0 meaning no limit. It returns
// {total, exhausted} per key, the total as a string so that it stays exact.
//
// Redis runs a script without interleaving other commands, but does not roll
// back writes made before an error. Every counter is therefore checked before any
// is written: if one cannot be added to, the script fails and changes nothing.
// Totals and limits are compared as decimal strings, not Lua numbers, which lose
// precision above 2^53 nano-USD (about $9 million).
var budgetAddScript = redis.NewScript(`
local function atLeast(a, b)
  if #a ~= #b then
    return #a > #b
  end
  return a >= b
end
for i, key in ipairs(KEYS) do
  local current = redis.call('GET', key)
  if current and not tonumber(current) then
    return redis.error_reply('budget counter is not a number: ' .. key)
  end
end
local out = {}
for i, key in ipairs(KEYS) do
  redis.call('INCRBY', key, ARGV[2 * i])
  if redis.call('TTL', key) < 0 then
    redis.call('EXPIRE', key, ARGV[1])
  end
  local total = redis.call('GET', key)
  local limit = ARGV[2 * i + 1]
  local exhausted = 0
  if limit ~= '0' and atLeast(total, limit) then
    exhausted = 1
  end
  out[#out + 1] = total
  out[#out + 1] = exhausted
end
return out
`)

// BudgetRedisOptions connects budget spend tracking to Redis.
type BudgetRedisOptions struct {
	Addr         string
	Username     string
	Password     string
	DB           int
	KeyPrefix    string
	Timeout      time.Duration
	SyncInterval time.Duration
	// OnUnavailable is what admission does once Redis has been unreachable for
	// StaleAfter: BudgetServeWhenUnavailable keeps admitting from local counts;
	// BudgetRefuseWhenUnavailable refuses requests a block budget covers (503).
	OnUnavailable string
	StaleAfter    time.Duration
}

// What admission does while shared budget spend is unavailable.
const (
	BudgetServeWhenUnavailable  = "serve"
	BudgetRefuseWhenUnavailable = "refuse"
)

// EnableBudgetRedis shares budget spend between gateways through Redis until ctx
// ends. It does not wait for Redis: until Redis answers, spend is counted locally
// and sent once it does.
func EnableBudgetRedis(ctx context.Context, opts BudgetRedisOptions) {
	client := redis.NewClient(&redis.Options{
		Addr:         opts.Addr,
		Username:     opts.Username,
		Password:     opts.Password,
		DB:           opts.DB,
		DialTimeout:  opts.Timeout,
		ReadTimeout:  opts.Timeout,
		WriteTimeout: opts.Timeout,
		// A charge that fails is kept and sent by the sync, so no retries here.
		MaxRetries: -1,
	})
	store := newRedisSpendStore(newMemorySpendStore(), client, opts.KeyPrefix, opts.Timeout)
	budgetSpend = store
	budgetRefuseWhenStale = 0
	if opts.OnUnavailable == BudgetRefuseWhenUnavailable {
		budgetRefuseWhenStale = opts.StaleAfter
	}
	slog.Info("Budget spend will be shared through Redis", "addr", opts.Addr, "sync_interval", opts.SyncInterval,
		"on_unavailable", opts.OnUnavailable, "stale_after", opts.StaleAfter)
	go store.run(ctx, opts.SyncInterval)
}

type redisSpendStore struct {
	local   *memorySpendStore
	client  *redis.Client
	prefix  string
	timeout time.Duration
	healthy atomic.Bool
	// downSince is when Redis was last found unreachable (UnixNano), or 0 while it
	// is reachable. A new store counts as unreachable until its first sync.
	downSince atomic.Int64

	mu      sync.Mutex
	pending map[string]int64     // charges not yet added to Redis
	watched map[string]time.Time // allowances to keep in step, with last use
}

func newRedisSpendStore(local *memorySpendStore, client *redis.Client, prefix string, timeout time.Duration) *redisSpendStore {
	s := &redisSpendStore{
		local: local, client: client, prefix: prefix, timeout: timeout,
		pending: make(map[string]int64),
		watched: make(map[string]time.Time),
	}
	s.downSince.Store(time.Now().UnixNano())
	return s
}

// unavailableFor is how long Redis has been unreachable, or 0 while it is reachable.
func (s *redisSpendStore) unavailableFor() time.Duration {
	since := s.downSince.Load()
	if since == 0 {
		return 0
	}
	// The wall clock can read the same instant just after Redis went down; never
	// report zero (reachable) while it is down.
	return max(time.Since(time.Unix(0, since)), time.Nanosecond)
}

// watch keeps key in the sync and reports whether it was already being kept in
// step (false the first time this gateway sees it, or after it went idle).
func (s *redisSpendStore) watch(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, known := s.watched[key]
	s.watched[key] = time.Now()
	return known
}

// Spent is what admission checks. The first time this gateway checks an
// allowance (or after it went idle and left the sync), its local counter knows
// nothing of other gateways' spend, so the shared total is read from Redis first;
// otherwise a gateway new to an allowance would admit requests however long ago
// the allowance was used up elsewhere. If Redis cannot answer, the local counter
// is used, as at any other time.
func (s *redisSpendStore) Spent(key string) int64 {
	if !s.watch(key) && s.healthy.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		text, err := s.client.Get(ctx, s.prefix+key).Result()
		cancel()
		switch {
		case err == nil:
			if shared, perr := strconv.ParseInt(text, 10, 64); perr == nil {
				s.mu.Lock()
				s.local.raiseTo(key, satAdd(shared, s.pending[key]))
				s.mu.Unlock()
			}
		case errors.Is(err, redis.Nil):
			// Nothing spent anywhere yet.
		case !isRedisReply(err):
			s.markDown(err)
		}
	}
	return s.local.Spent(key)
}

func (s *redisSpendStore) WithPrefix(prefix string) map[string]int64 {
	return s.local.WithPrefix(prefix)
}

func (s *redisSpendStore) Add(key string, nanos int64) (int64, int64) {
	r := s.AddAll(nanos, []spendCharge{{Key: key}})[0]
	return r.Before, r.After
}

// AddAll charges every allowance in one Redis script, so they are all charged
// or none is. If Redis cannot be reached, all of them are counted locally and
// kept to send later.
func (s *redisSpendStore) AddAll(nanos int64, charges []spendCharge) []spendResult {
	if nanos <= 0 || len(charges) == 0 {
		return s.local.AddAll(0, charges)
	}
	for _, c := range charges {
		s.watch(c.Key)
	}
	if s.healthy.Load() {
		amounts := make([]int64, len(charges))
		for i := range amounts {
			amounts[i] = nanos
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		shared, err := s.addShared(ctx, charges, amounts)
		cancel()
		if err == nil {
			s.mu.Lock()
			for i, c := range charges {
				s.local.raiseTo(c.Key, satAdd(shared[i].After, s.pending[c.Key]))
				shared[i].Before = shared[i].After - nanos
			}
			s.mu.Unlock()
			return shared
		}
		if isRedisReply(err) {
			// Redis answered but refused (for example a counter that is not a
			// number); count locally without retrying.
			slog.Warn("Redis refused a budget charge; counted on this gateway only", "error", err)
			return s.local.AddAll(nanos, charges)
		}
		s.markDown(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	results := s.local.AddAll(nanos, charges)
	for _, c := range charges {
		s.pending[c.Key] = satAdd(s.pending[c.Key], nanos)
	}
	return results
}

// addShared runs budgetAddScript: amounts[i] is added to charges[i].Key.
func (s *redisSpendStore) addShared(ctx context.Context, charges []spendCharge, amounts []int64) ([]spendResult, error) {
	keys := make([]string, len(charges))
	args := make([]any, 0, 1+2*len(charges))
	args = append(args, int64(budgetSpendTTL/time.Second))
	for i, c := range charges {
		keys[i] = s.prefix + c.Key
		args = append(args, amounts[i], c.Limit)
	}
	reply, err := budgetAddScript.Run(ctx, s.client, keys, args...).Slice()
	if err != nil {
		return nil, err
	}
	if len(reply) != 2*len(charges) {
		return nil, fmt.Errorf("budget script returned %d values for %d allowances", len(reply), len(charges))
	}
	results := make([]spendResult, len(charges))
	for i := range charges {
		text, _ := reply[2*i].(string)
		total, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("budget script returned a total that is not a number: %q", text)
		}
		exhausted, _ := reply[2*i+1].(int64)
		results[i] = spendResult{After: total, Exhausted: exhausted == 1}
	}
	return results, nil
}

func (s *redisSpendStore) markDown(err error) {
	if s.healthy.Swap(false) {
		s.downSince.Store(time.Now().UnixNano())
		slog.Warn("Redis unreachable; budget spend is counted on this gateway and sent when Redis is back", "error", err)
	}
}

func (s *redisSpendStore) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer s.client.Close()
	for {
		s.syncOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// syncOnce reconnects if needed, sends pending charges, and refreshes the local
// counters of every watched allowance from Redis.
func (s *redisSpendStore) syncOnce(ctx context.Context) {
	if !s.healthy.Load() {
		pingCtx, cancel := context.WithTimeout(ctx, s.timeout)
		err := s.client.Ping(pingCtx).Err()
		cancel()
		if err != nil {
			return
		}
		s.healthy.Store(true)
		s.downSince.Store(0)
		slog.Info("Redis reachable; budget spend is shared")
	}
	if !s.flushPending(ctx) {
		return
	}
	s.pull(ctx)
}

func (s *redisSpendStore) flushPending(ctx context.Context) bool {
	s.mu.Lock()
	charges := make([]spendCharge, 0, len(s.pending))
	amounts := make([]int64, 0, len(s.pending))
	for key, nanos := range s.pending {
		charges = append(charges, spendCharge{Key: key})
		amounts = append(amounts, nanos)
	}
	s.mu.Unlock()

	const chunk = 500
	for start := 0; start < len(charges); start += chunk {
		end := min(start+chunk, len(charges))
		callCtx, cancel := context.WithTimeout(ctx, s.timeout)
		_, err := s.addShared(callCtx, charges[start:end], amounts[start:end])
		cancel()
		if err != nil && !isRedisReply(err) {
			s.markDown(err)
			return false
		}
		if err != nil {
			slog.Warn("Redis refused pending budget spend; kept on this gateway only", "error", err)
		}
		s.mu.Lock()
		for i := start; i < end; i++ {
			key := charges[i].Key
			if s.pending[key] -= amounts[i]; s.pending[key] <= 0 {
				delete(s.pending, key)
			}
		}
		s.mu.Unlock()
	}
	return true
}

func (s *redisSpendStore) pull(ctx context.Context) {
	cutoff := time.Now().Add(-budgetWatchIdle)
	s.mu.Lock()
	keys := make([]string, 0, len(s.watched))
	for key, last := range s.watched {
		if last.Before(cutoff) {
			delete(s.watched, key)
			continue
		}
		keys = append(keys, key)
	}
	s.mu.Unlock()

	if len(keys) == 0 {
		// Nothing to pull: still check Redis, so an outage is noticed (and
		// on_unavailable applies) even on a gateway with no recent budget traffic.
		pingCtx, cancel := context.WithTimeout(ctx, s.timeout)
		err := s.client.Ping(pingCtx).Err()
		cancel()
		if err != nil {
			s.markDown(err)
		}
		return
	}

	const chunk = 500
	for start := 0; start < len(keys); start += chunk {
		end := min(start+chunk, len(keys))
		redisKeys := make([]string, 0, end-start)
		for _, key := range keys[start:end] {
			redisKeys = append(redisKeys, s.prefix+key)
		}
		callCtx, cancel := context.WithTimeout(ctx, s.timeout)
		values, err := s.client.MGet(callCtx, redisKeys...).Result()
		cancel()
		if err != nil {
			s.markDown(err)
			return
		}
		s.mu.Lock()
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				continue
			}
			shared, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				continue
			}
			key := keys[start+i]
			s.local.raiseTo(key, satAdd(shared, s.pending[key]))
		}
		s.mu.Unlock()
	}
}

// isRedisReply reports whether err is an error reply from Redis itself, as
// opposed to Redis not being reachable.
func isRedisReply(err error) bool {
	var reply redis.Error
	return errors.As(err, &reply)
}

func satAdd(a, b int64) int64 {
	if b > 0 && a > (1<<63-1)-b {
		return 1<<63 - 1
	}
	return a + b
}
