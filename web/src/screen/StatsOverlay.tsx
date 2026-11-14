// The live screen's connection stats on the web (#6558, docs/extensions/computer-use.md "Connection stats on the web"):
// the bar's toggle and the small panel it shows over the picture, as the iOS app's ⋯ › Connection Stats and
// ScreenStatsOverlay do. Off by default and kept per Mac (video.ts statsShown). ScreenView loads this chunk once the
// screen is live, so its words (screen-video) stay out of the first screen's catalog, which every page of the app sends.
//
// The panel measures only while it is shown (./probes, ./meter) and draws a reading once a second: the mode and path on
// its first line, then bitrate and frame rate, then the round trip to the hub on a line of its own and named so. That
// number is the browser's way to the hub, not the picture's latency, so it never sits on the first line the way the
// iOS app puts the video's end-to-end total there. A number that was not measured is not drawn.
//
// Its classes are ones other pages already use: Tailwind writes every class it finds in the source, comments included,
// into the one stylesheet the first screen sends, so a new one costs every page (startup-preload FIRST_SCREEN_BUDGET:
// #7368's two new ones, a translucent surface and a minimum width, took the ja first screen 29 B over it).
import { Fragment, useEffect, useRef, useState } from "react";
import { cx } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { t } from "../i18n";
import { mbpsText, ScreenMeter } from "./meter";
import { countBytes, countFrames, probeHub } from "./probes";
import type { ScreenStats } from "./video";

/** How often the panel draws a new reading. */
export const READ_EVERY_MS = 1000;

/** The bar's Connection stats toggle. */
export function StatsToggle({ on, onToggle }: { on: boolean; onToggle: (on: boolean) => void }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={() => onToggle(!on)}
      aria-label={t("screen-video.stats")}
      title={t("screen-video.stats")}
      data-testid="screen-stats-toggle"
      className={cx(
        "touch-hit flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-control px-2 text-label font-medium transition-colors hover:bg-hover hover:text-ink",
        on ? "bg-hover text-ink" : "text-ink-2",
      )}
    >
      <Icon name="chart" size={14} />
      {/* a narrow bar keeps the icon only; the button's name says what it is */}
      <span className="hidden md:inline">{t("screen-video.stats")}</span>
    </button>
  );
}

/** The panel over the picture: measures `rfb` (noVNC) and `socket` (the RFB WebSocket it was handed) while shown. */
export function StatsOverlay({ rfb, socket, onClose }: { rfb: object; socket: WebSocket; onClose: () => void }) {
  const [stats, setStats] = useState<ScreenStats>({ mode: "vnc", path: "relay" });
  useEffect(() => {
    // The meter counts frames only when this noVNC could be hooked (frames come later, never during this call).
    let m: ScreenMeter;
    const undoFrames = countFrames(rfb, () => m.drew());
    m = new ScreenMeter(performance.now(), undoFrames !== null);
    const undoBytes = countBytes(socket, (n) => m.received(n));
    const stopProbe = probeHub((ms) => m.roundTrip(ms, performance.now()));
    const read = setInterval(() => setStats(m.read(performance.now())), READ_EVERY_MS);
    return () => {
      clearInterval(read);
      stopProbe();
      undoBytes();
      undoFrames?.();
    };
  }, [rfb, socket]);

  // Dragged anywhere inside the page's picture area, from where it starts at the top right.
  const [at, setAt] = useState({ x: 0, y: 0 });
  const drag = useRef<{ id: number; x: number; y: number; from: { x: number; y: number }; min: { x: number; y: number }; max: { x: number; y: number } } | null>(null);
  const start = (e: React.PointerEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).closest("button")) return;
    const el = e.currentTarget;
    const room = el.parentElement?.getBoundingClientRect();
    if (!room) return;
    const box = el.getBoundingClientRect();
    el.setPointerCapture?.(e.pointerId);
    drag.current = {
      id: e.pointerId,
      x: e.clientX,
      y: e.clientY,
      from: at,
      min: { x: at.x - (box.left - room.left), y: at.y - (box.top - room.top) },
      max: { x: at.x + (room.right - box.right), y: at.y + (room.bottom - box.bottom) },
    };
  };
  const move = (e: React.PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || d.id !== e.pointerId) return;
    const clamp = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), Math.max(lo, hi));
    setAt({ x: clamp(d.from.x + e.clientX - d.x, d.min.x, d.max.x), y: clamp(d.from.y + e.clientY - d.y, d.min.y, d.max.y) });
  };
  const end = (e: React.PointerEvent<HTMLDivElement>) => {
    if (drag.current?.id === e.pointerId) drag.current = null;
  };

  const rates = [
    stats.bitrateMbps !== undefined ? t("screen-video.stats_mbps", { mbps: mbpsText(stats.bitrateMbps) }) : null,
    stats.fps !== undefined ? t("screen-video.stats_fps", { fps: Math.round(stats.fps) }) : null,
  ].filter((x): x is string => x !== null);
  return (
    <div
      role="group"
      aria-label={t("screen-video.stats")}
      data-testid="screen-stats"
      onPointerDown={start}
      onPointerMove={move}
      onPointerUp={end}
      onPointerCancel={end}
      style={{ transform: `translate(${at.x}px, ${at.y}px)` }}
      className="absolute top-3 right-3 z-10 flex cursor-grab touch-none flex-col gap-1 rounded-card bg-surface/90 px-2.5 py-2 text-label text-ink shadow-overlay backdrop-blur-md select-none tabular-nums active:cursor-grabbing"
    >
      <div className="flex items-center gap-1.5 whitespace-nowrap">
        <span className="font-semibold">{t("screen-video.stats_vnc")}</span>
        {stats.path === "relay" && (
          <>
            <span className="text-ink-3">·</span>
            <span className="text-ink-2">{t("screen-video.stats_via_hub")}</span>
          </>
        )}
        <span className="flex-1" />
        <button
          type="button"
          onClick={onClose}
          aria-label={t("screen-video.stats_hide")}
          title={t("screen-video.stats_hide")}
          data-testid="screen-stats-close"
          className="touch-hit -mr-1 ml-3 flex size-5 items-center justify-center rounded-chip text-ink-2 hover:bg-hover hover:text-ink"
        >
          <Icon name="x" size={12} />
        </button>
      </div>
      {rates.length > 0 && (
        <p className="flex items-center gap-1.5 whitespace-nowrap text-ink-2">
          {rates.map((r, i) => (
            <Fragment key={i}>
              {i > 0 && <span className="text-ink-3">·</span>}
              <span>{r}</span>
            </Fragment>
          ))}
        </p>
      )}
      {stats.hubRoundTripMs !== undefined && (
        <p className="whitespace-nowrap text-ink-2" data-testid="screen-stats-hub">
          {t("screen-video.stats_hub_round_trip", { ms: Math.round(stats.hubRoundTripMs) })}
        </p>
      )}
    </div>
  );
}
