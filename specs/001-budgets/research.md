# Research: Budgets

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md) | **Date**: 2026-10-08 | **Revised**: 2026-10-09 (architecture change: Redis-only counting, no spend in the control plane, gateway-detected thresholds — R5, R6, R9, R12); 2026-10-13 (Moesif presentation and event field location — R9); 2026-10-14 (budget owners: context plus appended ANDed owners, multi-value owners — R8, R13)

Each entry records a design decision, why it was chosen, and what else was considered. Code references are to `wso2/api-platform` @ `099636fbb` unless prefixed `GC:` (`wso2/gateway-controllers`, the external policy repo pinned in `gateway/build-manifest.yaml`).

---

## R1. Budget values are data, not policy parameters

**Decision**: Budget definitions are an org-level resource in platform-api, delivered to gateways the same way subscription plans are, and read by the enforcement policy **at request time**. They are never policy parameters.

**Rationale**:
- Policy parameters are bound once when the policy engine builds a chain (`policy-engine/internal/registry/registry.go:84-123` → `entry.Factory(metadata, mergedParams)`). Changing one requires a new deployment → `llmprovider.deployed` → controller fetch → transform → `PolicyChainConfig` xDS snapshot → chain rebuild (`xdsclient/handler.go:114-281`). Budgets as params would redeploy every LLM route on every edit (violates FR-010).
- Subscription plans already solve "org-level limits that change without redeploying APIs": platform-api broadcasts `subscriptionPlan.*` to every gateway in the org (`platform-api/internal/service/subscription_plan_service.go:264-290`), the controller persists them and pushes a separate xDS resource, and policies look them up from an SDK singleton per request.

**Alternatives considered**:
- *Budget as a user-attached policy with params* — rejected: redeploys, and budgets spanning many artifacts (org, project, claim) have no single route to attach to.
- *Policy pulls budgets from platform-api over HTTP* — rejected: puts the control plane in the request path and duplicates the existing push pipeline.

**Open (architecture leads, 2026-10-09)**: guidance received asked to "avoid new tables". Definitions still need one platform-api table (`budgets`) because they are org-level data edited in the UI and pushed to gateways; confirm a single definitions table is acceptable (plan risk 8a).

---

## R2. Transport for budget definitions: LazyResources

**Decision**: Reuse the generic **LazyResources** xDS channel (`ResourceType: "Budget"`), with a controller DB table as the source of truth on the gateway side and a REST resync on (re)connect.

Flow: platform-api `budget.created|updated|deleted` websocket event → controller `handleMessage` (`gateway-controller/pkg/controlplane/client.go:1457`) → persist to controller DB + `common/eventhub` event (replica sync) → `lazyResourceManager.StoreResource(...)` (`pkg/lazyresourcexds/lazy_resource_manager.go:47`) → policy engine `GetLazyResourceStoreInstance().GetResourcesByType("Budget")` (`sdk/core/policy/v1alpha2/lazy_resource.go:77-132`). On connect: `GET /api/internal/v1/budgets`, mirroring `syncSubscriptionPlans` (`client.go:867`).

**Rationale**: No new xDS type, SDK store or engine handler; the analytics system policy already reads LazyResources at request time (`gateway/system-policies/analytics/analytics.go:958`). Budget definitions change rarely, which matches the store's documented purpose. A store `ReplaceAll` takes effect on the next request (SC-002: < 60 s; expected < 5 s).

**Caveats**: The controller-side LazyResource store is in-memory (`storage/lazy_resource_store.go`), so budgets must be re-loaded from the controller DB on startup. Snapshots are state-of-the-world: fine for definitions; live spend never travels this way (it lives only in Redis, R5).

**Alternatives considered**: *Dedicated `v1.BudgetState` xDS type* (cleaner typing; needs `CombinedCache`, xdsclient and SDK-store changes) — keep as a follow-up if LazyResources becomes crowded.

---

## R3. Enforcement point: a prepended system policy, body phases only

**Decision**: New system policy `wso2_apip_sys_budget`, added to `defaultSystemPolicies` (`gateway-controller/pkg/utils/system_policies.go:78`) and prepended to every chain (`:224`) when budgets are enabled on the gateway. It no-ops unless `SharedContext.APIKind ∈ {LlmProvider, LlmProxy}` and the org has at least one budget.

