// UI version 9 (#9337, docs/extensions/extensions.md "UI version 9"): a tile's gauge, a ring filled to a ratio with the
// tile's value and unit in its centre and its title below, for a quota or a usage. A chunk of its own, loaded with the
// first tile that has one (ViewBody6.tsx TileView): an extension's page or card, never the first screen or the idle
// preload. TileView keeps the tile's surface, its press, badge, switch and menu; this draws what is inside.
import { hue, hueTint, type UITile } from "../../lib/extension-ui-v6";
import { GaugeFace as Ring } from "../../ui/GaugeFace";
import { gaugePercent, gaugeUnit } from "./ViewBody6";

/**
 * A gauge tile's face: the ring (ui/GaugeFace.tsx) in the tile's named colors (the gauge's, else the tile's, else the
 * accent), the value and unit in its centre (the percentage when the tile has no value), the title and subtitle below.
 * What it says is read out by TileView's label.
 */
export function GaugeFace({ tile, compact }: { tile: UITile; compact?: boolean }) {
  const g = tile.gauge!;
  const pct = gaugePercent(g.ratio);
  return (
    <Ring
      percent={pct}
      color={hue(g.color, hue(tile.color, "var(--accent)"))}
      track={hueTint(g.color, hueTint(tile.color, "color-mix(in srgb, var(--accent) 14%, transparent)"))}
      centre={tile.value || `${pct}%`}
      unit={tile.value ? gaugeUnit(g) : undefined}
      title={tile.title}
      subtitle={tile.subtitle}
      compact={compact}
    />
  );
}
