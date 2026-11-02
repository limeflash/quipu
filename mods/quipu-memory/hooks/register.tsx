import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { MemoryModel, MemoryProjectRow, MemoryStats } from '../types'
import { activitySvg, projectsSvg, sparkSvg, typesSvg } from './charts'
import { ACCENT, ago, bytes, clip, clockTime, compact, int, modelLabel, parseTime, plural, readable, records, typeInfo } from './format'
import { iconSvg, type IconName } from './icons'

const PANE = 'quipu-memory'
const TITLE = 'Память'
const REFRESH_MS = 60_000
const DOCK_COLUMNS = 56
const GREEN = '#10B981'
const INDIGO = '#6366F1'
const SKY = '#0EA5E9'

const stats = atom({ plugin: 'quipu-memory', key: 'stats' } as const, null)
const failure = atom({ plugin: 'quipu-memory', key: 'error' } as const, null)
const refreshedAt = atom({ plugin: 'quipu-memory', key: 'refreshedAt' } as const, 0)
const expanded = atom({ plugin: 'quipu-memory', key: 'expanded' } as const, null)
const tab = atom({ plugin: 'quipu-memory', key: 'tab' } as const, 'overview')
const bandHidden = atom({ plugin: 'quipu-memory', key: 'bandHidden' } as const, false)

const TABS = [
  { id: 'overview', label: 'Обзор' },
  { id: 'records', label: 'Записи' },
  { id: 'projects', label: 'Проекты' },
] as const

/** Theme keys, so the colours follow the person's theme on every surface. */
const STATE_COLOR = { ready: 'success', backoff: 'warning', disabled: 'error' } as const
const QUEUE_HIGH = 200

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
  } catch (err) {
    await update($, failure, () => (err instanceof Error ? err.message : String(err)))
  } finally {
    busy = false
    await syncStatus($)
  }
}

/** The status line carries the summary only while the band is hidden. */
async function syncStatus($: EngineInterface): Promise<void> {
  const s = await read($, stats)
  if (!(await read($, bandHidden))) return $.ui.status(undefined)
  $.ui.status(s ? statusLine(s) : '🧠 память: нет данных')
}

async function setBand($: EngineInterface, hidden: boolean): Promise<void> {
  await update($, bandHidden, () => hidden)
  await $.store.set('bandHidden', hidden)
  await syncStatus($)
}

async function openPane($: EngineInterface): Promise<void> {
  opened = true
  await $.ui.open({ id: PANE, title: TITLE, columns: DOCK_COLUMNS })
}

