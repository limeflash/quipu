# Auto-capture design

> Written first as a standalone design ("quipu") before the decision to build
> on engram instead. [FORK.md](../../FORK.md) maps it onto engram's packages.
>
> **Still applies:** §2 lessons and survey, §4 capture, §5.3 project profile,
> §6 processing, §7 providers, §11 evaluation.
> **Superseded by engram's own design:** §3 shape (engram has engram mcp /
> engram serve / engram setup), §5.1 tables and §8 MCP tools (engram's
> store, mem_* tools and 	opic_key upserts), §9 integration (engram's
> plugins + native hooks from FORK.md), §12 layout, §13 milestones (see
> FORK.md).

## 1. Goals and non-goals

**Goals**

1. Persistent memory across sessions for **Claude Code** and **Codex CLI**,
   shared between them (a decision made in Codex is visible in Claude Code).
2. **One static Go binary.** No Node, Bun, Python, Chroma or Docker. Windows,
   macOS and Linux are first-class; Windows is the platform the design is
   tested on first, because that is where everything before it broke.
3. **Nothing heavy on the hot path.** A tool call costs one short-lived process
   that does one SQLite insert and exits. No network, no worker round-trip.
4. **No daemon, no port, no watchdog.** Background work runs only while an agent
   session is open. With no agent running, quipu runs nothing and spends
   nothing.
5. **Bounded LLM spend by construction.** Every model call is stateless and
   capped in size; there is no ever-growing conversation to resend.
6. **Native Ollama Cloud**, with automatic fallback to **Claude Sonnet 5.5
   (effort: high)** through the user's own Claude Code subscription when the
   Ollama quota runs out. Nothing is lost while both are unavailable — work
   waits in the queue.
7. **Secrets never leave the machine** and never reach the database: redaction
   happens at capture time, before anything is written.
8. Safe with many sessions and subagents running in parallel.
9. Imports the existing claude-mem database.

**Non-goals** (deliberately dropped from claude-mem): web viewer, Chroma /
vector DB, knowledge "corpora", cloud sync, Claude Agent SDK and Gemini
providers, Cursor / opencode integrations. Vector search may return later
(§8), the rest will not.

## 2. Lessons that shaped this design

Every item below was observed on this machine while running claude-mem
13.x + an Ollama proxy, and each maps to a decision further down.

| Observed failure | Cause | Decision |
|---|---|---|
| 100+ `bun.exe` (~170 MB each, 17 GB total) | every hook boots the whole worker bundle in a new process; `PostToolUse "*"` + `PreToolUse Read` fire on every tool call of every session and subagent | hooks are a tiny Go exit-fast path (§4); reads are not captured |
| ~190k input tokens per generation call for a ~1k answer; weekly quota exhausted | generator keeps one conversation per session and resends all of it every call | stateless batched calls with hard caps (§6) |
| observation `type` values like `<what>\n <title>…` | free-form XML output parsed loosely | JSON output validated against a schema in Go (§6.3) |
| one repo shows up as 30 "projects" | project = worktree directory name | project identity from git remote / common dir (§5.2) |
| `sync_outbox`: 3.6M rows, 1.2 GB | cloud-sync outbox fills when sync is off | no sync feature at all |
| hung hooks outlive their timeout on Windows | Claude Code kills the bash wrapper, not the child | no wrapper; the binary enforces its own deadline |
| all plugins disabled after an edit | UTF-8 BOM written into `settings.json` | installer writes BOM-less JSON/TOML with backups (§9) |
| worker never came back after a mass kill | Bun transpiler cache entry truncated mid-write | no runtime with a mutable code cache |
| model calls start failing with 410 overnight | Ollama Cloud retires models (`deepseek-v4-flash:0731`, `glm-5.1`) | ordered model chain; 410 disables a model loudly (§7.1) |
| quota guard broke | undocumented `/api/usage` changed shape (fractions → request counts) | quota detected from the chat response itself (429), not from side APIs |
| `format` (JSON schema) ignored by Ollama Cloud models | constrained decoding is not applied in cloud | schema in the prompt + local validation + one repair retry |
| headless `claude -p` → 401 | child inherits the desktop app's `ANTHROPIC_BASE_URL` / `CLAUDE_CODE_*`; CLI's own OAuth token expired | scrub inherited env; use a `claude setup-token` token (§7.2) |

