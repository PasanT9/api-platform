# Data Model: Budgets

**Feature**: [spec.md](./spec.md) | **Decisions**: [research.md](./research.md) | **Revised**: 2026-10-09 — spend tables `budget_period_spend` and `budget_sync_cursors` removed; spend lives only in Redis (§5). 2026-10-14 — `scope_type`/`scope_key`/`scope_value`/`per_value` replaced by a context (organization or project) and an owner list (§1.1), stored as a JSON column on the same table. 2026-10-14 (later) — owners carry `values` lists (any of) instead of a single `value` (§1.1).

The relational model is one new table (`budgets`, §1) plus one nullable `details` column on the shipped `audit` table (§3), shipped with its per-dialect upgrade path (DB rule R0-UPGRADE-PATH). DDL must be added to `schema.postgres.sql`, `schema.sqlite.sql` and `schema.sqlserver.sql` with identical structure (DB rule R8), idempotent guards (DB rule R9), no `CHECK` enums (DB rule R4 — validated in Go constants), `SMALLINT` booleans (DB rule R3). Postgres types shown.

> **Open (architecture leads)**: guidance received on 2026-10-09 said "avoid new tables". Budget definitions are org-level configuration edited in the UI and pushed to gateways, so they still need `budgets`; confirm a single definitions table is acceptable (plan risk 8a).

Reference key: "research Rn" = a decision in [research.md](./research.md); "DB rule Rn" = `.agents/skills/designing-db-schemas/references/api-platform-db-schema-rules.md`.

## Entity overview

```text
platform-api DB                         gateway-controller DB          policy engine (memory)         shared Redis (per org)
organizations 1 ──< budgets  ──WS budget.*──► budgets (copy) ──LazyResources──► Budget definitions ──► counters  budget:v1:{uuid}:<period>:<valueKey>
                       │                                                                              markers   …:thr:<t>:<amountVersion>
                       ├── context → organization | project (first owner, fixed)
                       └── owners[] (JSON) → project | llm_provider | llm_proxy | model | application | claim(name) — each a values list (any of) or "each"

audit (shipped) + details column   (before/after values, §3)
```

No spend, counts or threshold state is stored in platform-api or the gateway-controller.

---

## 1. Budget (`budgets`, platform-api)

| Column | Type | Notes |
|---|---|---|
| `uuid` | `VARCHAR(40)` PK | |
| `handle` | `VARCHAR(40) NOT NULL` | URL id; unique per org (DB rule R2) |
| `organization_uuid` | `VARCHAR(40) NOT NULL` | FK `organizations(uuid) ON DELETE CASCADE` |
| `display_name` | `VARCHAR(255) NOT NULL` | |
| `description` | `VARCHAR(1023)` | optional, shown on the Budgets page |
| `context_type` | `VARCHAR(20) NOT NULL` | `ORGANIZATION` \| `PROJECT` — the AI Workspace mode the budget was created in; the first owner (FR-002) |
| `project_uuid` | `VARCHAR(40)` (nullable) | FK `projects(uuid) ON DELETE SET NULL` is **not** used: the service disables the budget instead (see validation). Set only when `context_type = PROJECT` |
| `owners` | `BYTEA NOT NULL` | appended owners as canonical UTF-8 JSON (§1.1); `[]` when the budget has only its first owner. Opaque to SQL, never queried with JSON operators → not `JSONB` (DB rule R3-JSONB); unbounded length → not `VARCHAR` (DB rule R3-LARGE-PAYLOAD). SQLite `TEXT`, SQL Server `NVARCHAR(MAX)` (DB rule R8 divergence, same as `audit.details`) |
| `owners_hash` | `CHAR(64) NOT NULL` | hex SHA-256 of the canonical `owners` JSON; used only by the unique constraint |
| `amount_nano` | `BIGINT NOT NULL` | USD × 10⁹; > 0 |
| `amount_version` | `BIGINT NOT NULL DEFAULT 1` | incremented by the service on every change of `amount_nano`; part of the threshold marker key (§5.2), so an amount change re-arms all thresholds (FR-009). Not changed by name/description/mode/threshold edits |
| `period` | `VARCHAR(10) NOT NULL` | `DAY` \| `WEEK` \| `MONTH` |
| `enforcement_mode` | `VARCHAR(10) NOT NULL DEFAULT 'ENFORCE'` | `ENFORCE` \| `MONITOR` |
| `threshold_percents` | `VARCHAR(64) NOT NULL DEFAULT ''` | ascending CSV, e.g. `50,80,100`; each 1–100 |
| `status` | `VARCHAR(20) NOT NULL DEFAULT 'ACTIVE'` | `ACTIVE` \| `DISABLED` |
| `status_reason` | `VARCHAR(20)` (nullable) | why the platform set the status; values from Go constants, validated in the service layer (no `CHECK`, DB rule R4): `OWNER_DELETED`. `NULL` for administrator-set status; cleared when an administrator enables or disables the budget. Returned read-only as `statusReason`. |
| `data_version` | `VARCHAR(20) NOT NULL DEFAULT '1.0'` | row format version (DB convention) — not the amount version |
| `created_by`, `updated_by` | `VARCHAR(200)` | |
| `created_at`, `updated_at` | `TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP` | |

