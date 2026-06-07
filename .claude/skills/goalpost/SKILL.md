---
name: goalpost
description: >-
  Author or amend independent, programmatic "goalpost" measure scripts that
  track ground-truth progress toward a goal — a small set of gates
  (definition-of-done) and trends (burndown) that emit a common JSON format
  and measure real artifacts (tests, files, endpoints, counts), never the
  agent's self-report. TRIGGERS — invoke when any of these holds — (1) the
  user asks to measure progress toward a goal without trusting agent self-
  reports; (2) you are starting goal-directed work and no .goalpost/ exists
  for the active goal; (3) you NOTICE THE GOAL HAS CHANGED — the stated goal,
  plan, or scope no longer matches what .goalpost/measures/ measures: a target
  is obsolete, a measure inspects artifacts that no longer exist, done-
  conditions exist that nothing measures, or the user redefined success (use
  `amend`, which records the move in CHANGELOG.md). INDEPENDENCE — the
  measurer must not be the doer: if you are the agent doing the work, do not
  author/amend measures inline; dispatch this skill to a fresh subagent given
  only the goal statement and repo (never your plan or progress notes), then
  continue working while a human reviews.
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
