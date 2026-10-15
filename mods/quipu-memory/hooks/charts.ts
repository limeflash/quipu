import type { MemoryDay, MemoryProjectRow } from '../types'
import { ACCENT, TYPES, ago, compact, dayLabel, int, localDate, parseTime, records, stackKey, typeInfo, weekday, xml } from './format'

/**
 * Charts as SVG strings for the desktop's `Svg` element. Colours come from CSS
 * variables with a dark-scheme override, so one drawing reads in both themes;
 * `<title>` gives the hover tooltips an interactive Svg shows.
 */
const STYLE = `<style>
svg{--fg:#1f2328;--muted:#6b7280;--grid:#e6e8eb;--tile:#f5f6f8;--track:#eceef1;--hl:#0f172a}
@media (prefers-color-scheme:dark){svg{--fg:#e7e7e7;--muted:#9aa1a9;--grid:#2c3036;--tile:#1d2025;--track:#2a2e34;--hl:#ffffff}}
text{font-family:"Segoe UI",-apple-system,Inter,system-ui,sans-serif;fill:var(--fg)}
.m{fill:var(--muted)}.g{stroke:var(--grid)}.t{fill:var(--tile)}.k{fill:var(--track)}
.col .hl{fill:var(--hl);opacity:0}.col:hover .hl{opacity:.06}
.row:hover .name{font-weight:600}
</style>`

function svg(width: number, height: number, body: string, label: string): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}" role="img" aria-label="${xml(label)}">${STYLE}${body}</svg>`
}

export type Tile = { value: string; label: string; sub?: string; tone?: 'ok' | 'warn' | 'bad' | 'accent' }

const TONE = { ok: '#10B981', warn: '#F59E0B', bad: '#EF4444', accent: ACCENT }

/** A row of tiles with one big number each. */
export function tilesSvg(tiles: Tile[], width = 640): string {
  const gap = 10
  const h = 78
  const w = (width - gap * (tiles.length - 1)) / tiles.length
  const body = tiles
    .map((t, i) => {
      const x = i * (w + gap)
      const tone = t.tone ? TONE[t.tone] : undefined
      return (
        `<g><title>${xml(`${t.label}: ${t.value}${t.sub ? ` — ${t.sub}` : ''}`)}</title>` +
        `<rect class="t" x="${x}" y="0" width="${w}" height="${h}" rx="10"/>` +
        (tone ? `<rect x="${x}" y="0" width="3" height="${h}" rx="1.5" fill="${tone}"/>` : '') +
        `<text x="${x + 14}" y="20" font-size="11.5" class="m">${xml(t.label)}</text>` +
        `<text x="${x + 14}" y="48" font-size="${valueSize(t.value, w)}" font-weight="600"${tone ? ` style="fill:${tone}"` : ''}>${xml(t.value)}</text>` +
        (t.sub ? `<text x="${x + 14}" y="67" font-size="11" class="m">${xml(t.sub)}</text>` : '') +
        `</g>`
      )
    })
    .join('')
  return svg(width, h, body, tiles.map(t => `${t.label} ${t.value}`).join(', '))
}

/** The big number's size: 24px, smaller as the text would outgrow its tile. */
function valueSize(value: string, tileW: number): number {
  const room = tileW - 24
  return Math.max(15, Math.min(24, Math.floor(room / Math.max(1, [...value].length * 0.6))))
}

/** A round number at or above n for an axis: 1, 2, 5 × 10^k. */
export function niceMax(n: number): number {
  if (n <= 0) return 1
  const p = 10 ** Math.floor(Math.log10(n))
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= n) return m * p
  return 10 * p
}

