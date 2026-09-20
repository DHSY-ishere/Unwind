# Run report

Autonomous build, one hour, no questions asked. This is the honest account:
what got built, what got cut, every judgment call made in your absence, and
what I'd want you to know before you hit record.

## Blocks completed

All 11 numbered blocks completed and are committed. Nothing was skipped.

| # | Block | Status |
|---|---|---|
| 1 | Tools and world state | done -- `list_vendors`/`read` class cut (DECISIONS.md L) |
| 2 | Act pipeline | done |
| 3 | Timeline renderer | done |
| 4 | Scripted driver | done |
| 5 | Rollback | done |
| 6 | Tests | done -- 4/4 passing |
| 7 | Idempotency | done |
| 8 | Policy (mutation cap only) | done -- amount caps/approvals/dryrun intentionally unbuilt |
| 9 | Web UI | done -- **not visually verified in a browser**, see Risks |
| 10 | Docs | done, tagged `v0.1.0` |
| 11 | Verify and report | done -- this file |

## `scripts/verify.sh` -- full output of the final run

```
=== clean slate ===

=== build ===

=== vet ===

=== start rogue server (:18080, verify-rogue.db, default policy.yaml) ===

=== rogue scenario ===
driving 17 calls against http://localhost:18080  (session rogue-7e7c7f)

  [ 1] cancel_subscription  committed
  [ 2] cancel_subscription  committed
  [ 3] issue_refund         committed
  [ 4] cancel_subscription  committed
  [ 5] cancel_subscription  committed
  [ 6] cancel_subscription  committed
  [ 7] issue_refund         committed
  [ 8] transfer_funds       committed
  [ 9] cancel_subscription  committed
  [10] cancel_subscription  committed
  [11] issue_refund         committed
  [12] cancel_subscription  committed
  [13] cancel_subscription  committed
  [14] cancel_subscription  committed
  [15] issue_refund         committed
  [16] cancel_subscription  committed
  [17] cancel_subscription  committed

committed=17 blocked=0 other=0
rogue-7e7c7f
session: rogue-7e7c7f

=== timeline before rollback ===
Session rogue-7e7c7f  [enforce]  active
├─ #1   ✔  cancel_subscription  vendor_id=vnd_001                                 ₹0  committed 
├─ #2   ✔  cancel_subscription  vendor_id=vnd_002                                 ₹0  committed 
├─ #3   ✔  issue_refund         amount=₹5,000 invoice_id=inv_0001             ₹5,000  committed 
├─ #4   ✔  cancel_subscription  vendor_id=vnd_003                                 ₹0  committed 
├─ #5   ✔  cancel_subscription  vendor_id=vnd_004                                 ₹0  committed 
├─ #6   ✔  cancel_subscription  vendor_id=vnd_005                                 ₹0  committed 
├─ #7   ✔  issue_refund         amount=₹7,500 invoice_id=inv_0002             ₹7,500  committed 
├─ #8   ✔  transfer_funds       amount=₹2,00,000 from=acc_main to=acc…     ₹2,00,000  committed  (irreversible)
├─ #9   ✔  cancel_subscription  vendor_id=vnd_006                                 ₹0  committed 
├─ #10  ✔  cancel_subscription  vendor_id=vnd_007                                 ₹0  committed 
├─ #11  ✔  issue_refund         amount=₹6,000 invoice_id=inv_0003             ₹6,000  committed 
├─ #12  ✔  cancel_subscription  vendor_id=vnd_008                                 ₹0  committed 
├─ #13  ✔  cancel_subscription  vendor_id=vnd_009                                 ₹0  committed 
├─ #14  ✔  cancel_subscription  vendor_id=vnd_010                                 ₹0  committed 
├─ #15  ✔  issue_refund         amount=₹4,500 invoice_id=inv_0004             ₹4,500  committed 
├─ #16  ✔  cancel_subscription  vendor_id=vnd_011                                 ₹0  committed 
└─ #17  ✔  cancel_subscription  vendor_id=vnd_012                                 ₹0  committed 

=== rollback ===
↺ compensated=16  ⚠ uncompensable=1  ✗ failed=0

Session rogue-7e7c7f  [enforce]  rolled_back
├─ #1   ↺  cancel_subscription  vendor_id=vnd_001                               ₹̶0̶  compensated   
├─ #2   ↺  cancel_subscription  vendor_id=vnd_002                               ₹̶0̶  compensated   
├─ #3   ↺  issue_refund         amount=₹5,000 invoice_id=inv_0001         ₹̶5̶,̶0̶0̶0̶  compensated   
├─ #4   ↺  cancel_subscription  vendor_id=vnd_003                               ₹̶0̶  compensated   
├─ #5   ↺  cancel_subscription  vendor_id=vnd_004                               ₹̶0̶  compensated   
├─ #6   ↺  cancel_subscription  vendor_id=vnd_005                               ₹̶0̶  compensated   
├─ #7   ↺  issue_refund         amount=₹7,500 invoice_id=inv_0002         ₹̶7̶,̶5̶0̶0̶  compensated   
├─ #8   ⚠  transfer_funds       amount=₹2,00,000 from=acc_main to=acc…     ₹2,00,000  uncompensable  (irreversible)
├─ #9   ↺  cancel_subscription  vendor_id=vnd_006                               ₹̶0̶  compensated   
├─ #10  ↺  cancel_subscription  vendor_id=vnd_007                               ₹̶0̶  compensated   
├─ #11  ↺  issue_refund         amount=₹6,000 invoice_id=inv_0003         ₹̶6̶,̶0̶0̶0̶  compensated   
├─ #12  ↺  cancel_subscription  vendor_id=vnd_008                               ₹̶0̶  compensated   
├─ #13  ↺  cancel_subscription  vendor_id=vnd_009                               ₹̶0̶  compensated   
├─ #14  ↺  cancel_subscription  vendor_id=vnd_010                               ₹̶0̶  compensated   
├─ #15  ↺  issue_refund         amount=₹4,500 invoice_id=inv_0004         ₹̶4̶,̶5̶0̶0̶  compensated   
├─ #16  ↺  cancel_subscription  vendor_id=vnd_011                               ₹̶0̶  compensated   
└─ #17  ↺  cancel_subscription  vendor_id=vnd_012                               ₹̶0̶  compensated   

=== timeline after rollback ===
Session rogue-7e7c7f  [enforce]  rolled_back
├─ #1   ↺  cancel_subscription  vendor_id=vnd_001                               ₹̶0̶  compensated   
├─ #2   ↺  cancel_subscription  vendor_id=vnd_002                               ₹̶0̶  compensated   
├─ #3   ↺  issue_refund         amount=₹5,000 invoice_id=inv_0001         ₹̶5̶,̶0̶0̶0̶  compensated   
├─ #4   ↺  cancel_subscription  vendor_id=vnd_003                               ₹̶0̶  compensated   
├─ #5   ↺  cancel_subscription  vendor_id=vnd_004                               ₹̶0̶  compensated   
├─ #6   ↺  cancel_subscription  vendor_id=vnd_005                               ₹̶0̶  compensated   
├─ #7   ↺  issue_refund         amount=₹7,500 invoice_id=inv_0002         ₹̶7̶,̶5̶0̶0̶  compensated   
├─ #8   ⚠  transfer_funds       amount=₹2,00,000 from=acc_main to=acc…     ₹2,00,000  uncompensable  (irreversible)
├─ #9   ↺  cancel_subscription  vendor_id=vnd_006                               ₹̶0̶  compensated   
├─ #10  ↺  cancel_subscription  vendor_id=vnd_007                               ₹̶0̶  compensated   
├─ #11  ↺  issue_refund         amount=₹6,000 invoice_id=inv_0003         ₹̶6̶,̶0̶0̶0̶  compensated   
├─ #12  ↺  cancel_subscription  vendor_id=vnd_008                               ₹̶0̶  compensated   
├─ #13  ↺  cancel_subscription  vendor_id=vnd_009                               ₹̶0̶  compensated   
├─ #14  ↺  cancel_subscription  vendor_id=vnd_010                               ₹̶0̶  compensated   
├─ #15  ↺  issue_refund         amount=₹4,500 invoice_id=inv_0004         ₹̶4̶,̶5̶0̶0̶  compensated   
├─ #16  ↺  cancel_subscription  vendor_id=vnd_011                               ₹̶0̶  compensated   
└─ #17  ↺  cancel_subscription  vendor_id=vnd_012                               ₹̶0̶  compensated   

=== start guarded server (:18081, verify-guarded.db, policy.guarded.yaml) ===

=== guarded scenario (expect blocks partway through) ===
driving 17 calls against http://localhost:18081  (session guarded-f8245b)

  [ 1] cancel_subscription  committed
  [ 2] cancel_subscription  committed
  [ 3] issue_refund         committed
  [ 4] cancel_subscription  committed
  [ 5] cancel_subscription  committed
  [ 6] cancel_subscription  committed
  [ 7] issue_refund         committed
  [ 8] transfer_funds       committed
  [ 9] cancel_subscription  BLOCKED  (per_tool.cancel_subscription (max 5))
  [10] cancel_subscription  BLOCKED  (per_tool.cancel_subscription (max 5))
  [11] issue_refund         committed
  [12] cancel_subscription  BLOCKED  (per_tool.cancel_subscription (max 5))
  [13] cancel_subscription  BLOCKED  (per_tool.cancel_subscription (max 5))
  [14] cancel_subscription  BLOCKED  (per_tool.cancel_subscription (max 5))
  [15] issue_refund         committed
  [16] cancel_subscription  BLOCKED  (per_tool.cancel_subscription (max 5))
  [17] cancel_subscription  BLOCKED  (per_tool.cancel_subscription (max 5))

committed=10 blocked=7 other=0
guarded-f8245b
session: guarded-f8245b

=== go test ./... ===
?       github.com/DHSY-ishere/unwind  [no test files]
?       github.com/DHSY-ishere/unwind/internal/api     [no test files]
ok      github.com/DHSY-ishere/unwind/internal/engine  (cached)
?       github.com/DHSY-ishere/unwind/internal/ledger  [no test files]
?       github.com/DHSY-ishere/unwind/internal/tools   [no test files]

=== done ===
all checks passed
EXIT: 0
```

