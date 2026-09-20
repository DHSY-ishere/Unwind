# Unwind — a transactional side-effect layer for AI agents

## Problem
Every mutating API assumes a human caller. That's why "Are you sure?" dialogs,
trash bins, undo toasts and confirmation emails exist. All of that is UI. It's
made of pixels and it's made for people.

Agents get none of it. An agent issues 200 destructive calls in four seconds
and there is no modal, no trash, no human. The safety net for API side effects
was never built, because until recently the caller could be trusted to read it.

Unwind is the missing primitive: a write-ahead log plus compensation engine
sitting between an agent and the real world. It makes agent actions
transactional — recorded before execution, capped by policy, reversible after.

Demo domain is money movement, because that's where "no undo" actually hurts.

## Architecture
agent -> Unwind proxy -> real tools
              |
           ledger (SQLite)

Every mutating call:
1. write intent record (status=pending)  <- BEFORE execution
2. check idempotency key -> replay cached result if seen
3. evaluate policy -> allow | block | hold-for-approval
4. capture compensation (the inverse action + prior state)
5. execute against the real tool
6. mark committed, store result

## Data model (SQLite)

sessions
  id TEXT PK
  created_at TIMESTAMP
  policy_mode TEXT       -- off | dryrun | enforce
  status TEXT            -- active | rolled_back

intents
  id TEXT PK
  session_id TEXT FK
  seq INTEGER            -- monotonic per session, drives rollback order
  tool TEXT
  args_json TEXT
  idempotency_key TEXT   -- UNIQUE(session_id, idempotency_key)
  reversibility TEXT     -- reversible | partial | irreversible
  status TEXT            -- pending | committed | failed | blocked
                         -- | awaiting_approval | compensated | uncompensable
  amount_minor INTEGER   -- paise, 0 for non-financial
  result_json TEXT
  compensation_json TEXT -- captured inverse: prior state + inverse call
  created_at TIMESTAMP
  settled_at TIMESTAMP

approvals
  id TEXT PK
  intent_id TEXT FK
  decision TEXT          -- approved | denied
  decided_at TIMESTAMP

## Tools (exactly four — do not add more)

1. list_vendors()                       READ. Not ledgered.
2. cancel_subscription(vendor_id)       REVERSIBLE.
     capture: full prior subscription row
     compensate: reinstate from captured row
3. issue_refund(invoice_id, amount)     PARTIAL.
     capture: invoice prior state, refund id
     compensate: post reversing journal entry, flag invoice as contested
4. transfer_funds(from, to, amount)     IRREVERSIBLE.
     capture: nothing to reverse
     compensate: returns ErrUncompensable, marks intent uncompensable
     -> exists to prove the policy gate is the answer for this class

## Reversibility classes — the core design position
Compensation is declarative and per-tool, not magic. Each tool registers its
own inverse. Irreversible operations are never "undone" — they are *prevented*
by the policy gate. That is why both halves live in one system.

## Compensation registry interface

type Tool interface {
    Name() string
    Reversibility() Class
    AmountMinor(args map[string]any) int64
    Execute(ctx, args map[string]any) (result map[string]any, err error)
    Capture(ctx, args map[string]any) (compensation map[string]any, err error)
    Compensate(ctx, compensation map[string]any) error
}

Registry maps name -> Tool. Adding a tool is one file, zero core changes.

## Policy (policy.yaml)

mode: enforce
caps:
  max_mutations_per_session: 20
  max_total_amount_inr: 50000
  per_tool:
    issue_refund: 5
    cancel_subscription: 10
rules:
  - tool: transfer_funds
    require_approval: always
  - tool: issue_refund
    require_approval_above_inr: 10000

Evaluation order: mode check -> per-tool cap -> session mutation cap ->
cumulative amount cap -> approval rules. First failure wins.
dryrun mode: ledger everything, execute nothing, return synthetic results.

## Rollback semantics
- Walk intents for session in DESCENDING seq.
- Skip: already compensated, blocked, failed, awaiting_approval.
- irreversible -> mark uncompensable, continue, report in summary.
- Idempotent: rerunning rollback on a rolled-back session is a no-op.
- Partial failure: mark that intent failed_compensation, continue the rest,
  report both counts. Never abort the whole rollback on one failure.

## HTTP API
POST /v1/act
  {session_id, tool, args, idempotency_key}
  200 {intent_id, status:"committed", result}
  202 {intent_id, status:"awaiting_approval", reason}
  403 {intent_id, status:"blocked", reason, policy_rule}
GET  /v1/sessions/{id}          -> session + ordered intents
POST /v1/sessions/{id}/rollback -> {compensated, uncompensable, failed}
POST /v1/approvals/{intent_id}  -> {decision:"approved"|"denied"}
GET  /v1/policy                 -> current policy

## CLI
unwind serve
unwind demo --scenario=rogue        # drives the agent, prints session id
unwind timeline <session_id>
unwind rollback <session_id>
unwind approve <intent_id> / unwind deny <intent_id>

## Non-goals (do not build)
auth, multi-tenancy, user accounts, migrations framework, agent framework,
real payment integration, distributed anything, tests beyond the compensation
engine, Docker, CI.

## Stack
Go 1.22+, stdlib net/http, modernc.org/sqlite (pure Go, no cgo),
spf13/cobra for CLI, gopkg.in/yaml.v3.
Agent driver: direct HTTP to the Anthropic Messages API, tool-use loop, no SDK.
Web UI: one static HTML file served by the Go binary, Tailwind via CDN.