# quipu-memory

A Claude Code mod: a live **Память** panel for your quipu/engram memory, beside the conversation in Claude Desktop (and as text in a terminal pane).

- **Tiles**: records in this repo, today's count and the week's, the whole memory (records, projects, database size), events waiting in the spool, which model is writing and how many are paused.
- **Activity, 14 days**: records stacked by type, model tokens as a line; hover a day for its breakdown, calls and errors.
- **Who writes the memory**: the backend chain in try order — ready, paused until when (with the last error), disabled.
- **This project**: its records by type, the latest session's goal and next steps, the twelve latest records — click one to read it, *В запрос* puts a reference to it into the prompt.
- **Projects**: the ten most recently active, square-root bars of captured vs other records, the current repo always listed.

It reads `engram autocapture stats --json` every minute (the first read resolves the repo through git, later ones pass `--project`) and shows a one-line summary in the status line. The panel opens by itself when the desktop attaches; `/memory` opens it anywhere.

## Install

Needs quipu's `engram` on `PATH` (or in `%LOCALAPPDATA%\Programs\engram`). Point Claude Code at this folder in `~/.claude/settings.json`:

```json
{ "env": { "CLAUDE_CODE_PLUGIN_DIRS": "C:\path\to\quipu\mods\quipu-memory" } }
```

New sessions — the desktop app's included — load it.

## Develop

```bash
claude plugin validate mods/quipu-memory
claude plugin test mods/quipu-memory
```
