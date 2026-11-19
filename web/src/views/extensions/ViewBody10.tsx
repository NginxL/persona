// UI version 10 (#9727, docs/extensions/extensions.md "UI version 10"): a view's `graph` drawn as an interactive
// canvas — nodes as DOM buttons on a transformed layer, the edges in one SVG behind them, group frames, fold
// controls, the selected node's ring; drag and wheel pan, pinch and Ctrl/Cmd + wheel and the buttons zoom (25%–200%),
// Fit, and a Full screen overlay with the same canvas. A chunk of its own, loaded with the first view that has a
// graph (ViewBody.tsx), never the first screen or the idle preload. The layout is lib/graph-layout.ts, the shared
// rules in slots; this file only multiplies by the web's node pitch and draws.
import { memo, useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { t } from "../../i18n";
import { layOutGraph } from "../../lib/graph-layout";
import { toneOf, type UIAction, type UIItem, type UIGraphNode } from "../../lib/extension-ui";
import { hue } from "../../lib/extension-ui-v6";
import { useExtensionOwner } from "../../lib/extension-owner";
import { useCloseOnBack } from "../../lib/back-close";
import { cx, EASE, IconButton, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useEscapeClose, useModalFocus } from "../../ui/overlay";
import { Badge, gaugePercent, glyphOf } from "./ViewBody6";
import { ROW_ICON } from "./ViewBody";

// The web's node pitch, at the standard text size; the whole of it follows the page's text-size choice
// (--text-scale, small 12/13 and large 14/13, styles.css), as every font does — the graph's node is a size of the
// design system (docs/design/DESIGN_SYSTEM.md, the graph of "UI version 10"), so it grows with the text.
const NODE_W = 224;
const NODE_H = 64;
const PITCH_X = 272;
const PITCH_Y = 92;
// The layer's own margin, so a group's frame and its title never touch the edge.
const MARGIN = 32;
// What a fitted view keeps free around the drawing.
const INSET = 40;
// A group's frame reaches this far above its members (its title) and around them.
const FRAME_PAD = 12;
const CAPTION = 28;
// 0.25 pans and zooms; a compact card's still fitted preview has no floor above this (a big graph fits 200 px;
// iOS calls it graphPreviewMinScale 0.01).
const MIN_ZOOM = 0.25;
const MIN_STILL_ZOOM = 0.01;
const MAX_ZOOM = 2;
const STEP = 1.25;

const clamp = (v: number, min: number, max: number) => Math.min(max, Math.max(min, v));

/** A node's tone (a role color) as one of the named hues the web's `--hue-*` colors are. */
const TONE_HUE: Record<string, string> = { positive: "green", caution: "orange", danger: "red" };
const nodeColor = (n: UIGraphNode): string | undefined => (n.color ? hue(n.color) : n.tone ? hue(TONE_HUE[toneOf(n.tone)]) : undefined);

interface CanvasView { x: number; y: number; zoom: number }

/** What a `key` keeps across redraws (docs: the person's pan, zoom and folding; a re-fit only while untouched). */
interface GraphStore { view: CanvasView; moved: boolean; folds: Set<string> | null }

/** Every canvas's persistence, by its owner and its `key`: one entry per owner, so two extensions using the same key
 *  never share what it keeps. Capped, but never at the cost of a canvas that is still mounted: when the cap is hit,
 *  the oldest entry no mounted canvas holds goes first, so a long session cannot evict the key on screen and quietly
 *  undo the person's pan when its states redraw. */
const STORES = new Map<string, GraphStore>();
const STORE_CAP = 128;
/** The store ids the mounted canvases hold now, with how many of them hold each (two canvases can share a key: the
 *  page and a card of the same view). Kept by the canvas itself, so it never lies. */
const HELD = new Map<string, number>();
const evict = () => {
  for (const id of STORES.keys()) if (!HELD.get(id)) {
    STORES.delete(id);
    return;
  }
};
const storeFor = (scope: string, key: string): GraphStore => {
  const id = `${scope}\u0000${key}`;
  let s = STORES.get(id);
  if (s) {
    STORES.delete(id); // touched: the oldest entries evict first
    STORES.set(id, s);
    return s;
  }
  if (STORES.size >= STORE_CAP) evict();
  STORES.set(id, (s = { view: { x: 0, y: 0, zoom: 1 }, moved: false, folds: null }));
  return s;
};
/** Forgets what every key kept (a test's clean slate; the page never calls it). */
export const forgetGraphStores = () => STORES.clear();

const KEY_PAN: Readonly<Record<string, [number, number]>> = {
  ArrowLeft: [48, 0], ArrowRight: [-48, 0], ArrowUp: [0, 48], ArrowDown: [0, -48],
};

/** A node's label, as the doc reads it: the title, then the badge, subtitle, progress, then "under <parent>". */
function nodeLabel(n: UIGraphNode, parentTitle: string | undefined): string {
  const parts = [n.title, n.badge?.text, n.subtitle, n.progress !== undefined ? `${gaugePercent(n.progress)}%` : ""].filter(Boolean);
  if (parentTitle) parts.push(t("extensions-viewbody.graph_under", { title: parentTitle }));
  return parts.join(t("extensions-viewbody.read_sep"));
}

/** The page's text-size choice (ui/look.ts) as a number the canvas multiplies its pitch by; 1 at "Standard". */
function textScaleNow(): number {
  if (typeof window === "undefined") return 1;
  const n = Number.parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--text-scale"));
  return Number.isFinite(n) && n > 0 ? n : 1;
}

// What one node drew the last time this canvas committed it (its fields as they read): a node that is new, or whose
// fields changed since, fades in; one that only moved place does not. The memory is one map per canvas instance, so it
// survives the layer being swapped (opening Full screen replaces the layer element) and dies with the canvas itself.
const drawSig = (n: UIGraphNode, parentTitle: string | undefined): string =>
  JSON.stringify([n.title, n.badge?.text ?? "", n.subtitle ?? "", n.progress ?? null, n.tone ?? "", n.color ?? "", n.icon ?? "", n.group ?? "", n.action?.id ?? "", parentTitle ?? ""]);

interface NodeProps {
  node: UIGraphNode;
  parentTitle?: string;
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  still: boolean;
  busy: string;
  pressable: boolean;
  onPress: (a: UIAction) => void;
  onReveal: (id: string) => void;
  /** The view's `selected` node: drawn with the accent's ring, and read as the current one. */
  selected?: boolean;
  /** True when the previous committed render of this canvas drew this node differently (or not at all). */
  fade: boolean;
  fold?: React.ReactNode;
}

/** One node: a card of the node token's size — the tone or color as a bar on the leading edge and a state dot,
 *  the icon at the start, the title one line with the badge at its end, the subtitle under, progress along the foot.
 *  Module-level and memoized, so a redraw re-renders a node in place instead of remounting all of them (keyboard
 *  focus included). A node without an action is a focusable group, so Tab reaches it too and a screen reader reads
 *  its whole label. The node being pressed stays enabled (as the row's own button, ViewBody's ItemButton): disabling
 *  it would blur it mid-action and the browser would drop the keyboard focus to the page. */
const GraphNode = memo(function GraphNode({ node: n, parentTitle, id, x, y, w, h, still, busy, pressable, onPress, onReveal, selected, fade, fold }: NodeProps) {
  const color = nodeColor(n);
  const glyph = glyphOf(n.icon, ROW_ICON);
  const label = nodeLabel(n, parentTitle);
  const act = still ? undefined : n.action; // a still preview presses nothing: it opens Full screen
  const isBusy = !!act && busy === act.id; // a slow action shows the row's own busy cue: a spinner and aria-busy
  const body = (
    <>
      {color && <span aria-hidden className="absolute inset-y-0 left-0 w-[3px] bg-current" style={{ color }} />}
      <span aria-hidden className={cx("flex items-center gap-2", color && "pl-1.5")}>
        {isBusy ? <Spinner size={12} /> : glyph ? <Icon name={glyph} size={14} className="shrink-0" style={color ? { color } : undefined} /> : null}
      </span>
      <span className={cx("flex min-w-0 flex-1 flex-col justify-center gap-0.5 py-1.5", color && "pl-1")}>
        <span className="flex min-w-0 items-center gap-1.5">
          {color && <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-current" style={{ color }} />}
          <span className="min-w-0 flex-1 truncate text-meta font-medium text-ink" title={n.title}>{n.title}</span>
          {n.badge && <Badge badge={n.badge} />}
        </span>
        {n.subtitle && <span className="min-w-0 truncate text-submeta text-ink-3" title={n.subtitle}>{n.subtitle}</span>}
      </span>
      {n.progress !== undefined && (
        <span aria-hidden className="absolute inset-x-0 bottom-0 h-[3px]">
          <span
            className="h-full rounded-r-full transition-[width] duration-500"
            style={{ width: `${gaugePercent(n.progress)}%`, background: color ?? "var(--accent)", transitionTimingFunction: EASE }}
          />
        </span>
      )}
    </>
  );
  // The card fades in only when it is new or its fields changed since the last committed render (a move or a redraw
  // that only changes states does not replay it); reduced motion takes the animation itself away (styles.css).
  const anim = fade ? { animation: `fade-in 200ms ${EASE} both` } : undefined;
  const common = { "data-graph-node": id, "aria-label": label, title: label, "aria-current": selected ? ("true" as const) : undefined } as const;
  return (
    <div role="listitem" data-graph-at={id} className="absolute" style={{ left: x, top: y, width: w, height: h }}>
      {act ? (
        <button
          type="button"
          {...common}
          disabled={(!!busy && busy !== act.id) || !pressable}
          aria-busy={isBusy || undefined}
          onFocus={() => onReveal(id)}
          onClick={() => onPress(act)}
          className={cx(
            "group flex h-full w-full items-center overflow-hidden rounded-card border border-line bg-surface pl-2.5 pr-2 text-left shadow-card",
            "transition-colors duration-300 enabled:hover:bg-hover focus-visible:z-10 disabled:opacity-60",
          )}
          style={anim}
        >
          {body}
        </button>
      ) : (
        <div
          {...common}
          role="group"
          tabIndex={still ? -1 : 0}
          onFocus={() => onReveal(id)}
          className="flex h-full w-full items-center overflow-hidden rounded-card border border-line bg-surface pl-2.5 pr-2 shadow-card transition-colors duration-300 focus-visible:outline-2 focus-visible:outline-accent"
          style={anim}
        >
          {body}
        </div>
      )}
      {fold}
    </div>
  );
});

interface FoldProps {
  id: string;
  w: number;
  h: number;
  /** The gap between the node and its children on the side they leave it (pitch − node): the control centres in it. */
  gap: number;
  down: boolean;
  folded: boolean;
  hidden: number;
  onToggle: (id: string) => void;
}

/** The fold control: its own control inside the node's own wrapper, fully beside the node on the side where its
 *  children leave it (right for a right tree, below for a down one) — never over the node, whose badge stays whole —
 *  and never a press of the node. It counters the layer's zoom, so it stays its own size on screen with a 44 px hit at
 *  any zoom. Folded, it shows the hidden count: a chip as wide as the count needs (+8 stays the 24 px circle,
 *  +123 grows past it), still centred on the gap. */
const FoldControl = memo(function FoldControl({ id, w, h, gap, down, folded, hidden, onToggle }: FoldProps) {
  const label = folded ? t("extensions-viewbody.graph_show_hidden", { count: hidden }) : t("extensions-viewbody.graph_fold");
  // +NNN at the submeta size with tabular numerals: about 7 px a digit plus the plus and the border's padding
  const wide = folded && hidden >= 100;
  const size = wide ? Math.max(24, 14 + String(hidden).length * 7) : 24;
  return (
    <button
      type="button"
      data-graph-fold={id}
      aria-label={label}
      aria-expanded={!folded}
      title={label}
      onClick={() => onToggle(id)}
      className={cx(
        "touch-hit absolute z-[3] flex items-center justify-center rounded-full border border-line bg-surface text-ink-3 shadow-hairline",
        "transition-transform duration-150 hover:text-ink",
      )}
      style={{
        width: size,
        height: 24,
        transform: "scale(var(--graph-fold-scale, 1))",
        transformOrigin: "center",
        ...(down ? { left: w / 2 - size / 2, top: h + gap / 2 - 12 } : { left: w + gap / 2 - size / 2, top: h / 2 - 12 }),
      }}
    >
      {folded ? (
        <span className="px-1 text-submeta leading-none font-medium tabular-nums">+{hidden}</span>
      ) : (
        <Icon name={down ? "chevron" : "chevronRight"} size={11} stroke={2.4} />
      )}
    </button>
  );
});

/**
 * A view's `graph` (UI version 10): the canvas. On a page it is 70% of the page's visible height (styles.css
 * `graph-canvas`) and the page scrolls around it; `compact` (a card, an overview or an assistant view) draws the
 * still fitted preview (`graph-preview`, 200 px) — a preview is not interactive: pressing it opens the Full screen
 * overlay, which draws the same canvas over the window with its position, zoom and folds, buttons, gestures and node
 * presses. Whose view it is comes from lib/extension-owner.tsx's context (the extension, else the page's own path), so
 * two owners' graphs never share what a key keeps — a context value stays right on a lone re-render, where a module
 * global set by the last render went stale.
 */
export function GraphCanvas({ item, compact, busy, onPress, canPress }: {
  item: UIItem;
  compact?: boolean;
  busy: string;
  onPress: (a: UIAction, destructive?: boolean) => void;
  canPress?: (a: UIAction) => boolean;
}) {
  const scoped = useExtensionOwner();
  const keyed = typeof item.key === "string" ? item.key : null;
  // A keyless graph keeps its state in this instance alone; a keyed one shares it per owner and key. A still
  // (compact) canvas never reads or writes the shared entry: whatever the person does to a card, the page's own
  // kept view stays as it was.
  const instance = useRef<GraphStore | null>(null);
  if (!instance.current) instance.current = { view: { x: 0, y: 0, zoom: 1 }, moved: false, folds: null };
  const readStore = (): GraphStore => (keyed !== null ? storeFor(scoped, keyed) : instance.current!);
  const storeKey = `${scoped}\u0000${keyed ?? ""}`;
  // This instance's entry is mounted: the store's cap never evicts it (a mounted canvas losing its entry would get a
  // fresh one on the next states-only redraw, which reads moved: false and throws the person's pan away).
  useLayoutEffect(() => {
    if (keyed === null) return;
    HELD.set(storeKey, (HELD.get(storeKey) ?? 0) + 1);
    return () => {
      const left = (HELD.get(storeKey) ?? 1) - 1;
      if (left > 0) HELD.set(storeKey, left);
      else HELD.delete(storeKey);
    };
  }, [storeKey, keyed]);
  // The person's folds for this `key`, kept across redraws (the store); null until they fold or unfold one, when the
  // view's `folded` applies, redraws included. Re-derived when the owner or the key changes: a canvas whose owner
  // changes without a keyed remount (the context value changes) must take the new owner's folds, not keep the old
  // one's — and its toggles then write the new owner's store. Memoized on its inputs, so a parent's re-render alone
  // does not take the layout and its listeners apart (a redraw mid-drag would drop the drag).
  const [personFolds, setPersonFolds] = useState<Set<string> | null>(() => (keyed !== null ? storeFor(scoped, keyed).folds : null));
  useLayoutEffect(() => {
    if (keyed === null) return;
    setPersonFolds((was) => {
      const kept = storeFor(scoped, keyed).folds;
      return was === kept ? was : kept; // no owner or key change: nothing to re-derive
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- scoped and keyed alone say whose folds these are
  }, [scoped, keyed]);
  const folds = useMemo(() => personFolds ?? new Set(item.folded ?? []), [personFolds, item.folded]);
  const [full, setFull] = useState(false);
  const stage = useRef<HTMLDivElement>(null);
  const layer = useRef<HTMLDivElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  // The control that opened Full screen: the page's Full screen button, or the compact preview button. Both are
  // replaced while the overlay is over them (the page's canvas is unmounted inside the portal, the preview button
  // becomes a placeholder), so useModalFocus's own returnFocus finds the opener gone and leaves the focus on body; a
  // layout effect after the close gives it back to the control that reopens the overlay.
  const opener = useRef<HTMLButtonElement | null>(null);
  const wasFull = useRef(false);
  if (full) wasFull.current = true;
  useLayoutEffect(() => {
    if (full) return;
    if (!wasFull.current) return;
    wasFull.current = false;
    // a close that moved the focus on purpose keeps it (returnFocus's rule): only a lost focus comes back
    if ((!document.activeElement || document.activeElement === document.body) && opener.current?.isConnected) opener.current.focus({ preventScroll: true });
  }, [full]);
  const view = useRef<CanvasView>({ x: 0, y: 0, zoom: 1 });
  // The person has moved this key's canvas (kept in the store, so a redraw does not mistake it for untouched).
  const moved = useRef(false);
  // The stage element the current bind of the pointer handlers saw: a pointer held over the Full screen switch (its
  // tap replaces the stage) leaves a stale entry in the pointer map, which the next bind drops.
  const boundStage = useRef<HTMLElement | null>(null);
  // The pointers of a drag or pinch in flight: outside the layout effect, which tears down and re-runs on every
  // redraw, so a gesture survives a redraw (states changing while the work runs) instead of losing its points. The
  // entry of a pointer that ends over another element — one finger on the canvas, another tapping a control outside
  // it — is dropped by the next bind (below), so a stale point never pairs with a fresh one into a phantom pinch.
  const pointsRef = useRef(new Map<number, { x: number; y: number }>());
  // What each node read on the last committed render of this canvas: a layer swap (opening Full screen) used to
  // restart it from empty, so every node faded again on the first redraw inside Full screen; a per-instance map
  // keeps the canvas's own memory across the swap. Written only by the commit effect (sigs), never in render.
  const memRef = useRef(new Map<string, string>());
  const controls = useRef<{ zoom: (factor: number) => void; fit: () => void } | null>(null);
  useModalFocus(dialog, full);
  useEscapeClose(full, () => setFull(false));
  // Back closes Full screen before it leaves the page, on a phone's gesture and a desktop's button alike.
  useCloseOnBack(full, () => setFull(false));
  // The node pitch follows the text-size choice (applyLook writes data-text-size on <html> when it changes).
  const [textScale, setTextScale] = useState(textScaleNow);
  useLayoutEffect(() => {
    if (typeof MutationObserver === "undefined") return;
    const mo = new MutationObserver(() => setTextScale(textScaleNow()));
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-text-size"] });
    return () => mo.disconnect();
  }, []);
  const nodeW = NODE_W * textScale;
  const nodeH = NODE_H * textScale;
  const pitchX = PITCH_X * textScale;
  const pitchY = PITCH_Y * textScale;

  // The layout follows from the nodes, edges and folds alone, so a redraw that changes only states keeps every node
  // in place (spec/fixtures/graph-layout/README.md).
  const laid = useMemo(
    () => layOutGraph({ nodes: item.nodes ?? [], edges: item.edges, layout: item.layout, direction: item.direction }, [...folds]),
    [item.nodes, item.edges, item.layout, item.direction, folds],
  );
  const byId = useMemo(() => Object.fromEntries((item.nodes ?? []).map((n) => [n.id, n])), [item.nodes]);
  const down = item.direction === "down";
  const lw = (laid.size[0] - 1) * pitchX + nodeW + 2 * MARGIN;
  const lh = (laid.size[1] - 1) * pitchY + nodeH + 2 * MARGIN;
  // A still preview is the one place this canvas is not interactive: a compact card that is not Full screen. Full
  // screen is the same canvas as the page's, gestures, buttons, folds and node presses included.
  const still = !!compact && !full;

  // The parent each node is read "under": the first edge that names it as a child, in the view's order.
  const parentTitle = useMemo(() => {
    const out: Record<string, string | undefined> = {};
    for (const [from, to] of laid.edges) if (!out[to]) out[to] = byId[from]?.title;
    return out;
  }, [laid, byId]);

  /** The node's place in the layer: slots × the web's pitch, plus the layer's margin. */
  const at = useCallback((id: string): [number, number] => {
    const p = laid.at[id];
    return p ? [p[0] * pitchX + MARGIN, p[1] * pitchY + MARGIN] : [0, 0];
  }, [laid, pitchX, pitchY]);
  const sel = item.selected && laid.at[item.selected] ? item.selected : undefined;

  useLayoutEffect(() => {
    const area = stage.current;
    const pane = layer.current;
    if (!area || !pane) return;
    const size = () => ({ w: area.clientWidth, h: area.clientHeight });
    // What the store kept for this key: the person's view, and whether they moved it (a redraw keeps both).
    if (!still) {
      const kept = readStore();
      moved.current = kept.moved;
      if (kept.moved) view.current = kept.view;
    }

    const apply = (v: CanvasView) => {
      view.current = v;
      pane.style.transform = `translate(${v.x}px, ${v.y}px) scale(${v.zoom})`;
      // the fold control counters the zoom, so it never shrinks below its own 24 px on screen (a 44 px hit)
      pane.style.setProperty("--graph-fold-scale", String(Math.max(1, 1 / v.zoom)));
      if (still) return; // a still preview never writes the store: the page's kept view stays whatever a card did
      const s = readStore();
      s.view = v;
      s.moved = moved.current;
    };

    // Keep some of the drawing in reach on each axis; a drawing that fits an axis stays centred on it.
    const clampPan = (v: CanvasView): CanvasView => {
      const { w, h } = size();
      const cw = lw * v.zoom;
      const ch = lh * v.zoom;
      return { zoom: v.zoom, x: cw <= w ? (w - cw) / 2 : clamp(v.x, INSET - cw, w - INSET), y: ch <= h ? (h - ch) / 2 : clamp(v.y, INSET - ch, h - INSET) };
    };

    const panBy = (dx: number, dy: number) => {
      const v = view.current;
      moved.current = true;
      apply(clampPan({ x: v.x + dx, y: v.y + dy, zoom: v.zoom }));
    };

    const zoomAt = (factor: number, cx: number, cy: number) => {
      const v = view.current;
      const zoom = clamp(v.zoom * factor, still ? MIN_STILL_ZOOM : MIN_ZOOM, MAX_ZOOM);
      if (zoom === v.zoom) return;
      const ratio = zoom / v.zoom;
      moved.current = true;
      apply(clampPan({ x: cx - (cx - v.x) * ratio, y: cy - (cy - v.y) * ratio, zoom }));
    };

    // It opens at the fitted scale held to 50%–100%, centred on `selected`, else with the first root at the top
    // start — a right tree's first root sits halfway down its children, so the layer's corner would not do.
    const open = () => {
      const { w, h } = size();
      if (!w || !h) return;
      const fit = Math.min((w - 2 * INSET) / lw, (h - 2 * INSET) / lh);
      const zoom = clamp(fit, 0.5, 1);
      const origin = sel
        ? (() => {
            const [sx, sy] = at(sel);
            return { x: w / 2 - (sx + nodeW / 2) * zoom, y: h / 2 - (sy + nodeH / 2) * zoom };
          })()
        : (() => {
            const [rx, ry] = laid.order.length ? at(laid.order[0]) : [MARGIN, MARGIN];
            return { x: INSET - rx * zoom, y: INSET - ry * zoom };
          })();
      apply(clampPan({ ...origin, zoom }));
    };

    // Fit shows everything: the page and Full screen down to 25%, a still preview with no floor above 1%, centred.
    // It counts as the person moving the canvas: without it, the next redraw's settled() would see the key untouched
    // and take the opening view again, throwing the person's fit away — on a live graph, one poll later.
    const fit = (floor = MIN_ZOOM) => {
      const { w, h } = size();
      if (!w || !h) return;
      const zoom = clamp(Math.min((w - 2 * INSET) / lw, (h - 2 * INSET) / lh), floor, 1);
      if (!still) moved.current = true;
      apply({ x: (w - lw * zoom) / 2, y: (h - lh * zoom) / 2, zoom });
    };

    const settled = () => {
      if (still) fit(MIN_STILL_ZOOM);
      else if (!moved.current) open();
      else apply(clampPan(view.current));
    };

    const local = (e: PointerEvent | WheelEvent): { x: number; y: number } => {
      const r = area.getBoundingClientRect();
      return { x: e.clientX - r.left, y: e.clientY - r.top };
    };
    const stop = (e: Event) => e.stopPropagation();
    // The click a drag that began on a control must swallow: with the stage's pointer capture, the gesture's click is
    // retargeted to the stage, so the control's own one-shot listener never fires. The leftover is taken off at the
    // start of the next gesture on that control (and when the effect rebinds), or it would sit there and eat the next
    // real tap in the capture phase — React's onClick would never run.
    let swallow: { el: Element; stop: (e: Event) => void } | null = null;
    const dropSwallow = () => {
      if (!swallow) return;
      swallow.el.removeEventListener("click", swallow.stop, true);
      swallow = null;
    };

    const wheel = (e: WheelEvent) => {
      e.preventDefault(); // the canvas is a pan surface: the wheel never scrolls the page past it
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? area.clientHeight : 1;
      if (e.ctrlKey || e.metaKey) {
        const p = local(e);
        zoomAt(Math.exp(clamp(-e.deltaY * unit * 0.004, -1, 1)), p.x, p.y);
      } else {
        panBy(-e.deltaX * unit, -e.deltaY * unit);
      }
    };

    // Drag pans; two pointers pinch around their centre (the point between them stays put). A press that starts on a
    // node (or any control) stays that control's press — the drag takes over only once the pointer moves 4 px, and
    // dragging never presses the node under it. The pointers live in a ref, not the effect: the effect re-runs on every
    // redraw (a new `laid`), and a map reborn empty would silently kill a drag or pinch that a live graph's state
    // change interrupted mid-gesture — exactly the case this canvas exists for.
    const points = pointsRef.current;
    // A pointer left over from the element this canvas last bound (one finger stayed down over the Full screen
    // switch, whose tap replaces the stage) must not pair with a fresh one into a phantom pinch: drop everything
    // this new bind did not see go down.
    if (boundStage.current !== area) points.clear();
    boundStage.current = area;
    const gesture = (ps: Map<number, { x: number; y: number }>) => {
      const [a, b] = ps.values();
      if (!a) return undefined;
      if (!b) return { cx: a.x, cy: a.y, d: 0 };
      return { cx: (a.x + b.x) / 2, cy: (a.y + b.y) / 2, d: Math.hypot(a.x - b.x, a.y - b.y) };
    };
    const down = (e: PointerEvent) => {
      if (e.button !== 0 || points.size >= 2) return;
      // a fresh press on the control whose leftover this canvas still carries takes the leftover off first: its
      // gesture is over, and the press that begins now is this control's own press (swallowed only if it pans)
      if (swallow && (e.target as Element | null)?.closest("button, a, input, select, textarea") === swallow.el) dropSwallow();
      points.set(e.pointerId, local(e));
    };
    const move = (e: PointerEvent) => {
      const p = points.get(e.pointerId);
      if (!p) return;
      const at = local(e);
      const onControl = (e.target as Element | null)?.closest("button, a, input, select, textarea") ?? null;
      const dragging = area.hasPointerCapture(e.pointerId);
      // a control's own press stays its own until the pointer really moves (4 px) and the drag takes it over
      if (!dragging && onControl && Math.hypot(at.x - p.x, at.y - p.y) < 4) return;
      if (!dragging) {
        area.setPointerCapture(e.pointerId);
        // the drag begins on a control: swallow its click rather than press it as well. With the capture the click
        // lands on the stage, not the control, so the swallow is taken off by the next down on it — never left to
        // eat the next real tap.
        if (onControl) {
          dropSwallow();
          onControl.addEventListener("click", stop, { capture: true, once: true });
          swallow = { el: onControl, stop };
        }
      }
      const before = gesture(points)!;
      points.set(e.pointerId, at);
      const after = gesture(points)!;
      if (before.d > 0 && after.d > 0) {
        // the point between the fingers stays put: zoom about it, then carry it with the fingers
        zoomAt(after.d / before.d, before.cx, before.cy);
      }
      panBy(after.cx - before.cx, after.cy - before.cy);
    };
    const end = (e: PointerEvent) => {
      points.delete(e.pointerId);
      // the gesture is over: what its click swallows (dispatched right after pointerup in the same task) is settled,
      // so the leftover listener comes off in the next task — never left to eat the next real tap
      if (swallow) setTimeout(dropSwallow, 0);
    };

    // With the canvas focused (not a node of it): the arrows pan, + / - zoom, 0 fits.
    const key = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || e.target !== area) return;
      const pan = KEY_PAN[e.key];
      const { w, h } = size();
      if (pan) (e.preventDefault(), panBy(pan[0], pan[1]));
      else if (e.key === "+" || e.key === "=") (e.preventDefault(), zoomAt(STEP, w / 2, h / 2));
      else if (e.key === "-" || e.key === "_") (e.preventDefault(), zoomAt(1 / STEP, w / 2, h / 2));
      else if (e.key === "0") (e.preventDefault(), fit());
    };

    controls.current = {
      zoom: (factor: number) => zoomAt(factor, area.clientWidth / 2, area.clientHeight / 2),
      fit: () => fit(),
    };
    settled();
    const resized = () => settled();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(resized);
    observer?.observe(area);
    if (!still) {
      area.addEventListener("wheel", wheel, { passive: false });
      area.addEventListener("pointerdown", down);
      area.addEventListener("pointermove", move);
      area.addEventListener("pointerup", end);
      area.addEventListener("pointercancel", end);
      area.addEventListener("lostpointercapture", end);
      area.addEventListener("keydown", key);
    }
    return () => {
      observer?.disconnect();
      controls.current = null;
      dropSwallow(); // a leftover swallow never outlives this bind: a rebind's first tap must press
      if (!still) {
        area.removeEventListener("wheel", wheel);
        area.removeEventListener("pointerdown", down);
        area.removeEventListener("pointermove", move);
        area.removeEventListener("pointerup", end);
        area.removeEventListener("pointercancel", end);
        area.removeEventListener("lostpointercapture", end);
        area.removeEventListener("keydown", key);
      }
    };
    // laid changes when the view is drawn again or a fold is toggled; the person's view survives it in `view`.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- readStore closes over keyed/scoped only through the store's own id
  }, [laid, still, full, storeKey, keyed, at, sel, nodeW, nodeH, lw, lh]);

  // A node the keyboard focuses is panned into view. It does not count as the person moving the canvas (an untouched
  // key still re-fits), but the shift itself is kept: without it, the next redraw of an untouched key would take the
  // opening view again and undo the scroll-into-view, dropping the focused node out of the visible area.
  // A node the keyboard focuses is panned into view. It does not count as the person moving the canvas (an untouched
  // key still re-fits), but the shift itself is kept: without it, the next redraw of an untouched key would take the
  // opening view again and undo the scroll-into-view, dropping the focused node out of the visible area.
  const reveal = useCallback((id: string) => {
    const area = stage.current;
    const pane = layer.current;
    if (!area || !pane) return;
    const w = area.clientWidth;
    const h = area.clientHeight;
    if (!w || !h) return;
    const v = view.current;
    const [nx, ny] = at(id);
    const sx = v.x + nx * v.zoom;
    const sy = v.y + ny * v.zoom;
    const nw = nodeW * v.zoom;
    const nh = nodeH * v.zoom;
    // the node's fold control sits beside it, fully past the node on the side its children leave it (right for a
    // right tree, below for a down one), and counters the zoom (24 px drawn, a 44 px hit): a reveal that stops at the
    // node's box leaves the control clipped at the stage edge, so the box it pans in grows by the control's far
    // extent on that side — half the gap plus its own radius.
    const gap = down ? (pitchY - nodeH) * v.zoom : (pitchX - nodeW) * v.zoom;
    const reach = Math.max(24, gap / 2 + 24) * Math.max(1, 1 / v.zoom);
    const dx = sx < 12 ? 12 - sx : sx + nw > w - 12 - (down ? 0 : reach) ? w - 12 - (down ? 0 : reach) - nw - sx : 0;
    const dy = sy < 12 ? 12 - sy : sy + nh > h - 12 - (down ? reach : 0) ? h - 12 - (down ? reach : 0) - nh - sy : 0;
    if (dx || dy) {
      const next = { x: v.x + dx, y: v.y + dy, zoom: v.zoom };
      view.current = next;
      pane.style.transform = `translate(${next.x}px, ${next.y}px) scale(${next.zoom})`;
      pane.style.setProperty("--graph-fold-scale", String(Math.max(1, 1 / next.zoom)));
      if (!still) {
        const s = readStore();
        s.view = next; // kept without marking the person's move: an untouched key still re-fits on a resize
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- readStore closes over keyed/scoped only through the store's own id
  }, [at, nodeW, nodeH, pitchX, pitchY, down, still, keyed, scoped]);

  const toggle = useCallback((id: string) => {
    const next = new Set(folds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    readStore().folds = next; // the person's choice, kept for this key (or this instance)
    setPersonFolds(next);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- folds is the only changing input; keyed and scoped say whose store it writes
  }, [folds, keyed, scoped]);
  // What each node read on the previous committed render: the fade is computed against it, and this render's own
  // signatures are committed after it (a useLayoutEffect), never during — a render must not write the memory it
  // reads, or StrictMode's discarded render leaves no fade on the committed one (and the double-invoked render reads
  // the signatures it just wrote). Committed here, so it also survives the layer swap (opening Full screen): the map
  // belongs to this canvas instance, not to the layer element.
  const sigs = useMemo(() => {
    const out = new Map<string, string>();
    for (const id of laid.order) out.set(id, drawSig(byId[id] as UIGraphNode, parentTitle[id]));
    return out;
  }, [laid, byId, parentTitle]);
  const fadeOf = (id: string): boolean => {
    const was = memRef.current.get(id);
    return was === undefined || was !== sigs.get(id);
  };
  useLayoutEffect(() => {
    memRef.current = sigs;
  }, [sigs]);
  const nodes = (
    // the canvas reads as a list of its visible nodes in reading order; each listitem wraps its node and its own fold
    <div role="list" className="absolute top-0 left-0">
      {laid.order.map((id) => {
        const n = byId[id] as UIGraphNode;
        const [x, y] = at(id);
        const act = still ? undefined : n.action;
        const pressable = !!act && (canPress ? canPress(act) : true);
        const folded = folds.has(id);
        const hidden = laid.folds[id];
        const needsFold = !still && hidden !== undefined;
        return (
          <GraphNode
            key={id}
            node={n}
            parentTitle={parentTitle[id]}
            id={id}
            x={x}
            y={y}
            w={nodeW}
            h={nodeH}
            still={still}
            busy={busy}
            pressable={pressable}
            onPress={onPress}
            onReveal={reveal}
            selected={id === sel}
            fade={fadeOf(id)}
            fold={needsFold ? <FoldControl id={id} w={nodeW} h={nodeH} gap={down ? pitchY - nodeH : pitchX - nodeW} down={down} folded={folded} hidden={hidden} onToggle={toggle} /> : undefined}
          />
        );
      })}
    </div>
  );

  const canvas = (
    <div
      ref={stage}
      role="group"
      aria-label={t("extensions-viewbody.graph")}
      tabIndex={still ? -1 : 0}
      data-graph-stage=""
      className={cx(
        "relative h-full w-full touch-none overflow-hidden overscroll-none select-none rounded-card focus-visible:outline-2 focus-visible:outline-accent",
        !still && "cursor-grab active:cursor-grabbing",
      )}
    >
      <div ref={layer} data-graph-layer="" className="absolute top-0 left-0 origin-top-left" style={{ width: lw, height: lh }}>
        {/* the edges, thin curves in the quiet ink, behind everything */}
        <svg aria-hidden className="absolute top-0 left-0 text-ink-3" width={lw} height={lh} viewBox={`0 0 ${lw} ${lh}`}>
          {laid.edges.map(([from, to], i) => {
            const [fx, fy] = at(from);
            const [tx, ty] = at(to);
            const [sx, sy, ex, ey] = down
              ? [fx + nodeW / 2, fy + nodeH, tx + nodeW / 2, ty]
              : [fx + nodeW, fy + nodeH / 2, tx, ty + nodeH / 2];
            const k = Math.max(24, down ? (ey - sy) / 2 : (ex - sx) / 2);
            return (
              <path
                key={i}
                data-graph-edge={`${from} ${to}`}
                d={down ? `M${sx} ${sy} C${sx} ${sy + k}, ${ex} ${ey - k}, ${ex} ${ey}` : `M${sx} ${sy} C${sx + k} ${sy}, ${ex - k} ${ey}, ${ex} ${ey}`}
                fill="none"
                stroke="currentColor"
                strokeWidth={1}
              />
            );
          })}
        </svg>
        {/* a group's frame: a quiet fill behind its members with its title above */}
        {laid.frames.map((f, i) => {
          const xs = f.members.map((m) => at(m)[0]);
          const ys = f.members.map((m) => at(m)[1]);
          const x = Math.min(...xs) - FRAME_PAD;
          const y = Math.min(...ys) - CAPTION;
          const w = Math.max(...xs) + nodeW - Math.min(...xs) + 2 * FRAME_PAD;
          const h = Math.max(...ys) + nodeH - Math.min(...ys) + FRAME_PAD + CAPTION;
          return (
            <div key={i} data-graph-frame={f.group} className="absolute rounded-card bg-inset" style={{ left: x, top: y, width: w, height: h }}>
              {/* the frame's title reads out (a group is part of the drawing, not decoration); the fill behind the
                  members is the div itself, clipped to the frame's box */}
              <span className="absolute top-1.5 left-3 text-submeta text-ink-3">{f.group}</span>
            </div>
          );
        })}
        {sel && (() => {
          const [x, y] = at(sel);
          return <span aria-hidden data-graph-selected="" className="pointer-events-none absolute z-[1] rounded-[calc(var(--radius-card)+2px)] shadow-[0_0_0_2px_var(--accent)]" style={{ left: x - 2, top: y - 2, width: nodeW + 4, height: nodeH + 4 }} />;
        })()}
        {nodes}
      </div>
    </div>
  );

  const buttons = (
    <div className="absolute right-3 bottom-3 z-[2] flex items-center gap-0.5 rounded-full border border-line bg-surface/90 p-1 shadow-card backdrop-blur">
      <IconButton icon="minus" label={t("extensions-viewbody.graph_zoom_out")} size={32} onClick={() => controls.current?.zoom(1 / STEP)} />
      <IconButton icon="plus" label={t("extensions-viewbody.graph_zoom_in")} size={32} onClick={() => controls.current?.zoom(STEP)} />
      <IconButton icon="fit" label={t("extensions-viewbody.graph_fit")} size={32} onClick={() => controls.current?.fit()} />
      {!full && <IconButton icon="expand" label={t("extensions-viewbody.graph_full_screen")} size={32} buttonRef={opener} onClick={() => setFull(true)} />}
    </div>
  );

  if (full) {
    return (
      <>
        {/* the page keeps the card's height while the overlay is over it, so it does not jump when it closes */}
        {compact && (
          <div aria-hidden className="px-4 py-3">
            <div className="graph-preview" />
          </div>
        )}
        {createPortal(
          <div
            ref={dialog}
            role="dialog"
            aria-modal="true"
            aria-label={t("extensions-viewbody.graph")}
            tabIndex={-1}
            className="fixed inset-0 z-50 flex flex-col p-2 outline-none sm:p-4" style={{ background: "var(--page)" }}
          >
            <div className="flex shrink-0 justify-end">
              <IconButton icon="x" label={t("extensions-viewbody.graph_close")} size={36} onClick={() => setFull(false)} />
            </div>
            <div className="relative min-h-0 flex-1">
              {canvas}
              {buttons}
            </div>
          </div>,
          document.body,
        )}
      </>
    );
  }
  if (compact) {
    return (
      <div className="px-4 py-3">
        <button
          type="button"
          ref={opener}
          aria-label={t("extensions-viewbody.graph_full_screen")}
          title={t("extensions-viewbody.graph_full_screen")}
          onClick={() => setFull(true)}
          className="graph-preview block w-full cursor-zoom-in overflow-hidden rounded-card"
        >
          {canvas}
        </button>
      </div>
    );
  }
  return (
    <div className="graph-canvas relative w-full">
      {canvas}
      {buttons}
    </div>
  );
}
