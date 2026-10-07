# limeflash/quipu — engram fork plan

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
| 9 | `engram import claude-mem` | `cmd/engram/import_claudemem.go` ✅ |
| 10 | Eval: recall test (48 questions from real sessions, Haiku answers, Sonnet judge), blind junk audit, token cost, hook p95 | FORK.md results; data local in `~/.engram/eval` |

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

   Chain order: Ollama models → **Codex** (`codex exec -m gpt-6-luna -c
   model_reasoning_effort=max --ignore-user-config --disable hooks --ephemeral
   -s read-only --json --output-schema … -o …`, the user's ChatGPT plan, 20
   calls/hour, native `codex.exe` behind the npm shim, strict-mode schema with
   a nullable summary) → **Claude** as the last resort. Probe on 2026-10-07:
   deepseek 0.9 s / 639 in, glm 6.8 s / 628 in, Codex 10.3 s / 16.8k in,
   Sonnet 8.8 s / 8k in.
4. ✅ Codex capture. Codex hooks are Claude-compatible (its engine is literally
   `ClaudeHooksEngine`): exec arrives as `Bash` with `tool_input.command`
   and a string `tool_response`; edits as `apply_patch` with the patch text in
   `tool_input.command`. `engram-capture codex` labels the events; the patch's
   first `*** Update/Add/Delete File:` path becomes the absolute target.
   Codex runs hook commands through `cmd.exe` by default, so the commands have
   no quotes or spaces; Stop is synchronous because `codex exec` exits before
   an async Stop hook finishes. New hooks need a one-time trust approval in
   Codex. Verified end to end with `codex exec` (prompt, Bash event, read-only
   Bash as touch, apply_patch, turn end). Config: `docs/fork/codex-config.toml`;
   Claude Code: `docs/fork/claude-settings.json`.

   Project attribution per event now tries, in order: the written file, a
   `cd`/`Set-Location`/`Push-Location` anywhere in a shell command, `git -C`,
   absolute paths in its arguments, then the shell's own working directory —
   which Claude Code reports per call. Only repository-backed answers count.
5. ✅ Project profile: one `project_profile` record per repository (topic key
   `profile/<project>`), rebuilt at most daily or when a manifest changes:
   stack from manifests (go.mod, package.json, Gradle, Cargo, csproj,
   Dockerfile, wrangler, …; line-based, `golang.org/x/*` and indirect deps
   skipped), languages and layout from one tree walk, commands actually run
   and most-touched files from the activity log (`touches.jsonl`, now with
   per-event project and `read`/`edit`/`cmd` kinds; worktree paths folded
   into the repository's), and a 3–6 sentence architecture overview — the only
   model call. SessionStart shows its one-line form;
   `engram autocapture profile [dir]` rebuilds it on demand.
6. ✅ `engram import claude-mem [--db PATH] [--root DIR]... [--map FROM=TO]...
   [--dry-run]`: observations, turn summaries and prompts through
   `Store.Import` with stable sync ids (`cm-obs-N`, `cm-sum-N`, `cm-prompt-N`),
   so a rerun adds nothing. Worktree suffixes and paths collapse to the
   repository; `--root` resolves names through engram's own detection
   (inspect only, no binding written); leaked field names (`**title**: …`)
   stripped, broken types mapped, records with only file lists kept;
   everything redacted again; rows after the first auto-captured record of
   the same session skipped. Imported records carry `tool_name = claude-mem`:
   they rank with captured ones at session start, notes on code that was only
   read rank last. Results and the comparison below.
7. ✅ Switched over (2026-10-07): the claude-mem plugin and its marketplace
   are uninstalled, the `claude-mem-ollama-proxy` and `claude-mem-watchdog`
   scheduled tasks unregistered, no claude-mem process left. Kept: the
   codebase-memory-mcp daemon, which a SessionStart hook keeps alive, not the
   watchdog. claude-mem's one hand-written note went into engram first.

## Milestone 6 results (2026-10-07)

Import of `~/.claude-mem/claude-mem.db` (735 MB): 49,071 observations, 1,509
turn summaries, 3,108 prompts, 38 sessions in 3.5 minutes; 8 records with no
text and no files skipped, 366 broken types mapped, 5,011 leaked field
prefixes stripped, 328 secrets redacted. 30 claude-mem project names became 22
engram projects (`cs2farm-fresh` + 2 worktrees → `cs2farm`, `geo-guessr` →
`tochka`, `deadlock-mods` → `soul-broker`, …).

Running the redactor over 49k model-written records found false positives
that also hit live capture, all fixed: a plain word followed by more words on
the same line is prose, not a value ("Password: encrypted by the store");
placeholders already in the input are kept instead of nesting; `curl -u` needs
a real user name (`date -u +%H:%M:%S`); `--x` values are CSS properties or
flags. Redactions went from 818 to 328; what remains is mostly real (server
and SSH passwords, API keys, access tokens).

Side by side on the same four sessions of 2026-10-07 — claude-mem until it
was switched off, engram after; the two never ran at the same time:

| | claude-mem | engram |
|---|---|---|
| records per hour of work | 270–330 | 36–66 |
| share of `discovery` (notes on read code) | 65% | 29% |
| model calls that day | 2,264 | 112 |
| input tokens that day | 343.6 M (avg 152k/call, max 867k) | 0.72 M (avg 6.4k/call) |
| input tokens on 2026-10-05 | 2.7 B | — |
| blind audit, 60 random records each: useful / noise / duplicate | 14 / 45 / 1 | 45 / 11 / 4 |
| junk share (target < 20%) | 77% | 25% |

The audit: a Sonnet 5.5 judge saw the two samples as lists A and B with
record bodies stripped of each system's field labels, one bar for both.
claude-mem's noise is retold code and mockups, status ("tests pass",
"committed", "agent launched") and navigation; engram's is minor refactors
and UI polish recorded without a cause, plus the same bug or measurement
written twice by different chunks of one session (title dedupe only catches
exact repeats).

