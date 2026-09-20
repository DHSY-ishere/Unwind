# Unwind

A transactional side-effect layer for AI agents.

## The problem

Every mutating API assumes a human caller. That's why "Are you sure?" dialogs,
trash bins, undo toasts and confirmation emails exist. All of that is UI. It's
made of pixels and it's made for people.

Agents get none of it. An agent issues 200 destructive calls in four seconds
and there is no modal, no trash, no human. The safety net for API side effects
was never built, because until recently the caller could be trusted to read
it.

Unwind is the missing primitive: a write-ahead log plus compensation engine
sitting between an agent and the real world. It makes agent actions
transactional -- recorded before execution, capped by policy, reversible
after.

The demo domain is money movement, because that's where "no undo" actually
hurts.

## Architecture

```
                    ┌──────────────────────────────────────┐
                    │              unwind serve             │
                    │                                        │
   agent  ──HTTP──▶ │  POST /v1/act                          │
  (or the           │       │                                │
   scripted         │       ▼                                │
   demo             │  1. write intent (status=pending)  ────┼──▶ ledger.db
   driver)          │       │                                │    (SQLite)
                    │       ▼                                │    sessions
                    │  2. idempotency check ── replay? ──────┼──▶ intents
                    │       │                                │    approvals
                    │       ▼                                │
                    │  3. evaluate policy                    │
                    │     allow | block | (hold-for-approval)│
                    │       │                                │
                    │       ▼                                │
                    │  4. capture compensation                │
                    │     (prior state, pre-execute)          │
                    │       │                                │
                    │       ▼                                │
                    │  5. execute ────────────────────────────┼──▶ the "real"
                    │       │                                │    tools:
                    │       ▼                                │    cancel_subscription
                    │  6. commit (result + compensation)      │    issue_refund
                    │                                        │    transfer_funds
                    └──────────────────────────────────────┘         │
                                     ▲                                ▼
                         GET  /v1/sessions/{id}              seeded fake world
                         POST /v1/sessions/{id}/rollback      (vendors, invoices,
                         GET  /                (dashboard)     subscriptions,
                                                                accounts) --
                                                                same SQLite file,
                                                                separate tables
```

## The reversibility-class design position

Compensation is **declarative and per-tool, not magic**. Every tool in the
registry states its own inverse:

| Tool | Class | Capture | Compensate |
|---|---|---|---|
| `cancel_subscription` | reversible | full prior subscription row | reinstate it exactly |
| `issue_refund` | partial | invoice prior state | post a reversing journal entry, flag the invoice **contested** |
| `transfer_funds` | irreversible | nothing to reverse | `ErrUncompensable` |

**Irreversible operations are never "undone" -- they are *prevented* by the
policy gate.** That's the whole reason a compensation engine and a policy
engine live in one system: rollback answers "how do we clean up after a
reversible mistake," and policy answers "how do we stop an irreversible one
before it happens." Neither half is optional, and neither can stand in for
the other -- `transfer_funds` exists in this build specifically to prove that
the policy gate, not the rollback walk, is the answer for that class.

## Quickstart

```sh
go build -o unwind .

# terminal 1
./unwind serve

# terminal 2 -- drives 17 calls (cancellations, refunds, one transfer)
./unwind demo --scenario=rogue
# prints a session id, e.g. rogue-a1b2c3

./unwind timeline rogue-a1b2c3      # colored terminal tree
./unwind rollback rogue-a1b2c3      # compensates everything reversible

# or open the dashboard
open http://localhost:8080/?session=rogue-a1b2c3
```

To see the policy gate block something instead of rolling it back:

```sh
./unwind serve --addr :8081 --policy policy.guarded.yaml --db guarded.db
./unwind demo --scenario=guarded --server http://localhost:8081
```

`policy.guarded.yaml` lowers `per_tool.cancel_subscription` from 15 to 5, so
the identical 17-call sequence starts returning `403 blocked` partway through
-- and the driver keeps going, because a blocked call isn't a crash.

Run the whole thing end to end non-interactively:

```sh
./scripts/verify.sh
```

## HTTP API

```
POST /v1/act                         run one tool call through the pipeline
GET  /v1/sessions                    list sessions, newest first
GET  /v1/sessions/{id}               session + ordered intents
POST /v1/sessions/{id}/rollback      compensate every committed intent
GET  /v1/policy                      the active policy, in minor units
GET  /                               the dashboard
```

`DECISIONS.md` is the full log of every ambiguity, contradiction and gap found
in the original spec, and the ruling made for each -- read it before assuming
something here is a bug rather than a documented call.

## Designed but not built in the hackathon window

Kept in the schema and interfaces (so adding them later is additive, not a
redesign), but with no working code path in this build:

- **Approvals** -- `POST /v1/approvals/{intent_id}`, the `awaiting_approval`
  flow, and `require_approval` / `require_approval_above_inr` policy rules.
  The schema, statuses and ledger methods exist; nothing in `Act` currently
  produces `awaiting_approval`.
- **The cumulative amount cap** (`max_total_amount_inr`). Only the blast-radius
  caps (`max_mutations_per_session`, `per_tool`) are wired into `Act`.
- **`dryrun` policy mode.** Parsed and stored per session, but `Act` doesn't
  branch on it -- every call executes for real regardless of mode.
- **The Anthropic tool-use loop driver.** Only the scripted driver exists.
  `unwind demo` talks to a running server over plain HTTP with a fixed call
  sequence; there is no `--driver=anthropic` flag.

See `DECISIONS.md` ruling **O** for the reasoning, and `RUN_REPORT.md` for
what was actually run and verified in this session.
