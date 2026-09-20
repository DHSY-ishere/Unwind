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
