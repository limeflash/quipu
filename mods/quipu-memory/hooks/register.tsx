import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { MemoryModel, MemoryProjectRow, MemoryStats } from '../types'
import { activitySvg, projectsSvg, tilesSvg, typesSvg, type Tile } from './charts'
import { ago, bytes, clip, clockTime, compact, int, modelLabel, parseTime, plural, readable, records, typeInfo } from './format'

const PANE = 'quipu-memory'
const TITLE = 'Память'
const REFRESH_MS = 60_000

const stats = atom({ plugin: 'quipu-memory', key: 'stats' } as const, null)
const failure = atom({ plugin: 'quipu-memory', key: 'error' } as const, null)
const refreshedAt = atom({ plugin: 'quipu-memory', key: 'refreshedAt' } as const, 0)
const expanded = atom({ plugin: 'quipu-memory', key: 'expanded' } as const, null)

const MODEL_TONE = { ready: '#10B981', backoff: '#F59E0B', disabled: '#EF4444' } as const

// Module state: starts over on a reload, which only costs one git lookup.
let cwd = ''
let project: string | undefined
let busy = false
let opened = false

async function engram($: EngineInterface, args: string[]): Promise<string> {
  const env = { ENGRAM_NO_UPDATE_CHECK: '1' }
  const tried: string[] = []
  const local = await $.env.get('LOCALAPPDATA')
  for (const exe of ['engram', ...(local ? [`${local}\\Programs\\engram\\engram.exe`] : [])]) {
    try {
      const run = await $.process.run([exe, ...args], { env, timeoutMs: 60_000 })
      if (run.exitCode === 0) return run.stdout
      tried.push(`${exe}: ${run.stderr.trim().split('\n').pop() ?? `exit ${run.exitCode}`}`)
    } catch (err) {
      tried.push(`${exe}: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  throw new Error(tried.join('; '))
}

async function refresh($: EngineInterface): Promise<void> {
  if (busy) return
  busy = true
  try {
    const here = await $.session.cwd()
    if (here !== cwd) {
      cwd = here
      project = undefined
    }
    // The first read resolves the project through git; later ones name it.
    const where = project ? ['--project', project] : ['--dir', cwd]
    const parsed = JSON.parse(await engram($, ['autocapture', 'stats', '--json', ...where])) as MemoryStats
    project = parsed.project?.name
    await update($, stats, () => parsed)
    await update($, failure, () => null)
    const landed = await $.clock.now()
    await update($, refreshedAt, () => landed)
    $.ui.status(statusLine(parsed))
  } catch (err) {
    await update($, failure, () => (err instanceof Error ? err.message : String(err)))
    $.ui.status('память: нет данных')
  } finally {
    busy = false
  }
}

async function openPane($: EngineInterface): Promise<void> {
  opened = true
  await $.ui.open({ id: PANE, title: TITLE })
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({ name: 'memory', description: 'Панель памяти: активность, модели, проекты и записи этого репозитория' })
    $.clock.after(200, () => void refresh($))
    $.clock.every(REFRESH_MS, () => void refresh($))
    if ((await $.session.surfaces()).includes('desktop')) void openPane($)
    return next(e)
  })

  // The desktop attaches after the session starts: the panel opens beside it once.
  on('session.attach', async ($, e, next) => {
    if (e.surface === 'desktop' && !opened) void openPane($)
    return next(e)
  })

  on('command.run', { command: 'memory' }, async $ => {
    await openPane($)
    void refresh($)
    return { text: 'Панель памяти открыта.' }
  }).catch(() => ({ text: 'Панель памяти не открылась: engram недоступен?' }))

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const s = await read($, stats)
    const err = await read($, failure)
    const at = await read($, refreshedAt)
    const open = await read($, expanded)
    const now = await $.clock.now()

    if (e.surface === 'terminal') {
      const { Box, Text } = $.ui.resolve(e)
      if (!s) return <Text dimColor>{err ? `Память недоступна: ${err}` : 'Загружаю память…'}</Text>
      const today = s.days[s.days.length - 1]
      return (
        <Box flexDirection="column">
          <Text bold>
            Память{s.project ? ` · ${s.project.name}` : ''} <Text dimColor>· обновлено {ago(at, now)}</Text>
          </Text>
          <Text>
            {compact(s.totals.records)} записей · сегодня +{int(today?.records ?? 0)} · в очереди {int(s.spool)} · пишет {writer(s.models)}
          </Text>
          {(s.project?.recent ?? []).slice(0, 8).map(r => (
            <Text>
              <Text color={typeInfo(r.type).color}>● </Text>
              {clip(r.title, Math.max(20, e.props.bodyColumns - 22))} <Text dimColor>{ago(parseTime(r.created_at), now)}</Text>
            </Text>
          ))}
        </Box>
      )
    }

    const { Box, Text, Button, Svg, Markdown } = $.ui.resolve(e)
    const width = Math.max(340, Math.min(760, Math.round(e.props.bodyColumns * 8)))

    if (!s) {
      return (
        <Box flexDirection="column" gap={1} padding={1}>
          <Text bold>{TITLE}</Text>
          {err ? (
            <Box flexDirection="column" gap={1}>
              <Text color="error">Не получилось прочитать память.</Text>
              <Text dimColor>{clip(err, 400)}</Text>
              <Text dimColor>Проверь в терминале: engram autocapture stats --json</Text>
              <Button key="retry" variant="primary" label="Повторить" onPress={() => refresh($)} />
            </Box>
          ) : (
            <Box flexDirection="row" gap={1} alignItems="center">
              <Text dimColor>Загружаю память…</Text>
              <Button key="load" plain dimColor label="Обновить" onPress={() => refresh($)} />
            </Box>
          )}
        </Box>
      )
    }

    const p = s.project
    const today = s.days[s.days.length - 1]
    const week = s.days.slice(-7).reduce((a, d) => a + d.records, 0)
    const active = s.models.find(m => m.state === 'ready')
    const paused = s.models.filter(m => m.state !== 'ready').length
    const tiles: Tile[] = [
      p
        ? { value: int(p.records), label: 'В этом проекте', sub: `${int(p.captured)} автозахват`, tone: 'accent' }
        : { value: compact(s.totals.records), label: 'Всего записей', tone: 'accent' },
      { value: `+${int(today?.records ?? 0)}`, label: 'Сегодня', sub: `за неделю ${compact(week)}` },
      { value: compact(s.totals.records), label: 'Вся память', sub: `${int(s.totals.projects)} ${plural(s.totals.projects, ['проект', 'проекта', 'проектов'])} · ${bytes(s.db_bytes)}` },
      {
        value: int(s.spool),
        label: 'В очереди',
        sub: s.spool > 200 ? 'сжатие отстаёт' : 'ждут сжатия',
        tone: s.spool > 200 ? 'warn' : undefined,
      },
      {
        value: active ? shortModel(active.name) : 'никто',
        label: 'Пишет',
        sub: paused ? `${paused} на паузе` : 'вся цепочка готова',
        tone: !active ? 'bad' : paused ? 'warn' : 'ok',
      },
    ]

    return (
      <Box flexDirection="column" gap={1} paddingX={1} paddingY={1}>
        <Box flexDirection="row" justifyContent="space-between" alignItems="center">
          <Text bold>{p ? `Память · ${p.name}` : TITLE}</Text>
          <Box flexDirection="row" gap={1} alignItems="center">
            <Text dimColor>{err ? `ошибка обновления, данные на ${clockTime(at)}` : `обновлено ${clockTime(at)}`}</Text>
            <Button key="refresh" plain dimColor label="↻" onPress={() => refresh($)} />
          </Box>
        </Box>
        {p?.profile ? <Text dimColor>{p.profile}</Text> : null}

        <Svg source={tilesSvg(tiles, width)} alt={tiles.map(t => `${t.label}: ${t.value}`).join('; ')} width={width} isInteractive />

        <Text bold>Активность за 14 дней</Text>
        <Svg source={activitySvg(s.days, width)} alt="Записи по дням и типам, токены моделей" width={width} isInteractive />

        <Text bold>Кто пишет память</Text>
        <Box flexDirection="row" flexWrap="wrap" columnGap={3} rowGap={0}>
          {s.models.map((m, i) => (
            <Box key={`m${i}`} flexDirection="column">
              <Text>
                <Text color={MODEL_TONE[m.state]}>● </Text>
                {modelLabel(m.name)} <Text dimColor>{modelNote(m, now)}</Text>
              </Text>
              {m.state !== 'ready' && m.last_error ? <Text dimColor>   {clip(m.last_error, 70)}</Text> : null}
            </Box>
          ))}
        </Box>

        {p ? (
          <Box flexDirection="column" gap={1}>
            <Text bold>Этот проект</Text>
            {Object.keys(p.types).length > 0 ? (
              <Svg source={typesSvg(p.types, width)} alt="Записи проекта по типам" width={width} isInteractive />
            ) : null}
            {p.summary ? (
              <Markdown text={`**Последняя сессия** · ${ago(parseTime(p.summary.updated_at), now)} назад\n\n${summaryText(p.summary.digest)}`} />
            ) : null}
            <Box flexDirection="column">
              {p.recent.map(r => (
                <Box key={`r${r.id}`} flexDirection="column">
                  <Button key={`rec-${r.id}`} plain onPress={() => update($, expanded, cur => (cur === r.id ? null : r.id))}>
                    <Text color={typeInfo(r.type).color}>● </Text>
                    {clip(r.title, 96)}
                    <Text dimColor>
                      {'  '}
                      {typeInfo(r.type).label} · {ago(parseTime(r.created_at), now)}
                      {r.source === 'imported' ? ' · из claude-mem' : r.source === 'agent' ? ' · сохранил агент' : ''}
                    </Text>
                  </Button>
                  {open === r.id ? (
                    <Box flexDirection="column" paddingLeft={2} paddingBottom={1} gap={1}>
                      <Markdown text={readable(r.preview)} />
                      <Box flexDirection="row" gap={1}>
                        <Button
                          key={`ask-${r.id}`}
                          label="В запрос"
                          onPress={() => $.prompt.fill({ text: `[память #${r.id}: ${r.title}] `, mode: 'insert' })}
                        />
                        <Text dimColor>#{r.id}</Text>
                      </Box>
                    </Box>
                  ) : null}
                </Box>
              ))}
              {p.recent.length === 0 ? <Text dimColor>Записей в этом проекте пока нет.</Text> : null}
            </Box>
          </Box>
        ) : (
          <Text dimColor>Эта папка не определилась как проект — общая картина ниже.</Text>
        )}

        <Text bold>Проекты</Text>
        <Svg source={projectsSvg(withCurrent(s), p?.name, now, width)} alt="Проекты по последней активности" width={width} isInteractive />

        <Text dimColor>
          {records(s.totals.records)}: автозахват {compact(s.totals.captured)}, импорт из claude-mem {compact(s.totals.imported)}
          {s.totals.agent ? `, сохранил агент ${int(s.totals.agent)}` : ''} · /memory открывает эту панель
        </Text>
      </Box>
    )
  })
}

