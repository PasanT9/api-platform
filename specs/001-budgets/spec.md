# Feature Specification: Budgets

**Feature Branch**: `feature/budgets`

**Created**: 2026-10-08

**Revised**: 2026-10-09 — architecture change decided by the feature owner: spend is counted only in a shared Redis that is required for budgets; the AI Workspace shows budget configuration only (no spend); no spend sync to the control plane; threshold warnings are detected by gateways and delivered through Moesif. 2026-10-13 — Moesif presentation decided: one Budget Usage card and a Budget filter on the existing LLM Analytics page (FR-024e). 2026-10-14 — budget ownership decided by the feature owner: a budget's first owner is the context it is created in (the organization in org mode, a project in project mode), and any number of further owners (project, LLM provider, LLM proxy, application, token claim) can be appended; all owners must match, and each appended owner is a specific value or "each" (FR-002, FR-003). This replaces the six owner scope types and the shared vs separate-for-each choice. Later on 2026-10-14 the feature owner added **model** as an owner dimension and decided that the **most specific budget wins** when a specific-value budget and a broader "each" budget with the same context, period and dimensions both match (FR-002, FR-003a). Also on 2026-10-14 the feature owner decided that an appended owner can list **several values**, any of which matches, sharing one allowance (e.g. LLM provider ∈ {OpenAI, Anthropic}, $2,000 a month; FR-003). See [checklists/requirements.md](./checklists/requirements.md).

**Status**: Draft

**Input**: Add Budgets to the API Platform AI Gateway. A budget is: this much money · for these owners · over this calendar period · warn at this point. A budget's first owner is the organization or project it is created in; further owners — a project, an LLM provider, an LLM proxy, a model, an application, or any identity claim (team, department, user, cost centre, …) — can be appended, each as one or more specific values (any of which matches, sharing one allowance) or "each" (a separate allowance per value), and a request counts only when it matches every owner; when a specific-value budget and a broader "each" budget with the same context, period and dimensions both match, the most specific one governs. Budgets are enforced in the request path of every AI route, independent of provider rate-limit configuration, and align to UTC calendar periods so figures reconcile with provider invoices. Spend is counted only in a shared Redis that is required wherever budgets are enforced; the AI Workspace shows budget configuration only; usage, threshold warnings and alerts live in the embedded Moesif analytics (a Budget Usage card and Budget filter on the existing LLM Analytics page, and one organization-wide threshold alert rule). Based on design-doc-v1, the AI gateway budgeting market analysis and the feature owner's decisions of 2026-10-08/09/13.

## Background

Today the only spend control is a cost limit inside an LLM provider's rate-limit configuration. It is per provider (no pot spanning providers), per consumer application or shared org-wide (no team or cost-centre pots), uses a rolling 720-hour window (does not reconcile with monthly invoices), requires redeploying the provider to change, has no owner, and sends no warnings. Market research shows that dedicated budgets (fixed money pots over calendar periods, stacked across scopes, with threshold alerts) are now a standard capability of AI gateways, and that WSO2 messaging already promises spend capping and chargeback.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Cap an organization, project, LLM provider or LLM proxy's monthly AI spend (Priority: P1)

A platform administrator creates a monthly budget of a fixed amount for the whole organization, a project, or a single LLM provider/proxy. AI requests that fall under that budget are allowed while spend is below the amount and refused once it is exhausted, until the next calendar month begins.

**Why this priority**: This is the minimum that turns the existing per-request cost calculation into a real spending ceiling, closes the gap with the product's "cap spend" positioning, and needs no identity setup by the customer.

**Independent Test**: Create a $10 monthly budget on one LLM proxy, send priced requests until spend passes $10, verify further requests are refused with a budget-specific error and a retry time equal to the start of next month (UTC), and that a request through a different proxy in the same org is unaffected.

**Acceptance Scenarios**:

