// The account windows and error details behind a click on the sidebar's usage bars (#10082): these are not needed
// for the cold first screen, so the overlay and its long strings load on intent.
import { useEffect, useState } from "react";
import { t as tr } from "../i18n";
import { sendDraft } from "../lib/drafts";
import { navigate } from "../lib/router";
import { getState } from "../lib/store";
import { balanceEmpty, balanceText } from "../lib/usage-balance";
import { canonicalUsageProvider, groupByProvider, isBalanceAccount, isPinnableUsageAccount, planNote, providerTimeline, providerTitle, remainingText, resetLabel, resetPinnedUsageAccounts, sampleAge, sortedWindows, sourceNames, TIMELINE_CAP_MS, TIMELINE_TICKS_HOURS, timelinePosition, togglePinnedUsageAccount, usePinnedUsageAccounts, windowKey, type UnconnectedProvider, type UsageAccount, type UsageWindow } from "../lib/usage-windows";
import { accountPace, type PaceState } from "../lib/usage-pace";
import { cx, EASE } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Meter } from "./UsageBars";

const PACE_TEXT: Record<PaceState, string> = { over: "text-orange", under: "text-green", on: "text-ink-3" };
const paceLabel = (s: PaceState) => (s === "over" ? tr("usage.pace_over") : s === "under" ? tr("usage.pace_under") : tr("usage.pace_on"));
const shown = (p: number) => `${Math.round(p)}%`;

type UsageTier = "low" | "mid" | "high";
const TIER_BG: Record<UsageTier, string> = { low: "bg-green", mid: "bg-orange", high: "bg-red" };
const TIER_TEXT: Record<UsageTier, string> = { low: "text-green", mid: "text-orange", high: "text-red" };

/** Tiered quota color: <50% green (--green), 50–80% orange (--orange), >=80% red (--red). */
export function usageTier(percent: number): UsageTier {
  if (percent >= 80) return "high";
  if (percent >= 50) return "mid";
  return "low";
}

/** Provider brand dot color from the design system's --hue-* palette (no raw hex). */
const PROVIDER_HUE: Record<string, string> = {
  claude: "var(--hue-orange)",
  codex: "var(--hue-green)",
  openai: "var(--hue-green)",
  gemini: "var(--hue-blue)",
  antigravity: "var(--hue-blue)",
  deepseek: "var(--hue-indigo)",
  kimi: "var(--hue-purple)",
  cursor: "var(--hue-teal)",
  copilot: "var(--hue-indigo)",
  trae: "var(--hue-green)",
  grok: "var(--hue-gray)",
  xai: "var(--hue-gray)",
};

export function providerHue(provider: string): string {
  return PROVIDER_HUE[canonicalUsageProvider(provider)] ?? "var(--hue-gray)";
}

const HOUR_MS = 3600_000;
const DAY_HOURS = 24;
const CAP_HOURS = TIMELINE_CAP_MS / HOUR_MS;
/** Minimum center-to-center spacing for secondary ticks and clear pixel gap between adjacent label bounding boxes. */
export const TIMELINE_MIN_TICK_GAP_PX = 20;
const TIMELINE_MIN_LABEL_GAP_PX = 6;
const ALL_TIMELINE_TICKS: readonly number[] = [0, ...TIMELINE_TICKS_HOURS];
/** Interior ticks dropped in priority order when narrow width causes spacing or bounding-box collisions. */
const DROPPABLE_TIMELINE_TICKS = [6, 120, 12, 72, 24] as const;

function tickLabel(hours: number): string {
  if (hours === 0) return tr("usage.timeline_now");
  if (hours < DAY_HOURS) return tr("usage.tick_hours", { h: hours });
  return tr("usage.tick_days", { d: hours / DAY_HOURS });
}

/** Conservative rendered width estimate in px for a text-submeta (11.5px) tick label so collision checks hold across locales. */
function estimateTickWidth(hours: number): number {
  const text = tickLabel(hours);
  let width = 0;
  for (const ch of text) width += ch.charCodeAt(0) > 0x7f ? 12 : 7;
  if (hours === 0) width += 2; // Now is font-semibold
  return Math.max(14, width);
}

