# **Budgets**

| | |
| :---- | :---- |
| Author(s) | Pasan Tennakoon pasan96tennakoon@gmail.com |
| Status | Draft |
| Last updated | 2026-10-13 |
| GitHub issue | NOT ANSWERED |
| Related docs | Budgets - Specification; Budgets - Implementation; Budgets - REST APIs; Budgets - Proposed DB Schema; UIs; Test Scenarios; Feature Capabilities; Security Impact Review Questionnaire; R&D Task Completion Questionnaire; "AI gateway budgeting market analysis"; "design-doc-v1" (the original Budgets design proposal) |

# What is the problem we are trying to solve?

Today the only spend control in the API Platform AI Gateway is a cost limit inside an LLM provider's rate-limit configuration (the `llm-cost-based-ratelimit` policy). It is a rate limit that happens to count money, not a budget: it cannot express "this team may spend $1,500 this month across every model it uses", it does not reconcile with provider invoices, and nobody is told before it trips. Each request is already priced by the existing `llm-cost` policy, but that figure is never accumulated into a ceiling that an owner manages.

The problem has five main parts:

* **No spending ceiling that spans artifacts or owners**

  The cost limit is attached to one LLM provider, so there is no pot spanning several providers, a project, or the whole organization. It is either per consumer application or shared organization-wide; there is no team, department, user or cost-centre pot, and the platform has no "team" entity to attach one to.

* **Periods that do not match invoices**

  The cost limit uses a rolling 720-hour window, so its figure never lines up with a calendar-month provider invoice or a finance team's monthly allocation.

* **Changing a limit means redeploying**

  The limit is a policy parameter of the provider. Raising it for an urgent need requires redeploying the provider, and the value has no owner of its own.

* **No warning before the stop**

  Nothing tells anyone that spend is approaching the limit. The first signal is production traffic failing.

* **No shared count, and a free path through streaming**

  Every replica of every gateway serving an organization sees only part of its traffic, so a ceiling needs one shared count — yet Redis is absent from the shipped Docker Compose files and Helm charts, and rate limiting's in-memory mode loses its count on restart. Separately, OpenAI/Azure streamed chat completions report no token usage unless the caller asks for it, so those calls are priced at $0 — a way around any money-based limit.

# Why should it be solved?

The "AI gateway budgeting market analysis" frames the demand: 98% of FinOps teams now manage AI spend, up from 31% in 2024 (State of FinOps 2026); 72% of engineering organisations had an unexpected AI cost spike in the past year and 52% have no clear owner for AI cost (Harness, 2026); and Uber reportedly used up its full-year 2026 AI budget in four months and then imposed a $1,500 per employee per month token cap (Outlook Business) — exactly a calendar-month budget keyed on a token claim. Dedicated budgets (fixed money pots over calendar periods, stacked across scopes, with threshold alerts) are now a standard AI-gateway capability, and WSO2 messaging already promises spend capping and chargeback.

* **A real ceiling for platform administrators** - an organization or a project — narrowed, if needed, to an LLM provider, LLM proxy, model or application — gets a hard daily, weekly or monthly cap that stops spend instead of only reporting it. Answers "no spending ceiling".
* **Chargeback and per-person caps without new identity infrastructure** - teams, departments, users and cost centres are budgeted through claims the customer's tokens already carry, so "every user gets $1,500 a month" works the day a customer decides it, and combines with other owners: "each user gets $10 a month on OpenAI in project-a". Answers "no ceiling that spans owners".
* **Figures finance can reconcile** - UTC calendar periods line up with provider invoices; history and reconciliation live in the analytics platform (Moesif). Answers "periods that do not match invoices".
* **Change limits in seconds, not deployments** - budgets are configured apart from any artifact and take effect on all gateways without a redeploy or restart. Answers "changing a limit means redeploying".
* **No surprise outages** - warning thresholds raise alerts before money runs out, and monitor mode lets a team size a budget from real traffic before blocking anything. Answers "no warning before the stop".
* **One exact count across every gateway** - a single shared counter limits overshoot to requests already in flight and survives every gateway and control-plane restart, and streamed calls are always priced. Answers the last problem.

# Proposed Solution