Constraints and indexes:
- `UNIQUE (organization_uuid, handle)`.
- `UNIQUE (organization_uuid, context_type, project_uuid, owners_hash, period)` — one budget per owner set and period (budgets are hard-deleted, so a DB constraint is safe). SQL Server and Postgres treat `NULL` `project_uuid` differently in unique constraints, so org-context rows store `project_uuid = ''`, not `NULL` (DB rule R8).
- `idx_budgets_org_status (organization_uuid, status)`; `idx_budgets_project (organization_uuid, project_uuid)` (project-mode list).

**Trade-off (owners as a JSON column).** Storing owners on the `budgets` row keeps the single-table design (guidance: avoid new tables) and makes a budget one row everywhere (API, controller copy, LazyResource). The cost: no foreign keys or indexed lookups on owner values, so "which budgets reference this deleted provider?" is a scan of the organization's budgets in the service layer (organizations have tens to hundreds of budgets, so this is cheap). A child `budget_owners` table would give FKs and indexes but is a second new table; revisit if per-org budget counts grow large.

### 1.1 Owners (JSON)

```json
[
  {"dimension": "PROJECT",      "values": ["<project uuid>"]},
  {"dimension": "LLM_PROVIDER", "values": ["<anthropic uuid>", "<openai uuid>"]},
  {"dimension": "MODEL",        "each": true},
  {"dimension": "CLAIM",        "claimName": "sub", "each": true}
]
```

| Field | Notes |
|---|---|
| `dimension` | `PROJECT` \| `LLM_PROVIDER` \| `LLM_PROXY` \| `MODEL` \| `APPLICATION` \| `CLAIM` (Go constants, DB rule R4; `MODEL` added 2026-10-14) |
| `claimName` | `CLAIM` only; matches `^[A-Za-z0-9_.:-]{1,255}$` |
| `values` | one or more uuids of projects / providers / proxies / applications (the API accepts and returns handles), model ids (`MODEL`: the upstream model id sent to the provider after any proxy rewrite, exact and case-sensitive, 1–255 characters, no control characters; not checked against a catalog), or claim values; a request matches when its value is **any** of them, and the listed values share one allowance (one counter). Stored sorted (bytewise) and de-duplicated, so the same set in any order hashes identically. Absent when `each`. *(Multi-value owners added 2026-10-14; replaces the single `value`.)* |
| `each` | `true` = a separate allowance per value; absent/false = the listed `values` |

