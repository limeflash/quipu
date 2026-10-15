/**
 * Line icons on a 24-unit grid, drawn for this mod: one stroke weight, round
 * caps, filled dots where a mark needs weight. `paths` goes inside a chart's
 * own SVG; `iconSvg` makes a standalone icon for an `Svg` element.
 */
const PATHS = {
  // A quipu: the cord the project is named after, three strands, knots.
  quipu: '<path d="M3 5.6c4 1.1 14 1.1 18 0"/><path d="M7 6.6v11.5M12 6.9v13.6M17 6.6v8.6"/>' +
    '<circle cx="7" cy="11.2" r="1.7" class="f"/><circle cx="12" cy="10.2" r="1.7" class="f"/>' +
    '<circle cx="12" cy="15.8" r="1.7" class="f"/><circle cx="17" cy="12.4" r="1.7" class="f"/>',
  trend: '<path d="M3 17l6-6 4 4 8-8"/><path d="M15 7h6v6"/>',
  model: '<rect x="6" y="6" width="12" height="12" rx="2.5"/><path d="M10 10h4v4h-4z"/>' +
    '<path d="M9.5 2.5v3M14.5 2.5v3M9.5 18.5v3M14.5 18.5v3M2.5 9.5h3M2.5 14.5h3M18.5 9.5h3M18.5 14.5h3"/>',
  queue: '<path d="M21.5 12.5h-5.5l-2 3h-4l-2-3H2.5"/>' +
    '<path d="M5.6 5h12.8l3.1 7.5v5A2.5 2.5 0 0 1 19 20H5a2.5 2.5 0 0 1-2.5-2.5v-5z"/>',
  activity: '<path d="M4 20.5V13M9.3 20.5V5M14.6 20.5v-9M19.9 20.5V9"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2.2 2.5H19a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  layers: '<path d="M12 3.5l9 4.8-9 4.8-9-4.8z"/><path d="M3 13.2l9 4.8 9-4.8"/>',
  database: '<ellipse cx="12" cy="5.5" rx="8" ry="2.8"/><path d="M4 5.5v13c0 1.5 3.6 2.8 8 2.8s8-1.3 8-2.8v-13"/>' +
    '<path d="M4 12c0 1.5 3.6 2.8 8 2.8s8-1.3 8-2.8"/>',
  session: '<circle cx="12" cy="12" r="8.6"/><path d="M12 7.2V12l3.2 2"/>',
  note: '<path d="M6.5 2.8h8l4 4v13.7a.7.7 0 0 1-.7.7H6.5a.7.7 0 0 1-.7-.7V3.5a.7.7 0 0 1 .7-.7z"/><path d="M14 2.8v4.5h4.5M9 13h6.5M9 16.8h4.5"/>',
} as const

export type IconName = keyof typeof PATHS

/** The paths of one icon, to be placed inside a chart's SVG under a transform. */
export function iconPaths(name: IconName): string {
  return PATHS[name]
}

const MUTED = '#6b7280'
const MUTED_DARK = '#9aa1a9'

/**
 * A standalone icon. Without `color` it takes the muted tone of the current
 * theme, so it sits beside dim text in light and dark alike.
 */
export function iconSvg(name: IconName, color?: string, size = 16): string {
  const style = color
    ? `svg{--c:${color}}`
    : `svg{--c:${MUTED}}@media (prefers-color-scheme:dark){svg{--c:${MUTED_DARK}}}`
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="${size}" height="${size}">` +
    `<style>${style}.q{fill:none;stroke:var(--c);stroke-width:2;stroke-linecap:round;stroke-linejoin:round}.q .f{fill:var(--c);stroke:none}</style>` +
    `<g class="q">${PATHS[name]}</g></svg>`
  )
}
