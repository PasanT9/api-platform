# **Test Scenarios**

# 1. Coverage Summary

All coverage below is planned; the feature is at design stage and no tests exist yet. Requirement ids refer to the Budgets - Specification document.

| Area | Unit | Integration | Manual | Notes |
| :---- | :---- | :---- | :---- | :---- |
| Budgets REST API (CRUD, validation, context and owners, `overrides` / `overriddenBy` and override warnings, definitions only) | Yes | Yes | No | platform-api unit tests for validation and service rules; integration tests for the endpoints. |
| Audit details and owner-deleted handling | Yes | Yes | No | platform-api. |
| Read-only mode in the service / repository layer | Yes | No | No | Calls the service directly, bypassing handlers. |
| Schema (`budgets`, `audit.details` upgrade) | Yes | No | No | Parity, idempotency and upgrade tests per dialect. |
| Definitions sync (platform-api → gateway-controller → policy engine) | Yes | Yes | No | Controller unit tests and gateway integration tests with a mock platform-api. |
| Activation check (budgets enabled without Redis) and unsafe-Redis warning | Yes | Yes | Yes | Controller config tests; gateway integration test via compose. |
| Budget policy: matching, overrides (most specific wins), model owners, Redis counters, 429 / 503 / headers, unpriced, loopback skip | Yes | Yes | No | miniredis unit tests; gateway integration tests (`gateway/it`, godog). |
| Streaming and `include_usage` injection | Yes | Yes | No | Streaming LLM integration feature. |
| Shared counter across replicas, restarts, outages, Redis failure modes | Yes | Yes | Yes | Two-replica and Redis-down features; data-loss run. |
| Threshold markers and analytics fields | Yes | Yes | Yes | miniredis race tests; analytics steps in the gateway integration tests; Moesif needed for alert delivery. |
| Analytics pipeline change (fields reach Moesif `metadata.*`) | Yes | No | Yes | policy-engine analytics and Moesif publisher tests; manual check in Moesif. |
| Moesif templates (Budget Usage card, Budget filter, alert rule) | No | No | Yes | Needs a Moesif application. |
| Policy-engine phase ordering | Yes | No | No | Pins the ordering the budget policy relies on. |
| Coexistence with `llm-cost-based-ratelimit` | No | Yes | Yes | |
| AI Workspace Budgets screens (configuration only) | Yes | No | Yes | Component tests and manual run-through against the mockups. |
| Upgrade and mixed gateway versions | No | No | Yes | |
| Performance (SC-008) | Yes | No | Yes | Matcher benchmark and load run; not measured yet. |

# 2. Preconditions and Test Data

* **Environment**: Local stack of platform-api, AI Workspace and gateway (`gateway/docker-compose.yaml`) connected to platform-api, **with the `redis` service** from the same compose file (`--appendonly yes --maxmemory-policy noeviction`, password `$REDIS_PASSWORD`). Gateway config: `[budgets] enabled = true` and `[policy_configurations.budgets_v1.redis]` pointing at `redis:6379` (shipped `gateway/configs/config.toml`). A mock OpenAI-compatible upstream returns fixed usage (1,000 prompt + 1,000 completion tokens) for buffered and streamed (`stream: true`) chat completions, and a model price makes each call cost **$0.01**. For Moesif scenarios: analytics enabled on the gateways, the Budget Usage card, Budget filter and "BUDGET: Threshold crossed" alert rule templates applied, and the AI Workspace runtime setting `APIP_AIW_MOESIF_ANALYTICS_URL` pointing at the embedded analytics.
* **Seed data**: Organization A with LLM provider `openai-mock` and LLM proxy `chat-proxy` (project `demo`), both deployed with `llm-cost` attached (the default). Organization B with at least one project, LLM provider, LLM proxy, application and budget, for tenant-isolation checks. A JWT issuer for callers with claims `sub` and `team` (users `alice`, `bob`; team `search`), an application with an API key, and a model with no price entry.
* **Credentials/roles**: `$ADMIN` is an `ap_admin` token (`ap:budget:manage`). `$OPS` is an `ap_operator` token (`ap:budget:read`). `$NOSCOPE` holds neither budget scope. Gateway callers use JWTs with `sub` / `team` or an application API key. Variables: `API=https://localhost:9243/api/v0.9`, `GW=https://localhost:8443`, and `rc` = `redis-cli` against the compose Redis.

# 3. Functional Scenarios

