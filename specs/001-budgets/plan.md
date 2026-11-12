# Implementation Plan: Budgets

**Branch**: `feature/budgets` | **Date**: 2026-10-08 | **Revised**: 2026-10-09 (architecture change by the feature owner); 2026-10-13 (Moesif presentation, re-analysis remediation); 2026-10-14 (budget owners: context plus appended owners, model owner, most-specific-wins override, multi-value owners, research R13) | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-budgets/spec.md`, design-doc-v1, the AI gateway budgeting market analysis, and the feature owner's architecture decisions of 2026-10-09.

## Summary

Add organization-level **Budgets** — a money ceiling for a set of owners over a UTC calendar period (day/week/month), with monitor/enforce modes and warning thresholds. A budget's first owner is the AI Workspace context it is created in (the organization, or a project); further owners — project, LLM provider, LLM proxy, model, application, token claim — can be appended, each a list of one or more specific values (any of which matches; they share one allowance, e.g. provider ∈ {OpenAI, Anthropic}) or "each", and a request counts only when it matches all of them; when a specific-value budget and a broader "each" budget with the same context, period and dimensions both match, the most specific wins (research R13, FR-003a).

Technical approach (revised 2026-10-09):
1. **platform-api** owns budget **definitions only** (one table, `budgets`; new REST resource). It stores no spend and receives nothing from gateways.
2. Definitions reach gateways **as data, not policy parameters** — the subscription-plan pipeline (websocket `budget.*` event → controller DB → LazyResources xDS → read at request time; resync via `GET /api/internal/v1/budgets`), so changes apply without redeploying anything.
3. A new **prepended, streaming-capable system policy** checks headroom in the request-body phase (after auth), injects `include_usage` for streamed chat completions, and charges `x-llm-cost` at end of response.
4. **Spend is counted only in a shared Redis, which is required for budgets.** Every replica of every gateway of the org reads counters at admission and atomically `INCRBY`s integer nano-USD counters per budget × period (× value) after the response; keys expire at period end + 1 day. Overshoot = in-flight requests only. Redis unreachable → refuse (configurable fail-open).
5. **Threshold warnings are detected by the gateway** (`SET NX` marker in Redis, so exactly one gateway emits each crossing) and carried on that request's analytics event (Moesif event `metadata.budget_*`) to **Moesif**, where one org-wide alert rule ("BUDGET: Threshold crossed") delivers them. Usage is shown by one **Budget Usage** card and a **Budget** filter on the existing LLM Analytics page of the embedded Moesif analytics (FR-024e). platform-api publishes nothing to Moesif; budget changes go to the audit log.
6. The **AI Workspace Budgets page is configuration only** (list / create / edit / disable / delete) with "See usage in Insights / Moesif" links to the LLM Analytics page with the Budget filter set — no spend, meters or near-limit state.

Decisions and alternatives: [research.md](./research.md) (R5 records why the 2026-10-08 local-counter + control-plane-sync design was dropped).

## Technical Context

**Language/Version**: Go 1.24+ (platform-api, gateway-controller, policy engine, policies); TypeScript / React 19 (AI Workspace)

**Primary Dependencies**: Envoy ext_proc + policy-engine SDK `sdk/core/policy/v1alpha2`; go-control-plane xDS (LazyResources); websocket channel platform-api ⇄ controller; oapi-codegen (models); go-redis (reusing the advanced-ratelimit client configuration shape); MUI 7

**Storage**: platform-api — PostgreSQL / SQLite / SQL Server (`budgets` + nullable `audit.details`); gateway-controller DB (`budgets` copy); **shared Redis (required where budgets are enabled)** for counters and threshold markers

**Testing**: Go unit tests (miniredis for counter/marker logic); platform-api integration tests; gateway integration tests (`gateway/it`, godog features, with the existing IT `redis` service) incl. a streaming LLM feature, a two-replica shared-counter feature, Redis-down / fail-open features and a budget + `llm-cost-based-ratelimit` coexistence feature (FR-029); a policy-engine phase-ordering test that pins research R3 (every request-header policy (auth) runs before any request-body policy, and the prepended budget policy's response/stream handler runs after `llm-cost` has set `x-llm-cost`); AI Workspace component tests; quickstart scenarios ([quickstart.md](./quickstart.md)) incl. SC-006 reconciliation (Redis counter vs Moesif sum) and SC-007 timing

**Target Platform**: Linux containers — Docker Compose and Kubernetes (Helm); SaaS (cloud control plane) and self-hosted/hybrid gateways

**Project Type**: Distributed web service (control plane + gateway data plane + web UI)

**Performance Goals**: budget evaluation < 5 ms p95 added latency including the Redis round-trips (SC-008; one pipelined read at admission, one pipelined write at response end); definitions propagate to all gateways < 60 s (SC-002); threshold crossings visible in Moesif < 2 min (SC-007)

**Constraints**: Redis required for budgets (refuse to activate without it); no redeploy on budget change; must not break token streaming; DB rules R0–R10 (additive-only; see `.claude/rules/db-schema-changes.md`); overshoot limited to in-flight requests; fail-closed when Redis is down unless configured open; no spend in the control plane

**Scale/Scope**: up to ~1,000 budgets/org; budgets with "each" owners up to ~10k distinct value combinations/period (≈ 10–60k small Redis keys; cardinality is the product of the "each" dimensions' active values); no limit on owners per budget in v1 (risk 13); gateways with up to ~20 runtime replicas; Redis ops per budgeted request: 2 pipelined round-trips (+1 `SET NX` only when a threshold is reached)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

N/A — `.specify/memory/constitution.md` has not been ratified for this repository (still the template). Repository rules used instead as gates:

| Gate | Source | Status |
|---|---|---|
| DB changes additive only; all dialects identical; no CHECK enums | `.claude/rules/db-schema-changes.md` (DB rules R0–R10) | ✅ one new table (`budgets`), plus one nullable column on the shipped `audit` table with a guarded per-dialect `ALTER` (DB rule R0-UPGRADE-PATH; [data-model.md §3](./data-model.md)). Architecture guidance "avoid new tables" → open item risk 8a |
| Every `ap:` scope declared in openapi.yaml and role map | `platform-api/resources/role-to-scope-mapping.yaml` header | ✅ `ap:budget:manage`, `ap:budget:read` |
| xDS / control-plane security | `.claude/rules/go-control-plane-xds-security.md` | ✅ reuse existing authenticated channels only (websocket + gateway-token internal GET); **no new controller endpoint** (usage ingest removed 2026-10-09) |
| AuthN/AuthZ, error handling, SSRF | `.claude/rules/authentication_authorization.md`, `error-handling.md`, `ssrf-prevention.md` | ✅ Redis is an operator-configured target (no user-supplied hosts); Redis credentials/TLS material from Secrets/env, never logged; read-only mode enforced in the service/repository layer (GO-AUTH-015); client-facing budget headers are platform-neutral `Budget-*` and 503 bodies carry no Redis detail (error-handling directive 2) |

Post-design re-check: ✅ no violations; Complexity Tracking empty.

## Architecture

```text
 AI Workspace (portals/ai-workspace)
   Budgets page (configuration only) ──REST /api/v0.9/budgets──┐        "See usage" links ──► Moesif LLM Analytics (Budget filter)
                                                               ▼
 ┌──────────────────────────── platform-api ─────────────────────────────┐
 │ budgets (definitions, amount_version) · audit.details (before/after)  │
 │ BudgetService ── broadcast budget.* ──┐   GET /api/internal/v1/budgets │
 └───────────────────────────────────────┼────────────────▲───────────────┘
                     WS budget.* (defs)  │                │ resync on connect
                                         ▼                │
 ┌──────────────────────── gateway-controller ────────────┴───────────────┐
 │ budgets (copy) → LazyResources "Budget" · config validation:           │
 │ budgets.enabled ⇒ [policy_configurations.budgets_v1.redis] required    │
 └──────────────┬─────────────────────────────────────────────────────────┘
     xDS (defs) │
                ▼
 ┌─ gateway-runtime replica × N  (every gateway of the org) ──────────────┐
 │ Envoy ─ext_proc─► policy engine chain:                                  │
 │  [wso2_apip_sys_budget][analytics][auth]…[llm-cost-based-ratelimit][llm-cost]
 │  req body : match budgets → GET counters → 429 | 503 | inject include_usage ──┐
 │  resp     : (stream, runs last) x-llm-cost → INCRBY → threshold SET NX ───────┤
 └──────────────── analytics event metadata.budget_* (+ budget_threshold_crossed) ──► Moesif ── org-wide alert rule ──► Slack / email / webhook
                                                                                ▼
                                       shared Redis (required; noeviction + AOF; TLS/auth)
                                       budget:v1:{uuid}:<period>:<valueKey>          (counter, TTL period end + 1 d)
                                       budget:v1:{uuid}:<period>:<valueKey>:thr:<t>:<amountVersion>  (marker)
                                       valueKey = the request's values for the "each" owners, canonical order (or "_")
                                       (overridden budgets — FR-003a — are skipped: no read, no INCRBY)