### 2.1 What others learned (survey, 2026-10-07)

Stars and issue references are from the GitHub API on that date.

| Project | ★ | Lang | Model | Take | Avoid |
|---|---|---|---|---|---|
| thedotmack/claude-mem | 97k | TS | hooks → LLM observer → SQLite+FTS5+Chroma | 3-layer progressive disclosure (search → timeline → get); `<private>` tags | unbounded observer history (#3497), loose XML parsing (#3606), bash/login-shell per hook on Windows ~3.5 s (#4121), dedup only by exact hash (#3038) |
| Gentleman-Programming/engram | 7k | **Go** | agent calls `mem_save` itself; SQLite+FTS5; MCP | `topic_key` upsert for explicit memories; idempotency keys; session-summary protocol | write latency growing with DB size (#1536), Git Bash windows on Windows (#1136), hooks firing in foreign sessions (#1672) |
| mem0ai/mem0 | 67k | Py | LLM fact extraction, ADD-only, vector+BM25 | scopes (user / agent / run) | audit found 97.8% of 10k memories junk (#4573); contradictions pile up, no recency (#4956, #5352); TOCTOU in hash dedup (#6515) |
| getzep/graphiti | 32k | Py | bi-temporal graph, immutable episodes | episodes as immutable source, facts point back to them; invalidate, never delete | invalidation searched across the whole graph — 41% of edges invalidated, mostly wrongly (#1728) |
| vectorize-io/hindsight | 47k | Py | retain / recall / reflect; Postgres+pgvector | per-repo memory banks; token-budgeted recall | heavy stack for a local tool |
| basicmachines-co/basic-memory | 4k | Py | Markdown files as truth, SQLite index | human-editable memory | hybrid RRF scored *worse* than either signal alone (#577) |
| MemPalace/mempalace | 59k | Py | verbatim storage, no LLM extraction | deterministic writes; tiny activation cost | its benchmark win is plain verbatim retrieval ([arXiv 2604.21284](https://arxiv.org/abs/2604.21284)) |
| rohitg00/agentmemory | 29k | TS | hooks, no LLM by default, BM25+vector+graph | secret filter before write; ≤ 3 results per session | — |

Consequences folded into this design:

- **Raw events are the source of truth; observations are derived and
  rebuildable** (Graphiti's episodes, simplified). A better prompt or model
  later means `quipu rebuild`, not hand-cleaning junk.
- **Two write paths**: automatic compression *and* explicit `remember` with a
  `topic_key` that upserts (engram) — automation alone is noisy, manual alone
  misses things.
- **Supersession is narrow**: a new observation can only supersede one with
  the same `topic_key` or the same file in the same project, never a global
  similarity search (Graphiti #1728). Superseded rows stay, hidden by default,
  and every result shows its date.
- **Search has a floor**: below a relevance threshold it returns nothing rather
  than the top-k anyway (cognee #4462). No blended RRF until measured to help.
- **Injected memory is untrusted input** (claude-mem #1935): fenced, labelled
  as recalled data, size-capped.
- **Evaluation exists from milestone 1**, not after: public LoCoMo /
  LongMemEval numbers measure chat recall of user facts, and LoCoMo's own
  answer key is ~6% wrong; neither says anything about coding memory.

Note on prior art: engram covers the "Go + SQLite + MCP" half of this design.
quipu exists for the other half it lacks — automatic capture compressed by a
cheap cloud model with a subscription fallback, shared across Claude Code and
Codex. Ideas are borrowed with credit; no code or prompts are copied from any
project above.

## 3. Shape

```
                 Claude Code                      Codex CLI
                 hooks + MCP                      hooks + MCP
                     │                                │
        ┌────────────┴───── quipu hook … ─────────────┴──────────┐
        │  capture: filter → redact → truncate → INSERT event    │  ~10 ms, no network
        │  SessionStart: SELECT → print context block            │
        └───────────────────────────┬────────────────────────────┘
                                    ▼
                     ~/.quipu/quipu.db   (SQLite, WAL, FTS5)
                    events │ jobs │ observations │ summaries │ prompts
                                    ▲
        ┌───────────────────────────┴────────────────────────────┐
        │  quipu mcp  (stdio, one per agent session)              │
        │   • tools: search / timeline / get / remember           │
        │   • processor goroutine: claims jobs by lease           │──► Ollama Cloud /api/chat
        │                                                         │──► claude -p (Sonnet 5.5, high)
        └─────────────────────────────────────────────────────────┘
```

One binary, subcommands:

| Command | Who runs it | What it does |
|---|---|---|
| `quipu hook <agent> <event>` | agent hooks | capture / inject; always exits 0 |
| `quipu mcp` | agent MCP config | MCP server + background processor |
| `quipu drain` | SessionEnd hook (detached), cron, by hand | process the queue until empty, then exit |
| `quipu install claude-code\|codex` / `uninstall` | user | wire hooks + MCP, BOM-less, with backup |
| `quipu import claude-mem` | user, once | migrate an existing claude-mem DB |
| `quipu status` / `doctor` | user | queue depth, failing jobs, provider state, auth checks |
| `quipu search …` | user | the MCP search, from a terminal |

## 4. Capture (the hot path)

`quipu hook` reads the hook JSON from stdin and must finish in tens of
milliseconds. Hard internal deadline: 2 s, after which it exits 0 having done
nothing — a lost event is acceptable, a blocked agent is not. If
`QUIPU_INTERNAL=1` is set (our own fallback `claude -p`), it exits immediately.

| Event (Claude Code / Codex) | Action |
|---|---|
| `SessionStart` | print the context block (§5.4) |
| `UserPromptSubmit` | store the prompt (redacted); `<private>…</private>` spans are dropped |
| `PostToolUse` | store one event for state-changing tools only — Claude: `Edit Write MultiEdit NotebookEdit Bash PowerShell`; Codex: see below |
| `Stop` | mark a turn boundary; enqueue compress + summary jobs for the session |
| `SessionEnd` | same as Stop, then spawn a detached `quipu drain` if no `quipu mcp` holds the processor lease |

Reads (`Read`, `Grep`, `Glob`, `LS`, read-only shell commands, web fetches)
are captured **as touches, not as events**: path or URL, tool, session, time —
no content, no LLM call. In the claude-mem DB 58% of observations are
`discovery` notes paraphrasing code that was only read; that paraphrase is what
cost the tokens, and the code itself remains the better source. What *is*
worth keeping about reads is cheap:

- which files a turn looked at → fed to the turn summary as a list, so
  "investigated" is accurate without describing each file;
- per project, which files are read most → part of the project profile (§5.3);
- `search` can answer "when did we last look at `auth/session.go`".

What the agent *learned* from reading surfaces in its final message, which the
turn summary already consumes.

Codex has no separate edit tool: everything is `exec`, and edits are
`apply_patch` invocations inside it (748 of 2,850 `exec` calls in local
rollouts). For Codex the read filter is therefore command-based: an `exec`
whose command is an `apply_patch` or is not a known read-only command (`cat`,
`rg`, `grep`, `ls`, `sed -n`, `head`, `Get-Content`, `git status/log/diff`, …)
is captured; the rest is dropped. The same command filter applies to Claude's
`Bash`/`PowerShell`.

Per event, in this order (order matters — a `<private>` block cut in half by
truncation leaks, engram #1558):

1. drop `<private>…</private>` spans;
2. replace base64 / binary blobs and images with a size note (claude-mem #3839);
3. redact secrets (patterns + entropy);
4. truncate input and output (head + tail, default 4 KB each);
5. reduce paths under ignore globs (`.env*`, `*.pem`, `id_*`, `*.key`, …) to
   "touched <path>".

The hook command in the agent config is the bare executable path — no bash
preamble, no login shell (on Windows that alone costs seconds per call,
claude-mem #4121). p95 hook latency is a tracked metric.

## 5. Storage

`~/.quipu/quipu.db`, `modernc.org/sqlite` (pure Go, no CGO, FTS5 built in),
WAL, `busy_timeout=5000`, `synchronous=NORMAL`. Hooks do one short write
transaction; contention between parallel sessions is resolved by SQLite.
Forward-only migrations embedded in the binary, one chain, tracked in
`PRAGMA user_version`; a binary that finds a schema **newer** than it knows
refuses to write instead of "repairing" it (claude-mem #3736 — an old cached
worker rolled a shared schema back).

Two FTS5 indexes over the same text: `unicode61` for words (Latin, Cyrillic)
and `trigram` for identifiers, paths and CJK (claude-mem #3982).

### 5.1 Tables

| Table | Purpose |
|---|---|
| `projects` | id, canonical key (§5.2), display name, root path |
| `sessions` | agent (`claude`/`codex`), agent session id, project, cwd, started/ended |
| `prompts` | user prompts, redacted (+FTS) |
| `events` | captured tool events, already redacted and truncated — **the source of truth**; kept (default 180 days) so observations can be rebuilt |
| `touches` | reads: session, path/URL, tool, time — no content |
| `profiles` | one per project: stack, commands, layout, hot files (§5.3) |
| `jobs` | `compress` / `summarize`; `lease_owner`, `lease_until`, `attempts`, `next_attempt_at`, `last_error` |
| `observations` | type, title, subtitle, facts[], narrative, files[], concepts[], `topic_key`, source event ids, model, provider, `created_at`, `superseded_at`/`superseded_by`, `content_hash` (+FTS) |
| `summaries` | per turn: request, investigated, learned, completed, next_steps (+FTS) |
| `provider_state` | per provider/model: `blocked_until`, `disabled` (410), last error, calls today |

### 5.2 Project identity

Key = normalized git remote URL of `origin`; else the git common dir (so all
worktrees of a repo collapse into one project); else the absolute cwd. Computed
once per session and cached in `sessions`, never per event.

### 5.3 Project profile — stack and architecture

The answer to "what is this project built with and how is it laid out" should
not be rediscovered from hundreds of read-notes. One record per project:

| Field | Source | LLM? |
|---|---|---|
| languages, frameworks, key deps | manifests: `go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, `*.csproj`, `Dockerfile`, `docker-compose*`, … | no |
| build / test / run commands | `Bash`/`exec` events that succeeded, ranked by frequency | no |
| top-level layout | directory tree (depth 2, ignore rules applied) | no |
| hot files | `touches` ranked by recency × frequency | no |
| architecture paragraph | one short call over the fields above + README head | yes, once |

Rebuilt when a manifest's hash changes or every N sessions — not per event.
SessionStart injects its one-line form (`Go 1.26 · SQLite · MCP · test: go
test ./...`); `get(profile)` returns the full record. For call graphs and
"who calls what", agents already have codebase-memory-mcp; the profile does not
duplicate it.

### 5.4 Context injection (SessionStart)

Deterministic, no LLM, ≤ 50 ms, hard budget (default 800 tokens):
the last summary of this project + the most recent `decision` / `gotcha` /
`bugfix` observations as one-line index entries (`#id date type title`),
ending with a hint that `get` returns details. Progressive disclosure keeps the
block small; the agent pulls details only when it needs them. Small matters
twice: whatever a hook injects is also written into the agent's session file
on every start (claude-mem #1269 — 7 KB × 8 per session pinned a CPU).

The block is fenced and labelled as recalled data, not instructions, and
observation text inside it is escaped — memory content was written from tool
output and is untrusted.

Optional (off by default until measured): on `UserPromptSubmit`, an FTS query
over the prompt injects up to 3 matches **above the relevance floor**. One
long-running user reported injection dropping from ~1,800 to ~1,000 tokens
with better relevance this way (claude-mem #1573).

## 6. Processing

### 6.1 Who runs it

A goroutine inside every `quipu mcp`. Jobs are claimed with an atomic
`UPDATE jobs SET lease_owner=?, lease_until=now+5m WHERE id=(SELECT … LIMIT 1)`;
a crashed holder's lease simply expires. N parallel sessions therefore mean at
most one call in flight per job, and **one** provider call at a time overall
(a global lease row) — throughput is not the goal, spend is.

### 6.2 Batching

A `compress` job covers one session's unprocessed events and is created when
any of: ≥ 20 events, a `Stop`, or the oldest event is 10 min old.
`summarize` runs after the turn's `compress`.

Every call is **stateless** and capped:

- system prompt + output schema (fixed, ~1.5k tokens)
- project name, the turn's user prompt(s)
- the batch's events (total ≤ 24k chars; oversize batches split)
- titles of the session's last 10 observations, for de-duplication

Worst case ≈ 10k input tokens per call, whatever the session length — versus
~190k today.

### 6.3 Output

JSON only: `{"observations":[{type,title,subtitle,facts[],narrative,files[],concepts[]}]}`
with `type` ∈ `decision bugfix feature refactor change gotcha security_note`.
The schema is sent in the prompt (and as `format` for Ollama, which cloud
models currently ignore). Go strips code fences, validates, and on failure
retries **once** with the validation error appended. A second failure marks
the job failed; its events are kept for a later retry or another provider.

A job is acknowledged only after its observations are committed; a response
that does not parse never marks events processed (claude-mem #3606 silently
dropped non-conforming output and confirmed the batch).

Dedup and supersession, both scoped narrowly:

- exact repeat (`content_hash` of normalized type+title+facts, per project):
  dropped, in the same transaction as the insert (`INSERT … ON CONFLICT`), not
  check-then-insert (mem0 #6515);
- same `topic_key`, or same title touching the same files, in the same
  project: the older row gets `superseded_at` / `superseded_by`. No global
  similarity search decides that two memories conflict.

## 7. Providers

An ordered chain, configured in `~/.quipu/config.toml`:

```toml
[[provider]]
kind   = "ollama"
models = ["deepseek-v4.1-flash", "glm-5.3-flash"]   # tried in order

[[provider]]
kind   = "claude"
model  = "claude-sonnet-5-5"
effort = "high"
max_calls_per_hour = 20       # it spends the user's Claude plan
```

### 7.1 Ollama Cloud (native)

`POST https://ollama.com/api/chat`, `stream:false`, `think:false`, `format`,
`options.temperature=0.2`, `options.num_predict` capped. Key from
`OLLAMA_API_KEY` or the config file (user-only ACL). Error handling by class:

| Response | Meaning | Action |
|---|---|---|
| 429 / usage-limit body | quota window exhausted | provider `blocked_until` = reset time if given, else back off 15 min → 1 h with a one-token probe |
| 410 | model retired | disable that model permanently, log loudly, try the next |
| 5xx / network | transient | retry with jitter, max 3 |
| 401 / 403 | bad key | block provider, surface in `status` / `doctor` |

### 7.2 Claude Sonnet 5.5 fallback

Used only when every Ollama model is blocked or disabled:

```
claude -p --model claude-sonnet-5-5 --effort high
          --output-format json --json-schema <schema>
          --no-session-persistence --tools "" --strict-mcp-config
          --settings '{"disableAllHooks":true}'
```

prompt on stdin, result from `structured_output` (validated by the CLI).

- **Environment is scrubbed** of inherited `ANTHROPIC_*`, `CLAUDE_CODE_*`,
  `CLAUDECODE`: inside the desktop app these point the child at the host's
  relay, and the child's requests fail with 401.
- **Auth**: a long-lived token from `claude setup-token`, stored in
  `~/.quipu/claude.token` and passed as `CLAUDE_CODE_OAUTH_TOKEN`. The CLI's
  own stored login is not relied upon — inside the desktop app it is refreshed
  by the app, not the CLI, and goes stale.
- **No recursion**: hooks disabled, no tools, no MCP, no session file written;
  `QUIPU_INTERNAL=1` as a second guard.
- **Budget**: `max_calls_per_hour`, one call at a time, and larger batches than
  for Ollama (fewer, fuller calls).

## 8. Retrieval (MCP)

Official Go SDK (`github.com/modelcontextprotocol/go-sdk`), stdio.

| Tool | Returns |
|---|---|
| `search(query, project?, type?, since?, limit=10)` | compact index rows: id, date, project, type, title — FTS5 BM25 over observations + summaries + prompts, current project boosted, recency as tie-break, superseded rows hidden; **empty below the relevance floor** |
| `timeline(id \| query, before=5, after=5)` | what happened around a point |
| `get(ids[])` | full records, with dates and supersession chain |
| `remember(text, type?, topic_key?, files?)` | an observation written directly by the agent — no LLM; an existing `topic_key` in the project is updated, not duplicated |

Vector search is deferred. Ollama Cloud serves no embedding models today, so
embeddings would have to be local (`ollama` `/api/embed`) or optional — the
baseline must not need them. FTS5 over ~50k short records is fast; if recall
proves insufficient, vectors go into a BLOB column with brute-force cosine (no
vector DB; 50k × 768 floats ≈ 150 MB, a scan is milliseconds), merged with
weights tuned on the eval set — never a blind RRF (basic-memory #577).

## 9. Integration

**Claude Code** — `quipu install claude-code` merges into
`~/.claude/settings.json` (BOM-less, backup first): hooks for the events in §4
calling `quipu hook claude <event>` directly (no bash wrapper), and the MCP
server `quipu mcp`.

**Codex CLI** (0.160: `hooks` feature stable) — `quipu install codex` adds a
marked block to `~/.codex/config.toml`: `[[hooks.SessionStart]]`,
`[[hooks.UserPromptSubmit]]`, `[[hooks.PostToolUse]]`, `[[hooks.Stop]]` calling
`quipu hook codex <event>`, and `[mcp_servers.quipu]`. Both agents share one
database, so memory crosses tools.

Both installers are idempotent and reversible (`quipu uninstall`).

## 10. Import from claude-mem

`quipu import claude-mem [--db ~/.claude-mem/claude-mem.db]`, read-only on the
source: observations, summaries and prompts; worktree-suffixed project names
collapsed (§5.2); malformed `type` values mapped to `change`; reads-only
`discovery` notes kept but de-prioritised in injection; dedup by content hash.

## 11. Evaluation

Built alongside milestone 1 and run on every prompt or model change:

- **Recall set**: ~50 questions about the owner's real repos with known answers
  ("why did we drop GLM?", "what broke the worker on 2026-10-07?") — answered
  only from `search` + `get`; score = right answer found in ≤ 2 tool calls.
- **Junk audit**: sample 100 observations, label useful / duplicate / noise
  (repeat of the mem0 #4573 audit); target < 20% junk.
- **Staleness**: share of answers that cite a superseded fact.
- **Cost**: input tokens per captured event and per session, per provider.
- **Hook latency**: p50 / p95 per event on Windows.
- **Side-by-side**: same sessions through claude-mem and quipu before
  switching over.

## 12. Layout

```
cmd/quipu/          main, subcommand dispatch
internal/store/     schema, migrations, queries, FTS
internal/capture/   hook handlers, filters, redaction, project identity
internal/llm/       ollama, claude, provider chain, schema validation
internal/process/   jobs, leases, batching, prompts
internal/mcpserver/ MCP tools
internal/install/   claude-code / codex installers, import
```

## 13. Milestones

1. store + capture (events + touches) + `install claude-code` + project
   profile (deterministic part) + SessionStart injection (no LLM yet) + eval
   harness
2. processor + Ollama provider + schema validation
3. MCP search/timeline/get/remember
4. Claude Sonnet fallback + budget
5. Codex installer
6. claude-mem import, side-by-side quality comparison on the same sessions
7. switch over, remove claude-mem + proxy + watchdog