| ID | Requirement | Scenario | Steps | Expected result |
| :---- | :---- | :---- | :---- | :---- |
| TS-1 | FR-001, FR-024d | Create a budget with every field | 1. `POST $API/budgets` as `$ADMIN` with display name, description, context, owners, amount `"1000.00"`, `MONTH`, `ENFORCE`, thresholds `[50, 80, 100]`. 2. `GET` it. | 201 with `Location`. The budget carries every field, `currency = USD`, `status = ACTIVE`, and **no spend fields**. |
| TS-2 | FR-002, FR-011, FR-013, FR-018, SC-005 | LLM proxy monthly cap | 1. Create `{"context":{"type":"ORGANIZATION"},"owners":[{"dimension":"LLM_PROXY","values":["chat-proxy"]}]}`, amount `0.05`, `MONTH`, thresholds 50/80/100. 2. Send 6 chat completions through `chat-proxy`. 3. Call `openai-mock` directly. 4. Read the Redis counter. | Calls 1–5: 200, `Budget-Remaining` 0.05 → 0.01 (read at admission). Call 6: 429 `BUDGET_EXHAUSTED`, `details.budget.id = chat-proxy-monthly`, `Retry-After` = seconds to the 1st of next month 00:00 UTC (±1 s). The direct provider call is **not** refused. `rc GET 'budget:v1:{<uuid>}:<YYYY-MM>:_'` = `50000000`; TTL ≈ seconds to period end + 86400. |
| TS-3 | FR-002, FR-016c | LLM provider budget covers proxy traffic exactly once | 1. Create an org budget with owner `LLM_PROVIDER = openai-mock`. 2. Send calls through `chat-proxy` (upstream `openai-mock`) and directly. | Both kinds of traffic are charged to the provider budget; each proxy call is charged once (the inner loopback hop is skipped). |
| TS-4 | FR-003 | Each project, including later projects | 1. Create `{"context":{"type":"ORGANIZATION"},"owners":[{"dimension":"PROJECT","each":true}]}`, `0.02`, `DAY`. 2. Traffic through proxies of two projects. 3. Create a new project with a proxy and send traffic. | Each project is refused independently after its own $0.02; the new project gets its own allowance without administrator action. |
| TS-5 | FR-003, FR-004 | Per-user claim budget | 1. Create `{"context":{"type":"ORGANIZATION"},"owners":[{"dimension":"CLAIM","claimName":"sub","each":true}]}`, `0.02`, `DAY`. 2. Send 3 calls each as `alice` and `bob`. 3. List the budget's Redis keys. | Each user is refused on their own 3rd call; the 429 names only the caller's own value. Two counters exist with hashed value keys (`…:h<32 hex>`); no raw `sub` value in any key name. |
| TS-6 | FR-003 | Specific claim value pools spend | 1. Create `{"context":{"type":"ORGANIZATION"},"owners":[{"dimension":"CLAIM","claimName":"team","values":["search"]}]}`. 2. Two users with `team = search` send calls. | Both users' spend accumulates in one counter. |
| TS-7 | FR-004 | New customer claim works without a release | 1. Issue tokens with `cost_centre = cc-42`. 2. Create a budget with owner `cost_centre = cc-42`. 3. Send calls. | The budget matches and charges the same day, with no product change. |
| TS-8 | FR-012 | Requests without claims (API key) | With the TS-5 and TS-2 budgets, call `chat-proxy` with an application API key. | Budgets with a claim owner do not apply; the LLM proxy budget does. No new authentication on the route. |
| TS-9 | FR-005 | Calendar periods reset in UTC | Exhaust `DAY`, `WEEK`, `MONTH` budgets; advance the clock past 00:00 UTC, Monday 00:00 UTC (ISO week), the 1st 00:00 UTC. | Requests allowed again without administrator action; new counters with labels `YYYY-MM-DD`, `YYYY-Www`, `YYYY-MM`. |
| TS-10 | FR-006 | Daily and monthly budgets on the same owners | Create a `DAY` and a `MONTH` budget for the same owner; exhaust the daily one. | Both are enforced; the first exhausted budget refuses. |
| TS-11 | FR-011, FR-013 | Several budgets on one request — order | With TS-2 and an org budget with owner `team = search` of `0.03`/DAY, a `team = search` caller sends calls. | The **team** budget refuses first (same number of owners, smaller amount) though the proxy budget has headroom; both counters increase for every allowed call. |
| TS-11a | FR-002, FR-003 | Combined owners: each user per provider in a project | In project mode for `demo`, create `{"context":{"type":"PROJECT","project":"demo"},"owners":[{"dimension":"CLAIM","claimName":"sub","each":true},{"dimension":"LLM_PROVIDER","each":true}]}`, `0.02`, `DAY`. As `alice`, 3 calls to `openai-mock` and 1 to a second provider via proxies of `demo`; as `bob`, 1 call to `openai-mock`; one call through a proxy of another project. | Alice's 3rd OpenAI call is refused; her other-provider call and bob's call are allowed; the other project's call is not counted. Redis holds one key per (provider, user) combination with traffic (`…:<provider uuid>:h<32 hex>`). |
| TS-11b | FR-002, FR-003, FR-011 | One user, different limits per provider; stacking | In org mode create `sub = alice` + `LLM_PROVIDER = openai-mock` (`0.02`) and `sub = alice` + `LLM_PROVIDER = <second provider>` (`0.01`); then add an org budget `sub = each` at `0.015`. | Before the third budget, alice's two allowances are independent. After it, all matching budgets apply: alice is refused on OpenAI once she reaches `0.015` (the `each` budget is tighter); the specific budget does not override it, because its dimension set (claim + provider) differs from the `each` budget's (claim only). |
| TS-11c | FR-002 | Many owners | Create an org budget with project, LLM provider, LLM proxy, application and two claim owners (`team`, `sub`). | Accepted (no owner limit in v1); matches only requests that satisfy all six owners. |
| TS-11d | FR-003a | Most specific wins | In project mode for `demo`, create `sub = each` at `0.02`/DAY, then `sub = alice` at `0.05`/DAY. Alice and bob send calls. | Create of the second budget returns 201 with `overrides` = the `each` budget and a `BUDGET_OVERRIDES_OTHER` warning; `GET` of the `each` budget lists it in `overriddenBy`. Bob is refused at `0.02`; alice is allowed until `0.05`. Alice's spend is not added to the `each` budget's counter (no `…:h<alice>` key), and her events carry `budget_overridden_ids` = the `each` budget. |
| TS-11e | FR-003a | Overrides never cross contexts or dimension sets | With TS-11d, add an org-mode budget `sub = each` at `0.03`/DAY. | Alice is refused at `0.03` (the org budget still applies — a project budget never overrides an org budget). |
| TS-11f | FR-003a | Partial specificity stacks | In org mode create `sub = alice` + `LLM_PROVIDER = each` (`0.05`) and `sub = each` + `LLM_PROVIDER = openai-mock` (`0.02`); alice calls `openai-mock`. | Neither overrides the other; both apply and alice is refused at `0.02`. Neither budget's `overrides` lists the other. |
| TS-11g | FR-003a, FR-007 | Monitor override lifts enforcement for one value | Enforce `sub = each` `0.02`; create Monitor `sub = alice` `0.02`. | Create returns the `BUDGET_OVERRIDES_OTHER` warning; alice is never refused (only the monitor budget governs her, `Budget-Monitor: would-refuse` after `0.02`); bob is refused. |
| TS-11h | FR-002, FR-003 | Model owner, specific and each | Org budgets `sub = each` + `MODEL = gpt-4o` (`0.02`) and `MODEL = each` (`0.03`). Through a proxy that rewrites the requested alias `fast` to `gpt-4o-mini`, send `gpt-4o` and `fast` calls. | `gpt-4o` calls count toward both budgets; `fast` calls count as `gpt-4o-mini` (the upstream id) and only toward the `each` budget, with one key per model (`…:m<32 hex>`). A user is refused on `gpt-4o` at `0.02` while `fast` stays allowed. |
| TS-11i | FR-002 | Model id missing | A route where the upstream model id is not available to the policy. | The request does not count toward budgets with a model owner; other matching budgets apply as usual. |
| TS-11j | FR-003 | Several values on one owner share one allowance | Org budget `{"context":{"type":"ORGANIZATION"},"owners":[{"dimension":"LLM_PROVIDER","values":["openai-mock","anthropic-mock"]}]}`, `0.03`/DAY. Send calls to both providers and one to a third provider; then `GET` the budget. | Calls to both providers add to one counter (`…:_`) and are refused together once the total reaches `0.03`; the third provider's call is not counted. The response returns `values` sorted with `displayNames` in the same order. |
| TS-11k | FR-003a | Subset of values overrides | With TS-11j, create `LLM_PROVIDER` `["openai-mock"]` at `0.01`/DAY; then `["openai-mock","gemini-mock"]` at `0.05`. | The `[openai-mock]` budget's create response lists the TS-11j budget in `overrides`; OpenAI calls are governed only by `0.01` and not charged to the shared budget, while Anthropic calls still use it. The `[openai-mock, gemini-mock]` budget is not a subset of TS-11j's values, so neither overrides the other and both apply to OpenAI traffic (the `[openai-mock]` budget overrides both). |
| TS-11l | FR-002, FR-003 | Team on several providers | Org budget `team = search` + `LLM_PROVIDER` `["openai-mock","anthropic-mock"]`, `0.03`/DAY; `team = search` and `team = ads` callers call both providers. | Search members' calls to either provider share one allowance; `ads` calls are not counted. |
| TS-11m | FR-009a | One of several listed values deleted | Delete `anthropic-mock` while the TS-11j budget exists. | The budget becomes `DISABLED` with `statusReason = OWNER_DELETED` (dropping the value instead is an open question). |
| TS-12 | FR-013 | Several budgets exhausted at once | Exhaust a `DAY` and a `MONTH` budget on the same request path. | `Retry-After` is the latest reset (the monthly one). |
| TS-13 | FR-007 | Monitor mode | 1. Create a `MONITOR` budget of `0.01`. 2. Exceed it. 3. Switch to `ENFORCE`. | After exhaustion calls return 200 with `Budget-Monitor: would-refuse` and `metadata.budget_monitor_would_refuse = true`; after the switch the next call is refused immediately (the counter already holds the monitored spend). |
| TS-14 | FR-008 | Edit mutable fields; disable and enable | 1. `PUT` name, description, thresholds, mode. 2. Set `status = DISABLED`, send calls. 3. Set `ACTIVE`. | Changes apply. While disabled, calls are not evaluated or counted; after enabling, the counter resumes from its earlier value in the same period. |
| TS-15 | FR-009, FR-010, SC-002 | Raise the amount with no redeploy | Raise the TS-2 budget to `0.10` via `PUT`. | Within 60 s the next call succeeds without redeploying anything; the Redis counter still holds `50000000` before that call; `chat-proxy` deployment history is unchanged. |
| TS-16 | FR-009a, FR-026 | Audit before/after | Create, update amount, disable, delete a budget. | Each action has an audit record with the actor; `details` holds `{after}` (create), `{before, after}` (update / status change, e.g. `before.amount = "0.05"`, `after.amount = "0.10"`), `{before}` (delete). No analytics event is published by platform-api. |
| TS-17 | FR-009a, FR-026 | Owner deleted | Delete LLM proxy `chat-proxy`. | Its budget becomes `DISABLED` with `statusReason = OWNER_DELETED`; the UI explains why; the audit log has an `UPDATE` by `system` with before/after status. |
| TS-18 | FR-014 | Allowed-response headers | With two budgets applicable, send a call. | `Budget-Id`, `Budget-Remaining`, `Budget-Reset` describe the most constraining budget (lowest remaining at admission). |
| TS-19 | FR-015, FR-024a | Unpriced traffic | Call a model with no price entry. | 200, never refused for being unpriced; Redis counter unchanged; the event has `metadata.llm_cost_status = not_calculated` and `metadata.budget_id` set. |
| TS-20 | FR-016, FR-016a | Streaming with `include_usage` | 1. Send `stream: true` chat completions without `stream_options` under an enforce budget. 2. Repeat under a monitor-only budget. 3. Exhaust the budget mid-stream. | The upstream receives `stream_options.include_usage = true` (other `stream_options` preserved) in both cases; the client receives tokens incrementally; the counter increases by $0.01 per call and `llm_cost_status = calculated`. A stream in progress when the budget runs out completes; the next request is refused. |
| TS-21 | FR-016b | Aborted stream | Abort a streamed call mid-way. | Until the SDK offers an `OnStreamError` hook: the call is neither charged nor marked (documented limitation). Expected behaviour after the hook: NOT ANSWERED (delivery phase 3). |
| TS-22 | FR-017, FR-019, SC-003 | Shared counter across replicas | Scale `gateway-runtime` to 2 replicas (and, if available, a second gateway of the organization on the same Redis); drive 20 concurrent calls under a `0.05` budget. | One counter; refusals start on every replica as soon as it reaches the amount; final overshoot ≤ the cost of calls in flight at that moment. |
| TS-23 | FR-022 | Budgets refuse to activate without Redis | Remove the `budgets_v1.redis` block, keep `budgets.enabled = true`, restart the gateway-controller. | It does not start; the log names `[policy_configurations.budgets_v1.redis]` as required for budgets. |
| TS-24 | FR-022 | Unsafe Redis warning | Start Redis with `--maxmemory-policy allkeys-lru --appendonly no`. | The gateway starts, logs a WARN that Redis may lose data, and sets `budget_redis_config_unsafe = 1`; repeated every `config_check_interval`. On a managed Redis that blocks `CONFIG`, an INFO is logged once instead. |
| TS-25 | FR-022b | Redis authentication, TLS and mTLS | Configure a Redis with ACL user/password and TLS (then client certificate). | The gateway connects with each configuration; a wrong password or untrusted certificate fails the connection; credentials never appear in logs. |
| TS-26 | FR-023, SC-007 | Threshold crossings exactly once | With 2 replicas, push a $0.10 budget with thresholds 50/80 past 50 % and 80 %. | Exactly one event with `budget_threshold_crossed = 50` and one with `= 80` in the period, whatever the replica count; two alerts from the "BUDGET: Threshold crossed" rule. Redis holds markers `…:thr:<t>:<amountVersion>`. |
| TS-27 | FR-009 | Re-arming and highest-only | Continue TS-26: 1. Raise the amount to $0.20 (spend $0.09 = 45 %). 2. Push spend to $0.10 and then $0.16. 3. Lower the amount to $0.11. 4. Send one more call. | Step 1: no event. Step 2: `= 50` again at $0.10 and `= 80` at $0.16 (new amount version). Step 4: one event with `= 80` only; the 50 % marker is set silently. |
| TS-28 | FR-024 | Budgets page shows configuration only | Open the Budgets list and detail pages as `$OPS`. | In org mode: all budgets with their context; in project mode: only that project's budgets. Name, description, owners summary (e.g. "Demo · each user · openai-mock"), amount, period, mode, thresholds and status (with reason) are shown; no spend, remaining, percentage, meter, per-value table or near-limit state anywhere. Budgets with `each` owners read like "each user gets $25 / day". |
| TS-29 | FR-024c | "See usage" link | Follow "See usage" on a budget. | Opens the LLM Analytics page under `APIP_AIW_MOESIF_ANALYTICS_URL` with the Budget filter set to the budget (falls back to the base URL until the deep-link format is confirmed). With `APIP_AIW_MOESIF_ANALYTICS_URL` changed, the Insights page and the link both use the new URL. |
| TS-30 | FR-024e | Budget Usage card and Budget filter | 1. After TS-2, TS-5 and TS-35, open the LLM Analytics page. 2. Set the Budget filter to the per-user `sub` budget. | The page is unchanged except for one Budget Usage card and a Budget filter next to the API filter (no new page, section or sidebar item). Each budget row shows name, amount, spend this period equal to its Redis counter ÷ 10⁹, % used and refused calls. With the filter set, one row per user (alice, bob) with their own spend, % used and refusals. |
| TS-31 | FR-024d | No spend in the control plane | Inspect platform-api responses and database after traffic. | No spend, request, refusal or unpriced counts in any API response or table. |
| TS-32 | FR-025 | Analytics fields on every event | Send allowed, refused and threshold-crossing calls; inspect the Moesif events. | Each carries `metadata.budget_id`, `budget_name`, `budget_owners`, `budget_owner_values`, `budget_period`, `budget_amount`, `budget_spend`, `budget_percent_used`, `budget_enforcement_mode`, `budget_refused`, `budget_monitor_would_refuse`, the `budgets` JSON and `llm_cost_status` with their JSON types; refused calls also produce events; on a crossing, the flat fields describe the crossing budget. Cost is `metadata.aiMetadata.llmCost`. |
| TS-33 | FR-029, SC-005 | Budget and cost-based rate limit on one route | 1. Attach `llm-cost-based-ratelimit` to `chat-proxy` with a $0.02 cost limit; keep the `0.05` monthly budget. 2. Raise the rate limit above the budget and exhaust the budget. | Step 1: the third call gets 429 with the rate-limit policy's own code and no budget body. Step 2: 429 `BUDGET_EXHAUSTED` with `details.budget` and `Retry-After`. The refusals are distinguishable by `code`; the rate-limit configuration works unchanged. |
| TS-34 | SC-006 | Reconciliation | Drive ≥ 100 priced and unpriced calls with 2 replicas under a budget without `each` owners; compare `rc GET … ÷ 10⁹` with the sum of `metadata.aiMetadata.llmCost` over events with that `budget_id`, `budget_period` and `llm_cost_status = calculated`. | Zero unexplained difference; any difference is explained by `budget_charge_failures_total` or `budget_check_skipped` events; unpriced calls visible as `not_calculated`. |
| TS-35 | SC-001, SC-002 | Create a working budget in the UI | As `$ADMIN`, time creating "Chat proxy daily" (LLM proxy `chat-proxy`, `0.03`, Day, 50/80/100, Enforce) in the AI Workspace, then send calls. | Save completes in under 2 minutes from opening the dialog; the 4th call returns 429 `BUDGET_EXHAUSTED` within 60 s of saving; no redeploy of `chat-proxy` or the gateway. |
| TS-36 | FR-009 | Amount lowered below current spend | Lower an enforce budget's amount below its counter. | The next request is refused; the counter is unchanged. |
| TS-37 | FR-005, FR-011 | Budget created mid-period; request across a period boundary | 1. Create a budget mid-period. 2. Start a long request before 00:00 UTC that finishes after it. | 1. Starts at zero (not backdated). 2. The charge lands in the counter of the period in which the response finished. |
| TS-38 | FR-011 | Budget deleted | Delete an exhausted budget. | Enforcement stops within seconds on every gateway; its Redis keys remain until their TTL and are then gone. |