```

## Changes by component

### platform-api
- `resources/openapi.yaml`: Budgets paths/schemas + scopes ([contract](./contracts/budgets-api.openapi.yaml), definitions only); `resources/role-to-scope-mapping.yaml`: `ap:budget:manage` → ap_admin; `ap:budget:read` → ap_operator (ap_viewer: open, risk 10).
- `internal/database/schema.{postgres,sqlite,sqlserver}.sql` (+ `schema.sql`, fixture `init-platform-api-db.sql`): `budgets` table incl. `context_type`, `project_uuid`, `owners` (JSON) + `owners_hash`, `amount_version` and `status_reason` ([data-model.md §1](./data-model.md)).
- **Audit before/after (FR-026, FR-009a)**: nullable `details` column on the shipped `audit` table — `BYTEA` (Postgres) / `TEXT` (SQLite) / `NVARCHAR(MAX)` (SQL Server) — in each `CREATE TABLE` body plus a guarded per-dialect `ALTER` for existing databases (Postgres `ADD COLUMN IF NOT EXISTS`, SQL Server `COL_LENGTH` guard, SQLite `pragma_table_info` check in `internal/database/connection.go`) ([data-model.md §3](./data-model.md)). `internal/repository/audit.go`: add `RecordWithDetails(..., details []byte)` (existing `Record` unchanged, writes `NULL`); `BudgetService` passes JSON `{before, after}`. This is the only record of budget changes.
- **Read-only mode** (GO-AUTH-015): `BudgetService` writes (create/update/delete/owner-deleted disable) refuse in read-only mode, and the budget repository's write methods refuse too, so every entry point (REST handler, internal callers) is covered — not only the `*_readonly.go` handlers.
- `internal/model/budget.go`, `internal/repository/budget_repository.go`, `internal/service/budget_service.go` (CRUD, owner validation and canonical ordering, `owners_hash`, `amount_version` increment on amount change, audit, `budget.*` broadcast), `internal/handler/budget_handler.go`; wiring in `internal/server/server.go`; error codes in `internal/apperror/{codes,catalog}.go`.
- `budgets.status_reason` (`OWNER_DELETED`) set when the context project or a project / LLM provider / LLM proxy / application named by an owner is deleted (the service scans the organization's budgets' owners — no FK on the JSON column); returned read-only as `statusReason`; every status change (manual or automatic) is audited with before/after (FR-009a). No Moesif action.
- `internal/handler/gateway_internal.go`: `GET /api/internal/v1/budgets` (+ `SkipPaths`, `resources/gateway-internal-api.yaml`).
- `internal/service/gateway_events.go`: `BroadcastBudgetEvent`.
- **Removed 2026-10-09**: `budget_period_spend`, `budget_sync_cursors`, `BudgetUsageService`, websocket `budget.usage` handling, `budget.totals`, threshold detection, Moesif Actions publisher (`Budget Threshold Crossed`, `Budget Changed`), purge job, `currentPeriod` / `perValueSummary` / `asOf` / `state` filter / `GET /budgets/{id}/values`.

### gateway-controller
- `pkg/controlplane/client.go`: handle `budget.created|updated|deleted`; resync on connect.
- Controller DB: `budgets` table; `common/eventhub` `EventTypeBudget` for replica sync.
- `pkg/lazyresourcexds`: publish `ResourceType = "Budget"`; rebuild from DB on startup.
- `pkg/config`: `budgets.enabled` (default `false`) and startup validation — enabled without `[policy_configurations.budgets_v1.redis]` fails with a clear error (FR-022).
- `pkg/utils/system_policies.go`: add `wso2_apip_sys_budget` to `defaultSystemPolicies` when `budgets.enabled`.
- **Removed 2026-10-09**: internal usage ingest endpoint, aggregator, outbox, `budget.usage` sender.

### Gateway runtime / policies
- `gateway/system-policies/budget/`: new system policy (request `BodyModeBuffer`, response `BodyModeStream` + `StreamingResponsePolicy`), config (`failure_mode`, `redis.*`), Redis client (TLS/mTLS/auth), config-safety check, budget matcher (match index keyed by owner dimension value — context project, provider, proxy, model, application, claim name/value — then an AND check of every owner; no scan of all budgets per request), override resolution (override sets precomputed from definitions on load; matched budgets overridden by another matched budget are skipped — FR-003a), upstream model id capture (request phase, kept to the response phase for the charge), `valueKey` builder from "each" owners, Redis counter (pipelined read; `INCRBY` + `EXPIREAT`), threshold markers (`SET NX EXAT`, highest-only, local marker cache), loopback skip, `include_usage` injection, 429/503 builders.
- `system-policies/system-build-lock.yaml`: include the policy.
- `gateway/system-policies/analytics` + `policy-engine/internal/analytics`: emit `budget_*` fields incl. `budget_threshold_crossed` and `llm_cost_status` ([contract §4.1](./contracts/gateway-budget-enforcement.md)); exempt budget events from Moesif sampling. **Where the fields land (verified in code)**: the Moesif publisher (`policy-engine/internal/analytics/publishers/moesif.go`) builds the Moesif event `metadata` map from an **allow-list** of `dto.Event.Properties` keys (`aiMetadata`, `aiTokenUsage`, guardrail keys, application/subscription fields, …), and `prepareAnalyticEvent` (`internal/analytics/analytics.go`) reads policy-written `analytics_data` keys into a local map but copies only known keys into `Properties`. Policy-written budget keys are therefore **not** forwarded today: both files need a change (copy `budget_*`, `budgets`, `llm_cost_status` from `analytics_data` into `Properties` with their JSON types, and add them to the Moesif `metadata` map), after which they appear as `metadata.budget_*` (e.g. `metadata.budget_threshold_crossed`). The request cost is already published as `metadata.aiMetadata.llmCost` (`internal/analytics/dto/aiMetaData.go`); there is no flat `llm_cost` field. The publisher change and `llm_cost_status` / `budget_check_skipped` / `budget_id` / `budget_name` emission are Phase 1 (unpriced marking and fail-open marking are MVP); the remaining `budget_*` fields are Phase 2.
- **Upstream asks** (gateway team / SDK): `OnStreamError` hook (FR-016b); upstream provider UUID on LLM proxy route metadata (research R4). Both tracked as GitHub issues referenced here and in the PR description — no deferral comments in code.
- **Removed 2026-10-09**: local counters, sync client, replica `sourceId` / `POD_NAME` env, default/strict mode switch.

### Deployments
- `gateway/docker-compose.yaml` and `gateway/distribution/docker-compose.yaml`: `redis` service (`redis-server --appendonly yes --maxmemory-policy noeviction --requirepass …`, volume), `budgets.enabled = true` and the `budgets_v1.redis` block in the shipped `gateway/configs/config.toml`.
- Helm `kubernetes/helm/gateway-helm-chart`: `gateway.budgets.enabled`, `gateway.budgets.redis` (external host/port/db/username, `existingSecret` for password and TLS material, `failureMode`) and an optional bundled Redis (`gateway.budgets.redis.bundled.enabled`: StatefulSet + PVC + Service with AOF and `noeviction`) rendered into `templates/gateway/gateway-config.yaml`.
- IT: reuse the `redis` service already in `gateway/it/docker-compose.test.yaml`.

### AI Workspace (`portals/ai-workspace`)
- `src/apis/budgetApis.ts`; `src/pages/appShell/appShellPages/budgets/` — **configuration only**: Budgets page in **both org mode and project mode** (org mode lists all budgets with a Context column; project mode lists that project's budgets); list (name, owners summary e.g. "Project-a · each user · OpenAI", amount, period, mode, thresholds, status label Active / Monitor / Disabled with `statusReason`), create/edit dialog with description, the first owner as a fixed chip (organization or the current project) and an **+ Add owner** row builder (dimension; value from searchable lists for projects, LLM providers/proxies and applications from existing APIs, or **Each**; free text only for claim name/value; free text for a model id; dimensions already used are disabled, project hidden in project mode; owners read-only on edit;, no override notice or lists (overrides are not shown in the UI)), disable/enable, delete, and a "See usage in Insights / Moesif" link per budget (org's Moesif URL, `budget_id` filter where supported). No meters, spend, per-value table or near-limit state. Route in `src/App.tsx`; sidebar item in `AppSidebar.tsx` (near Insights). Create / Edit / Disable / Delete shown only with `ap:budget:manage`.
- **Analytics link (U1)**: the Insights page hardcodes `https://www.moesif.com/wrap/basic` (`src/pages/appShell/appShellPages/insights/Main.tsx`). Add AI Workspace runtime config key `APIP_AIW_MOESIF_ANALYTICS_URL` (read via `getEnvOrDefault` / `window.__RUNTIME_CONFIG__` in `src/config.env.ts`, default the current URL) as the embedded analytics base URL, used by both the Insights page and the Budgets "See usage" link. The link opens the **LLM Analytics** page with the **Budget** filter set to the budget. **Open (confirm with the analytics team)**: the LLM Analytics page path under that base URL and the Budget filter query format (e.g. `?budget_id=<handle>` vs a Moesif filter expression on `metadata.budget_id`); until confirmed, the link falls back to the base URL.