/** Fourteen days: records stacked by type, model tokens as a line. */
export function activitySvg(days: MemoryDay[], width = 640): string {
  const H = 236
  const left = 34
  const right = 46
  const top = 14
  const plotH = 150
  const plotW = width - left - right
  const colW = plotW / Math.max(1, days.length)
  const barW = Math.min(26, colW * 0.62)
  const maxRec = niceMax(Math.max(1, ...days.map(d => d.records)))
  const tokens = days.map(d => d.tokens_in + d.tokens_out)
  const maxTok = niceMax(Math.max(1, ...tokens))
  const y = (v: number) => top + plotH - (v / maxRec) * plotH
  const yt = (v: number) => top + plotH - (v / maxTok) * plotH
  const order = [...TYPES.map(t => t.type), 'other']
  const present = new Set<string>()

  let body = ''
  for (const f of [0, 0.5, 1]) {
    const gy = top + plotH - f * plotH
    body += `<line class="g" x1="${left}" x2="${width - right}" y1="${gy}" y2="${gy}" stroke-dasharray="${f ? '3 4' : ''}"/>`
    body += `<text x="${left - 6}" y="${gy + 4}" font-size="10" text-anchor="end" class="m">${compact(maxRec * f)}</text>`
    body += `<text x="${width - right + 6}" y="${gy + 4}" font-size="10" class="m" style="fill:${ACCENT}">${f ? compact(maxTok * f) : '0'}</text>`
  }

  days.forEach((d, i) => {
    const cx = left + colW * i + colW / 2
    const stacked: Record<string, number> = {}
    for (const [type, n] of Object.entries(d.by_type)) stacked[stackKey(type)] = (stacked[stackKey(type)] ?? 0) + n
    const date = localDate(d.date)
    const parts = order
      .filter(t => stacked[t])
      .map(t => `${typeInfo(t).label} ${int(stacked[t] ?? 0)}`)
      .join(', ')
    const tip = [
      `${weekday(date)}, ${dayLabel(date)}`,
      d.records ? `${records(d.records)}${parts ? `: ${parts}` : ''}` : 'записей нет',
      d.calls ? `вызовов модели: ${int(d.calls)}${d.errors ? ` (ошибок ${int(d.errors)})` : ''} · токенов: ${compact(tokens[i] ?? 0)}` : '',
    ]
      .filter(Boolean)
      .join('\n')
    body += `<g class="col"><title>${xml(tip)}</title><rect class="hl" x="${left + colW * i}" y="${top}" width="${colW}" height="${plotH}" rx="4"/>`
    let acc = 0
    const segs = order.filter(t => stacked[t])
    segs.forEach((t, k) => {
      present.add(t)
      const y0 = y(acc)
      acc += stacked[t] ?? 0
      const y1 = y(acc)
      const isTop = k === segs.length - 1
      const hgt = Math.max(0.5, y0 - y1)
      body += isTop
        ? `<path d="M${cx - barW / 2},${y0}V${y1 + Math.min(3, hgt)}q0,-3 3,-3h${barW - 6}q3,0 3,3V${y0}Z" fill="${typeInfo(t === 'other' ? 'change' : t).color}"/>`
        : `<rect x="${cx - barW / 2}" y="${y1}" width="${barW}" height="${hgt}" fill="${typeInfo(t === 'other' ? 'change' : t).color}"/>`
    })
    const isToday = i === days.length - 1
    body += `<text x="${cx}" y="${top + plotH + 15}" font-size="10.5" text-anchor="middle"${isToday ? ' font-weight="600"' : ' class="m"'}>${date.getDate()}</text>`
    body += `<text x="${cx}" y="${top + plotH + 28}" font-size="9.5" text-anchor="middle" class="m">${weekday(date)}</text>`
    body += `</g>`
  })

  const pts = tokens.map((v, i) => `${left + colW * i + colW / 2},${yt(v)}`)
  if (tokens.some(v => v > 0)) {
    body += `<polyline points="${pts.join(' ')}" fill="none" stroke="${ACCENT}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" opacity=".9"/>`
    body += pts.map((p, i) => (tokens[i] ? `<circle cx="${p.split(',')[0]}" cy="${p.split(',')[1]}" r="2.6" fill="${ACCENT}"/>` : '')).join('')
  }

  // Legend: the types this window holds, then the line.
  let lx = left
  const ly = H - 12
  for (const t of order.filter(t => present.has(t))) {
    const info = t === 'other' ? { color: typeInfo('change').color, label: 'другое' } : typeInfo(t)
    body += `<rect x="${lx}" y="${ly - 8}" width="9" height="9" rx="2" fill="${info.color}"/><text x="${lx + 13}" y="${ly}" font-size="10.5" class="m">${info.label}</text>`
    lx += 13 + info.label.length * 6.2 + 12
  }
  body += `<line x1="${lx}" x2="${lx + 14}" y1="${ly - 3.5}" y2="${ly - 3.5}" stroke="${ACCENT}" stroke-width="2"/><text x="${lx + 18}" y="${ly}" font-size="10.5" class="m">токены моделей</text>`

  const total = days.reduce((a, d) => a + d.records, 0)
  return svg(width, H, body, `Активность за ${days.length} дней: ${records(total)}`)
}

