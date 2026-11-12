# **Budgets - REST APIs**

# 1. Summary of Changes

| Endpoint | Method | Change | Breaking? |
| :---- | :---- | :---- | :---- |
| `/api/v0.9/budgets` | GET | New — ListBudgets | No |
| `/api/v0.9/budgets` | POST | New — CreateBudget | No |
| `/api/v0.9/budgets/{budgetId}` | GET | New — GetBudget | No |
| `/api/v0.9/budgets/{budgetId}` | PUT | New — UpdateBudget | No |
| `/api/v0.9/budgets/{budgetId}` | DELETE | New — DeleteBudget | No |
| `/api/internal/v1/budgets` | GET | New — internal, gateway-token authenticated (definition resync) | No |
| Gateway websocket: `budget.created`, `budget.updated`, `budget.deleted` (platform-api → gateway-controller) | — | New internal message types on the existing channel | No |
| Every LLM provider and LLM proxy route on a gateway with budgets enabled | any | Modified — new 429 `BUDGET_EXHAUSTED` and 503 `BUDGET_CHECK_UNAVAILABLE` responses, new `Budget-*` response headers, `include_usage` added to streamed chat completions (§3) | No for existing clients that ignore unknown headers; behaviour changes only once an organization creates budgets |

The public API returns **budget definitions only**. The API Platform keeps no spend: spend is counted in the gateways' shared Redis and analysed in Moesif, so there are no spend fields, no per-value listing and no spend filters. There is no gateway → platform-api usage interface.

All public endpoints follow the platform's existing conventions (the subscription-plans resource is the reference): base path `/api/v0.9`, kebab-case paths, camelCase fields, PascalCase operation ids, the shared pagination envelope `{count, list, pagination{total, offset, limit}}`, and the shared error shape `{status, code, message, errors?, details?, trackingId?}`. Client-facing error messages are generic; the specific reason is logged internally only.

Common to every public endpoint:

| | |
| :---- | :---- |
| Authentication | OAuth2 bearer token (`Authorization: Bearer`), verified by the platform's existing authentication middleware. No new authentication is introduced. |
| Tenant scoping | The organization is taken **only** from the verified token claims placed in the request context by the authentication middleware — never from the path, query or body. Every repository query filters on that organization (GO-AUTH-005). A budget, project, LLM provider, LLM proxy or application belonging to another organization is indistinguishable from one that does not exist (same 404 response). |
| Authorization | Per-operation scope check. `ap:budget:manage` (granted to `ap_admin` by default) for writes; `ap:budget:read` (granted to `ap_operator`) or `ap:budget:manage` for reads. Being a member of a budget's owner (a project's members, a team) grants nothing (FR-028). |
| Read-only mode | Writes are refused in the budget service and repository layers, not only in handlers (GO-AUTH-015). |

Shared error responses (used by every public endpoint below):

| Status | Condition | Body |
| :---- | :---- | :---- |
| 401 | Missing, invalid, expired or revoked token — identical response for every cause | Platform's shared Unauthorized response (see §5, question 6) |
| 403 | Token lacks the required scope | Platform's shared Forbidden response, generic message |
| 500 | Unexpected error | `{"status": "error", "code": ..., "message": ..., "trackingId": ...}` with a generic message and a random tracking id; stack and cause logged only |

# 2. New Endpoints

## 2.1 ListBudgets

Lists the organization's budget definitions. Called by the AI Workspace Budgets page. No spend is returned — usage is in Insights / Moesif.

```
GET /api/v0.9/budgets
```

**Audience**: Public

**Authorization**

| | |
| :---- | :---- |
| Authentication | OAuth2 bearer |
| Required scope(s) | `ap:budget:read` or `ap:budget:manage` |
| Tenant scoping | Organization from verified token claims; only that organization's budgets are returned |

**Request**

