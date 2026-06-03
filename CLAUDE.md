# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`md` is a local markdown viewer that also doubles as a viewer for Claude Code
session transcripts. It's a single Go binary that serves an embedded web UI and
opens it in the browser. See `README.md` for the user-facing feature list.

## Commands

```bash
go build -o md .     # build the binary (REQUIRED after editing static/ — see below)
./md                 # serve the current directory
./md path/to/dir     # serve a directory
./md file.md         # serve a file's directory and open that file
./md -depth 5 dir    # scan/watch deeper than the default 3 levels
go vet ./...         # vet
gofmt -w main.go     # format
```

There is **no test suite** and no lint/CI configuration. Changes are verified by
running the app and exercising it in a browser (Playwright works well against
the printed `http://127.0.0.1:<port>` URL; note the server holds an open SSE
connection, so wait on `domcontentloaded` rather than `networkidle`).

## Critical: the frontend is embedded

`main.go` has `//go:embed static` — `static/index.html` is compiled into the
binary. **Editing the frontend has no effect until you `go build -o md .` and
restart.** This is the most common source of "my change didn't work."

The frontend has **no build step and no npm**: `static/index.html` is one
self-contained file (~2000 lines: HTML + CSS + one `<script>`). All libraries
(marked, highlight.js, mermaid, CodeMirror) load from CDNs at runtime.

The server binds a **random localhost port** (`127.0.0.1:0`) each launch. A
consequence worth remembering: the origin changes every run, so `localStorage`
(theme, full-width) effectively resets on each start.

## Architecture

Two largely independent feature areas share one server (`main.go`) and one page
(`static/index.html`):

### 1. Markdown file browsing
Rooted at the CLI-provided directory. `safeAbs(root, rel)` sandboxes every path
to that root. Endpoints: `/api/files` (tree), `/api/file` (GET reads, POST
writes — this is how Edit mode saves), `/api/search` (full-text, capped),
`/api/config` (initial file). `watchDir` uses fsnotify to watch the tree and
publishes debounced `reload` events over `/events` (SSE, fanned out by `Broker`).

Both the tree and the watcher are bounded: they skip dotfiles and `ignoredDirs`
(node_modules, vendor, target, …) and stop at `-depth` levels (default 3).
fsnotify holds one descriptor per watched directory on macOS, so without these
limits a large repo exhausts the default 256-FD soft limit and breaks HTTP
`accept()`. `raiseFDLimit()` also bumps the soft limit at startup, and the watch
count is capped at `softLimit - 256`.

### 2. Claude Code session viewing
Rooted at `~/.claude/projects` (`claudeProjectsDir`). Endpoints:
- `/api/sessions` — lists transcripts grouped by project, newest-first.
  Project directory names (`-Users-foo-bar`) are **ambiguous to decode**
  (paths can contain `-`), so the real `cwd` is read from inside each file
  rather than reconstructed from the name.
- `/api/session?id=<dir>/<file>.jsonl` — parses a transcript into turns.
  Parsing **streams the file line-by-line**, pre-filters on the substring
  `"role"` to skip huge non-message records cheaply, and emits: user *string*
  prompts, assistant `text` blocks, and one compact `tool` turn per `tool_use`
  (its `tool_result` is paired back in by `tool_use_id` and summarized into a
  one-line `result`). Working-directory prefixes (the record's `cwd` and the
  session root) are stripped from tool input/result so paths show relative.
  Thinking and snapshots are dropped. A 146 MB session collapses to a few MB. Results are cached by
  `path + mtime + size` (transcripts are append-only). The session title comes
  from `ai-title` records (`aiTitle` — Claude's generated name; the listing uses
  the first one in the head budget, the full parse uses the last/freshest),
  falling back to the first user prompt.
- `/api/watch-session?id=...` — points a single-file `sessionWatcher` at the
  open transcript and publishes `session-reload` events. Only one file is
  watched at a time (watching all of `~/.claude/projects` would spam events from
  every active session).

Compaction summaries are user records flagged `isCompactSummary`; the parser
surfaces this as `Turn.Compact` so the UI can label and format them specially.

### Frontend structure (`static/index.html`)
One script, no modules. Key pieces:
- **Two sidebar modes** (`switchMode`): Files (tree + file search) and Sessions
  (project→session list + client-side filter).
- **Markdown rendering** goes through `marked`; `enhance()` applies highlight.js
  and mermaid and is safe to call repeatedly (skips already-processed nodes).
  Custom highlight.js languages **Evident** and **SMT-LIB** are defined inline,
  with matching CodeMirror stream parsers for Edit mode.
- **Theme** (`preferredTheme`/`applyTheme`): defaults to `prefers-color-scheme`,
  follows live OS changes, and a manual toggle persists to localStorage and
  suppresses the live-follow.

### The windowed transcript renderer (`createSessionWindow`)
This is the most intricate code and the easiest to break. It renders a long
transcript as a **sliding window of `WINDOW_PAGES` (3) pages of `PAGE_SIZE` (20)
turns**, with top/bottom spacer `<div>`s standing in for un-mounted pages so the
scrollbar represents the whole conversation while the live DOM stays bounded.

Invariants that must be preserved when touching this code:
- Spacer heights come from a **fixed, text-length-based per-page estimate
  (`estPage`), computed once and never re-derived from measured DOM.** An earlier
  version averaged measured heights into a single mutable estimate; jumping into
  short pages collapsed it, shrank the scrollbar below the current scroll offset,
  and cascaded the view to the end. Don't reintroduce measured-height feedback
  into the global estimate.
- Viewport stability across mount/unmount comes from **anchoring on a surviving
  element's on-screen position** and correcting `scrollTop` by its shift.
- `adjust()` decides jump-vs-prefetch from the **real mounted geometry**, only
  re-centering when the viewport is actually over a spacer — so programmatic
  `scrollIntoView` (search jumps, reload restore) is never fought and snapped
  away.
- Position is preserved across live reloads by **topmost-visible-turn index**
  (stable across re-render/append), not scroll fraction (which maps differently
  depending on which pages are mounted).

In-transcript find (`setSearch`/`gotoMatch`) searches all turns in memory and
wraps matches in `<mark>` by walking text nodes only (keeps markup intact);
matches inside code spans may lose their mark when highlight.js re-tokenizes.