async function showTab($: EngineInterface, id: 'overview' | 'records' | 'projects'): Promise<void> {
  await update($, tab, () => id)
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({ name: 'memory', description: 'Панель памяти; заодно возвращает полосу над вводом' })
    await update($, bandHidden, () => false)
    if ((await $.store.get('bandHidden')) === true) await update($, bandHidden, () => true)
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
    await setBand($, false)
    void refresh($)
    return { text: 'Панель памяти открыта.' }
  }).catch(() => ({ text: 'Панель памяти не открылась: engram недоступен?' }))

  // The band above the prompt: the memory in one line, with the way into the panel.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const s = await read($, stats)
    if (e.props.hasSurvey || !s || (await read($, bandHidden))) return next(e)
    const today = s.days[s.days.length - 1]?.records ?? 0
    const writing = s.models.find(m => m.state === 'ready')
    const paused = s.models.filter(m => m.state !== 'ready').length
    const queueHigh = s.spool > QUEUE_HIGH

    if (e.surface === 'terminal') {
      const { Box, Text, Button } = $.ui.resolve(e)
      return (
        <Box flexDirection="row" columnGap={2}>
          <Text>
            <Text color="claude">◆</Text> <Text bold>{compact(s.totals.records)}</Text> <Text dimColor>в памяти · {bytes(s.db_bytes)}</Text>
            {'  '}
            <Text color="success">▲ +{int(today)}</Text> <Text dimColor>сегодня</Text>
            {'  '}
            <Text color={writing ? 'success' : 'error'}>●</Text> {writing ? shortModel(writing.name) : 'никто не пишет'}
            {queueHigh ? <Text color="warning">{`  очередь ${int(s.spool)}`}</Text> : ''}
          </Text>
          <Button key="band-open" label="Открыть" onPress={() => openPane($)} />
        </Box>
      )
    }

    const { Box, Text, Button, Svg } = $.ui.resolve(e)
    return (
      <Box flexDirection="row" alignItems="center" columnGap="md" rowGap="xs" flexWrap="wrap">
        <Box flexDirection="row" alignItems="center" columnGap="xs">
          <Svg source={iconSvg('quipu', ACCENT, 14)} alt="" width={14} height={14} isInteractive />
          <Text size="footnote" weight="semibold" tabular>{compact(s.totals.records)}</Text>
          <Text size="footnote" dimColor>в памяти</Text>
        </Box>
        <Box flexDirection="row" alignItems="center" columnGap="xs">
          <Text size="footnote" weight="semibold" color="success" tabular>+{int(today)}</Text>
          <Text size="footnote" dimColor>сегодня</Text>
          <Svg source={sparkSvg(s.days, 56, 14)} alt={`Записи за ${s.days.length} дней`} width={56} height={14} isInteractive />
        </Box>
        <Box flexDirection="row" alignItems="center" columnGap="xs">
          <Text size="footnote" color={writing ? (paused ? 'warning' : 'success') : 'error'}>●</Text>
          <Text size="footnote">{writing ? shortModel(writing.name) : 'никто не пишет'}</Text>
          {queueHigh ? <Text size="footnote" color="warning">· очередь {int(s.spool)}</Text> : null}
        </Box>
        <Text size="footnote" dimColor tabular>{bytes(s.db_bytes)}</Text>
        <Button key="band-open" plain label="Открыть ›" onPress={() => openPane($)} />
        <Button key="band-hide" plain dimColor label="Скрыть" onPress={() => setBand($, true)} />
      </Box>
    )
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const s = await read($, stats)
    const err = await read($, failure)
    const at = await read($, refreshedAt)
    const open = await read($, expanded)
    const current = await read($, tab)
    const now = await $.clock.now()

    if (e.surface === 'terminal') {
      const { Box, Text } = $.ui.resolve(e)
      if (!s) return <Text dimColor>{err ? `Память недоступна: ${err}` : 'Загружаю память…'}</Text>
      const today = s.days[s.days.length - 1]
      return (
        <Box flexDirection="column">
          <Text bold>
            <Text color="claude">◆</Text> Память{s.project ? ` · ${s.project.name}` : ''} <Text dimColor>· {clockTime(at)}</Text>
          </Text>
          <Text>
            {compact(s.totals.records)} записей · база {bytes(s.db_bytes)} · сегодня +{int(today?.records ?? 0)} · очередь {int(s.spool)} · пишет {writer(s.models)}
          </Text>
          {(s.project?.recent ?? []).slice(0, 8).map(r => (
            <Text>
              <Text color={typeInfo(r.type).color}>● </Text>
              {clip(r.title, Math.max(20, e.props.bodyColumns - 14))} <Text dimColor>{ago(parseTime(r.created_at), now)}</Text>
            </Text>
          ))}
        </Box>
      )
    }

    const { Box, Text, Button, Svg, Markdown } = $.ui.resolve(e)
    const width = Math.max(280, Math.min(560, Math.round(e.props.bodyColumns * 7.6)))
    const icon = (name: IconName, color: string, size = 16) => (
      <Svg source={iconSvg(name, color, size)} alt="" width={size} height={size} isInteractive />
    )
    // An interactive frame takes the height it is given, so each chart passes its own.
    const chart = (source: string, alt: string) => (
      <Svg source={source} alt={alt} width={width} height={heightOf(source)} isInteractive />
    )
    const section = (title: string, note?: string, noteColor?: string) => (
      <Box flexDirection="row" alignItems="center" columnGap="xs" marginTop="sm">
        <Text weight="semibold">{title}</Text>
        {note ? noteColor ? <Text size="footnote" color={noteColor}>{note}</Text> : <Text size="footnote" dimColor>{note}</Text> : null}
      </Box>
    )

    const header = (
      <Box flexDirection="row" alignItems="center" justifyContent="space-between" columnGap="sm">
        <Box flexDirection="row" alignItems="center" columnGap="sm" flexShrink={1}>
          {icon('quipu', ACCENT, 20)}
          <Text size="heading" weight="semibold">Память</Text>
          {s?.project ? <Text size="footnote" dimColor wrap="truncate-end">{s.project.name}</Text> : null}
        </Box>
        <Box flexDirection="row" alignItems="center" columnGap="xs">
          <Text size="caption" dimColor tabular>{err && s ? `данные на ${clockTime(at)}` : at ? clockTime(at) : ''}</Text>
          <Button key="refresh" plain dimColor label="↻" onPress={() => refresh($)} />
        </Box>
      </Box>
    )

    if (!s) {
      return (
        <Box flexDirection="column" rowGap="md" padding="md">
          {header}
          {err ? (
            <Box flexDirection="column" rowGap="sm" borderStyle="round" borderColor="error" paddingX="md" paddingY="sm">
              <Text weight="semibold" color="error">Не получилось прочитать память</Text>
              <Text size="footnote" dimColor>{clip(err, 300)}</Text>
              <Text size="footnote" dimColor>Проверь в терминале: engram autocapture stats --json</Text>
              <Box flexDirection="row">
                <Button key="retry" variant="primary" label="Повторить" onPress={() => refresh($)} />
              </Box>
            </Box>
          ) : (
            <Box flexDirection="row" columnGap="sm" alignItems="center">
              <Text dimColor>Загружаю память…</Text>
              <Button key="load" plain dimColor label="Обновить" onPress={() => refresh($)} />
            </Box>
          )}
        </Box>
      )
    }

    const p = s.project
    const today = s.days[s.days.length - 1]?.records ?? 0
    const week = s.days.slice(-7).reduce((a, d) => a + d.records, 0)
    const tabs = (
      <Box flexDirection="row" columnGap="xs">
        {TABS.map(t =>
          t.id === current ? (
            <Button key={`tab-${t.id}`} variant="primary" label={t.label} onPress={() => showTab($, t.id)} />
          ) : (
            <Button key={`tab-${t.id}`} plain dimColor label={t.label} onPress={() => showTab($, t.id)} />
          ),
        )}
      </Box>
    )

    type Card = { key: string; icon: IconName; tint: string; label: string; value: string; sub: string; valueColor?: string }
    const card = (c: Card) => (
      <Box
        key={c.key}
        flexDirection="column"
        flexGrow={1}
        flexShrink={1}
        width="50%"
        rowGap="xs"
        paddingX="md"
        paddingY="sm"
        borderStyle="round"
        borderDimColor
        hover={{ borderColor: 'claude', borderDimColor: false }}
      >
        <Box flexDirection="row" alignItems="center" columnGap="xs">
          {icon(c.icon, c.tint, 14)}
          <Text size="footnote" dimColor>{c.label}</Text>
        </Box>
        {c.valueColor ? (
          <Text size="title" weight="semibold" tabular color={c.valueColor}>{c.value}</Text>
        ) : (
          <Text size="title" weight="semibold" tabular>{c.value}</Text>
        )}
        <Text size="caption" dimColor wrap="truncate-end">{c.sub}</Text>
      </Box>
    )
    const grid = (cards: Card[]) => (
      <Box flexDirection="column" rowGap="sm">
        {pairs(cards).map((row, i) => (
          <Box key={`cards-${i}`} flexDirection="row" columnGap="sm">
            {row.map(card)}
          </Box>
        ))}
      </Box>
    )

    let body
    if (current === 'records') {
      body = (
        <Box flexDirection="column" rowGap="sm">
          {p?.summary ? (
            <Box flexDirection="column" rowGap="xs" paddingX="md" paddingY="sm" borderStyle="round" borderDimColor>
              <Box flexDirection="row" alignItems="center" columnGap="xs">
                {icon('session', ACCENT, 14)}
                <Text weight="semibold">Последняя сессия</Text>
                <Text size="footnote" dimColor>· {ago(parseTime(p.summary.updated_at), now)} назад</Text>
              </Box>
              <Markdown text={summaryText(p.summary.digest)} />
            </Box>
          ) : null}
          {p && Object.keys(p.types).length > 0 ? chart(typesSvg(p.types, width), 'Записи проекта по типам') : null}
          {section('Последние записи', p ? records(p.records) : undefined)}
          <Box flexDirection="column" rowGap="xs">
            {(p?.recent ?? []).map(r => (
              <Box key={`r${r.id}`} flexDirection="column" rowGap="xs">
                <Box flexDirection="row" alignItems="center" columnGap="xs">
                  <Text color={typeInfo(r.type).color}>●</Text>
                  <Box flexGrow={1} flexShrink={1}>
                    <Button
                      key={`rec-${r.id}`}
                      plain
                      label={clip(r.title, Math.max(24, e.props.bodyColumns - 14))}
                      onPress={() => update($, expanded, cur => (cur === r.id ? null : r.id))}
                    />
                  </Box>
                  <Text size="caption" dimColor tabular>{ago(parseTime(r.created_at), now)}</Text>
                </Box>
                {open === r.id ? (
                  <Box flexDirection="column" rowGap="xs" marginLeft="md" paddingX="md" paddingY="sm" borderStyle="round" borderDimColor>
                    <Text size="caption" dimColor>
                      {typeInfo(r.type).label} · #{r.id}
                      {r.source === 'imported' ? ' · из claude-mem' : r.source === 'agent' ? ' · сохранил агент' : ''}
                    </Text>
                    <Markdown text={readable(r.preview)} />
                    <Box flexDirection="row">
                      <Button key={`ask-${r.id}`} label="В запрос" onPress={() => $.prompt.fill({ text: `[память #${r.id}: ${r.title}] `, mode: 'insert' })} />
                    </Box>
                  </Box>
                ) : null}
              </Box>
            ))}
            {!p ? <Text dimColor>Эта папка не определилась как проект.</Text> : p.recent.length === 0 ? <Text dimColor>Записей пока нет.</Text> : null}
          </Box>
        </Box>
      )
    } else if (current === 'projects') {
      body = (
        <Box flexDirection="column" rowGap="sm">
          {section('Проекты', `${int(s.totals.projects)} ${plural(s.totals.projects, ['проект', 'проекта', 'проектов'])}`)}
          {chart(projectsSvg(withCurrent(s), p?.name, now, width), 'Проекты по последней активности')}
          <Text size="footnote" dimColor>
            {records(s.totals.records)}: автозахват {compact(s.totals.captured)}, импорт из claude-mem {compact(s.totals.imported)}
            {s.totals.agent ? `, сохранил агент ${int(s.totals.agent)}` : ''}
          </Text>
        </Box>
      )
    } else {
      const writing = s.models.find(m => m.state === 'ready')
      const queueHigh = s.spool > QUEUE_HIGH
      const perRecord = s.totals.records ? s.db_bytes / s.totals.records / 1024 : 0
      body = (
        <Box flexDirection="column" rowGap="sm">
          {grid([
            { key: 'c-total', icon: 'layers', tint: INDIGO, label: 'Вся память', value: compact(s.totals.records), sub: `${int(s.totals.projects)} ${plural(s.totals.projects, ['проект', 'проекта', 'проектов'])} · автозахват ${compact(s.totals.captured)}` },
            { key: 'c-db', icon: 'disk', tint: SKY, label: 'База данных', value: bytes(s.db_bytes), sub: `engram.db · ≈ ${perRecord.toFixed(1).replace('.', ',')} КБ на запись` },
            { key: 'c-today', icon: 'trend', tint: GREEN, label: 'Сегодня', value: `+${int(today)}`, valueColor: 'success', sub: `за неделю ${compact(week)}` },
            p
              ? { key: 'c-project', icon: 'folder', tint: ACCENT, label: 'В проекте', value: int(p.records), sub: `автозахват ${int(p.captured)}` }
              : { key: 'c-project', icon: 'folder', tint: ACCENT, label: 'В проекте', value: '—', sub: 'папка не проект' },
          ])}
          {section('Активность', `14 дней · ${records(s.days.reduce((a, d) => a + d.records, 0))}`)}
          {chart(activitySvg(s.days, width), 'Записи по дням и типам, токены моделей')}
          {section(
            'Кто пишет память',
            `${writing ? `сейчас ${shortModel(writing.name)}` : 'никто'} · в очереди ${int(s.spool)}`,
            queueHigh ? 'warning' : undefined,
          )}
          <Box flexDirection="column" rowGap="xs">
            {s.models.map((m, i) => {
              const [provider, model] = splitModel(m.name)
              return (
                <Box key={`m${i}`} flexDirection="row" alignItems="center" justifyContent="space-between" columnGap="sm">
                  <Box flexDirection="row" alignItems="center" columnGap="xs" flexShrink={1}>
                    <Text color={STATE_COLOR[m.state]}>●</Text>
                    <Text>{provider}</Text>
                    <Text dimColor wrap="truncate-end">{model}</Text>
                  </Box>
                  <Text size="footnote" dimColor>{modelNote(m, now)}</Text>
                </Box>
              )
            })}
          </Box>
        </Box>
      )
    }

    return (
      <Box flexDirection="column" rowGap="sm" paddingX="md" paddingY="md">
        {header}
        {p?.profile ? <Text size="caption" dimColor>{clip(p.profile, 160)}</Text> : null}
        <Box marginY="xs">{tabs}</Box>
        {body}
      </Box>
    )
  })
}

