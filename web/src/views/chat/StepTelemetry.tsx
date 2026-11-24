// Lazy-loaded L3 per-step attribution badges shown only when a step card is expanded (#13054).
// Displays honest per-step token consumption, elapsed duration, and optional TTFT from reported telemetry.
import { useMemo } from "react";
import { t } from "../../i18n";
import { formatLatencyMs, summarizeStepTelemetry } from "../../lib/session-insights";
import { compactTokens } from "../../lib/tokens";
import type { Item } from "../../lib/types";
import { workDuration } from "../../lib/work-groups";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

function formatStepDuration(ms: number): string {
  return ms < 10_000 ? formatLatencyMs(ms) : workDuration(ms);
}

export function StepTelemetry({
  items,
  durationMs,
  className,
}: {
  items: readonly (Item | undefined)[];
  durationMs?: number | null;
  className?: string;
}) {
  const telemetry = useMemo(() => summarizeStepTelemetry(items, durationMs), [items, durationMs]);
  if (!telemetry) return null;

  const tokenDetail = telemetry.tokens
    ? [
        t("chat-sessioninsights.step_tokens_detail", {
          input: compactTokens(telemetry.tokens.input),
          output: compactTokens(telemetry.tokens.output),
        }),
        telemetry.tokens.hasCache
          ? t("chat-sessioninsights.step_tokens_cache", {
              read: compactTokens(telemetry.tokens.cacheRead),
              write: compactTokens(telemetry.tokens.cacheWrite),
            })
          : "",
      ]
        .filter(Boolean)
        .join(" · ")
    : undefined;

  return (
    <div
      data-step-telemetry
      className={cx("flex flex-wrap items-center gap-1.5 font-sans text-submeta text-ink-3 tabular-nums", className)}
    >
      {telemetry.tokens && (
        <span
          data-step-tokens
          title={tokenDetail}
          className="inline-flex items-center gap-1 rounded-chip bg-hover-2 px-1.5 py-0.5 font-medium text-ink-2"
        >
          <Icon name="layers" size={11} className="shrink-0 text-ink-3" />
          <span>{t("chat-sessioninsights.step_tokens", { tokens: compactTokens(telemetry.tokens.total) })}</span>
        </span>
      )}
      {telemetry.durationMs !== null && (
        <span
          data-step-duration
          className="inline-flex items-center gap-1 rounded-chip bg-hover-2 px-1.5 py-0.5 font-medium text-ink-2"
        >
          <Icon name="clock" size={11} className="shrink-0 text-ink-3" />
          <span>{formatStepDuration(telemetry.durationMs)}</span>
        </span>
      )}
      {telemetry.ttftMs !== null && (
        <span
          data-step-ttft
          className="inline-flex items-center gap-1 rounded-chip bg-hover-2 px-1.5 py-0.5 font-medium text-ink-2"
        >
          <span>{t("chat-sessioninsights.step_ttft", { ttft: formatLatencyMs(telemetry.ttftMs) })}</span>
        </span>
      )}
    </div>
  );
}
