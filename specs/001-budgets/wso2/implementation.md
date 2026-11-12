# **Budgets - Implementation**

# **1\. Overview**

Budgets add an organization-level money ceiling — an amount for an owner (organization, project, LLM provider, LLM proxy, application, or any token claim) over a UTC calendar period, in enforce or monitor mode, with warning thresholds — to every LLM provider and LLM proxy route of the AI Gateway. Definitions are owned by platform-api and travel to gateways as data, never as policy parameters, so changing a budget never redeploys anything. Spend is counted **only** in a shared Redis that every gateway of the organization uses; the control plane holds no spend. Threshold crossings are detected by the gateway and published on the crossing request's analytics event; usage, warnings and alerts live in the embedded Moesif analytics.

```text
 AI Workspace Budgets page (configuration only) ──REST /api/v0.9/budgets──┐      "See usage" ──► Moesif LLM Analytics (Budget filter)
                                                                          ▼
 platform-api: budgets (definitions, amount_version) · audit.details ── WS budget.* ──► gateway-controller
            ▲ GET /api/internal/v1/budgets (resync on connect) ◄──────────────────────── (budgets copy → LazyResources "Budget")
                                                                                              │ xDS
 gateway-runtime replica × N (every gateway of the org): [wso2_apip_sys_budget][analytics][auth]…[llm-cost]
   request body : match budgets → read counters → 429 | 503 | inject include_usage
   response end : x-llm-cost → INCRBY → threshold SET NX → analytics metadata.budget_* ──► Moesif ── org-wide alert rule
                                  ▼
                 shared Redis (required; noeviction + AOF; TLS/auth): counters and threshold markers
```

The work splits into five streams, each reviewable on its own:

* **platform-api changes** (§2): one new `budgets` definitions table plus a nullable `audit.details` column; a new public REST resource `/api/v0.9/budgets` with scopes `ap:budget:manage` / `ap:budget:read`; `budget.*` websocket broadcasts; an internal resync endpoint; owner-deleted handling; read-only-mode enforcement in the service and repository layers. No spend, no Moesif publishing.
* **gateway-controller changes** (§3): receive and persist definitions, publish them as a LazyResource, resync on connect, validate that budgets are never enabled without Redis, and inject the budget system policy when enabled.
* **Gateway runtime changes** (§4): the new prepended, streaming-capable system policy `wso2_apip_sys_budget` with the Redis counters, threshold markers, refusal contract and `include_usage` injection; the analytics pipeline change that forwards budget fields to Moesif; the Moesif templates; Redis in the shipped Docker Compose and Helm deployments.
* **AI Workspace changes** (§5): a configuration-only Budgets page, create/edit dialog and detail page, and a configurable analytics base URL for "See usage" links.

**Preconditions an operator must satisfy before budgets are enforced on a gateway:**

* A Redis reachable from every replica of every gateway of the organization, ideally with `maxmemory-policy noeviction` and AOF persistence, configured identically on all of them under `[policy_configurations.budgets_v1.redis]`.
* `budgets.enabled = true` on the gateway (default `false`), then a restart of the gateway-controller and runtime.
* LLM proxies redeployed after the upgrade so they carry their project id (otherwise project budgets do not match their traffic).
* Analytics (Moesif) enabled on the gateways for threshold warnings and usage; the Budget Usage card, Budget filter and alert rule applied from the shipped templates.

# **2\. platform-api (Control Plane) Implementation**

## **2.1 API Changes**

Five new public operations on a new `Budgets` tag — `ListBudgets`, `CreateBudget`, `GetBudget`, `UpdateBudget`, `DeleteBudget` under `/api/v0.9/budgets` — returning budget **definitions only** (no spend), two new scopes (`ap:budget:manage`, `ap:budget:read`), one internal endpoint `GET /api/internal/v1/budgets` for gateway resync, and three websocket message types `budget.created|updated|deleted`. The full contract is in the REST APIs document.

| Area | File | Change |
| :---- | :---- | :---- |
| Contract | `platform-api/resources/openapi.yaml` | Budgets paths, schemas and scopes; models regenerated via `platform-api/oapi-codegen.yaml` |
| Roles | `platform-api/resources/role-to-scope-mapping.yaml` | `ap:budget:manage` → `ap_admin`; `ap:budget:read` → `ap_operator` (and `ap_admin`) |
| Model | `platform-api/internal/model/budget.go`, `budget_event.go` | `Budget` (incl. `AmountVersion`, `StatusReason`), `BudgetOwner` with canonical ordering and `OwnersHash()`, Go constants for context types, owner dimensions, periods, modes, status and status reasons; websocket payloads |
| Repository | `platform-api/internal/repository/budget_repository.go` | CRUD, list with `query` / `project` / `contextType` / `status` / `enforcementMode` filters and pagination, list-by-project; owners stored as canonical JSON; unique violations mapped to a typed conflict |
| Audit | `platform-api/internal/repository/audit.go` | New `RecordWithDetails(action, resourceUUID, resourceType, orgUUID, performedBy string, details []byte)`; existing `Record` unchanged (writes `NULL`) |
| Service | `platform-api/internal/service/budget_service.go` | Validation, handle generation, context and owner resolution and canonical ordering, `owners_hash`, override computation (`overrides` / `overriddenBy`, `BUDGET_OVERRIDES_OTHER` warning), `amount_version`, audit, broadcast, owner-deleted disable, read-only mode |
| Handler | `platform-api/internal/handler/budget_handler.go`; `platform-api/internal/server/server.go` | Routes with scope checks, strict DTO decoding, wrapped in `middleware.MapErrors` |
| Errors | `platform-api/internal/apperror/codes.go`, `catalog.go` | `BUDGET_INVALID`, `BUDGET_INVALID_OWNER`, `BUDGET_OWNER_DUPLICATE`, `BUDGET_OWNER_NOT_ALLOWED`, `BUDGET_OWNER_NOT_FOUND`, `BUDGET_ALREADY_EXISTS`, `BUDGET_IMMUTABLE_FIELD`, `BUDGET_NOT_FOUND` (400/400/400/400/404/409/400/404) |
| Gateway events | `platform-api/internal/service/gateway_events.go` | `BroadcastBudgetEvent(orgID, eventType, payload)` to every gateway of the organization |
| Internal API | `platform-api/internal/handler/gateway_internal.go`, `platform-api/internal/service/gateway_internal.go`, `platform-api/config/default_config.go` (`SkipPaths`), `platform-api/resources/gateway-internal-api.yaml` | `GET /api/internal/v1/budgets`, allowed in read-only mode |
| LLM proxy deploy | `platform-api/internal/service/llm_deployment.go` | Set annotation `gateway.api-platform.wso2.com/project-id` in `generateLLMProxyDeploymentYAML` (prerequisite for project budgets on proxy traffic) |

## **2.2 Request Validation**

Error bodies use the platform's standard `{status, code, message}` shape with generic messages; the specific reason is logged only. This table is the source for the Test Scenarios document, §4, row for row.

