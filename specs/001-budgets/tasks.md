---

description: "Task list for the Budgets feature (AI Gateway spend ceilings) — regenerated 2026-10-09 for the Redis-only architecture"
---

# Tasks: Budgets

**Input**: Design documents from `/specs/001-budgets/` (spec.md, plan.md, research.md, data-model.md, contracts/, quickstart.md)

**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md, data-model.md, contracts/

**Regenerated 2026-10-09** for the architecture change (spend only in a required shared Redis; configuration-only UI; no gateway → control-plane spend sync; gateway-detected thresholds via Redis markers; no platform-api Moesif actions). Task ids were renumbered; earlier ids (T001–T140) are obsolete.

**Updated 2026-10-13** (re-analysis remediation): new tasks T114–T119 — Moesif event plumbing and Phase 1 emission of `llm_cost_status` / `budget_check_skipped` / `budget_id` / `budget_name` (T114, T115), Moesif templates for the Budget Usage card, Budget filter and org-wide alert rule (T116–T118), AI Workspace analytics base-URL config (T119). Budget fields are published under the Moesif event `metadata` object (`metadata.budget_*`); the request cost is `metadata.aiMetadata.llmCost`.

**Updated 2026-10-14** (budget owners, research R13): task descriptions revised for the context + appended owners model (T017, T019, T025, T029, T027, T044, T048, T050, T060–T063, T065, T067, T073, T076–T083, T085, T089, T096, T098, T099, T100, T104, T111, T116, T117) and new task T120 (combined-owners IT). Task ids unchanged.

**Updated 2026-10-14 (later)** (model owner, most specific wins — FR-002, FR-003a): new tasks T013a/T013b (upstream model id prerequisite), T121 (service override computation and warnings), T122 (override IT), T123 (model-owner IT); descriptions revised for T017, T025, T050, T060, T065, T067, T073, T076, T077, T079, T080, T085, T120. Override resolution is Delivery phase 1; the MODEL dimension is Delivery phase 2.

**Updated 2026-10-14 (multi-value owners)** (FR-003, FR-003a, research R13): owners carry `values` lists (any of, one shared counter) instead of a single `value`; descriptions revised for T025, T048, T050, T060, T063, T065, T073, T076, T079, T099, T104, T121; new task T124 (multi-value gateway IT).

**Tests**: Included. The plan requires Go unit tests (miniredis for Redis logic), platform-api integration tests, gateway integration tests (`gateway/it`, godog features, using the existing IT `redis` service), a policy-engine phase-ordering test and AI Workspace component tests; quickstart.md defines the end-to-end validation scenarios.

**Organization**: Tasks are grouped by user story (US1–US5 from spec.md). Delivery phases from plan.md map onto these as follows:

| plan.md delivery phase | Task phases in this file |
|---|---|
| **1 — MVP** | Phase 1 Setup, Phase 2 Foundational, Phase 3 US1 (incl. T114, T115), Phase 7 US5 (T096–T099, T119), Phase 8 Polish items for compose/Helm Redis and docs |
| **2** | Phase 4 US2 (claim, application and model owners), Phase 5 US3 (thresholds + remaining analytics fields + alert rule template T116), Phase 6 US4 (monitor mode), Phase 7 US5 Moesif card and filter templates (T117, T118 — need T089 fields) |
| **3** | T110 (`OnStreamError` charging) |

## Format: `- [ ] Tnnn [P?] [USn?] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[USn]**: Which user story this task belongs to (US1–US5); Setup, Foundational and Polish tasks carry no story label
- All paths are repo-relative to the code repository `wso2/api-platform` (local checkout `/Users/pasant/Desktop/git/wso2/api-platform`)

## Path Conventions

- **platform-api**: `platform-api/resources/`, `platform-api/config/`, `platform-api/internal/{database,model,repository,service,handler,apperror,server,utils}/`
- **gateway-controller**: `gateway/gateway-controller/pkg/{config,controlplane,lazyresourcexds,utils,storage,models}/`
- **gateway runtime**: `gateway/system-policies/budget/` (new), `gateway/system-policies/analytics/`, `gateway/gateway-runtime/policy-engine/`, `sdk/core/policy/v1alpha2/`
- **shared**: `common/eventhub/`
- **gateway ITs**: `gateway/it/features/*.feature`, `gateway/it/steps_*.go`, `gateway/it/test-config.toml`, `gateway/it/docker-compose.test.yaml`, `tests/mock-servers/mock-platform-api/`, `gateway/it/mock-api/`
- **UI**: `portals/ai-workspace/src/{apis,pages/appShell/appShellPages/budgets}/`
- **deploy/docs**: `gateway/configs/`, `gateway/docker-compose.yaml`, `gateway/distribution/docker-compose.yaml`, `kubernetes/helm/gateway-helm-chart/`, `docs/gateway/budgets/`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Scaffolding for the new system policy and UI area

- [ ] T001 Create the budget system-policy Go module `gateway/system-policies/budget/go.mod` (same module-path style and `sdk/core/policy/v1alpha2` dependency as `gateway/system-policies/analytics/go.mod`, plus `github.com/redis/go-redis/v9` and `github.com/alicebob/miniredis/v2` for tests) and add `./gateway/system-policies/budget` to `go.work`
- [ ] T002 [P] Create `gateway/system-policies/budget/policy-definition.yaml` for policy `wso2_apip_sys_budget` (system policy, no user-facing parameters; request body mode buffer, response body mode stream) modelled on `gateway/system-policies/analytics/policy-definition.yaml`
- [ ] T003 Register `wso2_apip_sys_budget` (`filePath: ./budget`) in `gateway/system-policies/system-build-lock.yaml` (depends on T001, T002)
- [ ] T004 [P] Create the AI Workspace area `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/index.ts` and typed client stub `portals/ai-workspace/src/apis/budgetApis.ts` with TypeScript types mirroring `Budget` (incl. read-only `statusReason`), `BudgetScope`, `CreateBudgetRequest`, `UpdateBudgetRequest` from `specs/001-budgets/contracts/budgets-api.openapi.yaml` (definitions only — no spend types)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Prerequisites, Redis client/config and key schema, platform-api schema and API surface, and the definitions pipeline (platform-api → controller → policy engine) that every story needs

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

### Prerequisites (plan risks 1a, 2, 3; research R3)