/** The recently active projects, the current one always among them. */
function withCurrent(s: MemoryStats): MemoryProjectRow[] {
  const p = s.project
  const rows = s.projects.slice(0, 10)
  if (!p || rows.some(r => r.name === p.name)) return rows
  const row = { name: p.name, records: p.records, captured: p.captured, last_at: p.recent[0]?.created_at ?? '' }
  return [...rows.slice(0, 9), row]
}

function writer(models: MemoryModel[]): string {
  const m = models.find(x => x.state === 'ready')
  return m ? shortModel(m.name) : 'никто'
}

function shortModel(name: string): string {
  if (name.startsWith('codex:')) return 'Codex'
  if (name.startsWith('claude:')) return 'Claude'
  return name.replace(/-v?\d.*$/, '').replace(/^./, c => c.toUpperCase())
}

function modelNote(m: MemoryModel, now: number): string {
  if (m.state === 'disabled') return 'отключена'
  if (m.state === 'backoff') return `пауза до ${clockTime(parseTime(m.blocked_until))}`
  if (m.calls_hour) return `${m.calls_hour} за час`
  const last = parseTime(m.last_call)
  return Number.isFinite(last) ? `вызов ${ago(last, now)} назад` : 'готова'
}

function statusLine(s: MemoryStats): string {
  const today = s.days[s.days.length - 1]?.records ?? 0
  return `память ${compact(s.totals.records)} · сегодня +${int(today)} · пишет ${writer(s.models)}${s.spool > 200 ? ` · очередь ${int(s.spool)}` : ''}`
}

/** The digest's English labels, in Russian, as Markdown. */
function summaryText(digest: string): string {
  return digest
    .replace(/^Goal:\s*/m, '**Цель:** ')
    .replace(/^Next steps:\s*/im, '\n**Дальше:**\n')
    .replace(/^Accomplished:\s*/m, '\n**Сделано:**\n')
}
