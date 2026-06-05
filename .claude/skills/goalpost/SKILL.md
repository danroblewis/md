---
name: goalpost
description: Author or amend independent, programmatic "goalpost" measure scripts that track ground-truth progress toward a goal — a small set of gates (definition-of-done) and trends (burndown) that emit a common JSON format and measure real artifacts (tests, files, endpoints, counts), never the agent's self-report. Use when the user wants to measure progress against a goal without trusting what an agent claims about its own progress, or when a goal has changed and the measures need revising. Run in a session separate from the one doing the work — the measurer must be independent of the doer.
---

# Goalpost

Build trustworthy rulers for "how far toward the goal," independent of any agent's
self-report. The full methodology, hard rules, output format, and an example live
in **`prompt.md`** alongside this file — read it and follow it exactly. (That same
`prompt.md` is paste-able into a bare session, which is the non-skill path.)

## Dispatch on `$ARGUMENTS`

- `author <goal text or path to goal>` — create a fresh `.goalpost/` for this goal.
- `amend <what changed about the goal>` — revise existing measures; record the move
  in `.goalpost/CHANGELOG.md` so moving the goalposts stays auditable.
- empty / just a goal — treat as `author`.

If no goal is supplied and none can be inferred, ask the user for it (or for a
`/goal` statement) before doing anything.

## Stance (do not skip — this is what makes the output trustworthy)

You are the **measurer, not the doer.** Stay context-starved on purpose: work from
the goal statement and the repo's artifacts only. Do **not** read the doer's plan,
TODOs, or narration, and do **not** assume the work is finished. For every measure
ask: *what evidence would convince a skeptic — and what would catch this not being
done?* A measure that cannot fail is not a measure.

Produce the scripts and `REVIEW.md`, then **stop and hand off for human review** —
do not lock hashes. md refuses to run measures until a human approves them.
