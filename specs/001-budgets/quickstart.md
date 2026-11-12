# Quickstart: Validating Budgets end to end

Runnable validation scenarios that prove the feature works. Contracts: [budgets-api.openapi.yaml](./contracts/budgets-api.openapi.yaml), [gateway-budget-enforcement.md](./contracts/gateway-budget-enforcement.md), [gateway-definitions.md](./contracts/gateway-definitions.md). Data: [data-model.md](./data-model.md).

**Revised 2026-10-09**: Redis is a prerequisite; spend is verified through response headers, 429s, the Redis counter (`redis-cli`) and Moesif — the Budgets API and UI no longer return spend.

**Revised 2026-10-13**: field names such as `budget_spend` refer to the Moesif event's `metadata` object (`metadata.budget_spend`); the request cost is `metadata.aiMetadata.llmCost`. Usage is checked on the Budget Usage card of the LLM Analytics page (Scenario 15); SC-001 timing is Scenario 14.

## Prerequisites

- Local stack: platform-api + AI Workspace + gateway (`gateway/docker-compose.yaml`) connected to platform-api, **with the `redis` service** from the same compose file (`--appendonly yes --maxmemory-policy noeviction`, password `$REDIS_PASSWORD`).
- Gateway config: `[budgets] enabled = true` and `[policy_configurations.budgets_v1.redis]` pointing at `redis:6379` (shipped `gateway/configs/config.toml`).
- A mock OpenAI-compatible upstream that returns fixed usage (e.g. 1,000 prompt + 1,000 completion tokens) for both buffered and streamed (`stream: true`) chat completions, and a model price such that each call costs **$0.01**.
- One LLM provider `openai-mock` and one LLM proxy `chat-proxy` (project `demo`) deployed, both with `llm-cost` attached (default).
- Moesif configured for the organization's gateways (analytics enabled), for Scenarios 1, 3, 7, 9, 13, 15, with the Budget Usage card, Budget filter and "BUDGET: Threshold crossed" alert rule templates applied (contract §4.2), and the AI Workspace runtime config `APIP_AIW_MOESIF_ANALYTICS_URL` pointing at the embedded analytics.
- An admin token (`ap_admin`) as `$ADMIN`, an operator token as `$OPS`; a JWT issuer for callers with claims `sub` and `team`.
- `export API=https://localhost:9243/api/v0.9`, `export GW=https://localhost:8443`, and `alias rc='docker compose -f gateway/docker-compose.yaml exec redis redis-cli -a $REDIS_PASSWORD --no-auth-warning'`.

## Scenario 0 — Redis is required (FR-022, US1 scenario 5)

1. Remove the `budgets_v1.redis` block, keep `budgets.enabled = true`, restart the gateway-controller. Expected: it does not start; the log names `[policy_configurations.budgets_v1.redis]` as required for budgets.
2. Restore the block but start Redis with `--maxmemory-policy allkeys-lru --appendonly no`. Expected: the gateway starts and logs a WARN that Redis may lose data; `budget_redis_config_unsafe = 1`.
3. Restore the safe Redis settings for the remaining scenarios.

## Scenario 1 — LLM proxy monthly cap (US1, FR-011/013, SC-005)

```bash
curl -sk -X POST $API/budgets -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d '{
  "displayName": "Chat proxy monthly", "context": {"type":"ORGANIZATION"},
  "owners": [{"dimension":"LLM_PROXY","values":["chat-proxy"]}],
  "amount": "0.05", "period": "MONTH", "thresholds": [50, 80, 100] }'
```

Send 6 chat completions through `chat-proxy`.

Expected:
- Calls 1–5: `200`, `Budget-Remaining` decreasing 0.05 → 0.01 (value read at admission, before the call is charged).
- Call 6: `429`, body `code = BUDGET_EXHAUSTED`, `details.budget.id = chat-proxy-monthly`, `Retry-After` = seconds to the 1st of next month 00:00 UTC (±1 s).
- A call directly to `openai-mock` (not via the proxy) is **not** refused.
- `rc GET 'budget:v1:{<budget uuid>}:<YYYY-MM>:_'` = `50000000` (nano-USD); `rc TTL` ≈ seconds to period end + 86400.
- `GET $API/budgets/chat-proxy-monthly` returns the definition only (no spend fields). In Moesif the five calls carry `budget_spend` 0.01 … 0.05 and the refused call `budget_refused = true`.