1. **Given** a monthly budget of $1,000 on a project with $990 spent this month, **When** a request costing $5 is sent, **Then** it is allowed and recorded spend becomes $995.
2. **Given** the same budget with recorded spend ≥ $1,000, **When** any request in that project is sent, **Then** it is refused with a "budget exhausted" error (distinct from rate-limit errors) that identifies the budget and its period, and with a retry time equal to the start of the next calendar month in UTC.
3. **Given** an exhausted monthly budget, **When** the calendar month changes, **Then** requests are allowed again without any administrator action.
4. **Given** a team that wants both a daily and a monthly ceiling, **When** the administrator creates two budgets for the same owner with different periods, **Then** both are enforced and whichever is exhausted first refuses requests.
5. **Given** a gateway on which budgets are switched on but no shared counter (Redis) is configured, **When** the gateway starts, **Then** it refuses to activate budget enforcement with a clear error naming the missing configuration (budgets never "accept a ceiling and enforce nothing" silently); **and given** the shared counter is configured, a budget created by an administrator is enforced.
6. **Given** an organization budget of $2,000/month with owner LLM provider ∈ {OpenAI, Anthropic}, **When** requests go to OpenAI, Anthropic and Gemini, **Then** OpenAI and Anthropic spend accumulate in one shared $2,000 allowance and Gemini spend is not counted toward it.

---

### User Story 2 - Budgets for teams, departments and users via identity claims, combined with other owners (Priority: P1)

An administrator appends a token-claim owner to a budget, keyed on a claim carried by the caller's verified token (e.g. `team = search`, `department = engineering`, `sub = alice@acme.com`, or a customer-defined `cost_centre`). The claim owner is either a specific value (one pooled allowance for that value) or "each" (every distinct value of the claim gets its own allowance, e.g. "every user gets $1,500 a month"). Claim owners can be combined with other owners, e.g. "each user gets $10 a month on OpenAI in project-a", or "alice gets $x on OpenAI and $y on Anthropic".

**Why this priority**: Per-team and per-user ceilings are the most requested use case (chargeback, developer caps, per-employee agent caps) and the platform has no team or user entity, so claims are the only way to express them without new identity infrastructure.

**Independent Test**: With token authentication on a route, create an organization budget of $5/day with owner `sub = each`; verify two different users each get their own $5 and are refused independently. Then create a project budget with owners `sub = each` and provider `= each` and verify one user's spend on one provider does not reduce their allowance on another.

**Acceptance Scenarios**:

1. **Given** a budget with owner `team = search`, **When** two users from team `search` send requests, **Then** their spend accumulates against the same budget.
2. **Given** a budget of $1,500/month with owner `sub = each`, **When** a user who has never called before sends a request, **Then** they get their own $1,500 allowance without any administrator action.
3. **Given** a customer who starts issuing a new claim (e.g. `cost_centre`), **When** an administrator creates a budget on that claim, **Then** it works the same day without any product change or release.
4. **Given** a request whose credentials carry no claims (e.g. API-key authentication), **When** it is evaluated, **Then** budgets with a claim owner do not apply to it, but budgets whose owners are only the organization, project, LLM provider, LLM proxy or application still do.
5. **Given** two organization budgets, owners `sub = alice` + provider `= OpenAI` at $50 and `sub = alice` + provider `= Anthropic` at $20, **When** alice spends $50 on OpenAI, **Then** her OpenAI requests are refused and her Anthropic requests are still allowed.
6. **Given** a project budget with owners `sub = each` and provider `= each` at $10/month, **When** a user calls two providers, **Then** each user–provider combination has its own $10 and only combinations that received traffic have counters.
7. **Given** two project-a budgets, `sub = each` at $10/month and `sub = alice` at $50/month, **When** alice has spent $30 and bob $10, **Then** alice's requests are still allowed (only the $50 budget governs her, and her spend is not charged to the $10 budget) while bob's are refused.
8. **Given** an organization budget with owners `sub = each` + model `= gpt-4o` at $5/month, **When** a user has spent $5 on `gpt-4o`, **Then** their `gpt-4o` requests are refused while their requests to other models are still allowed.
9. **Given** an organization budget with owners `team = search` + LLM provider ∈ {OpenAI, Anthropic} at $1,500/month, **When** members of team `search` call OpenAI and Anthropic, **Then** both count toward the one $1,500 allowance, while their calls to other providers and other teams' calls do not.

---

### User Story 3 - Warnings before the money runs out (Priority: P1)

An administrator sets one or more warning thresholds on a budget (e.g. 50%, 80%, 100%). When recorded spend crosses each threshold, a notification is delivered once per threshold per period, and callers can see remaining budget on responses.

**Why this priority**: A hard stop with no warning causes production outages; every surveyed competitor alerts before blocking.

**Independent Test**: Create a $10 budget with 50% and 80% thresholds, push spend past $8 through two gateways, and verify exactly two threshold-crossing events reached the analytics platform in that period.

**Acceptance Scenarios**:

1. **Given** a budget with thresholds 50% and 80%, **When** spend crosses 50% and later 80%, **Then** one notification is delivered for each, and no duplicates are delivered for the rest of the period, however many gateways or replicas served the traffic.
2. **Given** a $5,000 budget whose 80% warning has already fired, **When** the administrator raises the amount to $10,000, **Then** the warning state is cleared and a new warning is delivered when spend reaches $8,000.
3. **Given** notifications are delivered through the analytics platform (Moesif: the Budget Usage card on the LLM Analytics page and one alert rule) — the API Platform itself sends no email/Slack — **When** a threshold is crossed, **Then** the analytics event of the request that crossed it carries the budget, owner, period, amount, current spend, percentage used and the threshold crossed, so a single organization-wide alert rule can notify the right people.

---

### User Story 4 - Roll out safely with monitor mode (Priority: P2)

An administrator creates a budget in monitor mode. Spend is tracked and warnings fire, but requests are never refused. Later the administrator switches the budget to enforce mode.

**Why this priority**: Customers need to size budgets from real traffic before blocking production; several competitors offer an equivalent audit/alert-only mode.

**Independent Test**: Create a monitor-mode budget of $1, exceed it, and verify requests keep succeeding while the responses and analytics events show that the budget would have refused them.

**Acceptance Scenarios**:

1. **Given** a monitor-mode budget that is exhausted, **When** a request is sent, **Then** it is allowed, and the response and analytics indicate the budget would have refused it.
2. **Given** a monitor-mode budget with spend recorded this period, **When** it is switched to enforce mode, **Then** the already recorded spend counts and enforcement starts immediately.

---

### User Story 5 - See budgets and find their usage in analytics (Priority: P2)

*(Reworded 2026-10-09: the AI Workspace no longer shows spend.)* A finance user or operator views the organization's budgets — what is capped, for whom, by how much, over which period, in which mode, with which thresholds and whether each budget is active — and follows a link from each budget to the analytics platform (Insights / Moesif) to see its usage. Spend, remaining amount and per-user or per-value figures are only available in analytics.

**Why this priority**: People need one place to see which ceilings exist and who owns them; usage analysis already has a home in the analytics platform, and showing spend in the AI Workspace would require copying live spend from the data plane to the control plane.

**Independent Test**: Create budgets with specific-value and "each" owners, in org mode and in project mode; verify a read-only user sees their configuration and status on the Budgets page, sees no spend figures, and that each budget's "See usage" link opens the analytics platform filtered to that budget.

**Acceptance Scenarios**:

1. **Given** several budgets, **When** a user with read permission opens the Budgets page, **Then** they see each budget's name, description, owner (by display name), amount, period, mode, thresholds and status (Active, Monitor or Disabled, with the reason when the platform disabled it), and no spend, remaining amount, percentage or near-limit/exhausted state.
2. **Given** a budget, **When** the user follows its "See usage in Insights / Moesif" link, **Then** the organization's analytics platform opens on the LLM Analytics page with the Budget filter set to that budget where supported, and its Budget Usage card shows the budget's spend, percentage used and refusals (FR-024e).
3. **Given** a budget with an "each" owner (e.g. every user gets $25 a day), **When** the user views it, **Then** its owners are summarised (e.g. "Project-a · each user · OpenAI") and the amount is shown as applying to each value ("each user gets $25 / day"), with a link to analytics for per-value usage; no per-value table is shown.
5. **Given** the AI Workspace in project mode, **When** a user opens Budgets, **Then** they see the budgets created in that project; in org mode they see all budgets of the organization, with each budget's context (organization or project).
4. **Given** a user with read permission only, **When** they open the Budgets page, **Then** create, edit, disable and delete actions are not offered.

---

### Edge Cases