# 4. Negative and Validation Scenarios

One row per row of the platform-api request-validation table in the Budgets - Implementation document, §2.2, in the same order. Every error body uses the standard `{status, code, message}` shape with a generic message.

| ID | Scenario | Input | Expected result |
| :---- | :---- | :---- | :---- |
| TS-N1 | Display name missing, empty or too long | `displayName` omitted, `""` or 256 characters | 400 `BUDGET_INVALID` |
| TS-N2 | Description too long | `description` of 1,024 characters | 400 `BUDGET_INVALID` |
| TS-N3 | Invalid amount | `amount` omitted, `"0"`, `"-5"`, `"abc"`, 13 integer digits, or 7 fractional digits | 400 `BUDGET_INVALID` |
| TS-N4 | Non-USD currency | `"currency": "EUR"` | 400 `BUDGET_INVALID` |
| TS-N5 | Unknown period | `"period": "YEAR"` | 400 `BUDGET_INVALID` |
| TS-N6 | Unknown enforcement mode | `"enforcementMode": "WARN"` | 400 `BUDGET_INVALID` |
| TS-N7 | Bad thresholds | 6 entries, `[50, 50]`, `[0]` or `[101]` | 400 `BUDGET_INVALID` |
| TS-N8 | Missing or unknown context | no `context`; `{"type":"TEAM"}`; `{"type":"PROJECT"}` without `project`; `{"type":"ORGANIZATION","project":"demo"}` | 400 `BUDGET_INVALID_OWNER` |
| TS-N9 | Unknown owner dimension | `{"dimension":"TEAM","values":["x"]}` | 400 `BUDGET_INVALID_OWNER` |
| TS-N10 | Owner with neither or both of `values` / `each`, an empty list or duplicate values | `{"dimension":"PROJECT"}`; `{"dimension":"CLAIM","claimName":"sub","each":true,"values":["alice"]}`; `{"dimension":"LLM_PROVIDER","values":[]}`; `{"dimension":"LLM_PROVIDER","values":["openai-mock","openai-mock"]}` | 400 `BUDGET_INVALID_OWNER` |
| TS-N11 | Duplicate dimension | two `LLM_PROVIDER` owners; two `CLAIM` owners with `claimName = sub` (two different claim names are allowed) | 400 `BUDGET_OWNER_DUPLICATE` |
| TS-N12 | Claim owner without a claim name, or an invalid claim name | `{"dimension":"CLAIM","values":["x"]}`; `"claimName": "team name"`; `"claimName": "team/name"`; 256-character claim | 400 `BUDGET_INVALID_OWNER` |
| TS-N12a | Invalid model value | `{"dimension":"MODEL","values":[""]}`; a 256-character model id; a value with a control character | 400 `BUDGET_INVALID_OWNER` |
| TS-N13 | Context project or owner does not exist in the caller's organization | `context.project = no-such-project`; an owner `{"dimension":"PROJECT","values":["no-such-project"]}`; an owner listing one existing and one unknown value (`{"dimension":"LLM_PROVIDER","values":["openai-mock","no-such-provider"]}`); separately the handle of a project in organization B; in project context, an LLM proxy of another project | 404 `BUDGET_OWNER_NOT_FOUND`, identical in every case |
| TS-N14 | LLM provider / proxy value of the wrong kind | `{"dimension":"LLM_PROVIDER","values":["chat-proxy"]}`; `{"dimension":"LLM_PROXY","values":["openai-mock"]}` | 400 `BUDGET_INVALID_OWNER` |
| TS-N14a | Project owner in a project context | `{"context":{"type":"PROJECT","project":"demo"},"owners":[{"dimension":"PROJECT","values":["demo"]}]}` | 400 `BUDGET_OWNER_NOT_ALLOWED` |
| TS-N15 | Duplicate context, owners and period | A second `MONTH` org budget with owner `LLM_PROXY = chat-proxy`; and the same owners as an existing budget listed in a different order; the same `LLM_PROVIDER` values in a different order (`["anthropic-mock","openai-mock"]` vs `["openai-mock","anthropic-mock"]`) | 409 `BUDGET_ALREADY_EXISTS` |
| TS-N16 | Duplicate handle | `handle` already used in the organization | 409 `BUDGET_ALREADY_EXISTS` |
| TS-N17 | Unknown request fields (mass assignment) | Extra `"organizationId": "{organization B uuid}"`, `"spent": "0"` or `"createdBy": "x"` | 400 `BUDGET_INVALID`; nothing is created |
| TS-N18 | Update changes an immutable field | `PUT` changing the context, the owners or the period | 400 `BUDGET_IMMUTABLE_FIELD` (see the REST APIs document, §5, question 4, on strict decoding) |
| TS-N19 | Budget not found | `GET` / `PUT` / `DELETE` with an unknown `budgetId` | 404 `BUDGET_NOT_FOUND` |
| TS-N20 | Bad query parameters | `limit=0`, `limit=101`, `status=PAUSED`, `contextType=TEAM`, `enforcementMode=WARN` | 400 `BUDGET_INVALID` |

