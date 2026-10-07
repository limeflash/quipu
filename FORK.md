# limeflash/engram — fork plan

This fork adds **automatic capture** to engram: tool activity from Claude Code
and Codex is compressed into engram observations by a cheap cloud model
(native **Ollama Cloud**), falling back to **Claude Sonnet 5.5 (effort high)**
through the user's own Claude Code subscription when the Ollama quota runs
out. Everything engram already does — `mem_save` with `topic_key` upserts,
3-layer search, dedupe, conflict judging, TUI — stays and keeps working.

Upstream engram trusts the agent to call `mem_save`. That works when the agent
remembers to, and misses everything it doesn't. This fork keeps that path and
adds a second one that needs no discipline from the agent.

Background and the full rationale (failures observed with claude-mem, a survey
of 25+ memory projects): [docs/fork/DESIGN.md](docs/fork/DESIGN.md).

## Ground rules

1. **Additive.** New behaviour lives in new packages and new migrations.
   Core files are touched only at seams (one call in `cmdMCP`, one in the
   installer). This keeps `git rebase upstream/main` cheap.
2. **Off unless configured.** Without an Ollama key or a Claude token the
   fork behaves exactly like upstream.
3. **Generic fixes go upstream** (issue first, per CONTRIBUTING.md) —
   e.g. native Go hooks on Windows. Fork-only: the auto-capture pipeline.

## What engram already gives us

| Need | engram |
|---|---|
| single Go binary, SQLite + FTS5 (incl. trigram), no CGO | `internal/store`, `modernc.org/sqlite` |
| MCP stdio server, progressive disclosure | `internal/mcp`: `mem_search` → `mem_timeline` → `mem_get_observation` |
| explicit memories that upsert | `mem_save` + `topic_key` (`revision_count`) |
| exact dedupe in a rolling window | hash + project + scope + type + title |
| conflict / supersession judging | `mem_judge`, `mem_compare`, `internal/store/relations.go` |
| project identity incl. worktrees | `internal/project` (git binding, canonical worktree) |
| lease-guarded background goroutine inside `engram mcp` | `internal/cloud/autosync` + `Store.AcquireSyncLease(targetKey, …)` |
| Claude Code + Codex integration | `plugin/claude-code`, `plugin/codex`, `engram setup` |
| `claude -p` runner | `internal/llm.ClaudeRunner` (haiku, for conflict judging) |

## What this fork adds

| # | Feature | Where |
|---|---|---|
| 1 | **Native Go hooks** for every Claude Code event (no bash, no `curl`, no `jq`, no `engram serve` needed for capture) | `cmd/engram` `hook claude-*`, extend the existing `hook` dispatcher |
| 2 | **Capture spool**: PostToolUse writes one redacted, truncated event; reads become content-free *touches* | `internal/autocapture/spool` |
| 3 | **Compressor**: batches a session's events into observations via `Store.AddObservation`; runs as a lease-guarded goroutine in `engram mcp` (and `engram serve`), like autosync | `internal/autocapture` |
| 4 | **Ollama Cloud runner** (native `/api/chat`), local JSON-schema validation + one repair retry, 429/410 handling, model chain | `internal/llm/ollama.go` |
| 5 | **Claude fallback**: `ClaudeRunner` gains model/effort config (`claude-sonnet-5-5`, `high`), `--json-schema`, env scrubbing, `claude setup-token` auth, hourly budget | `internal/llm/claude.go` |
| 6 | **Turn summaries** written automatically on Stop (same Goal / Discoveries / Accomplished / Next Steps / Files shape as `mem_session_summary`) | `internal/autocapture` |
| 7 | **Project profile**: stack from manifests, commands from successful runs, hot files from touches | `internal/autocapture/profile` |
| 8 | **Codex capture**: `exec` events, edits detected as `apply_patch`, read-only commands dropped | `plugin/codex` + `hook codex-post-tool-use` |
| 9 | `engram import claude-mem` | `cmd/engram/import_claudemem.go` |
| 10 | Eval harness: recall set, junk audit, token cost, hook p95 | `internal/autocapture/eval` |

The conflict judge (`ENGRAM_AGENT_CLI`) also gets the Ollama runner, so
judging stops costing Claude Haiku calls.

### Hot path decision (milestone 1)

Two candidates for what a PostToolUse hook does; pick by measurement on
Windows, p95 target < 50 ms:

- **A — spool file**: write one small JSON file into `~/.engram/spool/`
  (unique name, no locks, no DB open). The compressor ingests and deletes.
- **B — direct insert**: `store.New` + one INSERT. Simpler, but engram's store
  open runs migration checks; measure before choosing.

## Not used in our setup (kept, untouched)

Cloud sync and dashboard, Obsidian export, OpenCode / Gemini / Pi plugins.
They stay in the tree for clean rebases; nothing here depends on them.

## Milestones

1. Native Claude Code hooks + spool + touches; hot-path measurement; eval harness skeleton
2. Ollama runner + compressor + turn summaries (Claude Code)
3. Claude Sonnet fallback + budget
4. Codex capture
5. Project profile
6. `import claude-mem`; side-by-side quality run against claude-mem
7. Switch over; remove claude-mem, the Ollama proxy and the watchdog

## Syncing with upstream

```bash
git fetch upstream
git rebase upstream/main
```
