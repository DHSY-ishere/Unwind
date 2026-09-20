# 90-second recording script

Reset to a clean slate first, off camera:

```sh
rm -f unwind.db unwind.db-shm unwind.db-wal guarded.db guarded.db-shm guarded.db-wal
go build -o unwind .
```

---

### 0:00 -- 0:12 · Setup

```sh
./unwind serve
```

**Say:** "Unwind sits between an agent and a real tool. Every call gets
written to a SQLite ledger *before* it runs -- that's the write-ahead part --
then it's capped by policy and made reversible after."

*(leave this running in one terminal; do everything else in a second one)*

### 0:12 -- 0:35 · The rogue run

```sh
./unwind demo --scenario=rogue
```

**Say (while it prints):** "This is a scripted stand-in for an agent that's
gone off the rails -- twelve subscription cancellations, four refunds, and one
funds transfer, fired straight at the HTTP API. No LLM in the loop, so this
demo can't die to a flaky network." *(copy the session id it prints, e.g.
`rogue-a1b2c3`)*

### 0:35 -- 0:55 · Timeline of the damage

```sh
./unwind timeline rogue-a1b2c3
```

**Say:** "Every one of those seventeen calls is a row -- tool, args, amount,
status. Notice the transfer is tagged *irreversible*. That's not a bug, that's
the point I'm about to make."

*(optionally switch to the browser instead: `http://localhost:8080/?session=rogue-a1b2c3`,
same data, same read)*

### 0:55 -- 1:20 · Rollback

```sh
./unwind rollback rogue-a1b2c3
```

**Say:** "One command walks every committed intent in reverse order and
compensates it -- cancellations reinstated, refunds reversed with a
contested-invoice flag. The transfer comes back *uncompensable*, not
failed -- the walk doesn't stop, it just tells you the truth: that one can't
be undone." *(if doing this in the browser instead: click **ROLLBACK
SESSION** and let the rows flip green top to bottom)*

### 1:20 -- 1:30 · The guarded run (policy gate)

```sh
./unwind serve --addr :8081 --policy policy.guarded.yaml --db guarded.db &
./unwind demo --scenario=guarded --server http://localhost:8081
```

**Say:** "Same seventeen calls, lower cap. Watch it start returning 403 partway
through -- and keep going. That's the other half of the design: the
transfer's class was *irreversible*; policy is what should have stopped it
before it ran, not rollback after."

---

## Follow-up answers

**"Why does `db.SetMaxOpenConns(1)` give you a monotonic `seq` for free?"**

`seq` is computed as `SELECT COALESCE(MAX(seq), 0) + 1 FROM intents WHERE
session_id = ?` inside the same transaction as the insert. That's a
read-then-write race if two calls for the same session could run
concurrently -- two callers could both read the same max and insert the same
seq. Capping the pool at one open connection means the whole process only
ever holds one physical connection to SQLite, so every write -- across every
request goroutine -- is serialized through it. There's no window for two
`INSERT`s to interleave, so the compute-then-insert isn't actually a race in
practice, without a mutex or a unique-and-retry loop. The cost is that writes
queue up under real concurrent load; for a ledger whose entire job is
"one true order of events," that's the right trade, not a workaround.

**"Why are irreversible operations a policy problem rather than a rollback
problem?"**

Because rollback can only replay the past -- it needs the world to still hold
enough information to undo an action, and by definition an irreversible
action destroys that information the moment it executes (`transfer_funds`'
`Capture` is a no-op; there is nothing to reverse *from*). Asking rollback to
handle that class is asking it to invent state that was never recorded. The
only point where you can still change the outcome is *before* execution --
which is exactly what the policy gate is for. That's why `transfer_funds` is
in this build at all: not because the demo needs a bank transfer, but to make
the shape of the argument concrete -- the compensation engine and the policy
engine aren't two features bolted together, they're the two halves of one
answer to "what happens when an agent does something destructive," split
along the one axis that actually matters: can this be undone, yes or no.