# 5. Authorization and Tenant Isolation Scenarios

| ID | Scenario | Expected result |
| :---- | :---- | :---- |
| TS-A1 | Unauthenticated request to every `/budgets` operation (no token, expired token, malformed token) | 401, identical body for every failure cause. |
| TS-A2 | `$OPS` (`ap:budget:read` only) calls `POST`, `PUT`, `DELETE` (FR-027) | 403, generic body. `GET /budgets` and `GET /budgets/{id}` return 200. |
| TS-A3 | `$NOSCOPE` calls any `/budgets` operation (FR-027) | 403, generic body, no budget data. |
| TS-A4 | Caller from organization A uses the handle of an organization B budget in `GET`, `PUT`, `DELETE` | 404 `BUDGET_NOT_FOUND`, identical to a budget that does not exist; organization B's budget is unchanged. |
| TS-A5 | Organization A creates a budget whose `context.project` or an owner `value` is the handle of an organization B project, LLM provider, LLM proxy or application | 404 `BUDGET_OWNER_NOT_FOUND`, identical to a nonexistent owner. |
| TS-A6 | `GET /budgets` as organization A | Only organization A's budgets; the organization comes from the verified token, never from the payload or query. |
| TS-A7 | A project member who lacks `ap:budget:manage` tries to change a budget created in their own project, or naming it as an owner (FR-028) | 403; association with the owner grants nothing. |
| TS-A8 | Organization A creates budgets; traffic hits organization B's gateway | Organization A's budgets are never broadcast to, matched on, or charged by organization B's gateways. |
| TS-A9 | `GET /api/internal/v1/budgets` without a gateway token, with an invalid token, and with organization A's gateway token | The first two are rejected with no budget data; the valid call returns only organization A's non-deleted budgets. |
| TS-A10 | A budget with an `each` claim owner refuses `alice` | The 429 and the `Budget-*` headers name only `alice`'s own value, never other users' values. |
| TS-A11 | AI Workspace as `$OPS` | Budgets list and detail are visible; **Create Budget**, the row menu, **Edit**, **Disable / Enable** and **Delete** are hidden. |
| TS-A12 | A budget name, description or claim name containing `<script>alert(1)</script>` or `"><img src=x onerror=alert(1)>` | Rendered as literal text on the list and detail pages; no script executes; the "See usage" URL is encoded. |
| TS-A13 | Platform in read-only mode: call `BudgetService` create, update, delete and owner-deleted disable directly, bypassing handlers (GO-AUTH-015) | Every write is refused; the budget repository's write methods refuse too. The internal resync GET still works. |
| TS-A14 | Inspect Redis key names and INFO+ logs after per-user traffic | No raw claim values, API keys, tokens or Redis credentials appear. |
| TS-A15 | `ap_viewer` calls `GET /budgets` | NOT ANSWERED — depends on the open decision whether `ap_viewer` gets `ap:budget:read` (proposed: no, so 403). |

