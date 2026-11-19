// How many (#4538), in its own module rather than atoms: only the Inbox's section bar draws it so far (#9053), and atoms
// is on every first screen. #4653 brings it to the sidebar and its other counts; whichever of the two lands second keeps
// one copy.
import { cx } from "./atoms";

export type CountTone = "needs" | "neutral" | "accent";
const COUNT_TONE: Record<CountTone, string> = {
  // the web's Needs-you counts as they are drawn today (the sidebar's, SidebarParts BADGE_CLASS.attention); #4653 turns
  // every one of them to the Needs-you blue of DECISIONS 11-11 at once, this one with them
  needs: "bg-field text-attention",
  neutral: "bg-field text-ink-2",
  accent: "bg-accent-ink text-surface",
};

/** How many (#4538): `needs` counts what waits for the user, `neutral` a passive
 *  total, `accent` the selected item's count or a mention. Nothing at zero, "99+" past 99. `dense`: 16 px for a dense row,
 *  else 18. `label`: what a screen reader hears in place of the digits (by default the number, exact past 99); `false`
 *  where the host control's own label already says it. */
export function CountBadge({ count, tone = "needs", dense = false, label, className, ...rest }: { count: number; tone?: CountTone; dense?: boolean; label?: string | false; className?: string } & Omit<React.HTMLAttributes<HTMLSpanElement>, "children">) {
  if (!(count > 0)) return null;
  const capped = count > 99;
  const spoken = label === false ? undefined : (label ?? (capped ? String(count) : undefined));
  return (
    <>
      <span
        {...rest}
        data-count-badge={tone}
        aria-hidden={label === false || spoken ? true : undefined}
        className={cx("flex shrink-0 items-center justify-center whitespace-nowrap rounded-full px-1 font-semibold tabular-nums", dense ? "h-4 min-w-4 text-[10px]" : "h-4.5 min-w-4.5 text-[10.5px]", COUNT_TONE[tone], className)}
      >
        {capped ? "99+" : count}
      </span>
      {spoken && <span className="sr-only">{spoken}</span>}
    </>
  );
}
