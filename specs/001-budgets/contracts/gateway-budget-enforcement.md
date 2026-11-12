# Contract: What callers and analytics see

Covers the gateway's behaviour toward API callers (refusals, headers), the request rewrite for streaming, and the analytics/Moesif data. See [research.md](../research.md) R3, R5, R7, R9, R10.

**Revised 2026-10-09**: all figures come from the shared Redis counter; threshold crossings are carried on the crossing request's analytics event (§4.1–4.2); platform-api publishes no Moesif actions (former §4.2 action and §4.3 `Budget Changed` removed — budget changes are in the audit log only). **Revised 2026-10-13**: fields confirmed under the Moesif event `metadata` object (publisher change required, §4.1); cost field is `metadata.aiMetadata.llmCost`; §4.2 reduced to one card, one filter and one alert rule. **Revised 2026-10-14**: budgets have a context and an owner list (data-model §1.1); `scope` in the 429 body and `budget_scope`/`budget_scope_value` in events are replaced by `context`/`owners` and `budget_owners`/`budget_owner_values`.

---

## 1. Refusal (enforce-mode budget exhausted)

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Date: Thu, 08 Oct 2026 09:13:00 GMT
Retry-After: 2040420
Budget-Id: search-each-user-openai
Budget-Reset: 2026-11-01T00:00:00Z

{
  "status": "error",
  "code": "BUDGET_EXHAUSTED",
  "message": "The budget 'Search – each user on OpenAI' is exhausted until 2026-11-01T00:00:00Z.",
  "details": {
    "budget": {
      "id": "search-each-user-openai",
      "context": { "type": "PROJECT", "project": "search" },
      "owners": [
        { "dimension": "LLM_PROVIDER", "values": ["anthropic-prod", "openai-prod"] },
        { "dimension": "CLAIM", "claimName": "sub", "each": true, "callerValue": "alice@acme.com" }
      ],
      "period": "MONTH",
      "periodLabel": "2026-10",
      "amount": "1500.00",
      "currency": "USD",
      "resetAt": "2026-11-01T00:00:00Z"
    }
  }
}
```

Rules:
- `code` is always `BUDGET_EXHAUSTED` — never the rate-limit code — so clients can stop retrying (FR-013).
- Exhausted = the Redis counter for the budget's current period (and the caller's combination of values, for budgets with "each" owners) is ≥ the amount, read at admission.
- `Retry-After` = whole seconds until `resetAt`. If several budgets are exhausted, the one with the **latest** reset is reported (earliest time the request could pass).
- It is an upper bound: an administrator raising the amount can make a retry succeed sooner.
- For "each" owners the body reports the caller's own value only (`each: true` plus `callerValue`), never other callers' values. Owners with a `values` list report the list as configured (handles).
- Raised in the request-body phase, before anything is sent upstream (no provider cost incurred).

## 1a. Budget check unavailable (Redis unreachable, `failure_mode = "closed"`)

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json

{ "status": "error", "code": "BUDGET_CHECK_UNAVAILABLE", "message": "The request could not be checked against its budget. Try again later." }
```

- Only for requests subject to at least one **enforce-mode** budget. Requests subject only to monitor-mode budgets, or with no applicable budget, pass.
- No `Retry-After`, no Redis host or error detail in the body (logged only, error-handling directive 2/4).
- With `failure_mode = "open"` the request is allowed, carries no `Budget-*` headers, is not counted, and its analytics event has `budget_check_skipped = true`.

## 2. Allowed responses

Headers on every response to a request that matched at least one budget (most constraining budget = lowest remaining, from the counters read at admission):

| Header | Example | Meaning |
|---|---|---|
| `Budget-Id` | `team-search-monthly` | most constraining budget |
| `Budget-Remaining` | `312.58` | USD remaining at admission (amount − shared counter), floored at 0 |
| `Budget-Reset` | `2026-11-01T00:00:00Z` | period reset |
| `Budget-Monitor` | `would-refuse` | only for a **monitor-mode** budget that would have refused (US4) |

For streamed responses the headers reflect the state at request admission. Header names are deliberately platform-neutral (no vendor prefix — `.claude/rules/error-handling.md` directive 2; see research R10). A route that also has `llm-cost-based-ratelimit` keeps that policy's own 429 code; a budget refusal is always `BUDGET_EXHAUSTED` (FR-029).

## 3. Request rewrite for streamed chat completions (FR-016a)

When **any** budget applies (enforce **or monitor** mode, so monitor budgets measure streamed spend accurately), the route is an OpenAI-compatible chat completion and the body has `"stream": true`, the gateway forwards:

```json
{ "...": "...", "stream": true, "stream_options": { "include_usage": true } }
```

Existing `stream_options` fields are preserved. Effect for clients: one extra final SSE chunk with `usage` and empty `choices` (standard OpenAI behaviour).

## 4. Analytics and notifications (Moesif)

### 4.1 Per-request event fields (gateway analytics → Moesif)