- **Several budgets apply to one request**: first, overrides are resolved (FR-003a): a matching budget that is overridden by a more specific matching budget is skipped for that request. Every remaining budget whose owners all match is checked and charged (budgets stack; the tightest blocks); the first one without headroom refuses. Order of checking: budgets with more owners first, then smallest amount, then budget id. The refusal names that budget.
- **Most specific wins**: `sub = alice` at $100 and `sub = each` at $20 in the same context and period → alice is governed only by the $100 budget, and her spend does not accrue in the $20 budget. Budgets with different dimensions, contexts or periods still stack: an org-mode `sub = each` budget still applies to alice even if project-a has a `sub = alice` budget. Partial specificity is not an override: `sub = alice` + provider = each and `sub = each` + provider = OpenAI both apply to alice on OpenAI. A monitor-mode budget can override an enforce-mode one, which lifts enforcement for that value; the API returns a warning when a budget is created that overrides another (the AI Workspace does not show overrides).
- **Several values on one owner**: provider ∈ {OpenAI, Anthropic} matches requests to either provider; both add to the same counter, so the pair shares one allowance. Requests to an unlisted provider do not count toward it.
- **Model owner**: the model value is the upstream model id the gateway sends to the provider (after any proxy model rewrite), matched exactly (case-sensitive); a request whose model cannot be determined does not count toward budgets with a model owner.
- **Request lacks a value for an owner** (e.g. no `team` claim, or traffic not through any LLM proxy for a budget with a proxy owner): the budget does not apply to that request.
- **Several budgets exhausted at once**: the retry time is the latest reset among the exhausted budgets (the earliest moment the request could succeed).
- **Request in flight across a period boundary**: the charge belongs to the period in which the request finished.
- **Streaming responses**: the check gates entry only; a stream already in progress is never cut off because the budget ran out mid-stream. Streams that would carry no usage (e.g. OpenAI/Azure chat completions without usage reporting requested) must not become a free path around budgets.
- **Concurrent requests / overshoot**: cost is known only after a response, so the request that crosses the line and any requests already in flight on any gateway complete and are charged in full; all new requests on every gateway are refused as soon as the shared counter shows the budget exhausted. Overshoot is limited to requests already in flight (FR-017, SC-003).
- **Mode combinations**: track only = monitor mode without thresholds; track + warn = monitor + thresholds; track + block = enforce without thresholds; track + warn + block = enforce + thresholds (default suggested in the UI).
- **Cost cannot be determined** (unknown model/pricing): the request is charged zero, never blocked for that reason, and marked unpriced in its analytics event.
- **Budget amount lowered below current spend**: takes effect immediately; requests are refused until the period resets. Recorded spend is never altered by an amount change.
- **One request crosses several thresholds at once** (or an amount change leaves spend above several thresholds): only the highest crossed threshold is notified (no burst of warnings).
- **Budget created mid-period**: starts at zero for the current period (not backdated).
- **Budget disabled then re-enabled within a period**: spend recorded while enabled is retained; spend while disabled is not counted.
- **Budget deleted**: enforcement stops immediately; its counters expire on their own after the period ends; past spend remains available in the analytics platform.
- **Gateway loses contact with the control plane**: it keeps enforcing with its last known budget definitions; spend counting and enforcement are unaffected because they use only the shared counter. Budget changes made meanwhile reach the gateway when contact resumes.
- **Shared counter (Redis) unreachable**: requests subject to an enforce-mode budget are refused with a distinct "budget check unavailable" error by default; an administrator can configure a gateway to allow them instead (fail-open), in which case their spend is not counted. Requests subject only to monitor-mode budgets are allowed.
- **Shared counter loses its data** (e.g. Redis restarted without persistence, failover, eviction): the current period's spend for the affected budgets restarts at zero. This is documented; persistence and a no-eviction policy mitigate it, and the gateway warns when Redis is configured in a way that can lose data.
- **Owner deleted** (a project, LLM provider/proxy or application referenced by any of a budget's owners, including the project a project-mode budget belongs to): the budget is set to Disabled with a recorded reason, since it can no longer match traffic as defined — also when the deleted resource is one of several listed values (whether to drop that value and keep the budget active instead is an open question); the change is recorded in the audit log with before/after values (FR-009a), and the UI explains why so the admin can delete it.
- **Gateway restarts mid-period**: no recorded spend is lost (spend lives in the shared counter, not in the gateway).
- **Gateways of one organization in different regions or sites**: all gateways enforcing an organization's budgets must use the same shared counter; deployments that cannot share one (multi-region, hybrid) are an open question (plan risk 11).
- **Caller relies on automatic retries**: a budget refusal must be distinguishable from a rate limit so clients stop retrying.

## Requirements *(mandatory)*

### Functional Requirements

**Budget definition**

- **FR-001**: Administrators MUST be able to create a budget with: display name, optional description, its owners (FR-002), monetary amount (USD), calendar period, enforcement mode, and up to five warning thresholds (1–100 %).
- **FR-002**: *(Rewritten 2026-10-14, feature owner.)* A budget's **first owner** MUST be the context it is created in: the organization when created in the AI Workspace's org mode, or the project when created in project mode. Administrators MUST be able to append any number of further owners (no limit in v1; a cap, e.g. 3, may be added later). An appended owner's dimension MUST be one of: project (org mode only), LLM provider, LLM proxy, application, or model, or token claim (by claim name, e.g. `sub`, `team`). Each dimension MAY appear once per budget; token claim MAY appear once per claim name. LLM provider and LLM proxy are separate dimensions (2026-10-08): a provider owner matches all traffic to that provider, including traffic that arrives through proxies; a proxy owner matches traffic through that proxy only. *(Added 2026-10-14.)* A model owner matches the upstream model id the gateway sends to the provider (after any proxy model rewrite) — the same id used for pricing — exactly and case-sensitively; it requires the gateway to expose that id to the budget policy before the upstream call and after the response. Other artifact kinds (e.g. MCP proxies) can be added as dimensions once their traffic is priced.
- **FR-003**: *(Rewritten 2026-10-14.)* All of a budget's owners MUST match for a request to count toward it (logical AND). Each appended owner MUST be either a **list of one or more specific values** (e.g. provider = OpenAI; `team` = `search`; provider ∈ {OpenAI, Anthropic}) — a request matches when its value is any of the listed values (logical OR within the owner), and all listed values share one allowance — or **each** (every distinct value receives its own allowance of the same amount, including values that appear later). A budget with several "each" owners has one allowance per combination of values (e.g. project-a + `sub` = each + provider = each is $10 per user per provider in project-a). The first owner is always a specific value. Lists MUST be non-empty with no duplicate values; there is no limit on the number of values in v1 (a cap may be added with the owner cap). *(Multi-value owners added 2026-10-14, feature owner.)* This replaces the earlier owner scope types and the shared vs separate-for-each choice.
- **FR-003a**: *(Added 2026-10-14, feature owner: most specific wins.)* Budget B MUST override budget A for a request when both match it, they have the same context (the organization, or the same project), the same period and the same set of owner dimensions (token claims compared by claim name), and on every dimension B is at least as narrow as A, with B strictly narrower on at least one dimension. Per dimension: a values list is narrower than "each"; two "each" owners are equal; a values list is at least as narrow as another when it is a subset of it, and strictly narrower when it is a proper subset (e.g. provider ∈ {OpenAI} at $500 overrides provider ∈ {OpenAI, Anthropic} at $2,000 for OpenAI traffic). An overridden budget MUST be skipped for that request: not checked, not charged, and no thresholds evaluated. Budgets with different dimension sets, contexts or periods MUST still stack; partial specificity (more specific on one dimension, broader on another) is not an override. Enforcement mode MUST NOT affect precedence. Overrides MUST be determined from budget definitions only (no spend). The API MUST return, for each budget, the budgets it overrides and the budgets that override it, and creating or editing a budget that overrides another MUST produce a warning.
- **FR-004**: Claim names MUST be data, not product-defined; any claim present in the caller's verified token can be used without a product change.
- **FR-005**: Periods MUST be calendar-aligned in UTC: day (resets 00:00 UTC), week (ISO week, resets Monday 00:00 UTC), month (resets 00:00 UTC on the 1st).
- **FR-006**: The same owners MAY have several budgets with different periods; the combination of first owner, owner list (dimension, claim name, set of values or each — the same values in any order are the same owner) and period MUST be unique per organization.
- **FR-007**: Enforcement mode MUST be either *enforce* (refuse when exhausted) or *monitor* (track and warn only).
- **FR-008**: Display name, description, amount, thresholds, enabled state and enforcement mode MUST be editable; the first owner (context), the owner list (dimensions, claim names, values or each) and the period MUST NOT be editable (changing them requires a new budget), and the edit form shows them read-only. A disabled budget MUST be shown as Disabled and can be enabled again.
- **FR-009**: Changing the amount MUST NOT change recorded spend. Threshold-notification state for the current period MUST be re-armed by an amount change: every threshold is evaluated afresh against the new amount, and if spend is already above one or more thresholds after the change, only the highest such threshold is notified (no burst of warnings).
- **FR-009a**: *(Reworded 2026-10-09: audit only; the platform publishes no analytics event for budget changes.)* Every change to a budget's amount, mode or status (including automatic disable when its owner is deleted) MUST be recorded in the audit log with the old and new values, the actor and the time (see FR-026).
- **FR-010**: Budgets MUST be configured independently of LLM provider/proxy definitions; creating or changing a budget MUST NOT require redeploying any artifact, and MUST take effect on all gateways without restarting them.

**Enforcement**

- **FR-011**: Every AI route MUST evaluate all enabled budgets applicable to a request before forwarding it, and charge the request's calculated cost to all of them after the response completes.
- **FR-012**: Budget evaluation MUST use the caller identity established by whatever authentication the route already uses; the feature MUST NOT introduce new authentication.
- **FR-013**: When an enforce-mode budget has no headroom, the request MUST be refused with HTTP 429, an error code distinct from rate-limit errors, an identification of the refusing budget (name, owners, period, reset time), and a `Retry-After` value equal to the time until that budget's period resets (latest reset if several are exhausted).
- **FR-014**: Allowed responses MUST expose the most constraining applicable budget's remaining amount (as read from the shared counter at admission) and reset time to the caller.
- **FR-015**: *(Reworded 2026-10-09.)* A request whose cost cannot be determined MUST be charged zero, MUST NOT be refused for that reason, and MUST be marked as unpriced in its analytics event (FR-024a).
- **FR-016**: Streaming responses MUST NOT be terminated because a budget is exhausted mid-stream; their full cost is charged on completion.
- **FR-016a**: Callers MUST NOT be able to avoid budget charges by choosing a request form whose response omits usage information (e.g. a streamed chat completion that does not request usage reporting); for requests subject to any budget (enforce or monitor mode, so monitor budgets measure streamed spend accurately), the gateway MUST ensure usage is reported by the provider where the provider supports it.
- **FR-016b**: A streamed response that ends abnormally SHOULD be charged for the usage reported up to that point, if any, and otherwise marked unpriced. (Depends on the gateway exposing stream-termination events to policies; see plan.)
- **FR-016c**: Each external request MUST be charged exactly once, even when it passes through more than one gateway hop (e.g. an LLM proxy forwarding to an LLM provider on the same gateway); every budget applicable to any hop (proxy and provider) MUST still be evaluated and charged.

**Counting, durability and deployment** *(rewritten 2026-10-09: shared Redis required; no spend in the control plane)*

- **FR-017**: Budget spend MUST be counted only in a shared counter store (Redis) that is required wherever budgets are enforced. Every gateway (all replicas and all gateways of the organization) MUST atomically add each request's cost to the counter of every applicable budget for the current period (and, for budgets with "each" owners, the caller's combination of values; counters exist only for combinations that received traffic), and MUST check that counter before admitting a request, so overshoot is limited to requests already in flight. Spend MUST survive gateway and control-plane restarts.
- **FR-018**: Counters MUST be kept in whole nano-USD units and MUST expire on their own one day after their period ends; the platform keeps no spend history (history is in the analytics platform).
- **FR-019**: A single budget MUST represent one amount shared across all gateways serving the organization; all gateways enforcing an organization's budgets MUST therefore use the same shared counter.
- **FR-020**: When the shared counter is unreachable, requests subject to enforce-mode budgets MUST be refused by default with an error distinct from both budget exhaustion and rate limiting; administrators MUST be able to configure, per gateway, to allow such requests instead (fail-open), in which case their spend is not counted. Requests subject only to monitor-mode budgets MUST be allowed.
- **FR-021**: *(Reworded 2026-10-09.)* When a gateway cannot reach the control plane, it MUST keep enforcing with its last known budget definitions; counting and enforcement MUST NOT depend on the control plane. Definition changes MUST be applied when contact resumes (full resync).
- **FR-022**: Budgets MUST NOT be activated on a gateway that has no shared counter configured: the gateway MUST refuse to activate them with a clear error. The gateway SHOULD warn when the counter's configuration can lose data (an eviction policy other than no-eviction, or no persistence).
- **FR-022a**: If the shared counter loses its data, the current period's spend of the affected budgets MUST restart at zero (not fail requests); this behaviour MUST be documented together with the recommended mitigation (persistence, no-eviction).
- **FR-022b**: The shared-counter connection MUST support authentication (username/password) and TLS including mutual TLS, using the same configuration shape as the gateway's advanced rate-limit Redis settings.