- **Request body phase** (`BodyModeBuffer`): resolve applicable budgets, read their Redis counters, refuse with `ImmediateResponse` (`sdk/core/policy/v1alpha2/action.go:31,168`), and inject `stream_options.include_usage=true` for streamed OpenAI-style chat completions whenever any budget (enforce or monitor) applies (R7).
- **Response phase** (`BodyModeStream`, implements `StreamingResponsePolicy`): on `EndOfStream` (or `OnResponseBody` when buffered) read `Metadata["x-llm-cost"]` (string, `%.10f`) and `x-llm-cost-status`, `INCRBY` the counters, detect threshold crossings (R9).

**Rationale**:
- Header phase is impossible: a prepended header handler runs **before** auth and sees no claims; body phases run after all header handlers (`kernel/extproc.go:278,404`; `executor/chain.go:74,228`). This is what design-doc-v1 concluded.
- Response handlers run in reverse chain order (`executor/chain.go:381-396,833-850`), so the prepended policy runs **last**, after `llm-cost` has set `x-llm-cost` — the same pattern `llm-cost-based-ratelimit` uses (GC `advanced-ratelimit/cost_extractor.go:42,193,241`). A phase-ordering test pins both assumptions.
- Must be a streaming policy: if any response-body policy is buffer-only, the whole route falls back to buffered and users lose token streaming (`sdk/core/policy/v1alpha2/interface.go:127-131`; `kernel/body_mode.go:103-114`).

**Dependency**: `llm-cost` is a regular per-artifact policy that platform-api attaches by default (`platform-api/internal/service/llm_deployment.go:1131-1134`) but can be absent. Without it every request is *unpriced* (charged 0, `llm_cost_status = not_calculated` in its analytics event), which is visible in Moesif.

**Open (architecture leads, 2026-10-09)**: guidance says "system-level policies are not supported" as a product feature. Whether a built-in system policy (like the analytics collector) is acceptable, or the budget policy must be auto-attached to every LLM deployment, remains open (plan risk 8b). Auto-attaching would reintroduce redeploys when budgets are first enabled, but not on budget edits (definitions stay data, R1).

---

## R4. Charge exactly once across proxy → provider hops

**Decision**: Evaluate and charge only on the **outermost** hop. Requests carrying `x-wso2-internal-loopback` (set on the proxy→provider hop, `gateway-controller/pkg/utils/llm_transformer.go:988-995`) skip the budget policy. The outer (proxy) hop's applicable set includes both the proxy artifact **and** its upstream provider artifact (FR-016c).

**Rationale**: Charging both hops double-counts; charging only the inner hop loses the caller's identity and project.

**Open item (verify in implementation)**: confirm the proxy route exposes its upstream provider's artifact UUID to policies (`Metadata.LLM{TemplateHandle, ProviderName}` exists — `gateway-controller/pkg/models/runtime_deploy_config.go:49-63`; UUID may need adding to route metadata).

---

## R5. Counting: shared Redis, required (revised 2026-10-09)

**Decision** (feature owner, 2026-10-09): Spend is counted **only in a shared Redis** that every gateway enforcing an organization's budgets uses. Redis is **required** for budgets. There is no spend in platform-api and no gateway → control-plane spend sync.

**Mechanism**:
1. Admission (request-body phase): for every applicable budget, read `budget:v1:{<budgetUuid>}:<periodLabel>:<valueKey>` (one pipelined round-trip). Refuse when an enforce-mode budget's value ≥ its amount.
2. Charge (response end): `INCRBY` each applicable counter by the request's cost in integer nano-USD (pipelined, one round-trip), and set `EXPIRE` to period end + 1 day on first write (`INCRBY` + `EXPIRE … NX`, or `EXPIREAT` in the same pipeline). `INCRBY` returns the new total, which feeds the analytics event (spend after request) and threshold detection (R9).
3. All replicas of all gateways of the org share the same keys, so overshoot is limited to requests already in flight (SC-003). Restarts of gateways or the control plane lose nothing (SC-004).

**Key choices**: integer nano-USD avoids float drift (`INCRBYFLOAT` rejected); the `{budgetUuid}` hash tag keeps a budget's counters and markers in one slot for a future Redis Cluster; claim values are hashed in `valueKey` (no PII in keys; see data-model §5).