# 6. Failure, Retry and Recovery Scenarios

| ID | Scenario | Expected result |
| :---- | :---- | :---- |
| TS-F1 | Gateway runtime restarts mid-period (SC-004) | The Redis counter is unchanged; enforcement continues. |
| TS-F2 | Gateway-controller restarts mid-period (SC-004) | Budget definitions are rebuilt from the controller DB; the counter is unchanged; enforcement continues. |
| TS-F3 | platform-api stopped for 2 minutes (FR-021) | Gateways keep enforcing with the last definitions and counting in Redis; budget edits made after platform-api returns reach gateways on reconnect (resync). |
| TS-F4 | Redis stopped, `failure_mode = "closed"`, request under an enforce-mode budget (FR-020) | 503 `BUDGET_CHECK_UNAVAILABLE` with a generic body and no Redis detail; ERROR logged (rate-limited) with the Redis endpoint. |
| TS-F5 | Redis stopped, `failure_mode = "open"` (FR-020) | Requests pass with no `Budget-*` headers, are not counted, and their events carry `budget_check_skipped = true`; WARN logged. |
| TS-F6 | Redis stopped, request under monitor-only budgets (FR-020) | Allowed; never 503. |
| TS-F7 | Redis restarted **without** its volume (FR-022a) | The counter restarts at zero and requests are allowed again (documented behaviour). |
| TS-F8 | `INCRBY` fails after a successful admission | No client impact; ERROR logged and `budget_charge_failures_total` increments; the cost is still in Moesif (explains an SC-006 difference). |
| TS-F9 | Threshold marker `SET NX` fails | No client impact; WARN and `budget_threshold_events_total{outcome="marker_error"}`; no emission for that request. |
| TS-F10 | Gateway serves traffic before budget definitions arrive | Requests allowed; budgets enforced once definitions arrive; one WARN. |
| TS-F11 | Analytics disabled on the gateway that wins a threshold marker | The marker is set but no warning reaches Moesif (documented at-most-once delivery); the Budget Usage card still shows % used from other events. |
| TS-F12 | A `budget.*` websocket event is missed | The change arrives on the next resync on connect; reconciliation deletes budgets absent from the list. |
| TS-F13 | Concurrent `PUT` requests to the same budget | NOT ANSWERED: the concurrency rule for budget updates is not specified. |