**Warnings and visibility**

- **FR-023**: *(Reworded 2026-10-09.)* Each threshold crossing MUST be detected by the gateway that charged the request that crossed it and MUST be emitted exactly once per budget, period, value combination (for budgets with "each" owners), threshold and amount version across all gateways, as part of that request's analytics event in the analytics platform (Moesif), which is responsible for delivering warnings and other notifications (email, Slack, webhooks) through an organization-wide alert rule. The API Platform MUST NOT send notifications itself in v1, and the control plane MUST NOT publish threshold or budget-change events.
- **FR-024**: *(Reworded 2026-10-09.)* Users with read permission MUST be able to view, on a dedicated Budgets page, each budget's configuration: name, description, context (organization or project), owners (by display name, e.g. "Project-a · each user · OpenAI"), amount, period, enforcement mode (shown as its own Mode column: Enforce or Monitor), thresholds and status (Active or Disabled, with the platform's reason when it disabled the budget). The Budgets page MUST NOT show spend, remaining amount, percentage used, near-limit/exhausted state, per-value spend or unpriced counts; each budget MUST link to its usage in the analytics platform (Insights / Moesif).
- **FR-024d**: *(Reworded 2026-10-09.)* The control plane MUST NOT receive, store or sync spend, request counts, refused-request counts or unpriced counts; current-period spend exists only in the shared counter. Request volumes, refusals, spend over time and per-value spend are visible in the analytics platform (each request, including each refused one, is an analytics event).
- **FR-024b**: *(Removed 2026-10-09: the platform no longer reports spend, so it has no near-limit / exhausted state; status labels are defined in FR-024.)*
- **FR-024c**: Links from budget views to the analytics platform MUST use the organization's embedded analytics (Moesif) address configured for the AI Workspace (not a hard-coded URL) and, where supported, open the LLM Analytics page with the Budget filter (FR-024e) set to the budget.
- **FR-024e**: *(Added 2026-10-13, feature owner.)* The embedded analytics (Moesif) MUST present budget usage as one **Budget Usage** card on the existing **LLM Analytics** page — no new page, section or navigation item — plus a **Budget** filter next to the existing API filter. The card MUST list each budget with its name, amount, spend this period, percentage used and refused-request count; when the Budget filter selects a budget with "each" owners, the card MUST list one row per value or combination of values (e.g. per user, or per user and provider). The rest of the LLM Analytics page is unchanged.
- **FR-024a**: *(Reworded 2026-10-09.)* Requests that could not be priced MUST be identifiable in the analytics platform: each AI request event MUST carry its cost status (priced / not priced) together with the budgets it was evaluated against, so a budget over unpriced traffic (e.g. a self-hosted model with no price) is never silently ineffective. The platform keeps no unpriced counter.
- **FR-025**: Analytics events for AI requests MUST carry, for each budget evaluated, its identifier, name, owners and the resolved owner values (`budget_owner_values`: the caller's own values for "each" owners), amount, period, spend after the request (from the shared counter) and percentage used, whether it refused the request (or, in monitor mode, would have), the budgets skipped because a more specific budget overrode them (`budget_overridden_ids`), and — when this request crossed a threshold — the threshold crossed, so spend can be analysed and alerted on per budget in the analytics platform.
- **FR-026**: Budget create, update, enable/disable (including automatic disable when the owner is deleted) and delete actions MUST be audit-logged with the actor, and the before/after values MUST be stored in the audit record's details.

**Access control**

- **FR-027**: Only users granted budget-management permission (by default the organization administrator role) MUST be able to create, change or delete budgets in org mode and in any project; operators MUST be able to read all budgets in the organization; other roles MUST NOT see or change budgets unless granted.
- **FR-028**: Being associated with any of a budget's owners (e.g. a project's members, a team) MUST NOT grant permission to change that budget.