```
Headers:
  Authorization: Bearer {token}

Query parameters:
  limit            integer  No  shared default  Page size, 1–100 (shared limit parameter)
  offset           integer  No  0               Zero-based index of the first item (shared offset parameter)
  query            string   No  —               Free-text search (shared query parameter)
  project          string   No  —               Project handle; only budgets created in that project (project mode). Omit in org mode to list all budgets
  contextType      string   No  —               ORGANIZATION | PROJECT
  status           string   No  —               ACTIVE | DISABLED
  enforcementMode  string   No  —               ENFORCE | MONITOR
```

**Response** — `200 OK`

```json
{
  "count": 2,
  "list": [
    {
      "id": "team-search-monthly",
      "uuid": "6f1c2a0e-7d1b-4a59-9a3e-0f6f2b8d1c11",
      "displayName": "Search team – monthly",
      "description": "Chargeback for the search squad",
      "context": { "type": "ORGANIZATION" },
      "owners": [ { "dimension": "CLAIM", "claimName": "team", "values": ["search"] } ],
      "amount": "1500.00",
      "currency": "USD",
      "period": "MONTH",
      "enforcementMode": "ENFORCE",
      "thresholds": [50, 80, 100],
      "status": "ACTIVE",
      "createdBy": "admin@acme.com",
      "createdAt": "2026-10-01T08:00:00Z",
      "updatedBy": "admin@acme.com",
      "updatedAt": "2026-10-08T09:12:44Z"
    },
    {
      "id": "search-each-user-openai",
      "uuid": "0b7e5c1d-2f44-4d0e-8a51-6c3d9e2a7f02",
      "displayName": "Search – each user on OpenAI or Anthropic",
      "context": { "type": "PROJECT", "project": "search", "displayName": "Search" },
      "owners": [
        { "dimension": "LLM_PROVIDER", "values": ["anthropic-prod", "openai-prod"], "displayNames": ["Anthropic", "OpenAI"] },
        { "dimension": "CLAIM", "claimName": "sub", "each": true }
      ],
      "amount": "25.00",
      "currency": "USD",
      "period": "DAY",
      "enforcementMode": "ENFORCE",
      "thresholds": [80, 100],
      "status": "ACTIVE",
      "createdBy": "admin@acme.com",
      "createdAt": "2026-10-01T08:00:00Z",
      "updatedBy": "admin@acme.com",
      "updatedAt": "2026-10-01T08:00:00Z"
    }
  ],
  "pagination": { "total": 2, "offset": 0, "limit": 20 }
}
```

**Error responses**

| Status | Condition | Body |
| :---- | :---- | :---- |
| 400 | `limit` outside 1–100, or an unknown `contextType` / `status` / `enforcementMode` value | `code: BUDGET_INVALID`, generic message |
| 404 | `project` names a project that does not exist in the caller's organization | `code: BUDGET_OWNER_NOT_FOUND`, generic message |
| 401 / 403 | See shared error responses | Shared bodies |

**Notes**

* Budgets with "each" owners are returned as their definition only (`each: true`); per-value spend is in Moesif (Budget Usage card with the Budget filter set to the budget).
* Owners are returned in canonical order: `PROJECT`, `LLM_PROVIDER`, `LLM_PROXY`, `MODEL`, `APPLICATION`, then `CLAIM` by claim name.
* Each budget carries the read-only `overrides` and `overriddenBy` lists (most specific wins; see §2.3).

## 2.2 CreateBudget

Creates a budget. Takes effect on every gateway of the organization without redeploying anything.

```
POST /api/v0.9/budgets
```

**Audience**: Public

**Authorization**

| | |
| :---- | :---- |
| Authentication | OAuth2 bearer |
| Required scope(s) | `ap:budget:manage` |
| Tenant scoping | Organization from verified token claims. `context.project` and owner values are resolved only within that organization (in a project context, proxies and applications only within that project); `created_by` / `updated_by` are taken from the verified token |

**Request**

```
Headers:
  Authorization: Bearer {token}
  Content-Type: application/json
```