## Scenario 2 — No redeploy on change (FR-010, SC-002)

Raise the amount to `0.10` with `PUT $API/budgets/chat-proxy-monthly`. Without redeploying anything, within 60 s the next call succeeds; the Redis counter still holds `50000000` before that call (amount change never touches spend); deployment history of `chat-proxy` is unchanged; the audit record for the update has `details.before.amount = "0.05"`, `details.after.amount = "0.10"` (FR-009a, FR-026).

## Scenario 3 — Per-user claim budget (US2, FR-003/004)

Create `{"context":{"type":"ORGANIZATION"},"owners":[{"dimension":"CLAIM","claimName":"sub","each":true}],"amount":"0.02","period":"DAY"}`. Using JWTs for `alice` and `bob`, send 3 calls each. Expected: each user is refused on their own 3rd call; the 429 names only the caller's own value; Redis holds two counters for the budget with hashed value keys (`…:h<32 hex>`), no raw `sub` values in key names; with the Budget filter set to this budget, the Budget Usage card on the LLM Analytics page lists one row per user (`metadata.budget_owner_values`).

Then call with an API key (no claims): the claim budget does not apply (US2 scenario 4); the LLM proxy budget from Scenario 1 still does.

## Scenario 3a — Combined owners (US2 scenarios 5–6, FR-002/003)

In project mode for project `search`, create `{"context":{"type":"PROJECT","project":"search"},"owners":[{"dimension":"CLAIM","claimName":"sub","each":true},{"dimension":"LLM_PROVIDER","each":true}],"amount":"0.02","period":"DAY"}`. As `alice`, send 2 calls through a proxy of `search` to `openai-mock`, then 1 call to `anthropic-mock`. Expected: the 3rd OpenAI call is refused; the Anthropic call is allowed; Redis holds one counter per (provider, user) combination that had traffic (`…:<provider uuid>:h<32 hex>`); a call through a proxy of another project is not counted. Then try to append a `PROJECT` owner in project mode, or a second `LLM_PROVIDER` owner: `400 BUDGET_OWNER_NOT_ALLOWED` / `400 BUDGET_OWNER_DUPLICATE`.

In org mode, create two budgets for `sub = alice` with `LLM_PROVIDER = openai-mock` (`0.02`) and `LLM_PROVIDER = anthropic-mock` (`0.01`). Expected: alice's OpenAI and Anthropic allowances are independent. With `team = search` + `LLM_PROVIDER ∈ {openai-mock, anthropic-mock}` (`0.03`), search members' calls to either provider share one allowance (US2 scenario 9).

## Scenario 3d — Several values on one owner (US1 scenario 6, US2 scenario 9, FR-003)

In org mode create `{"context":{"type":"ORGANIZATION"},"owners":[{"dimension":"LLM_PROVIDER","values":["openai-mock","anthropic-mock"]}],"amount":"0.03","period":"DAY"}`. Send calls to `openai-mock` and `anthropic-mock` alternately, and one to a third provider. Expected: both providers' calls add to one Redis counter (`…:_`) and are refused together once the total reaches `0.03`; the third provider's call is not counted. The response lists `values` sorted, with `displayNames` in the same order. Creating the same budget with the values in the other order → `409 BUDGET_ALREADY_EXISTS`; `"values":[]`, `"values":["openai-mock","openai-mock"]`, or `values` together with `"each":true` → `400 BUDGET_INVALID_OWNER`. Then create `LLM_PROVIDER ∈ {openai-mock}` (`0.01`/DAY): the response lists the two-provider budget in `overrides`; OpenAI calls are now governed only by the `0.01` budget, while Anthropic calls still use the shared `0.03` budget.

## Scenario 3b — Most specific wins (FR-003a)

In project mode for `search`, create `LLM_PROXY = each` (`0.02`/DAY) and `LLM_PROXY = proxy-a` (`0.05`/DAY). Expected: the create response for the second budget lists the first in `overrides` with a `BUDGET_OVERRIDES_OTHER` warning, and a GET of the first lists the second in `overriddenBy`. Calls through `proxy-a` are refused only after `0.05`, the `each` budget's Redis counter for `proxy-a` is never created, and each event carries `budget_overridden_ids`; calls through `proxy-b` are refused after `0.02`. Add an org-mode `LLM_PROXY = each` budget of `0.03`/DAY: it still applies to `proxy-a` (different context, so it stacks) and refuses first. A `WEEK` budget with the same owners also stacks.

