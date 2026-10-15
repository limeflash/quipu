import type { MemoryDay, MemoryProjectRow } from '../types'
import { ACCENT, TYPES, ago, compact, dayLabel, int, localDate, parseTime, records, stackKey, typeInfo, weekday, xml } from './format'
import { iconPaths, type IconName } from './icons'

/**
 * Charts as SVG strings for the `Svg` element. Colours are CSS variables with
 * a dark-scheme override, so one drawing reads in both themes; `<title>` gives
 * the hover tooltips an interactive Svg shows.
 */
const STYLE = `<style>
svg{--fg:#1f2328;--muted:#6b7280;--grid:#e8eaed;--tile:#f6f7f9;--track:#eceef1;--hl:#0f172a}
@media (prefers-color-scheme:dark){svg{--fg:#ececec;--muted:#9aa1a9;--grid:#2c3036;--tile:#202328;--track:#2b2f35;--hl:#ffffff}}
text{font-family:"Segoe UI",-apple-system,Inter,system-ui,sans-serif;fill:var(--fg)}
.m{fill:var(--muted)}.g{stroke:var(--grid)}.t{fill:var(--tile)}.k{fill:var(--track)}
.col .hl{fill:var(--hl);opacity:0}.col:hover .hl{opacity:.06}
.row:hover .name{font-weight:600}
</style>`

function svg(width: number, height: number, body: string, label: string): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}" role="img" aria-label="${xml(label)}">${STYLE}${body}</svg>`
}

/** An icon of `icons.ts` placed at (x, y), `size` units wide, in `color`. */
function icon(name: IconName, x: number, y: number, size: number, color: string): string {
  const paths = iconPaths(name).replace(/class="f"/g, `fill="${color}" stroke="none"`)
  return `<g transform="translate(${x} ${y}) scale(${size / 24})" fill="none" stroke="${color}" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${paths}</g>`
}

export type Tile = { icon: IconName; value: string; label: string; sub?: string; color: string }

/** A grid of tiles, `cols` across: an icon in a tinted square, a label, one big number. */
export function tilesSvg(tiles: Tile[], width: number, cols = 2): string {
  const gap = 8
  const h = 66
  const rows = Math.ceil(tiles.length / cols)
  const w = (width - gap * (cols - 1)) / cols
  const body = tiles
    .map((t, i) => {
      const x = (i % cols) * (w + gap)
      const y = Math.floor(i / cols) * (h + gap)
      return (
        `<g><title>${xml(`${t.label}: ${t.value}${t.sub ? ` — ${t.sub}` : ''}`)}</title>` +
        `<rect class="t" x="${x}" y="${y}" width="${w}" height="${h}" rx="12"/>` +
        `<rect x="${x + 12}" y="${y + 12}" width="26" height="26" rx="8" fill="${t.color}" opacity=".14"/>` +
        icon(t.icon, x + 17, y + 17, 16, t.color) +
        `<text x="${x + 46}" y="${y + 23}" font-size="11.5" class="m">${xml(t.label)}</text>` +
        `<text x="${x + 46}" y="${y + 43}" font-size="${valueSize(t.value, w - 58)}" font-weight="650">${xml(t.value)}</text>` +
        (t.sub ? `<text x="${x + 46}" y="${y + 58}" font-size="10.5" class="m">${xml(t.sub)}</text>` : '') +
        `</g>`
      )
    })
    .join('')
  return svg(width, rows * h + (rows - 1) * gap, body, tiles.map(t => `${t.label}: ${t.value}`).join('; '))
}

/** The big number's size: 20px, smaller as the text would outgrow its room. */
function valueSize(value: string, room: number): number {
  return Math.max(13, Math.min(20, Math.floor(room / Math.max(1, [...value].length * 0.58))))
}

/** A round number at or above n for an axis: 1, 2, 2.5, 5 × 10^k. */
export function niceMax(n: number): number {
  if (n <= 0) return 1
  const p = 10 ** Math.floor(Math.log10(n))
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= n) return m * p
  return 10 * p
}