| Validation | Expected behavior |
| :---- | :---- |
| `displayName` omitted, empty, or > 255 chars | 400 `BUDGET_INVALID` |
| `description` > 1023 chars | 400 `BUDGET_INVALID` |
| `amount` omitted, ≤ 0, not a decimal string, > 12 integer digits or > 6 fractional digits | 400 `BUDGET_INVALID` |
| `currency` present and not `USD` | 400 `BUDGET_INVALID` |
| `period` not `DAY` / `WEEK` / `MONTH` | 400 `BUDGET_INVALID` |
| `enforcementMode` not `ENFORCE` / `MONITOR` | 400 `BUDGET_INVALID` |
| `thresholds`: more than 5, duplicates, or a value outside 1–100 | 400 `BUDGET_INVALID` |
| `context` omitted, `context.type` not `ORGANIZATION` / `PROJECT`, `PROJECT` without `project`, or `ORGANIZATION` with `project` | 400 `BUDGET_INVALID_OWNER` |
| An owner with unknown `dimension`, with both or neither of `values` / `each`, an empty `values` list or duplicate values, or `CLAIM` without `claimName` / `claimName` not matching `^[A-Za-z0-9_.:-]{1,255}$` | 400 `BUDGET_INVALID_OWNER` |
| The same `dimension` twice, or `CLAIM` twice with the same `claimName` | 400 `BUDGET_OWNER_DUPLICATE` |
| A `PROJECT` owner on a budget whose context is a project | 400 `BUDGET_OWNER_NOT_ALLOWED` |
| `context.project` or any listed owner value names a project / LLM provider / LLM proxy / application that does not exist in the caller's organization, or (project context) a proxy / application of another project | 404 `BUDGET_OWNER_NOT_FOUND` (same response whether it exists in another organization or not at all) |
| An `LLM_PROVIDER` / `LLM_PROXY` listed value resolves to an artifact of a different kind | 400 `BUDGET_INVALID_OWNER` |
| A budget with the same context, owners and period already exists | 409 `BUDGET_ALREADY_EXISTS` |
| `handle` already used in the organization | 409 `BUDGET_ALREADY_EXISTS` |
| Unknown request fields | 400 `BUDGET_INVALID` (strict DTO decoding — no mass assignment) |
| Update changes the context, the owners, or the period | 400 `BUDGET_IMMUTABLE_FIELD` |
| `budgetId` not found in the caller's organization | 404 `BUDGET_NOT_FOUND` |
| Query params: `limit` outside 1–100, unknown `status` / `contextType` / `enforcementMode` value | 400 `BUDGET_INVALID` |

## **2.3 Storage Model**

One new table, `budgets`, holds the definitions (one row per budget; money as integer nano-USD in `amount_nano`; `amount_version` incremented by the service on every amount change; `threshold_percents` as an ascending CSV; `context_type` and `project_uuid` for the first owner; the appended owners as a canonical JSON column `owners` with `owners_hash` for uniqueness; `status_reason` for platform-set status). One nullable `details` column is added to the shipped `audit` table for before/after values. Nothing about spend, counts or threshold state is stored in platform-api. Enumerated values are validated against Go constants (no `CHECK` constraints). The full DDL, indexes and upgrade path are in the Proposed DB Schema document, §2.

## **2.4 Request Handling**

### **2.4.1 Create**

1. Decode strictly into `CreateBudgetRequest`; reject unknown fields.
2. Validate per §2.2. Take the organization and `created_by` from the verified token only.
3. Generate `handle` from `displayName` if omitted (max 40 characters).
4. Resolve `context.project` and every listed owner value (handles) to uuids within the caller's organization (`values` non-empty, no duplicates); for `LLM_PROVIDER` / `LLM_PROXY` check the artifact's `type` is `LlmProvider` / `LlmProxy`; in a project context, proxies and applications must belong to that project. Check each dimension appears once (`CLAIM` once per claim name) and that a project context has no `PROJECT` owner. `MODEL` values are checked for length (1–255) and control characters only, not against the model catalog. Sort owners canonically (`PROJECT`, `LLM_PROVIDER`, `LLM_PROXY`, `MODEL`, `APPLICATION`, then `CLAIM` by claim name), sort each owner's `values`, and compute `owners_hash`. No limit on the number of owners or values.
5. Compute overrides from the organization's definitions (same context, period and dimension set; this budget at least as narrow on every dimension — a values list is narrower than `each`, a subset list is at least as narrow — and narrower on at least one) and return a `BUDGET_OVERRIDES_OTHER` warning listing any budgets the saved budget overrides. Not an error.
6. Store thresholds as ascending CSV, amount as `amount_nano`, `amount_version = 1`.
7. Insert; map a unique violation to 409 `BUDGET_ALREADY_EXISTS`.
8. `auditRepo.RecordWithDetails("CREATE", uuid, "budget", org, actor, {"after": {...}})`.
9. Broadcast `budget.created` to every gateway of the organization.

Delivery phase 1 accepts both contexts and the `PROJECT`, `LLM_PROVIDER` and `LLM_PROXY` owner dimensions; `CLAIM` and `APPLICATION` owners are enabled in phase 2.

### **2.4.2 Update**

1. Load by handle **and** organization; 404 `BUDGET_NOT_FOUND` otherwise.
2. Accept only `displayName`, `description`, `amount`, `thresholds`, `enforcementMode`, `status`; reject a change of the context, the owners or the period with `BUDGET_IMMUTABLE_FIELD`.
3. If the amount changed, increment `amount_version` (this re-arms every threshold on the gateways; recorded spend is never touched). Name, description, mode or threshold edits do not change it.
4. An administrator status change clears `status_reason`.
5. Audit `UPDATE` with JSON `{before, after}` of the mutable fields (display name, description, amount, thresholds, enforcement mode, status, status reason).
6. Broadcast `budget.updated`.

`MONITOR ⇄ ENFORCE` is a free edit; the Redis counter is unaffected, so switching to enforce uses spend already counted.

### **2.4.3 Delete**

Hard delete of the row, audit `DELETE` with `{before}`, broadcast `budget.deleted` (payload `{budgetUuid}`). Redis counters and markers are left to expire (period end + 1 day); a re-created budget gets a new uuid and never sees old counters.

### **2.4.4 Owner deleted**

`BudgetService.DisableBudgetsForOwner` is called from the delete paths in `platform-api/internal/service/project.go`, `platform-api/internal/service/llm.go` (LLM provider and LLM proxy) and `platform-api/internal/service/application.go`. It finds budgets whose context project, or any owner's `values`, includes the deleted resource (even one of several values), sets `status = DISABLED` and `status_reason = OWNER_DELETED`, audits `UPDATE` with before/after and actor `system`, and broadcasts `budget.updated`. No analytics event is published.

### **2.4.5 Internal resync**

`GET /api/internal/v1/budgets` authenticates the gateway token with `authenticateRequest`, resolves organization and gateway server-side, and returns every non-deleted budget of that organization (including `DISABLED`) in the same shape as the websocket payload. It is a read and stays available in read-only mode.

### **2.4.6 Read-only mode**

`BudgetService` create, update, delete and `DisableBudgetsForOwner` return a read-only error, and the write methods of the budget repository refuse too, so every entry point is covered — not only the `*_readonly.go` handlers (GO-AUTH-015).

## **2.5 Error Handling**

| Condition | Status | Client-facing behaviour | Logged internally |
| :---- | :---- | :---- | :---- |
| Validation / not-found / conflict on `/budgets` | 400 / 404 / 409 | Codes in §2.2, generic message (the scope value is never echoed) | DEBUG: field and reason |
| Write attempted in read-only mode | NOT ANSWERED (status and code of the read-only error are not specified) | Generic message | NOT ANSWERED |
| Unexpected error on `/budgets` | 500 | Generic message with a random tracking id | ERROR with stack |
| Internal resync without or with an invalid gateway token | 401 | The internal API's existing error response | Existing gateway-auth logging |

