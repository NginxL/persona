import { contextTooltip, contextWarn, type ContextUsage } from "../lib/context-usage";
import { cx, EASE } from "./atoms";

export function ContextUsageBar({ usage, className }: { usage: ContextUsage; className: string }) {
  return (
    <span
      role="progressbar"
      aria-label={contextTooltip(usage)}
      aria-valuenow={usage.used}
      aria-valuemin={0}
      aria-valuemax={100}
      className={cx("block shrink-0 overflow-hidden rounded-full bg-hover-2", className)}
    >
      <span
        className={cx("block h-full rounded-full motion-safe:transition-[width] motion-safe:duration-300", contextWarn(usage) ? "bg-orange" : "bg-accent")}
        style={{ width: `${usage.used}%`, transitionTimingFunction: EASE }}
      />
    </span>
  );
}