### Analytics (Moesif templates)
- **Budget Usage card** (FR-024e) on the **existing LLM Analytics page** of the embedded Moesif analytics — no new page, section or sidebar item. Segmentation table grouped by `metadata.budget_name` and `metadata.budget_period`: `max(metadata.budget_amount)`, `max(metadata.budget_spend)`, `max(metadata.budget_percent_used)`, count of events where `metadata.budget_refused = true`. When the Budget filter selects a budget with "each" owners, rows are additionally grouped by `metadata.budget_owner_values` (one row per value or combination, e.g. per user and provider).
- **Budget filter** next to the existing API filter: filters events on `metadata.budget_id` (values labelled by `metadata.budget_name`); applies to the whole page.
- **Alert rule** "BUDGET: Threshold crossed" — **one per organization**: events where `metadata.budget_threshold_crossed` exists, grouped by `metadata.budget_name`, `metadata.budget_threshold_crossed` and `metadata.budget_owner_values`, count > 0 over 15 minutes. Additional rules are optional and only route budgets to different recipients.
- Delivered as templates under `docs/gateway/budgets/moesif/` with the user guide; mockups: `budgets-design-doc/mockups/moesif/` (m1 card, m2 per-user filter, m3 alert rule). Whether they can be provisioned through the Moesif management API is open (risk 12).

