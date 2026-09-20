# I Tried to Delete My Own Audit Trail. AWS Said No.

I want to start with the moment that actually convinced me this project was working, because it wasn't a green checkmark. It was a `403`.

I'd just built a feature that archives an AI agent's action history to Amazon S3 with Object Lock turned on — the pitch being "even if something goes wrong, this record can't be tampered with." That's an easy sentence to write and a much harder thing to prove. So instead of trusting the checkbox, I logged in as an IAM user whose policy *technically* grants the permission to override the lock, and tried to hard-delete the exact locked version of an object.

```
operation error S3: DeleteObject, https response error StatusCode: 403,
api error AccessDenied: Access Denied because object protected by object lock.
```

Denied. Not because the user lacked permission — it had `s3:BypassGovernanceRetention` — but because GOVERNANCE mode requires you to *explicitly* pass an override flag on that specific request. Having the permission isn't enough. You have to deliberately reach for it. That's the difference between "this can't be deleted" and "this can't be deleted by accident," and it's a much better guarantee than the one I started out claiming.

That test is also, I think, the whole idea of the project in miniature: **don't just say a safety mechanism holds — try to break it, on purpose, and show your work.**

## The problem nobody built a safety net for

Every application that lets a human delete or change something has some kind of net underneath: an "Are you sure?" dialog, a trash bin, an undo toast, a confirmation email with a cancel link. None of that is backend logic — it's UI, made of pixels, made for a human who might click the wrong thing and needs five seconds to reconsider.

AI agents don't get any of it. An agent with API access can fire two hundred destructive calls in the time it takes you to read this sentence, and there's no dialog box in the loop to catch it. The safety net for API side effects was never built because until recently, the caller could be trusted to read the screen before clicking.

That trust assumption is gone now, and the tooling hasn't caught up. So I built **Unwind**: a write-ahead ledger and compensation engine that sits between an agent and the real tools it calls. Every action is recorded *before* it executes, capped by a live policy gate, and reversible after — except for the one class of action that genuinely can't be undone, where the answer isn't a better rollback, it's a policy that stops it before it ever runs.

## What it actually does

Three real tools, each with a declared reversibility class:

| Tool | Class | What compensation means |
|---|---|---|
| `cancel_subscription` | reversible | the prior row is captured and reinstated exactly |
| `issue_refund` | partial | a reversing ledger entry is posted, invoice flagged contested |
| `transfer_funds` | irreversible | there's nothing to reverse — `Capture` is a no-op |

Every call goes through the same six-step pipeline: write the intent down before anything runs, check idempotency so nothing double-fires, evaluate the policy gate, capture whatever state you'd need to undo it, execute, commit. The whole thing is Go and SQLite — no framework, no ORM — and the frontend is one embedded HTML file talking to it over a live event stream, so a demo of this is a demo of real HTTP calls landing in a real database, not a slideshow pretending to be one.

The part I keep coming back to is `transfer_funds`. It's in the project on purpose, specifically to make an argument concrete: rollback can only replay information that was captured *before* something ran. An irreversible action, by definition, destroys that information the instant it executes. Asking a rollback engine to fix that after the fact is asking it to invent history that was never written down. The only point where you can still change the outcome is before it runs — which is what a policy gate is *for*. That's why this project has both a compensation engine and a policy engine instead of just one: they're not two features bolted together, they're two halves of the same answer, split along the one axis that actually matters — can this be undone, yes or no.

## Where AWS actually did the work

The ledger's biggest structural weakness is that it lives on the same machine as the agent whose blast radius it's supposed to be recording. Anything that can reach the process can reach the file. So every session, once it's settled, gets archived to **Amazon S3** with **Object Lock** and **Versioning** enabled — using the AWS SDK for Go v2 directly against the real API, no wrapper on top.

The default retention I set is GOVERNANCE, not COMPLIANCE, and that was a deliberate call, not a lesser one. COMPLIANCE mode is unbreakable even by the account root — genuinely permanent — and I was still actively building and testing this feature. Locking myself out of my own mistakes while I was still capable of making them would have been the wrong trade. GOVERNANCE with a scoped IAM policy gets you real protection against tampering while leaving a documented, deliberate escape hatch for the one person who's supposed to have it.

The IAM setup follows the same logic as the product: the credentials this project runs on are scoped to exactly S3 and Bedrock, nothing broader. When I later deployed the whole thing to an **EC2** instance so it'd be reachable as a real public demo, I made a point of *not* copying those AWS credentials onto that box — I'd since had to add `AmazonEC2FullAccess` to the same IAM user, and putting that key on a public-facing server would mean anyone who ever read that disk could spin up or tear down infrastructure in my account. So the deployed instance only carries the one API key it actually needs for the live-agent demo, and archiving to S3 stays a local-only capability, where the broader key is safe. It's the same "don't grant more blast radius than the task in front of you needs" argument, just applied to my own deployment instead of the agent's.

## The two things that broke, and what they taught me

**The region mismatch.** My S3 bucket lived in `eu-north-1`; my first client was configured for `us-east-1`. The error I got back was a technically-correct `301 PermanentRedirect` that told me the request went to the wrong endpoint — but not *which* region the bucket was actually in. I had to make a separate `GetBucketLocation` call just to find out. A one-line hint in that error message would have saved a round trip that I suspect a lot of people hit on their very first S3 request.

**The Gemini quota exhaustion.** The project also supports a real LLM — Gemini or Claude — actually deciding what actions to take, through the identical tool-use pipeline. The moment I pointed the deployed EC2 instance at it, it came back with `429 RESOURCE_EXHAUSTED`. Turned out `gemini-2.5-flash`'s free tier caps at 20 requests a day, and my own testing earlier that session had already spent the whole budget before the public server took its first real request. The fix was straightforward once I stopped assuming and checked: `gemini-2.5-flash-lite` carries a completely separate quota bucket, and I confirmed against the live API — not from memory, not from docs — that it supports the identical function-calling shape before swapping the model over. Five minutes, once I stopped guessing and started testing.

Neither of those is a dramatic failure. They're just the two moments where the real system pushed back on an assumption, and both times the fix was the same: stop reasoning about what *should* happen and go check what *actually* happens.

## Why this mattered to me

I think the honest version of "how did you use AWS" isn't a list of service names — it's what you were forced to learn by actually running your claims against the real thing instead of trusting the console. Object Lock's guarantee turned out to be stronger and more specific than the marketing copy: not "nobody can delete this," but "this can't be deleted without a deliberate, logged decision to do it." I only know that because I tried to break it and watched it hold.

That's the standard I'd want anyone building safety tooling — for agents or otherwise — to hold themselves to. Don't ship a claim you haven't tried to disprove.

---

*Unwind is open source. Code, architecture notes, and the full decision log (every ambiguity I hit and the reasoning behind each call) are on GitHub: [github.com/DHSY-ishere/Unwind](https://github.com/DHSY-ishere/Unwind).*
