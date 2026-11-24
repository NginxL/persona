// Pace is only shown in the lazily loaded usage popover.
import { windowMinutes, type UsageAccount, type UsageWindow } from "./usage-windows";

const PACE_QUIET_FRACTION = 0.05;
const PACE_TOLERANCE = 5;

export type PaceState = "over" | "under" | "on";
interface Pace {
  /** Where use would be if it were spread evenly over the window, 0-100. */
  expected: number;
  state: PaceState;
}

/** Compares actual use with an even spread and allows a five-point difference. The first 5% of a window has no pace. */
export function pace(w: UsageWindow, now = Date.now()): Pace | null {
  const total = windowMinutes(w) * 60_000;
  const resets = w.resets_at ? Date.parse(w.resets_at) : NaN;
  if (!(total > 0) || !Number.isFinite(resets)) return null;
  const elapsed = total - (resets - now);
  if (elapsed < 0 || elapsed > total) return null;
  if (elapsed / total < PACE_QUIET_FRACTION) return null;
  const expected = (elapsed / total) * 100;
  const diff = w.used_percent - expected;
  return { expected, state: diff > PACE_TOLERANCE ? "over" : diff < -PACE_TOLERANCE ? "under" : "on" };
}

/** A stale account still has usage and reset information. Its pace is unavailable (#12411). */
export function accountPace(a: Pick<UsageAccount, "stale">, w: UsageWindow, now = Date.now()): Pace | null {
  return a.stale ? null : pace(w, now);
}
