# Feature Specification: Budgets in the AI Gateway

**Feature Branch**: `feature/budgets-gateway`

**Created**: 2026-11-12

**Status**: Clarified

## Clarifications

### Session 2026-11-12

- Q: Gateway-local budgets (own management API, pushed up read-only) in scope? → A: Yes, in this iteration.
- Q: Project consumer? → A: Out, with agents.
- Q: Environments? → A: Out; a budget naming environments is rejected with a reason.
- Q: API key consumer? → A: Out for now. When it returns it follows Agent Manager's model (key UUID, or owning artifact plus key name); `llmProxy` is dropped as a consumer type.
- Q: First use of an allowance? → A: Never wait: admit at once and treat the allowance as $0 until its total arrives.
- Q: Flag cost usage as covered by a budget? → A: No; derived later if needed.
- Storage: a budget is an `artifacts` row of kind `Budget` plus a `budgets` per-kind table holding the Budget kind as JSON, so the shared artifact sync, push-up and deletion paths apply.

**Input**: User description: "Implement the gateway side of FinOps budgets from the design 'FinOps Support for AI Gateway V2' (Concepts BR-1 to BR-18, Architecture, Interfaces sections 3 to 5 and 7). The gateway receives budgets from the Agent control plane (and its own management API), enforces Block and Allow budgets on every LLM request, counts spend and cost in a store shared by all gateways, and reports spend per allowance and cost usage to the control plane. Only the gateway-controller talks to the shared store. Agents as consumers are out of scope for now."

## User Scenarios & Testing *(mandatory)*

Actors:

- **Budget manager**: creates budgets in the Agent control plane (or the gateway's own management API).
- **Caller**: an application or user sending LLM requests through the gateway; budgets in this iteration match on the caller's verified token claims.
- **Platform operator**: runs one or more gateways and the shared spend store.

### User Story 1 - A Block budget stops spend at its block point (Priority: P1)

A budget manager sets "$1,000 a month on OpenAI, block at 99%". Callers keep using the gateway normally until recorded spend for that budget reaches $990 in the month. From then on, covered requests are refused with a clear reason naming the budget and when it resets, until the period resets or the limit is raised.

**Why this priority**: Stopping runaway spend is the core value of budgets; everything else builds on matching and counting correctly.

**Independent Test**: With one gateway, create one Block budget with a small amount, send priced requests until the block point, and check that the next covered request is refused while uncovered requests still pass.

**Acceptance Scenarios**:

1. **Given** an active Block budget of $1,000 a month at 99% with $989.50 spent, **When** a covered request costing $0.80 completes, **Then** spent becomes $990.30 and the next covered request is refused.
2. **Given** a refused request, **When** the caller reads the refusal, **Then** it names the budget (handle) and its reset time, and never shows another consumer's identity.
3. **Given** an exhausted Block budget, **When** a request is not covered by it (other provider, exempt consumer), **Then** the request is served.
4. **Given** an exhausted budget, **When** the budget manager raises the amount above spent, **Then** covered requests are served again within seconds.
5. **Given** an exhausted monthly budget, **When** the month ends (UTC), **Then** spend restarts at zero and covered requests are served.

---

### User Story 2 - Spend is shared across gateways (Priority: P1)

A platform operator runs three gateways for the same organization. A budget covers traffic on all of them. Spend from every gateway adds up to one total, so the budget blocks at the same point whichever gateway the traffic goes through.

**Why this priority**: Real deployments run several gateways; per-gateway counting would multiply the limit.

**Independent Test**: Run two gateways against one shared store, send covered traffic to both, and check one total and a refusal on both gateways after the block point.

**Acceptance Scenarios**:

1. **Given** two gateways and one budget, **When** each serves $300 of covered traffic, **Then** the budget's spent is $600 on both gateways within about 2 seconds.
2. **Given** spend on gateway A uses up a budget, **When** the next covered request reaches gateway B, **Then** gateway B refuses it within about 2 seconds of the block point being reached.
3. **Given** a request was already counted, **When** its report is delivered twice (retry after a lost acknowledgement), **Then** it is counted once.

---

### User Story 3 - The control plane sees spend and cost (Priority: P1)

The Agent control plane shows spend per budget and per allowance for the current period, sends warnings, and shows what each project, API key, user, provider and model cost per month. The gateway reports both kinds of data: spend for budgets, and cost usage for every LLM request whether or not a budget covers it.

**Why this priority**: Warnings and the AI Cost Management pages depend on it; without it budgets are invisible to managers.

**Independent Test**: Connect a gateway to a control plane stub, send covered and uncovered traffic, and check the reported budget spend and cost usage totals.

**Acceptance Scenarios**:

1. **Given** a covered request and an uncovered request, **When** the gateway reports, **Then** the covered one appears in that budget's spend and both appear in cost usage, each exactly once.
2. **Given** a request matching two budgets, **When** reported, **Then** it adds to both budgets' spend and once to cost usage.
3. **Given** a request exempt from a budget, **When** reported, **Then** it is not in that budget's spend but is in cost usage.
4. **Given** the control plane is unreachable for 10 minutes, **When** it comes back, **Then** the latest totals arrive and enforcement was unaffected meanwhile.
5. **Given** the same totals are reported by several gateways or more than once, **When** the control plane applies them, **Then** no spend is double-counted.

---

### User Story 4 - Allow budgets and unpriced requests (Priority: P2)

A budget manager sets an Allow budget to track spend without refusing anything. Requests to a model with no price are served, charged $0, and counted as unpriced so managers can see them.

**Why this priority**: Tracking without blocking and transparent handling of missing prices are needed for trust, but they extend the P1 path.

**Independent Test**: Create an Allow budget, exceed it, and check that requests are still served and spend keeps growing; send a request to a model without a price and check the $0 charge and the unpriced count.

**Acceptance Scenarios**:

1. **Given** an Allow budget past its limit, **When** covered requests arrive, **Then** they are served and spend keeps counting beyond the limit.
2. **Given** a model with no price, **When** a covered request completes, **Then** it is served, charged $0 and counted as missing price; it never causes a refusal by itself.
3. **Given** a response with no reliable token usage, **When** it completes, **Then** it is charged $0 and counted as usage unavailable.

---

### User Story 5 - Consumers, allocation and exemptions (Priority: P2)

A budget can apply to everyone, or only to requests from a given API key, user, team or custom attribute taken from the caller's verified token. It can give one shared allowance, or a separate allowance per value (for example, $50 a month for each user). An exemption removes a consumer from one budget.

**Why this priority**: Most real budgets are per team or per user; this widens the P1 budget to real use.

**Independent Test**: Create "each user gets $50 a month" with one exempt user; send traffic from three users and check three allowances, one exempt user never counted or blocked by it.

**Acceptance Scenarios**:

1. **Given** a separate-allowance budget per user, **When** two users call, **Then** each spends from their own allowance and one being exhausted does not refuse the other.
2. **Given** a budget for team "R&D" from a token claim, **When** a request has no such claim, **Then** the budget does not cover it and the request never fails because of it.
3. **Given** an exemption for one user, **When** that user calls, **Then** that budget neither counts nor refuses the request; other covering budgets still apply.
4. **Given** several matching budgets, **When** any matching Block allowance is exhausted, **Then** the request is refused.

---

### User Story 6 - Budgets reach and stay on the gateway (Priority: P2)

Budgets created in the control plane reach every covered gateway within seconds, survive gateway restarts and keep being enforced while the control plane is unreachable. Disabled budgets are kept but enforce nothing; deleted budgets are removed.

**Why this priority**: Enforcement must not depend on the control plane being up.

**Independent Test**: Create, edit, disable and delete a budget in a control plane stub and check the gateway's state after each change and after a gateway restart with the control plane down.

**Acceptance Scenarios**:

1. **Given** a new budget in the control plane, **When** it is saved, **Then** covered gateways enforce it within 5 seconds.
2. **Given** a gateway restart while the control plane is down, **When** traffic arrives, **Then** previously received active budgets are enforced.
3. **Given** a disabled budget, **When** covered traffic arrives, **Then** it is neither refused nor counted by that budget; re-enabling keeps its earlier spend for the period.
4. **Given** a deleted budget, **When** the gateway reconnects, **Then** the budget is gone and nothing refuses because of it.

---

### User Story 7 - Running without a shared store (Priority: P3)

For development, one gateway can run budgets without the shared spend store; spend lives in that gateway's memory and resets when it restarts.

**Why this priority**: Makes local development and demos possible; production always uses the shared store.

**Independent Test**: Start one gateway with no shared store configured, exceed a Block budget, check refusal, restart, check spend restarted at zero.

**Acceptance Scenarios**:

1. **Given** no shared store, **When** a Block budget is exceeded, **Then** covered requests are refused as with the shared store.
2. **Given** no shared store, **When** the gateway restarts, **Then** spend restarts at zero for the current period.

### Edge Cases

- A request matches no budget: served and still counted in cost usage.
- A request is admitted just before the block point and completes after it: it is charged in full; spend may go slightly over the block point (bounded overshoot).
- A response fails or a stream aborts with reliable usage: charged normally.
- A budget is edited mid-period (amount, action, block-at): existing spend is kept and the new state applies within seconds; lowering the amount below spent blocks at once.
- Fixed fields change (resource, consumers, period): rejected; a new budget is needed.
- The period boundary passes while a request is in flight: it is charged to the period it was admitted in.
- A claim used by a budget is missing or has the wrong type: the budget does not cover the request; the request never fails.
- A multi-valued claim (several groups): every selected value matches; in a per-value budget the request charges each matching allowance.
- The gateway-controller restarts: requests keep being admitted with the totals already known; reports are held and sent when it is back, and counted once.
- The policy engine restarts: at most its last second of unreported spend is lost; totals are fetched again on first use.
- The shared store is unavailable or loses data: out of scope; operators run it highly available.
- Hundreds of thousands of separate allowances (per-user budgets in a large organization): only allowances in use are held in memory.

## Requirements *(mandatory)*

### Functional Requirements

Receiving and keeping budgets

- **FR-001**: The gateway MUST accept budget definitions from the Agent control plane and apply create, update, disable, enable and delete within 5 seconds of the change.
- **FR-002**: The gateway MUST fetch the full list of control-plane budgets on connect, keep newer definitions and remove budgets no longer listed.
- **FR-003**: The gateway MUST store active and inactive budgets so a restart enforces them before the control plane is reachable; deleted budgets MUST NOT be kept.
- **FR-004**: Control-plane budgets MUST be read-only through the gateway's management API.
- **FR-005**: The gateway's management API MUST let an operator create, read, update and delete budgets locally, and the gateway MUST push those budgets to the control plane as read-only.
- **FR-006**: The gateway MUST reject a budget that uses a consumer type it cannot identify, with a reason, when the budget is received.

Matching

- **FR-007**: A budget MUST cover a request only when it is active and its resource (all LLM providers, or one), its environments and every listed consumer match; different consumer types combine with AND.
- **FR-008**: Supported consumers in this iteration MUST be user, team and custom attribute, all from claims in the gateway's verified authentication context (never an unverified token or an untrusted header). Agent, project and API key consumers are out of scope, and `llmProxy` is not a consumer type; the gateway MUST reject a budget naming them, with a reason.
- **FR-009**: Environments are out of scope: the gateway MUST reject a budget that names environments, with a reason.
- **FR-010**: A missing or wrongly typed claim MUST mean "no match" and MUST NOT fail the request.
- **FR-011**: An exemption MUST remove the matching traffic from that one budget only (no counting, no refusal by it).
- **FR-012**: Allocation MUST support shared (one allowance) and separate (one allowance per distinct value, or per combination of separately allocated values).
- **FR-013**: An LLM proxy call MUST be counted once, on the outer call, not again on the provider call it loops back to.

Admission and refusal

- **FR-014**: Admission MUST NOT wait on the network per request: it uses totals the gateway already holds, plus spend it has not yet reported.
- **FR-015**: A covered priced request MUST be refused when any matching, non-exempt Block allowance has spent at or above its block point (amount × block-at percentage, 1–100, default 100).
- **FR-016**: Allow budgets MUST never refuse.
- **FR-017**: A refusal MUST use HTTP 429 with a machine-readable body listing each exhausted budget's handle and reset time, and MUST NOT include allowance values (user IDs, key names, claim values).
- **FR-018**: Requests to a model with no price MUST be served whatever budgets cover them.
- **FR-019**: The first time an allowance is used, the gateway MUST admit the request at once, treating the allowance as $0, and request its current total; the total applies to later requests as soon as it arrives.
- **FR-020**: A raised amount or a new period MUST lift a block within about a second; a lowered amount below spent MUST block at once.

Counting

- **FR-021**: After each response the gateway MUST charge the request's cost to every matching allowance of the period it was admitted in, exactly once, including failed or aborted responses with reliable usage.
- **FR-022**: Missing-price requests MUST add $0 and count as missing price; responses without reliable usage MUST add $0 and count as usage unavailable.
- **FR-023**: Spend MUST be counted in a store shared by all gateways covered by a budget, so every gateway sees one total; only the gateway's management process may connect to that store.
- **FR-024**: The request path MUST add up costs in memory and hand them over about once a second, not per request; a handover that is retried MUST be applied once.
- **FR-025**: Spend from other gateways MUST be visible to every gateway within about 2 seconds.
- **FR-026**: Every LLM request, covered or not, MUST be counted in cost usage per month and per combination of AI application, API key, user, LLM provider and model.
- **FR-027**: Without a shared store (development only, one gateway), spend MUST be kept in memory and MAY reset on restart.

Reporting to the control plane

- **FR-028**: The gateway MUST report to the control plane about once a second, on its existing connection, the current totals of budget spend per allowance and period, and of cost usage per month and cost key, in one message with two separate lists.
- **FR-029**: Reported values MUST be totals (snapshots), so a repeated, late or duplicated report never adds spend twice; every minute the gateway MUST resend totals written in the last 10 minutes.
- **FR-030**: While disconnected, the gateway MUST keep only the latest total per key and send it on reconnect; enforcement MUST NOT depend on the control plane.

Analytics and privacy

- **FR-031**: Each request's analytics event MUST carry the handles of the budgets it matched and its cost status (priced, missing price, usage unavailable), and never allowance values.
- **FR-032**: Shared-store keys MUST hold no names or user identifiers; budgets, allowances and cost keys are referenced by opaque IDs and handles.

### Key Entities

- **Budget**: a limit (amount in USD, period day/week/month, action Block or Allow, block-at percentage), a resource, environments, consumers with allocation, exemptions and a status (active/inactive). Identified by a handle and a UUID.
- **Allowance**: one spend total of a budget for one period, either shared or for one consumer value (or combination). Identified by an opaque UUID; holds spent and an unpriced count.
- **Cost key**: one combination of month, AI application, API key, user, LLM provider and model; holds cost and $0-request counts by reason.
- **Usage report (request path → management process)**: amounts added since the last report, per allowance and per cost key, with an ID for exactly-once handling.
- **Usage message (gateway → control plane)**: current totals, split into budget spend and cost usage.
- **Refusal**: HTTP 429 listing exhausted budgets and reset times.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A budget change made in the control plane is enforced on covered gateways within 5 seconds.
- **SC-002**: Once a Block allowance reaches its block point, the gateway that served the last spend refuses the next covered request at once, and every other gateway within 2 seconds.
- **SC-003**: Overshoot past the block point is limited to requests already admitted, plus at most 2 seconds of traffic on other gateways.
- **SC-004**: Budget checks add no network round trip to a request after an allowance's first use; added latency on the request path stays under 1 ms at the 99th percentile.
- **SC-005**: Reported spend and cost usage match the sum of request costs exactly (to the cent) after any number of retries, restarts of the management process, and duplicate reports.
- **SC-006**: The control plane receives totals within 2 seconds in normal operation, and within 1 minute of reconnecting after an outage.
- **SC-007**: A gateway serving 2,000 active allowances at 5,000 LLM requests per second keeps admission and counting correct.
- **SC-008**: No refusal or analytics event exposes another consumer's identity.

## Assumptions

- The Agent control plane side (budget storage, REST API, UI, warnings, cost pages) is built separately in Agent Manager; this spec covers the gateway (api-platform) and the gateway policies (gateway-controllers).
- Agents are out of scope for this iteration: no agent consumers, no per-agent allowances, no agent exemptions. Cost usage still carries the AI application, from which the control plane derives agent, project and environment.
- Production uses a highly available shared store (Valkey or Redis) with no eviction; store outages and data loss are not handled by the gateway.
- Prices come from the existing LLM cost policy, which also provides the cost status; it must also publish the resolved model.
- The API key policy exposes the validated key's identity (owner and name) to later policies.
- One gateway belongs to one organization; the organization comes from its control-plane registration.
- Periods are UTC calendar days, ISO weeks starting Monday, and calendar months; only the current period is kept.
- Amounts are USD with exact decimal arithmetic.
- The design document "FinOps Support for AI Gateway V2" is the source of truth for message shapes and storage keys; the plan and contracts follow it.
