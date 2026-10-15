import type { On } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'

import type { MemoryDay, MemoryStats } from '../types'
import { activitySvg, niceMax, projectsSvg, sparkSvg, tilesSvg } from './charts'
import { compact, int } from './format'

const DAYS: MemoryDay[] = Array.from({ length: 14 }, (_, i): MemoryDay => ({
  date: `2026-10-${String(i + 2).padStart(2, '0')}`,
  records: i * 10,
  by_type: i ? { bugfix: i, feature: i * 9 } : {},
  calls: i,
  errors: i === 13 ? 1 : 0,
  tokens_in: i * 1000,
  tokens_out: i * 100,
}))

const STATS: MemoryStats = {
  generated_at: '2026-10-15T12:00:00Z',
  db_bytes: 550_858_752,
  totals: { records: 56_947, captured: 7_875, agent: 1, imported: 49_071, projects: 53 },
  project: {
    name: 'engram',
    records: 72,
    captured: 72,
    profile_id: 282,
    profile: 'Go 1.25 · templ · bubbletea',
    summary: { id: 86, session_id: 's', updated_at: '2026-10-15 11:00:00', digest: 'Goal: ship quipu\nNext steps: - recall test' },
    recent: [
      { id: 501, type: 'bugfix', title: 'Search falls back to any term', source: 'captured', created_at: '2026-10-15 10:00:00', preview: '**What**: AllThenAny\n**Why**: AND missed records' },
      { id: 400, type: 'discovery', title: 'Old note', source: 'imported', created_at: '2026-09-01 10:00:00', preview: 'from claude-mem' },
    ],
    types: { bugfix: 16, feature: 32, discovery: 9 },
  },
  projects: [
    { name: 'engram', records: 72, captured: 72, last_at: '2026-10-15 10:00:00' },
    { name: 'cs2farm', records: 19_859, captured: 1_200, last_at: '2026-10-14 10:00:00' },
  ],
  days: DAYS,
  models: [
    { name: 'deepseek-v4.1-flash', state: 'ready', calls_hour: 3 },
    { name: 'glm-5.3-flash', state: 'backoff', blocked_until: '2026-10-15T13:00:00Z', last_error: 'HTTP 500', calls_hour: 0 },
    { name: 'codex:gpt-6-luna', state: 'ready', calls_hour: 0 },
  ],
  spool: 4,
}

const PANE = {
  title: 'Память',
  isFocused: false,
  bodyColumns: 90,
  placement: 'dock',
  scroll: { offset: 0, bodyRows: 60 },
  view: {},
} as const

test('numbers read the Russian way', () => {
  expect(int(56947)).toBe('56 947')
  expect(compact(56947)).toBe('56,9 тыс.')
  expect(compact(6_100_000)).toBe('6,1 млн')
  expect(compact(950)).toBe('950')
})

test('axes end on round numbers', () => {
  expect(niceMax(1856)).toBe(2000)
  expect(niceMax(226)).toBe(250)
  expect(niceMax(0)).toBe(1)
})

test('the activity chart has one tooltip per day and a legend', () => {
  const svg = activitySvg(DAYS, 640)
  expect((svg.match(/<g class="col"><title>/g) ?? []).length).toBe(14)
  expect(svg).toContain('130 записей: баг 13, фича 117')
  expect(svg).toContain('ошибок 1')
  expect(svg).toContain('токены')
  expect(svg).toContain('prefers-color-scheme:dark')
})

test('the projects chart marks the current project', () => {
  const svg = projectsSvg(STATS.projects, 'engram', Date.parse('2026-10-15T12:00:00Z'), 640)
  expect(svg).toContain('engram (этот проект)')
  expect(svg).toContain('19,9 тыс.')
})

