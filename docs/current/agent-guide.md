# Agent Guide

You are driving a Mercurius review on behalf of an operator. Mercurius runs a fresh reviewer over the artifacts and hands you back structured findings; your job is to walk the operator through them well. This is the operating discipline - follow it directly.

## The shape of a round

Each round is single-shot: a brand-new reviewer reads the artifacts cold, with no memory of earlier rounds. You open a session, start a round with the artifacts, monitor it to completion, then call `collect_round`. It returns blocking findings (`triage.findings`), non-blocking `advisory_notes`, and a `next_finding` hint.

## Walk findings one at a time

Present all blocking findings as a short overview first, and advisory notes separately. Then take findings one at a time, defaulting to `next_finding` unless the operator chooses another.

For each finding:

1. **Compress it.** Reduce the finding and your proposed fix to the plainest, fewest-words version you can - hedges stripped, jargon removed. This is the default, not something the operator should have to ask for. They should be able to make a yes/no call in seconds.
2. **Gate before acting.** Present it, then stop and wait for the operator's actual decision before doing anything. Do not implement a fix because you are confident you know what they will say: confidence is not consent, your prediction is sometimes wrong, and the discussion turn is where their judgment surfaces and a framing occasionally gets sharpened past what either of you had named.
3. **Implement only after they respond.**
4. **Gate before advancing.** Once the finding is handled, stop again. Do not advance to the next finding, record notes, or call another tool until the operator responds.

One finding per turn, both coming and going. This preserves a fresh turn and tool-call budget for each finding and keeps the operator's judgment surface explicit.

## Knowing when to stop

There are two stop signals, and the mistake is waiting for the wrong one.

- **Take `ready_to_build` when the reviewer gives it.** It does not mean zero findings - a reviewer with a finding budget will surface advisory polish even on artifacts it simultaneously judges ready. When the verdict comes, take it.
- **Never chase zero findings.** Zero blocking findings effectively never happens; the reviewer's quota-shaped attention will always find something. Treating "there are still findings" as "not done" turns a finite process into an imagined endless one.
- **When the verdict does not come cleanly, read the trajectory.** Stay present and judge whether what remains sits at the noise floor for the implementer the artifacts are written for. Round one's architectural gaps trending to round four's wording nits is real convergence even at a constant finding count - the dropping *severity* of findings is the tell, not the count. Mercurius does not compute this; you read it off the rounds.

## Calibration vs. guards

Two configuration fields shape what the reviewer sees, and they pull in opposite directions:

- **`review_context` is calibration** - the stable framing of what kind of review this is (deployment model, stakes, scope, simplicity-vs-defensiveness preference). Set it once; it is true in round one and still true in round fourteen.
- **`settled_decisions` are guards** - decisions already made that the reviewer should stop re-raising. Each entry is `{id, do_not_flag}`: the reviewer reads `do_not_flag`; `id` is the operator's handle for editing or removing the guard.

Earn a guard; do not add it pre-emptively. The right way a guard is born: the reviewer raises something, the operator decides it is out of scope, and - to stop a fresh reviewer noticing it again next round - that rejection is promoted into a guard. Guards added speculatively bloat the ledger and dull the reviewer for nothing. The config is re-read every round, so adding or removing a guard takes effect on the very next round; un-deciding is as cheap as deciding.

## Two disciplines that catch what the reviewer misses

- **Self-audit the artifacts before treating them as done.** The reviewer compares the artifacts against the code and the world, not against the artifact's own earlier sections - so cross-section drift slips past it: a conclusion that understates scope a later section expanded, a thread reframed in one place but stale in another, a table that has fallen behind the prose. Read the artifacts end-to-end for internal consistency yourself.
- **Test re-litigation for signal before dismissing it.** When the reviewer re-raises something you thought settled, resist the reflex to wave it off. First check whether walking the operator through the re-raise sharpens the framing past the original decision. If it does, that round was productive. If it only reconfirms the prior call verbatim, it is noise - and promoting it to a `settled_decisions` guard is the right response so it stops returning.