## Scenario 3c — Model owner (FR-002, Delivery phase 2)

In org mode create `sub = each` + `MODEL = gpt-4o` (`0.02`/DAY). As `alice`, send `gpt-4o` calls until refused, then a `gpt-4o-mini` call. Expected: the `gpt-4o-mini` call is allowed. Through a proxy that rewrites model `fast` → `gpt-4o-mini`, a `MODEL = gpt-4o-mini` budget counts the call and a `MODEL = fast` budget does not. `MODEL = each` creates one counter per upstream model (`…:m<32 hex>`). A `MODEL` value of empty string or 256 characters → `400 BUDGET_INVALID_OWNER`.

## Scenario 4 — Several budgets on one request (Edge cases, R8)

With Scenario 1 and an org budget with owner `team = search` of `0.03`/DAY: a `team=search` caller is refused by the **team** budget first (same number of owners, smaller amount, so evaluated first) once it is exhausted, even though the proxy budget still has headroom; both budgets' Redis counters increase for every allowed call.

## Scenario 5 — Streaming (FR-016, FR-016a)

Send `stream: true` chat completions **without** `stream_options` through `chat-proxy` under an enforce-mode budget. Expected: the mock upstream receives `stream_options.include_usage = true`; the client receives tokens incrementally (first chunk before the last is produced); the counter increases by $0.01 and the event has `llm_cost_status = calculated`. When the budget runs out mid-stream, that stream completes; the next request is refused.

## Scenario 6 — Monitor mode (US4)

Create a `MONITOR` budget of `0.01`. Calls keep returning `200` after exhaustion with `Budget-Monitor: would-refuse` and `budget_monitor_would_refuse = true` in Moesif; switching to `ENFORCE` refuses the next call immediately (the counter already holds the monitored spend). A streamed call (`stream: true`, no `stream_options`) under the monitor budget alone also reaches the mock upstream with `stream_options.include_usage = true` and is charged $0.01 (FR-016a).

## Scenario 7 — Unpriced traffic (FR-015, FR-024a)

Call a model with no price entry. Expected: `200`, never refused for being unpriced; the Redis counter is unchanged; the Moesif event has `metadata.llm_cost_status = not_calculated` and `metadata.budget_id` set (delivery phase 1), so it is found on the LLM Analytics page by setting the Budget filter and filtering on `llm_cost_status`.

## Scenario 8 — Shared counter, restarts and outages (FR-017, FR-019–FR-022a, SC-003, SC-004)

1. Scale `gateway-runtime` to 2 replicas (and, if available, a second gateway of the same org pointing at the same Redis); drive 20 concurrent calls under a `0.05` budget. Expected: one counter; refusals start on all replicas as soon as it reaches the amount; final overshoot ≤ the cost of calls in flight at that moment (SC-003).
2. Restart one runtime, the controller and platform-api mid-period. Expected: the counter is unchanged (SC-004); enforcement continues.
3. Stop platform-api for 2 minutes: gateways keep enforcing with the last definitions and counting in Redis; budget edits made after restart arrive on reconnect (FR-021).
4. Stop Redis: requests under the enforce-mode budget get `503 BUDGET_CHECK_UNAVAILABLE`; requests under monitor-only budgets pass. Set `failure_mode = "open"` and repeat: requests pass with no `Budget-*` headers and `budget_check_skipped = true` (FR-020).
5. Restart Redis **without** its volume: the counter restarts at zero and requests are allowed again (FR-022a, documented behaviour).

## Scenario 9 — Threshold notifications via Moesif (US3, FR-009, FR-023, SC-007)

With Moesif configured and the org-wide alert rule "BUDGET: Threshold crossed" on `metadata.budget_threshold_crossed` (contract §4.2), push a $0.10 budget with thresholds 50/80 past 50 % and 80 % using 2 runtime replicas. Expected: exactly one event with `budget_threshold_crossed = 50` and one with `= 80` in the period, regardless of replica count, and two alerts. Re-arming: raise the amount to $0.20 (spend $0.09 = 45 %): no event yet; the call that brings spend to $0.10 emits `= 50` again and the call reaching $0.16 emits `= 80` again (new `amountVersion`). Highest-only: now lower the amount to $0.11 (spend $0.16 ≈ 145 %): the next call emits one event with `= 80` only; the 50 % marker is set silently. Redis holds markers `…:thr:<t>:<amountVersion>` for each amount version.