function tickBounds(hours: number, axisWidthPx: number, widthPx: number): [number, number] {
  if (hours === 0) return [0, widthPx];
  if (hours === CAP_HOURS) return [axisWidthPx - widthPx, axisWidthPx];
  const cxPos = timelinePosition(hours * HOUR_MS) * axisWidthPx;
  return [cxPos - widthPx / 2, cxPos + widthPx / 2];
}

/** Visible timeline tick hours for an axis width: enforces both >=20px tick spacing and non-overlapping label bounds,
 *  progressively dropping 6h, 5d, 12h, 3d, 1d so narrow sidebars (e.g. 240px / 284px) never overlap or wrap. */
export function visibleTimelineTickHours(axisWidthPx: number | null, labelWidths?: Readonly<Record<number, number>>): number[] {
  const ticks = [...ALL_TIMELINE_TICKS];
  if (axisWidthPx === null || axisWidthPx <= 0) return ticks;
  const widthOf = (h: number) => labelWidths?.[h] ?? estimateTickWidth(h);
  const collides = (a: number, b: number) => {
    const centerGap = (timelinePosition(b * HOUR_MS) - timelinePosition(a * HOUR_MS)) * axisWidthPx;
    const [, aRight] = tickBounds(a, axisWidthPx, widthOf(a));
    const [bLeft] = tickBounds(b, axisWidthPx, widthOf(b));
    return centerGap < TIMELINE_MIN_TICK_GAP_PX || bLeft - aRight < TIMELINE_MIN_LABEL_GAP_PX;
  };
  for (const drop of DROPPABLE_TIMELINE_TICKS) {
    const idx = ticks.indexOf(drop);
    if (idx <= 0 || idx >= ticks.length - 1) continue;
    if (collides(ticks[idx - 1], ticks[idx]) || collides(ticks[idx], ticks[idx + 1])) {
      ticks.splice(idx, 1);
    }
  }
  return ticks;
}

/** Compact duration for narrow (<300px) cards: "45m", "1.5h", "2d 3h", "5d", or "Resets soon". */
export function compactResetText(resetsAt: string | undefined, now: number): string {
  const at = resetsAt ? Date.parse(resetsAt) : NaN;
  if (!Number.isFinite(at)) return "";
  const ms = at - now;
  if (ms <= 0) return tr("usage.due");
  const mins = Math.ceil(ms / 60_000);
  if (mins < 60) return tr("usage.tick_minutes", { m: Math.max(1, mins) });
  const hours = ms / HOUR_MS;
  if (hours < DAY_HOURS) return tr("usage.tick_hours", { h: Math.round(hours * 10) / 10 });
  const d = Math.floor(hours / DAY_HOURS);
  const h = Math.round(hours - d * DAY_HOURS);
  if (h >= DAY_HOURS) return tr("usage.tick_days", { d: d + 1 });
  return h === 0 ? tr("usage.tick_days", { d }) : tr("usage.tick_days_hours", { d, h });
}

function problemText(a: UsageAccount): string {
  switch (a.problem) {
    case "rejected":
      return tr("usage.problem_rejected");
    case "unreachable":
      return tr("usage.problem_unreachable");
    case "failed":
      return tr("usage.problem_failed");
    case "error":
      return tr("usage.problem_error");
    default:
      return "";
  }
}

/** Why a balance could not be read, or why the amount shown is the last one the manager had. */
function balanceProblemText(a: UsageAccount): string {
  if (!isBalanceAccount(a)) return "";
  switch (a.problem) {
    case "rejected":
      // A refused key after an earlier good read keeps the amount: say it is the last known one, not a fresh number.
      return a.balance ? tr("usage.balance_rejected_kept") : tr("usage.balance_rejected");
    case "unreachable":
      return a.balance ? tr("usage.balance_unreachable_kept") : tr("usage.balance_unreachable");
    case "failed":
      return a.balance ? tr("usage.balance_failed_kept") : tr("usage.balance_failed");
    default:
      return "";
  }
}

