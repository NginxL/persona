// The sidebar's bottom (#2432, docs/server/usage-windows.md): the two fullest subscription accounts as thin bars; a click opens
// a popover with every window of every account, its reset countdown, when it was sampled and whether use is ahead of or
// behind an even pace. Hidden entirely while the hub has no data.
import { useSidebarUsageShown } from "../lib/sidebar-usage";
import { type ComponentType, useEffect, useRef, useState } from "react";
import { t as tr } from "../i18n";
import { fullest, isBalanceAccount, isWarn, liveAccounts, selectSidebarUsageAccounts, sourceNames, usePinnedUsageAccounts, useUsageWindows, type UnconnectedProvider, type UsageAccount } from "../lib/usage-windows";
import { cx } from "../ui/atoms";
import { lazyOverlay } from "./LazyOverlay";

const UsagePopover = lazyOverlay<{ accounts: UsageAccount[]; unconnected?: readonly UnconnectedProvider[]; onClose: () => void }>(() => import("./UsagePopover").then((m) => m.UsagePopover));
let LoadedUnconnected: ComponentType<{ accounts: UsageAccount[]; onChange: (next: readonly UnconnectedProvider[]) => void }> | undefined;

const percent = (p: number) => Math.min(100, Math.max(0, p));
const shown = (p: number) => `${Math.round(p)}%`;
const fill = (p: number) => (isWarn(p) ? "bg-orange" : "bg-accent");
// The strip's row text: one place for the named secondary-meta size.
const ROW_NAME = "min-w-0 flex-1 truncate text-submeta text-ink-3";
const ROW_VALUE = "shrink-0 text-submeta tabular-nums";

/** A bar: the track, the used part (caution colour from 80%, or a caller-chosen tier class) and, with `expected`, a 1px tick where an even pace would be. */
export function Meter({
  used,
  expected,
  protect,
  label,
  tall,
  barClass,
  reserveTop = true,
}: {
  used: number;
  expected?: number;
  protect?: number;
  label: string;
  tall?: boolean;
  barClass?: string;
  reserveTop?: boolean;
}) {
  const hasProtect = typeof protect === "number" && Number.isFinite(protect) && protect >= 0 && protect <= 100;
  return (
    <div role="progressbar" aria-label={hasProtect ? `${label}, ${tr("usage.protect_label", { percent: Math.round(protect) })}` : label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(percent(used))} className={cx("relative w-full rounded-full bg-inset", tall ? "h-1.5" : "h-1", hasProtect && reserveTop && "mt-3.5")}>
      <div className={cx("h-full rounded-full transition-[width] duration-300", barClass ?? fill(used))} style={{ width: `${percent(used)}%` }} />
      {hasProtect && (
        <>
          <span aria-hidden data-protect-zone className="absolute inset-y-0 right-0 rounded-r-full" style={{ left: `${percent(protect)}%`, backgroundImage: "repeating-linear-gradient(135deg, transparent 0 2px, color-mix(in srgb, var(--orange) 45%, transparent) 2px 3px)" }} />
          <span aria-hidden data-protect-line className="absolute -top-1 -bottom-1 w-0.5 -translate-x-1/2 rounded-full bg-orange" style={{ left: `${percent(protect)}%` }} />
          <span aria-hidden data-protect-flag className="absolute -top-4 -translate-x-1/2 whitespace-nowrap rounded-sm bg-surface px-0.5 text-submeta font-bold leading-none text-orange" style={{ left: `${Math.min(percent(protect), 92)}%` }}>
            {tr("usage.protect_flag", { percent: Math.round(protect) })}
          </span>
        </>
      )}
      {expected !== undefined && <span aria-hidden data-pace-tick className="absolute -top-0.5 -bottom-0.5 w-px -translate-x-1/2 rounded-full bg-ink-3" style={{ left: `${percent(expected)}%` }} />}
    </div>
  );
}

const EMPTY_UNCONNECTED: readonly UnconnectedProvider[] = [];

/** The sidebar's strip. `accounts` as the hub gave them; `unconnected` lists active providers without usage data yet. */
export function UsageBars({ accounts, unconnected = EMPTY_UNCONNECTED, now = Date.now() }: { accounts: UsageAccount[]; unconnected?: readonly UnconnectedProvider[]; now?: number }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const pinnedIds = usePinnedUsageAccounts();
  const live = liveAccounts(accounts, now);
  const { bars: top, balances } = selectSidebarUsageAccounts(live, pinnedIds);
  const notices = !top.length && !balances.length ? live.filter((a) => !isBalanceAccount(a) && a.windows.length === 0 && Boolean(a.notice?.trim() || a.headline?.trim())).slice(0, 2) : [];

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

  if (!top.length && !balances.length && !notices.length && !unconnected.length) return null;
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
                <span className={ROW_NAME}>{a.name}</span>
                <span className={cx(ROW_VALUE, isWarn(used) ? "text-orange" : "text-ink-2")}>{shown(used)}</span>
              </span>
              <Meter used={used} label={tr("usage.used_label", { name: a.name, percent: shown(used) })} />
            </span>
          );
        })}
        {balances.map((a) => (
          <BalanceRow key={a.id} a={a} />
        ))}
        {notices.map((a) => (
          <span key={a.id} data-usage-notice-strip={a.id} className={cx("flex items-baseline gap-2", a.stale && "opacity-60")}>
            <span className={ROW_NAME}>{a.name}</span>
            <span className="min-w-0 max-w-[55%] truncate text-submeta text-ink-3">{a.notice?.trim() || a.headline?.trim()}</span>
          </span>
        ))}
        {unconnected.slice(0, Math.max(1, 4 - top.length)).map((p) => (
          <span key={`_${p.id}`} data-usage-unconnected={p.id} className="flex items-baseline gap-2 text-xs text-ink-3">
            <span className="min-w-0 flex-1 truncate">{p.title}</span>
            <span className="shrink-0">{tr("usage.not_connected")}</span>
          </span>
        ))}
      </button>
    </div>
  );
}

/** A prepaid account's row: the amount and currency, no bar. A genuine zero and an unreadable balance are both
 *  caution-coloured, but only the first shows an amount. */
function BalanceRow({ a }: { a: UsageAccount }) {
  // Why a balance is missing or old is the popover's detail (progressive disclosure); the strip only fades a stale one.
  return (
    <span className={cx("flex items-baseline gap-2", a.stale && "opacity-60")}>
      <span className={ROW_NAME}>{a.name}</span>
      <span className={cx(ROW_VALUE, !a.balance || Number(a.balance.amount) === 0 ? "text-orange" : "text-ink-2")}>
        {a.balance ? `${a.balance.currency} ${a.balance.amount}`.trim() : "—"}
      </span>
    </span>
  );
}

/** The sidebar mounts. */
export function SidebarUsage() {
  // Off in Settings (lib/sidebar-usage.ts): nothing mounts, so the usage windows are not even read.
  return useSidebarUsageShown() ? <SidebarUsageShown /> : null;
}

function SidebarUsageShown() {
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
