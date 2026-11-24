import { cx } from "./atoms";

/** The ring's geometry in its 100 × 100 box: a stroke of 9 inside the edge. */
const R = 45.5;
const STROKE = 9;

export interface GaugeFaceProps {
  /** How much of the ring is filled, a whole percentage from 0 to 100 (the caller clamps and rounds it). */
  percent: number;
  /** The arc's CSS color and the track's: the caller's, so an extension's named color and a feature's own token draw the same ring. */
  color: string;
  track: string;
  /** What sits in the centre, and the unit under it. */
  centre: string;
  unit?: string;
  title: string;
  subtitle?: string;
  compact?: boolean;
}

/**
 * A ring filled to a ratio with a value and unit in its centre and its title below (an extension tile's gauge, UI
 * version 9, and Health's water and protein): the track in the caller's tint, the arc from the top clockwise to the
 * percentage, round ends. The arc eases to a new ratio when it is drawn again, and snaps with Reduce Motion (styles.css
 * strips transitions). What it says is read out by the caller's own label, so the ring itself is hidden from assistive
 * technology.
 */
export function GaugeFace({ percent, color, track, centre, unit, title, subtitle, compact }: GaugeFaceProps) {
  return (
    <span data-gauge className="flex min-w-0 flex-col items-center gap-0.5 p-3 text-center">
      <span className={cx("relative mb-1.5 grid shrink-0 place-items-center", compact ? "size-20" : "size-24")}>
        <svg viewBox="0 0 100 100" aria-hidden="true" className="absolute inset-0 size-full -rotate-90">
          <circle cx={50} cy={50} r={R} fill="none" stroke={track} strokeWidth={STROKE} />
          {percent > 0 && (
            <circle
              data-gauge-arc
              cx={50}
              cy={50}
              r={R}
              fill="none"
              stroke={color}
              strokeWidth={STROKE}
              strokeLinecap="round"
              pathLength={100}
              strokeDasharray="100 100"
              strokeDashoffset={100 - percent}
              className="transition-[stroke-dashoffset] duration-500 ease-out motion-reduce:transition-none"
            />
          )}
        </svg>
        <span className="relative flex max-w-[72%] min-w-0 flex-col items-center leading-tight">
          <span className={cx("max-w-full truncate font-semibold text-ink tabular-nums", compact ? "text-lg" : "text-xl")}>{centre}</span>
          {unit && <span className="max-w-full truncate text-submeta text-ink-3">{unit}</span>}
        </span>
      </span>
      <span className="max-w-full truncate text-meta font-medium text-ink">{title}</span>
      {subtitle && <span className="max-w-full truncate text-submeta text-ink-3">{subtitle}</span>}
    </span>
  );
}
