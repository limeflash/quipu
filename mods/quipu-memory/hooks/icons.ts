/**
 * Line icons on a 24-unit grid, drawn for this mod: one stroke weight, round
 * caps, filled dots where a mark needs weight. Colours are attributes, not CSS,
 * so the icon draws the same wherever the surface puts the markup.
 */
const PATHS = {
  // A quipu: the cord the project is named after, three strands, knots.
  quipu: '<path d="M3 5.6c4 1.1 14 1.1 18 0"/><path d="M7 6.6v11.5M12 6.9v13.6M17 6.6v8.6"/>' +
    '<circle cx="7" cy="11.2" r="1.7" class="f"/><circle cx="12" cy="10.2" r="1.7" class="f"/>' +
    '<circle cx="12" cy="15.8" r="1.7" class="f"/><circle cx="17" cy="12.4" r="1.7" class="f"/>',
  trend: '<path d="M3 17l6-6 4 4 8-8"/><path d="M15 7h6v6"/>',
  layers: '<path d="M12 3.5l9 4.8-9 4.8-9-4.8z"/><path d="M3 13.2l9 4.8 9-4.8"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2.2 2.5H19a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  disk: '<rect x="2.5" y="13" width="19" height="7.5" rx="2"/><path d="M4.6 13l2.5-6.9A2.4 2.4 0 0 1 9.4 4.5h5.2a2.4 2.4 0 0 1 2.3 1.6l2.5 6.9"/>' +
    '<circle cx="6.8" cy="16.75" r="1.3" class="f"/>',
  session: '<circle cx="12" cy="12" r="8.6"/><path d="M12 7.2V12l3.2 2"/>',
} as const

export type IconName = keyof typeof PATHS

/** One icon as a standalone SVG for an `Svg` element; muted grey by default. */
export function iconSvg(name: IconName, color = '#8B919A', size = 16): string {
  const paths = PATHS[name].replace(/class="f"/g, `fill="${color}" stroke="none"`)
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="${size}" height="${size}">` +
    `<g fill="none" stroke="${color}" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${paths}</g></svg>`
  )
}