- [ ] T005 Set annotation `gateway.api-platform.wso2.com/project-id` from the proxy's project in `generateLLMProxyDeploymentYAML` in `platform-api/internal/service/llm_deployment.go` (mirroring REST APIs in `platform-api/internal/utils/api.go:543`) so `SharedContext.ProjectID` is populated for LLM proxy traffic
- [ ] T006 [P] Unit test that generated LLM proxy deployment YAML carries the project-id annotation in `platform-api/internal/service/llm_deployment_test.go`
- [ ] T007 [P] Verify and, if missing, propagate the `project_id` route metadata for `LlmProxy` routes in `gateway/gateway-controller/pkg/utils/llm_transformer.go`, with a test in `gateway/gateway-controller/pkg/utils/llm_transformer_test.go`
- [ ] T008 Add the upstream LLM provider artifact UUID to LLM proxy route metadata (research R4) in `gateway/gateway-controller/pkg/models/runtime_deploy_config.go` and populate it in `gateway/gateway-controller/pkg/utils/llm_transformer.go`; expose it to policies via `sdk/core/policy/v1alpha2/context_accessors.go`
- [ ] T009 [P] Test the upstream-provider UUID on proxy routes in `gateway/gateway-controller/pkg/utils/llm_transformer_multiprovider_test.go` and `sdk/core/policy/v1alpha2/context_accessors_test.go`
- [ ] T013a Verify with the gateway team, and add if missing, access for system policies to the **upstream request model id** (the provider template's `requestModel` extraction applied to the request as sent upstream, after any proxy model rewrite — today done by `gateway/system-policies/analytics/analytics.go`, metadata `aitoken:requestmodelid`) in the request-body phase, kept to the response phase; expose via `sdk/core/policy/v1alpha2/context_accessors.go` (plan risk 2a, research R8). Needed for MODEL owners (Delivery phase 2); non-blocking for phase 1
- [ ] T013b [P] Test that the upstream model id is available in the request phase and identical in the response phase for direct provider traffic, proxy traffic with a model rewrite, and Bedrock path-param models, in `sdk/core/policy/v1alpha2/context_accessors_test.go` (depends on T013a)
- [ ] T010 [P] Raise the SDK/gateway-team ask for an `OnStreamError` streaming hook (FR-016b) against `sdk/core/policy/v1alpha2/interface.go` as a GitHub issue (owner + target release); reference the issue in plan.md risk 3, in T110 and in the PR description — no `TODO`/deferral comment in code (non-blocking)
- [ ] T011 [P] Policy-engine phase-ordering test pinning research R3: with a chain `[prepended system policy, analytics, auth (header phase), …, llm-cost]`, assert that every request-header policy (auth) runs before any request-body policy (so the budget policy sees `AuthContext`), and that the prepended policy's response/stream handler runs after `llm-cost` has set `Metadata["x-llm-cost"]` (reverse order on `EndOfStream` and on buffered bodies), in `gateway/gateway-runtime/policy-engine/internal/executor/chain_phase_order_test.go`

### Shared domain helpers

- [ ] T012 [P] Implement UTC calendar periods for the gateway in `gateway/system-policies/budget/period.go`: labels `2026-10-08` (DAY, resets 00:00 UTC), `2026-W41` (ISO week, resets Monday 00:00 UTC), `2026-10` (MONTH, resets 00:00 UTC on the 1st); `PeriodStart/End(period, t)`; table tests incl. ISO-week year boundaries in `gateway/system-policies/budget/period_test.go`
- [ ] T013 [P] Implement money conversion (decimal string matching `^\d{1,12}(\.\d{1,6})?$` ↔ integer nano-USD, USD × 10⁹) in `platform-api/internal/utils/budget_money.go` with tests in `platform-api/internal/utils/budget_money_test.go`; and cost-string (`x-llm-cost`, `%.10f`) → nano-USD rounding in `gateway/system-policies/budget/money.go` with tests in `gateway/system-policies/budget/money_test.go`

### Redis foundations (gateway runtime + controller)

- [ ] T014 Implement `budgets_v1` configuration parsing in `gateway/system-policies/budget/config.go`: `failure_mode` (`closed` default | `open`), `config_check_interval` (default `10m`), required `redis` block in the advanced-ratelimit shape (host, port, username, password, db, `key_prefix` default `budget:v1:`, dial/read/write timeouts, pool size, `tls` {enabled, ca_file, server_name, cert_file, key_file, insecure_skip_verify}); the policy factory returns a clear error when `redis` is missing (FR-022); tests in `gateway/system-policies/budget/config_test.go`
- [ ] T015 Implement the Redis client in `gateway/system-policies/budget/redis_client.go` (go-redis v9 universal client built from T014 config; TLS with CA/server name, client cert for mTLS, ACL username/password; secrets never logged; health ping at startup), with tests against miniredis and a TLS-enabled miniredis in `gateway/system-policies/budget/redis_client_test.go`
- [ ] T016 Implement the data-loss safety check in `gateway/system-policies/budget/redis_safety.go`: at startup and every `config_check_interval`, best-effort `CONFIG GET maxmemory-policy` and `INFO persistence`; WARN when not `noeviction` or neither AOF nor RDB is enabled; set gauge `budget_redis_config_unsafe`; log INFO once and continue when `CONFIG` is not permitted (managed Redis); tests in `gateway/system-policies/budget/redis_safety_test.go`
- [ ] T017 [P] Implement the key schema in `gateway/system-policies/budget/keys.go` (data-model §5): counter key `budget:v1:{<budgetUuid>}:<periodLabel>:<valueKey>`, marker key `…:thr:<threshold>:<amountVersion>`, `valueKey` = the request's values for the budget's "each" owners in canonical order (project, provider, proxy uuids; `m` + first 32 hex of SHA-256 of the model id; application uuid; `h` + first 32 hex of SHA-256 for each claim value, claims sorted by claim name) joined with `:`, `_` when there is no "each" owner, `x` + SHA-256 when longer than 200 chars, expiry = period end + 86400 s; tests in `gateway/system-policies/budget/keys_test.go` (no raw claim value ever appears in a key)
- [ ] T018 Add `[budgets] enabled` (default `false`) to `gateway/gateway-controller/pkg/config/config.go` and validation in `gateway/gateway-controller/pkg/config/validator.go` that fails startup with `budgets.enabled requires [policy_configurations.budgets_v1.redis] (Redis is required for budgets)` when enabled without the Redis block (FR-022); append `wso2_apip_sys_budget` to `defaultSystemPolicies` (prepended to every chain) only when enabled in `gateway/gateway-controller/pkg/utils/system_policies.go`; tests in `gateway/gateway-controller/pkg/config/config_test.go` and `gateway/gateway-controller/pkg/utils/system_policies_test.go`

### platform-api schema, model, repository, errors, API surface

- [ ] T019 Add table `budgets` to `platform-api/internal/database/schema.postgres.sql` (idempotent guards, no `CHECK` enums, `SMALLINT` booleans): `uuid VARCHAR(40)` PK; `handle VARCHAR(40) NOT NULL`; `organization_uuid VARCHAR(40) NOT NULL` FK `organizations(uuid) ON DELETE CASCADE`; `display_name VARCHAR(255) NOT NULL`; `description VARCHAR(1023)`; `context_type VARCHAR(20) NOT NULL`; `project_uuid VARCHAR(40) NOT NULL DEFAULT ''`; `owners BYTEA NOT NULL` (canonical JSON, data-model §1.1; SQLite `TEXT`, SQL Server `NVARCHAR(MAX)`); `owners_hash CHAR(64) NOT NULL`; `amount_nano BIGINT NOT NULL`; `amount_version BIGINT NOT NULL DEFAULT 1`; `period VARCHAR(10) NOT NULL`; `enforcement_mode VARCHAR(10) NOT NULL DEFAULT 'ENFORCE'`; `threshold_percents VARCHAR(64) NOT NULL DEFAULT ''`; `status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE'`; `status_reason VARCHAR(20)` (nullable); `data_version VARCHAR(20) NOT NULL DEFAULT '1.0'`; `created_by`, `updated_by VARCHAR(200)`; `created_at`, `updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP`; `UNIQUE (organization_uuid, handle)`; `UNIQUE (organization_uuid, context_type, project_uuid, owners_hash, period)`; `idx_budgets_org_status`; `idx_budgets_project (organization_uuid, project_uuid)`
- [ ] T020 [P] Add the equivalent `budgets` table (`owners` as `TEXT`) to `platform-api/internal/database/schema.sqlite.sql` and `platform-api/internal/database/schema.sql` (DB rule R8 dialect parity)
- [ ] T021 [P] Add the equivalent `budgets` table (`owners` as `NVARCHAR(MAX)`) to `platform-api/internal/database/schema.sqlserver.sql` (DB rule R8 dialect parity)
- [ ] T022 [P] Schema parity/idempotency test (columns, PK, uniques, indexes identical across dialects; DDL re-applies cleanly) in `platform-api/internal/database/budgets_schema_test.go`
- [ ] T023 [P] Add nullable `details` to the shipped `audit` table (FR-026, FR-009a; data-model §3): in the `CREATE TABLE audit` body of `platform-api/internal/database/schema.postgres.sql` (`BYTEA`), `schema.sqlite.sql` and `schema.sql` (`TEXT`), `schema.sqlserver.sql` (`NVARCHAR(MAX)`) and the fixture `platform-api/internal/database/init-platform-api-db.sql`, **plus** the guarded upgrade `ALTER` for existing databases (Postgres `ALTER TABLE audit ADD COLUMN IF NOT EXISTS details BYTEA NULL`; SQL Server `IF COL_LENGTH(N'dbo.audit', N'details') IS NULL ALTER TABLE dbo.audit ADD details NVARCHAR(MAX) NULL`; SQLite `pragma_table_info('audit')` check then `ALTER TABLE audit ADD COLUMN details TEXT NULL` in `platform-api/internal/database/connection.go`); test in `platform-api/internal/database/audit_details_upgrade_test.go` that a database created with the previous `audit` DDL gains the column on startup and that re-running is a no-op
- [ ] T024 Add `RecordWithDetails(action, resourceUUID, resourceType, orgUUID, performedBy string, details []byte)` to the `AuditRepository` interface and `platform-api/internal/repository/audit.go` (existing `Record` unchanged, writes `NULL`; named-column INSERT, no `SELECT *`), with tests in `platform-api/internal/repository/audit_test.go` (depends on T023)
- [ ] T025 [P] Create `platform-api/internal/model/budget.go`: `Budget` (incl. `AmountVersion`, `StatusReason`), `BudgetOwner` (dimension, claimName, values []string, each) with canonical ordering (owners by dimension, `values` sorted and de-duplicated) and `OwnersHash()`, Go constants for context types `ORGANIZATION|PROJECT` and owner dimensions `PROJECT|LLM_PROVIDER|LLM_PROXY|MODEL|APPLICATION|CLAIM` (canonical order), periods `DAY|WEEK|MONTH`, modes `ENFORCE|MONITOR`, status `ACTIVE|DISABLED`, status reasons `OWNER_DELETED` (validated in Go, no DB `CHECK`)
- [ ] T026 [P] Create websocket payload models in `platform-api/internal/model/budget_event.go` for `budget.created|updated|deleted` per contract gateway-definitions §1.1 (incl. `amountVersion` and `thresholds`; no usage/totals messages)
- [ ] T027 Implement `platform-api/internal/repository/budget_repository.go` (create/get-by-handle/get-by-uuid/list with `query`, `project`, `contextType`, `status`, `enforcementMode` filters and pagination/update/delete/list-by-org/list-by-project; owners (de)serialised as canonical JSON; map unique violations to a typed conflict) following `platform-api/internal/repository/subscription_plan_repository.go`
- [ ] T028 [P] Repository tests against SQLite in `platform-api/internal/repository/budget_repository_test.go` (CRUD, filters, unique conflicts, rows removed when the organization is deleted)
- [ ] T029 [P] Add error codes `BUDGET_INVALID`, `BUDGET_INVALID_OWNER`, `BUDGET_OWNER_DUPLICATE`, `BUDGET_OWNER_NOT_ALLOWED`, `BUDGET_OWNER_NOT_FOUND`, `BUDGET_ALREADY_EXISTS`, `BUDGET_IMMUTABLE_FIELD`, `BUDGET_NOT_FOUND` to `platform-api/internal/apperror/codes.go` and their status/generic messages (400/400/400/400/404/409/400/404) to `platform-api/internal/apperror/catalog.go`
- [ ] T030 Merge the Budgets paths and schemas from `specs/001-budgets/contracts/budgets-api.openapi.yaml` into `platform-api/resources/openapi.yaml`, declare scopes `ap:budget:manage` and `ap:budget:read` under the OAuth2 security scheme, and regenerate models via `platform-api/oapi-codegen.yaml`
- [ ] T031 Map `ap:budget:manage` → `ap_admin` and `ap:budget:read` → `ap_operator` (and `ap_admin`) in `platform-api/resources/role-to-scope-mapping.yaml`; update expectations in `platform-api/internal/server/role_scope_map_test.go`
- [ ] T032 Add `BroadcastBudgetEvent(orgID, eventType, payload)` sending `budget.*` to every gateway of the organization in `platform-api/internal/service/gateway_events.go` (pattern `BroadcastSubscriptionPlanUpdatedEvent`)
- [ ] T033 Add internal endpoint `GET /api/internal/v1/budgets` (gateway-token auth via `authenticateRequest`, returns `{list: [...]}` of all non-deleted budgets of the gateway's org in the gateway-definitions §1.1 shape) in `platform-api/internal/handler/gateway_internal.go` and `platform-api/internal/service/gateway_internal.go`; add the path to `SkipPaths` in `platform-api/config/default_config.go` and document it in `platform-api/resources/gateway-internal-api.yaml`
- [ ] T034 [P] Allow the internal budgets GET in read-only mode in `platform-api/internal/handler/gateway_internal_readonly.go` with a test in `platform-api/internal/handler/gateway_internal_readonly_test.go`

### gateway-controller definitions pipeline

- [ ] T035 [P] Add controller table `budgets` (definition fields of data-model §1 minus audit columns, incl. `amount_version`, `threshold_percents`) to `gateway/gateway-controller/pkg/storage/gateway-controller-db.sql`, `gateway-controller-db.postgres.sql`, `gateway-controller-db.sqlserver.sql` and `gateway/gateway-controller/resources/gateway-controller-db.sql`; keep `gateway/gateway-controller/pkg/storage/schema_consistency_test.go` green
- [ ] T036 [P] Create `gateway/gateway-controller/pkg/models/budget.go` (budget definition model matching contracts/gateway-definitions.md §1.1)
- [ ] T037 Add budget CRUD methods to `gateway/gateway-controller/pkg/storage/interface.go` and implement them in `gateway/gateway-controller/pkg/storage/sql_store.go`, with tests in `gateway/gateway-controller/pkg/storage/budget_store_test.go`
- [ ] T038 [P] Add `EventTypeBudget` to `common/eventhub/types.go` for replica sync of budget definitions
- [ ] T039 Implement `UpsertBudget` / `DeleteBudget` (persist to DB, publish `EventTypeBudget`, `lazyResourceManager.StoreResource` / `RemoveResourceByIDAndType` with `ResourceType = "Budget"`, idempotent by uuid + `updatedAt`) in `gateway/gateway-controller/pkg/utils/budget_resource.go` (pattern `subscription_resource.go`), with tests in `gateway/gateway-controller/pkg/utils/budget_resource_test.go`
- [ ] T040 Rebuild the `Budget` LazyResources from the controller DB on startup in `gateway/gateway-controller/pkg/lazyresourcexds/lazy_resource_manager.go` (or the controller bootstrap that seeds it), test in `gateway/gateway-controller/pkg/lazyresourcexds/lazyresourcexds_test.go`
- [ ] T041 Add `FetchBudgets()` calling `GET /api/internal/v1/budgets` in `gateway/gateway-controller/pkg/utils/api_utils.go`
- [ ] T042 Handle `budget.created|updated|deleted` messages and add `syncBudgets(gwID)` on connect/reconnect (reconcile: delete locally what is absent), mirroring `syncSubscriptionPlans`, in `gateway/gateway-controller/pkg/controlplane/client.go` and `gateway/gateway-controller/pkg/controlplane/events.go`; unit tests in `gateway/gateway-controller/pkg/controlplane/budget_events_test.go`
- [ ] T043 Wire replica sync for `EventTypeBudget` in `gateway/gateway-controller/pkg/utils/replica_sync_dependencies.go`

### Policy runtime and IT foundations

- [ ] T044 Implement the definitions reader over `GetLazyResourceStoreInstance().GetResourcesByType("Budget")` (decode §1.1 maps into typed budgets incl. `contextType`, `projectUuid`, `owners`, `amountVersion` and `thresholds`, build the match index keyed by owner dimension value, skip `DISABLED`) in `gateway/system-policies/budget/definitions.go` with tests in `gateway/system-policies/budget/definitions_test.go`
- [ ] T045 [P] Extend `tests/mock-servers/mock-platform-api/` to serve `GET /api/internal/v1/budgets` and push `budget.*` websocket events on demand (used by gateway ITs)
- [ ] T046 [P] Add `gateway/it/steps_budget.go` (create/update/delete budget via mock platform-api, wait for propagation, assert 429/503 body, `Retry-After`, `Budget-*` headers, read/assert Redis counter and marker keys via a go-redis client against the IT `redis` service, stop/start Redis) and register the steps in `gateway/it/suite_test.go`
- [ ] T047 [P] Enable budgets in the IT gateway config `gateway/it/test-config.toml` (`[budgets] enabled = true`, `[policy_configurations.budgets_v1.redis]` → `redis:6379`, password `redis`, db 1 to stay clear of semantic-cache keys) and make the gateway services depend on the existing `redis` service in `gateway/it/docker-compose.test.yaml`

**Checkpoint**: Foundation ready — a budget created in platform-api reaches every gateway's policy engine as a `Budget` LazyResource, and the policy can connect to Redis; user story implementation can begin

---

## Phase 3: User Story 1 - Cap an organization, project, LLM provider or LLM proxy's monthly AI spend (Priority: P1) 🎯 MVP

**Goal**: Administrators create budgets in org mode or project mode and append project, LLM provider and LLM proxy owners (one or more specific values, or each), with DAY/WEEK/MONTH periods; every gateway counts spend in the shared Redis and refuses requests once an enforce-mode budget is exhausted, until the period resets.

**Independent Test**: Create a $10 (quickstart: $0.05) monthly budget on one LLM proxy; send priced requests until spend passes the amount; the next request gets 429 `BUDGET_EXHAUSTED` with `Retry-After` = seconds to the 1st of next month 00:00 UTC; a request through a different proxy (or directly to the provider) is unaffected (quickstart Scenarios 0, 1, 2, 5, 7, 8).

### Tests for User Story 1 ⚠️

> Write these tests FIRST and ensure they FAIL before implementation

- [ ] T048 [P] [US1] Service validation tests covering every row of plan.md "Request validation" for non-claim scopes (displayName empty/>255 → `BUDGET_INVALID`; description >1023; amount ≤0/not decimal/>12 int or >6 frac digits; currency ≠ USD; bad period/enforcementMode; thresholds >5, duplicates, outside 1–100; bad/missing context → `BUDGET_INVALID_OWNER`; owner with both/neither values and each, an empty `values` list or duplicate values → `BUDGET_INVALID_OWNER`; repeated dimension → `BUDGET_OWNER_DUPLICATE`; PROJECT owner in project context → `BUDGET_OWNER_NOT_ALLOWED`; unknown/other-org value (any listed one) or proxy of another project → 404 `BUDGET_OWNER_NOT_FOUND`; LLM_PROVIDER/LLM_PROXY of wrong artifact kind; owners stored in canonical order and `values` sorted regardless of input order; duplicate context+owners+period (any input order, including reordered `values`) or handle → 409; immutable-field change → `BUDGET_IMMUTABLE_FIELD`; `amount_version` increments only on amount change) in `platform-api/internal/service/budget_service_test.go`
- [ ] T049 [P] [US1] platform-api integration test for `/api/v0.9/budgets` CRUD (201 + Location, GET by handle returns definition only with no spend fields, PUT amount increments `amount_version` and audits before/after, DELETE 204, cross-org 404, `ap_operator` 403 on POST/PUT/DELETE, unknown fields rejected) in `platform-api/internal/handler/budget_integration_test.go` (pattern `subscription_plan_integration_test.go`)
- [ ] T050 [P] [US1] Policy matcher tests (org context always; project context and project owner via `SharedContext.ProjectID`; LLM proxy via `APIId` when `APIKind = LlmProxy`; LLM provider via `APIId` or proxy's upstream provider; every owner must match (AND); a multi-value owner matches when the request value is any listed value and all listed values share one counter (`provider ∈ {openai, anthropic}` → both add to `…:_`, gemini not counted); "each" owners build `valueKey` in canonical order, several "each" owners give one key per combination; a budget with a provider and a proxy owner matches only proxy traffic to that provider; direct provider traffic matches no project-context budget; candidate lookup via the match index returns the same set as a full scan; override resolution — most specific wins (FR-003a): `sub = alice` overrides `sub = each` in the same context and period, `provider ∈ {openai}` overrides `provider ∈ {openai, anthropic}` for openai traffic (subset), two non-nested lists stack, the overridden budget is neither read nor charged and its id is reported; different dimension sets, different contexts (org vs project) or different periods stack; partial specificity (`sub = alice` + provider = each vs `sub = each` + provider = openai) stacks; a MONITOR override lifts an ENFORCE `each` budget for that value; override sets recomputed on budget add/change/remove; evaluation order after override resolution: more owners first, then smallest amount, then id) in `gateway/system-policies/budget/matcher_test.go`
- [ ] T051 [P] [US1] Redis counter tests with miniredis (one pipelined read for all applicable counters; missing key = 0; `INCRBY` nano-USD + `EXPIREAT` period end + 1 day in one pipeline; returned totals; zero-cost requests do not write; charge lands in the period in which the response finished; two policy instances sharing one miniredis see one total) in `gateway/system-policies/budget/counter_test.go`
- [ ] T052 [P] [US1] Policy enforcement tests (429 body/headers per contract §1; `Retry-After` = latest reset among exhausted budgets; allowed-response headers `Budget-Id`, `Budget-Remaining`, `Budget-Reset` for the most constraining budget from the admission read; loopback `x-wso2-internal-loopback` skips; non-LLM `APIKind` no-op; unpriced when `x-llm-cost` missing or `x-llm-cost-status = not_calculated` → charge 0, never refuse) in `gateway/system-policies/budget/budget_test.go`
- [ ] T053 [P] [US1] Redis failure tests (miniredis closed: `failure_mode = closed` → 503 `BUDGET_CHECK_UNAVAILABLE` with generic body for requests under an enforce-mode budget; `failure_mode = open` → allowed without `Budget-*` headers and with `budget_check_skipped` metadata; `INCRBY` failure after admission increments `budget_charge_failures_total` and does not affect the response; policy factory refuses to start without `redis` config) in `gateway/system-policies/budget/failure_test.go`
- [ ] T054 [P] [US1] Streaming tests (`stream: true` chat completion gets `stream_options.include_usage = true` when any budget applies, while preserving existing `stream_options`; non-chat/non-stream bodies untouched; charge on `EndOfStream`; policy implements `StreamingResponsePolicy` so the route stays streamed) in `gateway/system-policies/budget/stream_test.go`
- [ ] T055 [P] [US1] Gateway IT `gateway/it/features/budgets.feature`: LLM proxy monthly cap (calls allowed until exhausted, then 429 `BUDGET_EXHAUSTED` with `Retry-After`), Redis counter equals the summed cost, other proxy and direct provider unaffected, provider budget charged for proxy traffic exactly once, project budget applies to proxy traffic, raising amount re-allows within 60 s without redeploy, delete stops enforcement, unpriced model never refused
- [ ] T056 [P] [US1] Gateway IT `gateway/it/features/budgets-streaming.feature` plus a streamed OpenAI-compatible mock with fixed usage under `gateway/it/mock-api/paths/`: upstream receives `include_usage`, client receives incremental chunks, call is charged, mid-stream exhaustion does not cut the stream, next request refused
- [ ] T057 [P] [US1] Gateway IT `gateway/it/features/budgets-shared-counter.feature`: two runtime replicas share one Redis total and both refuse once it is exhausted (overshoot ≤ in-flight calls under 20 concurrent requests, SC-003); runtime and controller restart leave the counter unchanged (SC-004); mock platform-api outage keeps enforcing with last definitions (FR-021)
- [ ] T058 [P] [US1] Gateway IT `gateway/it/features/budgets-redis-failure.feature` (FR-020, FR-022, FR-022a): Redis stopped → 503 `BUDGET_CHECK_UNAVAILABLE` under an enforce budget; `failure_mode = open` override → requests pass uncounted; Redis flushed → counter restarts at zero and requests are allowed; controller started with budgets enabled and no Redis block exits with the documented error (using `gateway/it/steps_compose.go`)
- [ ] T059 [P] [US1] Gateway IT `gateway/it/features/budgets-ratelimit-coexistence.feature` (FR-029, quickstart Scenario 12): a route with both `llm-cost-based-ratelimit` and a budget; the rate-limit refusal is a 429 with the rate-limit policy's own code and no budget body, the budget refusal is a 429 with `BUDGET_EXHAUSTED` and `Retry-After`; existing rate-limit configuration unchanged

### Implementation for User Story 1 — platform-api

- [ ] T060 [US1] Implement `platform-api/internal/service/budget_service.go` Create: strict validation per plan.md table (contexts ORGANIZATION/PROJECT; owner dimensions PROJECT/LLM_PROVIDER/LLM_PROXY in this phase; no limit on owner count), handle generation from displayName if omitted (max 40), resolve the context project and every listed owner value (handles) → uuids within the caller's org (`values` non-empty, no duplicates, stored sorted) (artifact `type` must be `LlmProvider`/`LlmProxy`; in project context, proxies must belong to the project), sort owners canonically and compute `owners_hash`, thresholds stored as ascending CSV, amount → `amount_nano`, `amount_version = 1`; audit `auditRepo.RecordWithDetails("CREATE", uuid, "budget", org, actor, {after})` (T024); broadcast `budget.created`; return `overrides` / `overriddenBy` and the `BUDGET_OVERRIDES_OTHER` warning (T121)
- [ ] T061 [US1] Implement Get/List/Update/Delete in `platform-api/internal/service/budget_service.go`: update only displayName, description, amount, thresholds, enforcementMode, status (reject context / owners / period changes with `BUDGET_IMMUTABLE_FIELD`); increment `amount_version` when the amount changes; audit UPDATE/DELETE via `RecordWithDetails` with JSON `{before, after}` of the mutable fields incl. status and statusReason (FR-009a, FR-026); an administrator status change clears `status_reason`; broadcast `budget.updated` / `budget.deleted`; responses return context and owner handles with `displayName`, and no spend
- [ ] T062 [US1] Implement `platform-api/internal/handler/budget_handler.go` (ListBudgets, CreateBudget, GetBudget, UpdateBudget, DeleteBudget; strict DTO decoding with unknown-field rejection; `limit` 1–100; `project`/`contextType`/`status`/`enforcementMode` filters; wrapped in `middleware.MapErrors`) and register routes with scope checks in `platform-api/internal/server/server.go`; keep `platform-api/internal/server/scope_route_coverage_test.go` green
- [ ] T063 [US1] Disable budgets whose context project or any listed owner value (project / LLM provider / LLM proxy — even one of several values) is deleted — found by scanning the org's budgets' owners — (set `status = DISABLED` and `status_reason = OWNER_DELETED`, audit with before/after via `RecordWithDetails` and actor `system`, broadcast `budget.updated`; no analytics event) via `BudgetService.DisableBudgetsForOwner`, called from the delete paths in `platform-api/internal/service/project.go` and `platform-api/internal/service/llm.go` (LLM provider and LLM proxy), with tests in `platform-api/internal/service/budget_owner_deleted_test.go`
- [ ] T064 [US1] Enforce read-only mode in the service/repository layer (GO-AUTH-015), not only in handlers: `BudgetService` create/update/delete/`DisableBudgetsForOwner` return a read-only error and the write methods of `platform-api/internal/repository/budget_repository.go` refuse too, in `platform-api/internal/service/budget_service.go`, with tests in `platform-api/internal/service/budget_readonly_test.go` that call the service directly (bypassing handlers)

### Implementation for User Story 1 — budget system policy

- [ ] T065 [P] [US1] Implement the budget matcher: match index by owner dimension value, AND check of context and every owner (PROJECT, LLM_PROVIDER incl. proxy's upstream provider, LLM_PROXY; values list — request value ∈ values, indexed under every listed value — or each), `valueKey` per budget via `keys.go`, override sets precomputed from definitions and per-request override resolution (FR-003a), evaluation ordering, in `gateway/system-policies/budget/matcher.go`
- [ ] T066 [P] [US1] Implement the Redis counter in `gateway/system-policies/budget/counter.go`: `Read(ctx, keys)` (one pipeline of `GET`s) and `Charge(ctx, keys, costNano, expireAt)` (one pipeline of `INCRBY` + `EXPIREAT`, returning new totals), with timeouts from T014 and metrics hooks for `budget_redis_request_duration_seconds{op}` / `budget_redis_errors_total{op}`
- [ ] T067 [US1] Implement the request-body phase in `gateway/system-policies/budget/budget.go`: no-op unless `APIKind ∈ {LlmProvider, LlmProxy}` and the org has budgets; skip loopback hop; read the counters of all applicable budgets after override resolution (overridden budgets are not read and not charged; their ids go to `budget_overridden_ids`); refuse with `ImmediateResponse` 429 per contract §1 (code `BUDGET_EXHAUSTED`, `details.budget`, `Retry-After`, `Budget-Id`, `Budget-Reset`; "each" owners report only the caller's own values); stash the evaluation in request metadata; DEBUG log without claim values
- [ ] T068 [US1] Implement Redis-unavailable handling per `failure_mode` (503 `BUDGET_CHECK_UNAVAILABLE` contract §1a when an enforce-mode budget applies; `open` → allow, no headers, `budget_check_skipped`; rate-limited ERROR/WARN logs) in `gateway/system-policies/budget/budget.go`
- [ ] T069 [US1] Implement `include_usage` injection via `UpstreamRequestModifications{Body}` for streamed OpenAI-compatible chat completions whenever any budget applies (enforce or monitor mode, FR-016a) in `gateway/system-policies/budget/stream.go`
- [ ] T070 [US1] Implement the response phase (`BodyModeStream`, `StreamingResponsePolicy`): on `EndOfStream`/buffered body read `Metadata["x-llm-cost"]` and `x-llm-cost-status`, `Charge` every evaluated budget in the period in which the response finished (skip when unpriced), record totals in metadata, count `budget_charge_failures_total` on error; add `Budget-Id`, `Budget-Remaining`, `Budget-Reset` response headers for the most constraining budget from the admission read, in `gateway/system-policies/budget/budget.go`
- [ ] T071 [US1] Wire Redis client (T015) and safety check (T016) into policy initialisation and allow requests (WARN once) when no definitions have been received yet after start, in `gateway/system-policies/budget/budget.go` and `gateway/system-policies/budget/definitions.go`

### Implementation for User Story 1 — analytics (delivery phase 1: unpriced and fail-open marking)

- [ ] T114 [US1] Forward policy-written budget analytics keys to Moesif `metadata.*`: in `prepareAnalyticEvent` copy `budget_*`, `budgets` and `llm_cost_status` from `analytics_data` (typed values, not stringified) into `Properties` in `gateway/gateway-runtime/policy-engine/internal/analytics/analytics.go`, and add them to the Moesif `metadataMap` allow-list in `gateway/gateway-runtime/policy-engine/internal/analytics/publishers/moesif.go` (cost stays `metadata.aiMetadata.llmCost`), with tests in `gateway/gateway-runtime/policy-engine/internal/analytics/analytics_test.go` and `gateway/gateway-runtime/policy-engine/internal/analytics/publishers/moesif_test.go` asserting e.g. `metadata.budget_threshold_crossed` and `metadata.llm_cost_status` on the published event
- [ ] T115 [US1] Emit `llm_cost_status` (`calculated` | `not_calculated`, from `x-llm-cost-status` / missing `x-llm-cost`), `budget_check_skipped` (fail-open), `budget_id` and `budget_name` of the most constraining evaluated budget as analytics metadata on every budgeted AI request, including refused ones (FR-015, FR-020, FR-024a), in `gateway/system-policies/budget/budget.go`, with tests in `gateway/system-policies/budget/analytics_fields_test.go` (depends on T114)

### Implementation for User Story 1 — AI Workspace (configuration)

- [ ] T072 [P] [US1] Implement `listBudgets`, `getBudget`, `createBudget`, `updateBudget`, `deleteBudget` in `portals/ai-workspace/src/apis/budgetApis.ts`
- [ ] T073 [US1] Create/edit dialog (display name, description, first owner as a fixed chip — the organization in org mode, the current project in project mode —, "+ Add owner" rows: dimension select (Project in org mode only, LLM provider, LLM proxy, and — Delivery phase 2 — Model (free text), Application, Token claim; used dimensions disabled), one or more values from searchable multi-select lists via existing APIs (e.g. LLM provider: OpenAI and Anthropic, summarised as "OpenAI or Anthropic") or "Each", remove-row; amount USD, period, thresholds; owners and period read-only in edit; no summary box and no override notice (overrides are not shown in the UI)) in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDialog.tsx`
- [ ] T074 [US1] Add the Budgets route in `portals/ai-workspace/src/App.tsx` and the sidebar item near Insights in `portals/ai-workspace/src/pages/appShell/AppSidebar.tsx`; show Create/Edit/Disable/Enable/Delete only with `ap:budget:manage`, read-only otherwise
- [ ] T075 [P] [US1] Component tests for dialog validation and permission gating in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDialog.test.tsx`

- [ ] T121 [US1] Implement override computation in `platform-api/internal/service/budget_service.go` (`overrides` / `overriddenBy` for get and list, computed from the organization's definitions with the FR-003a rule — per dimension a values list is narrower than each, and a subset list is at least as narrow (a proper subset strictly narrower); `warnings: [{code: BUDGET_OVERRIDES_OTHER, budgets}]` on create/update when the saved budget overrides others, highlighting a MONITOR budget that overrides an ENFORCE one) with tests in `platform-api/internal/service/budget_service_test.go`
- [ ] T122 [P] [US1] Gateway IT `gateway/it/features/budgets-override.feature`: project budgets `LLM_PROXY = each` $0.02 and `LLM_PROXY = proxy-a` $0.05 → proxy-a refused only after $0.05 and its spend absent from the `each` budget's counter; an org-context `LLM_PROXY = each` budget still stacks; same budgets with different periods stack; `budget_overridden_ids` on the events (quickstart Scenario 3b)

**Checkpoint**: US1 fully functional — org/project/provider/proxy budgets enforced on every gateway from the shared Redis counter

---

## Phase 4: User Story 2 - Budgets for teams, departments and users via identity claims (Priority: P1)

**Goal**: Claim owners on any verified token claim (specific value `team = search` or `sub = each`) and application owners, combinable with US1 owners (e.g. `sub = each` + provider = each in a project); requests without claims are still covered by budgets without claim owners.

**Independent Test**: With JWT auth on a route, create an org budget of $5/day (quickstart: $0.02) with owner `sub = each`; two users each get their own allowance and are refused independently; an API-key call is not covered by the claim budget but still by the proxy budget (quickstart Scenarios 3, 4).

### Tests for User Story 2 ⚠️

- [ ] T076 [P] [US2] Service validation tests for CLAIM owners (`claimName` required and matching `^[A-Za-z0-9_.:-]{1,255}$`; values (one or more claim values) or each; two claim owners with different claim names allowed, same claim name → `BUDGET_OWNER_DUPLICATE`) MODEL owners (each listed value non-empty, ≤ 255 characters, no control characters → else `BUDGET_INVALID_OWNER`; not checked against the catalog; values list or each; repeated → `BUDGET_OWNER_DUPLICATE`) and APPLICATION owners (exists in org — and in the project for project context — 404 `BUDGET_OWNER_NOT_FOUND` otherwise) in `platform-api/internal/service/budget_service_test.go`
- [ ] T077 [P] [US2] Matcher tests: `sub` from `AuthContext.Subject`, other claims from `AuthContext.Properties`, walking `AuthContext.Previous`; application from `AuthContext.Properties["ApplicationID"]` / `Metadata["x-wso2-application-id"]`; "each" claim `valueKey` part is the SHA-256-derived hash; claim + provider "each" owners give one key per (provider, user); `sub = alice` + provider = openai matches only alice's OpenAI traffic; API-key request with no claims skips budgets with claim owners but keeps others; MODEL owner matches the upstream request model id exactly and case-sensitively (after proxy rewrite, not the client alias), `model = each` builds the `m`-hash part, a request without a model id skips budgets with a model owner; in `gateway/system-policies/budget/matcher_test.go`
- [ ] T078 [P] [US2] Gateway IT `gateway/it/features/budgets-claims.feature` using `tests/mock-servers/mock-jwks`: per-user `sub` budget refuses alice and bob independently with 429 naming only the caller's value; Redis keys contain hashed values only; `team = search` owner accumulates across users and refuses before the proxy budget (smaller amount); API-key call not covered by claim budget

### Implementation for User Story 2

- [ ] T079 [US2] Enable `CLAIM`, `APPLICATION` and `MODEL` owner dimensions (values list or each) in validation and owner resolution in `platform-api/internal/service/budget_service.go`, and disable budgets that list the deleted application in an owner's values (`status_reason = OWNER_DELETED`, via `DisableBudgetsForOwner`) when the application is deleted in `platform-api/internal/service/application.go`
- [ ] T080 [US2] Implement claim, application and model owner resolution (model from T013a; a gateway without it WARNs once and does not apply MODEL budgets), their match-index entries and their `valueKey` parts (hashed claim values via `keys.go`) in `gateway/system-policies/budget/matcher.go`
- [ ] T081 [US2] Ensure 429 and response headers for budgets with "each" claim owners expose only the caller's own value, and claim values never appear in INFO+ logs or Redis key names, in `gateway/system-policies/budget/budget.go`
- [ ] T082 [US2] Extend the "+ Add owner" dimension select with Application (searchable list via `portals/ai-workspace/src/apis/applicationApis.ts`, scoped to the project in project mode) and Token claim (free-text claim name, then value or "Each"; one row per claim name) in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDialog.tsx`
- [ ] T083 [P] [US2] Component tests for claim/application owner rows (incl. duplicate claim name blocked, project dimension hidden in project mode) in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDialog.test.tsx`

- [ ] T120 [P] [US2] Gateway IT `gateway/it/features/budgets-combined-owners.feature`: project-context budget with `sub = each` + provider = each refuses one user–provider combination while the same user's other provider and other users stay allowed; org budgets `sub = alice` + provider = openai and `sub = alice` + provider = anthropic are independent; a specific-value `sub = alice` budget with a different dimension set (e.g. `sub = alice` + provider = openai) still stacks with a `sub = each` budget; Redis holds keys only for combinations with traffic (quickstart Scenario 3a)
- [ ] T124 [P] [US1] Gateway IT `gateway/it/features/budgets-multi-value.feature`: org budget `LLM_PROVIDER ∈ {openai-mock, anthropic-mock}` $0.03/DAY — calls to both providers add to one counter and are refused together at $0.03, a third provider is not counted; a `LLM_PROVIDER ∈ {openai-mock}` $0.01 budget overrides it for OpenAI only; `team = search` + provider ∈ {openai-mock, anthropic-mock} shares one allowance across both providers for that team (quickstart Scenario 3d)
- [ ] T123 [P] [US2] Gateway IT `gateway/it/features/budgets-model.feature` (depends on T013a): org budget `sub = each` + model = gpt-4o $0.02 refuses alice's gpt-4o calls while her other-model calls pass; a proxy that rewrites `fast` → `gpt-4o-mini` is counted under `gpt-4o-mini`, not `fast`; `model = each` keeps one counter per model; `sub = each` $0.10 and `sub = alice` $0.50 → alice governed by $0.50 only (override with claims) (quickstart Scenario 3c)

**Checkpoint**: Claim and application owners work alongside and combined with US1 owners

---

## Phase 5: User Story 3 - Warnings before the money runs out (Priority: P1)

**Goal**: Up to five thresholds per budget; each crossing is detected by the gateway after `INCRBY`, deduplicated across all gateways with a Redis `SET NX` marker keyed by amount version, and carried on the crossing request's analytics event to Moesif (`budget_threshold_crossed`); every AI request event carries `budget_*` fields and `llm_cost_status`.

**Independent Test**: Create a $10 budget with 50 % and 80 % thresholds, push spend past $8 through two runtime replicas, verify exactly two events with `budget_threshold_crossed` (50, 80) in the period; raising the amount re-arms; lowering it so several thresholds are exceeded emits only the highest (quickstart Scenario 9).

### Tests for User Story 3 ⚠️

- [ ] T084 [P] [US3] Threshold marker tests with miniredis in `gateway/system-policies/budget/thresholds_test.go`: one emission per budget × period × value × threshold × amountVersion across two policy instances racing on the same counter; a charge reaching several thresholds emits only the highest and silently sets lower markers; a new `amountVersion` re-arms all thresholds; marker TTL = period end + 1 day; local marker cache avoids repeated `SET NX`; `SET NX` error → WARN, no emission, `budget_threshold_events_total{outcome="marker_error"}`
- [ ] T085 [P] [US3] Analytics field tests for the phase 2 fields (`budget_owners`, `budget_owner_values` — the caller's own values for "each" owners, canonical order —, `budget_period`, `budget_amount`, `budget_spend`, `budget_percent_used`, `budget_enforcement_mode`, `budget_refused`, `budget_monitor_would_refuse`, `budget_threshold_crossed` only on the winning event, `budgets` JSON, `budget_overridden_ids` only when an override applied and overridden budgets absent from all other fields; refused requests also emit an event; flat fields describe the crossing budget when a threshold was crossed; all published as Moesif `metadata.*`; `llm_cost_status`, `budget_check_skipped`, `budget_id`, `budget_name` are covered by T115) in `gateway/system-policies/analytics/analytics_budget_test.go`
- [ ] T086 [P] [US3] Gateway IT `gateway/it/features/budgets-thresholds.feature` using the analytics steps in `gateway/it/steps_analytics.go`: two replicas, thresholds 50/80 → exactly one event per threshold; amount raised → thresholds re-armed; amount lowered → only the highest emitted

### Implementation for User Story 3

- [ ] T087 [US3] Implement threshold detection in `gateway/system-policies/budget/thresholds.go`: after `Charge`, compute the highest reached threshold per counter from the new total, `SET <marker> 1 NX EXAT <periodEnd+86400>` (lower reached markers set silently in the same pipeline), bounded LRU marker cache keyed `(budgetUuid, periodLabel, valueKey, amountVersion)` (valueKey = the "each" owner combination), INFO log per emission, `budget_threshold_events_total{outcome}`; call it from the response phase in `gateway/system-policies/budget/budget.go`
- [ ] T088 [US3] Write per-request budget evaluation results (all evaluated budgets, spend after charge, refused/would-refuse, crossed threshold, skipped) to request metadata for analytics in `gateway/system-policies/budget/budget.go`
- [ ] T089 [US3] Emit the remaining `budget_*` flat fields (`budget_owners`, `budget_owner_values`, `budget_period`, `budget_amount`, `budget_spend`, `budget_percent_used`, `budget_enforcement_mode`, `budget_refused`, `budget_monitor_would_refuse`), `budget_threshold_crossed` and the `budgets` JSON (forwarded to Moesif `metadata.*` by T114; `llm_cost_status` already emitted by T115) in `gateway/system-policies/analytics/analytics.go` and `gateway/gateway-runtime/policy-engine/internal/analytics/analytics.go` (+ `internal/analytics/dto/`), and exempt events with budget fields (at least crossings and refusals) from Moesif dynamic sampling in `gateway/gateway-runtime/policy-engine/internal/analytics/publishers/`
- [ ] T116 [P] [US3] Moesif alert rule template "BUDGET: Threshold crossed" (one per organization: events where `metadata.budget_threshold_crossed` exists, group by `metadata.budget_name`, `metadata.budget_threshold_crossed`, `metadata.budget_owner_values`, count > 0 over 15 minutes; optional per-budget routing copies) per contract gateway-budget-enforcement §4.2 in `docs/gateway/budgets/moesif/alert-threshold-crossed.json`, with setup steps in `docs/gateway/budgets/moesif-alerts.md`
- [ ] T090 [US3] Threshold chips in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDialog.tsx` (max 5, 1–100, unique; default 50/80/100) with a hint "Warnings are delivered by your Moesif alert rule" linking to the alert-setup guide (T104)

**Checkpoint**: Warnings flow to Moesif exactly once per threshold across all gateways; request events carry budget fields

---

## Phase 6: User Story 4 - Roll out safely with monitor mode (Priority: P2)

**Goal**: `MONITOR` budgets are counted in Redis and warn but never refuse; switching to `ENFORCE` uses the already-counted spend immediately.

**Independent Test**: Create a $1 (quickstart: $0.01) monitor budget, exceed it; requests keep returning 200 with `Budget-Monitor: would-refuse`; switch to ENFORCE and the next request is refused (quickstart Scenario 6).

### Tests for User Story 4 ⚠️

- [ ] T091 [P] [US4] Policy tests: exhausted monitor budget allows, sets `Budget-Monitor: would-refuse` and analytics `budget_monitor_would_refuse = true`; monitor spend is counted in Redis; `include_usage` also injected when only a monitor-mode budget applies (FR-016a); Redis down with only monitor budgets → allowed, never 503 (assertion moved here from T053); mode switch takes effect on next request with existing spend, in `gateway/system-policies/budget/monitor_test.go`
- [ ] T092 [P] [US4] Gateway IT `gateway/it/features/budgets-monitor.feature` (monitor exceed → 200 + header; update to ENFORCE → 429; Redis stopped with only a monitor-mode budget → request passes, moved here from T058)

### Implementation for User Story 4

- [ ] T093 [US4] Accept `enforcementMode` `MONITOR` on create/update (free edit, spend unaffected, audited) in `platform-api/internal/service/budget_service.go`
- [ ] T094 [US4] Implement monitor semantics (evaluate and charge, never refuse, would-refuse header and metadata, never 503) in `gateway/system-policies/budget/budget.go`
- [ ] T095 [US4] Add the Enforce/Monitor selector with mode-combination help text (track only / track + warn / track + block / track + warn + block, default enforce + thresholds) to `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDialog.tsx`

**Checkpoint**: Monitor mode usable for safe rollout

---

## Phase 7: User Story 5 - See budgets and find their usage in analytics (Priority: P2)

**Goal**: The Budgets page shows each budget's configuration and status — no spend — and links each budget to Insights / Moesif for usage (FR-024, FR-024c).

**Independent Test**: As an operator, the Budgets page lists budgets with context, owners, amount, period, mode, thresholds and status (Active / Monitor / Disabled with reason), shows no spend figures, offers no edit actions, and each "See usage" link opens Moesif filtered to the budget where supported (quickstart Scenario 10).

- [ ] T096 [P] [US5] Component tests in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/Budgets.test.tsx`: list columns (name, context in org mode, owners summary e.g. "Project-a · each user · OpenAI", amount, period, mode, thresholds, status label Active / Monitor / Disabled), `OWNER_DELETED` explanation, no spend/percentage/near-limit rendering anywhere, "See usage in Insights / Moesif" link URL, read-only view without `ap:budget:manage`
- [ ] T097 [US5] Implement a usage-link helper that opens the **LLM Analytics** page of the embedded Moesif analytics under `APIP_AIW_MOESIF_ANALYTICS_URL` (T119) with the **Budget** filter set to the budget (`metadata.budget_id`; path and query format per the analytics team's answer, plan risk 12), falling back to the base URL, in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/usageLink.ts` (depends on T119)
- [ ] T119 [US5] Add AI Workspace runtime config key `APIP_AIW_MOESIF_ANALYTICS_URL` (embedded analytics base URL, default `https://www.moesif.com/wrap/basic`) in `portals/ai-workspace/src/config.env.ts` and use it instead of the hard-coded URL in `portals/ai-workspace/src/pages/appShell/appShellPages/insights/Main.tsx`; record the analytics team's answer on the LLM Analytics path and Budget filter query format in `docs/gateway/budgets/index.md` (open question, plan risk 12)
- [ ] T117 [P] [US5] Moesif Budget Usage card template for the existing LLM Analytics page (no new page/section/sidebar item): `max(metadata.budget_spend)`, `max(metadata.budget_amount)`, `max(metadata.budget_percent_used)` and count where `metadata.budget_refused = true`, grouped by `metadata.budget_name` and `metadata.budget_period`, plus `metadata.budget_owner_values` rows when the Budget filter selects a budget with "each" owners (FR-024e, mockups m1/m2) in `docs/gateway/budgets/moesif/llm-analytics-budget-usage-card.json` (delivery phase 2; needs T089)
- [ ] T118 [P] [US5] Moesif Budget filter template next to the existing API filter on the LLM Analytics page, filtering on `metadata.budget_id` with `metadata.budget_name` labels (FR-024e) in `docs/gateway/budgets/moesif/llm-analytics-budget-filter.json` (delivery phase 2; needs T089)
- [ ] T098 [US5] Budgets list page (org mode: all budgets with a Context column; project mode: that project's budgets; configuration columns incl. an Owners summary, status labels, enable/disable/delete for managers, "See usage" link per row; budgets with "each" owners rendered as e.g. "each user gets $25 / day") in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetsList.tsx`
- [ ] T099 [US5] Budget detail page (description, context, owners list (dimension, values — e.g. "OpenAI or Anthropic" — or Each), amount, period, mode, thresholds, status, created/updated by/at, "See usage in Insights / Moesif" link; for `OWNER_DELETED` render "Disabled — its project / LLM provider / LLM proxy / application was deleted" naming the deleted owner, with Delete for managers) in `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetDetail.tsx`

**Checkpoint**: All user stories independently functional

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Deployments with Redis, docs, metrics, configuration templates, hardening and end-to-end validation

- [ ] T100 [P] Gateway runtime metrics `budget_checks_total{result="allowed|refused|monitor_would_refuse|unavailable|skipped", context_type}`, `budget_redis_request_duration_seconds{op}`, `budget_redis_errors_total{op}`, `budget_charge_failures_total`, `budget_threshold_events_total{outcome}`, `budget_unpriced_requests_total`, `budget_redis_config_unsafe` (low-cardinality labels only) in `gateway/system-policies/budget/metrics.go` registered via `gateway/gateway-runtime/policy-engine/internal/metrics/metrics.go`
- [ ] T101 [P] Add `[budgets] enabled` and `[policy_configurations.budgets_v1]` (`failure_mode`, `config_check_interval`, `redis` block with TLS/mTLS keys and env-referenced password) with explanatory comments (one Redis per org's gateways, `noeviction`, AOF) to `gateway/configs/config-template.toml`, and enable it against the compose Redis in `gateway/configs/config.toml`
- [ ] T102 [P] Add a `redis` service (`redis:7` with `redis-server --appendonly yes --maxmemory-policy noeviction --requirepass ${REDIS_PASSWORD}`, named volume, healthcheck) and gateway-controller/runtime `depends_on` to `gateway/docker-compose.yaml` and `gateway/distribution/docker-compose.yaml`, with `REDIS_PASSWORD` in `gateway/local.env`
- [ ] T103 [P] Helm: add `gateway.budgets.enabled`, `gateway.budgets.failureMode` and `gateway.budgets.redis` (host, port, db, username, `existingSecret` for password and TLS CA/client cert/key, TLS flags) to `kubernetes/helm/gateway-helm-chart/values.yaml`; render `[budgets]` and `[policy_configurations.budgets_v1(.redis)]` in `kubernetes/helm/gateway-helm-chart/templates/gateway/gateway-config.yaml` (fail rendering when budgets are enabled without Redis); optional bundled Redis (`gateway.budgets.redis.bundled.enabled`: StatefulSet + PVC + Service + ConfigMap with AOF and `noeviction`) in `kubernetes/helm/gateway-helm-chart/templates/gateway/redis/`; mount secrets in `kubernetes/helm/gateway-helm-chart/templates/gateway/gateway-runtime/deployment.yaml`; document in `kubernetes/helm/gateway-helm-chart/README.md`
- [ ] T104 [P] User guide `docs/gateway/budgets/index.md` (concepts, owners: context (org/project mode), appended owners incl. provider vs proxy, values lists (any of, one shared allowance) vs each, AND semantics and stacking, periods, evaluation order, 429/503 contract, headers, unpriced requests, overshoot = in-flight requests, monitor mode, viewing usage in Insights/Moesif, rollback via `budgets.enabled`) and `docs/gateway/budgets/moesif-alerts.md` (exactly: the Budget Usage card and Budget filter on the existing LLM Analytics page (T117, T118) and one org-wide alert rule "BUDGET: Threshold crossed" on `metadata.budget_threshold_crossed` (T116), with optional extra rules only for routing; sampling exemption; warnings require analytics enabled)
- [ ] T105 [P] Redis guide `docs/gateway/budgets/redis.md`: Redis is required; all gateways of an organization must share one Redis (multi-region/hybrid caveat); TLS/mTLS and ACL auth; `maxmemory-policy noeviction` and AOF persistence; key schema and sizing; behaviour on outage (`failure_mode`) and on data loss (period spend restarts at zero); the unsafe-config warning
- [ ] T106 [P] Migration guide from `llm-cost-based-ratelimit` (incl. sharing one Redis with a different key prefix) and the LLM proxy redeploy needed for the project-id annotation (T005) in `docs/gateway/budgets/migration.md`
- [ ] T107 [P] Add Budgets endpoints (definitions only) to the REST API docs under `docs/rest-apis/`
- [ ] T108 (optional, pending decision on plan risk 9) Report budget enforcement capability from gateways on connect and warn in the Budgets API/UI when an org has budgets and connected gateways do not enforce them, in `platform-api/internal/service/budget_service.go` and `portals/ai-workspace/src/pages/appShell/appShellPages/budgets/BudgetsList.tsx`
- [ ] T109 Security review: no claim values, API keys, tokens or Redis credentials in INFO+ logs or Redis key names; Redis TLS verification on by default; strict DTO decoding; 404 indistinguishable across orgs; read-only mode enforced in service/repository; client-facing headers platform-neutral (`Budget-*`) and 503 body without infrastructure detail — across `gateway/system-policies/budget/` and `platform-api/internal/{service,handler,repository}/budget_*.go`
- [ ] T110 (Delivery phase 3) Implement `OnStreamError` charging (usage reported so far, else unpriced) once the SDK hook from T010 exists, in `gateway/system-policies/budget/stream.go`, with tests in `gateway/system-policies/budget/stream_test.go`
- [ ] T111 Performance check of budget evaluation (< 5 ms p95 added latency including Redis round-trips, SC-008) with ~1,000 budgets (incl. budgets with 5+ owners) and 10k "each" value combinations using `gateway/perf/performance-test-scripts/` and `gateway/docker-compose-perf.yaml` (+ Redis), plus a Go benchmark in `gateway/system-policies/budget/matcher_bench_test.go`
- [ ] T112 Run all quickstart.md scenarios (0–15) against the local stack (`gateway/docker-compose.yaml` with Redis + platform-api + AI Workspace) and record results, including the SC-001 create-a-budget time from Scenario 14 (must be under 2 minutes), in `docs/gateway/budgets/validation.md`
- [ ] T113 Validate SC-006 (Redis counter equals the sum of per-request `metadata.aiMetadata.llmCost` for the budget and period in Moesif, differences explained only by `budget_charge_failures_total` / `budget_check_skipped`) and SC-007 (each crossing event in Moesif < 2 min, exactly once across replicas) per quickstart Scenario 13; record the measurements in `docs/gateway/budgets/validation.md`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: no dependencies
- **Foundational (Phase 2)**: depends on Setup — BLOCKS all user stories. T005 (project-id annotation) and T008 (upstream provider UUID) are hard prerequisites for project and provider budgets on proxy traffic; T014–T018 (Redis config, client, safety check, key schema, activation check) block all gateway enforcement work.
- **US1 (Phase 3)**: depends on Foundational. MVP.
- **US2 (Phase 4)**, **US3 (Phase 5)**, **US4 (Phase 6)**: depend on Foundational and on US1's service/policy core (T060–T071), since they extend the same files; otherwise independent of each other.
- **US5 (Phase 7)**: depends on US1's API client (T072) and route (T074) only; no backend dependency beyond the definitions API.
- **Polish (Phase 8)**: T101–T103 (config, compose, Helm Redis) are needed to ship Delivery phase 1; T110 depends on the external SDK hook (T010).

### User Story Dependencies

- **US1 (P1)**: after Foundational — no dependency on other stories
- **US2 (P1)**: after US1 core — independently testable via org budgets with a claim owner only
- **US3 (P1)**: after US1 core (counter `Charge` returns totals) — independently testable via thresholds + analytics mock
- **US4 (P2)**: after US1 core — independently testable via a monitor-only budget
- **US5 (P2)**: after T072/T074 — independently testable with any budgets

### Within Each User Story

- Tests first (fail), then models/repositories → services → handlers/endpoints → gateway policy → UI
- platform-api, gateway-controller and policy work within a story can proceed in parallel once contracts (Phase 2) are fixed

### Parallel Opportunities

- Setup: T002, T004 in parallel
- Foundational: T006/T007/T009/T010/T011 prerequisites; T012/T013 helpers; T017 key schema; T020/T021/T022/T023 dialect DDL; T025/T026 models; T028/T029; T035/T036/T038 controller; T045/T046/T047 IT harness
- US1: all tests T048–T059 in parallel; T065 and T066 in parallel; UI T072 in parallel with backend work
- US2/US3/US4 can be staffed in parallel after US1 core, coordinating edits to `budget_service.go` and `budget.go`; US5 in parallel with all of them
- Polish: T100–T107 in parallel

---

## Parallel Example: User Story 1

```bash
# Launch US1 tests together:
Task: "Service validation tests in platform-api/internal/service/budget_service_test.go"
Task: "Policy matcher tests in gateway/system-policies/budget/matcher_test.go"
Task: "Redis counter tests in gateway/system-policies/budget/counter_test.go"
Task: "Redis failure tests in gateway/system-policies/budget/failure_test.go"
Task: "Gateway IT gateway/it/features/budgets.feature"

# Launch independent US1 implementation pieces together:
Task: "Budget matcher in gateway/system-policies/budget/matcher.go"
Task: "Redis counter in gateway/system-policies/budget/counter.go"
Task: "Budget API client in portals/ai-workspace/src/apis/budgetApis.ts"
```

## Parallel Example: User Story 3

```bash
Task: "Threshold marker tests in gateway/system-policies/budget/thresholds_test.go"
Task: "Analytics field tests in gateway/system-policies/analytics/analytics_budget_test.go"
Task: "Gateway IT gateway/it/features/budgets-thresholds.feature"
```

---

## Implementation Strategy

### MVP First (Delivery Phase 1)

1. Phase 1 Setup
2. Phase 2 Foundational, including prerequisites T005 (LLM proxy project-id annotation), T008 (upstream provider UUID), T011 (phase-ordering test) and the Redis foundations T014–T018
3. Phase 3 US1 — org- and project-context budgets with project / LLM provider / LLM proxy owners (values list or each), enforce mode, Redis counters, 429 + Retry-After, 503 / fail-open, streaming + `include_usage`, loopback skip, analytics plumbing and unpriced / fail-open marking (T114, T115)
4. Phase 7 US5 — configuration-only Budgets page with usage links (T096–T099, T119)
5. Polish items needed to ship: T100–T106, T109
6. **STOP and VALIDATE**: quickstart Scenarios 0, 1, 2, 5, 7, 8, 10, 11, 12, 14

### Incremental Delivery

1. Delivery Phase 1 (above) → demo
2. Delivery Phase 2: US2 → US3 → US4, then the Moesif templates T116–T118 (each independently testable; quickstart Scenarios 3, 4, 6, 9, 13, 15; T113)
3. Delivery Phase 3: T110 `OnStreamError` charging when the SDK hook lands

### Parallel Team Strategy

1. Whole team: Setup + Foundational (split: platform-api / controller / policy runtime + Redis / UI)
2. Then: Dev A platform-api US1 → US2 service parts; Dev B policy US1 → US3/US4; Dev C AI Workspace US1 → US5; Dev D compose/Helm Redis + docs

---

## Notes

- [P] tasks = different files, no dependencies on incomplete tasks
- [USn] labels map tasks to spec.md user stories for traceability
- DB rules R0–R10: one new table (`budgets`) plus one nullable column on the shipped `audit` table with its guarded per-dialect `ALTER` (T023); identical across dialects, no `CHECK` enums
- Removed with the 2026-10-09 architecture change (no tasks): `budget_period_spend`, `budget_sync_cursors`, usage ingest/aggregator/outbox, `budget.usage`/`budget.totals`, replica source id and `POD_NAME`, sync intervals, disconnect alerts, purge job, Moesif Actions publisher, `currentPeriod`/`perValueSummary`/`GET /budgets/{id}/values`, meters and per-value table
- Open items to resolve with architecture/product leads before or during Phase 1 (plan.md risks 8, 9, 11): single `budgets` definitions table vs "avoid new tables"; built-in system policy vs auto-attached policy; shared Redis for multi-region/hybrid gateways
- Commit after each task or logical group; stop at each checkpoint to validate the story independently