Canonical order (stored, hashed and used for Redis keys): `PROJECT`, `LLM_PROVIDER`, `LLM_PROXY`, `MODEL`, `APPLICATION`, then `CLAIM` sorted by `claimName`. No limit on the number of owners or on values per owner in v1 (FR-002, FR-003); the request-body size limit bounds both in practice.

Validation rules (service layer):
- `context_type = PROJECT`: `project_uuid` exists in the org; a `PROJECT` owner is rejected (`BUDGET_OWNER_NOT_ALLOWED`). `context_type = ORGANIZATION`: `project_uuid = ''`.
- Each `dimension` at most once, except `CLAIM`, which is at most once per `claimName` (`BUDGET_OWNER_DUPLICATE`).
- Exactly one of `values` / `each = true` per owner; `values` non-empty with no duplicates (`BUDGET_INVALID_OWNER`).
- Each `MODEL` value: non-empty, at most 255 characters, no control characters (`BUDGET_INVALID_OWNER`); not resolved against the model catalog, because the catalog can lag new models.
- Each `LLM_PROVIDER` / `LLM_PROXY` value: the artifact exists in the org and its `artifacts.type` is `LlmProvider` / `LlmProxy` respectively; each `PROJECT` / `APPLICATION` value: the row exists in the org; in project context, project-scoped resources (proxies, applications) must belong to that project (`BUDGET_OWNER_NOT_FOUND` otherwise).
- Immutable after create: `context_type`, `project_uuid`, `owners`, `period`, `handle` (FR-008).
- A project referenced as context or owner, or an LLM provider / LLM proxy / application listed in any owner's `values` (even one of several), deleted → budget is marked `DISABLED` with `status_reason = OWNER_DELETED` (it can no longer match traffic as defined) so the admin can review and delete it — whether to instead drop a deleted value and keep the budget active when other values remain is an open question; this is audited with before/after details (§3, FR-009a) like any other status change. No analytics event is published.

State transitions:

```text
           create
             │
             ▼
   ┌──── ACTIVE ◄────┐
   │        │ disable │ enable
   │        ▼         │
   │     DISABLED ────┘
   │        │
   └──delete┴──delete──► (row removed; Redis counters/markers expire by TTL)
```

Enforcement mode `MONITOR ⇄ ENFORCE` is a free edit on an `ACTIVE`/`DISABLED` budget; the Redis counter is unaffected.

---

## 2. Gateway-controller copy (`budgets`, controller DB)

The controller persists the budget definitions it receives (fields of §1 minus audit columns, incl. `amount_version` and `threshold_percents`) in `gateway/gateway-controller/pkg/storage/gateway-controller-db.sql` (and the Postgres / SQL Server variants), so that the in-memory LazyResource store can be rebuilt on restart (research R2). Replica sync via `common/eventhub` (`EventTypeBudget`). No usage outbox (removed 2026-10-09).

---

## 3. Audit details (`audit`, platform-api — shipped table, additive column)

FR-026 and FR-009a need before/after values; the shipped `audit` table (`uuid, action, resource_uuid, resource_type, organization_uuid, performed_by, performed_at`) has nowhere to put them. Add one **nullable** column (DB rule R0-FROZEN allows a new nullable column on a shipped table):

| Dialect | Column | Why this type |
|---|---|---|
| PostgreSQL | `details BYTEA NULL` | opaque JSON never queried with JSON operators → not `JSONB` (DB rule R3-JSONB); bare `TEXT` is banned in Postgres (DB rule R3-NO-TEXT); payload can exceed `VARCHAR(1023)` (DB rule R3-LARGE-PAYLOAD) |
| SQLite | `details TEXT NULL` | intentional divergence (DB rule R8) |
| SQL Server | `details NVARCHAR(MAX) NULL` | intentional divergence (DB rule R8) |

Content: UTF-8 JSON `{"before": {...}, "after": {...}}` with the budget's mutable fields (displayName, description, amount, thresholds, enforcementMode, status, statusReason); `before` absent on CREATE, `after` absent on DELETE. Automatic owner-deleted disable uses actor `system`. No secrets, tokens or claim values from callers. Existing callers of `Record` leave it `NULL`. This is the only record of budget changes (no Moesif action, FR-009a).

