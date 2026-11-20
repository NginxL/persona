// The sidebar's bottom (#2432, docs/server/usage-windows.md): the two fullest subscription accounts as thin bars; a click opens
// a popover with every window of every account, its reset countdown, when it was sampled and whether use is ahead of or
// behind an even pace. Hidden entirely while the hub has no data.
import { type ComponentType, useEffect, useRef, useState } from "react";
import { t as tr } from "../i18n";
import { fullest, isWarn, liveAccounts, topAccounts, useUsageWindows, sourceNames, type UnconnectedProvider, type UsageAccount } from "../lib/usage-windows";
import { cx } from "../ui/atoms";
import { lazyOverlay } from "./LazyOverlay";

const UsagePopover = lazyOverlay<{ accounts: UsageAccount[]; unconnected?: readonly UnconnectedProvider[]; onClose: () => void }>(() => import("./UsagePopover").then((m) => m.UsagePopover));
let LoadedUnconnected: ComponentType<{ accounts: UsageAccount[]; onChange: (next: readonly UnconnectedProvider[]) => void }> | undefined;

const percent = (p: number) => Math.min(100, Math.max(0, p));
const shown = (p: number) => `${Math.round(p)}%`;
const fill = (p: number) => (isWarn(p) ? "bg-orange" : "bg-accent");

/** A bar: the track, the used part (caution colour from 80%) and, with `expected`, a tick where an even pace would be. */
export function Meter({ used, expected, label, tall }: { used: number; expected?: number; label: string; tall?: boolean }) {
  return (
    <div role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(percent(used))} className={cx("relative w-full rounded-full bg-inset", tall ? "h-1.5" : "h-1")}>
      <div className={cx("h-full rounded-full transition-[width] duration-300", fill(used))} style={{ width: `${percent(used)}%` }} />
      {expected !== undefined && <span aria-hidden data-pace-tick className="absolute -top-0.5 -bottom-0.5 w-0.5 -translate-x-1/2 rounded-full bg-ink-2" style={{ left: `${percent(expected)}%` }} />}
    </div>
  );
}

const EMPTY_UNCONNECTED: readonly UnconnectedProvider[] = [];

/** The sidebar's strip. `accounts` as the hub gave them; `unconnected` lists active providers without usage data yet. */
export function UsageBars({ accounts, unconnected = EMPTY_UNCONNECTED, now = Date.now() }: { accounts: UsageAccount[]; unconnected?: readonly UnconnectedProvider[]; now?: number }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const live = liveAccounts(accounts, now);
  const top = topAccounts(live);

  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    addEventListener("pointerdown", down, true);
    addEventListener("keydown", key);
    return () => {
      removeEventListener("pointerdown", down, true);
      removeEventListener("keydown", key);
    };
  }, [open]);

  if (!top.length && !unconnected.length) return null;
  return (
    <div ref={ref} className="relative shrink-0 px-2.5 pb-2.5 pt-1" data-usage-bars>
      {open && <UsagePopover accounts={live} unconnected={unconnected} onClose={() => setOpen(false)} />}
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={tr("usage.title")}
        className="touch-hit flex w-full flex-col gap-1.5 rounded-control px-2.5 py-2 text-left shadow-hairline transition-colors hover:bg-hover"
      >
        {top.map((a) => {
          const used = fullest(a);
          const from = sourceNames(a);
          const title = [from.length ? tr("usage.sources_title", { sources: from.join(" · ") }) : "", a.stale ? tr("usage.stale_title") : ""].filter(Boolean).join(" · ");
          return (
            <span key={a.id} className={cx("flex flex-col gap-1", a.stale && "opacity-60")} title={title || undefined}>
              <span className="flex items-baseline gap-2">
                <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3">{a.name}</span>
                <span className={cx("shrink-0 text-[11.5px] tabular-nums", isWarn(used) ? "text-orange" : "text-ink-2")}>{shown(used)}</span>
              </span>
              <Meter used={used} label={tr("usage.used_label", { name: a.name, percent: shown(used) })} />
            </span>
          );
        })}
        {unconnected.slice(0, 4 - top.length || 1).map((p) => (
          <span key={`_${p.id}`} data-usage-unconnected={p.id} className="flex items-baseline gap-2 text-xs text-ink-3">
            <span className="min-w-0 flex-1 truncate">{p.title}</span>
            <span className="shrink-0">{tr("usage.not_connected")}</span>
          </span>
        ))}
      </button>
    </div>
  );
}

/** The sidebar mounts. */
export function SidebarUsage() {
  return <SidebarUsageInner accounts={useUsageWindows()} />;
}

function SidebarUsageInner({ accounts }: { accounts: UsageAccount[] }) {
  const [, setLoaded] = useState(() => Boolean(LoadedUnconnected));
  const [unconnected, setUnconnected] = useState(EMPTY_UNCONNECTED);
  useEffect(() => {
    if (LoadedUnconnected) return;
    void import("./UsageUnconnected").then((m) => {
      LoadedUnconnected = m.UsageUnconnected;
      setLoaded(true);
    });
  }, []);
  return (
    <>
      {LoadedUnconnected && <LoadedUnconnected accounts={accounts} onChange={setUnconnected} />}
      <UsageBars accounts={accounts} unconnected={unconnected} />
    </>
  );
}