**Compatibility**

- **FR-029**: Existing cost-based rate-limit configuration on LLM providers MUST continue to work unchanged; budgets and rate limits MAY both apply to the same request, and a budget refusal MUST remain distinguishable from a rate-limit refusal.

### Key Entities

- **Budget**: an organization-owned spending ceiling. Attributes: name, description, context (organization, or a project), owners (an ordered list of dimension, claim name where applicable, and a set of one or more values or "each"; dimensions: project, LLM provider, LLM proxy, model, application, token claim), overrides and overridden-by (derived, read-only: the budgets it replaces or is replaced by under FR-003a), amount, amount version (changes whenever the amount changes; re-arms thresholds), period (day/week/month), enforcement mode, thresholds, enabled flag, status reason (why the platform disabled it, e.g. its owner was deleted), audit fields. Stored by the control plane and delivered to gateways.
- **Period Counter**: *(revised 2026-10-09)* the spend of one budget (or one combination of values of its "each" owners) for one calendar period, held only in the shared counter store and incremented by every gateway; expires one day after the period ends. Not stored by the control plane; not a history store.
- **Threshold Marker**: a short-lived record in the shared counter store that a given threshold was emitted for a budget, period, value combination and amount version, so exactly one gateway emits each crossing; the crossing itself is published as part of a request's analytics event.
- **Budget Refusal**: the information returned to a refused caller — refusing budget, period, reset time, error code.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An administrator can create a working budget in under 2 minutes without redeploying any API, provider or gateway.
- **SC-002**: A newly created or changed budget is enforced on every gateway within 60 seconds.
- **SC-003**: *(Reworded 2026-10-09.)* Final period spend never exceeds the budget amount by more than the cost of requests that were already in flight (on any gateway) at the moment the budget was exhausted.
- **SC-004**: *(Reworded 2026-10-09.)* After any gateway or control-plane restart, recorded period spend is unchanged (no loss), provided the shared counter stays available.
- **SC-005**: 100% of budget refusals carry a distinct error code and a retry time accurate to the period reset within 1 second.
- **SC-006**: *(Reworded 2026-10-09.)* A budget's shared-counter value for the current period matches the sum of per-request priced costs recorded for that budget and period in the analytics platform with zero unexplained difference; unpriced requests are identifiable in analytics, not hidden.
- **SC-007**: *(Reworded 2026-10-09.)* Each threshold crossing is visible in the analytics platform within 2 minutes of the crossing request and appears exactly once per budget, period, value and threshold (per amount version) regardless of the number of gateways and replicas, so alert rules fire once per threshold.
- **SC-008**: Budget evaluation adds no user-perceptible latency to AI requests (under 5 ms at the 95th percentile, including the shared-counter round-trip, with the counter in the same network region as the gateway).

