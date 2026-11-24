// Lazy-loaded Session Insights popover opened from ContextMeter (#12541, #13054).
// Shows context window usage with stacked ContextBreakdown bar (exact/~estimated badge, auto-compact marker,
// top consumer hint), cumulative session token & cache summary, per-model breakdown, performance metrics
// (turn duration, tok/s, TTFT), and one-click /compact.
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type RefObject } from "react";
import { createPortal } from "react-dom";
import { formatNumber, t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { COMPACT_FEATURE, compactBody, compactPath, formatTokens } from "../../lib/compaction";
import { contextHint } from "../../lib/context-notes";
import { contextTier, contextTooltip, currentContextTokens, type ContextTier } from "../../lib/context-usage";
import { useFeed } from "../../lib/feed";
import {
  buildSessionInsights,
  compactActionSupport,
  formatHitRate,
  formatLatencyMs,
  type ContextBreakdownKey,
  type SessionContextBreakdown,
} from "../../lib/session-insights";
import { hubHas, useStore } from "../../lib/store";
import { formatRate, type RateHistory } from "../../lib/token-rate";
import { compactTokens, USAGE_COLOR } from "../../lib/tokens";
import type { ContextBreakdown, Item, Session } from "../../lib/types";
import { workDuration } from "../../lib/work-groups";
import { cx, EASE, toast } from "../../ui/atoms";
import { ContextUsageBar } from "../../ui/ContextUsageBar";
import { Icon } from "../../ui/icons";
import { placeMenu, useEscapeClose, useModalFocus } from "../../ui/overlay";

const COMPACT_REQUESTED = "persona:compact-requested";
const POPOVER_WIDTH = 340;
const EMPTY_ITEMS: readonly Item[] = [];
const TIER_TEXT: Record<ContextTier, string> = { ok: "text-ink", warn: "text-orange", danger: "text-orange" };

/** Color-blind-validated categorical tokens from styles.css (--usage-*), paired for light & dark modes. */
const BREAKDOWN_COLOR: Record<ContextBreakdownKey, string> = {
  system_and_rules: USAGE_COLOR.cache_write,
  tools_and_mcp: USAGE_COLOR.in,
  conversation: USAGE_COLOR.cache_read,
  tool_outputs: USAGE_COLOR.out,
};

const BREAKDOWN_LABEL_KEY: Record<ContextBreakdownKey, string> = {
  system_and_rules: "chat-sessioninsights.breakdown_system_and_rules",
  tools_and_mcp: "chat-sessioninsights.breakdown_tools_and_mcp",
  conversation: "chat-sessioninsights.breakdown_conversation",
  tool_outputs: "chat-sessioninsights.breakdown_tool_outputs",
};

const formatShare = (pct: number): string => `${Math.round(Math.min(100, Math.max(0, pct)))}%`;

export interface SessionInsightsPopoverProps {
  anchor: RefObject<HTMLElement | null>;
  session: Session;
  items?: readonly Item[];
  assistant?: boolean;
  onCompact?: () => boolean | void | Promise<boolean | void | unknown>;
  compactBlocked?: boolean;
  onClose: () => void;
  /** Optional overrides for tests. */
  rateHistory?: RateHistory | null;
  breakdown?: ContextBreakdown | null;
  ttftMs?: number | null;
}

async function requestAssistantCompaction(session: Session): Promise<boolean> {
  try {
    await api(compactPath(session.session_id), { method: "POST", json: compactBody(session.machine_id) });
    window.dispatchEvent(new CustomEvent(COMPACT_REQUESTED, { detail: { mid: session.machine_id, sid: session.session_id } }));
    return true;
  } catch (e) {
    toast(t("assistant-assistantchat.compact_start_failed", { error: errorText(e) }), "error");
    return false;
  }
}

function StackedBreakdownBar({ breakdown }: { breakdown: SessionContextBreakdown }) {
  const markerShare = breakdown.autoCompactShare !== null ? Math.min(99, Math.max(1, breakdown.autoCompactShare)) : null;
  const markerLabel =
    breakdown.autoCompactThreshold !== null && breakdown.autoCompactShare !== null
      ? t("chat-sessioninsights.breakdown_auto_compact_at", {
          tokens: compactTokens(breakdown.autoCompactThreshold),
          percent: Math.round(breakdown.autoCompactShare),
        })
      : undefined;

  return (
    <div className="relative h-2 w-full" data-insights-stacked-bar>
      <div className="flex h-full w-full overflow-hidden rounded-full bg-hover-2">
        {breakdown.segments.map((seg) => {
          if (seg.tokens <= 0 || seg.windowShare <= 0) return null;
          const label = t(BREAKDOWN_LABEL_KEY[seg.key]);
          const tokText = breakdown.source === "estimated"
            ? t("chat-sessioninsights.breakdown_estimated_tokens", { tokens: compactTokens(seg.tokens) })
            : compactTokens(seg.tokens);
          return (
            <div
              key={seg.key}
              data-breakdown-segment={seg.key}
              title={`${label}: ${tokText} (${formatShare(seg.windowShare)})`}
              className="h-full shrink-0 first:rounded-l-full last:rounded-r-full motion-safe:transition-[width] motion-safe:duration-300"
              style={{ width: `${seg.windowShare}%`, backgroundColor: BREAKDOWN_COLOR[seg.key], transitionTimingFunction: EASE }}
            />
          );
        })}
      </div>
      {markerShare !== null && markerLabel && (
        <span
          role="img"
          aria-label={markerLabel}
          title={markerLabel}
          data-breakdown-compact-marker
          className="pointer-events-auto absolute -top-0.5 -bottom-0.5 w-0.5 -translate-x-1/2 rounded-full bg-ink"
          style={{ left: `${markerShare}%` }}
        />
      )}
    </div>
  );
}

function BreakdownCard({ breakdown }: { breakdown: SessionContextBreakdown }) {
  const estimated = breakdown.source === "estimated";
  const formatCatTokens = (n: number) =>
    estimated ? t("chat-sessioninsights.breakdown_estimated_tokens", { tokens: compactTokens(n) }) : compactTokens(n);

  return (
    <div className="rounded-lg bg-inset px-2.5 py-2" data-insights-breakdown>
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <span className="font-medium text-ink-2">{t("chat-sessioninsights.breakdown_section")}</span>
        <span
          data-breakdown-source={breakdown.source}
          className={cx(
            "shrink-0 rounded-chip px-1.5 py-0.5 text-submeta font-medium",
            estimated ? "bg-hover-2 text-ink-3" : "bg-green-tint text-green",
          )}
        >
          {t(estimated ? "chat-sessioninsights.breakdown_source_estimated" : "chat-sessioninsights.breakdown_source_exact")}
        </span>
      </div>

      <dl className="flex flex-col gap-1 tabular-nums">
        {breakdown.segments.map((seg) => {
          const isTop = breakdown.topConsumer?.key === seg.key;
          return (
            <div key={seg.key} className="flex items-center justify-between gap-2" data-breakdown-row={seg.key}>
              <dt className="flex min-w-0 items-center gap-1.5 text-ink-3">
                <span
                  aria-hidden
                  className="size-2 shrink-0 rounded-full"
                  style={{ backgroundColor: BREAKDOWN_COLOR[seg.key] }}
                />
                <span className={cx("truncate", isTop && "font-medium text-ink")}>{t(BREAKDOWN_LABEL_KEY[seg.key])}</span>
              </dt>
              <dd className="shrink-0 text-ink">
                <span className="font-medium">{formatCatTokens(seg.tokens)}</span>
                {breakdown.totalTokens > 0 && (
                  <span className="ml-1 text-ink-3">{formatShare(seg.windowShare)}</span>
                )}
              </dd>
            </div>
          );
        })}

        {breakdown.freeTokens !== null && (
          <div className="flex items-center justify-between gap-2" data-breakdown-row="free">
            <dt className="flex min-w-0 items-center gap-1.5 text-ink-3">
              <span aria-hidden className="size-2 shrink-0 rounded-full border border-line-strong bg-hover-2" />
              <span className="truncate">{t("chat-sessioninsights.breakdown_free")}</span>
            </dt>
            <dd className="shrink-0 text-ink-2">
              <span className="font-medium">{compactTokens(breakdown.freeTokens)}</span>
              {breakdown.freeShare !== null && (
                <span className="ml-1 text-ink-3">{formatShare(breakdown.freeShare)}</span>
              )}
            </dd>
          </div>
        )}

        {breakdown.autoCompactThreshold !== null && (
          <div className="mt-1 flex items-center justify-between gap-2 border-t border-line pt-1.5 text-submeta" data-breakdown-auto-compact>
            <dt className="flex min-w-0 items-center gap-1.5 text-ink-3">
              <span aria-hidden className="inline-block h-2.5 w-0.5 shrink-0 rounded-full bg-ink" />
              <span className="truncate">{t("chat-sessioninsights.breakdown_auto_compact")}</span>
            </dt>
            <dd className="shrink-0 font-medium text-ink-2">
              {compactTokens(breakdown.autoCompactThreshold)}
              {breakdown.autoCompactShare !== null && (
                <span className="ml-1 font-normal text-ink-3">{formatShare(breakdown.autoCompactShare)}</span>
              )}
            </dd>
          </div>
        )}
      </dl>

      {breakdown.topConsumer && (
        <div
          data-breakdown-top-consumer={breakdown.topConsumer.key}
          className="mt-1.5 flex items-center gap-1.5 border-t border-line pt-1.5 text-submeta text-ink-2"
        >
          <span
            aria-hidden
            className="size-2 shrink-0 rounded-full"
            style={{ backgroundColor: BREAKDOWN_COLOR[breakdown.topConsumer.key] }}
          />
          <span className="min-w-0">
            {t("chat-sessioninsights.breakdown_top_consumer", {
              category: t(BREAKDOWN_LABEL_KEY[breakdown.topConsumer.key]),
              percent: Math.round(breakdown.topConsumer.usedShare),
            })}
          </span>
        </div>
      )}
    </div>
  );
}

export function SessionInsightsPopover({
  anchor,
  session,
  items = EMPTY_ITEMS,
  assistant,
  onCompact,
  compactBlocked,
  onClose,
  rateHistory,
  breakdown,
  ttftMs,
}: SessionInsightsPopoverProps) {
  const cardRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const [compactingNow, setCompactingNow] = useState(false);
  const [place, setPlace] = useState<{ left: number; top: number; width: number } | null>(null);

  const hubSupportsAssistantCompact = useStore((s) => hubHas(s.features, COMPACT_FEATURE));
  const routed = useFeed((s) => Boolean(s.routes[session.session_id]));
  const insights = useMemo(
    () => buildSessionInsights(session, items, {
      ...(rateHistory !== undefined ? { rateHistory } : {}),
      ...(breakdown !== undefined ? { breakdown } : {}),
      ...(ttftMs !== undefined ? { ttftMs } : {}),
    }),
    [session, items, rateHistory, breakdown, ttftMs],
  );
  const compact = useMemo(
    () => compactActionSupport(session, {
      assistant,
      hubSupportsAssistantCompact,
      hasSendCompact: Boolean(onCompact),
      blocked: compactBlocked,
      routed,
    }),
    [session, assistant, hubSupportsAssistantCompact, onCompact, compactBlocked, routed],
  );

  useEscapeClose(true, onClose);
  useModalFocus(cardRef, true);

  useEffect(() => {
    const down = (e: PointerEvent) => {
      const target = e.target as Node | null;
      if (target && !cardRef.current?.contains(target) && !anchor.current?.contains(target)) onClose();
    };
    document.addEventListener("pointerdown", down, true);
    return () => document.removeEventListener("pointerdown", down, true);
  }, [anchor, onClose]);

  useLayoutEffect(() => {
    const el = anchor.current;
    const box = cardRef.current;
    if (!el || !box) return;
    const measure = () => {
      const rect = el.getBoundingClientRect();
      const screenW = document.documentElement.clientWidth || innerWidth;
      const screenH = innerHeight;
      const at = placeMenu(rect, box.offsetHeight || 240, {
        width: POPOVER_WIDTH,
        align: "right",
        side: "top",
        screenW,
        screenH,
        gap: 8,
      });
      setPlace((prev) => (
        prev && prev.left === at.left && prev.top === at.top && prev.width === at.width
          ? prev
          : { left: at.left, top: at.top, width: at.width }
      ));
    };
    measure();
    addEventListener("resize", measure);
    addEventListener("scroll", measure, { capture: true, passive: true });
    return () => {
      removeEventListener("resize", measure);
      removeEventListener("scroll", measure, { capture: true });
    };
  }, [anchor, insights, compact.supported]);

  const triggerCompact = async () => {
    if (!compact.supported || compact.running || compactingNow) return;
    setCompactingNow(true);
    try {
      let ok = false;
      if (onCompact) {
        const res = await onCompact();
        ok = res !== false;
        if (!ok) toast(t("chat-sessioninsights.compact_failed"), "error");
      } else if (compact.mode === "assistant") {
        ok = await requestAssistantCompaction(session);
      }
      if (ok) onClose();
    } catch (e) {
      toast(t("assistant-assistantchat.compact_start_failed", { error: errorText(e) }), "error");
    } finally {
      setCompactingNow(false);
    }
  };

  const { context, tokens, performance, breakdown: normalizedBreakdown } = insights;
  const hitText = tokens ? formatHitRate(tokens.cacheHitRate) : null;
  const compactBusy = compact.running || compactingNow;
  const current = currentContextTokens(session);
  const hasContextSection = Boolean(current || normalizedBreakdown);
  const turnRates = performance?.rateSource === "turn";
  const style: CSSProperties = place
    ? { left: place.left, top: place.top, width: place.width, animation: `pop-in 160ms ${EASE} both`, transformOrigin: "bottom right" }
    : { left: 0, top: 0, width: POPOVER_WIDTH, visibility: "hidden" };

  return createPortal(
    <div
      ref={cardRef}
      role="dialog"
      aria-labelledby={titleId}
      tabIndex={-1}
      data-session-insights
      className="fixed z-50 max-h-[min(70vh,520px)] overflow-y-auto rounded-xl border border-line bg-surface p-4 text-label text-ink shadow-overlay outline-none"
      style={style}
    >
      <div className="flex items-center justify-between gap-2">
        <h2 id={titleId} className="text-meta font-semibold text-ink">{t("chat-sessioninsights.title")}</h2>
        <button
          type="button"
          onClick={onClose}
          aria-label={t("chat-sessioninsights.close")}
          title={t("chat-sessioninsights.close")}
          className="inline-flex size-6 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover hover:text-ink"
          data-insights-close
        >
          <Icon name="x" size={13} stroke={2} />
        </button>
      </div>

      <div className="mt-3 flex flex-col gap-3">
        {hasContextSection && (
          <section className="flex flex-col gap-2" data-insights-context>
            {context ? (
              <div className="flex items-center justify-between gap-2">
                <span className="font-medium text-ink-2">{t("chat-sessioninsights.context_section")}</span>
                <span className="tabular-nums text-ink-3">
                  {t("context.tokens", { used: formatTokens(context.tokens), window: formatTokens(context.window) })}
                </span>
              </div>
            ) : current > 0 && (
              <span className="font-medium tabular-nums text-ink-2">{t("context.current_tokens", { tokens: formatNumber(current) })}</span>
            )}
            {normalizedBreakdown && normalizedBreakdown.totalTokens > 0 ? (
              <StackedBreakdownBar breakdown={normalizedBreakdown} />
            ) : context ? (
              <ContextUsageBar usage={context} className="h-1.5 w-full" />
            ) : null}
            {context && (
              <div className="flex flex-col gap-1">
                <div className={cx("font-medium tabular-nums", TIER_TEXT[contextTier(context)])}>
                  {contextTooltip(context)}
                </div>
                <p className="leading-relaxed text-ink-3">{contextHint(context)}</p>
              </div>
            )}
            {normalizedBreakdown && <BreakdownCard breakdown={normalizedBreakdown} />}
          </section>
        )}

        {tokens && (
          <section className={cx("flex flex-col gap-2", hasContextSection && "border-t border-line pt-3")} data-insights-tokens>
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium text-ink-2">{t("chat-sessioninsights.tokens_section")}</span>
              <span className="tabular-nums text-ink-3">
                {t("chat-sessioninsights.total_tokens", { tokens: compactTokens(tokens.total) })}
              </span>
            </div>
            <dl className="grid grid-cols-2 gap-2 tabular-nums">
              <div className="rounded-lg bg-inset px-2.5 py-1.5">
                <dt className="text-ink-3">{t("chat-sessioninsights.input")}</dt>
                <dd className="font-medium text-ink" data-insights-input>{compactTokens(tokens.input)}</dd>
              </div>
              <div className="rounded-lg bg-inset px-2.5 py-1.5">
                <dt className="text-ink-3">{t("chat-sessioninsights.output")}</dt>
                <dd className="font-medium text-ink" data-insights-output>{compactTokens(tokens.output)}</dd>
              </div>
              {tokens.hasCache && (
                <>
                  <div className="rounded-lg bg-inset px-2.5 py-1.5">
                    <dt className="text-ink-3">{t("chat-sessioninsights.cache_read")}</dt>
                    <dd className="font-medium text-ink" data-insights-cache-read>{compactTokens(tokens.cacheRead)}</dd>
                  </div>
                  <div className="rounded-lg bg-inset px-2.5 py-1.5">
                    <dt className="text-ink-3">{t("chat-sessioninsights.cache_write")}</dt>
                    <dd className="font-medium text-ink" data-insights-cache-write>{compactTokens(tokens.cacheWrite)}</dd>
                  </div>
                </>
              )}
            </dl>
            {hitText && (
              <div className="flex items-center justify-between rounded-lg bg-inset px-2.5 py-1.5 tabular-nums" data-insights-cache-hit>
                <span className="text-ink-3">{t("chat-sessioninsights.cache_hit_rate")}</span>
                <span className="font-medium text-ink">{hitText}</span>
              </div>
            )}
            {tokens.models.length > 1 ? (
              <div className="flex flex-col gap-1.5" data-insights-models>
                <div className="text-ink-3">{t("chat-sessioninsights.by_model")}</div>
                <ul className="flex flex-col gap-1 tabular-nums">
                  {tokens.models.map((m) => {
                    const modelHit = formatHitRate(m.cacheHitRate);
                    const name = m.label || t("chat-sessioninsights.unknown_model");
                    const label = m.fast ? `${name} · ${t("chat-sessiondialogs.speed_fast")}` : name;
                    return (
                      <li key={`${m.model || "__unknown__"}\n${m.fast ? 1 : 0}`} className="flex items-center justify-between gap-2 rounded-lg bg-inset px-2.5 py-1.5">
                        <span className="min-w-0 truncate font-medium text-ink">{label}</span>
                        <span className="shrink-0 text-ink-3">
                          {compactTokens(m.total)}
                          {modelHit ? ` · ${modelHit}` : ""}
                        </span>
                      </li>
                    );
                  })}
                </ul>
              </div>
            ) : tokens.models.length === 1 && (tokens.models[0].label || tokens.models[0].fast) ? (
              <div className="flex items-center justify-between gap-2 text-ink-3" data-insights-model-single>
                <span>{t("chat-sessioninsights.model_single")}</span>
                <span className="truncate font-medium text-ink">
                  {tokens.models[0].fast
                    ? `${tokens.models[0].label || t("chat-sessioninsights.unknown_model")} · ${t("chat-sessiondialogs.speed_fast")}`
                    : tokens.models[0].label}
                </span>
              </div>
            ) : null}
          </section>
        )}

        {performance && (
          <section className={cx("flex flex-col gap-2", (hasContextSection || tokens) && "border-t border-line pt-3")} data-insights-performance>
            <span className="font-medium text-ink-2">{t("chat-sessioninsights.performance_section")}</span>
            <dl className="grid grid-cols-2 gap-2 tabular-nums">
              {performance.latestTurnDurationMs !== null && (
                <div className="rounded-lg bg-inset px-2.5 py-1.5">
                  <dt className="text-ink-3">{t("chat-sessioninsights.latest_turn")}</dt>
                  <dd className="font-medium text-ink" data-insights-duration>{workDuration(performance.latestTurnDurationMs)}</dd>
                </div>
              )}
              {performance.ttftMs != null && performance.ttftMs >= 0 && (
                <div className="rounded-lg bg-inset px-2.5 py-1.5">
                  <dt className="text-ink-3">{t("chat-sessioninsights.ttft")}</dt>
                  <dd className="font-medium text-ink" data-insights-ttft>
                    {formatLatencyMs(performance.ttftMs)}
                  </dd>
                </div>
              )}
              {performance.latestTokPerSec !== null && (
                <div className="rounded-lg bg-inset px-2.5 py-1.5">
                  <dt className="text-ink-3">
                    {t(turnRates ? "chat-sessioninsights.latest_turn_rate" : "chat-sessioninsights.latest_speed")}
                  </dt>
                  <dd className="font-medium text-ink" data-insights-speed-latest>
                    {t("chat-sessioninsights.tok_per_sec", { rate: formatRate(performance.latestTokPerSec) })}
                  </dd>
                </div>
              )}
              {performance.avgTokPerSec !== null && (
                <div className="rounded-lg bg-inset px-2.5 py-1.5">
                  <dt className="text-ink-3">
                    {t(turnRates ? "chat-sessioninsights.average_turn_rate" : "chat-sessioninsights.average_speed")}
                  </dt>
                  <dd className="font-medium text-ink" data-insights-speed-avg>
                    {t("chat-sessioninsights.tok_per_sec", { rate: formatRate(performance.avgTokPerSec) })}
                  </dd>
                </div>
              )}
              {performance.peakTokPerSec !== null && (
                <div className="rounded-lg bg-inset px-2.5 py-1.5">
                  <dt className="text-ink-3">{t("chat-sessioninsights.peak_speed")}</dt>
                  <dd className="font-medium text-ink" data-insights-speed-peak>
                    {t("chat-sessioninsights.tok_per_sec", { rate: formatRate(performance.peakTokPerSec) })}
                  </dd>
                </div>
              )}
            </dl>
            {turnRates && <p className="text-ink-3">{t("chat-sessioninsights.turn_rate_hint")}</p>}
          </section>
        )}

        {compact.supported && (
          <div className={cx((hasContextSection || tokens || performance) && "border-t border-line pt-3")}>
            <button
              type="button"
              onClick={() => void triggerCompact()}
              disabled={compactBusy}
              data-insights-compact
              className="flex w-full items-center justify-between gap-2 rounded-lg border border-line bg-inset px-3 py-2 text-label font-medium text-ink transition-colors hover:bg-hover disabled:opacity-50"
            >
              <span className="flex items-center gap-1.5">
                <Icon name="layers" size={14} className="shrink-0 text-ink-2" />
                <span>{compactBusy ? t("chat-sessioninsights.compacting") : t("chat-sessioninsights.compact_action")}</span>
              </span>
              {compact.mode === "agent" && <span className="font-mono text-ink-3">/compact</span>}
            </button>
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}