# 7. Upgrade and Compatibility Scenarios

| ID | Scenario | Expected result |
| :---- | :---- | :---- |
| TS-U1 | Upgrade an existing deployment with data present, on PostgreSQL, SQLite and SQL Server | `budgets` and its indexes are created by guarded DDL; `audit.details` is added by the guarded per-dialect `ALTER`; existing `audit` rows keep `details = NULL`; re-running start-up is a no-op. |
| TS-U2 | Upgrade an existing gateway-controller | The controller `budgets` table is created; existing data is intact. |
| TS-U3 | Traffic right after upgrade, budgets not enabled | No behaviour change: `budgets.enabled` defaults to `false`, so no policy is injected. |
| TS-U4 | Gateway older than this feature connected to an upgraded platform-api, with budgets in the organization | It logs and drops the unknown `budget.*` messages and keeps serving traffic; budgets are **not** enforced on it (known limitation). |
| TS-U5 | Existing API clients and existing LLM provider rate-limit configurations after upgrade | Unchanged (FR-029); the budget endpoints are new. |
| TS-U6 | LLM proxies deployed before the upgrade, with a project budget | Project budgets do not match their traffic until the proxy is redeployed with the project-id annotation; afterwards they apply. |
| TS-U7 | Rollback to the previous release after budgets were created | The previous release runs against the upgraded schema unchanged; alternatively `budgets.enabled = false` removes the policy. Redis keys expire on their own. |
| TS-U8 | AI Workspace upgraded without setting `APIP_AIW_MOESIF_ANALYTICS_URL` | The Insights page opens the same URL as before (`https://www.moesif.com/wrap/basic`). |

