# Unwind

**A transactional side-effect layer for AI agents.** Every mutating API
assumes a human caller — the "Are you sure?" dialogs, undo toasts and trash
bins that exist for people don't exist for agents. Unwind is the missing
primitive: a write-ahead ledger plus compensation engine sitting between an
agent and the real world. Every action is recorded before it runs, capped by
a live policy gate, and reversible after — except the class that genuinely
isn't, which the policy gate exists specifically to prevent, not undo.

The demo domain is money movement (subscription cancellations, refunds, fund
transfers), because that's where "no undo" actually hurts.

**The code lives in [`unwind/`](unwind/).** Start there:

- **[`unwind/README.md`](unwind/README.md)** — full architecture, the
  reversibility-class design position, quickstart, HTTP API, and the AWS /
  real-agent integrations
- **[`unwind/DEMO.md`](unwind/DEMO.md)** — the recording script
- **[`unwind/DECISIONS.md`](unwind/DECISIONS.md)** — every ambiguity in the
  original spec and the ruling made for it, in order, including the AWS and
  LLM-agent work added after initial ship
- **[`SPEC.md`](SPEC.md)** — the original spec this was built from

## Quickstart

```sh
cd unwind
go build -o unwind .
./unwind serve
# open http://localhost:8080
```

## What it does, in one run

1. **Launch** a scripted or swarm agent from the Control Room — it fires
   real tool calls (cancel a subscription, issue a refund, move funds)
   through a write-ahead ledger.
2. **Watch** the policy gate block calls once a cap is hit, live, on an
   SSE-driven activity feed.
3. **Roll back** the session — every reversible action is compensated in
   reverse order; the one irreversible action (a funds transfer) is reported
   as *uncompensable*, not silently failed, because that's the whole point:
   irreversible operations are a policy problem, not a rollback problem.
4. **Archive** the settled session to Amazon S3 with Object Lock — an audit
   record even the operator can't quietly delete.

Optionally, a real LLM (Gemini or Claude, direct HTTP tool-use loop, no SDK)
can drive the agent instead of the script, deciding its own actions — see
`unwind/README.md` for setup.