/** Why a provider has no bars, in plain words (#12411). Only the built-in subscriptions can be read on a computer, so
 *  they have not been read yet; any other launcher (one run through Claude Code against another provider included)
 *  can only be read through its relay, which is the case connectPrompt hands the assistant too. */
function connectHint(p: UnconnectedProvider): string {
  if (p.agent === "claude" && p.id === "claude") return tr("usage.connect_hint_claude");
  if (p.agent === "agy" || p.agent === "antigravity" || (p.agent === "codex" && p.id === "codex")) return tr("usage.connect_hint_pending", { name: p.title });
  return tr("usage.connect_hint_other");
}

function connectPrompt(p: UnconnectedProvider): string {
  if (p.agent === "claude" && p.id === "claude") return tr("usage.connect_prompt_claude", { id: p.id });
  if (p.agent === "agy" || p.agent === "antigravity") return tr("usage.connect_prompt_agy", { id: p.id });
  return tr("usage.connect_prompt_other", { name: p.title, id: p.id });
}

/** Every live account, window by window, plus active providers without usage data yet. */
export function UsagePopover({
  accounts,
  unconnected = [],
  now: fixed,
  onClose,
}: {
  accounts: UsageAccount[];
  unconnected?: readonly UnconnectedProvider[];
  now?: number;
  onClose?: () => void;
}) {
  const pinnedIds = usePinnedUsageAccounts();
  const pinnedSet = new Set(pinnedIds);
  const livePinnedCount = accounts.reduce((n, a) => n + (isPinnableUsageAccount(a) && pinnedSet.has(a.id) ? 1 : 0), 0);
  const [tick, setTick] = useState(() => Date.now());
  useEffect(() => {
    if (fixed !== undefined) return;
    const t = setInterval(() => setTick(Date.now()), 1000);
    return () => clearInterval(t);
  }, [fixed]);
  const now = fixed ?? tick;
  const askAssistant = (p: UnconnectedProvider) => {
    sendDraft("persona-new", { type: "replace", text: connectPrompt(p) });
    onClose?.();
    const onStarter = typeof location !== "undefined" && (location.pathname === "/persona/new" || (location.pathname === "/persona" && !getState().assistant?.main));
    if (!onStarter) navigate("/persona/new");
  };
  return (
    <div role="dialog" aria-label={tr("usage.title")} className="absolute bottom-full left-2.5 right-2.5 z-20 mb-1.5 max-h-[60vh] overflow-y-auto rounded-card bg-surface p-3 shadow-overlay" style={{ animation: `pop-in 160ms ${EASE} both`, transformOrigin: "bottom left" }}>
      <div className="flex flex-col gap-3">
        {pinnedIds.length > 0 && accounts.length > 0 && (
          <div data-usage-pinned-bar className="flex items-center justify-between gap-2 border-b border-line pb-2 text-submeta">
            <span className="min-w-0 truncate text-ink-3">
              {livePinnedCount > 0 ? tr("usage.pinned_summary", { count: livePinnedCount }) : tr("usage.pinned_fallback")}
            </span>
            <button
              type="button"
              data-usage-reset-pins
              onClick={() => resetPinnedUsageAccounts()}
              className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline"
            >
              {tr("usage.reset_pinned")}
            </button>
          </div>
        )}
        {groupByProvider(accounts).map((g) => {
          const hasWindows = g.accounts.some((a) => a.windows.length > 0);
          return (
            <section key={g.provider} data-usage-provider={g.provider} aria-label={providerTitle(g.provider)} className="@container flex flex-col gap-3 rounded-control border border-line p-2.5">
              <header className="flex flex-col gap-0.5">
                <div className="flex items-center gap-2">
                  <span className="flex min-w-0 flex-1 items-center gap-1.5">
                    <span aria-hidden data-provider-dot={g.provider} className="size-2 shrink-0 rounded-full" style={{ background: providerHue(g.provider) }} />
                    <h3 className="truncate text-meta font-semibold text-ink">{providerTitle(g.provider)}</h3>
                    <span className="shrink-0 whitespace-nowrap text-submeta text-ink-3">{tr("usage.provider_accounts", { count: g.accounts.length })}</span>
                  </span>
                  {g.sampledAt && <span className="shrink-0 whitespace-nowrap text-submeta text-ink-3">{sampleAge(g.sampledAt, now)}</span>}
                </div>
                {hasWindows && (
                  <p data-usage-sort-subtitle className="text-submeta text-ink-3">
                    {tr("usage.card_sort_subtitle")}
                  </p>
                )}
              </header>
              {g.accounts.map((a, i) => (
                <div key={a.id} className={cx("flex flex-col", i > 0 && "border-t border-line pt-2.5")}>
                  <AccountSection a={a} now={now} pinned={pinnedSet.has(a.id)} />
                </div>
              ))}
              <ProviderTimeline accounts={g.accounts} now={now} />
            </section>
          );
        })}
        {unconnected.map((p, i) => (
          <section key={`provider:${p.id}`} aria-label={p.title} className={cx("flex flex-col gap-1 text-xs", (accounts.length > 0 || i > 0) && "border-t border-line pt-2.5")}>
            <div className="flex items-baseline gap-2">
              <h3 className="min-w-0 flex-1 truncate font-medium text-ink">{p.title}</h3>
              <button
                type="button"
                data-usage-connect={p.id}
                onClick={() => askAssistant(p)}
                className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline"
              >
                {tr("usage.connect_action")}
              </button>
            </div>
            <div className="leading-snug text-ink-3">{connectHint(p)}</div>
          </section>
        ))}
      </div>
    </div>
  );
}

