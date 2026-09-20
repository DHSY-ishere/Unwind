# Decisions

Rulings on ambiguities, contradictions and gaps found in `SPEC.md` during design
review. Each is binding. Where a decision departs from the literal text of the
spec, that is stated explicitly. Lettering matches the original review.

---

### A. Capture runs pre-execute; the result is merged in post-execute

**Problem.** The pipeline captures compensation (step 4) *before* execution
(step 5), but `issue_refund` is specified to capture "invoice prior state,
**refund id**" — and the refund id does not exist until the call has run.
`Capture(ctx, args)` has no access to the result.

**Ruling.** `Capture` runs pre-execute and records prior state only. The engine
then merges the execute result into `compensation_json` under a `result` key, in
the *same transaction* as the `pending -> committed` status flip.

**Departs from spec:** step ordering. The invariant the spec is protecting — a
durable record exists before any side effect — is preserved, because the
pre-execute capture and the intent row are both written before execution.

---

### B. `read` is a fourth reversibility class

**Problem.** `list_vendors` is "READ. Not ledgered." but the only entry point is
`POST /v1/act`, whose step 1 writes an intent row unconditionally. The `Class`
enum (`reversible | partial | irreversible`) has no value to dispatch on.

**Ruling.** Add class `read`. `Act` short-circuits on it at the top of the
function, before any database write. Read tools never produce an intent row,
never consume caps, and never appear in a timeline or rollback.

**Departs from spec:** adds a value to the `Class` enum.

---

### C. Approval executes inline and overrides caps

**Problem.** `POST /v1/approvals/{intent_id}` is specified to return only
`{decision}`. Nothing says who runs steps 4-6 after an approval, whether policy
is re-evaluated, or how the caller ever receives the tool result.

**Ruling.** Approval executes the held intent inline and returns the tool result
alongside the decision. **Caps are not re-evaluated** — an approval is an
override, not a second check.

Approved:
```json
{"intent_id": "...", "decision": "approved", "status": "committed", "result": {...}}
```
Denied:
```json
{"intent_id": "...", "decision": "denied", "status": "denied", "reason": "..."}
```

`denied` is added to the intent status enum.

**Departs from spec:** response shape of the approvals endpoint; adds a status.

---

### D. What counts toward a cap

**Problem.** Step 1 writes the intent as `pending` *before* step 3 evaluates
policy, so it is undefined whether blocked or held intents consume budget.

**Ruling.**

| Cap | Counts |
|---|---|
| `max_total_amount_inr` (cumulative amount) | `committed` + `awaiting_approval` |
| `max_mutations_per_session` | `committed` only |
| `per_tool` | `committed` only |

`awaiting_approval` **reserves** against the amount cap so a pending approval
cannot be double-spent. `blocked` counts toward nothing.

---

### E. Only `committed` intents are compensable

**Problem.** The rollback skip list omits `uncompensable` and
`failed_compensation`, which contradicts "rerunning rollback on a rolled-back
session is a no-op". `failed_compensation` is used in the prose but absent from
the schema enum, and `sessions.status` has no value for a rollback that
partially failed.

**Ruling.** Replace the skip list with a single positive rule:

> **Only intents in status `committed` are candidates for compensation.
> Every other status is skipped by definition.**

This makes rollback idempotent for free: compensation moves an intent out of
`committed`, so a second pass finds nothing. Enum additions:

- `intents.status` gains `failed_compensation` and `denied`
- `sessions.status` gains `partially_rolled_back`

**Departs from spec:** restates the skip list; adds three enum values.

---

### F. No rupee value exists past the YAML parser

**Problem.** `intents.amount_minor` is paise; `policy.yaml` is denominated in
rupees (`max_total_amount_inr`, `require_approval_above_inr`). Every comparison
would otherwise carry a x100 conversion.

**Ruling.** The policy loader converts to minor units once, at load. Every field
downstream is `*_minor`. `GET /v1/policy` serves minor units. No rupee-valued
number exists anywhere past the parser.