**Upgrade path (DB rule R0-UPGRADE-PATH)** — the column goes into each dialect's `CREATE TABLE audit` body (fresh installs) **and** a guarded per-dialect `ALTER` that runs on every startup (already-provisioned databases; nullable, so it applies while the previous release is still running):

```sql
-- PostgreSQL (schema.postgres.sql, after the audit CREATE)
ALTER TABLE audit ADD COLUMN IF NOT EXISTS details BYTEA NULL;

-- SQL Server (schema.sqlserver.sql)
IF COL_LENGTH(N'dbo.audit', N'details') IS NULL
ALTER TABLE dbo.audit ADD details NVARCHAR(MAX) NULL;

-- SQLite: no ADD COLUMN IF NOT EXISTS — guard in Go (connection.go) by checking
-- pragma_table_info('audit') for 'details' before running:
ALTER TABLE audit ADD COLUMN details TEXT NULL;
```

The same change lands in `schema.sql` and the fixture `init-platform-api-db.sql` (DB rule R0-SCOPE). Layer sync (DB rule R7-LAYER-SYNC): `AuditRepository` gains `RecordWithDetails(action, resourceUUID, resourceType, orgUUID, performedBy string, details []byte)`; `Record` keeps its signature and writes `NULL`. Rollback: the previous release never selects the column, so it runs unchanged.

---

## 4. Runtime (policy engine) state — not persisted

| Item | Key | Content |
|---|---|---|
| Budget definitions | LazyResource `ResourceType="Budget"`, `ID=budget uuid` | §1 fields needed for matching, thresholds and the 429 message (handle, display name, context, owners, amount, amountVersion, thresholds, period, mode, status). The policy engine also keeps a **match index** keyed by dimension value (project, provider, proxy, model, application, claim name/value; a multi-value owner is indexed under every listed value) so a request looks up candidate budgets instead of scanning all budgets; each candidate is then checked against all its owners. The engine also precomputes, from definitions only, each budget's **override set** (FR-003a: the budgets it overrides — same context, period and dimension set, at least as narrow on every dimension and narrower on one; a values list is narrower than `each`, and a list is at least as narrow as another when it is a subset of it, strictly when a proper subset), so per-request override resolution is a set lookup among the matched candidates |
| Redis client | process | one pooled client per policy engine, built from `[policy_configurations.budgets_v1.redis]` |
| Emitted-marker cache | `(budgetUuid, periodLabel, valueKey, amountVersion)` | highest threshold this replica knows is already marked; avoids a `SET NX` per request. Bounded LRU; a miss only costs one extra `SET NX` |

No spend is held in the gateway: every check reads Redis.

---

## 5. Redis key schema (shared counter, required)

All keys use the `{budgetUuid}` hash tag so one budget's keys share a slot (ready for Redis Cluster later). `valueKey` is built from the budget's **"each" owners only**, in canonical order (§1.1), joined with `:`:
- no "each" owner: `_`
- each `PROJECT` / `LLM_PROVIDER` / `LLM_PROXY` / `APPLICATION` owner: the request's project / artifact / application uuid
- each `MODEL` owner: `m` + first 32 hex chars of SHA-256(model id) — model ids can contain `:` and `/`
- each `CLAIM` owner: `h` + first 32 hex chars of SHA-256(claim value) — no raw claim values (e.g. emails) in Redis keys
- if the joined key exceeds 200 characters: `x` + hex SHA-256 of it

Example: owners `PROJECT = p1`, `LLM_PROVIDER = each`, `CLAIM sub = each` → `budget:v1:{b7}:2026-10:<openaiUuid>:h3f9a…`. Keys are created lazily by the first `INCRBY`, so only combinations that received traffic exist. A multi-value owner (e.g. `LLM_PROVIDER ∈ {anthropic, openai}`) contributes nothing to `valueKey`: its listed values share the budget's one counter.