/** The height `charts.ts` declares on its root `<svg>`, so the frame fits the drawing. */
function heightOf(source: string): number {
  return Number(/ height="([\d.]+)"/.exec(source)?.[1] ?? 120)
}

function pairs<T>(items: T[]): T[][] {
  const rows: T[][] = []
  for (let i = 0; i < items.length; i += 2) rows.push(items.slice(i, i + 2))
  return rows
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

/** "Ollama · deepseek-v4.1-flash" → ["Ollama", "deepseek-v4.1-flash"]. */
function splitModel(name: string): [string, string] {
  const [provider = '', model = ''] = modelLabel(name).split(' · ')
  return [provider, model]
}

function modelNote(m: MemoryModel, now: number): string {
  if (m.state === 'disabled') return 'отключена'
  if (m.state === 'backoff') return `пауза до ${clockTime(parseTime(m.blocked_until))}${m.last_error ? ` · ${clip(m.last_error, 40)}` : ''}`
  if (m.calls_hour) return `${m.calls_hour} за час`
  const last = parseTime(m.last_call)
  return Number.isFinite(last) ? `вызов ${ago(last, now)} назад` : 'готова'
}

function statusLine(s: MemoryStats): string {
  const today = s.days[s.days.length - 1]?.records ?? 0
  return `🧠 ${compact(s.totals.records)} · 💾 ${bytes(s.db_bytes)} · 📈 +${int(today)} сегодня · ⚡ ${writer(s.models)}${s.spool > QUEUE_HIGH ? ` · 📥 ${int(s.spool)}` : ''}`
}

/** The digest's English labels, in Russian, as Markdown: the goal and what is next. */
function summaryText(digest: string): string {
  const goal = /^Goal:\s*([\s\S]*?)(?=^Next steps:|^Accomplished:|$(?![\s\S]))/m.exec(digest)?.[1]?.trim()
  const next = /^Next steps:\s*([\s\S]*?)(?=^Accomplished:|$(?![\s\S]))/m.exec(digest)?.[1]?.trim()
  const parts = []
  if (goal) parts.push(`**Цель:** ${clip(goal, 280)}`)
  if (next) parts.push(`**Дальше:**\n${next.split('\n').slice(0, 4).join('\n')}`)
  return parts.length ? parts.join('\n\n') : clip(digest, 400)
}
