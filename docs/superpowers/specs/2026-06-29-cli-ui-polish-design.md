# bbctl CLI UI/UX Polish — Design

**Date:** 2026-06-29
**Status:** Approved (brainstorming)
**Scope:** `ec2ctl` (the `bbctl` CLI). Backend (`ec2ctl-backend`) is out of scope.

## Goal

Make `bbctl` feel like a production-grade tool engineers enjoy using, without
rewriting the interactive flows. Polish the **existing** stack (cobra +
`chzyer/readline` + `ktr0731/go-fuzzyfinder` + raw ANSI) by introducing a
shared visual layer and applying it across the surfaces that currently feel
flat or give no feedback.

## Decisions (locked during brainstorming)

- **Approach:** Polish the current stack. No Bubble Tea event loop, no REPL
  rewrite. Add styling only.
- **Surfaces:** All of — loading & progress, status & errors, tables &
  details, welcome & prompt, plus the RDS REPL prompt.
- **Visual tone:** Refined brand — evolve the existing red (`38;5;167`) + cyan
  into a disciplined palette. Professional, not flashy.
- **Styling library:** `github.com/charmbracelet/lipgloss` (styling only;
  coexists with readline/fuzzyfinder). Chosen over hand-rolled ANSI to avoid
  re-inventing capability detection and style composition.

## Architecture: the `ui` package (foundation)

All hardcoded ANSI currently lives scattered across `welcome.go`,
`interactive.go`, `run.go`, `db_display.go`. The keystone of this work is
centralizing it.

**New package `ec2ctl/internal/ui/`** (small, focused files):

- `theme.go` — the single source of color truth. Named semantic tokens built
  on Lip Gloss styles: `Brand` (red 167), `Accent` (cyan), `Success`,
  `Warning`, `Danger`, `Muted`, `Dim`, `Text`. One change repalettes the tool.
- `status.go` — styled primitives returning strings:
  `Success`, `Warn`, `Err`, `Info`, `Arrow` with consistent glyphs
  (`✔ ⚠ ✘ → ℹ`). Replaces ad-hoc `fmt.Printf("⚠️ …")` calls.
- `caps.go` — **terminal capability detection** (production-grade requirement):
  - Honors `NO_COLOR` (industry standard) → strip all color.
  - Detects non-TTY / piped stdout → strip all ANSI and disable spinners so
    `bbctl run … | grep` stays byte-correct.
  - Degrades Unicode glyphs to ASCII (`✔` → `[OK]`, braille → `|/-\`) when the
    terminal can't render them.
- `spinner.go` — lightweight goroutine spinner (braille frames, ASCII
  fallback), brand-red, **writes to stderr** so it never pollutes stdout.
  API: `sp := ui.NewSpinner(msg); sp.Start(); defer sp.Stop()` with
  `StopOK`/`StopErr` leaving a ✔/✘ summary line.
- `progress.go` — determinate progress bar
  (`████████░░░░ 62%  4.1/6.6 MB`) for file transfers.
- `card.go` / `table.go` — bordered key/value cards and styled tables (shared
  by ticket card, instance details, RDS results).

**Design principles:** functions return strings (testable without a TTY);
capability flags resolved once at startup; all rendering routes through `ui`.

## Surface specs

### 1. Loading & progress
- `ui.NewSpinner` wired into the slow paths:
  - `ec2picker.LoadAll` (instance load).
  - Concurrent RDS fan-out in `runInteractiveRDS`.
  - Replaces the `Loading instances...` / `\r%-30s\r` hacks in `interactive.go`.
- `ui/progress.go` bar hooked into `runUploadSession` / `runDownloadSession`,
  which currently have zero feedback.

### 2. Status & errors
- `run.go::handleAPIError` and the `⚠️`/`⏱` lines → `ui.Err/Warn/Info`.
- Ticket-approval output (`runCommandDirect` ticket block, and
  `db_display.go::renderRestricted`) → a bordered card:
  ```
  ╭─ Approval required ──────────────────────╮
  │ Ticket   REQ-4821                        │
  │ Status   awaiting manager approval       │
  │ Re-run   bbctl run i-0abc --ticket … -- … │
  ╰──────────────────────────────────────────╯
  ```

### 3. Tables & details
- `db_display.go::renderTable` → Lip Gloss table styling: brand-red bold
  headers, dim borders, dimmed `NULL`, right-aligned numeric columns. Keep the
  MySQL-familiar shape; plain ASCII when piped/non-TTY.
- `interactive.go::printInstanceDetails` → styled key/value card matching the
  ticket card.

### 4. Welcome & prompt
- `welcome.go`: keep ASCII logo; move its raw ANSI consts into `ui.theme`.
  Wire the already-defined-but-empty `InstanceCount` / `AccountCount` /
  `CacheAge` fields with live data after instance load.
- `shell.FormatPrompt`: add color — brand-red instance, cyan dir, green
  `[approved]` badge — all gated through `ui/caps`.

### 5. RDS REPL modernization
- `db_connect.go:270` — replace `mysql> ` with a branded, context-aware prompt:
  `bbctl-db · <db-identifier> ❯ ` (brand-red name, cyan `❯`).
- Continuation prompt (`db_connect.go:285`) `    -> ` → `  … `.
- Green `[approved]` badge when a ticket is active (mirrors EC2 shell prompt).
- Connect banner (`db_connect.go:250-252`) → `ui.Success` line + compact
  details card (host · version · session).

## Cross-cutting guarantees (production-grade)
- **TTY-safe everywhere:** piped/non-interactive output is clean ASCII, no
  escape codes, no spinners.
- **`NO_COLOR` honored.**
- **Incremental & non-breaking:** `ui` package lands first; each surface
  migrates independently. No big-bang rewrite.

## Testing
- `ui` functions return strings → table-driven unit tests assert content and
  that capability flags strip styling (`NO_COLOR`, non-TTY).
- Run with `-race` (spinner uses a goroutine).
- Target 80%+ coverage on the `ui` package.

## Out of scope (YAGNI)
- No Bubble Tea interactive dashboard.
- No theme-switching / user-configurable palettes.
- No changes to backend, auth, or command classification logic.

## New dependency
- `github.com/charmbracelet/lipgloss` (+ its small transitive set). Styling
  only; no event loop.

## Affected files
- New: `internal/ui/{theme,status,caps,spinner,progress,card,table}.go` (+ tests).
- Modified: `internal/shell/welcome.go`, `internal/shell/shell.go`
  (`FormatPrompt`), `internal/ec2/picker.go` (optional accent on headers),
  `commands/interactive.go`, `commands/run.go`, `commands/db_display.go`,
  `commands/db_connect.go`, file-transfer sessions
  (`commands/upload.go`, `commands/download.go`).