**Failure modes**:
- **Redis unreachable** → refuse requests subject to enforce-mode budgets with 503 `BUDGET_CHECK_UNAVAILABLE` (FR-020). Per gateway `failure_mode = "open"` allows them; their spend is not counted. Monitor-only requests are always allowed. This is the opposite of the rate-limit default (`failureMode: open`, GC `advanced-ratelimit/policy-definition.yaml:430-437`), because failing open spends money that cannot be recovered.
- **Charge fails after a successful admission** (Redis drops mid-request) → the request already ran; its cost is lost from the counter, logged and counted in `budget_charge_failures_total`; it is still in Moesif (SC-006 explains the difference).
- **Redis loses data** (restart without persistence, failover to a lagging replica, eviction) → counters restart at zero for the period (FR-022a). Documented; mitigated by AOF persistence and `maxmemory-policy noeviction`. There is no re-seed source, by design.

**Alternatives considered**:
- *Local counters per replica + durable ledger in platform-api, synced every 2–10 s via controller → websocket (`budget.usage` / `budget.totals`), Redis as optional strict mode* (the 2026-10-08 design) — **rejected 2026-10-09 once spend left the UI**: its main purpose was to give the Budgets page current-period figures without Moesif and to work without extra infrastructure. Without the UI need it costs two new tables (`budget_period_spend`, `budget_sync_cursors`), a new controller ingest endpoint, replica identities, an outbox, sync intervals and disconnect alerts, and still only bounds overshoot to `syncInterval × spend rate of other replicas` instead of in-flight requests. It also conflicts with the architecture guidance to avoid new tables and rely on Redis.
- *Synchronous platform-api call per request* — rejected: control plane in the data path; hot-row contention on org budgets.
- *Moesif as the counter* (enforce from analytics) — rejected: minutes of lag, sampling, and no API suited to per-request checks.
- *Per-gateway Redis* — rejected: one budget is one amount across all gateways (FR-019).

---

## R6. Redis configuration, activation and safety checks

**Decision**:
- Config reuses the `redis.*` shape of GC `advanced-ratelimit/policy-definition.yaml:387-523` (host, port, username/ACL, password, db, TLS incl. CA, server name, client cert/key for mTLS; dial/read/write timeouts; pool size) under `[policy_configurations.budgets_v1.redis]`. **Correction to design-doc-v1**: TLS and credentials *are* expressible in the rate-limit policy; they are only missing from the shipped `config.toml` template. No Sentinel/Cluster yet (GC `redis_clients.go:89`); a later phase.
- **Activation** (FR-022): budgets are switched on per gateway (`budgets.enabled`, default `false`). With `budgets.enabled = true` and no `budgets_v1.redis` block, the gateway-controller fails config validation at startup with a clear error ("budgets require [policy_configurations.budgets_v1.redis]"), and the policy factory refuses to initialise as a second guard. Never a silent no-op.
- **Data-loss warning**: on startup and every 10 minutes, best-effort `CONFIG GET maxmemory-policy` and `INFO persistence`; WARN (and set `budget_redis_config_unsafe` gauge to 1) when the policy is not `noeviction` or neither AOF nor RDB is enabled. Managed Redis may block `CONFIG`; a failure to read is itself logged at INFO, not treated as unsafe.
- **Shipped deployments** add Redis where budgets are used: `gateway/docker-compose.yaml` and `gateway/distribution/docker-compose.yaml` get a `redis` service (`--appendonly yes --maxmemory-policy noeviction`, password); the gateway Helm chart gets an optional bundled Redis (StatefulSet + PVC) or an external-Redis reference with a Secret for the password and TLS material.
- **Topology**: all gateways of an organization must share one Redis. Multi-region and hybrid deployments (cloud + self-hosted gateways for one org) need either a shared Redis reachable from all sites (latency) or per-region budgets — open question (plan risk 11).
- Optimisation if per-request round-trips prove costly: the `redis-local-async` flush-coordinator pattern (GC `algorithms/fixedwindow/redis_local_async.go`) for the charge step only; admission stays synchronous.

---

## R7. Streaming and usage reporting

**Findings**: The policy engine streams LLM responses chunk-by-chunk (Envoy ext_proc `FULL_DUPLEX_STREAMED`, `policy-engine/internal/kernel/execution_context.go:767-769,1972-2019`) and `llm-cost` prices on `EndOfStream` (GC `policies/llm-cost/llm_cost.go:64-88`). OpenAI/Azure chat-completions streams carry no usage unless the request sets `stream_options.include_usage` — nothing injects it today (GC `docs/azure-llm-cost/v1.0/docs/azure-llm-cost.md`), so such calls are priced at $0: **a free path around budgets**.

