# quipu-memory

A Claude Code mod for your quipu/engram memory, with its own line icons (a quipu — the knotted cord the project is named after — for the memory itself), light and dark alike.

**A band above the prompt**, one line: the whole memory, today's new records in green, a fourteen-day spark line, which model is writing (amber when some are paused) and a queue warning when compression falls behind. *Открыть ›* opens the panel; *Скрыть* hides the band for good (it is remembered across sessions, and the status line takes over); `/memory` brings both back.

**A slim panel** beside the conversation, in three tabs:

- **Обзор**: four tiles (this repo, today and the week, the whole memory, the spool), fourteen days of records stacked by type with model tokens as a line (hover a day for its breakdown, calls and errors), and the backend chain — ready, paused until when and why, disabled.
- **Записи**: the latest session's goal and next steps, this repo's records by type, its twelve latest records — click one to read it, *В запрос* quotes it into the prompt.
- **Проекты**: the ten most recently active, square-root bars of captured vs other records, the current repo always listed.

It reads `engram autocapture stats --json` every minute in the background (the first read resolves the repo through git, later ones pass `--project`). The panel opens by itself when the desktop attaches; closed, `/memory` reopens it.

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