test('tiles carry an icon each and wrap two to a row', () => {
  const svg = tilesSvg(
    [
      { icon: 'folder', value: '72', label: 'В проекте', color: '#D97757' },
      { icon: 'trend', value: '+263', label: 'Сегодня', color: '#10B981' },
      { icon: 'database', value: '57 тыс.', label: 'Вся память', color: '#6366F1' },
    ],
    400,
    2,
  )
  expect((svg.match(/<g transform="translate/g) ?? []).length).toBe(3)
  expect(svg).toContain('height="140"')
})

test('the spark line marks today', () => {
  const svg = sparkSvg(DAYS, 70, 16)
  expect((svg.match(/<rect /g) ?? []).length).toBe(14)
  expect(svg).toContain('opacity="1"')
})

function stub(on: On) {
  mock.clock(on, { now: Date.parse('2026-10-15T12:00:00Z') })
  mock.env(on, {})
  mock.store(on, {})
  on('session.cwd', () => ({ value: 'C:/code/quipu' }))
  on('process.run', (_$, e) =>
    e.argv.includes('stats')
      ? { value: { exitCode: 0, stdout: JSON.stringify(STATS), stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
      : { value: { exitCode: 1, stdout: '', stderr: 'unexpected', isStdoutTruncated: false, isStderrTruncated: false } },
  )
}

test('the pane loads the stats, switches tabs and opens a record', async ($, on) => {
  stub(on)

  const desktop = await $.ui.mount({ plugin: 'quipu-memory', surface: 'desktop', component: 'Pane', props: PANE, requestId: 'quipu-memory' })
  expect(await desktop.find({ text: /Загружаю память/ })).toBeDefined()
  await desktop.press({ key: 'load' })

  expect(await desktop.find({ text: '· engram' })).toBeDefined()
  const alts = (await desktop.findAll({ type: 'Svg' })).map(svg => String(svg.props.alt))
  expect(alts.some(alt => alt.includes('В проекте: 72'))).toBe(true)
  expect(await desktop.find({ text: /пауза до/ })).toBeDefined()
  expect(await desktop.find({ key: 'rec-501' })).toBeUndefined()

  await desktop.press({ key: 'tab-records' })
  expect(await desktop.find({ text: /Последняя сессия/ })).toBeDefined()
  await desktop.press({ key: 'rec-501' })
  expect(await desktop.find({ key: 'ask-501' })).toBeDefined()
  expect(await desktop.find({ text: /Почему:/ })).toBeDefined()

  await desktop.press({ key: 'tab-projects' })
  expect((await desktop.findAll({ type: 'Svg' })).some(svg => String(svg.props.alt).startsWith('Проекты'))).toBe(true)

  const terminal = await $.ui.mount({ plugin: 'quipu-memory', surface: 'terminal', component: 'Pane', props: PANE, requestId: 'quipu-memory' })
  expect(await terminal.find({ text: /Search falls back to any term/ })).toBeDefined()
})

const BAND = { hasSurvey: false, isWorking: false, maxRows: 4, bodyColumns: 100, scroll: { offset: 0, bodyRows: 4 }, view: {} } as const

test('the band shows the memory in one line and hides on request', async ($, on) => {
  stub(on)
  // What the engine draws in the band when no plugin does.
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => $.ui.resolve(e).Text({ children: 'engine-band' }))
  const pane = await $.ui.mount({ plugin: 'quipu-memory', surface: 'desktop', component: 'Pane', props: PANE, requestId: 'quipu-memory' })
  await pane.press({ key: 'load' })

  for (const surface of ['desktop', 'terminal'] as const) {
    const band = await $.ui.mount({ plugin: 'quipu-memory', surface, component: 'AbovePrompt', props: BAND })
    expect(await band.find({ key: 'band-open' })).toBeDefined()
    expect(await band.find({ text: /\+263|\+130/ })).toBeDefined()
  }
  const band = await $.ui.mount({ plugin: 'quipu-memory', surface: 'desktop', component: 'AbovePrompt', props: BAND })
  await band.press({ key: 'band-hide' })
  expect(await band.find({ key: 'band-open' })).toBeUndefined()
  expect(await band.find({ text: 'engine-band' })).toBeDefined()
  const again = await $.ui.mount({ plugin: 'quipu-memory', surface: 'terminal', component: 'AbovePrompt', props: BAND })
  expect(await again.find({ key: 'band-open' })).toBeUndefined()
})