# 8. Performance Scenarios

| ID | Scenario | Target | Measured |
| :---- | :---- | :---- | :---- |
| TS-P1 | Added latency of budget evaluation, including both pipelined Redis round-trips, with Redis in the gateway's network region (SC-008) | < 5 ms at the 95th percentile | NOT ANSWERED: not measured yet |
| TS-P2 | Propagation of a new or changed budget to every gateway (SC-002) | < 60 s | NOT ANSWERED: not measured yet |
| TS-P3 | Threshold crossing visible in Moesif after the crossing request (SC-007) | < 2 min, exactly once | NOT ANSWERED: not measured yet |
| TS-P4 | Scale envelope: ~1,000 budgets per organization, ~10,000 value combinations per period on a budget with `each` owners, budgets with many owners, up to ~20 runtime replicas | No regression of TS-P1 / TS-P2 | NOT ANSWERED: not measured yet |
| TS-P5 | Streaming responses keep incremental delivery under the budget policy | First chunk delivered before the last is produced (no fallback to buffered) | NOT ANSWERED: not measured yet |

# 9. Automation

| Test suite | Location | PR |
| :---- | :---- | :---- |
| platform-api unit tests (validation and service rules, read-only mode, owner deleted, money conversion, schema parity and audit upgrade, repositories, audit, LLM proxy project-id annotation) | Planned: `platform-api/internal/service/budget_service_test.go`, `budget_readonly_test.go`, `budget_owner_deleted_test.go`, `llm_deployment_test.go`; `platform-api/internal/utils/budget_money_test.go`; `platform-api/internal/database/budgets_schema_test.go`, `audit_details_upgrade_test.go`; `platform-api/internal/repository/budget_repository_test.go`, `audit_test.go`; `platform-api/internal/handler/gateway_internal_readonly_test.go`; `platform-api/internal/server/role_scope_map_test.go` | NOT ANSWERED |
| platform-api integration tests (Budgets API CRUD, tenant isolation, scopes) | Planned: `platform-api/internal/handler/budget_integration_test.go` | NOT ANSWERED |
| gateway-controller unit tests | Planned: `gateway/gateway-controller/pkg/controlplane/budget_events_test.go`, `pkg/utils/budget_resource_test.go`, `pkg/lazyresourcexds/lazyresourcexds_test.go`, `pkg/storage/budget_store_test.go`, `pkg/config/config_test.go`, `pkg/utils/system_policies_test.go`, `pkg/utils/llm_transformer_test.go`, `pkg/utils/llm_transformer_multiprovider_test.go` | NOT ANSWERED |
| Budget system policy unit tests (miniredis) | Planned: `gateway/system-policies/budget/` — `config_test.go`, `redis_client_test.go`, `redis_safety_test.go`, `keys_test.go`, `period_test.go`, `money_test.go`, `definitions_test.go`, `matcher_test.go`, `counter_test.go`, `budget_test.go`, `failure_test.go`, `stream_test.go`, `thresholds_test.go`, `monitor_test.go`, `analytics_fields_test.go`, `matcher_bench_test.go`; `sdk/core/policy/v1alpha2/context_accessors_test.go` | NOT ANSWERED |
| Analytics pipeline tests | Planned: `gateway/gateway-runtime/policy-engine/internal/analytics/analytics_test.go`, `publishers/moesif_test.go`; `gateway/system-policies/analytics/analytics_budget_test.go` | NOT ANSWERED |
| Policy-engine phase-ordering test | Planned: `gateway/gateway-runtime/policy-engine/internal/executor/chain_phase_order_test.go` | NOT ANSWERED |
| Gateway integration tests (godog) | Planned: `gateway/it/features/budgets.feature`, `budgets-streaming.feature`, `budgets-shared-counter.feature`, `budgets-redis-failure.feature`, `budgets-ratelimit-coexistence.feature`, `budgets-claims.feature`, `budgets-combined-owners.feature`, `budgets-thresholds.feature`, `budgets-monitor.feature`; steps in `gateway/it/steps_budget.go`; mock platform-api in `tests/mock-servers/mock-platform-api/`; streamed mock under `gateway/it/mock-api/paths/` | NOT ANSWERED |
| AI Workspace component tests | Planned: `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDialog.test.tsx`, `Budgets.test.tsx` | NOT ANSWERED |
| End-to-end validation record (incl. SC-001, SC-006, SC-007 measurements) | Planned: `docs/gateway/budgets/validation.md` | NOT ANSWERED |
| Performance (SC-008) | Planned: `gateway/perf/performance-test-scripts/` with `gateway/docker-compose-perf.yaml` (+ Redis) | NOT ANSWERED |

# 10. Open Questions

| Question | Owner | Status |
| :---- | :---- | :---- |
| What is the concurrency rule for two simultaneous updates to the same budget (last write wins, or optimistic locking)? TS-F13 needs it. | Feature owner | Open |
| Does `ap_viewer` get `ap:budget:read`? TS-A15 needs it. | Product leads | Open |
| Should platform-api or the UI warn when an organization has budgets and some connected gateways do not enforce them (TS-U4)? | Feature owner | Open |
| Does the LLM proxy route expose its upstream provider's id to policies? TS-3 depends on it; verify early in delivery phase 1. | Gateway team | Open |
| Which TS-P1 load profile (budgets per request, request rate, replicas, Redis placement) is representative for SC-008? | Feature owner | Open |
| Expected behaviour of TS-21 once an `OnStreamError` hook exists. | Gateway team | Open |
| Deep-link format for TS-29 (LLM Analytics path and Budget filter query). | Analytics team | Open |