```json
{
  "handle": "alice-openai-monthly",
  "displayName": "Alice on OpenAI – monthly",
  "description": "Alice's OpenAI allowance",
  "context": { "type": "ORGANIZATION" },
  "owners": [
    { "dimension": "CLAIM", "claimName": "sub", "values": ["alice@acme.com"] },
    { "dimension": "LLM_PROVIDER", "values": ["openai-prod"] }
  ],
  "amount": "1500.00",
  "currency": "USD",
  "period": "MONTH",
  "enforcementMode": "ENFORCE",
  "thresholds": [50, 80, 100]
}
```

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| `handle` | string | No | Up to 40 characters; unique in the organization; generated from `displayName` if omitted; immutable. |
| `displayName` | string | Yes | 1–255 characters. |
| `description` | string | No | Up to 1023 characters. |
| `context.type` | string | Yes | The first owner: `ORGANIZATION` (created in org mode) or `PROJECT` (created in project mode). |
| `context.project` | string | For `PROJECT` | Project handle. Must be omitted for `ORGANIZATION`. |
| `owners` | object[] | No (default `[]`) | Appended owners; a request counts only when it matches the context and **every** owner. No limit on the number of owners in v1. Any order; stored and returned in canonical order. |
| `owners[].dimension` | string | Yes | `PROJECT` (only when `context.type` is `ORGANIZATION`), `LLM_PROVIDER`, `LLM_PROXY`, `MODEL`, `APPLICATION`, `CLAIM`. Each dimension at most once; `CLAIM` at most once per `claimName`. |
| `owners[].claimName` | string | For `CLAIM` | Claim name, `^[A-Za-z0-9_.:-]{1,255}$` (for example `sub`, `team`, `department`, `cost_centre`). Any claim in the caller's verified token can be used. |
| `owners[].values` | string[] | Exactly one of `values` / `each` | One or more handles of projects / LLM providers / LLM proxies / applications, model ids (`MODEL`: the upstream model id sent to the provider after any proxy rewrite, e.g. `gpt-4o`; each 1–255 characters, exact and case-sensitive, not checked against the model catalog), or claim values. A request matches when its value is **any** of them, and the listed values share one allowance — e.g. `["openai-prod", "anthropic-prod"]` with `amount` `2000.00` is one $2,000 pool for both providers. Non-empty, no duplicates, no limit on length in v1; stored and returned sorted. |
| `owners[].each` | boolean | Exactly one of `values` / `each` | `true`: every value of this dimension (including ones that appear later) gets its own allowance of `amount`; with several `each` owners, every combination of values does. |
| `amount` | string | Yes | USD decimal string, `^\d{1,12}(\.\d{1,6})?$`, greater than zero. |
| `currency` | string | No (default `USD`) | Only `USD`. |
| `period` | string | Yes | `DAY` (resets 00:00 UTC), `WEEK` (ISO week, resets Monday 00:00 UTC), `MONTH` (resets 00:00 UTC on the 1st). |
| `enforcementMode` | string | No (default `ENFORCE`) | `ENFORCE` (refuse when exhausted) or `MONITOR` (track and warn only). |
| `thresholds` | integer[] | No | Up to five unique values, each 1–100 (percent of `amount`). |

**Response** — `201 Created`, with a `Location` header for the new budget. Body: the `Budget` object as in GetBudget (§2.3).

**Error responses**

| Status | Condition | Body |
| :---- | :---- | :---- |
| 400 | Field validation failure (name, description, amount, currency, period, mode, thresholds), or unknown request fields (strict decoding — no mass assignment) | `code: BUDGET_INVALID`, generic message |
| 400 | Invalid context or owner: missing or unknown `context.type`; `PROJECT` without `project` or `ORGANIZATION` with `project`; unknown `dimension`; both or neither of `values` / `each`; an empty `values` list or duplicate values; `CLAIM` without a valid `claimName`; a `MODEL` value that is empty, longer than 255 characters or contains control characters; an LLM provider/proxy value resolving to an artifact of the other kind | `code: BUDGET_INVALID_OWNER`, generic message |
| 400 | The same `dimension` twice, or `CLAIM` twice with the same `claimName` | `code: BUDGET_OWNER_DUPLICATE`, generic message |
| 400 | A `PROJECT` owner on a budget whose context is a project | `code: BUDGET_OWNER_NOT_ALLOWED`, generic message |
| 404 | `context.project` or an owner `value` names a project / LLM provider / LLM proxy / application that does not exist in the caller's organization, or (project context) an LLM proxy / application of another project — same response whether it exists in another organization or not at all | `code: BUDGET_OWNER_NOT_FOUND`, generic message that does not echo the value |
| 409 | A budget with the same context, owners (in any order, with values in any order) and period already exists, or the `handle` is already used in the organization | `code: BUDGET_ALREADY_EXISTS`, generic message |
| 401 / 403 | See shared error responses | Shared bodies |