## **2.6 Concurrency, Retries and Failure Modes**

* **Broadcast failure**: a gateway that misses a `budget.*` event receives the change on its next resync (on every connect). Handlers on the gateway side are idempotent (upsert by uuid, compare `updatedAt`).
* **Concurrent updates to the same budget**: the concurrency rule (last write wins or optimistic locking) is NOT ANSWERED — see §12.
* **Duplicate create**: the `UNIQUE (organization_uuid, context_type, project_uuid, owners_hash, period)` and `UNIQUE (organization_uuid, handle)` constraints make concurrent duplicates fail with 409.
* **platform-api restart or outage**: gateways keep enforcing with their last definitions and keep counting in Redis (FR-021); nothing about spend depends on platform-api.

# **3\. gateway-controller Implementation**

## **3.1 Changes**

| Area | File | Change |
| :---- | :---- | :---- |
| Control-plane client | `gateway/gateway-controller/pkg/controlplane/client.go`, `events.go` | Handle `budget.created|updated|deleted`; `syncBudgets(gwID)` on connect/reconnect, mirroring `syncSubscriptionPlans` |
| Fetch | `gateway/gateway-controller/pkg/utils/api_utils.go` | `FetchBudgets()` calling `GET /api/internal/v1/budgets` |
| Storage | `gateway/gateway-controller/pkg/storage/interface.go`, `sql_store.go`; schema files listed in the Proposed DB Schema document | Controller `budgets` table and CRUD |
| Model | `gateway/gateway-controller/pkg/models/budget.go` | Definition model matching the websocket payload |
| Resource publishing | `gateway/gateway-controller/pkg/utils/budget_resource.go` | `UpsertBudget` / `DeleteBudget`: persist, publish `EventTypeBudget`, `lazyResourceManager.StoreResource` / `RemoveResourceByIDAndType` with `ResourceType = "Budget"` (pattern `subscription_resource.go`) |
| Replica sync | `common/eventhub/types.go`; `gateway/gateway-controller/pkg/utils/replica_sync_dependencies.go` | New `EventTypeBudget` |
| Startup rebuild | `gateway/gateway-controller/pkg/lazyresourcexds/lazy_resource_manager.go` (or the bootstrap that seeds it) | Rebuild `Budget` LazyResources from the controller DB — the controller-side store is in-memory |
| Configuration | `gateway/gateway-controller/pkg/config/config.go`, `validator.go` | `[budgets] enabled` (default `false`) and the Redis-required startup check |
| Policy injection | `gateway/gateway-controller/pkg/utils/system_policies.go` | Add `wso2_apip_sys_budget` to `defaultSystemPolicies` (prepended to every chain) only when `budgets.enabled = true` |
| Route metadata | `gateway/gateway-controller/pkg/utils/llm_transformer.go`, `pkg/models/runtime_deploy_config.go` | Verify `project_id` route metadata for `LlmProxy` routes; add the upstream LLM provider artifact uuid to LLM proxy route metadata if missing; expose the resolved upstream model id to policies in the request and response phases (model owners) |

## **3.2 Definition Handling**

1. A `budget.*` message arrives on the existing authenticated websocket. The organization is never taken from the payload — the controller is already bound to one organization.
2. Upsert or delete by uuid, ignoring stale messages by `updatedAt`.
3. Persist to the controller DB, publish `EventTypeBudget` for the other controller replicas, and store or remove the LazyResource `{ID: <budget uuid>, ResourceType: "Budget", Resource: <budget map>}`.
4. On every (re)connect, fetch the full list and reconcile — delete locally what is absent.
5. On startup, rebuild the LazyResources from the controller DB before serving.

With `budgets.enabled = false` definitions are still received and stored, so enabling later needs no resync, but nothing is enforced on that gateway.

## **3.3 Activation Check**

`budgets.enabled = true` without `[policy_configurations.budgets_v1.redis]` fails configuration validation at startup with:

```
budgets.enabled requires [policy_configurations.budgets_v1.redis] (Redis is required for budgets)
```

The gateway does not start (FR-022). The budget policy factory refuses to initialise without the Redis block as a second guard. Logs: INFO on budget definition sync (count, organization); ERROR on budgets enabled without Redis.

# **4\. Gateway Runtime Implementation**

## **4.1 Structure**

```
gateway/system-policies/budget/            new Go module (added to go.work; go-redis v9, miniredis for tests)
  policy-definition.yaml                   wso2_apip_sys_budget: system policy, no user parameters,
                                           request body mode buffer, response body mode stream
  config.go          budgets_v1 config: failure_mode, config_check_interval, required redis block
  redis_client.go    go-redis client: TLS (CA, server name), mTLS client cert, ACL user/password; health ping
  redis_safety.go    startup + periodic maxmemory-policy / persistence check
  keys.go            counter and marker keys, valueKey hashing, expiry
  period.go          UTC labels and period start/end
  money.go           x-llm-cost string → nano-USD
  definitions.go     reads "Budget" LazyResources, builds the owner-value match index and override sets, skips DISABLED
  matcher.go         candidate lookup, AND check of every owner, override resolution, valueKey, evaluation order
  counter.go         pipelined read; pipelined INCRBY + EXPIREAT
  thresholds.go      highest-reached threshold, SET NX marker, local marker cache
  stream.go          include_usage injection (and OnStreamError charging, phase 3)
  budget.go          request-body and response phases, 429/503 builders, headers, analytics metadata
  metrics.go         budget_* metrics
gateway/system-policies/system-build-lock.yaml   registers wso2_apip_sys_budget (filePath: ./budget)
```

## **4.2 Entry Points**

| Phase | Mode | What runs |
| :---- | :---- | :---- |
| Request body | `BodyModeBuffer` | No-op unless `SharedContext.APIKind ∈ {LlmProvider, LlmProxy}` and the organization has budgets; skip requests carrying `x-wso2-internal-loopback`; resolve applicable budgets; one pipelined Redis read; refuse with `ImmediateResponse` 429 or 503, or allow; inject `include_usage`; stash the evaluation in request metadata |
| Response body / stream | `BodyModeStream` (`StreamingResponsePolicy`) | On `EndOfStream` (or the buffered body) read `Metadata["x-llm-cost"]` and `x-llm-cost-status`; one pipelined `INCRBY` + `EXPIREAT`; threshold detection; response headers; analytics metadata |

The policy is **prepended** to every chain. Request-header handlers (authentication) all run before any request-body handler, so the body phase sees the authentication context; response handlers run in reverse chain order, so the budget policy runs **last**, after `llm-cost` has set `x-llm-cost` — the same pattern `llm-cost-based-ratelimit` uses. A header-phase check is impossible because a prepended header handler runs before authentication. The response side must be a streaming policy: a buffer-only response policy would force the whole route into buffered mode and break token streaming. A phase-ordering test in `gateway/gateway-runtime/policy-engine/internal/executor/chain_phase_order_test.go` pins both ordering assumptions.

## **4.3 Matching and Evaluation Order**