/** Fourteen days: records stacked by type, model tokens as a line, a legend that wraps. */
export function activitySvg(days: MemoryDay[], width: number): string {
  const left = 30
  const right = 38
  const top = 10
  const plotH = 108
  const plotW = width - left - right
  const colW = plotW / Math.max(1, days.length)
  const barW = Math.min(18, colW * 0.62)
  const maxRec = niceMax(Math.max(1, ...days.map(d => d.records)))
  const tokens = days.map(d => d.tokens_in + d.tokens_out)
  const maxTok = niceMax(Math.max(1, ...tokens))
  const y = (v: number) => top + plotH - (v / maxRec) * plotH
  const yt = (v: number) => top + plotH - (v / maxTok) * plotH
  const order = [...TYPES.map(t => t.type), 'other']
  const present = new Set<string>()
  const colorOf = (t: string) => typeInfo(t === 'other' ? 'change' : t).color

  let body = ''
  for (const f of [0, 0.5, 1]) {
    const gy = top + plotH - f * plotH
    body += `<line class="g" x1="${left}" x2="${width - right}" y1="${gy}" y2="${gy}"${f ? ' stroke-dasharray="3 4"' : ''}/>`
    body += `<text x="${left - 6}" y="${gy + 3.5}" font-size="9.5" text-anchor="end" class="m">${compact(maxRec * f)}</text>`
    if (f) body += `<text x="${width - right + 5}" y="${gy + 3.5}" font-size="9.5" style="fill:${ACCENT}">${compact(maxTok * f)}</text>`
  }

  days.forEach((d, i) => {
    const cx = left + colW * i + colW / 2
    const stacked: Record<string, number> = {}
    for (const [type, n] of Object.entries(d.by_type)) stacked[stackKey(type)] = (stacked[stackKey(type)] ?? 0) + n
    const date = localDate(d.date)
    const parts = order.filter(t => stacked[t]).map(t => `${typeInfo(t).label} ${int(stacked[t] ?? 0)}`).join(', ')
    const tip = [
      `${weekday(date)}, ${dayLabel(date)}`,
      d.records ? `${records(d.records)}${parts ? `: ${parts}` : ''}` : 'записей нет',
      d.calls ? `вызовов модели: ${int(d.calls)}${d.errors ? ` (ошибок ${int(d.errors)})` : ''} · токенов: ${compact(tokens[i] ?? 0)}` : '',
    ].filter(Boolean).join('\n')
    body += `<g class="col"><title>${xml(tip)}</title><rect class="hl" x="${left + colW * i}" y="${top}" width="${colW}" height="${plotH}" rx="4"/>`
    let acc = 0
    const segs = order.filter(t => stacked[t])
    segs.forEach((t, k) => {
      present.add(t)
      const y0 = y(acc)
      acc += stacked[t] ?? 0
      const y1 = y(acc)
      const hgt = Math.max(0.5, y0 - y1)
      body += k === segs.length - 1
        ? `<path d="M${cx - barW / 2},${y0}V${y1 + Math.min(3, hgt)}q0,-3 3,-3h${barW - 6}q3,0 3,3V${y0}Z" fill="${colorOf(t)}"/>`
        : `<rect x="${cx - barW / 2}" y="${y1}" width="${barW}" height="${hgt}" fill="${colorOf(t)}"/>`
    })
    const isToday = i === days.length - 1
    if (isToday || i % 2 === (days.length - 1) % 2) {
      body += `<text x="${cx}" y="${top + plotH + 13}" font-size="9.5" text-anchor="middle"${isToday ? ' font-weight="650"' : ' class="m"'}>${isToday ? 'сег' : date.getDate()}</text>`
    }
    body += `</g>`
  })

  const pts = tokens.map((v, i) => [left + colW * i + colW / 2, yt(v)] as const)
  if (tokens.some(v => v > 0)) {
    body += `<polyline points="${pts.map(p => p.join(',')).join(' ')}" fill="none" stroke="${ACCENT}" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round" opacity=".9"/>`
    body += pts.map((p, i) => (tokens[i] ? `<circle cx="${p[0]}" cy="${p[1]}" r="2.2" fill="${ACCENT}"/>` : '')).join('')
  }

  // Legend, wrapped to the width: the types this window holds, then the line.
  const items = order.filter(t => present.has(t)).map(t => ({ color: colorOf(t), label: t === 'other' ? 'другое' : typeInfo(t).label, line: false }))
  items.push({ color: ACCENT, label: 'токены', line: true })
  let lx = 0
  let ly = top + plotH + 32
  for (const it of items) {
    const w = 14 + it.label.length * 5.9 + 12
    if (lx + w > width && lx > 0) {
      lx = 0
      ly += 16
    }
    body += it.line
      ? `<line x1="${lx}" x2="${lx + 10}" y1="${ly - 3.5}" y2="${ly - 3.5}" stroke="${it.color}" stroke-width="2" stroke-linecap="round"/>`
      : `<rect x="${lx}" y="${ly - 8}" width="8" height="8" rx="2" fill="${it.color}"/>`
    body += `<text x="${lx + 13}" y="${ly}" font-size="10" class="m">${it.label}</text>`
    lx += w
  }

  const total = days.reduce((a, d) => a + d.records, 0)
  return svg(width, ly + 6, body, `Активность за ${days.length} дней: ${records(total)}`)
}