**Decision**:
- When **any** budget applies (enforce **or monitor** mode — monitor budgets must measure streamed spend accurately, otherwise a monitor-mode rollout under-reports exactly the traffic it is meant to size) and the request is an OpenAI-style chat completion with `stream: true`, the budget policy sets `stream_options.include_usage = true` via `UpstreamRequestModifications{Body}` (pattern: GC `policies/prompt-decorator/promptdecorator.go:480-527`) — FR-016a. The extra final usage chunk is standard OpenAI behaviour and harmless to clients.
- Check gates entry only; in-flight streams are never cut (FR-016).
- **Aborted streams (FR-016b)**: the SDK has no `OnStreamError` yet (`interface.go:157-165`, "planned … cleanup of token counters for billing"). Until it exists an aborted stream is neither charged nor marked. Tracked as a dependency; request the hook from the gateway team.

---

## R8. Identity and owner resolution

**Decision**: Resolve each owner dimension per request from:

| Dimension | Source in policy context |
|---|---|
| org | implicit — a gateway belongs to one org; org is resolved server-side from the gateway token |
| project | `SharedContext.ProjectID` — filled from the route's `project_id` metadata, which comes from the `gateway.api-platform.wso2.com/project-id` annotation (`gateway-controller/pkg/transform/restapi.go:63-98`). **Gap (verified 2026-10-09):** platform-api sets this annotation for REST APIs (`platform-api/internal/utils/api.go:543`) but not for LLM proxies (`generateLLMProxyDeploymentYAML` in `internal/service/llm_deployment.go` sets only `Name`), so `ProjectID` is empty for LLM proxy traffic today. Fix: add the annotation there; already-deployed proxies get it on redeploy. LLM providers are org-level, so direct provider traffic matches no project budget. |
| LLM proxy | `SharedContext.APIId` when `APIKind = LlmProxy` |
| LLM provider | `SharedContext.APIId` when `APIKind = LlmProvider`, or — on proxy traffic — the proxy's upstream provider (R4) |
| application | `AuthContext.Properties["ApplicationID"]` / `Metadata["x-wso2-application-id"]` (set by GC `api-key-auth/apikey.go:244-257`) |
| model *(added 2026-10-14)* | the upstream request model id — the provider template's `requestModel` extraction (as used by `gateway/system-policies/analytics/analytics.go`, metadata `aitoken:requestmodelid`) applied to the request as it is sent upstream, i.e. after any proxy model rewrite. **Prerequisite (gateway team, verify in Phase 1):** the budget policy must be able to read this id in the request phase (for the check) and keep it to the response phase (for the charge); today it is extracted by the analytics policy. The same id is used for check and charge so both hit the same counter. Pricing uses the response model id when present (`aitoken:modelid`), which can be a dated snapshot of the requested model (e.g. `gpt-4o-2024-08-06`); the budget does not match on it. No model id → budgets with a model owner do not apply |
| claim | `AuthContext.Subject` (for `sub`) and `AuthContext.Properties` (flattened claims, GC `jwt-auth/jwtauth.go:2728-2757`); walk `AuthContext.Previous` for multi-layer auth |

**Matching (revised 2026-10-14, R13)**: a budget applies when **every** owner matches — its context (org: always; project: `ProjectID` equals the budget's project) and each appended owner (values list: the request's value is any of the listed values; each: the request has any value for that dimension). Requests with no value for a dimension (e.g. direct provider traffic has no project; API-key traffic has no claims) are not covered by budgets that have an owner on that dimension.

**Override resolution** (2026-10-14, most specific wins, FR-003a): among the matched budgets, a budget is skipped when another matched budget has the same context, period and dimension set (claims compared by claim name) and is at least as specific on every dimension and more specific (a specific value where the other has "each") on at least one. Skipped budgets are not checked, not charged and evaluate no thresholds. Override sets are precomputed from definitions when budgets are (re)loaded, so per-request resolution is a lookup among the matched candidates.

**Order of evaluation** (revised 2026-10-14): after override resolution, budgets with more owners first, then smallest amount, then budget id. Every remaining budget is evaluated (needed for the latest-reset `Retry-After`) and charged.

**"Each" owners**: `valueKey` = the request's values for the budget's "each" owners, in canonical order (project id, provider id — the route's provider, or a proxy's upstream provider per R4 —, proxy id, a hash of the model id, application id, then a hash of each claim value by claim name), joined with `:`; `_` when the budget has no "each" owner (data-model §5).

---

## R9. Warnings and notifications via Moesif (revised 2026-10-09)