---

### G. Caps outrank approvals, and say so

**Problem.** Evaluation order puts caps before approval rules, and
`transfer_funds` is `require_approval: always`. A transfer exceeding the
cumulative cap therefore returns `403 blocked` and never reaches the approval
gate — a human cannot approve past a cap. This reads as a bug.

**Ruling.** Precedence is kept as specified. The blocked reason is worded to
make the behaviour legible:

> `exceeds cumulative cap (not approvable)`

---

### H. Idempotency replays terminal states verbatim

**Problem.** "Replay cached result if seen" is defined only for committed
intents. Blocked, failed, denied and crashed-`pending` replays are undefined,
and the API declares no conflict status.

**Ruling.** A repeated `(session_id, idempotency_key)` replays the recorded
terminal state, status code included:

| Recorded status | Replay |
|---|---|
| `committed` | `200` + cached result |
| `blocked` | `403` + cached reason and rule |
| `awaiting_approval` | `202` |
| `denied` | `200` + decision |
| `pending` | `409` — a previous attempt crashed mid-flight |

---

### I. Sessions auto-create; the row wins

**Problem.** `/v1/act` takes a `session_id` but no endpoint creates one, and
`sessions.policy_mode` duplicates `mode` in `policy.yaml` with no stated
precedence.

**Ruling.** The first act against an unknown `session_id` creates the session and
copies the current `policy.yaml` mode into `sessions.policy_mode`. Thereafter
**the row wins** — editing `policy.yaml` does not retroactively change the mode
of a running session. `GET /v1/policy` always reports the file.

This makes a per-session `dryrun` demonstrable.

---

### J. `GET /v1/sessions` is authorized

**Problem.** The web UI is required by the Stack section but has no route, and
with no session-listing endpoint its landing page could only ask for a pasted
session id.

**Ruling.** Two additions to the HTTP API:

- `GET /` serves the embedded UI
- `GET /v1/sessions` returns `id`, `created_at`, `status`, `intent_count`,
  newest first

**Departs from spec:** adds an endpoint beyond the five listed.

---

### K. The scripted driver is the primary demo path

**Problem.** `unwind demo` drives the Anthropic Messages API. A missing API key
or a dead network makes the demo unrunnable — including during a presentation.

**Ruling.** The demo has two drivers behind one flag:

- `--driver=scripted` (**default**) — a fixed sequence of calls fired at
  `/v1/act`. No network, no key, deterministic.
- `--driver=anthropic` — the tool-use loop, built last as a stretch goal.

Both drive the identical HTTP surface; the server cannot tell them apart. The
scripted driver lands early (slice 1.5) and is the exercise path for every later
slice in place of hand-rolled `curl`.

Only the `rogue` scenario is in scope.

---

### L. `list_vendors` and the `read` class are cut

**Problem.** Ruling B added a `read` reversibility class solely so
`list_vendors` (a non-ledgered read tool) could short-circuit `Act` before any
database write. Building an autonomous run under a hard time budget.

**Ruling.** `list_vendors` is cut entirely. The `Class` enum reverts to exactly
`reversible | partial | irreversible`, matching SPEC.md's data model with no
addition. `Act` no longer needs a pre-write short-circuit because every
registered tool is now ledgered. Ruling B is superseded by this cut.

**Reasoning.** `list_vendors` was a read-only convenience with no compensation
story and no policy interaction -- cutting it removes a whole code path (the
short-circuit, the enum value, the "not ledgered" special case in every
consumer of intents) for zero loss to the demo, which is about mutation and
reversal, not catalog browsing.

---

### M. `cancel_subscription(vendor_id)` identifies the subscription by vendor

**Problem.** SPEC.md's own tool signature is `cancel_subscription(vendor_id)`
-- there is no `subscription_id` argument, so the tool cannot address a
specific subscription if a vendor could have more than one.

**Ruling.** The seeded world gives each vendor at most one subscription. The
tool finds "the active subscription for this vendor" by `vendor_id` alone.
This is the only reading under which the spec's literal signature is callable.