function AccountSection({ a, now, pinned }: { a: UsageAccount; now: number; pinned: boolean }) {
  const from = sourceNames(a);
  const plan = planNote(a);
  const showPin = isPinnableUsageAccount(a) || pinned;
  // A balance's failure is about the provider's balance route, not a usage gateway's stored windows (#11825).
  const problem = isBalanceAccount(a) ? balanceProblemText(a) : problemText(a);
  return (
    <section aria-label={a.name} data-usage-stale={a.stale ? "true" : undefined} className="flex flex-col gap-2">
      <header className="flex flex-col gap-0.5">
        <div className="flex items-center gap-1.5">
          <h3 className={cx("min-w-0 flex-1 truncate text-label font-medium", a.stale ? "text-ink-2" : "text-ink")}>{a.name}</h3>
          <span className="shrink-0 whitespace-nowrap text-submeta text-ink-3">{a.stale ? tr("usage.stale_age", { age: sampleAge(a.sampled_at, now) }) : sampleAge(a.sampled_at, now)}</span>
          {showPin && (
            <button
              type="button"
              data-usage-pin={a.id}
              aria-pressed={pinned}
              aria-label={pinned ? tr("usage.unpin_account", { name: a.name }) : tr("usage.pin_account", { name: a.name })}
              title={pinned ? tr("usage.unpin_title") : tr("usage.pin_title")}
              onClick={() => togglePinnedUsageAccount(a.id)}
              className={cx(
                "inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-control transition-colors",
                pinned ? "bg-hover-2 text-accent-ink" : "text-ink-3 hover:bg-hover hover:text-ink",
              )}
            >
              <Icon name="pin" size={12} />
            </button>
          )}
        </div>
        {(from.length > 0 || plan) && (
          <div data-usage-source className="flex min-w-0 items-baseline gap-1.5 text-submeta text-ink-3">
            {plan && <span className="shrink-0 whitespace-nowrap">{plan}</span>}
            {plan && from.length > 0 && <span aria-hidden>·</span>}
            <span className="min-w-0 truncate" title={tr("usage.sources_title", { sources: from.join(" · ") })}>{from.join(" · ")}</span>
          </div>
        )}
        {problem && <div className="text-submeta leading-snug text-orange">{problem}</div>}
        {a.headline?.trim() && <div data-usage-headline className="text-submeta leading-snug text-ink-2">{a.headline.trim()}</div>}
        {a.notice?.trim() && <div data-usage-notice className="text-submeta leading-snug text-ink-3">{a.notice.trim()}</div>}
      </header>
      {a.balance && (
        <div data-usage-balance className="flex items-baseline justify-between gap-2 rounded-control bg-inset px-2.5 py-2">
          <span className="text-xs text-ink-3">{tr("usage.balance_label")}</span>
          <span className={cx("text-sm font-medium tabular-nums", a.stale ? "text-ink-2" : balanceEmpty(a.balance) ? "text-orange" : "text-ink")}>{balanceText(a.balance)}</span>
        </div>
      )}
      {!a.balance && isBalanceAccount(a) && <div data-usage-balance-unavailable className="text-xs text-orange">{tr("usage.balance_unavailable")}</div>}
      {sortedWindows(a).map((w) => (
        <WindowRow key={windowKey(w)} name={a.name} w={w} stale={Boolean(a.stale)} now={now} />
      ))}
    </section>
  );
}