**Decision** (feature owner, 2026-10-08 / 2026-10-09): The API Platform sends no notifications, and platform-api publishes nothing to Moesif. Threshold crossings are **detected by the gateway** and carried on the request's analytics event. Background in [`.research-moesif.md`](./.research-moesif.md).

- **Detection**: after `INCRBY` returns the new total `T` for a counter with amount `A` and thresholds `t₁ < … < tₙ`, let `h` = the highest `tᵢ` with `T ≥ tᵢ% × A`. If `h` exists and the gateway's local cache does not already know `h` is marked for this `(budget, period, value, amountVersion)`, it runs `SET budget:v1:{<uuid>}:<period>:<value>:thr:<h>:<amountVersion> 1 NX EXAT <periodEnd+1d>`. Exactly one gateway wins; the winner adds the crossing to the request's analytics event (`budget_threshold_crossed = h`, contract §4.1). In the same pipeline it also sets the markers of lower reached thresholds with `NX` without emitting them, so a request that jumps over several thresholds (or an amount change that leaves spend above several) emits only the highest (FR-009).
- **Re-arming**: the marker key includes the budget's `amountVersion`, which platform-api increments on every amount change and ships with the definition. A new amount therefore re-arms every threshold with no reset message (replaces the former `resetNotifiedThresholds`). Name or description edits do not change `amountVersion`, so they do not re-fire warnings. Adding a threshold creates a new marker key naturally.
- **Delivery**: one organization-wide Moesif alert rule, "BUDGET: Threshold crossed", on events where `metadata.budget_threshold_crossed` exists (group by `metadata.budget_name`, `metadata.budget_threshold_crossed` and `metadata.budget_owner_values`; count > 0 over 15 minutes) delivers to Slack / email / webhook. Additional rules are optional, only to route budgets to different recipients. Creating or changing budgets needs no Moesif change. Budget events must be excluded from Moesif dynamic sampling — otherwise the single event carrying a crossing may be dropped.
- **Delivery guarantee**: exactly-once detection (Redis `NX`), at-most-once delivery: if the analytics event is lost after the marker is set (analytics disabled on that gateway, publisher failure, sampling), the warning is lost. Documented; a budget whose warning was lost still shows its % used on the Budget Usage card (FR-024e).
- **Every AI request event** carries flat budget fields: `budget_id`, `budget_name`, `budget_owners` (summary, e.g. `project=search; provider=each; sub=each`), `budget_owner_values` (the caller's resolved values for the "each" owners, e.g. `provider=OpenAI; sub=alice@acme.com`; empty when none), `budget_period`, `budget_amount`, `budget_spend`, `budget_percent_used`, `budget_refused`, `budget_monitor_would_refuse`, `budget_threshold_crossed` (most constraining budget, or the crossing budget when a threshold is crossed; full list in the `budgets` JSON field) and `llm_cost_status` (FR-024a, FR-025). The request cost is the existing `aiMetadata.llmCost` (`policy-engine/internal/analytics/dto/aiMetaData.go`), published as `metadata.aiMetadata.llmCost`; there is no flat `llm_cost` field.
- **Where the fields live in the Moesif event (verified 2026-10-13)**: all budget fields are published under the event's `metadata` object (e.g. `metadata.budget_threshold_crossed`, `metadata.budget_spend`). This needs a code change: the Moesif publisher (`policy-engine/internal/analytics/publishers/moesif.go`) builds `metadata` from an allow-list of `dto.Event.Properties` keys, and `prepareAnalyticEvent` (`internal/analytics/analytics.go`) reads policy-written `analytics_data` keys but copies only known ones (cost, guardrail, user id, auth) into `Properties`. Both must forward the `budget_*`, `budgets` and `llm_cost_status` keys with their JSON types (tasks T114/T115). `llm_cost_status`, `budget_check_skipped`, `budget_id` and `budget_name` are emitted from Phase 1 (unpriced and fail-open marking are MVP); the rest in Phase 2.
- **Presentation in Moesif** (feature owner, 2026-10-09/13): **one** new **Budget Usage** card on the **existing LLM Analytics page** of the embedded Moesif analytics (no new page, section or sidebar item) — per budget: name, amount (`max(metadata.budget_amount)`), spend this period (`max(metadata.budget_spend)`), % used (`max(metadata.budget_percent_used)`), refused (count where `metadata.budget_refused = true`), grouped by `metadata.budget_name` and `metadata.budget_period` — plus a **Budget** filter next to the existing API filter (on `metadata.budget_id` / `metadata.budget_name`). When the filter selects a budget with "each" owners the card lists one row per `metadata.budget_owner_values` (e.g. per user, or per user and provider). No budget-vs-actual dashboards or per-budget views (FR-024e).
- **Links to Moesif**: the Insights page currently hardcodes `https://www.moesif.com/wrap/basic` (`portals/ai-workspace/src/pages/appShell/appShellPages/insights/Main.tsx`). A new AI Workspace runtime config key `APIP_AIW_MOESIF_ANALYTICS_URL` (via `getEnvOrDefault` in `src/config.env.ts`) holds the embedded analytics base URL; the Budgets page links each budget to the LLM Analytics page under it with the Budget filter set. **Open, to confirm with the analytics team**: the LLM Analytics path and the Budget filter query format (plan risk 12); until then the link opens the base URL.
- **Budget changes** (amount, mode, status incl. owner-deleted auto-disable) are recorded **only in the audit log** with before/after details (FR-009a, FR-026). The former `Budget Changed` and `Budget Threshold Crossed` Moesif Actions from platform-api are removed, and with them the open question of platform-api access to the org's Moesif application id.

**Alternatives considered**:
- *platform-api detects crossings from synced totals and publishes a Moesif Action* (2026-10-08 design) — rejected 2026-10-09: there is no longer any spend in platform-api.
- *Gateway emits a marker without coordination* — rejected: every replica crossing at once would duplicate warnings; the Redis `NX` marker makes it exactly-once.
- *Moesif computes thresholds itself* — rejected: Moesif calendar alerts evaluate only after the period ends and grouped thresholds are a single fixed number, so Moesif cannot compute "≥ 80 % of *this* budget"; it can, however, alert on the gateway-provided `budget_percent_used`.

---

## R10. Retry-After and error contract

**Decision**: 429 with code `BUDGET_EXHAUSTED` (distinct from rate-limit 429s), body naming the budget, and `Retry-After` in seconds = time to the reset of the exhausted budget (latest reset if several are exhausted). Allowed responses carry `Budget-Id` / `Budget-Remaining` / `Budget-Reset` (and `Budget-Monitor` for monitor-mode would-refuse) for the most constraining budget, computed from the Redis counters read at admission. Redis unreachable under `failure_mode = "closed"`: 503 `BUDGET_CHECK_UNAVAILABLE`. See [contracts/gateway-budget-enforcement.md](./contracts/gateway-budget-enforcement.md).

**Header naming** (analysis remediation 2026-10-09): the earlier `x-wso2-budget-*` names were renamed to the neutral `Budget-Id`, `Budget-Remaining`, `Budget-Reset`, `Budget-Monitor`. `.claude/rules/error-handling.md` directive 2 forbids generating vendor/infrastructure-specific headers in client-facing responses and allows only standard or platform-agnostic headers; a vendor-branded `x-wso2-` prefix also reveals the gateway implementation. Unprefixed names follow RFC 6648 (no `X-`). The internal `x-wso2-internal-loopback` request header (research R4) is not client-facing and is unchanged.

**Rationale**: Calendar periods make the reset exact; no surveyed competitor sends `Retry-After` on budget rejections (Vercel 402, Portkey 412, LiteLLM 422). 429 kept (not 402/403) because standard SDKs already treat it as a stop/back-off signal, and the distinct code lets them stop retrying.

---

## R11. platform-api conventions

- Public API under `/api/v0.9` (`platform-api/internal/constants/constants.go:170-177`), spec in `platform-api/resources/openapi.yaml`, models-only codegen (`oapi-codegen.yaml`), hand-written handlers wrapped in `middleware.MapErrors`. Reference resource: subscription plans (`openapi.yaml:6497-6658`; `internal/handler/subscription_plan_handler.go`; `internal/service/subscription_plan_service.go`; `internal/repository/subscription_plan_repository.go`).
- Pagination `{list, count, pagination{total, offset, limit}}`; error `{status, code, message, errors?, details?, trackingId?}`; codes in `internal/apperror/codes.go` + `catalog.go`.
- Schemas: `schema.postgres.sql`, `schema.sqlite.sql`, `schema.sqlserver.sql`, idempotent DDL re-applied at startup; rules R0–R10 (`.agents/skills/designing-db-schemas/references/api-platform-db-schema-rules.md`): new tables only, no `CHECK` enums, `SMALLINT` booleans, `data_version`, explicit `ON DELETE`, index org/status/FK columns, all dialects identical. **One** new table (`budgets`) since 2026-10-09.
- RBAC: new scopes `ap:budget:manage` (ap_admin) and `ap:budget:read` (ap_operator; ap_viewer open — plan risk 10), declared in `openapi.yaml` security schemes (startup fails otherwise) and `resources/role-to-scope-mapping.yaml`.
- Audit: `auditRepo.Record("CREATE"|"UPDATE"|"DELETE", uuid, "budget", org, actor)` (pattern `subscription_plan_service.go:112,216,251`) has no place for before/after values (FR-026, FR-009a), so budgets use a new `RecordWithDetails(..., details []byte)` writing a new nullable `audit.details` column (data-model §3; additive column on a shipped table with a guarded per-dialect `ALTER`).
- Internal gateway endpoints: `internal/handler/gateway_internal.go:1089-1113`, `authenticateRequest` → (org, gateway); add `GET /api/internal/v1/budgets` to `SkipPaths` (`config/default_config.go:58-75`) and to `resources/gateway-internal-api.yaml`. No inbound usage endpoint or websocket message exists any more.
- UI: `portals/ai-workspace` (React 19 + MUI 7). New `src/pages/appShell/appShellPages/budgets/`, route in `src/App.tsx`, sidebar item in `AppSidebar.tsx`, client `src/apis/budgetApis.ts`. Configuration only; usage links to Insights / Moesif.

---

## R12. Smaller decisions

| Topic | Decision |
|---|---|
| Money | Integer nano-USD (`BIGINT` in the `budgets` table; integer `INCRBY` in Redis); API uses decimal strings (`"1500.00"`) to avoid float rounding. |
| Periods | UTC; labels `2026-10-08` (day), `2026-W41` (ISO week, Monday), `2026-10` (month). A charge belongs to the period in which the response finished. |
| Unpriced requests | Charge 0, never block; the request event carries `llm_cost_status = not_calculated` (FR-015, FR-024a); no counter anywhere. Future: org option to block unpriced models under enforce budgets; custom per-model prices. |
| Deleted budgets | Hard delete of the definition; audited. Redis counters and markers are left to expire (TTL period end + 1 day); a re-created budget gets a new uuid, so it never sees old counters. Past spend is in Moesif. |
| Amount change | Never touches the counter. `amountVersion` increments → every threshold re-armed; if spend already exceeds several thresholds, only the highest is emitted on the next charge. Audited with before/after (no Moesif action). |
| Provider vs proxy | Separate owner dimensions `LLM_PROVIDER` and `LLM_PROXY` (feature owner, 2026-10-08; kept as dimensions 2026-10-14): they mean different things (org-wide vendor connection vs project-owned entry point, shown separately in the sidebar), and a combined "each provider/proxy" dimension would count proxy traffic twice. Both may appear on one budget. |
| Budgets UI | **Revised 2026-10-09**: configuration only — list, create, edit, disable, delete — with "See usage in Insights / Moesif" links. No meters, "used this period", per-value table, unpriced count or Exhausted/near-limit status. Rejected: showing spend by reading Redis from platform-api (control plane would need a data-plane Redis connection; hybrid deployments may have none) or by syncing spend (R5 alternative). |
| Who manages | `ap:budget:manage` (ap_admin by default), `ap:budget:read` (ap_operator); grantable to other roles via role-to-scope mapping. Budgets created in project mode need the same permission in v1; per-project delegation is future work. |
| Overshoot | Request that crosses the line completes and is charged; requests already in flight on any gateway complete; new requests refused immediately on every gateway. Pre-estimating from `max_tokens` is future work. |
| History | Not kept in the API Platform: no spend at all in the control plane (2026-10-09), no budget-change history beyond the audit log. History and reconciliation are in Moesif. |
| Per-value budgets in UI | **Revised 2026-10-09**: shown as configuration only ("each user gets $25 / day"); per-value spend in Moesif (Budget Usage card rows by `metadata.budget_owner_values` when the Budget filter selects the budget). Removes `GET /budgets/{id}/values` and `perValueSummary`, and with them the exposure of claim values (e.g. emails) to `ap:budget:read` holders. |
| Request, refused and unpriced counts | Not counted by the platform: enforcement, warnings and refusals work from spend; every request (refused or not, priced or not) is an analytics event. |
| Existing `llm-cost-based-ratelimit` | Unchanged and independent (FR-029). Migration guidance only. It can share the same Redis instance (different key prefix). |
| Concurrency | No pre-authorisation/reservation in v1 (no competitor documents it; LiteLLM's claimed reservation is not in its official docs). |

---

## R13. Budget owners: context plus appended owners (2026-10-14)

**Decision** (feature owner, 2026-10-14): A budget's first owner is the AI Workspace context it is created in — the organization (org mode) or a project (project mode). Any number of further owners can be appended; dimensions are project (org mode only), LLM provider, LLM proxy, model (added later the same day), application and token claim (per claim name). When budgets overlap, the most specific wins (FR-003a). All owners are ANDed. Each appended owner is a specific value or "each". Owners are immutable after creation. Stored as a JSON column on `budgets` (data-model §1.1).

**Rationale**:
- One owner per budget could not express the most common FinOps asks: different limits per provider or per project for the same user ("alice gets $x on OpenAI and $y on Anthropic"; "each user gets $10 a month in project-a"). Owners as conditions express these without a new scope type per combination, and new dimensions (e.g. model) are one more owner kind.
- Matches the AI Workspace's two modes, so a budget created inside a project is naturally scoped to it.
- Comparable to composable dimensions in Portkey (conditions plus group-by), Cloudflare and TrueFoundry rules.
- Runtime cost stays bounded: one counter per active combination (lazy keys) and one pipelined `INCRBY` per matching budget; the gateway indexes budgets by owner value rather than scanning.

**Not decided / follow-ups**:
- **Cap on owners and on values per owner**: none in v1; a cap (e.g. 3 owners, 2 "each" owners, a maximum number of values per owner) may be added if gateway matching cost (SC-008, 5 ms p95) or Redis key cardinality requires it.

**Decided later on 2026-10-14 (feature owner)**:
- **Override: most specific wins.** Budget B overrides A for a request when both match, same context, same period, same dimension set, and B is at least as specific on every dimension and more specific on one (refined below for multi-value owners: a values list is narrower than each, and a subset of values is at least as narrow). The overridden budget is skipped (not checked, not charged). Different dimension sets, contexts or periods still stack, so org and project caps stay authoritative; partial specificity stacks. Mode does not affect precedence, so a monitor budget can lift enforcement for one value — the API returns `overrides` / `overriddenBy` and warns on create/edit. *Rationale*: "everyone $10, alice $50" is the most common exception request (Vercel defaults with custom overrides, TrueFoundry per-entity overrides, LiteLLM team-member budgets); requiring the same dimension set keeps the rule predictable and computable from definitions only. *Alternative not adopted*: an explicit `override: true` flag or priority number — more control, but another field to reason about.
- **Model is an owner dimension.** Value = the upstream request model id (after proxy rewrite), exact and case-sensitive; values list or each. *Rationale*: steering users to cheaper models is the core FinOps use; matching the id the provider is actually called with means a proxy alias cannot be used to escape a model budget. *Alternative*: the client-requested model/alias (Portkey matches the model name in the incoming request body) — easier for users to recognise, but different aliases for the same upstream model would split spend, and a rewrite could escape the budget. Depends on the gateway prerequisite above.
- **Multi-value owners (any of).** An appended owner carries `values` — one or more specific values, any of which matches — or `each`. Listed values share one allowance (one counter), so "OpenAI and Anthropic together, $2,000 a month" is one budget (`LLM_PROVIDER ∈ {OpenAI, Anthropic}`). AND across owners, OR within an owner. For most specific wins, a values list is narrower than `each`, and a list is at least as narrow as another when it is a subset of it (strictly when a proper subset): `{OpenAI}` $500 overrides `{OpenAI, Anthropic}` $2,000 for OpenAI traffic. If any listed resource is deleted the budget is disabled (`OWNER_DELETED`), as before. *Rationale*: without lists, a shared pool across two providers, models or teams needs one budget per value, which gives separate pools rather than one. *Precedent*: Portkey policy condition values can be arrays (OR within a condition). *Alternative not adopted*: OR across whole owners (any of several owner sets) — far harder to explain, index and override; separate budgets cover it. *Open*: whether to drop a deleted value and keep the budget active when other values remain; "each" over a subset of values ("each of OpenAI, Anthropic") is out of scope.

**Alternatives considered**:
- *Six owner scope types with shared / separate for each* (2026-10-08) — superseded: one owner per budget.
- *An optional filter (provider/proxy/model) on a single-owner budget* — subsumed: an appended owner with a values list is that filter.
- *A child `budget_owners` table* — FKs and indexed lookups, but a second new table against the "avoid new tables" guidance; revisit if per-org budget counts grow large.