/** A tiny bar line of the days for the band above the prompt; today in full colour. */
export function sparkSvg(days: MemoryDay[], width = 84, height = 18): string {
  const max = Math.max(1, ...days.map(d => d.records))
  const gap = 2
  const w = (width - gap * (days.length - 1)) / Math.max(1, days.length)
  const body = days
    .map((d, i) => {
      const h = d.records ? Math.max(2, (d.records / max) * height) : 1.5
      const last = i === days.length - 1
      return `<rect x="${i * (w + gap)}" y="${height - h}" width="${w}" height="${h}" rx="1.2" fill="${ACCENT}" opacity="${last ? 1 : d.records ? 0.45 : 0.18}"/>`
    })
    .join('')
  return svg(width, height, body, `За ${days.length} дней: ${records(days.reduce((a, d) => a + d.records, 0))}`)
}

/** One stacked bar of a project's records by type, with a wrapping legend of counts. */
export function typesSvg(types: Record<string, number>, width: number): string {
  const entries = Object.entries(types).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1])
  const total = entries.reduce((a, [, n]) => a + n, 0)
  let body = `<clipPath id="bar"><rect x="0" y="0" width="${width}" height="8" rx="4"/></clipPath><rect class="k" x="0" y="0" width="${width}" height="8" rx="4"/><g clip-path="url(#bar)">`
  let x = 0
  for (const [type, n] of entries) {
    const w = (n / Math.max(1, total)) * width
    const info = typeInfo(type)
    body += `<rect x="${x}" y="0" width="${Math.max(0, w - 1.5)}" height="8" fill="${info.color}"><title>${xml(`${info.label}: ${int(n)} (${Math.round((n / total) * 100)}%)`)}</title></rect>`
    x += w
  }
  body += '</g>'
  let lx = 0
  let ly = 26
  for (const [type, n] of entries) {
    const info = typeInfo(type)
    const label = `${info.label} ${int(n)}`
    const w = 12 + label.length * 6 + 12
    if (lx + w > width && lx > 0) {
      lx = 0
      ly += 16
    }
    body += `<circle cx="${lx + 4}" cy="${ly - 4}" r="3.6" fill="${info.color}"/><text x="${lx + 12}" y="${ly}" font-size="10.5" class="m">${xml(label)}</text>`
    lx += w
  }
  return svg(width, ly + 5, body, entries.map(([t, n]) => `${typeInfo(t).label} ${n}`).join(', '))
}

/** Projects by recent activity: name, a bar on a square-root scale, size and age. */
export function projectsSvg(rows: MemoryProjectRow[], current: string | undefined, now: number, width: number): string {
  const rowH = 24
  const nameW = Math.min(150, Math.round(width * 0.36))
  const tailW = 92
  const barMax = Math.max(40, width - nameW - tailW)
  const max = Math.sqrt(Math.max(1, ...rows.map(r => r.records)))
  const chars = Math.floor((nameW - 8) / 6.4)
  let body = ''
  rows.forEach((r, i) => {
    const y = i * rowH
    const isCurrent = r.name === current
    const w = Math.max(3, (Math.sqrt(r.records) / max) * barMax)
    const capW = r.records ? (w * r.captured) / r.records : 0
    const last = parseTime(r.last_at)
    const tip = `${r.name}${isCurrent ? ' (этот проект)' : ''}\n${records(r.records)}: автозахват ${int(r.captured)}, импорт и прочие ${int(r.records - r.captured)}\nпоследняя запись: ${ago(last, now)} назад`
    body +=
      `<g class="row"><title>${xml(tip)}</title>` +
      `<text class="name" x="0" y="${y + 15}" font-size="11.5"${isCurrent ? ` font-weight="650" style="fill:${ACCENT}"` : ''}>${xml(r.name.length > chars ? r.name.slice(0, chars - 1) + '…' : r.name)}</text>` +
      `<rect class="k" x="${nameW}" y="${y + 6}" width="${w}" height="11" rx="3"/>` +
      (capW > 0 ? `<rect x="${nameW}" y="${y + 6}" width="${Math.max(2, capW)}" height="11" rx="3" fill="${isCurrent ? ACCENT : '#10B981'}" opacity=".9"/>` : '') +
      `<text x="${width - tailW + 8}" y="${y + 15}" font-size="11">${compact(r.records)}</text>` +
      `<text x="${width}" y="${y + 15}" font-size="10.5" text-anchor="end" class="m">${ago(last, now)}</text>` +
      `</g>`
  })
  return svg(width, rows.length * rowH + 2, body, `Проекты: ${rows.map(r => r.name).join(', ')}`)
}