A **budget** is: this much money (USD) · for these owners · over this UTC calendar period (day, ISO week, month) · in enforce or monitor mode · warning at these thresholds. The **first owner** is the AI Workspace context the budget is created in: the organization (org mode) or a project (project mode). Any number of further owners can be appended — a project (org mode only), an LLM provider, an LLM proxy, a model (the upstream model id, e.g. `gpt-4o`), an application, or any identity claim (`team`, `department`, `sub`, `cost_centre`, …) — and a request counts toward the budget only when it matches **all** of them. Each appended owner lists one or more **specific values**, any of which matches (`provider = OpenAI`, `team = search`, or `provider ∈ {OpenAI, Anthropic}` sharing one allowance), or is **each** (every value gets its own allowance of the same amount, including values that appear later); several "each" owners give one allowance per combination. Examples: org + `sub = alice` + provider = OpenAI at $50 and org + `sub = alice` + provider = Anthropic at $20; project-a + `sub = each` + provider = each at $10 (per user per provider in project-a); org + `sub = each` + model = gpt-4o at $5; org + provider ∈ {OpenAI, Anthropic} at $2,000 (one pool for both). All matching budgets apply and the tightest one blocks, except that the **most specific wins**: when two budgets have the same context, period and owner dimensions and one is more specific (project-a `sub = alice` $50 vs project-a `sub = each` $10), only the more specific one applies to that value. Budgets with different dimensions or contexts still stack, so organization caps always apply.

platform-api owns budget **definitions only** — a new organization-level REST resource backed by one table — and stores no spend. Definitions reach every gateway of the organization as data, over the same websocket-then-xDS path subscription plans already use, and are read by an enforcement policy at request time, so a change never redeploys an artifact. On each gateway a new system policy, prepended to every LLM provider and LLM proxy route, finds the budgets whose owners all match the request through an index keyed by owner value, drops any budget overridden by a more specific matching one, reads their counters from a **shared Redis that is required for budgets** after authentication, refuses with a budget-specific 429, distinct from rate limiting, when an enforce-mode budget is exhausted, and atomically adds the request's calculated cost to every applicable counter when the response completes. The gateway that pushes a counter past a threshold claims a one-time Redis marker and records the crossing on that request's analytics event; Moesif delivers warnings through one organization-wide alert rule and shows usage in one Budget Usage card and a Budget filter on its existing LLM Analytics page. The AI Workspace gets a configuration-only Budgets page that links each budget to that usage.

![][image1]

The component-by-component design is in the Budgets - Implementation document; the five public endpoints, the internal resync and the gateway response changes are in the Budgets - REST APIs document; the one new table and one new audit column are in the Budgets - Proposed DB Schema document; the screens and Moesif templates are in the UIs document.

**platform-api responsibilities**

* Budget CRUD with validation, two new permission scopes, and audit before/after values for every change.
* Push `budget.*` definitions to every gateway of the organization; serve a resync list on gateway connect.
* Validate owners (each dimension once, claims once per claim name, no project owner in a project context, referenced resources in the organization) and store them in canonical order.
* Compute, from definitions only, which budgets each budget overrides and is overridden by (most specific wins); return them on reads and warn on create/update when a budget overrides another.
* Disable budgets whose context project, or any resource listed in an owner, is deleted; increment an amount version on every amount change.

**Gateway responsibilities**

* gateway-controller: persist and publish definitions; refuse to start when budgets are enabled without Redis.
* Budget system policy: match budgets (all owners, via an owner-value index), read and increment Redis counters, refuse or allow, inject `include_usage` on streamed chat completions, detect threshold crossings, write budget fields to the analytics event.
* Analytics pipeline: forward the budget fields to Moesif event metadata (today an allow-list drops them).

**AI Workspace responsibilities**

* Budgets list, create/edit dialog (first owner fixed by the mode, "+ Add owner" rows) and detail page in both org mode and project mode — configuration only — with "See usage" links built from a configurable analytics URL.

**Design principles**

* **Budgets are data, not policy parameters** - policy parameters are bound when a chain is built, so a budget as a parameter would redeploy every LLM route on every edit; reading definitions at request time keeps edits redeploy-free.
* **One counter, in Redis, required** - every replica of every gateway of an organization counts in the same Redis, so one budget is one amount and overshoot is limited to in-flight requests. Budgets refuse to activate without it rather than accept a ceiling and enforce nothing.
* **No spend in the control plane** - platform-api stores definitions only and receives nothing from gateways; current spend exists only in Redis, history only in Moesif.
* **Fail closed where money cannot be recovered** - if Redis is unreachable, enforce-mode budgets refuse by default; failing open is an explicit per-gateway choice.
* **Never block for the platform's own blind spots** - a request that cannot be priced is charged zero, never refused, and marked in its analytics event so the gap is visible.
* **No new authentication** - budgets use whatever identity the route's existing authentication established, so claim budgets do not apply to API-key traffic, which carries no claims.

## **Key Behaviours**

* **Overshoot is limited to requests in flight** - cost is known only after a response, so the request that crosses the line and any requests already in flight on any gateway complete and are charged; every new request on every gateway is refused as soon as the shared counter shows the budget exhausted. A stream in progress is never cut off.
* **Redis data loss restarts the period at zero** - if Redis loses its data (no persistence, failover, eviction), the affected budgets' spend for the period restarts at zero; persistence and `noeviction` are recommended and the gateway warns when they are missing.
* **Warnings are exactly-once detected, at-most-once delivered** - if the crossing request's analytics event is lost, so is the warning.