| Owner dimension | Source in the policy context |
| :---- | :---- |
| Organization (context) | Implicit — a gateway belongs to one organization |
| Project (context or owner) | `SharedContext.ProjectID` (from the route's `project_id` metadata, i.e. the `gateway.api-platform.wso2.com/project-id` annotation). Direct LLM provider traffic has no project. |
| LLM proxy | `SharedContext.APIId` when `APIKind = LlmProxy` |
| LLM provider | `SharedContext.APIId` when `APIKind = LlmProvider`, or — on proxy traffic — the proxy's upstream provider |
| Model | The upstream model id the gateway sends to the provider, after any proxy model rewrite — the id used for pricing. Today the provider template's `requestModel` is extracted by the analytics policy; the budget policy needs it in the request phase (check) and response phase (charge) — gateway prerequisite. Exact, case-sensitive. |
| Application | `AuthContext.Properties["ApplicationID"]` / `Metadata["x-wso2-application-id"]` |
| Claim | `AuthContext.Subject` (for `sub`) and `AuthContext.Properties` (flattened claims), walking `AuthContext.Previous` for multi-layer authentication |

* **Matching (owner model, 2026-10-14)**: a budget applies when its context matches (organization: always; project: `ProjectID` equals the budget's project) and **every** appended owner matches — a values owner when the request's value is any of the listed values (OR within the owner; the listed values share one counter), an `each` owner when the request has any value for that dimension. Requests with no value for a dimension (e.g. no claims, or direct provider traffic for a project-context budget or a project owner) are not covered by budgets with an owner on that dimension.
* **Index, not scan**: `definitions.go` indexes budgets by each specific owner value (project, provider, proxy, model, application, claim name/value) and keeps budgets whose owners are all `each` (or that have only an organization context) in a per-dimension list. The matcher gathers candidates from the request's values and then checks every owner of each candidate. Matching cost grows with owners and budgets per organization; there is no limit on owners in v1, so the performance check covers budgets with many owners (SC-008, 5 ms p95).
* **Overrides — most specific wins (2026-10-14)**: among the matching budgets, budget B overrides budget A when they have the same context, period and dimension set (claims compared by claim name) and B is at least as narrow on every dimension (a values list where A has `each`, or B's values a subset of A's) and strictly narrower on at least one (a list vs `each`, or a proper subset — e.g. `[openai]` $500 overrides `[anthropic, openai]` $2,000 for OpenAI traffic). Overridden budgets are dropped before admission: not checked, not charged, no thresholds. Override sets are precomputed in `definitions.go` whenever definitions change (pairs within the same context, period and dimension-set group), so request-time cost is a set lookup. Partial specificity is not an override; enforcement mode does not affect precedence. Their ids are recorded as `budget_overridden_ids` on the analytics event.
* Order: budgets with more owners first, then smallest amount, then budget id. Every remaining applicable budget is evaluated (needed for the latest-reset `Retry-After`) and charged. Budgets with different dimension sets, contexts or periods stack, so organization caps always apply.
* `valueKey` is built from the request's values for the budget's `each` owners only, in canonical order; budgets with no `each` owner use `_`.
* Charge only on the outermost hop: the proxy → provider hop carries `x-wso2-internal-loopback` and skips the policy; the outer hop's applicable set includes both the proxy and its upstream provider (FR-016c).
* `DISABLED` budgets are not evaluated, so nothing is counted while disabled.

## **4.4 Redis Counters**

| Item | Rule |
| :---- | :---- |
| Counter key | `budget:v1:{<budgetUuid>}:<periodLabel>:<valueKey>` — integer nano-USD |
| `valueKey` | The request's values for the budget's `each` owners, in canonical order, joined with `:` — project / provider / proxy / application uuid, `m` + first 32 hex chars of SHA-256(model id) for an `each` model (model ids can contain `:` and `/`), and `h` + first 32 hex chars of SHA-256(claim value) for each `each` claim (no raw claim values in key names); `_` when the budget has no `each` owner; `x` + SHA-256 of the joined key when it exceeds 200 characters. Keys are created by the first `INCRBY`, so only combinations with traffic exist. |
| `periodLabel` | `2026-10-08` (DAY), `2026-W41` (WEEK, ISO), `2026-10` (MONTH) |
| Admission | One pipelined round-trip of `GET`s for every applicable counter. Missing key = 0. Enforce-mode budget refuses when counter ≥ amount. |
| Charge | One pipelined round-trip: `INCRBY <cost_nano>` (returns the new total) plus `EXPIREAT periodEnd + 86400` (`EXPIREAT … NX` on Redis ≥ 7, otherwise unconditional). Lands in the period in which the response finished. |
| Unpriced | No write (`INCRBY 0` skipped). |
| Monitor | Counted exactly like enforce. |
| Hash tag | `{budgetUuid}` keeps a budget's counters and markers in one slot, ready for Redis Cluster later. |

Redis operations per budgeted request: two pipelined round-trips, plus one `SET NX` only when a threshold is reached. If per-request round-trips prove costly, the charge step (never admission) can later adopt the asynchronous flush-coordinator pattern of the rate-limit Redis client.

## **4.5 Threshold Markers**

1. After `INCRBY` returns the new total `T` for a counter with amount `A` and thresholds `t₁ < … < tₙ`, find `h` = the highest `tᵢ` with `T ≥ tᵢ % × A`.
2. If `h` exists and the local marker cache does not already know `h` is marked for `(budgetUuid, periodLabel, valueKey, amountVersion)`, run `SET budget:v1:{<uuid>}:<period>:<valueKey>:thr:<h>:<amountVersion> 1 NX EXAT <periodEnd + 86400>`. In the same pipeline, set the markers of lower reached thresholds with `NX` without emitting them.
3. Only the `SET NX` winner adds `budget_threshold_crossed = h` to that request's analytics event and logs it at INFO. Losers emit nothing.
4. A new `amountVersion` means new marker keys, so an amount change re-arms every threshold; old markers expire.
5. The local cache is a bounded LRU; a miss only costs one extra `SET NX`.

Guarantee: exactly-once detection, at-most-once delivery — if the analytics event is lost after the marker is set, the warning is lost.

## **4.6 Refusal, Unavailable and Allowed Responses**

```http
HTTP/1.1 429 Too Many Requests
Retry-After: 2040420
Budget-Id: search-each-user-openai
Budget-Reset: 2026-11-01T00:00:00Z

{"status":"error","code":"BUDGET_EXHAUSTED",
 "message":"The budget 'Search – each user on OpenAI' is exhausted until 2026-11-01T00:00:00Z.",
 "details":{"budget":{"id":"search-each-user-openai","context":{"type":"PROJECT","project":"search"},
  "owners":[{"dimension":"LLM_PROVIDER","values":["openai-prod"]},{"dimension":"CLAIM","claimName":"sub","each":true,"callerValue":"alice@acme.com"}],
  "period":"MONTH","periodLabel":"2026-10","amount":"1500.00","currency":"USD","resetAt":"2026-11-01T00:00:00Z"}}}
```

* `code` is always `BUDGET_EXHAUSTED`, never the rate-limit code. `Retry-After` = whole seconds to `resetAt`; with several exhausted budgets, the latest reset. For `each` owners, refusals report only the caller's own values. Raised in the request-body phase, before anything reaches the upstream.
* Redis unreachable at admission with `failure_mode = "closed"` and an enforce-mode budget applicable: `503` with `{"status":"error","code":"BUDGET_CHECK_UNAVAILABLE","message":"The request could not be checked against its budget. Try again later."}` — no `Retry-After`, no Redis host or error detail.
* `failure_mode = "open"`: allowed, no `Budget-*` headers, not counted, analytics `budget_check_skipped = true`. Requests under monitor-only budgets always pass.
* Allowed responses carry, for the most constraining budget (lowest remaining at admission): `Budget-Id`, `Budget-Remaining` (USD, floored at 0), `Budget-Reset`, and `Budget-Monitor: would-refuse` for a monitor-mode budget that would have refused. For streams, the headers reflect admission. Header names are platform-neutral (`.claude/rules/error-handling.md` directive 2).

## **4.7 Streaming and `include_usage`**

When any budget (enforce or monitor) applies, the route is an OpenAI-compatible chat completion and the body has `"stream": true`, the policy sets `stream_options.include_usage = true` through `UpstreamRequestModifications{Body}`, preserving other `stream_options` fields. Clients receive one extra final SSE chunk with `usage` and empty `choices` (standard OpenAI behaviour). In-flight streams are never cut. Aborted streams are neither charged nor marked until the SDK offers an `OnStreamError` hook (delivery phase 3).

## **4.8 Redis Configuration, Safety Check and Failure Modes**

```toml
[budgets]
enabled = false

[policy_configurations.budgets_v1]
failure_mode = "closed"
config_check_interval = "10m"

[policy_configurations.budgets_v1.redis]   # same shape as advanced-ratelimit redis.*
host = "redis"
port = 6379
username = ""
password = "${REDIS_PASSWORD}"
db = 0
key_prefix = "budget:v1:"
dial_timeout = "500ms"
read_timeout = "100ms"
write_timeout = "100ms"
pool_size = 50

[policy_configurations.budgets_v1.redis.tls]
enabled = true
ca_file = "/etc/ssl/redis/ca.pem"
server_name = "redis"
cert_file = "/etc/ssl/redis/client.pem"
key_file = "/etc/ssl/redis/client-key.pem"
insecure_skip_verify = false
```

(The timeouts, pool size and TLS values above are the documented example; their defaults are listed in §6.)

* Every replica of every gateway of the organization must point at the same Redis and `db`. Different organizations may share a Redis (keys contain globally unique budget uuids).
* On startup and every `config_check_interval`: best-effort `CONFIG GET maxmemory-policy` and `INFO persistence`; WARN `budget redis may lose data: maxmemory-policy=<p>, aof=<a>, rdb=<r>` and set `budget_redis_config_unsafe = 1` when not `noeviction` or no persistence. If `CONFIG` is not permitted (managed Redis), log INFO once and continue.
* Secrets (password, client key) come from environment or file references, never inline in a ConfigMap.

| Condition | Client impact | Logged internally |
| :---- | :---- | :---- |
| Enforce-mode budget exhausted | 429 `BUDGET_EXHAUSTED` | DEBUG: budget id, scope type, period label (no claim values) |
| Redis unreachable at admission, `closed`, enforce budget applies | 503 `BUDGET_CHECK_UNAVAILABLE` | ERROR (rate-limited): Redis endpoint, error |
| Redis unreachable at admission, `open` | Allowed, uncounted, `budget_check_skipped = true` | WARN (rate-limited) |
| `INCRBY` fails at response end | None (response already sent); cost missing from the counter but present in Moesif | ERROR + `budget_charge_failures_total` |
| Threshold marker `SET NX` fails | None; the crossing may be emitted by a later request or missed | WARN + `budget_threshold_events_total{outcome="marker_error"}` |
| Redis configuration can lose data | None | WARN at startup and every interval; gauge `budget_redis_config_unsafe = 1` |
| Definitions not yet received after start | Requests allowed until definitions arrive | WARN once |
| Redis loses data | Period spend of affected budgets restarts at zero; requests allowed again | Covered by the unsafe-configuration warning |

## **4.9 Analytics Fields and Pipeline Change**

The budget policy writes these keys as analytics metadata; they are published under the Moesif event's `metadata` object as flat keys (`metadata.budget_spend`, `metadata.budget_threshold_crossed`, …). The flat `budget_*` fields describe the most constraining budget, except when the request crossed a threshold — then the crossing budget (highest-priority one if several). Refused requests also emit an event.

| Field | Type | Example / meaning |
| :---- | :---- | :---- |
| `budget_id` | string | `search-each-user-openai` |
| `budget_name` | string | `Search – each user on OpenAI` |
| `budget_owners` | string | `project=search; provider=anthropic-prod\|openai-prod; sub=each` — context first, then owners in canonical order; a multi-value owner's values joined with `\|` |
| `budget_owner_values` | string | `sub=alice@acme.com` — the caller's own values for the `each` owners; empty when there are none |
| `budget_overridden_ids` | string | `per-user-default` — comma-separated ids of matching budgets skipped because a more specific budget overrode them; absent when none |
| `budget_period` | string | `2026-10` |
| `budget_amount` | number | `1500.0` |
| `budget_spend` | number | Redis total after this request's `INCRBY` (before it, for refused requests) |
| `budget_percent_used` | number | `80.07` |
| `budget_enforcement_mode` | string | `ENFORCE` / `MONITOR` |
| `budget_refused` | boolean | |
| `budget_monitor_would_refuse` | boolean | |
| `budget_threshold_crossed` | integer | Present only on the one event, across all gateways, whose charge crossed the threshold |
| `budget_check_skipped` | boolean | `true` only for fail-open |
| `budgets` | JSON string | All evaluated budgets `[{id, period, ownerValues?, amount, spend, refused, wouldRefuse, thresholdCrossed?}]` |
| `llm_cost_status` | string | `calculated` / `not_calculated` |

The request cost stays the **existing** `metadata.aiMetadata.llmCost` (`gateway/gateway-runtime/policy-engine/internal/analytics/dto/aiMetaData.go`); there is no flat cost field. `INCRBY` runs before the analytics event is finalised, so spend and the crossing are on the same event as the cost.

**Pipeline change (required).** Policy-written budget keys are not forwarded today:

* `prepareAnalyticEvent` in `gateway/gateway-runtime/policy-engine/internal/analytics/analytics.go` reads policy-written `analytics_data` keys but copies only known ones (cost, guardrail, user id, auth) into `dto.Event.Properties`. It must copy `budget_*`, `budgets` and `llm_cost_status` with their JSON types (not stringified).
* The Moesif publisher `gateway/gateway-runtime/policy-engine/internal/analytics/publishers/moesif.go` builds the event `metadata` map from an allow-list of `Properties` keys. The budget keys must be added.
* Budget events — at least every event with `budget_threshold_crossed` or `budget_refused` — must be exempted from Moesif dynamic sampling in `gateway/gateway-runtime/policy-engine/internal/analytics/publishers/`.
* `gateway/system-policies/analytics/analytics.go` and `internal/analytics/dto/` carry the remaining fields.

Delivery phase 1 ships the pipeline change and `llm_cost_status`, `budget_check_skipped`, `budget_id`, `budget_name` (unpriced and fail-open marking are MVP); phase 2 adds the remaining fields and `budget_threshold_crossed`.

## **4.10 Moesif Templates**

Exactly three Moesif artifacts, shipped as templates with the user guide under `docs/gateway/budgets/moesif/`. Creating or changing budgets needs no Moesif change.

| Artifact | Definition |
| :---- | :---- |
| **Budget Usage** card on the existing LLM Analytics page (`llm-analytics-budget-usage-card.json`) | Grouped by `metadata.budget_name` and `metadata.budget_period`: Amount = `max(metadata.budget_amount)`, Spend this period = `max(metadata.budget_spend)`, % used = `max(metadata.budget_percent_used)`, Refused = count where `metadata.budget_refused = true`. When the Budget filter selects a budget with `each` owners, rows are additionally grouped by `metadata.budget_owner_values` (one row per value or combination). No new page, section or sidebar item. |
| **Budget** filter next to the existing API filter (`llm-analytics-budget-filter.json`) | Filters the page's events on `metadata.budget_id`, labelled by `metadata.budget_name`. |
| Alert rule **"BUDGET: Threshold crossed"** — one per organization (`alert-threshold-crossed.json`, setup in `docs/gateway/budgets/moesif-alerts.md`) | Events where `metadata.budget_threshold_crossed` exists; group by `metadata.budget_name`, `metadata.budget_threshold_crossed`, `metadata.budget_owner_values`; count > 0 over 15 minutes; channels Slack / email / PagerDuty / webhook. Optional extra rules with the same condition filtered to specific budgets, only for routing. |

Unpriced traffic for a budget is found by setting the Budget filter and filtering on `metadata.llm_cost_status = not_calculated`. Whether these can be provisioned through the Moesif management API is open (§12). The card and filter need the phase-2 fields.

## **4.11 Deployments**

* `gateway/docker-compose.yaml` and `gateway/distribution/docker-compose.yaml`: a `redis` service (`redis:7`, `redis-server --appendonly yes --maxmemory-policy noeviction --requirepass ${REDIS_PASSWORD}`, named volume, healthcheck), `depends_on` from the gateway-controller/runtime, `REDIS_PASSWORD` in `gateway/local.env`; the shipped `gateway/configs/config.toml` sets `budgets.enabled = true` with the Redis block.
* `gateway/configs/config-template.toml`: `[budgets]` and `[policy_configurations.budgets_v1]` with explanatory comments (one Redis per organization's gateways, `noeviction`, AOF).
* Helm `kubernetes/helm/gateway-helm-chart`: `gateway.budgets.enabled`, `gateway.budgets.failureMode`, `gateway.budgets.redis` (host, port, db, username, `existingSecret` for password and TLS CA / client cert / key, TLS flags) rendered into `templates/gateway/gateway-config.yaml`; rendering fails when budgets are enabled without Redis; optional bundled Redis `gateway.budgets.redis.bundled.enabled` (StatefulSet + PVC + Service + ConfigMap with AOF and `noeviction`) in `templates/gateway/redis/`; secrets mounted in `templates/gateway/gateway-runtime/deployment.yaml`.
* Integration tests reuse the `redis` service already in `gateway/it/docker-compose.test.yaml` (db 1 to stay clear of semantic-cache keys).

# **5\. AI Workspace Implementation**

The Budgets area is **configuration only**: no spend, meters, percentage, per-value table, unpriced count or near-limit state anywhere. Screens and states are in the UIs document.

| Area | File | Change |
| :---- | :---- | :---- |
| API client | `portals/ai-workspace/src/apis/budgetApis.ts` | `listBudgets`, `getBudget`, `createBudget`, `updateBudget`, `deleteBudget`; types mirror `Budget` (incl. read-only `statusReason`), `BudgetContext`, `BudgetOwner`, `CreateBudgetRequest`, `UpdateBudgetRequest` — no spend types |
| List | `.../appShellPages/budgets/BudgetsList.tsx` | Available in org mode (all budgets, with a Context column) and project mode (`?project=<handle>`: that project's budgets). Name, owners summary (e.g. "Project-a · each user · OpenAI"), amount, period, mode, thresholds, status label; enable/disable/delete for managers; "See usage" link per row; budgets with `each` owners rendered as e.g. "each user gets $25 / day" |
| Dialog | `.../appShellPages/budgets/BudgetDialog.tsx` | Display name, description, the first owner as a fixed chip (the organization in org mode, the current project in project mode), "+ Add owner" rows (dimension select — Project only in org mode, LLM provider, LLM proxy, Application, Token claim; dimensions already used are disabled, a token claim once per claim name — then one or more values from searchable multi-select lists of existing APIs (e.g. OpenAI and Anthropic, shown as "OpenAI or Anthropic") or **Each**; free text only for claim name/values and model ids; no limit on rows), amount, period, Enforce/Monitor with mode-combination help, threshold chips (max 5, 1–100, unique; default 50/80/100) with a hint linking to the alert-setup guide; context, owners and period read-only on edit |
| Detail | `.../appShellPages/budgets/BudgetDetail.tsx` | Description, context, owners (dimension, values or Each), amount, period, mode, thresholds, status, created/updated by/at, "See usage" link; `OWNER_DELETED` explained as "Disabled — its project / LLM provider / LLM proxy / application was deleted" naming the deleted owner, with Delete for managers |
| Usage link | `.../appShellPages/budgets/usageLink.ts` | Opens the LLM Analytics page under `APIP_AIW_MOESIF_ANALYTICS_URL` with the Budget filter set (`metadata.budget_id`); falls back to the base URL until the path and filter format are confirmed |
| Analytics base URL | `portals/ai-workspace/src/config.env.ts`; `.../appShellPages/insights/Main.tsx` | New runtime key `APIP_AIW_MOESIF_ANALYTICS_URL` (via `getEnvOrDefault` / `window.__RUNTIME_CONFIG__`), replacing the hard-coded `https://www.moesif.com/wrap/basic` on the Insights page |
| Navigation | `portals/ai-workspace/src/App.tsx`; `.../appShell/AppSidebar.tsx` | Budgets route in both org mode and project mode; sidebar item near Insights |
| Permissions | Same files | Create / Edit / Disable / Enable / Delete shown only with `ap:budget:manage`; read-only otherwise |

All user-supplied values (names, descriptions, claim names and values, owner names) are rendered with React's default text interpolation, and values placed in link URLs are encoded with `encodeURIComponent` (`.claude/rules/js-output-encoding-xss.md`).

# **6\. Configuration**

| Key | Type | Default | Component | Description |
| :---- | :---- | :---- | :---- | :---- |
| `budgets.enabled` | bool | `false` | gateway-controller | Injects the budget system policy into LLM routes. Requires `policy_configurations.budgets_v1.redis` (startup fails otherwise — fail-closed). `false` is the rollback switch; nothing is enforced on that gateway. The shipped compose and Helm values set it `true` together with Redis. |
| `policy_configurations.budgets_v1.redis.*` | object | unset | gateway runtime | **Required when budgets are enabled.** Same shape as the advanced-ratelimit Redis settings: host, port, username, password, db, `key_prefix`, TLS (enabled, CA, server name, client cert/key for mTLS, insecure_skip_verify), dial/read/write timeouts, pool size. All gateways of an organization must use the same Redis and db. |
| `policy_configurations.budgets_v1.redis.key_prefix` | string | `budget:v1:` | gateway runtime | Fixed default; change only to share a Redis between isolated environments. |
| `policy_configurations.budgets_v1.redis.dial_timeout` / `read_timeout` / `write_timeout` / `pool_size` | duration / int | NOT ANSWERED (documented example: `500ms` / `100ms` / `100ms` / `50`) | gateway runtime | Connection tuning. |
| `policy_configurations.budgets_v1.redis.tls.enabled` | bool | NOT ANSWERED (documented example: `true`) | gateway runtime | TLS to Redis. |
| `policy_configurations.budgets_v1.redis.tls.insecure_skip_verify` | bool | `false` (TLS verification on by default) | gateway runtime | Safe default. |
| `policy_configurations.budgets_v1.failure_mode` | string | `closed` | gateway runtime | Redis unreachable: `closed` refuses requests under enforce-mode budgets (503) — fail-closed; `open` allows them uncounted. |
| `policy_configurations.budgets_v1.config_check_interval` | duration | `10m` | gateway runtime | How often to re-check `maxmemory-policy` / persistence and WARN if unsafe. |
| Helm `gateway.budgets.enabled` | bool | NOT ANSWERED (see §12) | Helm | Renders `[budgets] enabled`. |
| Helm `gateway.budgets.failureMode` | string | NOT ANSWERED | Helm | Renders `failure_mode`. |
| Helm `gateway.budgets.redis.*` | object | unset | Helm | External Redis host, port, db, username, `existingSecret` (password, TLS material), TLS flags. |
| Helm `gateway.budgets.redis.bundled.enabled` | bool | `false` | Helm | Deploy a single-replica Redis (AOF, `noeviction`, PVC) with the gateway; production should prefer an external, managed/HA Redis. |
| `APIP_AIW_MOESIF_ANALYTICS_URL` | string | `https://www.moesif.com/wrap/basic` | AI Workspace runtime config | Base URL of the embedded Moesif analytics, used by the Insights page and the Budgets "See usage" links (replaces the hard-coded URL). |

There is no UI feature flag: the Budgets page is always available to users with `ap:budget:read`. On upgrade, budgets are off on every gateway (`budgets.enabled = false`) until an operator provisions Redis and enables them. Configuration removed with the 2026-10-09 design change: `mode`, `sync_interval`, `sync_interval_fast`, `fast_sync_percent`, `disconnect_alert_after`, `failure_mode = fallback`, `budgets.period_purge_after`, `budgets.moesif.enabled`.

# **7\. Security Considerations**

* **Authorization**: `ap:budget:manage` for create / update / delete; `ap:budget:read` or `ap:budget:manage` for list / get. Both scopes declared in `openapi.yaml` (startup fails otherwise) and mapped in `role-to-scope-mapping.yaml`. Membership of an owner scope grants nothing (FR-028). The internal resync endpoint uses the existing gateway-token authentication. The full questionnaire is in the Security Impact Review document.
* **Tenant isolation**: the organization always comes from verified context — the JWT for the public API, the gateway token or websocket connection internally — never from a payload, path or query (GO-AUTH-005). Budget events go only to the owning organization's gateways. Cross-organization budget or owner lookups return the same 404 as a missing resource.
* **Input handling**: strict DTO decoding rejects unknown fields; enumerations and limits per §2.2; claim names restricted to `^[A-Za-z0-9_.:-]{1,255}$`.
* **Read-only mode**: enforced in the budget service and repository (GO-AUTH-015).
* **Redis**: an operator-configured target only (no user-supplied hosts); credentials and TLS material from Secrets / environment, never logged; TLS verification on by default. Claim values never appear in Redis key names (hashed).
* **Client-facing data**: 429 bodies and `Budget-*` headers intentionally reveal the budget's id, name, context and owners (the caller's own values only for `each` owners), period, amount and reset time to the caller; 503 bodies carry no infrastructure detail.
* **PII in analytics**: `budget_owner_values` sends the caller's claim values (often an email) to Moesif, and the Budget Usage card shows them per row for budgets with `each` claim owners. Whether masking is needed is open (§12).

# **8\. Observability**

* **Logs** (no claim values, API keys, tokens or Redis passwords at INFO and above):
  * gateway runtime: DEBUG per refusal (budget id, owner dimensions, period label); ERROR (rate-limited) on Redis unavailability and charge failures; WARN on unsafe Redis configuration and marker failures; INFO per emitted threshold crossing (budget id, threshold, period); WARN once when serving before definitions arrive.
  * gateway-controller: INFO on budget definition sync (count, organization); ERROR on budgets enabled without Redis.
  * platform-api: INFO on budget create / update / delete / auto-disable (budget id, actor).
* **Metrics** (low-cardinality labels only — no user, claim or budget values; gateway runtime):
  * `budget_checks_total{result="allowed|refused|monitor_would_refuse|unavailable|skipped", context_type}`
  * `budget_redis_request_duration_seconds{op="read|charge|marker"}` histogram and `budget_redis_errors_total{op}`
  * `budget_charge_failures_total`
  * `budget_threshold_events_total{outcome="emitted|suppressed|marker_error"}` (`suppressed` = lost the `SET NX` race or a lower threshold set silently)
  * `budget_unpriced_requests_total`
  * `budget_redis_config_unsafe` gauge
* **Audit**: every budget change in the platform-api audit log with before/after details.

# **9\. Testing Plan**

| Test area | Required coverage |
| :---- | :---- |
| platform-api validation and service rules | Every row of §2.2; owners canonicalised (same owners in another order → 409); `amount_version` increments only on amount change; audit before/after; owner-deleted disable; read-only mode through the service directly |
| platform-api REST integration | CRUD, definition-only responses, cross-organization 404, scope 403, unknown fields rejected |
| platform-api schema | `budgets` identical across dialects and idempotent; `audit.details` added to a database created with the previous `audit` DDL, re-run is a no-op |
| gateway-controller | Websocket handling, resync reconciliation, LazyResource publish and startup rebuild, storage, `EventTypeBudget` replica sync, activation check, policy injection, project-id and upstream-provider route metadata |
| Budget policy (miniredis) | Config parsing; TLS/mTLS client; safety check; keys and no raw claim values; periods incl. ISO-week year boundaries; money rounding; matcher (AND of all owners, OR within a values list with one shared counter, index returns the same set as a full scan, several `each` owners → one key per combination), overrides (applied, partial specificity stacks, cross-context does not override, overridden budget not charged), model owner (specific and each, missing model id) and evaluation order; counters (pipelined read, `INCRBY` + `EXPIREAT`, two instances share one total); 429/503/headers; loopback skip; unpriced; failure modes; streaming and `include_usage`; thresholds (race between two instances, highest-only, re-arming, TTL, cache, marker error); monitor mode |
| Analytics pipeline | Budget keys reach the published Moesif event as typed `metadata.*` (e.g. `metadata.budget_threshold_crossed`, `metadata.llm_cost_status`); refused requests emit events; sampling exemption |
| Phase ordering | Every request-header policy runs before any request-body policy; the prepended policy's response handler runs after `llm-cost` (buffered and streamed) |
| Gateway integration (`gateway/it`, godog) | Proxy cap and refusal; streaming; two replicas sharing one counter with restarts and control-plane outage; Redis failure modes and missing-Redis startup; budget + `llm-cost-based-ratelimit` coexistence; claims; combined owners (user × provider in a project; one user on two providers); thresholds across replicas; monitor mode |
| AI Workspace | Dialog validation and permission gating; owner rows (fixed first owner per mode, used dimensions disabled, project hidden in project mode, claim/application entry); list columns and status labels; no spend rendering anywhere; `OWNER_DELETED` explanation; "See usage" URL |
| End-to-end | Activation, caps, no-redeploy changes, per-user budgets, stacked and combined-owner budgets, streaming, monitor mode, unpriced traffic, shared counter / restarts / outages, thresholds, access control, owner deleted, rate-limit coexistence, reconciliation and alert timing (SC-006, SC-007), create-in-under-2-minutes (SC-001), Budget Usage card and filter |
| Performance | < 5 ms p95 added latency incl. Redis (SC-008) with ~1,000 budgets (incl. budgets with 5+ owners) and 10k `each` value combinations; matcher benchmark |

The concrete cases are in the Test Scenarios document.

# **10\. Rollout**

* **Phasing**:

| Phase | Scope | Requirements |
| :---- | :---- | :---- |
| 1 — MVP | CRUD + definitions sync; Redis client/config, activation check and unsafe-config warning; system policy with Redis counters for organization and project contexts with appended project, LLM provider and LLM proxy owners (values list or each), owner match index (multi-value owners indexed under every listed value); enforce mode; 429 + `Retry-After`; 503 / fail-open; streaming + `include_usage`; loopback skip; analytics pipeline change with `llm_cost_status`, `budget_check_skipped`, `budget_id`, `budget_name`; configuration-only Budgets page with usage links and `APIP_AIW_MOESIF_ANALYTICS_URL`; compose + Helm Redis; project-id annotation on LLM proxies | FR-001, 002, 003 (contexts; project, LLM provider, LLM proxy owners), 005, 006, 008, 009 (amount change keeps spend), 009a, 010–015, 016, 016a, 016c, 017–022b, 024, 024a, 024c, 024d, 026–029 |
| 2 | Claim, application and model owners (values list or each; model needs the gateway to expose the upstream model id), override resolution (most specific wins), monitor mode, remaining analytics fields, gateway threshold detection with Redis markers and re-arming via `amountVersion`, Moesif templates (Budget Usage card + Budget filter on the LLM Analytics page, organization-wide alert rule) and guide | FR-002/003 (claim, application, model owners), 003a, 004, 007, 009 (threshold re-arming), 023, 024e, 025 |
| 3 | `OnStreamError` charging once the SDK hook exists; Redis Sentinel/Cluster support; asynchronous charge optimisation if needed | FR-016b |

* **Default state**: on upgrade nothing changes for traffic — `budgets.enabled` defaults to `false`, so no policy is injected. platform-api accepts budget definitions and pushes them, but gateways without budgets enabled do not enforce them.
* **Enabling on a gateway**: provision Redis (shipped compose/Helm include it; production: external Redis with TLS + auth, `noeviction`, AOF), configure `budgets_v1.redis` identically on every gateway of the organization, set `budgets.enabled = true`, restart the gateway-controller and runtime.
* **Prerequisite**: LLM proxies must carry their project id before project-context budgets and project owners apply to their traffic; existing proxies need a redeploy.
* **Mixed gateway versions**: gateways older than this feature log and drop the unknown `budget.*` websocket messages and keep serving, so budgets are **not enforced on them**.
* **Redis data loss**: the current period's spend restarts at zero for affected budgets; documented with the persistence recommendation.
* **Rollback**: disable or delete budgets (enforcement stops within seconds), or set `budgets.enabled = false` on a gateway. The new table and the nullable `audit.details` column are additive, so the previous release runs against the upgraded schema unchanged. Redis keys expire on their own.

# **11\. Future Work**

* Charging aborted streams for usage reported so far, once the gateway SDK exposes an `OnStreamError` hook (delivery phase 3).
* Redis Sentinel / Cluster support (the reused client supports neither today); the `{budgetUuid}` hash tag already keeps a budget's keys in one slot.
* Asynchronous charge step if per-request Redis round-trips prove costly (admission stays synchronous).
* A dedicated xDS type for budget definitions if the LazyResources channel becomes crowded.
* Gateways reporting budget capability on connect, so the platform can warn about gateways that do not enforce (pending the decision in §12).
* Organization-level IANA time zone, fiscal quarters and custom periods.
* An organization option to refuse unpriced models while an enforce-mode budget applies, and per-model custom prices.
* Per-project delegation of budget management.
* Pre-estimating cost from `max_tokens` to reduce overshoot.
* MCP proxy and other artifact owners, once their traffic is priced.
* A cap on owners per budget, if needed.

# **12\. Open Questions**

| # | Question | Context / owner |
| :---- | :---- | :---- |
| 1 | Is a single `budgets` definitions table in platform-api acceptable given the 2026-10-09 guidance to "avoid new tables"? | Architecture leads |
| 2 | Is a built-in system policy acceptable, or must the budget policy be auto-attached to every LLM deployment (guidance: "system-level policies are not supported")? | Architecture leads |
| 3 | Multi-region and hybrid deployments that cannot share one Redis: shared cross-region Redis (latency vs SC-008) or per-region budgets / Redis? | Architecture and product leads |
| 4 | Should gateways report budget capability on connect so platform-api / UI can warn when an organization has budgets and some connected gateways do not enforce them? The supporting work is optional pending this decision. | NOT ANSWERED |
| 5 | Moesif: LLM Analytics path under `APIP_AIW_MOESIF_ANALYTICS_URL` and Budget filter deep-link query format (e.g. `?budget_id=<handle>` vs a Moesif filter expression on `metadata.budget_id`); provisioning of the card, filter and alert rule through the Moesif management API; sampling-exemption mechanism. Must be answered before the "See usage" deep link ships. | Analytics team |
| 6 | Can the gateway team provide an `OnStreamError` hook? To be raised as a GitHub issue with owner and target release, referenced in the PR description — not as a code comment. | Gateway team |
| 7 | Does the LLM proxy route expose its upstream provider's uuid to policies? Verify early in phase 1; add it to route metadata if missing. | Gateway team |
| 8 | Concurrency rule for two simultaneous updates to the same budget (last write wins, or optimistic locking)? | Feature owner |
| 9 | Status and error code returned when a budget write is attempted in read-only mode. | Feature owner |
| 10 | Defaults for Redis `dial_timeout`, `read_timeout`, `write_timeout`, `pool_size` and `tls.enabled`, and for Helm `gateway.budgets.enabled` / `failureMode`. | Feature owner |
| 11 | The shipped Helm values are said to enable budgets together with Redis, but the bundled Redis defaults to `false` and rendering fails when budgets are enabled without Redis. Which Redis does the default Helm install use — bundled on, or budgets off by default? | Feature owner |
| 12 | `budget_owner_values` sends callers' claim values (often emails) to Moesif and shows them on the Budget Usage card. Is masking or a narrower audience required? | Security / product leads |
| 13 | Case sensitivity of claim names and values when matching requests to claim budgets. | Feature owner |
| 14 | Product decisions ("split by", override and model owner resolved 2026-10-14): meaning of "dynamic budgets for apps", custom model prices, USD only, track/warn/block mapping, `ap_viewer` read access (proposed: no). | Product leads |
| 15 | Cap on owners per budget and on values per owner (none in v1; e.g. 3 owners, at most 2 `each`) if matching latency or Redis key counts require it. | Architecture and product leads |
| 16 | When one of several listed values is deleted (e.g. one of two LLM providers), drop the value and keep the budget active instead of disabling it? | Product leads |