---

### N. Refund and transfer amounts are minor units on the wire

**Problem.** SPEC.md doesn't state the unit for `issue_refund`'s and
`transfer_funds`' `amount` argument.

**Ruling.** Tool args carry amounts in paise (minor units), consistent with
`intents.amount_minor` and with ruling F's "no rupee value past the parser" --
here extended to "no rupee value anywhere in the system", including tool call
arguments. The demo driver and any future UI convert from rupees at the point
of user input, never before.

---

### O. Designed but not built in this autonomous run

**Context.** Building solo against a hard clock, with explicit instruction to
keep the tree buildable and never stop for questions. The following are kept
in SPEC.md's design (schema columns, enum values, interface shapes already
account for them) but have no working code path in this build:

- **Approvals** (`POST /v1/approvals/{intent_id}`, the `awaiting_approval`
  flow, ruling C's inline-execute-on-approve). The `approvals` table and the
  `awaiting_approval`/`denied` intent statuses exist in the schema and
  `ledger.MarkAwaitingApproval`/`MarkDenied` exist, but nothing in `Act`
  currently produces `awaiting_approval`, because...
- **Policy rules and the cumulative amount cap** (`rules:` in policy.yaml,
  `require_approval`, `require_approval_above_inr`, `max_total_amount_inr`).
  Block 8 wires only `max_mutations_per_session` and `per_tool` -- the blast
  radius caps -- because those alone are enough to make the `guarded` demo
  scenario block visibly, which is the point being proven on camera.
- **`dryrun` policy mode.** `sessions.policy_mode` is captured per DECISIONS.md
  I and `LoadPolicy` parses `mode: dryrun` correctly, but `Act` does not branch
  on it -- every call executes for real regardless of mode.
- **The Anthropic tool-use loop driver** (SPEC.md's actual agent). Only the
  scripted driver (block 4) exists; ruling K already made the scripted path
  primary, and the LLM loop was always the stretch goal.

None of these are silently broken -- they are simply absent. `unwind serve`
never returns a 202, `policy.yaml`'s `rules:` section is parsed but ignored,
and there is no `--driver=anthropic` flag. This is restated in RUN_REPORT.md
and the README's "designed but not built" section.

---

### P. The demo requires a separately running server; guarded uses a second policy file

**Problem.** SPEC.md doesn't say whether `unwind demo` starts its own server or
talks to one already running, and the guarded scenario needs a lower
`per_tool.cancel_subscription` cap than the default `policy.yaml` without
touching that file (which the rogue scenario also relies on).

**Ruling.** `unwind demo` is a pure HTTP client (`--server`, default
`http://localhost:8080`) -- it never starts a server itself, matching "over
HTTP as an ordinary client, not in-process" literally. Both `--scenario=rogue`
and `--scenario=guarded` fire the *identical* 17-call sequence
(`rogueScenario()` in `demo.go`); what changes is which policy the server was
started with. `policy.guarded.yaml` duplicates `policy.yaml` with
`per_tool.cancel_subscription` dropped to 5 (ruling Q raises the default to
15), so the 6th
`cancel_subscription` call in the sequence blocks. DEMO.md gives the exact
two-terminal invocation for each.

---

### Q. `policy.yaml`'s default `cancel_subscription` cap was raised to fit the rogue scenario

**Problem.** Found by `scripts/verify.sh`, not by inspection: the original
`policy.yaml` (copied verbatim from SPEC.md) caps
`per_tool.cancel_subscription` at 10, but `rogueScenario()` (block 4) fires 12
`cancel_subscription` calls. Once block 8 wired the per-tool cap into `Act`,
the "rogue" run itself started returning two 403s at calls #16-17 -- against
the *default*, supposedly-uncapped policy. That breaks the intended contrast:
rogue is supposed to demonstrate what happens with no effective ceiling before
policy is introduced as the fix; guarded is supposed to be the one that
blocks.

**Ruling.** `policy.yaml`'s `per_tool.cancel_subscription` is raised from 10 to
15 -- comfortably above the 12 the rogue scenario needs, while
`policy.guarded.yaml` keeps it at 5 to force a block partway through the
identical sequence (ruling P). `max_mutations_per_session: 20` already had
headroom (17 calls total) and needed no change.

---

### R. Live-editable caps, an in-process demo trigger, and a World read model

**Context.** Post-ship, the user asked for a bigger walkthrough surface: a
Control Room to launch the scenario from the browser, a World screen showing
the fake world's real state, and an interactive Policy console. None of this
changes the pipeline; it's additive read/trigger endpoints plus one runtime
mutability seam.

**Rulings.**

- **Caps become live-mutable.** `Engine.Policy.Caps` is now guarded by a
  `sync.RWMutex` (`CapsSnapshot()` / `UpdateCaps()`), edited via
  `POST /v1/policy`. This is a **runtime-only override for the current
  process** -- it never writes to `policy.yaml`, never touches `Mode` or
  `Rules`, and resets to the file's values on restart. `GET /v1/policy` now
  serves `Engine.PolicySnapshot()` (mode/rules from the file, caps live)
  instead of the raw loaded struct.
- **`rogue` vs `guarded` collapses into one trigger.** `POST /v1/demo/run`
  fires the identical 17-call sequence in-process (moved to a new leaf
  package, `internal/demo`, shared with the CLI driver so there's exactly one
  copy of the sequence) against whatever caps are live at that moment.
  Whether a given run looks "rogue" or "guarded" is now a property of the
  caps you dialed in beforehand, not a separate code path. The CLI's
  `--scenario=rogue|guarded` flag and `policy.guarded.yaml` are unchanged and
  still used by `scripts/verify.sh` -- this is an additional front door, not
  a replacement.
- **The world gets a read model.** `GET /v1/world` (via `tools.Snapshot`) is a
  plain, never-ledgered read of accounts/vendor-subscriptions/invoices --
  exactly the same status as the cut `list_vendors` (ruling L), just serving
  the dashboard instead of an agent.
- **The world needed a reset button.** Found while testing the Control Room
  live: the world is shared across every session, so launching the scenario
  twice in a row makes the second run's `cancel_subscription` calls
  legitimately fail (nothing active left to cancel) -- not "block," which
  looks like a bug when you're mid-demo trying to show the policy gate.
  `POST /v1/world/reset` (`tools.ResetWorld`) wipes and reseeds the world
  (same deterministic ids every time -- `seed.sql`'s RNG has a fixed seed) so
  the demo is repeatable without restarting the server. It never touches the
  ledger -- past sessions keep their history against a freshly reset world.

---

### S. Swarm, live ticker, and a client-side time-travel scrubber

**Context.** Second post-ship round: the user wanted the demo to say something
bigger than "one agent, caught." Three additions, all additive to the
existing pipeline.

- **Multi-Agent Swarm** (`POST /v1/swarm/run`). Three named, colored agents
  (`internal/demo`: `Cutter-Alpha` cancels subscriptions, `Refund-Beta`
  issues refunds, `Finance-Gamma` makes the one transfer) fire concurrently
  via goroutines at **one shared session**, racing against the same caps.
  `Act()` itself has no concept of "agent" -- the name and color are a
  presentation-layer tag carried only in the SSE event and the HTTP
  response, never written to the ledger. This is deliberate: the point is
  that the policy gate arbitrates a shared budget regardless of which caller
  is asking, so the gate must not need to know who's asking. No engine
  changes were needed for correctness -- the single-writer SQLite connection
  (`ledger.Open`'s `SetMaxOpenConns(1)`) plus the caps mutex already added for
  the Policy console (ruling R) make concurrent `Act()` calls race-free by
  construction.
- **Live ticker** (`GET /v1/stream`, Server-Sent Events via a small in-process
  `Broker` in `internal/api`). Every `/v1/act`, `/v1/demo/run`,
  `/v1/swarm/run` call and every rollback publishes an event; any number of
  browser tabs can subscribe. It is explicitly **not durable** -- a tab that
  wasn't open when an event fired never sees it; the ledger remains the only
  system of record. A small server-side jitter (60-220ms between calls in
  `postDemoRun`/`postSwarmRun`) is added purely for watchability -- without
  it, 17 SQLite writes finish in single-digit milliseconds and the ticker has
  nothing to show. This does not change `Act()`'s behavior or timing
  guarantees, only how quickly the *demo driver* fires successive calls.
- **Time-travel scrubber** (Session view only). Pure client-side replay over
  data already fetched from `GET /v1/sessions/{id}` -- dragging it dims every
  row past the chosen point. No new endpoint; the ledger's append-only history
  is what makes this free.

---

### T. The real agent -- a genuine tool-use loop, raw HTTP, additive only

**Context.** SPEC.md always specified a real Anthropic driver ("direct HTTP to
the Anthropic Messages API, tool-use loop, no SDK") as the actual agent; it
was deferred as a stretch goal in the original build (ruling K) and never
built (ruling O). Built now, on request, as a genuine third addition -- not a
replacement for the scripted or swarm drivers, which remain the primary,
key-free, deterministic demo path.

**What it is.** `internal/llmagent` is a small raw-HTTP client (no SDK,
matching SPEC.md's own instruction) plus a bounded tool-use loop
(`RunAgent`): Claude Opus 5 is given the three real tool schemas (matching
tool args exactly, paise and all -- ruling N) plus one extra, `get_world_state`,
a read-only tool that exists **only for this driver** so the model can see
real vendor/invoice/account ids before acting, exactly as the cut
`list_vendors` would have (ruling L) -- it is not part of `tools.Registry`
and is never ledgered. `POST /v1/agent/run` wires the three real tool calls
straight through `Engine.Act` (the exact same pipeline every other driver
uses -- policy gate, capture, compensation record, all of it) and publishes
every decision to the live ticker with agent name "Claude" so a real model's
choices show up exactly like the scripted and swarm runs.

**Guardrails, because this is the one driver whose behavior isn't fully
determined by code:**
- Hard bounds regardless of what the model decides: `maxTurns = 12`,
  `maxToolCalls = 20` (`internal/llmagent/agent.go`). An agentic loop against
  a real system must never be allowed to run unbounded.
- `ANTHROPIC_API_KEY` is read from the server process's environment at
  request time, via a plain `os.Getenv` -- not any credential-profile
  resolution. A missing key returns `503` with a clear message rather than a
  crash or a silent no-op; the scripted and swarm drivers are entirely
  unaffected by whether it's set.
- Because the key is read from the process's own environment (fixed at
  `unwind serve`'s start, per normal OS process semantics), exporting it in
  another shell after the server is already running has no effect -- the
  server must be restarted with the variable set.
- Blocked/failed tool results are fed back to the model as a normal (if
  `is_error: true`) tool result, with the policy reason included, so the
  model can see it was stopped and move on -- per its system prompt
  instruction not to retry a blocked call.

**Not verified live in this session.** No `ANTHROPIC_API_KEY` (or `ant`
credential profile) was available in the build environment, so the actual
tool-use loop against the real API could not be exercised here. The `503`
missing-key path, the request/response JSON shapes (built from documented
Messages API examples, not guessed), and every other driver were verified.
This is the one piece of the project running on read-the-docs-correctly
rather than a green checkmark -- test it with a real key before relying on it
for a recording.

---

## Build order

| Slice | Delivers |
|---|---|
| 0 | skeleton: serve, schema, `GET /v1/policy` |
| 1 | ledger + happy-path act |
| 1.5 | scripted demo driver |
| 2 | idempotency replay |
| 3 | policy gate |
| 4 | compensation + rollback (+ engine tests) |
| 5 | approvals |
| 6 | web UI |
| 7 | Anthropic tool-use loop *(stretch)* |