# Challenges and solutions

| Challenge | Solution |
| :---- | :---- |
| Claims are not visible in a request-header phase: a prepended header handler runs before authentication. | Check budgets in the request-body phase, which runs after all header handlers, and charge in the response phase, which runs in reverse chain order so the prepended policy runs last — after `llm-cost` has set the request's cost (the pattern `llm-cost-based-ratelimit` uses). |
| A buffer-only response policy forces the whole route into buffered mode, breaking token streaming. | The budget policy is a streaming response policy that charges on end of stream. |
| OpenAI/Azure streamed chat completions carry no usage unless requested, so they would cost $0 and bypass budgets. | When any budget applies, the gateway asks the upstream for usage on the stream (`stream_options.include_usage`), preserving the caller's other stream options. |
| An LLM proxy forwards to an LLM provider on the same gateway, so naive charging counts the request twice — but charging only the inner hop loses the caller's identity and project. | Evaluate and charge only on the outermost hop, with the applicable set covering both the proxy and its upstream provider. |
| Project budgets would not match LLM proxy traffic: platform-api does not set the project-id annotation on LLM proxy deployments. | Delivery prerequisite: add the annotation for LLM proxies; already-deployed proxies get it on redeploy. |
| Any number of owners per budget would make matching a scan of every budget on every request, and "each" owners multiply counters. | The gateway indexes budgets by owner value and checks only candidates; counter keys hold only the "each" owners' values and are created on first charge, so only combinations with traffic exist. |
| Many gateways racing on one counter would each send the same threshold warning. | A Redis `SET NX` marker per budget, period, value combination, threshold and amount version: exactly one gateway wins and emits; an amount change creates new markers, re-arming every threshold. |
| Moesif cannot compute "80% of this budget" itself: calendar alerts evaluate only after the period ends and a rule has one fixed threshold. | The gateway computes the crossing and publishes it as an event field; one Moesif rule alerts on that field's presence. |
| The Moesif publisher forwards only an allow-list of event properties, so budget fields written by a policy never reach Moesif. | Extend the analytics event preparation and the publisher's metadata allow-list (delivery phase 1). |

# Risks and open decisions

* **Architecture guidance not yet reconciled** - guidance of 2026-10-09 asked to avoid new tables and said system-level policies are not supported. The design keeps one definitions table and a built-in system policy; both need confirmation from architecture leads.
* **Redis is a hard runtime dependency** - and all gateways of an organization must share it; multi-region and hybrid deployments that cannot are an open question.
* **Older gateways, or gateways with budgets switched off, silently do not enforce** - whether the platform warns about them is open.
* **Moesif specifics unconfirmed** - deep-link path and Budget filter format, provisioning of the card / filter / rule through the Moesif API, and exempting budget events from sampling.
* **Unverified gateway capability** - provider budgets on proxy traffic depend on the proxy route exposing its upstream provider's id; aborted streams are not charged until the policy SDK offers a stream-termination hook.
* **Owner cap (open)** - no cap on owners per budget, or on values per owner, yet (decided 2026-10-14); a cap such as 3 may be needed for latency or Redis key counts.
* **Model owner depends on the gateway** - the budget policy must see the upstream model id (after any proxy rewrite) before the call and after the response; requests without it do not count toward model budgets.
* **Overrides can lift a cap** - a more specific budget replaces a broader one for its value, including a Monitor budget replacing an Enforce one; creating one shows a warning, needs `ap:budget:manage` and is audited.
* **Product decisions still open** - the meaning of "dynamic budgets for apps", custom model prices, USD only, whether track / warn / block maps onto enforce / monitor + thresholds, and viewer-role read access.

# Out of Scope

* Spend of any kind in the API Platform or the AI Workspace, and notifications sent by the API Platform itself — both live in the analytics platform.
* New analytics pages or dashboards beyond one card, one filter and one alert rule.
* Currencies other than USD, per-model custom prices and per-budget pricing overrides.
* Automatic fallback to a cheaper model, budget rollover, temporary increases, "dynamic budgets for apps" and FOCUS-format cost export.
* A "team" platform entity — teams are claims.

The full list, with the reason for each, is in the Budgets - Specification document, §1.

# Future Works

* Organization-level IANA time zone, fiscal quarters and custom periods (v1 is UTC day/week/month only).
* An organization option to refuse unpriced models while an enforce-mode budget applies, and per-model custom prices.
* Charging aborted streams once the gateway SDK exposes a stream-termination hook; Redis Sentinel/Cluster support.
* Per-project delegation of budget management; pre-estimating cost from `max_tokens` to reduce overshoot.
* MCP proxy and other artifact owners, once their traffic is priced.
* A cap on owners per budget, if gateway matching cost or counter cardinality requires it.

<!-- [image1]: <path-or-data-uri> -->