## Assumptions

- Warnings and other notifications are configured and delivered in the analytics platform (Moesif); the feature owner decided this on 2026-10-08. Warnings therefore require analytics to be enabled on the gateways.
- Monetary amounts are in USD, matching the existing cost-calculation policy's pricing data; multi-currency is out of scope.
- A budget belongs to one organization; in project mode it also belongs to that project. Projects, LLM providers, LLM proxies and applications referenced as owners must belong to the budget's organization, and in project mode project-scoped resources must belong to that project.
- There is no limit on the number of owners per budget in v1 (feature owner, 2026-10-14); a cap on owners and on values per owner (e.g. 3 owners, 2 "each" owners) may be added if gateway matching cost or counter cardinality requires it. When a specific-value budget and a broader "each" budget have the same context, period and dimensions, the most specific wins (FR-003a, feature owner, 2026-10-14).
- Per-request cost is taken from the existing LLM cost-calculation capability; improving pricing coverage is out of scope.
- Periods are UTC-only in v1; an organization-level IANA time zone, fiscal quarters and custom periods are future work.
- "Team" is not introduced as a platform entity; teams, departments and users are expressed through identity claims.
- The API Platform keeps no spend at all in the control plane and shows none in the AI Workspace (feature owner, 2026-10-09). Current-period and historical spend, per-user / per-application spend, refusals and unpriced requests are analysed in the analytics platform (Moesif / Insights). This supersedes the 2026-10-08 decision to show current-period spend in the API Platform.
- A shared Redis is required wherever budgets are enforced (feature owner, 2026-10-09). Its connection settings reuse the shape of the gateway's advanced rate-limit Redis settings (host, port, username, password, database, TLS incl. mutual TLS). Persistence and a no-eviction policy are recommended and documented; the shipped Docker Compose and Helm deployments include Redis where budgets are used.
- "Dynamic budgets for apps" (budgets that are derived from or adjust per application, e.g. via subscription plans, rollover or temporary increases) is out of scope; decided by the feature owner on 2026-10-08.
- Unpriced requests are never blocked in v1. An organization-level option to refuse unpriced models while an enforce-mode budget applies, and per-model custom prices, are future work.
- Out of scope for v1: automatic fallback to a cheaper model on exhaustion, FOCUS-format cost export, budget rollover and temporary increases, per-budget pricing overrides, and budgets in currencies other than USD.
- The "reserve worst-case cost up front" approach is not adopted in v1; overshoot limited to in-flight requests is accepted, consistent with the rest of the market.
