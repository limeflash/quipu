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

## Two write paths, one memory

| | Agent saves (`mem_save`, upstream) | Auto-capture (this fork) |
|---|---|---|
| Quality per record | high — the agent knows *why* | lower — inferred from what was done |
| Coverage | only what the agent remembers to save | everything that changed state |
| Who pays | the main model (most expensive tokens) | a cheap cloud model, or Sonnet as fallback |
| Typical misses | long sessions, subagents, after compaction, a forgotten save | the reasoning behind a change |

Neither alone is enough: upstream already ships a 15-minute "MEMORY REMINDER"
nudge because agents forget to save; claude-mem showed what capture without
judgement turns into. So both stay, with agent saves ranking first:

- the compressor sees what the agent already saved in the session and does not
  restate it (titles in the prompt, `topic_key` upserts, engram's dedupe);
- auto observations are marked as such and rank below agent-written ones in
  search and context injection;
- the eval (milestone 2) measures which path actually answers questions.

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
| 1 | **Capture hook** for PostToolUse: native Go, no bash / `curl` / `jq` / `engram serve`; opt-in via `~/.engram/autocapture.json` | `cmd/engram-capture`, `internal/autocapture` ✅ |
| 2 | **Capture spool**: one redacted, truncated event per state-changing call; reads become content-free *touches* | `internal/autocapture` ✅, secrets: `internal/redact` ✅ |
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

### Hot path (decided in milestone 1, measured on Windows 11, 2026-10-07)

The PostToolUse hook writes one small JSON file into `~/.engram/spool/`
(unique name, temp-then-rename, no locks, no store) and exits; the compressor
ingests and deletes. It ships as a separate binary, `engram-capture`, because
on Windows process start cost tracks image size:

| What runs per tool call | p50 | p95 |
|---|---|---|
| `engram-capture` (3.9 MB), Edit | 41 ms | 46 ms |
| `engram-capture`, Bash with 200 KB output | 55 ms | 62 ms |
| `engram-capture`, Read (touch) | 38 ms | 41 ms |
| `engram hook claude-post-tool-use` (30 MB), Edit | 121 ms | 138 ms |
| `engram stats` — what opening the store costs | 588 ms | 657 ms |

Go's own init is 5 ms; the rest of the 30 MB binary's ~110 ms floor is the OS
loading and scanning the image (a 2 MB hello-world starts in 19 ms). The hook
is also `async`, so the agent never waits on it.

engram's own hooks (SessionStart, UserPromptSubmit, SessionEnd, SubagentStop)
stay as upstream ships them: they talk to `engram serve`, which owns session
identity, and none of them runs per tool call. They are replaced only if
measurements show they hurt.

## Not used in our setup (kept, untouched)

Cloud sync and dashboard, Obsidian export, OpenCode / Gemini / Pi plugins.
They stay in the tree for clean rebases; nothing here depends on them.

## Milestones

1. ✅ Capture hook + spool + touches + secret redaction; hot-path measurement
   (the eval harness moves to milestone 2 — there is nothing to evaluate until
   observations are generated)
2. ✅ Ollama runner + compressor + turn summaries (Claude Code) — results below
3. ✅ Claude Sonnet fallback + budget: `claude -p --model claude-sonnet-5-5
   --effort high --json-schema … --system-prompt … --tools "" --strict-mcp-config
   --disable-slash-commands --settings {"disableAllHooks":true}
   --no-session-persistence`, inherited `ANTHROPIC_*` / `CLAUDE*` scrubbed,
   token from `claude setup-token` in `~/.engram/claude.token`, 20 calls/hour.
   A bad credential disables only its own provider. `engram autocapture probe`
   checks every backend.
4. Codex capture
5. Project profile
6. `import claude-mem`; side-by-side quality run against claude-mem
7. Switch over; remove claude-mem, the Ollama proxy and the watchdog

## Milestone 2 results (2026-10-07, real sessions, deepseek-v4.1-flash)

`engram autocapture drain --dry-run` runs the model without writing, so prompt
changes are compared on the same spool before anything reaches memory.

| | First prompt | Tightened prompt | Real drain |
|---|---|---|---|
| events in | 112 | ~150 | ~170 |
| model calls | 10 | 13 | 15 |
| input tokens per call | ~7.8k | ~7.4k | ~7.4k |
| records | 59 | 54 | 69 → 66 rows (engram dedupe / topic upserts) |
| status snapshots ("tests pass", "committed X") | ~15% | ~0 | — |
| records carrying a `topic_key` | nearly all | decision/architecture/config/pattern only | — |

For comparison, claude-mem sent ~190k input tokens per call on the same
machine. What the review of run 1 changed:

- **topic_key** is honoured only for evolving types and only once per reply:
  the model put one key on three different records, and an engram topic upsert
  replaces the record in place.
- **Project per event**: an edit belongs to the repository its file lives in
  (and a shell command to the directory its leading `cd` enters); scratch and
  temp directories never become projects. One session had edited four repos.
- **Exact title repeats** within a session are dropped in code — the model
  restated "already recorded" items now and then.
- Status snapshots, commit/push/deploy notices and trivial compile errors are
  excluded in the prompt; at most 8 records per call.

Known gaps: session summaries need the Stop hook (installed after this run);
a formal junk audit waits for a few days of data.

### SessionStart injection

`engram autocapture context` (Claude Code SessionStart hook, ~1.2 s on
Windows, mostly opening the store) injects a fenced block of at most ~800
tokens, built so memories never cross over:

- **one project**: the one the session's working directory resolves to, by the
  same detection the compressor uses; an ambiguous or unknown directory gets
  nothing rather than a guess;
- **this session vs others**: a resumed or compacted session sees its own
  summary first; other sessions' summaries are labelled with their id and age
  and flagged "may still be running in parallel" when updated in the last 30
  minutes;
- then record titles only (`#id [type] title`), agent-written first, then
  decisions / fixes / config, then the rest.

Other projects are deliberately absent from the block; asked to look further,
the agent uses `mem_search(query, all_projects=true)` or `mem_list_projects`,
which the block mentions.

## Syncing with upstream

```bash
git fetch upstream
git rebase upstream/main
```