Written by the budget policy as analytics metadata and published by the Moesif publisher under the event's **`metadata`** object as flat keys, so Moesif can filter/group/sum them as `metadata.<field>` (e.g. `metadata.budget_threshold_crossed`, `metadata.budget_spend`). Today the publisher forwards only an allow-list of keys into `metadata` (`policy-engine/internal/analytics/publishers/moesif.go`) and `prepareAnalyticEvent` (`internal/analytics/analytics.go`) copies only known `analytics_data` keys into event properties, so both must be extended to forward these fields (tasks T114/T115). `llm_cost_status`, `budget_check_skipped`, `budget_id` and `budget_name` are emitted from delivery phase 1; the others from phase 2. The flat `budget_*` fields describe the **most constraining** budget, except when this request crossed a threshold — then they describe the budget whose threshold was crossed (the highest-priority one if several). The `budgets` JSON lists every evaluated budget.

| Field | Type | Example |
|---|---|---|
| `budget_id` | string | `search-each-user-openai` |
| `budget_name` | string | `Search – each user on OpenAI` |
| `budget_owners` | string | `project=search; provider=anthropic-prod\|openai-prod; model=gpt-4o; sub=each` — context first, then owners in canonical order; a multi-value owner lists its values joined with `\|` |
| `budget_owner_values` | string | `model=gpt-4o; sub=alice@acme.com` — the caller's own values for the "each" owners, canonical order; empty when the budget has none |
| `budget_period` | string | `2026-10` |
| `budget_amount` | number | `1500.0` |
| `budget_spend` | number | `1201.10` — Redis total after this request's `INCRBY` (before it, for refused requests) |
| `budget_percent_used` | number | `80.07` |
| `budget_enforcement_mode` | string | `ENFORCE` \| `MONITOR` |
| `budget_refused` | boolean | `false` |
| `budget_monitor_would_refuse` | boolean | `false` |
| `budget_threshold_crossed` | integer | `80` — **present only on the one event, across all gateways, whose charge crossed this threshold** (Redis `SET NX` marker winner, data-model §5.2); absent otherwise |
| `budget_overridden_ids` | string | `per-user-default` — comma-separated ids of matching budgets skipped because a more specific matching budget overrode them (FR-003a); absent when no override applied. Overridden budgets are not checked, not charged and appear in no other `budget_*` field |
| `budget_check_skipped` | boolean | `true` only when Redis was unreachable and `failure_mode = "open"` |
| `budgets` | JSON string | all evaluated budgets `[{id, period, ownerValues?, amount, spend, refused, wouldRefuse, thresholdCrossed?}]` |
| `aiMetadata.llmCost` | number | `0.0125` — **existing** request cost, already published as `metadata.aiMetadata.llmCost` (`internal/analytics/dto/aiMetaData.go`); not a new field and not flat |
| `llm_cost_status` | string | `calculated` \| `not_calculated` (unpriced — FR-024a) |

- Refused requests also emit an event (`budget_refused = true`).
- Budget events — at least every event with `budget_threshold_crossed` or `budget_refused` — must be excluded from Moesif dynamic sampling; otherwise a crossing can be lost.
- Charge order: `INCRBY` happens at end of response, before the analytics event is finalised, so `budget_spend` and `budget_threshold_crossed` are on the same event as the cost.

### 4.2 Moesif presentation and threshold alert (feature owner, 2026-10-09/13)

Exactly three Moesif artifacts, delivered as templates with the user guide (mockups: `budgets-design-doc/mockups/moesif/`). Creating or changing budgets needs no Moesif change.

1. **Budget Usage card** — one new card on the **existing LLM Analytics page** of the embedded Moesif analytics (no new page, section or sidebar item; the rest of the page is unchanged). One row per budget, grouped by `metadata.budget_name` and `metadata.budget_period`:

   | Column | Query |
   |---|---|
   | Budget | `metadata.budget_name` |
   | Amount | `max(metadata.budget_amount)` |
   | Spend this period | `max(metadata.budget_spend)` |
   | % used | `max(metadata.budget_percent_used)` |
   | Refused | count of events where `metadata.budget_refused = true` |

   When the Budget filter selects a budget with "each" owners, rows are additionally grouped by `metadata.budget_owner_values` — one row per value or combination (e.g. per user, or per user and provider).
2. **Budget filter** — next to the existing API filter; filters the page's events on `metadata.budget_id` (labelled by `metadata.budget_name`). Deep links from the AI Workspace set it (path and query format: open, plan risk 12).
3. **Alert rule "BUDGET: Threshold crossed"** — **one per organization**:
   - Event filter: `metadata.budget_threshold_crossed` exists.
   - Group by `metadata.budget_name`, `metadata.budget_threshold_crossed`, `metadata.budget_owner_values`; count > 0 over 15 minutes.
   - Channels: Slack / email / PagerDuty / webhook. The alert payload carries `budget_name`, `budget_owners`, `budget_owner_values`, `budget_period`, `budget_amount`, `budget_spend`, `budget_percent_used`, `budget_threshold_crossed`.
   - Optional: extra rules with the same condition filtered to specific budgets, only to route them to different recipients.

Re-arming after an amount change is automatic (marker key includes `amountVersion`). Unpriced traffic for a budget is visible by setting the Budget filter and filtering on `metadata.llm_cost_status = not_calculated`; no separate dashboards or per-budget views are provided.

### 4.3 Budget changes

*(Removed 2026-10-09.)* No `Budget Changed` Moesif action. Amount, mode and status changes — including the automatic disable when an owner is deleted — are recorded in the platform-api audit log with before/after details (FR-009a, FR-026; data-model §3).