const TIMELINE_NAME_W = "calc(var(--spacing) * 18)";
const LANE_COLOR = { short: "var(--hue-teal)", long: "var(--hue-blue)" } as const;

/** Callback-ref width observer so re-mounting the timeline axis while the popover stays open rebinds ResizeObserver. */
function useAxisWidth(): [(el: HTMLDivElement | null) => void, number | null] {
  const [node, setNode] = useState<HTMLDivElement | null>(null);
  const [width, setWidth] = useState<number | null>(null);
  useEffect(() => {
    if (!node) {
      setWidth(null);
      return;
    }
    const measure = (w: number) => setWidth(w > 0 ? Math.round(w) : null);
    measure(node.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([entry]) => {
      if (entry) measure(entry.contentRect.width);
    });
    ro.observe(node);
    return () => ro.disconnect();
  }, [node]);
  return [setNode, width];
}

function uniqueLaneLabels(rows: ReturnType<typeof providerTimeline>, long: boolean): string[] {
  const out: string[] = [];
  for (const r of rows) {
    for (const l of r.lanes) {
      if (l.long === long && !out.includes(l.label)) out.push(l.label);
    }
  }
  return out;
}

/** One reset timeline per provider card. Shows short-unit ticks from TIMELINE_TICKS_HOURS, hiding interior ticks
 *  progressively when narrow width would cause collisions or <20px spacing. */