Per hour that is still ~70 useful claude-mem records against ~37 engram ones,
buried under three junk records each and at ~500× the input tokens. Whether
engram misses facts that matter is what the recall set (milestone 2's eval
harness, still to build) has to answer.

### Cross-chunk continuations (2026-10-08)

Three of the four duplicate pairs shared almost no words ("Proxy disk window
keeps chunks older than head-3" one chunk, "Stale demand re-fetched evicted
chunk" — its cause — the next), so title matching cannot catch them. The
model now sees each already-recorded record of the session as `#id [type]
title — start of what it says`, and a record that continues one of them —
the same bug, measurement, decision or component, now with a cause, a fix or
a better number — carries `updates: <id>` and is written as the complete
merged record over that one (`UpdateObservation`, so the old text stays in the
version history). Only records auto-capture wrote in the session qualify; the
agent's own saves and imported history are never overwritten, and an unknown
id falls back to a new record. On the audit case all four backends set
`updates` and wrote one merged record; on an unrelated change in the same
session (deepseek, glm) they did not.

The same day: the compressor used to look for the session summary and the
"already recorded" list among the session's 200 *oldest* rows, which in a
session with imported claude-mem history are months old — the summary was
never found and restarted every turn. It now reads the session most recently
updated first (`RecentSessionObservations`).

### Recall test (2026-10-08)

Does quipu keep the facts a later session needs? Six real sessions captured
by quipu (Android TV app, this fork, a VPN panel, a Steam trading panel, a home
automation setup); their user–agent dialogue after capture began — tool output
removed — is the ground truth. A Sonnet 5.5 agent per session wrote 8–10
questions the owner would plausibly ask later (decisions and why, bug causes
and fixes, config and paths, gotchas, unfinished work), each with key facts
and a quote: 48 in all. Haiku 5.5 agents answered them from memory only —
`mem_search` / `mem_get_observation`, at most three calls a question, no file
access — and a Sonnet 5.5 judge graded each answer and, for every miss,
searched memory itself to tell *not stored* from *stored but not found*.
Questions, answers and grades stay on the machine (`~/.engram/eval/`); they
are about private projects.

| | correct | partial | wrong | not found |
|---|---|---|---|---|
| first run | 12 | 14 | 5 | 17 |
| after the fixes below | **21** | 20 | **0** | 7 |

Whether the fact was stored at all (judge's search): **not stored 0 of 48**
in both runs; 7 stored only in part (a number, a per-device setting, one step
of a procedure). The misses were nearly all on the way out:

- **search**: the default all-terms match returned nothing as soon as one of
  the agent's keywords was not in the record. `mem_search` and `engram search`
  now fall back to any-term BM25 when all-terms finds nothing
  (`store.AllThenAny`; `Store.Search` keeps upstream's strict default because
  callers use it to prove absence). Retrieval misses went 22 → 14;
- **project**: 6 facts sit under the working directory's project instead of
  the repository the work was about — decisions made in conversation, with no
  file edited, follow the shell's cwd. The SessionStart block points the agent
  to `all_projects=true`; the answering agent here used it only as a last call;
- **the answering agent**: the remaining retrieval misses are answers built on
  a record's preview or an older record while the newer one was one call away.

The test also contaminated what it measured: the agents wrote the question
and answer files, the capture hook saw those writes as work, and the
compressor turned the answer key into memories. The hook now drops every
event whose target lies inside the data directory, and those records were
soft-deleted before the second run. Discussing answers in a captured session
leaks them the same way — a rerun needs fresh questions.

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