## Every decision made in your absence

Full text with reasoning is in `DECISIONS.md`; this is the index. Rulings
**A** through **K** were yours, from the pre-build review, and were not
revisited. Everything from **L** onward is new, made autonomously while you
were away:

- **L** -- cut `list_vendors` and the `read` reversibility class entirely
  (you told me to; this supersedes your own earlier ruling B).
- **M** -- `cancel_subscription(vendor_id)` identifies the subscription by
  vendor, because that's the only tool signature SPEC.md actually gives you.
- **N** -- tool call args carry amounts in paise, not rupees, consistent with
  everything else in the system.
- **O** -- the explicit list of what's designed but not built: approvals, the
  cumulative amount cap, `dryrun` mode, the Anthropic driver. All four have
  schema/enum/interface support already in place; none has a working code
  path. This is the single most important thing in this report if you're
  about to demo something that assumes one of them works.
- **P** -- `unwind demo` is a pure HTTP client, never starts its own server;
  rogue and guarded fire the identical 17-call sequence, differing only in
  which policy file the server was started with.
- **Q** -- caught by `verify.sh`, not by review: the default `policy.yaml`'s
  `per_tool.cancel_subscription` cap (10) was *lower* than the rogue
  scenario's 12 cancellations, so the "unguarded" run was blocking two calls
  on its own -- undercutting the entire point of having a second, more
  restrictive `guarded` policy. Raised the default to 15. If you rerun
  `verify.sh` and see `blocked=0` for rogue and `blocked=7` for guarded,
  this is fixed; those exact numbers are what's pasted above.

