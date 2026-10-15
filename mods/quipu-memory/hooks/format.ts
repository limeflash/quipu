/** Record types in the order they stack, each with its colour and Russian label. */
export const TYPES: readonly { type: string; color: string; label: string }[] = [
  { type: 'decision', color: '#8B5CF6', label: 'решение' },
  { type: 'architecture', color: '#6366F1', label: 'архитектура' },
  { type: 'bugfix', color: '#EF4444', label: 'баг' },
  { type: 'feature', color: '#10B981', label: 'фича' },
  { type: 'refactor', color: '#F59E0B', label: 'рефакторинг' },
  { type: 'config', color: '#0EA5E9', label: 'настройка' },
  { type: 'pattern', color: '#EC4899', label: 'паттерн' },
  { type: 'learning', color: '#84CC16', label: 'урок' },
  { type: 'discovery', color: '#94A3B8', label: 'находка' },
]

const OTHER = { color: '#A1A1AA', label: 'другое' }

export function typeInfo(type: string): { color: string; label: string } {
  return TYPES.find(t => t.type === type) ?? { ...OTHER, label: type.replace(/_/g, ' ') }
}

/** Folds the types the chart does not stack (change, security_note…) into "other". */
export function stackKey(type: string): string {
  return TYPES.some(t => t.type === type) ? type : 'other'
}

export const ACCENT = '#D97757'

/** 56947 → "56 947"; Russian digit grouping with a thin no-break space. */
export function int(n: number): string {
  return Math.round(n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ' ')
}

/** 56947 → "56,9 тыс.", 6100000 → "6,1 млн". */
export function compact(n: number): string {
  const one = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1).replace(/\.0$/, '')).replace('.', ',')
  if (n >= 1e9) return `${one(n / 1e9)} млрд`
  if (n >= 1e6) return `${one(n / 1e6)} млн`
  if (n >= 1e4) return `${one(n / 1e3)} тыс.`
  return int(n)
}

/** Russian plural: plural(5, ['проект', 'проекта', 'проектов']) → "проектов". */
export function plural(n: number, forms: readonly [string, string, string]): string {
  const a = Math.abs(Math.round(n)) % 100
  const b = a % 10
  if (a > 10 && a < 20) return forms[2]
  if (b === 1) return forms[0]
  if (b >= 2 && b <= 4) return forms[1]
  return forms[2]
}

/** "1 запись", "3 записи", "56 947 записей". */
export function records(n: number): string {
  return `${int(n)} ${plural(n, ['запись', 'записи', 'записей'])}`
}

/** Bytes as "525 МБ". */
export function bytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1).replace('.', ',')} ГБ`
  return `${Math.round(n / (1 << 20))} МБ`
}

/** engram's "2006-01-02 15:04:05" (UTC) or RFC 3339 → ms since the epoch. */
export function parseTime(s: string | undefined): number {
  if (!s) return NaN
  return Date.parse(s.includes('T') ? s : s.replace(' ', 'T') + 'Z')
}

/** "только что", "5 мин", "3 ч", "2 дн", "14 окт". */
export function ago(ms: number, now: number): string {
  if (!Number.isFinite(ms)) return ''
  const s = Math.max(0, (now - ms) / 1000)
  if (s < 45) return 'только что'
  if (s < 3600) return `${Math.round(s / 60)} мин`
  if (s < 86400) return `${Math.round(s / 3600)} ч`
  if (s < 7 * 86400) return `${Math.round(s / 86400)} дн`
  return dayLabel(new Date(ms))
}

const MONTHS = ['янв', 'фев', 'мар', 'апр', 'мая', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек']
const WEEKDAYS = ['вс', 'пн', 'вт', 'ср', 'чт', 'пт', 'сб']

export function dayLabel(d: Date): string {
  return `${d.getDate()} ${MONTHS[d.getMonth()] ?? ''}`
}

export function weekday(d: Date): string {
  return WEEKDAYS[d.getDay()] ?? ''
}

/** "2026-10-14" as a local date. */
export function localDate(day: string): Date {
  const [y = 1970, m = 1, d = 1] = day.split('-').map(Number)
  return new Date(y, m - 1, d)
}

export function clockTime(ms: number): string {
  const d = new Date(ms)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

export function xml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

export function clip(s: string, n: number): string {
  const chars = [...s]
  return chars.length <= n ? s : chars.slice(0, n - 1).join('') + '…'
}

/** A record's text without engram's field labels, one paragraph per field. */
export function readable(preview: string): string {
  return preview
    .replace(/\*\*(What|Why|Where|Learned)\*\*:\s*/g, (_, k: string) => `**${FIELD[k]}:** `)
    .trim()
}

const FIELD: Record<string, string> = { What: 'Что', Why: 'Почему', Where: 'Где', Learned: 'Вывод' }

/** Short model names for chips: "codex:gpt-6-luna" → "Codex · gpt-6-luna". */
export function modelLabel(name: string): string {
  if (name.startsWith('codex:')) return `Codex · ${name.slice(6)}`
  if (name.startsWith('claude:')) return `Claude · ${name.slice(7).replace('claude-', '')}`
  return `Ollama · ${name}`
}
