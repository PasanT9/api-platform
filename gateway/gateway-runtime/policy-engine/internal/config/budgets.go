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

package config

import (
	"fmt"
	"time"
)

// BudgetsConfig is [policy_engine.budgets].
type BudgetsConfig struct {
	// Redis optionally shares budget spend between gateways. Without it every
	// gateway keeps its own spend in memory, exactly as before.
	Redis BudgetRedisConfig `koanf:"redis"`
}

// BudgetRedisConfig is [policy_engine.budgets.redis]. The connection fields use the
// same names as the rate-limit policies' Redis settings, so one Redis serves both.
type BudgetRedisConfig struct {
	Enabled   bool   `koanf:"enabled"`
	Host      string `koanf:"host"`
	Port      int    `koanf:"port"`
	Username  string `koanf:"username"`
	Password  string `koanf:"password"`
	DB        int    `koanf:"db"`
	KeyPrefix string `koanf:"key_prefix"`
	// Timeout bounds each Redis call. A charge that cannot reach Redis in time is
	// counted locally and sent later, so a slow Redis never holds up a response.
	Timeout time.Duration `koanf:"timeout"`
	// SyncInterval is how often local spend is refreshed from Redis and charges
	// made while Redis was unreachable are sent.
	SyncInterval time.Duration `koanf:"sync_interval"`
	// OnUnavailable is what admission does once Redis has been unreachable for
	// StaleAfter: "serve" keeps admitting from this gateway's own counts, so a
	// limit can be exceeded during an outage; "refuse" refuses requests that a
	// block budget covers with HTTP 503 until Redis is back. Allow budgets and
	// requests no block budget covers are always served.
	OnUnavailable string        `koanf:"on_unavailable"`
	StaleAfter    time.Duration `koanf:"stale_after"`
}

func defaultBudgetsConfig() BudgetsConfig {
	return BudgetsConfig{Redis: BudgetRedisConfig{
		Host:          "localhost",
		Port:          6379,
		KeyPrefix:     "budget:v1:",
		Timeout:       200 * time.Millisecond,
		SyncInterval:  time.Second,
		OnUnavailable: "serve",
		StaleAfter:    5 * time.Second,
	}}
}

func (c BudgetRedisConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Host == "" {
		return fmt.Errorf("policy_engine.budgets.redis.host is required when budget Redis is enabled")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("policy_engine.budgets.redis.port must be between 1 and 65535, got %d", c.Port)
	}
	if c.DB < 0 {
		return fmt.Errorf("policy_engine.budgets.redis.db must not be negative")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("policy_engine.budgets.redis.timeout must be positive")
	}
	if c.SyncInterval <= 0 {
		return fmt.Errorf("policy_engine.budgets.redis.sync_interval must be positive")
	}
	if c.OnUnavailable != "serve" && c.OnUnavailable != "refuse" {
		return fmt.Errorf("policy_engine.budgets.redis.on_unavailable must be \"serve\" or \"refuse\", got %q", c.OnUnavailable)
	}
	if c.OnUnavailable == "refuse" && c.StaleAfter <= 0 {
		return fmt.Errorf("policy_engine.budgets.redis.stale_after must be positive when on_unavailable is \"refuse\"")
	}
	return nil
}