export function ProviderTimeline({ accounts, now }: { accounts: UsageAccount[]; now: number }) {
  const rows = providerTimeline(accounts, now);
  const [axisRef, axisWidth] = useAxisWidth();
  if (!rows.length) return null;
  const ticks = visibleTimelineTickHours(axisWidth);
  const shortLabels = uniqueLaneLabels(rows, false);
  const longLabels = uniqueLaneLabels(rows, true);
  return (
    <div data-usage-timeline className="border-t border-line pt-2" role="group" aria-label={tr("usage.timeline_title")}>
      <div className="mb-1.5 text-submeta text-ink-3">{tr("usage.timeline_title")}</div>
      <div ref={axisRef} data-timeline-axis className="relative h-4 whitespace-nowrap text-submeta leading-none text-ink-3" style={{ marginLeft: TIMELINE_NAME_W }} aria-hidden>
        {ticks.map((h) => {
          const isNow = h === 0;
          const isEnd = h === CAP_HOURS;
          return (
            <span
              key={h}
              data-timeline-tick={h}
              className={cx("absolute top-0 whitespace-nowrap", isNow ? "left-0 font-semibold text-ink" : isEnd ? "right-0" : "-translate-x-1/2")}
              style={isNow || isEnd ? undefined : { left: `${timelinePosition(h * HOUR_MS) * 100}%` }}
            >
              {tickLabel(h)}
            </span>
          );
        })}
      </div>
      <div className="relative" style={{ marginLeft: TIMELINE_NAME_W }}>
        {TIMELINE_TICKS_HOURS.map((h) => (
          <span key={h} aria-hidden className={cx("absolute inset-y-0 z-[1] w-px bg-line-strong", h !== DAY_HOURS && "opacity-60")} style={{ left: `${timelinePosition(h * HOUR_MS) * 100}%` }} />
        ))}
        <span aria-hidden data-timeline-now className="absolute inset-y-0 z-[3] w-0.5 rounded-full bg-ink" />
        {rows.map((r, i) => {
          const parts = r.lanes.map((l) => `${l.label} · ${resetLabel(l.resetsAt, now)}`);
          const plan = planNote(r.account).split(" ")[0];
          const shaded = i % 2 === 0;
          return (
            <div key={r.account.id} data-timeline-row={r.account.id} role="img" aria-label={`${r.account.name}: ${parts.join("; ")}`} title={parts.join("\n")} className="relative flex min-h-8 flex-col justify-center gap-0.5 py-1">
              {shaded && <span aria-hidden className="absolute inset-y-0 right-0 rounded-md bg-inset" style={{ left: `calc(-1 * ${TIMELINE_NAME_W})` }} />}
              <span className="absolute inset-y-0 right-full z-[1] flex flex-col justify-center pr-2 pl-1 text-submeta leading-tight text-ink-2" style={{ width: TIMELINE_NAME_W }}>
                <span className="truncate" title={r.account.name}>{r.account.name}</span>
                {plan && <small className="truncate text-ink-3">{plan}</small>}
              </span>
              {r.lanes.map((l) => {
                const color = l.long ? LANE_COLOR.long : LANE_COLOR.short;
                const end = `${l.end * 100}%`;
                return (
                  <span key={l.key} data-timeline-lane={l.label} aria-hidden className="relative z-[2] block">
                    {/* Share the free space around the label in proportion to the endpoint. */}
                    <span className="mx-1 grid" style={{ gridTemplateColumns: `${l.end}fr auto ${1 - l.end}fr` }}>
                      <span data-timeline-remaining className={cx("col-start-2 min-w-0 text-submeta font-semibold leading-none text-ink", shaded ? "bg-inset" : "bg-surface")}>
                        {remainingText(l.remainingMs)}
                      </span>
                    </span>
                    <span className={cx("relative block h-2.5", r.account.stale && "opacity-60")}>
                      <span className={cx("absolute left-0 top-1/2 -translate-y-1/2 rounded", l.long ? "h-2" : "h-1.5")} style={{ width: end, backgroundImage: `linear-gradient(90deg, transparent, ${color})` }} />
                      <span className="absolute top-0 size-2.5 -translate-x-1/2 rounded-full border-2 bg-surface" style={{ left: end, borderColor: color }} />
                    </span>
                  </span>
                );
              })}
            </div>
          );
        })}
      </div>
      <div data-timeline-legend aria-hidden className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-submeta text-ink-3">
        {shortLabels.map((windowLabel) => (
          <span key={`short:${windowLabel}`} data-legend-item="short" className={cx("inline-flex max-w-full items-center gap-1.5", windowLabel.length <= 12 && "whitespace-nowrap")}>
            <span className="inline-block h-1 w-3 shrink-0 rounded-full" style={{ background: LANE_COLOR.short }} />
            <span>{tr("usage.legend_window", { window: windowLabel })}</span>
          </span>
        ))}
        {longLabels.map((windowLabel) => (
          <span key={`long:${windowLabel}`} data-legend-item="long" className={cx("inline-flex max-w-full items-center gap-1.5", windowLabel.length <= 12 && "whitespace-nowrap")}>
            <span className="inline-block h-1 w-3 shrink-0 rounded-full" style={{ background: LANE_COLOR.long }} />
            <span>{tr("usage.legend_window", { window: windowLabel })}</span>
          </span>
        ))}
        <span data-legend-item="now" className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <span className="inline-block h-1 w-3 shrink-0 rounded-full bg-ink" />
          <span>{tr("usage.timeline_now")}</span>
        </span>
        <span data-legend-item="protect" className="inline-flex items-center gap-1.5 whitespace-nowrap" title={tr("usage.legend_protect_title")}>
          <span className="inline-block h-1 w-3 shrink-0 rounded-full bg-orange" />
          <span>{tr("usage.legend_protect")}</span>
        </span>
      </div>
    </div>
  );
}

