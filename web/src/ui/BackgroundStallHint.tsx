import { backgroundStall } from "../lib/background-work";
import type { Session } from "../lib/types";
import { cx, useNow } from "./atoms";
import { Icon } from "./icons";

/** A muted line on a Working Agent whose background commands look stuck (#13744): "Background commands quiet for 3 hr".
 *  The Agent still counts as Working; the tooltip says so. Nothing when the hub reports no stall. */
export function BackgroundStallHint({ session, className }: { session: Session; className?: string }) {
  const now = useNow(60_000, Boolean(session.background_stalled_since));
  const stall = backgroundStall(session, now);
  if (!stall) return null;
  return (
    <span data-bg-stalled title={stall.title} className={cx("flex min-w-0 items-center gap-1 text-ink-3", className)}>
      <Icon name="clock" size={11} stroke={2} className="shrink-0" />
      <span className="truncate">{stall.text}</span>
    </span>
  );
}