**Notes**

* The same owners may have several budgets with different periods (for example a daily and a monthly ceiling); context plus owners plus period is unique per organization.
* All budgets whose owners match a request apply, except that the **most specific wins**: a budget overrides another with the same context, period and owner dimensions when it is narrower on at least one dimension and no broader on any (a values list where the other has `each`, or a proper subset of the other's values), e.g. `sub = alice` $50 overrides `sub = each` $10 for alice, and `LLM_PROVIDER` `[openai-prod]` $500 overrides `[anthropic-prod, openai-prod]` $2,000 for OpenAI traffic. Budgets with different dimensions or contexts stack.
* Creating a budget that overrides another is not an error: the `201` body lists them in `overrides` and carries a `warnings` entry `BUDGET_OVERRIDES_OTHER` naming them (for example, a `MONITOR` budget for `sub = alice` lifts an `ENFORCE` `sub = each` budget for alice).
* A new budget starts at zero spend for the current period (never backdated).
* Create is audit-logged with the actor and the new values, and broadcast to every gateway of the organization as `budget.created` (§2.7). It enforces on every gateway within 60 seconds (SC-002).
* Delivery phase 1 accepts both contexts and the `PROJECT`, `LLM_PROVIDER` and `LLM_PROXY` owner dimensions; `CLAIM`, `APPLICATION` and `MODEL` owners arrive in phase 2 (`MODEL` needs the gateway to expose the upstream model id to the budget policy).

## 2.3 GetBudget

Returns one budget definition.

```
GET /api/v0.9/budgets/{budgetId}
```

**Audience**: Public

**Authorization**

| | |
| :---- | :---- |
| Authentication | OAuth2 bearer |
| Required scope(s) | `ap:budget:read` or `ap:budget:manage` |
| Tenant scoping | Lookup by handle **and** the organization from verified token claims |

**Request**

```
Headers:
  Authorization: Bearer {token}

Path parameters:
  budgetId  string  Budget handle
```

**Response** — `200 OK`

```json
{
  "id": "chat-proxy-monthly",
  "uuid": "9a2d4c6e-1b3f-4e8a-b7c9-5d1e3f2a4b60",
  "displayName": "Chat proxy monthly",
  "description": "",
  "context": { "type": "ORGANIZATION" },
  "owners": [ { "dimension": "LLM_PROXY", "values": ["chat-proxy"], "displayNames": ["chat-proxy"] } ],
  "amount": "0.05",
  "currency": "USD",
  "period": "MONTH",
  "enforcementMode": "ENFORCE",
  "thresholds": [50, 80, 100],
  "status": "DISABLED",
  "statusReason": "OWNER_DELETED",
  "overrides": [],
  "overriddenBy": [ { "id": "chat-proxy-alice", "displayName": "Chat proxy — alice" } ],
  "createdBy": "admin@acme.com",
  "createdAt": "2026-10-08T09:00:00Z",
  "updatedBy": "system",
  "updatedAt": "2026-10-09T10:30:00Z"
}
```

| Field | Type | Description |
| :---- | :---- | :---- |
| `id` | string | Budget handle. The same value is published as `metadata.budget_id` in analytics, which the Moesif Budget filter uses. |
| `uuid` | string | Budget uuid; used by gateways in Redis key names. |
| `context.displayName` | string | Response only: the project's display name; absent for `ORGANIZATION`. |
| `owners[].displayNames` | string[] | Response only: display names of the listed projects / LLM providers / LLM proxies / applications, in the same order as `values`; absent for `MODEL`, `CLAIM` and `each` owners. |
| `statusReason` | string | Response only: `OWNER_DELETED` when the platform disabled the budget because its context project, or a project / LLM provider / LLM proxy / application named by one of its owners, was deleted; absent when an administrator set the status; cleared when an administrator enables or disables the budget. |
| `overrides` | object[] | Response only, computed from definitions: budgets (`id`, `displayName`) this budget overrides — same context, period and owner dimensions, and this budget at least as narrow on every dimension (a values list is narrower than `each`; a subset of values is at least as narrow) and narrower on at least one. For a request matching both, the overridden budget is skipped (not checked, not charged). |
| `overriddenBy` | object[] | Response only: budgets that override this one for the values they name. |
| `warnings` | object[] | Create and update responses only: `{ code: "BUDGET_OVERRIDES_OTHER", message, budgets[] }` when the saved budget overrides others. |

**Error responses**

| Status | Condition | Body |
| :---- | :---- | :---- |
| 404 | No budget with that handle in the caller's organization | `code: BUDGET_NOT_FOUND`, generic message |
| 401 / 403 | See shared error responses | Shared bodies |

## 2.4 UpdateBudget

Changes the mutable fields of a budget: display name, description, amount, thresholds, enforcement mode and status (enable/disable).

```
PUT /api/v0.9/budgets/{budgetId}
```

**Audience**: Public

**Authorization**

| | |
| :---- | :---- |
| Authentication | OAuth2 bearer |
| Required scope(s) | `ap:budget:manage` |
| Tenant scoping | Lookup by handle and the organization from verified token claims; `updated_by` from the verified token |

**Request**

```
Headers:
  Authorization: Bearer {token}
  Content-Type: application/json

Path parameters:
  budgetId  string  Budget handle
```

```json
{
  "displayName": "Search team – monthly",
  "description": "Raised for the October launch",
  "amount": "10000.00",
  "enforcementMode": "ENFORCE",
  "thresholds": [50, 80, 100],
  "status": "ACTIVE"
}
```

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| `displayName` | string | Yes | 1–255 characters. |
| `description` | string | No | Up to 1023 characters. |
| `amount` | string | Yes | Same rule as on create. |
| `enforcementMode` | string | No | `ENFORCE` or `MONITOR`. |
| `thresholds` | integer[] | No | Up to five unique values, 1–100. |
| `status` | string | No | `ACTIVE` or `DISABLED`. Setting it clears `statusReason`. |

**Response** — `200 OK`, body: the updated `Budget` object (as in §2.3).

**Error responses**

| Status | Condition | Body |
| :---- | :---- | :---- |
| 400 | Field validation failure or unknown request fields | `code: BUDGET_INVALID`, generic message |
| 400 | The request changes the context, the owners, or the period | `code: BUDGET_IMMUTABLE_FIELD`, generic message |
| 404 | No budget with that handle in the caller's organization | `code: BUDGET_NOT_FOUND`, generic message |
| 401 / 403 | See shared error responses | Shared bodies |

**Notes**

* Changing the amount never changes recorded spend (the Redis counter). It increments the budget's amount version, which re-arms every threshold on the gateways; if spend is already above several thresholds, only the highest is emitted on the next charge.
* Lowering the amount below current spend takes effect immediately (requests are refused until the period resets).
* Switching `MONITOR` → `ENFORCE` counts spend already recorded this period; enforcement starts immediately.
* Disabling keeps spend recorded while enabled (the Redis key keeps its value until it expires); spend while disabled is not counted.
* Every update is audit-logged with before/after values and broadcast as `budget.updated`. No analytics event is published.

## 2.5 DeleteBudget

Deletes a budget. Enforcement stops within seconds on every gateway; its Redis counters expire on their own after the period ends. Past spend remains in Moesif.

```
DELETE /api/v0.9/budgets/{budgetId}
```

**Audience**: Public

**Authorization**

| | |
| :---- | :---- |
| Authentication | OAuth2 bearer |
| Required scope(s) | `ap:budget:manage` |
| Tenant scoping | Lookup by handle and the organization from verified token claims |

**Request**

```
Headers:
  Authorization: Bearer {token}

Path parameters:
  budgetId  string  Budget handle
```

**Response** — `204 No Content`. No body.

**Error responses**

| Status | Condition | Body |
| :---- | :---- | :---- |
| 404 | No budget with that handle in the caller's organization | `code: BUDGET_NOT_FOUND`, generic message |
| 401 / 403 | See shared error responses | Shared bodies |

**Notes**

* Hard delete; audit-logged with the before values and broadcast as `budget.deleted`. A re-created budget gets a new uuid and never sees the old counters.

## 2.6 List budgets for a gateway (definition resync)

Called by the gateway-controller on every (re)connect to resynchronise budget definitions, mirroring the existing subscription-plan resync.

```
GET /api/internal/v1/budgets
```

**Audience**: Internal (gateway-controller → platform-api)

**Authorization**

| | |
| :---- | :---- |
| Authentication | Gateway token, validated by the existing `authenticateRequest` in `platform-api/internal/handler/gateway_internal.go`. The path is added to the user-JWT middleware's `SkipPaths` because it uses gateway-token authentication instead. |
| Required scope(s) | None — gateway-token authenticated, as for the other internal gateway endpoints. |
| Tenant scoping | Organization and gateway are resolved server-side from the gateway token; the endpoint takes no organization input. |

**Response** — `200 OK`

```json
{
  "list": [
    {
      "uuid": "6f1c2a0e-7d1b-4a59-9a3e-0f6f2b8d1c11",
      "handle": "team-search-monthly",
      "displayName": "Search team – monthly",
      "contextType": "ORGANIZATION",
      "projectUuid": "",
      "owners": [ { "dimension": "CLAIM", "claimName": "team", "values": ["search"] } ],
      "amountNano": 1500000000000,
      "amountVersion": 3,
      "period": "MONTH",
      "enforcementMode": "ENFORCE",
      "thresholds": [50, 80, 100],
      "status": "ACTIVE",
      "updatedAt": "2026-10-08T09:12:44Z"
    }
  ]
}
```

All non-deleted budgets of the gateway's organization, including `DISABLED` ones so the policy can skip them explicitly. The controller reconciles its local copy and deletes anything absent. Allowed in read-only mode (it is a read).

**Error responses**

| Status | Condition | Body |
| :---- | :---- | :---- |
| 401 | Missing or invalid gateway token | The internal API's existing error response |
| 500 | Unexpected error | The internal API's existing error response, generic message |

## 2.7 Definition push (platform-api → gateway-controller, websocket)

Broadcast over the existing authenticated gateway websocket to **every gateway in the organization** on create, update and delete, using the same pattern as subscription-plan events.

```json
{
  "type": "budget.updated",
  "payload": {
    "budget": {
      "uuid": "6f1c2a0e-7d1b-4a59-9a3e-0f6f2b8d1c11",
      "handle": "team-search-monthly",
      "displayName": "Search team – monthly",
      "contextType": "ORGANIZATION",
      "projectUuid": "",
      "owners": [ { "dimension": "CLAIM", "claimName": "team", "values": ["search"] } ],
      "amountNano": 1500000000000,
      "amountVersion": 3,
      "period": "MONTH",
      "enforcementMode": "ENFORCE",
      "thresholds": [50, 80, 100],
      "status": "ACTIVE",
      "updatedAt": "2026-10-08T09:12:44Z"
    }
  }
}
```

| Type | Payload |
| :---- | :---- |
| `budget.created`, `budget.updated` | `{ "budget": { ... } }` as above |
| `budget.deleted` | `{ "budgetUuid": "…" }` |

* `amountVersion` increments on every amount change; gateways use it in the threshold-marker key, so an amount change re-arms thresholds with no extra message.
* `thresholds` travel because threshold detection runs on the gateway.
* `overrides` / `overriddenBy` are not pushed: each gateway computes override sets (most specific wins) from the definitions it holds whenever a budget is added, changed or removed, using the same rule as platform-api.
* Handlers are idempotent (upsert by uuid, compare `updatedAt`). Organization and gateway are never taken from the payload; the controller is already bound to one organization.
* Gateways older than this feature log and drop these unknown messages, so they do not enforce budgets.

# 3. Changes to Existing Endpoints

Every LLM provider and LLM proxy route served by a gateway with `budgets.enabled = true`, once the organization has at least one applicable budget.

**Refusal — enforce-mode budget exhausted**

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 2040420
Budget-Id: team-search-monthly
Budget-Reset: 2026-11-01T00:00:00Z
```

```json
{
  "status": "error",
  "code": "BUDGET_EXHAUSTED",
  "message": "The budget 'Search team – monthly' is exhausted until 2026-11-01T00:00:00Z.",
  "details": {
    "budget": {
      "id": "team-search-monthly",
      "context": { "type": "ORGANIZATION" },
      "owners": [ { "dimension": "CLAIM", "claimName": "team", "values": ["search"] } ],
      "period": "MONTH",
      "periodLabel": "2026-10",
      "amount": "1500.00",
      "currency": "USD",
      "resetAt": "2026-11-01T00:00:00Z"
    }
  }
}
```

* `code` is always `BUDGET_EXHAUSTED`, never the rate-limit code, so clients can stop retrying. A route that also has `llm-cost-based-ratelimit` keeps that policy's own 429 code.
* `Retry-After` = whole seconds until `resetAt`; with several exhausted budgets, the latest reset. It is an upper bound: raising the amount can make a retry succeed sooner.
* For `each` owners the body reports only the caller's own value (`"each": true, "callerValue": "<caller's value>"`); owners with `values` are reported as configured. Raised before anything is sent upstream.

**Budget check unavailable — Redis unreachable, `failure_mode = "closed"`**

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json
```

```json
{ "status": "error", "code": "BUDGET_CHECK_UNAVAILABLE", "message": "The request could not be checked against its budget. Try again later." }
```

* Only for requests subject to at least one enforce-mode budget. No `Retry-After`, no Redis detail.
* With `failure_mode = "open"` the request is allowed, carries no `Budget-*` headers and is not counted.

**Allowed responses** — headers for the most constraining budget (lowest remaining, read from the shared counter at admission):

| Header | Example | Meaning |
| :---- | :---- | :---- |
| `Budget-Id` | `team-search-monthly` | Most constraining budget |
| `Budget-Remaining` | `312.58` | USD remaining at admission (amount − shared counter), floored at 0 |
| `Budget-Reset` | `2026-11-01T00:00:00Z` | Period reset |
| `Budget-Monitor` | `would-refuse` | Only for a monitor-mode budget that would have refused |

For streamed responses the headers reflect the state at admission. Header names are platform-neutral (no vendor prefix, `.claude/rules/error-handling.md` directive 2).

**Request rewrite for streamed chat completions** — when any budget applies, the route is OpenAI-compatible and the body has `"stream": true`, the gateway forwards `"stream_options": { "include_usage": true }`, preserving existing `stream_options` fields.

* **Backward compatibility**: no change until an organization creates budgets and the gateway has budgets enabled. Clients then see the new headers, possible 429/503 responses with new codes, and — for streamed chat completions — one extra final SSE chunk with `usage` and empty `choices` (standard OpenAI behaviour).
* **Behaviour when omitted**: a request matching no budget is untouched and carries no `Budget-*` headers.

# 4. OpenAPI Spec

* Spec file(s) updated:
  * `platform-api/resources/openapi.yaml` — the five public operations (`ListBudgets`, `CreateBudget`, `GetBudget`, `UpdateBudget`, `DeleteBudget`) under a new `Budgets` tag, with schemas `BudgetContext`, `BudgetOwnerDimension`, `BudgetOwner`, `Money`, `CreateBudgetRequest`, `UpdateBudgetRequest`, `Budget`, `BudgetListResponse`. Reuses the shared `limit-Q`, `offset-Q`, `query-Q` parameters, the `Pagination` and `Error` schemas and the shared error responses. Models are generated with the existing models-only `oapi-codegen` configuration; handlers are hand-written.
  * `platform-api/resources/gateway-internal-api.yaml` — `GET /budgets` (served at `/api/internal/v1/budgets`).
  * `platform-api/resources/role-to-scope-mapping.yaml` — `ap:budget:manage` → `ap_admin`; `ap:budget:read` → `ap_operator` (and `ap_admin`).
  * REST API docs under `docs/rest-apis/` — Budgets endpoints (definitions only).
* Scopes declared in the spec (under the `OAuth2Security` scheme; startup fails if a mapped scope is undeclared):
  * `ap:budget:manage` — Create, update and delete budgets
  * `ap:budget:read` — Read budget definitions
* CLI sync required: NOT ANSWERED

# 5. Open Questions

| # | Question | Context |
| :---- | :---- | :---- |
| 1 | CreateBudget: is an unknown `context.project` or owner `value` a `404 BUDGET_OWNER_NOT_FOUND` or a `400`? | The validation rules give 404; the drafted OpenAPI fragment lists `BUDGET_OWNER_NOT_FOUND` under the 400 response and declares no 404 for CreateBudget. This document follows the validation rules. |
| 2 | The drafted OpenAPI fragment omits responses that the validation rules produce: `400` on ListBudgets (bad query parameters), `401` / `403` on GetBudget, UpdateBudget and DeleteBudget, and `404` on CreateBudget. They need adding when merging into `openapi.yaml`. | |
| 3 | UpdateBudget is a `PUT` with only `displayName` and `amount` required. Does omitting `description`, `thresholds`, `enforcementMode` or `status` keep the stored value or clear/reset it? | Not specified. |
| 4 | UpdateBudget: with strict decoding, a body carrying `context`, `owners` or `period` is rejected as an unknown field (`BUDGET_INVALID`) before the immutable-field check (`BUDGET_IMMUTABLE_FIELD`) can apply. Which code does the client get? | Both rules are stated. |
| 5 | Should `ap:budget:read` also be granted to `ap_viewer`? Proposed default: no. | Product decision. |
| 6 | The repository's error-handling rule prescribes the 401 body `{"error": "unauthorized", "message": "Invalid or expired credentials."}`; platform-api's shared Unauthorized response uses the `{status, code, message}` shape. These endpoints use the shared one. Confirm. | Platform-wide, not specific to budgets. |
| 7 | `amount` accepts up to 12 integer digits, but storage is `BIGINT` nano-USD, which overflows above about USD 9.22 billion. Cap the pattern at 10 integer digits? | See the Proposed DB Schema document, §7. |
| 8 | Handle format: `handle` is up to 40 characters, generated from `displayName` when omitted. Its character rules and collision handling on generation are not specified. | |
| 9 | Status and code of the error returned when a budget write is attempted in read-only mode. | Not specified. |
| 10 | Should platform-api warn (in the API or UI) when an organization has budgets but some connected gateways are older than this feature, or have budgets disabled, and so do not enforce them? | Older gateways drop unknown websocket messages. |
| 11 | Should the number of owners per budget, and of values per owner, be capped (for example 3 owners, with at most 2 `each`)? No limit in v1; only the request-body size bounds it. | Gateway matching cost and Redis key counts grow with owners (2026-10-14). |
| 12 | When a resource listed in a multi-value owner is deleted (e.g. one of two LLM providers), should platform-api drop that value and keep the budget active when other values remain, instead of disabling it with `statusReason = OWNER_DELETED`? | Owners are otherwise immutable (2026-10-14). |
