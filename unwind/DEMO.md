# 3-minute submission video script

Hard budget: 180 seconds, four required beats (about the project / tech stack
& architecture / AWS usage / learning). Timings are speaking pace, not
padding — rehearse once before recording so you're not racing the clock live.

## Before you hit record

One terminal, one browser tab, side by side. Off camera:

```sh
cd unwind
rm -f unwind.db unwind.db-shm unwind.db-wal
go build -o unwind .
./unwind serve
```

Open `http://localhost:8080/` in the browser. Confirm the **Real agent** card
shows "LAUNCH REAL AGENT" as clickable (your `.env` key loaded) and the
**Blast radius** panel reads all zeros (fresh DB). Have your AWS S3 console
open in a second tab, on the `unwind-hackathon-bucket` object list, so you
can flip to it in one click during the AWS beat.

---

### 0:00–0:20 — About the project (20s)

**Say, over the Control Room screen:**

> "Every mutating API assumes a human caller — that's why 'Are you sure?'
> dialogs and undo buttons exist. Agents get none of that. Unwind is the
> missing primitive: a write-ahead ledger and compensation engine that sits
> between an AI agent and the real world — every action is recorded before
> it runs, capped by policy, and reversible after, except the one class that
> genuinely isn't — which policy exists specifically to prevent, not undo."

---

### 0:20–1:00 — Tech stack & architecture (40s)

**Say, while pointing at the Policy console / Blast radius cards:**

> "It's Go and SQLite end to end — no framework, no ORM. Every call runs
> through a six-step pipeline: write the intent *before* execution, check
> idempotency, evaluate a live policy gate, capture the compensation state,
> execute, commit. Three real tools — cancel a subscription, issue a refund,
> transfer funds — each declares its own reversibility class and its own
> inverse. The frontend is one embedded HTML file talking to that engine over
> a Server-Sent-Events ticker, so everything you're about to see is a real
> HTTP call landing in a real ledger, not an animation."

---

### 1:00–1:35 — Live proof (35s)

**Click LAUNCH SWARM.** Three colored agents fire concurrently at a shared
session.

> "Three concurrent agents, one shared budget. Watch them race against the
> same cap—" *(drag the cancel_subscription slider down, click Reset World,
> launch again)* "—and now it blocks partway through, live, no restart."

**Switch to the Session view, click ROLLBACK SESSION.**

> "Rollback walks it backward and compensates everything reversible. The one
> funds transfer comes back *uncompensable* — not failed, not hidden. That's
> the whole thesis: irreversible operations are a policy problem, prevented
> before they run, not a rollback problem fixed after."

---

### 1:35–2:25 — How AWS was used (50s) — the section judges are scoring

**Switch to the AWS S3 console tab.**

> "The ledger normally lives on the same machine as the agent's blast
> radius — anything that can reach the process can reach the file. So every
> settled session archives to Amazon S3 with **Object Lock** turned on."

**Click the object, show its version.**

> "This isn't just versioned — I tested it against a real delete. Even an
> IAM user whose policy technically grants the bypass permission gets a 403,
> because Object Lock in GOVERNANCE mode requires you to *explicitly* invoke
> the override on that specific request. It can't be deleted by accident —
> only by a deliberate, logged action."

**Say, back on the Unwind UI (optional, if time):**

> "On the build side: Go's own AWS SDK v2, direct — no wrapper. I scoped the
> IAM user to exactly S3 and Bedrock, nothing broader, on purpose — same
> philosophy as the product itself. Bedrock's wired for a fourth agent
> driver behind the identical interface Gemini and Claude already use; I
> held off spending credits on it live since it's the one AWS service here
> that's actually metered per call, and I wanted the demo itself to cost
> basically nothing."

---

### 2:25–2:50 — The real agent (25s)

**Click LAUNCH REAL AGENT on the Control Room.**

> "And this isn't only scripted — a real Gemini model can drive it too, same
> tool-use loop pattern Bedrock will plug into. It reads live account and
> invoice data, decides its own targets, and every call it makes still goes
> through that exact same policy gate."

---

### 2:50–3:00 — Close (10s)

> "Unwind: agents get 'Are you sure?' too — recorded before, capped live,
> reversible after, or stopped before it ever runs. Repo and README are
> linked below."

---

## Cutting it down further if you run long

Drop, in this order, until you're under 3:00: the "build side" paragraph in
the AWS section (it's marked optional above), the swarm-blocking slider demo
(just show the swarm running, skip re-triggering the block), the closing
line (end on the real-agent click instead).

## Follow-up answers (if judges ask live / Q&A)

**"Why does `db.SetMaxOpenConns(1)` give you a monotonic `seq` for free?"**

`seq` is computed as `SELECT COALESCE(MAX(seq), 0) + 1 FROM intents WHERE
session_id = ?` inside the same transaction as the insert — a read-then-write
race if two calls could run concurrently. Capping the pool at one connection
means every write across every goroutine is serialized through it, so there's
no window for two inserts to interleave, without a mutex or a retry loop.

**"Why are irreversible operations a policy problem rather than a rollback
problem?"**

Rollback can only replay information that was captured before execution.
An irreversible action's `Capture` is a no-op by definition — there's nothing
to reverse *from*. The only point you can still change the outcome is
*before* it runs, which is what the policy gate is for. `transfer_funds`
exists in this build specifically to make that argument concrete, not
because the demo needs a bank transfer.
