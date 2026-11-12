# Contract: Budget definitions to gateways, and the gateway's Redis configuration

Internal contracts only (not public). See [research.md](../research.md) R2, R5, R6, R9.

**Revised 2026-10-09**: only budget **definitions** travel between platform-api and gateways. There is no usage or totals sync: the runtime → controller ingest (`POST /internal/budgets/usage`), the `budget.usage` / `budget.totals` websocket messages, replica source ids, sync intervals, fast sync and disconnect alerts are removed (§4). Spend is counted in the shared Redis (§3).

```text
              definitions (rare)
platform-api ── WS budget.created/updated/deleted ──► gateway-controller ── LazyResources xDS ──► policy engine (N replicas)
             ◄── GET /api/internal/v1/budgets (resync on connect)                                        │
                                                                                     GET / INCRBY / SET NX
                                                                                                         ▼
                                                                  shared Redis (one per organization, all gateways)
```

---

## 1. Definitions: platform-api → gateway

### 1.1 Websocket events (platform-api → controller)

Broadcast to **every gateway in the organization** (pattern: `subscription_plan_service.go:264-290` → `BroadcastSubscriptionPlanUpdatedEvent`).

```json
{
  "type": "budget.updated",
  "payload": {
    "budget": {
      "uuid": "6f1c…",
      "handle": "search-each-user-openai",
      "displayName": "Search – each user on OpenAI",
      "contextType": "PROJECT",
      "projectUuid": "9a0e…",
      "owners": [
        { "dimension": "LLM_PROVIDER", "values": ["8be2…", "c41d…"] },
        { "dimension": "CLAIM", "claimName": "sub", "each": true }
      ],
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

Types: `budget.created`, `budget.updated`, `budget.deleted` (payload `{ "budgetUuid": "…" }`). Handlers are idempotent (upsert by uuid, compare `updatedAt`).

- `amountVersion` increments on every amount change (data-model §1). Gateways use it in the threshold marker key (data-model §5.2), so an amount change re-arms thresholds without any extra message. (The former `resetNotifiedThresholds` flag is removed.)
- `thresholds` are needed by the gateway because threshold detection now happens there (research R9).
- Org and gateway are never taken from the payload; the controller is already bound to one org.
- `owners` uses uuids (not handles) and is already in canonical order (data-model §1.1); the gateway builds `valueKey` from the `each` owners in that order. The gateway never resolves handles. A `MODEL` owner's `values` are model ids themselves (not uuids). *(2026-10-14)* An owner carries `values` (one or more, sorted; a request matches if its value is any of them, and they share one counter) or `each: true`; the gateway indexes a multi-value owner under every listed value.
- *(2026-10-14)* The gateway computes override sets (most specific wins, FR-003a) itself from the definitions it holds, whenever a budget is added, changed or removed; `overrides` / `overriddenBy` are not part of the push. A gateway that cannot read the upstream model id (prerequisite, plan risk 2a) logs a WARN and does not apply budgets with a `MODEL` owner.
- *(Revised 2026-10-14)* `scopeType` / `scopeKey` / `scopeValue` / `perValue` are replaced by `contextType`, `projectUuid` and `owners`.

### 1.2 Resync on connect (controller → platform-api)

`GET /api/internal/v1/budgets` — gateway-token auth (`authenticateRequest`, `gateway_internal.go:112-140`); add the path to `SkipPaths` and to `resources/gateway-internal-api.yaml`. Allowed in read-only mode (it is a read).

Response `200`: `{ "list": [ <budget as in 1.1> … ] }` — all non-deleted budgets of the gateway's org (including `DISABLED`, so the policy can skip them explicitly). The controller reconciles (deletes what is absent), mirroring `syncSubscriptionPlans` (`client.go:867`).

### 1.3 Controller → policy engine

LazyResource `{ ID: <budget uuid>, ResourceType: "Budget", Resource: <budget map> }` via `lazyResourceManager.StoreResource` / `RemoveResourceByIDAndType`. Rebuilt from the controller DB on startup.

### 1.4 Control plane unreachable

The controller keeps its last definitions (DB + LazyResources); the policy engine keeps enforcing with them. Counting is unaffected because it only uses Redis (FR-021). On reconnect, §1.2 runs.

---

## 2. Gateway activation

```toml
[budgets]
enabled = false            # true injects wso2_apip_sys_budget into LLM routes; requires the redis block below
```

- `budgets.enabled = true` without `[policy_configurations.budgets_v1.redis]` → the gateway-controller **fails config validation at startup** with `budgets.enabled requires [policy_configurations.budgets_v1.redis] (Redis is required for budgets)`; the policy factory refuses to initialise as a second guard (FR-022).
- `budgets.enabled = false` → the policy is not injected; definitions are still received and stored (so enabling later needs no resync), but **nothing is enforced on that gateway** (plan risk 9).

---

## 3. Redis configuration (gateway runtime)

```toml
[policy_configurations.budgets_v1]
failure_mode = "closed"     # Redis unreachable: "closed" refuses requests under enforce-mode budgets (503 BUDGET_CHECK_UNAVAILABLE) | "open" allows them uncounted
config_check_interval = "10m"   # how often to re-check maxmemory-policy / persistence and WARN if unsafe

[policy_configurations.budgets_v1.redis]   # same shape as advanced-ratelimit redis.*
host = "redis"
port = 6379
username = ""
password = "${REDIS_PASSWORD}"
db = 0
key_prefix = "budget:v1:"   # fixed default; change only to share a Redis between isolated environments
dial_timeout = "500ms"
read_timeout = "100ms"
write_timeout = "100ms"
pool_size = 50

[policy_configurations.budgets_v1.redis.tls]
enabled = true
ca_file = "/etc/ssl/redis/ca.pem"
server_name = "redis"
cert_file = "/etc/ssl/redis/client.pem"   # mTLS (optional)
key_file = "/etc/ssl/redis/client-key.pem"
insecure_skip_verify = false
```

Semantics:
- Every replica of every gateway of the organization **must point at the same Redis** and `db` (FR-019). Different organizations may share a Redis (keys contain budget uuids, which are globally unique).
- On startup and every `config_check_interval`: best-effort `CONFIG GET maxmemory-policy` and `INFO persistence`; WARN `budget redis may lose data: maxmemory-policy=<p>, aof=<a>, rdb=<r>` when not `noeviction` or no persistence (FR-022). If `CONFIG` is not permitted (managed Redis), log INFO once and continue.
- Secrets (password, client key) come from env/file references, never inline in a ConfigMap (Helm: Secret).
- Key schema and TTLs: [data-model.md §5](../data-model.md).

---

## 4. Removed 2026-10-09

| Removed | Reason |
|---|---|
| `POST /internal/budgets/usage` (runtime → controller), its auth/authz rules and IP allow-list | no gateway-local counting |
| WS `budget.usage` (controller → platform-api) and `budget.totals` (platform-api → controller) | no spend in platform-api |
| Replica `sourceId` / `seq`, controller usage outbox, `budget_sync_cursors` | idempotency of a sync that no longer exists |
| `mode` (`default` / `strict`), `sync_interval`, `sync_interval_fast`, `fast_sync_percent`, `disconnect_alert_after`, `failure_mode = fallback` | Redis is the only mode; fail-open is `failure_mode = "open"` |
| `resetNotifiedThresholds` on `budget.updated` | replaced by `amountVersion` in the marker key |