function WindowRow({ name, w, stale, now }: { name: string; w: UsageWindow; stale: boolean; now: number }) {
  const [expanded, setExpanded] = useState(false);
  const p = accountPace({ stale }, w, now);
  const tier = usageTier(w.used_percent);
  const hasProtect = typeof w.protect_percent === "number" && Number.isFinite(w.protect_percent) && w.protect_percent >= 0 && w.protect_percent <= 100;
  const fullReset = resetLabel(w.resets_at, now);
  const shortReset = compactResetText(w.resets_at, now);
  const paceVerdict = p && p.state !== "on" ? paceLabel(p.state) : "";
  const hasDetail = Boolean(paceVerdict || fullReset);
  const meterBaseLabel = w.model
    ? tr("usage.window_model_label", { name, window: w.label, model: w.model, percent: shown(w.used_percent) })
    : tr("usage.window_used_label", { name, window: w.label, percent: shown(w.used_percent) });
  const meterLabel = paceVerdict ? `${meterBaseLabel}, ${paceVerdict}` : meterBaseLabel;
  const rowTitle = [w.label, shown(w.used_percent), p ? paceLabel(p.state) : "", fullReset].filter(Boolean).join(" · ");
  return (
    <div className="flex flex-col gap-1">
      {w.model && (
        <span data-usage-model className="truncate text-submeta text-ink-3" title={w.model}>
          {w.model}
        </span>
      )}
      <button
        type="button"
        data-window-row={w.label}
        aria-expanded={hasDetail ? expanded : undefined}
        aria-label={rowTitle}
        title={rowTitle}
        disabled={!hasDetail}
        onClick={() => hasDetail && setExpanded((v) => !v)}
        className={cx(
          "grid w-full grid-cols-[1.75rem_minmax(2.5rem,1fr)_auto_minmax(0,auto)_auto] items-center gap-x-1.5 rounded-sm text-left text-submeta [@media(pointer:coarse)]:min-h-11",
          hasProtect && "mt-3.5",
          hasDetail && "hover:bg-hover/60",
        )}
      >
        <span className="w-7 truncate text-ink-2">{w.label}</span>
        <span className={cx("block min-w-0", stale && "opacity-70")}>
          <Meter
            used={w.used_percent}
            expected={p?.expected}
            protect={w.protect_percent}
            barClass={TIER_BG[tier]}
            reserveTop={false}
            label={meterLabel}
            tall
          />
        </span>
        <span data-usage-percent className={cx("shrink-0 whitespace-nowrap text-right font-medium tabular-nums", stale ? "text-ink-2" : TIER_TEXT[tier])}>
          {shown(w.used_percent)}
        </span>
        {fullReset ? (
          <span data-usage-reset className="min-w-0 truncate text-right text-ink-3" title={fullReset}>
            <span data-reset-full className="hidden @[300px]:inline">{fullReset}</span>
            <span data-reset-short className="@[300px]:hidden">{shortReset}</span>
          </span>
        ) : (
          <span aria-hidden />
        )}
        {hasDetail ? (
          <span aria-hidden data-window-chevron className={cx("inline-flex shrink-0 items-center text-ink-3 transition-transform", expanded ? "rotate-180" : "-rotate-90")}>
            <Icon name="chevron" size={10} />
          </span>
        ) : (
          <span aria-hidden />
        )}
      </button>
      {hasDetail && expanded && (
        <div data-usage-pace-detail className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5 pl-7 text-submeta">
          {paceVerdict && p && <span className={cx("whitespace-nowrap", PACE_TEXT[p.state])}>{paceVerdict}</span>}
          {fullReset && <span className="text-ink-3">{fullReset}</span>}
        </div>
      )}
    </div>
  );
}
