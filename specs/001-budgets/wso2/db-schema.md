# **Budgets - Proposed DB Schema**

# 1. Summary of Changes

| Component | Object | Change | Shipped table? | Upgrade path |
| :---- | :---- | :---- | :---- | :---- |
| platform-api | `budgets` | New table (budget definitions) | No | Guarded `CREATE TABLE` (all three dialects) |
| platform-api | `idx_budgets_org_status`, `idx_budgets_project` | New indexes on the new table | No | Guarded `CREATE INDEX` |
| platform-api | `audit.details` | New **nullable** column on a shipped table (before/after values of budget changes, FR-009a, FR-026) | **Yes** | Column added to each dialect's `CREATE TABLE audit` body (fresh installs) plus a guarded per-dialect `ALTER TABLE` run at every start-up (existing databases) — §2.3, §5 |
| gateway-controller | `budgets` (copy of the definitions) | New table | No | Guarded `CREATE TABLE`; exact DDL is an open question (§7) |

The relational change is **one new table plus one nullable column**. The only change to a shipped table is `audit.details`; it is additive (DB rule R0-FROZEN allows a new nullable column) and ships with its upgrade path (DB rule R0-UPGRADE-PATH). No existing column, index or constraint is changed. The DB rules referred to here are R0–R10 in `.agents/skills/designing-db-schemas/references/api-platform-db-schema-rules.md`, governed by `.claude/rules/db-schema-changes.md`.

**No spend is stored in any database.** Since the design change of 2026-10-09, spend counters and threshold markers live only in the gateways' shared Redis, with expiry one day after each period ends (§4). The spend ledger and sync-cursor tables of the earlier design (`budget_period_spend`, `budget_sync_cursors`) and the controller usage outbox are dropped and are not part of this change.

# 2. Schema Changes

## 2.1 platform-api (Control Plane): `budgets` — budget definitions

One row per budget: a money ceiling for a context (the organization or a project) plus any number of appended owners over one UTC calendar period (owner model decided 2026-10-14). Definitions are organization-level configuration edited in the AI Workspace and pushed to gateways, so they need durable storage in the control plane. Money is stored as integer nano-USD (USD × 10⁹) in `BIGINT`; the REST API exchanges decimal strings, so no floating-point rounding occurs. Enumerated values (`context_type`, owner `dimension`, `period`, `enforcement_mode`, `status`, `status_reason`) are validated against Go constants in the service layer; there are no `CHECK` enum constraints (DB rule R4).