## Risks and things I'd flag before recording

**The web UI has not been opened in an actual browser.** I verified: it
compiles and embeds correctly, `GET /` returns 200 with the right
content-type, and the inline `<script>` block parses as valid JavaScript
(checked with `node -e`). I did not verify the fetch/render/poll/rollback
loop actually paints correctly in a real DOM, because no browser automation
was available in this environment. This is the one part of the deliverable
running on hope rather than a green checkmark. **Before you record: open
`http://localhost:8080/?session=<a session id>` yourself first.** If
something's visually off, it's almost certainly in `internal/api/ui/index.html`
and easy to fix live.

**Two known, minor realism gaps in the fake world**, neither a spec
violation, both worth knowing about if you improvise past the scripted
scenarios on camera:
- `issue_refund` doesn't check the refund amount against the invoice's
  outstanding balance, and doesn't prevent refunding an already-refunded
  invoice. It'll happily "succeed" both times.
- `cancel_subscription` and the demo scenarios assume a fresh database.
  Rerunning `unwind demo` against a database that already has those vendors'
  subscriptions cancelled will show `failed` rows (correctly -- there's
  nothing active left to cancel), not `committed`. `DEMO.md` and
  `scripts/verify.sh` both start from a clean `*.db` for exactly this reason
  -- don't skip that step live.

