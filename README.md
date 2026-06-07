# mdserver

A lightweight markdown file viewer with live reload.

## Features

- **File browser** — collapsible tree in the left sidebar
- **Live reload** — files update automatically when changed on disk
- **Mermaid diagrams** — rendered inline
- **Syntax highlighting** — for code blocks and source files (including Evident and SMT-LIB)
- **Edit mode** — click Edit, modify, click outside to save
- **Search** — full-text search across all files
- **Dark mode** — follows your OS appearance by default; toggle to override
- **Print-page ticks** — marks down the right edge of a document estimating where print pages would break
- **Claude Code session viewer** — browse and read `~/.claude/projects` transcripts as rendered markdown
- **Phone friendly** — the sidebar becomes a slide-over drawer on small screens; serve with `-listen` to reach it from your phone

## Phone access

By default the server binds a random localhost port, unreachable from other
devices. To read from your phone, bind a real port:

```bash
./md -listen :8080 .
```

The startup log prints the `http://<lan-ip>:8080` URL to open on your phone.

**Be careful where you do this:** the server lets anyone who can connect read
*and edit* the served directory (and read your Claude Code transcripts), so
only expose it on a network you trust. A safer option is a private overlay
network like [Tailscale](https://tailscale.com), which makes the URL reachable
from your devices only. On a phone the UI is read-only (no Edit mode); the
sidebar opens from the ☰ button.

## Session viewer

Switch the sidebar to the **Sessions** tab to browse your Claude Code
transcripts (the `.jsonl` files under `~/.claude/projects`), grouped by project
and sorted newest-first. Click one to read it as a clean, chat-style transcript.

- **Focused rendering** — prompts and replies render as markdown; each tool
  call collapses to a single line (name + input on the left, result on the
  right) that you can click to expand, and thinking/bookkeeping is filtered out
  server-side.
- **Windowed transcripts** — long sessions render as a sliding window of pages,
  so the live DOM stays small no matter how many thousands of messages there are.
  Use **Start**/**End** (or Home/End keys) to jump; a live readout shows your
  position ("msgs 120–140 of 5874").
- **Find in transcript** — search across every message (⌘/Ctrl+F), with match
  count and next/previous; jumps to and highlights each match.
- **Live updates** — an open transcript reloads as the session is written,
  staying pinned to the bottom while you watch a run, or holding your place
  otherwise.
- **Compaction summaries** — context-compaction summaries are detected, labeled,
  and rendered as markdown rather than raw text.
- **Shareable URLs** — the open session is reflected in the URL, so a refresh or
  bookmark reopens it.

## Goalposts

md can track **ground-truth progress toward a goal** — measures of real
artifacts (tests, files, endpoints, counts), never an agent's self-report.
A project opts in by adding `.goalpost/measures/` containing small
self-describing scripts; md discovers new scripts within a minute, runs each
on its own schedule (60s floor, backing off to the script's runtime or its
declared `period_s`), and stores a point whenever a value changes.

**To adopt it in your repo:** copy `.claude/skills/goalpost/` from this
repository into your project's `.claude/skills/` directory. Claude Code picks
the skill up automatically — then ask for `/goalpost author <your goal>` (or
just describe the goal; the skill's triggers cover authoring, amending when
the goal changes, and keeping the measurer independent of the agent doing the
work). The skill's `prompt.md` is the full spec: directory layout, JSON output
format, script time budgets, the artifact pattern for expensive checks, an
SLI/SLO-style decision tree, and a catalog of useful measures. It's also
paste-able into a bare session if you don't use skills.

## Example Diagram

```mermaid
graph TD
    A[Browser] -->|SSE| B[Go Server]
    B -->|File Watch| C[fsnotify]
    C -->|Events| B
    B -->|Serve Files| A
    A -->|Edit Save| B
```

## Code Example

```go
package main

import "fmt"

func main() {
    fmt.Println("Hello from mdserver!")
}
```

## Table Example

| Feature          | Status |
|------------------|--------|
| Live reload      | ✅     |
| Mermaid          | ✅     |
| Syntax highlight | ✅     |
| Dark mode        | ✅     |
| Edit mode        | ✅     |
| Search           | ✅     |
| Session viewer   | ✅     |