| Column | Notes |
| :---- | :---- |
| `uuid` | Primary key. |
| `handle` | URL id; unique per organization; immutable after create. |
| `organization_uuid` | Owning organization; foreign key to `organizations(uuid)` `ON DELETE CASCADE`. |
| `display_name` | Required, up to 255 characters. |
| `description` | Optional, up to 1023 characters; shown on the Budgets page. |
| `context_type` | `ORGANIZATION` (created in org mode) or `PROJECT` (created in project mode) — the budget's first owner. |
| `project_uuid` | The project of a `PROJECT` context; `''` for `ORGANIZATION` (not `NULL`, so the `UNIQUE` behaves the same on all dialects). Not a foreign key: when the project is deleted the service disables the budget (`OWNER_DELETED`) instead of cascading. |
| `owners` | The appended owners as canonical UTF-8 JSON — an array of `{dimension, claimName?, values?, each?}` where `dimension` is `PROJECT`, `LLM_PROVIDER`, `LLM_PROXY`, `MODEL`, `APPLICATION` or `CLAIM`, `values` is a list of one or more project / provider / proxy / application uuids (the API accepts and returns handles), model ids (`MODEL`: the upstream model id, up to 255 characters each) or claim values — any of which matches, sharing one allowance — stored sorted and de-duplicated so the same set hashes identically, and `each: true` gives every value its own allowance. Sorted canonically (`PROJECT`, `LLM_PROVIDER`, `LLM_PROXY`, `MODEL`, `APPLICATION`, then `CLAIM` by claim name); `[]` when the budget has no appended owner. No limit on the number of owners. Opaque to SQL (never queried with JSON operators). |
| `owners_hash` | Hex SHA-256 of the canonical `owners` JSON; only used by the `UNIQUE` constraint, because `owners` is unbounded and cannot be part of a key. |
| `amount_nano` | Amount in nano-USD; greater than zero. |
| `amount_version` | Starts at `1`; incremented by the service on every change of `amount_nano` and shipped to gateways. It is part of the Redis threshold-marker key, so an amount change re-arms every threshold (FR-009). Not changed by name, description, mode or threshold edits. |
| `period` | `DAY`, `WEEK` or `MONTH`. |
| `enforcement_mode` | `ENFORCE` or `MONITOR`. |
| `threshold_percents` | Ascending comma-separated list, for example `50,80,100`; each 1–100; at most five. |
| `status` | `ACTIVE` or `DISABLED`. |
| `status_reason` | Nullable. Why the **platform** set the current status; the only value is `OWNER_DELETED` (the budget's project / LLM provider / LLM proxy / application was deleted, so the budget was disabled automatically). `NULL` when an administrator set the status; cleared when an administrator enables or disables the budget. Returned read-only as `statusReason`. |
| `data_version` | Row format version (DB convention) — not the amount version. |
| `created_by`, `created_at`, `updated_by`, `updated_at` | Standard audit set (DB rule R5); written by a user-initiated REST call, so `created_by` / `updated_by` come from verified token claims. |

Immutable after create: `context_type`, `project_uuid`, `owners` (and so `owners_hash`), `period`, `handle`.

Overrides (most specific wins, 2026-10-14) are **not stored**: `overrides` / `overriddenBy` are computed from the organization's definitions on read, so no column or table is added for them.

**Why a JSON column and not a child table.** Owners are always read and written with their budget (API, push to gateways, controller copy), so one row per budget keeps the single new table the architecture guidance allows. The cost is no foreign keys or index on owner values: "which budgets name this deleted LLM provider?" is answered by scanning the organization's budgets in the service layer, which is cheap at the designed ~1,000 budgets per organization. A `budget_owners` child table would add FKs and indexed lookups at the price of a second new table (§7).

**PostgreSQL**

```sql
CREATE TABLE IF NOT EXISTS budgets (
    uuid               VARCHAR(40)   PRIMARY KEY,
    handle             VARCHAR(40)   NOT NULL,
    organization_uuid  VARCHAR(40)   NOT NULL,
    display_name       VARCHAR(255)  NOT NULL,
    description        VARCHAR(1023),
    context_type       VARCHAR(20)   NOT NULL,
    project_uuid       VARCHAR(40)   NOT NULL DEFAULT '',
    owners             BYTEA         NOT NULL,
    owners_hash        CHAR(64)      NOT NULL,
    amount_nano        BIGINT        NOT NULL,
    amount_version     BIGINT        NOT NULL DEFAULT 1,
    period             VARCHAR(10)   NOT NULL,
    enforcement_mode   VARCHAR(10)   NOT NULL DEFAULT 'ENFORCE',
    threshold_percents VARCHAR(64)   NOT NULL DEFAULT '',
    status             VARCHAR(20)   NOT NULL DEFAULT 'ACTIVE',
    status_reason      VARCHAR(20),
    data_version       VARCHAR(20)   NOT NULL DEFAULT '1.0',
    created_by         VARCHAR(200),
    created_at         TIMESTAMPTZ   DEFAULT CURRENT_TIMESTAMP,
    updated_by         VARCHAR(200),
    updated_at         TIMESTAMPTZ   DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (organization_uuid) REFERENCES organizations(uuid) ON DELETE CASCADE,
    UNIQUE (organization_uuid, handle),
    UNIQUE (organization_uuid, context_type, project_uuid, owners_hash, period)
);
```

**SQLite** — same columns and order; `owners TEXT`; `created_at` / `updated_at` are `DATETIME DEFAULT CURRENT_TIMESTAMP`.

```sql
CREATE TABLE IF NOT EXISTS budgets (
    uuid               VARCHAR(40)   PRIMARY KEY,
    handle             VARCHAR(40)   NOT NULL,
    organization_uuid  VARCHAR(40)   NOT NULL,
    display_name       VARCHAR(255)  NOT NULL,
    description        VARCHAR(1023),
    context_type       VARCHAR(20)   NOT NULL,
    project_uuid       VARCHAR(40)   NOT NULL DEFAULT '',
    owners             TEXT          NOT NULL,
    owners_hash        CHAR(64)      NOT NULL,
    amount_nano        BIGINT        NOT NULL,
    amount_version     BIGINT        NOT NULL DEFAULT 1,
    period             VARCHAR(10)   NOT NULL,
    enforcement_mode   VARCHAR(10)   NOT NULL DEFAULT 'ENFORCE',
    threshold_percents VARCHAR(64)   NOT NULL DEFAULT '',
    status             VARCHAR(20)   NOT NULL DEFAULT 'ACTIVE',
    status_reason      VARCHAR(20),
    data_version       VARCHAR(20)   NOT NULL DEFAULT '1.0',
    created_by         VARCHAR(200),
    created_at         DATETIME      DEFAULT CURRENT_TIMESTAMP,
    updated_by         VARCHAR(200),
    updated_at         DATETIME      DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (organization_uuid) REFERENCES organizations(uuid) ON DELETE CASCADE,
    UNIQUE (organization_uuid, handle),
    UNIQUE (organization_uuid, context_type, project_uuid, owners_hash, period)
);
```

**SQL Server** — `OBJECT_ID` guard (`CREATE TABLE IF NOT EXISTS` is not valid T-SQL); `owners NVARCHAR(MAX)`; timestamps `DATETIME2(7) DEFAULT SYSUTCDATETIME()`.

```sql
IF OBJECT_ID(N'dbo.budgets', N'U') IS NULL
CREATE TABLE dbo.budgets (
    uuid               VARCHAR(40)   PRIMARY KEY,
    handle             VARCHAR(40)   NOT NULL,
    organization_uuid  VARCHAR(40)   NOT NULL,
    display_name       VARCHAR(255)  NOT NULL,
    description        VARCHAR(1023),
    context_type       VARCHAR(20)   NOT NULL,
    project_uuid       VARCHAR(40)   NOT NULL DEFAULT '',
    owners             NVARCHAR(MAX) NOT NULL,
    owners_hash        CHAR(64)      NOT NULL,
    amount_nano        BIGINT        NOT NULL,
    amount_version     BIGINT        NOT NULL DEFAULT 1,
    period             VARCHAR(10)   NOT NULL,
    enforcement_mode   VARCHAR(10)   NOT NULL DEFAULT 'ENFORCE',
    threshold_percents VARCHAR(64)   NOT NULL DEFAULT '',
    status             VARCHAR(20)   NOT NULL DEFAULT 'ACTIVE',
    status_reason      VARCHAR(20),
    data_version       VARCHAR(20)   NOT NULL DEFAULT '1.0',
    created_by         VARCHAR(200),
    created_at         DATETIME2(7)  DEFAULT SYSUTCDATETIME(),
    updated_by         VARCHAR(200),
    updated_at         DATETIME2(7)  DEFAULT SYSUTCDATETIME(),
    FOREIGN KEY (organization_uuid) REFERENCES organizations(uuid) ON DELETE CASCADE,
    UNIQUE (organization_uuid, handle),
    UNIQUE (organization_uuid, context_type, project_uuid, owners_hash, period)
);
```

The second `UNIQUE` enforces "one budget per context, owner list and period" (FR-006); because `owners` is stored canonically, the same owners given in a different order hash identically. The `owners` type differs per dialect (`BYTEA` / `TEXT` / `NVARCHAR(MAX)`) for the same reasons as `audit.details` (DB rules R3-JSONB, R3-NO-TEXT, R3-LARGE-PAYLOAD; intentional R8 divergence). A database constraint is safe here because budgets are hard-deleted, so no tombstone rows can collide with a new budget.

## 2.2 platform-api: indexes

PostgreSQL and SQLite:

```sql
CREATE INDEX IF NOT EXISTS idx_budgets_org_status ON budgets (organization_uuid, status);
CREATE INDEX IF NOT EXISTS idx_budgets_project ON budgets (organization_uuid, project_uuid);
```

SQL Server:

```sql
IF NOT EXISTS (SELECT 1 FROM sys.indexes WHERE name = N'idx_budgets_org_status' AND object_id = OBJECT_ID(N'dbo.budgets'))
CREATE INDEX idx_budgets_org_status ON dbo.budgets (organization_uuid, status);

IF NOT EXISTS (SELECT 1 FROM sys.indexes WHERE name = N'idx_budgets_project' AND object_id = OBJECT_ID(N'dbo.budgets'))
CREATE INDEX idx_budgets_project ON dbo.budgets (organization_uuid, project_uuid);
```

| Index | Query it serves |
| :---- | :---- |
| `idx_budgets_org_status` | Listing an organization's budgets filtered by `status`; also covers `organization_uuid` lookups (DB rule R6). |
| `idx_budgets_project` | Listing a project's budgets in project mode, and finding them when the project is deleted. Not a prefix of the `UNIQUE` constraint (which leads with `context_type`). Budgets that name a deleted provider, proxy, application or project in `owners` are found by scanning the organization's budgets. |

The foreign-key column `budgets.organization_uuid` leads `UNIQUE (organization_uuid, handle)` (DB rule R6).

## 2.3 platform-api: `audit` — new nullable `details` column (shipped table)

FR-026 and FR-009a require the before/after values of every budget create, update, enable/disable (including the automatic disable when the owner is deleted) and delete to be audit-logged; since 2026-10-09 the audit log is the **only** record of budget changes (no analytics event is published). The shipped `audit` table (`uuid`, `action`, `resource_uuid`, `resource_type`, `organization_uuid`, `performed_by`, `performed_at`) has nowhere to put them, so one **nullable** column is added.

| Dialect | Column | Why this type |
| :---- | :---- | :---- |
| PostgreSQL | `details BYTEA NULL` | Opaque JSON that is never queried with JSON operators, so not `JSONB` (DB rule R3-JSONB); bare `TEXT` is banned in Postgres (DB rule R3-NO-TEXT); the payload can exceed `VARCHAR(1023)` (DB rule R3-LARGE-PAYLOAD). |
| SQLite | `details TEXT NULL` | Intentional dialect divergence (DB rule R8). |
| SQL Server | `details NVARCHAR(MAX) NULL` | Intentional dialect divergence (DB rule R8). |

Content: UTF-8 JSON `{"before": {...}, "after": {...}}` with the budget's mutable fields (display name, description, amount, thresholds, enforcement mode, status, status reason); `before` is absent on create and `after` on delete. The automatic owner-deleted disable uses actor `system`. It carries no secrets, tokens or claim values from callers. Existing writers of `audit` leave the column `NULL`.

The column goes into each dialect's `CREATE TABLE audit` body (fresh installs) **and** a guarded `ALTER TABLE` that runs on every start-up (already-provisioned databases):

```sql
-- PostgreSQL (schema.postgres.sql, after the audit CREATE TABLE)
ALTER TABLE audit ADD COLUMN IF NOT EXISTS details BYTEA NULL;

-- SQL Server (schema.sqlserver.sql)
IF COL_LENGTH(N'dbo.audit', N'details') IS NULL
ALTER TABLE dbo.audit ADD details NVARCHAR(MAX) NULL;

-- SQLite has no ADD COLUMN IF NOT EXISTS: platform-api/internal/database/connection.go first checks
-- pragma_table_info('audit') for a 'details' column and runs this only when it is absent:
ALTER TABLE audit ADD COLUMN details TEXT NULL;
```

The same change lands in the legacy SQLite copy `platform-api/internal/database/schema.sql` and the fixture `platform-api/internal/database/init-platform-api-db.sql` (DB rule R0-SCOPE). Layer sync (DB rule R7-LAYER-SYNC): the `AuditRepository` interface and `platform-api/internal/repository/audit.go` gain `RecordWithDetails(action, resourceUUID, resourceType, orgUUID, performedBy string, details []byte)` with a named-column `INSERT`; the existing `Record` keeps its signature and writes `NULL`. Because the column is nullable, the `ALTER` can run while the previous release is still serving.

## 2.4 gateway-controller (Data Plane): `budgets` copy

The gateway-controller persists the budget definitions it receives from platform-api so that its in-memory LazyResource store (which feeds definitions to the policy engine over xDS) can be rebuilt on restart. The stored fields are the definition fields of §2.1 without the audit columns — the same fields the definition push carries: uuid, handle, display name, context type, project uuid, owners (canonical, uuids), amount in nano-USD, **amount version**, period, enforcement mode, **thresholds**, status, and `updatedAt` for idempotent upserts. (Amount version and thresholds are needed because threshold detection now runs on the gateway.) Changes propagate between controller replicas through `common/eventhub` with a new `EventTypeBudget`. There is no usage outbox.

Schema files that must all carry the new table (the controller keeps two copies of its SQLite schema — DB rule R0-SCOPE):

* `gateway/gateway-controller/pkg/storage/gateway-controller-db.sql`
* `gateway/gateway-controller/pkg/storage/gateway-controller-db.postgres.sql`
* `gateway/gateway-controller/pkg/storage/gateway-controller-db.sqlserver.sql`
* `gateway/gateway-controller/resources/gateway-controller-db.sql`

`gateway/gateway-controller/pkg/storage/schema_consistency_test.go` must stay green. The column types, key shape and DDL have not been designed yet (§7). Gateway-controller schemas are exempt from the DB rule R3 type rules, but R4 (no `CHECK` enums), R9 (guarded DDL) and R8 (dialect alignment) apply.

# 3. Dialect Coverage

| Dialect | Schema file | Updated |
| :---- | :---- | :---- |
| PostgreSQL | `platform-api/internal/database/schema.postgres.sql` | Yes (`budgets`, indexes, `audit.details` + guarded `ALTER`) |
| SQLite | `platform-api/internal/database/schema.sqlite.sql` | Yes (`budgets`, indexes, `audit.details`; `ALTER` guarded in `connection.go`) |
| MySQL | — | No (platform-api has no MySQL schema) |
| MSSQL | `platform-api/internal/database/schema.sqlserver.sql` | Yes (`budgets`, indexes, `audit.details` + `COL_LENGTH`-guarded `ALTER`) |
| SQLite (legacy copy) | `platform-api/internal/database/schema.sql` | Yes (`budgets` and `audit.details`) |
| Fixture | `platform-api/internal/database/init-platform-api-db.sql` | Yes (`budgets` and `audit.details`) |
| PostgreSQL / SQLite / SQL Server | the four gateway-controller files listed in §2.4 | Yes (DDL open, §7) |

The SQL Server guard for the new column on a shipped table uses `COL_LENGTH` (§2.3), equivalent to the `sys.columns` check:

```sql
IF COL_LENGTH(N'dbo.audit', N'details') IS NULL
ALTER TABLE dbo.audit ADD details NVARCHAR(MAX) NULL;
```

The `audit.details` column type differs per dialect (`BYTEA` / `TEXT` / `NVARCHAR(MAX)`); every `budgets` column is identical across the three platform-api dialects apart from the usual timestamp and boolean spellings. The AI Workspace packages platform-api's schemas, so these changes also ship to AI Workspace customers.

# 4. Data Model Notes

* **Org scoping**: `budgets.organization_uuid NOT NULL` with a foreign key to `organizations` (`ON DELETE CASCADE`), indexed through `UNIQUE (organization_uuid, handle)` and `idx_budgets_org_status`. Every query filters on the organization taken from the caller's verified token (or, for the internal resync, from the gateway token).
* **Cardinality / growth**: designed for up to about 1,000 budgets per organization; rows change only when an administrator edits budgets. Traffic never writes to the database.
* **Retention / cleanup**: deleting a budget is a **hard delete**; the delete is audit-logged and past spend remains in Moesif. Deleting an organization cascades to its budgets. `audit.details` follows the existing retention of `audit` rows; no new cleanup.
* **Status reason**: `status_reason = OWNER_DELETED` is written together with `status = DISABLED` when the budget's context project, or a project / LLM provider / LLM proxy / application listed in any owner's `values` (even one of several), is deleted, and cleared when an administrator changes the status.
* **Derived values**: none. Spend, remaining amount, percentage used and threshold state are not stored or computed by the control plane.
* **Spend lives in Redis (runtime state, not database state)**: per budget × period × combination of the "each" owners' values, an integer nano-USD counter `budget:v1:{<budgetUuid>}:<periodLabel>:<valueKey>` and up to five threshold markers `…:thr:<threshold>:<amountVersion>`, all expiring one day after the period ends. Budget without "each" owners: 1 counter + ≤ 5 markers per period; with "each" owners: the same per active combination, e.g. 10,000 users ≈ 10–60k small keys (a few MB), 10,000 users × 4 providers up to four times that. Keys are created on first charge, so only combinations with traffic exist. Keys never accumulate across periods. Claim values are hashed in key names. Redis must run with `maxmemory-policy noeviction` and AOF/RDB persistence; otherwise a data loss restarts the period's spend at zero (FR-022a).
* **Sensitive columns**: none. No credentials, API keys or tokens are stored. `owners` can hold identity-claim values listed in a claim owner (for example a `team` name, or a user's `sub`); this is administrator-entered configuration, stored in clear. `audit.details` holds a budget's mutable fields only.
* **Not stored**: spend, request counts, refused-request counts, unpriced counts and threshold state (FR-024d).

# 5. Upgrade and Migration Impact

* **Fresh install**: covered by the guarded `CREATE TABLE` / `CREATE INDEX` statements and the `details` column in the `CREATE TABLE audit` body.
* **Existing deployment**: the new table and indexes are created by their guarded `CREATE ... IF NOT EXISTS` (or `OBJECT_ID` / `sys.indexes` guard on SQL Server) at start-up. The one shipped-table change, the nullable `audit.details` column, needs an `ALTER` path, which runs on every start-up and is a no-op once the column exists:

```sql
-- PostgreSQL
ALTER TABLE audit ADD COLUMN IF NOT EXISTS details BYTEA NULL;

-- SQL Server
IF COL_LENGTH(N'dbo.audit', N'details') IS NULL
ALTER TABLE dbo.audit ADD details NVARCHAR(MAX) NULL;

-- SQLite (guarded in platform-api/internal/database/connection.go by a pragma_table_info('audit') check)
ALTER TABLE audit ADD COLUMN details TEXT NULL;
```

  Existing `audit` rows keep `details = NULL`. A start-up test (`platform-api/internal/database/audit_details_upgrade_test.go`) creates a database with the previous `audit` DDL, starts the service, and checks that the column is added and that re-running is a no-op. A schema parity test (`platform-api/internal/database/budgets_schema_test.go`) checks that `budgets` is identical across dialects and re-applies cleanly.
* **Rollback**: the previous release runs unchanged against the upgraded schema; it ignores the new table and never selects `audit.details`. Redis keys expire on their own.
* **Tested against**: NOT ANSWERED — not yet exercised on any dialect.

# 6. Accepted Legacy Deviations

| Table | Rule | Deviation | Recorded |
| :---- | :---- | :---- | :---- |
| gateway-controller `subscription_plans`, `subscriptions`, `api_keys` (representative of a blanket pattern in `gateway/gateway-controller/pkg/storage/gateway-controller-db*.sql` and `resources/gateway-controller-db.sql`) | R4 (no `CHECK` enums) | `status` columns use `CHECK(status IN (...))` enum constraints. Shipped, so accepted legacy; the new controller `budgets` table must not copy the pattern. To be added to Appendix A of `.claude/rules/db-schema-changes.md`. | 2026-10-09 |

# 7. Open Questions

| # | Question | Context |
| :---- | :---- | :---- |
| 1 | Architecture guidance received 2026-10-09 said to **avoid new tables**. Is a single `budgets` definitions table in platform-api acceptable? The spend tables of the earlier design are gone. | Definitions are organization-level configuration edited in the UI and pushed to gateways. Owner: architecture leads. |
| 2 | `amount_nano` is `BIGINT` (maximum about 9.22 × 10¹⁸ nano-USD, i.e. about USD 9.22 billion), but the API accepts amounts with up to 12 integer digits (up to USD 999,999,999,999.999999), which overflows the column. Should the API cap the integer digits (10 digits fits), or should storage change? | Arithmetic on the stated types and validation rule. |
| 3 | ~~`idx_budgets_scope` is a prefix of the owner-and-period `UNIQUE`.~~ Resolved 2026-10-14: the scope columns and that index are gone; `idx_budgets_project` is not a prefix of the new `UNIQUE`. | Owner model change. |
| 7 | Owners are a JSON column (no FKs, no index on owner values). Acceptable, or should they be a `budget_owners` child table (a second new table, against the "avoid new tables" guidance)? | Decided 2026-10-14 as a JSON column; revisit if per-organization budget counts grow far beyond ~1,000. Owner: architecture leads. |
| 4 | DB rule R1-IDENTITY asks named resources for `name` and `version`. `budgets` uses `display_name` (as `subscription_plans` does) and has no `version`. Accept as the convention for this resource type, or add the columns? | New table, so the rule applies. |
| 5 | Gateway-controller `budgets` copy: exact columns, types, key shape (existing controller tables are keyed by `(gateway_id, uuid)`) and DDL for all four controller schema files are not designed. | Only the field list is fixed. |
| 6 | Should the end-to-end fixture `tests/integration-e2e/init-db.sql` also receive the `budgets` table and the `audit.details` column? | DB rule R0-SCOPE says fixtures are kept in sync. The design names `schema.sql` and `init-platform-api-db.sql` (§3) but not this fixture. |