## Scenario 10 — Access control and configuration-only UI (FR-024, FR-027/028, US5)

`$OPS` can `GET` budgets but gets `403` on `POST/PUT/DELETE`. A publisher/viewer gets `403` (whether `ap_viewer` gets `ap:budget:read` is an open product decision, plan risk 10). In the AI Workspace as `$OPS`: the Budgets page lists the budgets with owner names, amount, period, mode, thresholds and status (Active / Monitor / Disabled with reason), no spend or percentage anywhere, no create/edit/delete actions, and each row's "See usage in Insights / Moesif" link opens the LLM Analytics page under `APIP_AIW_MOESIF_ANALYTICS_URL` with the Budget filter set to the budget (falls back to the base URL until the path/filter format is confirmed, plan risk 12).

## Scenario 11 — Owner deleted (edge case, FR-009a)

Delete the LLM proxy `chat-proxy`. Expected: its budget becomes `DISABLED` with `statusReason = OWNER_DELETED`, shown in the UI with an explanation; the audit log has an `UPDATE` by `system` with before/after status; no Moesif action is published by platform-api.

## Scenario 12 — Budget and cost-based rate limit on the same route (FR-029, SC-005)

Attach `llm-cost-based-ratelimit` to `chat-proxy` with a cost limit lower than the budget (e.g. $0.02), and keep the `0.05` monthly budget. Expected: the third call gets `429` with the rate-limit policy's own error code (not `BUDGET_EXHAUSTED`) and no `Budget-*` refusal body. Then raise the rate limit above the budget and exhaust the budget: the refusal is `429` with `code = BUDGET_EXHAUSTED`, `details.budget` and `Retry-After` to the period reset. The two refusals are distinguishable by `code` alone; the existing rate-limit configuration works unchanged.

## Scenario 13 — Reconciliation and alert timing (SC-006, SC-007)

1. **SC-006 reconciliation**: drive a mix of priced and unpriced traffic (≥ 100 calls, 2 runtime replicas) under a budget without "each" owners, then compare the Redis counter (`rc GET …`, ÷ 10⁹) with the sum of `metadata.aiMetadata.llmCost` over events with `metadata.budget_id` = the budget, `metadata.budget_period` = the period and `metadata.llm_cost_status = calculated` in Moesif. Expected: zero unexplained difference (any difference must be explained by `budget_charge_failures_total` or `budget_check_skipped` events); unpriced calls are visible as `llm_cost_status = not_calculated`.
2. **SC-007 timing**: note the time of the call that pushes spend across a threshold (gateway access log) and the arrival of its event (with `budget_threshold_crossed`) and alert in Moesif. Expected: < 2 minutes, exactly one event per threshold.

## Scenario 14 — Create a working budget in under 2 minutes (SC-001)

As `ap_admin` in the AI Workspace, start a timer on the Budgets page, click Create, enter name "Chat proxy daily", owner LLM proxy `chat-proxy` (re-create it first if Scenario 11 deleted it), amount `0.03`, period Day, thresholds 50/80/100, Enforce, and save. Without redeploying any API, provider or gateway, send calls through `chat-proxy` until one is refused. Expected: save completes in **under 2 minutes** from opening the dialog; the 4th call returns `429 BUDGET_EXHAUSTED` within 60 s of saving (SC-002); deployment history of `chat-proxy` and the gateway are unchanged. Record the elapsed time.

## Scenario 15 — Budget Usage card and Budget filter in Moesif (FR-024e)

After Scenarios 1–3 (and Scenario 14), open the LLM Analytics page of the embedded Moesif analytics. Expected: the page is unchanged except for one new **Budget Usage** card and a **Budget** filter next to the API filter (no new page, section or sidebar item); the card lists each budget (e.g. "Chat proxy monthly") with its name, current amount, spend this period equal to its Redis counter ÷ 10⁹, % used and the number of refused calls. Set the Budget filter to the per-user `sub` budget: the card lists one row per user (alice, bob) with their own spend, % used and refusals. Following "See usage" for a budget in the AI Workspace lands on the same view with the filter set (where the deep-link format is supported).