**`checkCaps` (policy.go) fails open on a database error** -- if
`CountCommitted`/`CountCommittedByTool` errors, that's treated as "cap not
exceeded" rather than blocking the call. Given the single-writer SQLite setup
this is very unlikely to trigger, but it means a cap is not a hard
information-theoretic guarantee against a DB fault, which is worth knowing if
anyone asks a pointed question about it.

**Rollback trusts each tool's `Compensate` to self-report irreversibility**
via `ErrUncompensable`; there's no independent check against the
`reversibility` column stored on the intent. This is the intended design
(ruling: compensation is declarative and per-tool, not magic) but it does
mean a mis-implemented tool could lie about its own reversibility with
nothing else in the system to catch it. Worth knowing, not worth fixing under
a clock -- all three shipped tools are correct.

**This environment auto-commits on file writes** (visible in the git log as
plain, un-authored-by-me commit messages like `feat(api): implement act and
session management endpoints` sitting between my own commits). Those are
harmless intermediate snapshots, not something I did deliberately or that
duplicates work -- the state at each of my own named commits is the one that
matters and each was verified to build, vet and (from block 6 onward) pass
tests before I made it.

Everything else -- the six pipeline steps, capture-before-execute, the
rollback walk's four properties, idempotency replay, the mutation cap, the
CLI, the tests -- was run and its actual output inspected, not assumed. The
verify script above is that evidence, not a summary of it.

## `git log --oneline`

```
70a1e71 test: end-to-end verify script; fix rogue/guarded cap collision
1a636fd docs: readme, decisions, and demo script
5ea9791 feat(web): session timeline dashboard
c15acfe feat(policy): blast-radius mutation cap
41ddde6 feat(engine): idempotency key replay
f501f16 test(engine): rollback ordering, rerun no-op, uncompensable, continue-past-failure
09a28ae feat(engine): reverse-order compensation walk
447e865 feat(demo): scripted rogue scenario
6956b79 feat(cli): session timeline renderer
3524ebb feat(engine): act pipeline with capture-before-execute
0311ab4 feat(api): implement act and session management endpoints
297155c feat(tools): tool registry, seeded world, three mutating tools
9f5b55f Add initial implementation of Unwind with API, ledger, and policy management
```

Tagged `v0.1.0` at `1a636fd`. This report's own commit lands after it, on top
of `70a1e71`.
