// The account windows and error details behind a click on the sidebar's usage bars (#10082): these are not needed
// for the cold first screen, so the overlay and its long strings load on intent.
import { useEffect, useState } from "react";
import { t as tr } from "../i18n";
import { isWarn, pace, PACE_LABEL, planNote, problemText, resetLabel, sampleAge, sourceNames, windowKey, type PaceState, type UsageAccount, type UsageWindow } from "../lib/usage-windows";
import { cx, EASE } from "../ui/atoms";
import { Meter } from "./UsageBars";

const PACE_TEXT: Record<PaceState, string> = { over: "text-orange", under: "text-green", on: "text-ink-3" };
const shown = (p: number) => `${Math.round(p)}%`;

/** Every live account, window by window. `now` ticks while it is open so the countdowns and sample ages keep up. */
export function UsagePopover({ accounts, now: fixed }: { accounts: UsageAccount[]; now?: number; onClose?: () => void }) {
  const [tick, setTick] = useState(() => Date.now());
  useEffect(() => {
    if (fixed !== undefined) return;
    const t = setInterval(() => setTick(Date.now()), 1000);
    return () => clearInterval(t);
  }, [fixed]);
  const now = fixed ?? tick;
  return (
    <div role="dialog" aria-label={tr("usage.title")} className="absolute bottom-full left-2.5 right-2.5 z-20 mb-1.5 max-h-[60vh] overflow-y-auto rounded-card bg-surface p-3 shadow-overlay" style={{ animation: `pop-in 160ms ${EASE} both`, transformOrigin: "bottom left" }}>
      <div className="flex flex-col gap-3">
        {accounts.map((a) => (
          <AccountSection key={a.id} a={a} now={now} />
        ))}
      </div>
    </div>
  );
}

function AccountSection({ a, now }: { a: UsageAccount; now: number }) {
  const from = sourceNames(a);
  const plan = planNote(a);
  const problem = problemText(a);
  return (
    <section aria-label={a.name} className={cx("flex flex-col gap-2", a.stale && "opacity-70")}>
      <header className="flex flex-col gap-0.5">
        <div className="flex items-baseline gap-2">
          <h3 className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink">{a.name}</h3>
          <span className="shrink-0 whitespace-nowrap text-[11px] text-ink-3">{a.stale ? tr("usage.stale_age", { age: sampleAge(a.sampled_at, now) }) : sampleAge(a.sampled_at, now)}</span>
        </div>
        {(from.length > 0 || plan) && (
          <div data-usage-source className="flex min-w-0 items-baseline gap-1.5 text-[11px] text-ink-3">
            {plan && <span className="shrink-0 whitespace-nowrap">{plan}</span>}
            {plan && from.length > 0 && <span aria-hidden>·</span>}
            <span className="min-w-0 truncate" title={tr("usage.sources_title", { sources: from.join(" · ") })}>{from.join(" · ")}</span>
          </div>
        )}
        {problem && <div className="text-[11px] leading-snug text-orange">{problem}</div>}
      </header>
      {a.windows.map((w) => (
        <WindowRow key={windowKey(w)} name={a.name} w={w} now={now} />
      ))}
    </section>
  );
}

function WindowRow({ name, w, now }: { name: string; w: UsageWindow; now: number }) {
  const p = pace(w, now);
  return (
    <div className="flex flex-col gap-1">
      {w.model && (
        <span data-usage-model className="truncate text-[11px] text-ink-3" title={w.model}>
          {w.model}
        </span>
      )}
      <div className="flex items-baseline gap-2 text-[11.5px]">
        <span className="w-7 shrink-0 text-ink-2">{w.label}</span>
        <span className={cx("shrink-0 tabular-nums", isWarn(w.used_percent) ? "text-orange" : "text-ink")}>{shown(w.used_percent)}</span>
        {p && p.state !== "on" && <span className={cx("shrink-0 whitespace-nowrap", PACE_TEXT[p.state])}>{PACE_LABEL[p.state]}</span>}
        <span className="min-w-0 flex-1 truncate text-right text-ink-3">{resetLabel(w.resets_at, now)}</span>
      </div>
      <Meter used={w.used_percent} expected={p?.expected} label={w.model ? tr("usage.window_model_label", { name, window: w.label, model: w.model, percent: shown(w.used_percent) }) : tr("usage.window_used_label", { name, window: w.label, percent: shown(w.used_percent) })} tall />
    </div>
  );
}