/** One stacked bar of a project's records by type, with a legend of counts. */
export function typesSvg(types: Record<string, number>, width = 640): string {
  const entries = Object.entries(types)
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1])
  const total = entries.reduce((a, [, n]) => a + n, 0)
  const H = 46
  let body = `<rect class="k" x="0" y="0" width="${width}" height="10" rx="5"/>`
  let x = 0
  body += `<clipPath id="bar"><rect x="0" y="0" width="${width}" height="10" rx="5"/></clipPath><g clip-path="url(#bar)">`
  for (const [type, n] of entries) {
    const w = (n / Math.max(1, total)) * width
    const info = typeInfo(type)
    body += `<rect x="${x}" y="0" width="${Math.max(0, w - 1.5)}" height="10" fill="${info.color}"><title>${xml(`${info.label}: ${int(n)} (${Math.round((n / total) * 100)}%)`)}</title></rect>`
    x += w
  }
  body += '</g>'
  let lx = 0
  for (const [type, n] of entries) {
    const info = typeInfo(type)
    const label = `${info.label} ${int(n)}`
    if (lx + label.length * 6.4 + 16 > width) break
    body += `<circle cx="${lx + 4}" cy="31" r="4" fill="${info.color}"/><text x="${lx + 12}" y="35" font-size="11" class="m">${xml(label)}</text>`
    lx += 12 + label.length * 6.4 + 14
  }
  return svg(width, H, body, entries.map(([t, n]) => `${typeInfo(t).label} ${n}`).join(', '))
}

/** Projects by recent activity: name, a bar on a square-root scale, size and age. */
export function projectsSvg(rows: MemoryProjectRow[], current: string | undefined, now: number, width = 640): string {
  const rowH = 26
  const H = rows.length * rowH + 4
  const nameW = 168
  const tailW = 120
  const barMax = width - nameW - tailW
  const max = Math.sqrt(Math.max(1, ...rows.map(r => r.records)))
  let body = ''
  rows.forEach((r, i) => {
    const y = i * rowH + 4
    const isCurrent = r.name === current
    const w = Math.max(3, (Math.sqrt(r.records) / max) * barMax)
    const capW = r.records ? (w * r.captured) / r.records : 0
    const last = parseTime(r.last_at)
    const tip = `${r.name}${isCurrent ? ' (этот проект)' : ''}\n${records(r.records)}: автозахват ${int(r.captured)}, импорт и прочие ${int(r.records - r.captured)}\nпоследняя запись: ${ago(last, now)} назад`
    body +=
      `<g class="row"><title>${xml(tip)}</title>` +
      `<text class="name" x="0" y="${y + 13}" font-size="12"${isCurrent ? ` font-weight="600" style="fill:${ACCENT}"` : ''}>${xml(r.name.length > 24 ? r.name.slice(0, 23) + '…' : r.name)}</text>` +
      `<rect class="k" x="${nameW}" y="${y + 4}" width="${w}" height="12" rx="3"/>` +
      (capW > 0 ? `<rect x="${nameW}" y="${y + 4}" width="${Math.max(2, capW)}" height="12" rx="3" fill="${isCurrent ? ACCENT : '#10B981'}" opacity=".9"/>` : '') +
      `<text x="${width - tailW + 10}" y="${y + 13}" font-size="11.5">${compact(r.records)}</text>` +
      `<text x="${width}" y="${y + 13}" font-size="11" text-anchor="end" class="m">${ago(last, now)}</text>` +
      `</g>`
  })
  return svg(width, H, body, `Проекты: ${rows.map(r => r.name).join(', ')}`)
}
