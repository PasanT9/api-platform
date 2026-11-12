# Specification Quality Checklist: Budgets

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-10-08
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

- HTTP 429 / `Retry-After` / response headers are kept deliberately: for an API gateway they are the user-facing contract, not implementation detail.
- The counter technology is described by capability ("shared low-latency counter", "durably persisted by the platform") rather than by product; the default-mode-plus-optional-strict-mode decision was made by the feature owner on 2026-10-08.
- Resolved 2026-10-08: notification channel → Moesif (US3, FR-023, FR-025).
- Resolved 2026-10-08: "dynamic budgets for apps" → out of scope; User Story 6 removed.
- All items pass; ready for `/speckit-plan`.
- Review round 1 (2026-10-08): Budgets-page meter from platform ledger (FR-024), threshold re-evaluation on amount change + Budget Changed event (FR-009/009a), overshoot and mode-combination edge cases, unpriced-model future options.
- Added FR-016a/b after code review: OpenAI/Azure streamed chat completions report no usage unless requested, which would let callers bypass budgets.
- Review round 2 (2026-10-08): description added; scope display names; no spend history, budget-change history or summary tiles in the API Platform UI; per-value budgets kept but show definition only (per-value spend in Moesif); unpriced counter kept (FR-024a).
- Review round 3 (2026-10-08): per-user / per-app spend IS shown for the current period (list summary + per-value table on the budget page); reverses round 2.
- UI alignment pass (2026-10-08): currentPeriod.state (FR-024b), Moesif links (FR-024c), read-only UI for ap:budget:read, index for per-value sort.
- Review round 4 (2026-10-08): "separate for each" allowed for every scope type except organization (FR-003), enabling a constant create dialog.
- Review round 5 (2026-10-08): LLM provider and LLM proxy split into separate scope types (FR-002).
- Review round 6 (2026-10-08): refused-request counts removed from ledger, sync, API and UI (FR-024d); refusals live in Moesif.
- UI consistency review (2026-10-08): 22 findings applied — status labels incl. Disabled/Enable, near limit = 80–99 %, asOf definition, displayName in sync payload, warnings field dropped, edit-locking rules, explicit phase FR lists, LLM provider/proxy wording, handles in API, sortOrder, owner-deleted edge case.
- Review round 7 (2026-10-08): request counts removed from ledger, sync, API and UI (FR-024d); only spend and unpriced count tracked.
- Analysis remediation 2026-10-09 (approved by the feature owner): U1 audit before/after stored in a new nullable `audit.details` column with guarded per-dialect `ALTER` (FR-026, data-model §8); C1 strict-mode fallback configurable per gateway (FR-020, edge case); A1 controller usage ingest requires mandatory internal credentials plus an in-handler authorization check, IP allow-list optional defence-in-depth only; A2 policy-engine phase-ordering test (T134); U2 threshold crossings recorded at detection (`notified_thresholds`) independently of Moesif publishing (`published_thresholds`); E1 `Budget Changed` for every status change incl. owner-deleted auto-disable (T139); E2 budget + `llm-cost-based-ratelimit` coexistence IT and quickstart Scenario 12 (FR-029, T137); E3 SC-006 reconciliation and SC-007 timing in quickstart Scenario 13 (T140); U3 `status_reason` / read-only `statusReason` with UI explanation (T138); R1 `organization_uuid` FK on `budget_period_spend` and `budget_sync_cursors`; R2 client-facing headers renamed from `x-wso2-budget-*` to neutral `Budget-Id` / `Budget-Remaining` / `Budget-Reset` / `Budget-Monitor` (error-handling rule directive 2); R3 read-only mode enforced in service/repository layer (T136); T1 "research Rn" vs "DB rule Rn" disambiguated in plan.md and data-model.md; X1 T128 marked optional pending plan risk 9; U4 `include_usage` injected when any budget (enforce or monitor) applies (FR-016a); I1 plan delivery-phase FR lists aligned with tasks.md and stale tasks.md note removed; I2 `OnStreamError` tracked as a GitHub issue, no `TODO` comment in code (T011); D1 duplicate "Multiple gateways" edge case removed (FR-019 covers it). New tasks T132–T140.
- Architecture change 2026-10-09 (feature owner): spend is counted only in a shared Redis that is required for budgets (every gateway atomically `INCRBY`s integer nano-USD counters per budget × period × value, TTL period end + 1 day; overshoot = in-flight requests only; refuse to activate without Redis; warn on eviction / no persistence; Redis down → refuse by default, per-gateway fail-open; data loss → period spend restarts at zero); no gateway → control-plane spend sync (removed `budget.usage` / `budget.totals`, controller usage ingest, replica source id, sync intervals, disconnect alerts, `budget_period_spend`, `budget_sync_cursors`, purge job, `currentPeriod` / `perValueSummary` / `asOf` / `state` filter / `GET /budgets/{budgetId}/values`); AI Workspace Budgets page is configuration only with "See usage in Insights / Moesif" links (US5 reworded); threshold crossings detected by gateways with a Redis `SET NX` marker keyed by amount version and carried on the request's analytics event (`budget_threshold_crossed`) to one org-wide Moesif alert rule; platform-api publishes no Moesif actions (budget changes audit-only, FR-009a). Requirement ids: reworded FR-009, FR-009a, FR-014, FR-015, FR-016b, FR-017–FR-022, FR-023, FR-024, FR-024a, FR-024d, FR-025, SC-003, SC-004, SC-006, SC-007, SC-008; removed FR-024b; new FR-022a (data loss), FR-022b (Redis auth/TLS). The spec now names Redis explicitly because it is an operator-visible deployment requirement (supersedes the earlier "described by capability" note). Open for architecture leads: single `budgets` definitions table vs "avoid new tables"; built-in system policy vs auto-attached policy; one shared Redis per organization for multi-region / hybrid deployments. Closed: platform-api access to Moesif (no longer needed). tasks.md regenerated (T001–T113).
- Re-analysis remediation 2026-10-13 (approved by the feature owner): G1 new FR-024e — one Budget Usage card and a Budget filter on the existing LLM Analytics page of the embedded Moesif analytics (per-value rows when filtered), plan "Analytics (Moesif templates)" section, tasks T116 (org-wide alert rule "BUDGET: Threshold crossed"), T117 (card), T118 (filter), T097 now links to LLM Analytics with the Budget filter; G2 cost field is `metadata.aiMetadata.llmCost` (flat `llm_cost` removed everywhere) and budget fields are published under the Moesif event `metadata.*` — verified in `policy-engine/internal/analytics` that the publisher forwards only an allow-list, so a publisher change is required (T114); C1 contract §4.2 and T104 reduced to one card + Budget filter + one org-wide alert rule (optional routing rules), budget-vs-actual dashboards / per-budget views and backstop rule removed, plan risk 5 reworded; F1 FR-025 and research R9 list `budget_name`, `budget_scope`, `budget_scope_value`; I1 `llm_cost_status`, `budget_check_skipped`, `budget_id`, `budget_name` emission moved to delivery phase 1 (T115); U1 AI Workspace runtime config `APIP_AIW_MOESIF_ANALYTICS_URL` replaces the hard-coded Insights URL (T119), LLM Analytics path and Budget filter query format open with the analytics team (plan risk 12); O1 "monitor-only allowed when Redis is down" moved from T053/T058 to T091/T092; L1 `contracts/gateway-sync.md` renamed `contracts/gateway-definitions.md` and references updated; L2 spec Input line replaced with the current design summary; S1 quickstart Scenario 14 (SC-001, under 2 minutes) and Scenario 15 (FR-024e), T112 runs 0–15 and records SC-001; A1 `ap_viewer` read access left open as a product decision (plan risk 10). New tasks T114–T119.
- 2026-10-13: FR-024 status split into Mode (Enforce/Monitor) and Status (Active/Disabled) to match the list mockup; create-dialog mockups re-rendered over the configuration-only list.
- Ownership decision 2026-10-14 (feature owner): a budget's first owner is its AI Workspace context (organization in org mode, project in project mode); any number of owners can be appended (project in org mode only, LLM provider, LLM proxy, application, token claim per claim name), each a specific value or "each", all ANDed (FR-002, FR-003, research R13). Replaces the six scope types and shared vs separate-for-each (FR-002/003 of review rounds 4–5). Resolves "split by". Open: owner cap, override/precedence (most specific wins), model dimension. Updated spec, plan, research, data-model (owners JSON column + owners_hash), contracts, quickstart (Scenario 3a), tasks (T120).
- Override and model decisions 2026-10-14 (feature owner): the most specific budget wins (FR-003a — same context, period and dimension set; the overridden budget is skipped, not charged; different dimension sets, contexts or periods stack; API returns `overrides` / `overriddenBy` and warns on create/update), and model is an owner dimension (upstream request model id after proxy rewrite, exact match; gateway prerequisite T013a). The owner cap remains open.
- Multi-value owners decision 2026-10-14 (feature owner): an appended owner holds a `values` list (one or more, any of which matches; listed values share one allowance, e.g. LLM provider ∈ {OpenAI, Anthropic} at $2,000/month) or `each` (FR-003); for most specific wins a values list is narrower than `each`, and a subset list is at least as narrow (FR-003a); deleting any listed resource disables the budget (dropping the value instead is open). Precedent: Portkey condition arrays.
