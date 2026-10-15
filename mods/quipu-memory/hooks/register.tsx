import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { MemoryModel, MemoryProjectRow, MemoryStats } from '../types'
import { activitySvg, projectsSvg, sparkSvg, tilesSvg, typesSvg, type Tile } from './charts'
import { ACCENT, ago, bytes, clip, clockTime, compact, int, modelLabel, parseTime, plural, readable, records, typeInfo } from './format'
import { iconSvg, type IconName } from './icons'

const PANE = 'quipu-memory'
const TITLE = 'Память'
const REFRESH_MS = 60_000
const DOCK_COLUMNS = 56
const GREEN = '#10B981'
const AMBER = '#F59E0B'
const RED = '#EF4444'

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

const STATE_COLOR = { ready: GREEN, backoff: AMBER, disabled: RED } as const

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
    const queueHigh = s.spool > 200

    if (e.surface === 'terminal') {
      const { Box, Text, Button } = $.ui.resolve(e)
      return (
        <Box flexDirection="row" columnGap={2}>
          <Text>
            <Text color={ACCENT}>◆</Text> <Text bold>{compact(s.totals.records)}</Text> <Text dimColor>в памяти</Text>
            {'  '}
            <Text color={GREEN}>▲ +{int(today)}</Text> <Text dimColor>сегодня</Text>
            {'  '}
            <Text color={writing ? GREEN : RED}>●</Text> {writing ? shortModel(writing.name) : 'никто не пишет'}
            {queueHigh ? <Text color={AMBER}>{`  очередь ${int(s.spool)}`}</Text> : ''}
          </Text>
          <Button key="band-open" label="Открыть" onPress={() => openPane($)} />
        </Box>
      )
    }

    const { Box, Text, Button, Svg } = $.ui.resolve(e)
    const ic = (name: IconName, color?: string) => <Svg source={iconSvg(name, color, 15)} alt="" width={15} height={15} />
    return (
      <Box flexDirection="row" alignItems="center" columnGap={2} flexWrap="wrap">
        <Box flexDirection="row" alignItems="center" columnGap={1}>
          {ic('quipu', ACCENT)}
          <Text bold>{compact(s.totals.records)}</Text>
          <Text dimColor>в памяти</Text>
        </Box>
        <Box flexDirection="row" alignItems="center" columnGap={1}>
          {ic('trend', GREEN)}
          <Text color={GREEN} bold>+{int(today)}</Text>
          <Text dimColor>сегодня</Text>
        </Box>
        <Svg source={sparkSvg(s.days, 70, 16)} alt={`Записи за ${s.days.length} дней`} width={70} height={16} />
        <Box flexDirection="row" alignItems="center" columnGap={1}>
          {ic('model', writing ? (paused ? AMBER : GREEN) : RED)}
          <Text>{writing ? shortModel(writing.name) : 'никто не пишет'}</Text>
          {paused ? <Text dimColor>· {paused} на паузе</Text> : null}
        </Box>
        {queueHigh ? (
          <Box flexDirection="row" alignItems="center" columnGap={1}>
            {ic('queue', AMBER)}
            <Text color={AMBER}>{int(s.spool)} в очереди</Text>
          </Box>
        ) : null}
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
            <Text color={ACCENT}>◆</Text> Память{s.project ? ` · ${s.project.name}` : ''} <Text dimColor>· {clockTime(at)}</Text>
          </Text>
          <Text>
            {compact(s.totals.records)} записей · сегодня +{int(today?.records ?? 0)} · очередь {int(s.spool)} · пишет {writer(s.models)}
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
    const ic = (name: IconName, color?: string, size = 15) => <Svg source={iconSvg(name, color, size)} alt="" width={size} height={size} />
    const heading = (name: IconName, text: string, note?: string) => (
      <Box flexDirection="row" alignItems="center" columnGap={1}>
        {ic(name)}
        <Text bold>{text}</Text>
        {note ? <Text dimColor>{note}</Text> : null}
      </Box>
    )

    const header = (
      <Box flexDirection="row" alignItems="center" justifyContent="space-between">
        <Box flexDirection="row" alignItems="center" columnGap={1}>
          {ic('quipu', ACCENT, 18)}
          <Text bold>Память</Text>
          {s?.project ? <Text dimColor>· {s.project.name}</Text> : null}
        </Box>
        <Box flexDirection="row" alignItems="center" columnGap={1}>
          <Text dimColor>{err && s ? `данные на ${clockTime(at)}` : at ? clockTime(at) : ''}</Text>
          <Button key="refresh" plain dimColor label="↻" onPress={() => refresh($)} />
        </Box>
      </Box>
    )

    if (!s) {
      return (
        <Box flexDirection="column" gap={1} padding={1}>
          {header}
          {err ? (
            <Box flexDirection="column" gap={1}>
              <Text color="error">Не получилось прочитать память.</Text>
              <Text dimColor>{clip(err, 300)}</Text>
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
    const tabs = (
      <Box flexDirection="row" columnGap={1}>
        {TABS.map(t =>
          t.id === current ? (
            <Button key={`tab-${t.id}`} variant="primary" label={t.label} onPress={() => showTab($, t.id)} />
          ) : (
            <Button key={`tab-${t.id}`} plain dimColor label={t.label} onPress={() => showTab($, t.id)} />
          ),
        )}
      </Box>
    )

    let body
    if (current === 'records') {
      body = (
        <Box flexDirection="column" gap={1}>
          {p?.summary ? (
            <Box flexDirection="column" borderStyle="round" paddingX={1} paddingY={0}>
              {heading('session', 'Последняя сессия', `· ${ago(parseTime(p.summary.updated_at), now)} назад`)}
              <Markdown text={summaryText(p.summary.digest)} />
            </Box>
          ) : null}
          {p && Object.keys(p.types).length > 0 ? (
            <Svg source={typesSvg(p.types, width)} alt="Записи проекта по типам" width={width} isInteractive />
          ) : null}
          {heading('note', 'Последние записи', p ? `· ${records(p.records)}` : undefined)}
          <Box flexDirection="column">
            {(p?.recent ?? []).map(r => (
              <Box key={`r${r.id}`} flexDirection="column">
                <Button key={`rec-${r.id}`} plain onPress={() => update($, expanded, cur => (cur === r.id ? null : r.id))}>
                  <Text color={typeInfo(r.type).color}>● </Text>
                  {clip(r.title, Math.max(24, e.props.bodyColumns - 16))}
                  <Text dimColor>
                    {'  '}
                    {ago(parseTime(r.created_at), now)}
                  </Text>
                </Button>
                {open === r.id ? (
                  <Box flexDirection="column" paddingLeft={2} paddingBottom={1} gap={1}>
                    <Text dimColor>
                      {typeInfo(r.type).label} · #{r.id}
                      {r.source === 'imported' ? ' · из claude-mem' : r.source === 'agent' ? ' · сохранил агент' : ''}
                    </Text>
                    <Markdown text={readable(r.preview)} />
                    <Button key={`ask-${r.id}`} label="В запрос" onPress={() => $.prompt.fill({ text: `[память #${r.id}: ${r.title}] `, mode: 'insert' })} />
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
        <Box flexDirection="column" gap={1}>
          {heading('layers', 'Проекты', `· ${int(s.totals.projects)} ${plural(s.totals.projects, ['проект', 'проекта', 'проектов'])}`)}
          <Svg source={projectsSvg(withCurrent(s), p?.name, now, width)} alt="Проекты по последней активности" width={width} isInteractive />
          <Text dimColor>
            {records(s.totals.records)}: автозахват {compact(s.totals.captured)}, импорт из claude-mem {compact(s.totals.imported)}
            {s.totals.agent ? `, сохранил агент ${int(s.totals.agent)}` : ''} · база {bytes(s.db_bytes)}
          </Text>
        </Box>
      )
    } else {
      const writing = s.models.find(m => m.state === 'ready')
      const tiles: Tile[] = [
        p
          ? { icon: 'folder', value: int(p.records), label: 'В проекте', sub: `${int(p.captured)} автозахват`, color: ACCENT }
          : { icon: 'folder', value: '—', label: 'В проекте', sub: 'папка не проект', color: ACCENT },
        { icon: 'trend', value: `+${int(today?.records ?? 0)}`, label: 'Сегодня', sub: `за неделю ${compact(week)}`, color: GREEN },
        { icon: 'database', value: compact(s.totals.records), label: 'Вся память', sub: `${int(s.totals.projects)} ${plural(s.totals.projects, ['проект', 'проекта', 'проектов'])} · ${bytes(s.db_bytes)}`, color: '#6366F1' },
        { icon: 'queue', value: int(s.spool), label: 'В очереди', sub: s.spool > 200 ? 'сжатие отстаёт' : 'ждут сжатия', color: s.spool > 200 ? AMBER : '#0EA5E9' },
      ]
      body = (
        <Box flexDirection="column" gap={1}>
          <Svg source={tilesSvg(tiles, width, 2)} alt={tiles.map(t => `${t.label}: ${t.value}`).join('; ')} width={width} isInteractive />
          {heading('activity', 'Активность', '· 14 дней')}
          <Svg source={activitySvg(s.days, width)} alt="Записи по дням и типам, токены моделей" width={width} isInteractive />
          {heading('model', 'Кто пишет память', writing ? `· сейчас ${shortModel(writing.name)}` : '· никто')}
          <Box flexDirection="column">
            {s.models.map((m, i) => (
              <Box key={`m${i}`} flexDirection="row" alignItems="center" columnGap={1}>
                {ic('model', STATE_COLOR[m.state], 13)}
                <Text>{modelLabel(m.name)}</Text>
                <Text dimColor>{modelNote(m, now)}</Text>
              </Box>
            ))}
          </Box>
        </Box>
      )
    }

    return (
      <Box flexDirection="column" gap={1} paddingX={1} paddingY={1}>
        {header}
        {p?.profile ? <Text dimColor>{clip(p.profile, 160)}</Text> : null}
        {tabs}
        {body}
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
  if (m.state === 'backoff') return `пауза до ${clockTime(parseTime(m.blocked_until))}${m.last_error ? ` · ${clip(m.last_error, 40)}` : ''}`
  if (m.calls_hour) return `${m.calls_hour} за час`
  const last = parseTime(m.last_call)
  return Number.isFinite(last) ? `вызов ${ago(last, now)} назад` : 'готова'
}

function statusLine(s: MemoryStats): string {
  const today = s.days[s.days.length - 1]?.records ?? 0
  return `🧠 ${compact(s.totals.records)} · 📈 +${int(today)} сегодня · ⚡ ${writer(s.models)}${s.spool > 200 ? ` · 📥 ${int(s.spool)}` : ''}`
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