### Docs
- User guide: concepts, owners (context, appended owners incl. model, "each", AND semantics, stacking, most specific wins), periods, overshoot (in-flight only), monitor mode, **Redis requirement** (one Redis per org's gateways, TLS/auth, `noeviction`, AOF persistence, what happens on data loss and on outage, `failure_mode`), Moesif alert rule "BUDGET: Threshold crossed" on `metadata.budget_threshold_crossed`, viewing usage on the LLM Analytics page (Budget Usage card + Budget filter), migration from `llm-cost-based-ratelimit`.

## Project Structure

### Documentation (this feature)

```text
specs/001-budgets/
├── spec.md
├── plan.md              # this file
├── research.md          # Phase 0
├── data-model.md        # Phase 1
├── quickstart.md        # Phase 1
├── contracts/
│   ├── budgets-api.openapi.yaml
│   ├── gateway-definitions.md
│   └── gateway-budget-enforcement.md
├── checklists/requirements.md
└── tasks.md             # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
platform-api/
├── resources/openapi.yaml, gateway-internal-api.yaml, role-to-scope-mapping.yaml
└── internal/{database,model,repository,service,handler,apperror,server}/   # budget files

gateway/
├── gateway-controller/pkg/{config,controlplane,lazyresourcexds,utils,storage}/
├── system-policies/budget/            # new system policy (+ Redis counter)
├── system-policies/analytics/         # budget_* fields
├── gateway-runtime/policy-engine/     # analytics wiring, phase-ordering test
├── configs/config-template.toml, config.toml
└── docker-compose.yaml                # + redis

gateway/distribution/docker-compose.yaml     # + redis (shipped gateway distribution)
kubernetes/helm/gateway-helm-chart/    # budgets + Redis values/templates
portals/ai-workspace/src/{apis,pages/appShell/appShellPages/budgets}/
docs/gateway/budgets/                  # user guide
```

**Structure Decision**: Existing monorepo layout; no new top-level projects. The budget policy lives with the other system policies because it must be prepended to every chain and ship with the gateway image (subject to risk 8b).

## Request validation (platform-api, `/budgets`)

Proposed; mirrors the OpenAPI contract and `data-model.md`. Error bodies use the platform's standard `{status, code, message}` shape with generic messages; the specific reason is logged only.

| Validation | Expected behaviour |
|---|---|
| `displayName` omitted, empty, or > 255 chars | 400 `BUDGET_INVALID` |
| `description` > 1023 chars | 400 `BUDGET_INVALID` |
| `amount` omitted, ≤ 0, not a decimal string, > 12 integer digits or > 6 fractional digits | 400 `BUDGET_INVALID` |
| `currency` present and not `USD` | 400 `BUDGET_INVALID` |
| `period` not `DAY` / `WEEK` / `MONTH` | 400 `BUDGET_INVALID` |
| `enforcementMode` not `ENFORCE` / `MONITOR` | 400 `BUDGET_INVALID` |
| `thresholds`: more than 5, duplicates, or a value outside 1–100 | 400 `BUDGET_INVALID` |
| `context` omitted, `context.type` not `ORGANIZATION` / `PROJECT`, `PROJECT` without `project`, or `ORGANIZATION` with `project` | 400 `BUDGET_INVALID_OWNER` |
| An owner with unknown `dimension`, with both or neither of `values` / `each`, an empty `values` list or duplicate values, or `CLAIM` without `claimName` / `claimName` not matching `^[A-Za-z0-9_.:-]{1,255}$`, or a `MODEL` value (any listed one) that is empty, longer than 255 characters or contains control characters | 400 `BUDGET_INVALID_OWNER` |
| The same `dimension` twice, or `CLAIM` twice with the same `claimName` | 400 `BUDGET_OWNER_DUPLICATE` |
| A `PROJECT` owner on a budget whose context is a project | 400 `BUDGET_OWNER_NOT_ALLOWED` |
| `context.project` or any listed owner value names a project / LLM provider / LLM proxy / application that does not exist in the caller's organization, or (project context) a proxy / application of another project | 404 `BUDGET_OWNER_NOT_FOUND` (same response whether it exists in another organization or not at all) |
| An `LLM_PROVIDER` / `LLM_PROXY` listed value resolves to an artifact of a different kind | 400 `BUDGET_INVALID_OWNER` |
| A budget with the same context, owners and period already exists | 409 `BUDGET_ALREADY_EXISTS` |
| `handle` already used in the organization | 409 `BUDGET_ALREADY_EXISTS` |
| Unknown request fields | 400 `BUDGET_INVALID` (strict DTO decoding — no mass assignment) |
| Update changes the context, the owners, or the period | 400 `BUDGET_IMMUTABLE_FIELD` |
| `budgetId` not found in the caller's organization | 404 `BUDGET_NOT_FOUND` |
| Create/update of a budget that overrides another (FR-003a) | Not an error: 201/200 with the read-only `overrides` list populated and a `warnings` entry `BUDGET_OVERRIDES_OTHER` naming the overridden budgets |
| Query params: `limit` outside 1–100, unknown `status` / `contextType` / `enforcementMode` value, `project` not found | 400 `BUDGET_INVALID` |

## Error handling

| Condition | Status | Client-facing behaviour | Logged internally |
|---|---|---|---|
| Enforce-mode budget exhausted (gateway) | 429 | `BUDGET_EXHAUSTED`, budget id/name, period, reset time, `Retry-After` | DEBUG: budget id, owner dimensions, period label (no claim values) |
| Redis unreachable at admission, `failure_mode = closed`, enforce-mode budget applies (gateway) | 503 | `BUDGET_CHECK_UNAVAILABLE`, generic message | ERROR (rate-limited): Redis endpoint, error |
| Redis unreachable at admission, `failure_mode = open` (gateway) | — | Request allowed, no `Budget-*` headers, not counted; event `budget_check_skipped = true` | WARN (rate-limited) |
| Redis `INCRBY` fails at response end (gateway) | — | No client impact (response already sent); cost missing from the counter | ERROR + `budget_charge_failures_total` |
| Threshold marker `SET NX` fails (gateway) | — | No client impact; crossing may be emitted by a later request or missed | WARN |
| Budgets enabled without Redis configured (gateway-controller startup) | — | Gateway does not start; config error names `[policy_configurations.budgets_v1.redis]` | ERROR |
| Redis config can lose data (eviction policy ≠ `noeviction`, no AOF/RDB) | — | None | WARN at startup and every `config_check_interval`; gauge `budget_redis_config_unsafe = 1` |
| Budget definitions not yet received after gateway start (gateway) | — | Requests allowed; budgets enforce once definitions arrive (definitions resync on connect) | WARN once |
| Validation / not-found / conflict on `/budgets` | 400 / 404 / 409 | Codes in the validation table, generic message | DEBUG: field and reason |
| Unexpected error on `/budgets` | 500 | Generic message with tracking id | ERROR with stack |

## Configuration

| Key | Type | Default | Component | Description |
|---|---|---|---|---|
| `budgets.enabled` | bool | `false` | gateway-controller | Injects the budget system policy into LLM routes. Requires `policy_configurations.budgets_v1.redis` (startup fails otherwise). `false` = rollback switch; nothing is enforced on that gateway. The shipped compose and Helm values set it `true` together with Redis. |
| `policy_configurations.budgets_v1.redis.*` | object | unset | gateway runtime | **Required when budgets are enabled.** Same shape as the advanced-ratelimit Redis settings: host, port, username, password, db, TLS (enabled, CA, server name, client cert/key for mTLS, insecure_skip_verify), timeouts, pool size. All gateways of an org must use the same Redis/db. |
| `policy_configurations.budgets_v1.failure_mode` | string | `closed` | gateway runtime | Redis unreachable: `closed` refuses requests under enforce-mode budgets (503); `open` allows them uncounted. |
| `policy_configurations.budgets_v1.config_check_interval` | duration | `10m` | gateway runtime | Re-check `maxmemory-policy` / persistence and WARN if unsafe. |
| Helm `gateway.budgets.redis.bundled.enabled` | bool | `false` | Helm | Deploy a single-replica Redis (AOF, `noeviction`, PVC) with the gateway; for production prefer an external, managed/HA Redis. |
| `APIP_AIW_MOESIF_ANALYTICS_URL` | string | `https://www.moesif.com/wrap/basic` | AI Workspace runtime config | Base URL of the embedded Moesif analytics, used by the Insights page and the Budgets "See usage" links (replaces the hard-coded URL). LLM Analytics path and Budget filter format: open, risk 12. |

No feature flag in the UI: the Budgets page is always available to users with `ap:budget:read`. Removed 2026-10-09: `mode`, `sync_interval`, `sync_interval_fast`, `fast_sync_percent`, `disconnect_alert_after`, `failure_mode = fallback`, `budgets.period_purge_after`, `budgets.moesif.enabled`.

## Observability

- **Logs** (no claim values, API keys, tokens or Redis passwords at INFO and above):
  - gateway runtime: DEBUG per refusal (budget id, owner dimensions, period label); ERROR (rate-limited) on Redis unavailability and charge failures; WARN on unsafe Redis configuration and marker failures; INFO per emitted threshold crossing (budget id, threshold, period).
  - gateway-controller: INFO on budget definition sync (count, org); ERROR on budgets enabled without Redis.
  - platform-api: INFO on budget create/update/delete/auto-disable (budget id, actor).
- **Metrics** (low-cardinality labels only — no user, claim or budget values):
  - `budget_checks_total{result="allowed|refused|monitor_would_refuse|unavailable|skipped", context_type}` (gateway runtime)
  - `budget_redis_request_duration_seconds{op="read|charge|marker"}` histogram and `budget_redis_errors_total{op}` (gateway runtime)
  - `budget_charge_failures_total` (gateway runtime)
  - `budget_threshold_events_total{outcome="emitted|suppressed|marker_error"}` (gateway runtime; `suppressed` = lost the `SET NX` race or a lower threshold set silently)
  - `budget_unpriced_requests_total` (gateway runtime; unpriced requests under any budget)
  - `budget_redis_config_unsafe` gauge (gateway runtime)

## Rollout and rollback

- **Default state**: on upgrade nothing changes for traffic — `budgets.enabled` defaults to `false`, so no policy is injected; platform-api accepts budget definitions and pushes them, but gateways without budgets enabled do not enforce them (risk 9).
- **Enabling on a gateway**: provision Redis (shipped compose/Helm include it; production: external Redis with TLS + auth, `noeviction`, AOF), configure `budgets_v1.redis` identically on every gateway of the org, set `budgets.enabled = true`, restart the gateway-controller/runtime.
- **Prerequisite**: LLM proxies must carry their project id (risk 1a) before project-context budgets and project owners apply to LLM traffic.
- **Mixed gateway versions**: gateways older than this feature ignore `budget.*` events (unknown websocket messages are logged and dropped), so budgets are **not enforced on them** (risk 9).
- **Redis data loss**: the current period's spend restarts at zero for affected budgets (FR-022a); documented in the user guide with the persistence recommendation.
- **Rollback**: disable or delete budgets (enforcement stops within seconds); or set `budgets.enabled = false` on a gateway. The new table and the nullable `audit.details` column are additive, so the previous release runs against the upgraded schema unchanged. Redis keys expire on their own.

## Delivery phases

| Phase | Scope | Spec coverage |
|---|---|---|
| **1 — MVP** | CRUD + definitions sync; Redis client/config, activation check and unsafe-config warning; system policy with Redis counters for organization and project contexts with appended project, LLM provider and LLM proxy owners (one or more specific values, or each), owner match index (multi-value owners indexed under every listed value); override resolution (most specific wins, FR-003a) and `overrides` / `overriddenBy` in the API; enforce mode; 429 + Retry-After; 503 / fail-open; streaming + `include_usage`; loopback skip; unpriced marking and fail-open marking in analytics (`llm_cost_status`, `budget_check_skipped`, `budget_id`, `budget_name` under Moesif `metadata.*`, incl. the analytics publisher change); Budgets configuration page with usage links (`APIP_AIW_MOESIF_ANALYTICS_URL`); compose + Helm Redis | US1, US5; FR-001, 002, 003 (contexts; project, LLM provider, LLM proxy owners), 003a, 005, 006, 008, 009 (amount change keeps spend), 009a, 010–015, 016, 016a, 016c, 017–022b, 024, 024a, 024c, 024d, 026–029 |
| **2** | Claim, application and model owners (one or more specific values, or each; model needs the Phase 1 prerequisite, risk 2a), monitor mode, remaining analytics budget fields, gateway threshold detection with Redis markers and re-arming via `amountVersion`, Moesif templates (Budget Usage card + Budget filter on the LLM Analytics page, org-wide alert rule) and guide | US2–US4; FR-002/003 (claim, application, model owners), 004, 007, 009 (threshold re-arming), 023, 024e, 025 |
| **3** | `OnStreamError` charging once the SDK hook exists; Redis Sentinel/Cluster support; async charge optimisation if needed | FR-016b |

## Risks and open items

| # | Item | Mitigation / owner |
|---|---|---|
| 1 | Redis becomes a hard runtime dependency of budgeted traffic (fail-closed by default) | HA/managed Redis recommended; `failure_mode = open` per gateway; metrics + alerts on `budget_redis_errors_total`; documented |
| 1a | LLM proxies don't carry their project to the gateway (no project-id annotation in the proxy deployment), so project budgets would not match LLM traffic | Phase 1 prerequisite: set `gateway.api-platform.wso2.com/project-id` in `generateLLMProxyDeploymentYAML`; redeploy or migrate existing proxies |
| 2 | LLM proxy route may not expose the upstream provider UUID (research R4) | Verify early in Phase 1; add to route metadata if missing |
| 2a | The budget policy needs the upstream request model id (after any proxy model rewrite) in the request phase for the check and in the response phase for the charge; today the provider template's `requestModel` is extracted by the analytics policy | Gateway team; verify early in Phase 1 (task T013a); until available, model owners are rejected by the gateway's definition loader with a WARN and the API can hide the dimension behind a feature flag |
| 3 | No `OnStreamError` hook → aborted streams uncharged | Request hook from gateway team via a GitHub issue (link recorded here and in the PR description, not as a code comment); document limitation |
| 4 | Redis data loss resets the period's spend to zero (restart without persistence, failover, eviction) | Startup/periodic unsafe-config WARN + gauge; docs require AOF + `noeviction`; accepted by the feature owner (FR-022a) |
| 5 | `llm-cost` missing on an LLM provider/proxy → its traffic is unpriced | `metadata.llm_cost_status = not_calculated` on each event (Phase 1); visible on the LLM Analytics page by setting the Budget filter and filtering on `llm_cost_status` — no separate per-budget view |
| 6 | Threshold warning lost if the crossing request's analytics event is lost (analytics disabled, publisher failure, Moesif sampling) — marker already set | Exempt budget events from sampling; document that warnings require analytics; the Budget Usage card still shows % used per budget |
| 7 | Budgets with "each" owners with very high cardinality (Redis memory), worse with several "each" owners (product of values) | Keys created lazily and expire per period; sizing guidance in docs (data-model §5.3); a cap on "each" owners can be added (risk 13) |
| 8 | Architecture guidance received (2026-10-09) | **Open with architecture leads**: (a) guidance said "avoid new tables" — confirm a single `budgets` definitions table in platform-api is acceptable (spend tables are gone); (b) "system-level policies are not supported" — is a built-in system policy (like the analytics collector) acceptable, or must the budget policy be auto-attached to LLM deployments? |
| 9 | Gateways older than this feature, or with `budgets.enabled = false`, silently do not enforce an org's budgets | Open: should gateways report budget capability on connect so platform-api / UI can warn when an org has budgets and some connected gateways do not enforce them? |
| 10 | Product decisions still open ("split by", override and model owner resolved 2026-10-14, R13): meaning of "dynamic budgets for apps", custom model prices, currency (USD only proposed), whether track/warn/block via Enforce/Monitor + thresholds is accepted, whether `ap_viewer` gets `ap:budget:read` (proposed default: no) | Product leads |
| 11 | All gateways of an organization must share one Redis; multi-region and hybrid (cloud + self-hosted gateways) deployments may not be able to | **Open**: shared cross-region Redis (latency vs SC-008) vs per-region budgets/Redis (then one budget ≠ one amount) — decision needed from architecture/product leads |
| 12 | Moesif specifics: LLM Analytics page path under `APIP_AIW_MOESIF_ANALYTICS_URL` and the Budget filter deep-link query format; whether the Budget Usage card, Budget filter and org-wide alert rule can be provisioned via the Moesif management API; sampling exemption mechanism | **Open — confirm with the analytics team** (path/filter format before the US5 link ships); fallback: link to the base URL and documented manual setup |
| 13 | No limit on owners per budget or on values per owner (feature owner, 2026-10-14): gateway matching cost grows with owners and budgets per org (SC-008, 5 ms p95), and "each" owners multiply Redis keys | Match index by owner value; benchmark with many-owner budgets in T111; add a cap (e.g. 3 owners, 2 "each", a maximum number of values per owner) if needed |

Removed 2026-10-09: former risk 1 (spend-sync plumbing), former risk 4 (platform-api access to the org's Moesif application id — platform-api no longer publishes to Moesif), former risk 6 (default-mode overshoot bound).

## Complexity Tracking

> No constitution violations to justify.