`periodLabel`: `2026-10-08` (DAY), `2026-W41` (WEEK), `2026-10` (MONTH). `periodEnd` = next reset in UTC.

### 5.1 Period counter

| Key | Type / value | Written by | TTL |
|---|---|---|---|
| `budget:v1:{<budgetUuid>}:<periodLabel>:<valueKey>` | string, integer nano-USD | `INCRBY <cost_nano>` on charge (returns new total); `GET`/`MGET`-style pipelined reads on admission | `EXPIREAT periodEnd + 86400` set in the same pipeline as the first `INCRBY` (`EXPIREAT … NX` on Redis ≥ 7, else unconditional — idempotent value) |

- Missing key = 0 spent (new period, new budget, or data loss — FR-022a).
- Unpriced requests (`INCRBY 0` is skipped) do not touch the counter.
- Monitor-mode budgets are counted exactly like enforce-mode budgets, so switching to enforce uses recorded spend (US4).
- Disabled budgets are not evaluated, so nothing is counted while disabled; the existing key keeps its value until TTL, so re-enabling in the same period resumes from it.

### 5.2 Threshold marker

| Key | Value | Written by | TTL |
|---|---|---|---|
| `budget:v1:{<budgetUuid>}:<periodLabel>:<valueKey>:thr:<threshold>:<amountVersion>` | `1` | `SET … 1 NX EXAT periodEnd+86400` by the gateway whose `INCRBY` result first reaches `threshold % × amount` (research R9) | period end + 1 day |

- The `SET NX` winner emits `budget_threshold_crossed = <threshold>` on that request's analytics event; losers emit nothing.
- When one charge (or an amount change) reaches several thresholds, only the highest is emitted; lower reached thresholds get their markers set silently in the same pipeline.
- A new `amountVersion` means new keys → every threshold re-armed (FR-009); old markers expire.

### 5.3 Sizing

Budget without "each" owners: 1 counter + ≤ 5 markers per period. Budget with "each" owners: 1 counter (+ ≤ 5 markers) per active combination per period, e.g. 10,000 users ≈ 10–60k small keys (≈ a few MB); 10,000 users × 4 providers ≈ 40–240k keys. Cardinality grows with the product of the "each" dimensions' active values; this is why a cap on "each" owners may be added later. Keys never accumulate across periods (TTL). Redis must run with `maxmemory-policy noeviction` and AOF/RDB persistence (research R6).

---

## 6. Derived / transient entities

- **Override resolution** (per request, not stored): among the matched budgets, drop every budget that is overridden by another matched budget (FR-003a). Dropped budgets are not checked, not charged and evaluate no thresholds; their ids go to the `budget_overridden_ids` analytics field. Overrides are not transitive across mismatches: a budget is dropped only if an overriding budget itself matched the request.
- **Overrides / overridden-by** (control plane, derived on read, not stored): for each budget, the ids of the budgets in the same organization it overrides and that override it, computed from definitions with the same rule; returned read-only by the API and used for the create/edit warning.
- **Budget evaluation result** (per request, not stored): ordered list of applicable budgets (after override resolution) with `{budgetUuid, valueKey, amount, spentBefore, spentAfter, remaining, resetAt, refused, wouldRefuse, thresholdCrossed}`; drives the 429 decision, response headers and analytics fields (contract gateway-budget-enforcement §4.1).
- **Threshold crossing**: not stored outside the Redis marker; published once as fields on the crossing request's analytics event.

## Removed 2026-10-09

- `budget_period_spend` (ledger), `budget_sync_cursors` (usage idempotency), the controller `budget_usage_outbox`, the `asOf` freshness definition, runtime global-totals cache / unsynced deltas / source id, and the "re-seed Redis from platform-api totals" rule — all consequences of moving spend exclusively to Redis (research R5).
