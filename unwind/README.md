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
GET  /v1/policy                      the active policy (mode/rules from the
                                      file, caps live -- see ruling R)
POST /v1/policy                      edit live mutation caps (runtime-only)
GET  /v1/world                       accounts / vendor subscriptions / invoices
POST /v1/world/reset                 reseed the world back to its fixed state
POST /v1/demo/run                    fire the scripted sequence in-process
GET  /                               the dashboard (Control Room / World / Session)
```

The dashboard is a 3-view SPA: **Control Room** (launch the agent, tune caps
live with sliders, browse recent sessions), **World** (live account balances,
vendor subscription status, touched invoices), and **Session** (the per-run
timeline and rollback, as before). Launching from the Control Room and tuning
the Policy console's sliders both act on the same running server the CLI
talks to -- there's no separate demo mode.

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

The Anthropic and Gemini tool-use loops described below *are* built --
`POST /v1/agent/run` and the Control Room's "LAUNCH REAL AGENT" card -- as an
additional front door alongside the scripted and swarm drivers, not a
replacement for either.

See `DECISIONS.md` rulings **O**, **T** and **U** for the reasoning, and
`RUN_REPORT.md` for what was actually run and verified in the original
session.

## AWS: the audit tier

The SQLite ledger proves what an agent did, but it lives on the same machine
as the agent's blast radius -- anything that can reach the process can reach
the file. Unwind exports each settled session to **Amazon S3** as a
self-contained, tamper-evident audit record: the session, every intent in seq
order, and the captured compensation state an auditor needs to reconstruct
what happened and what was reversed.

```sh
AWS_S3_BUCKET=my-unwind-audit AWS_REGION=us-east-1 ./unwind serve
```

- `POST /v1/sessions/{id}/archive` archives on demand
- rollback archives **automatically** -- a rolled-back session is a settled
  one, and that's the moment worth freezing
- the key is stable per session (`sessions/<id>.json`), so with **bucket
  versioning + Object Lock** every re-archive preserves the prior version
  rather than replacing it. That's what makes it an audit trail rather than a
  log the agent could rewrite.

Unconfigured, the feature is simply absent: the endpoint answers 503, the UI
button never renders, and rollback behaves exactly as before. Set
`AWS_ENDPOINT_URL=http://localhost:4566` to develop against LocalStack with
no AWS account at all.

## The real agent

`POST /v1/agent/run` runs a genuine tool-use loop -- Gemini or Claude decides
what to do, not a script -- with every decision routed through the exact same
`Act()` pipeline, policy gate, and ledger as the scripted and swarm drivers.
Set one of these before starting the server (it's read once at boot, so
export it first):

```sh
export GEMINI_API_KEY=...      # tried first
# or
export ANTHROPIC_API_KEY=...   # used if Gemini's key isn't set
./unwind serve
```

Then hit **LAUNCH REAL AGENT** on the Control Room, or:

```sh
curl -X POST http://localhost:8080/v1/agent/run \
  -d '{"goal": "Cancel two subscriptions and issue one refund. Be brief."}'
```

The model gets the three real tools (exact same arg shapes the scripted
driver uses) plus one extra, `get_world_state` -- a read-only tool that
exists only for this driver, never ledgered, never part of the tool
registry -- so it can see real vendor/invoice/account ids before acting
instead of inventing them. Every call it makes shows up on the live ticker
tagged with the model's name, and the resulting session rolls back exactly
like any other. Verified end-to-end against Gemini 2.5 Flash: see
`DECISIONS.md` ruling **U**.
