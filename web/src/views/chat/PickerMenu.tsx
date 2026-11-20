import { Fragment, useId, useLayoutEffect, useReducer, useRef, useState, type ComponentProps, type ReactNode, type RefObject } from "react";
import { EASE } from "../../ui/atoms";

const CARD_WIDTH = 280;
const EDGE = 8;
type Place = { side: "inside" | "left" | "right"; top: number; height: number; clippedTitle: boolean };
export type PickerButton = ComponentProps<"button">;

/** Presentation only. Callers own their selection, IME, and what confirming a row does. */
export function PickerMenu<T>({ items, current, label, itemKey, group, renderRow, onHover, onPick,
  title, detail, describedBy, header, footer, empty, root, mode, wide = true, onBlur, moveOnly = true,
}: {
  items: T[]; current: number; label: string; itemKey: (item: T) => string;
  group?: (item: T) => string; renderRow: (item: T, props: PickerButton) => ReactNode;
  onHover: (i: number) => void; onPick: (item: T) => void;
  title?: string; detail?: ReactNode; describedBy?: (item: T, index: number) => string | undefined;
  header?: ReactNode; footer?: ReactNode; empty?: ReactNode;
  root?: RefObject<HTMLDivElement | null>; mode: "at" | "plus" | "slash"; wide?: boolean;
  onBlur?: ComponentProps<"div">["onBlur"]; moveOnly?: boolean;
}) {
  const detailId = useId();
  const panel = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const active = useRef<HTMLButtonElement>(null);
  const glide = useRef<HTMLSpanElement>(null);
  const card = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState<Place>({ side: "inside", top: 0, height: 0, clippedTitle: false });
  const [, measure] = useReducer((n: number) => n + 1, 0);
  const width = wide ? 480 : 320;
  const activeKey = items[current] && itemKey(items[current]);
  useLayoutEffect(() => {
    const p = panel.current;
    const popup = p?.parentElement;
    const anchor = popup?.offsetParent;
    if (!p || !popup || !anchor) return;
    // New-chat composers can sit midway down the page. Size against the actual anchor
    // and visible keyboard viewport, including when an ancestor scrolls.
    const fit = () => {
      const box = anchor.getBoundingClientRect();
      const view = window.visualViewport;
      const top = view?.offsetTop ?? 0;
      const bottom = top + (view?.height ?? innerHeight);
      const above = (mode === "plus" ? box.bottom : box.top - EDGE) - top - EDGE;
      const below = bottom - box.bottom - 2 * EDGE;
      const flip = mode !== "plus" && above < 144 && below > above;
      popup.style.bottom = flip ? "auto" : "";
      popup.style.top = flip ? "calc(100% + 8px)" : "";
      p.style.setProperty("--picker-room", Math.max(1, flip ? below : above) + "px");
      measure();
    };
    const scroll = (event: Event) => { if (!(event.target instanceof Node && p.contains(event.target))) fit(); };
    fit();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(fit);
    observer?.observe(anchor);
    window.addEventListener("resize", fit);
    window.visualViewport?.addEventListener("resize", fit);
    window.addEventListener("scroll", scroll, true);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", fit);
      window.visualViewport?.removeEventListener("resize", fit);
      window.removeEventListener("scroll", scroll, true);
    };
  }, [mode]);
  useLayoutEffect(() => {
    const l = list.current, row = active.current;
    if (!l || !row) return;
    if (row.offsetTop < l.scrollTop) l.scrollTop = row.offsetTop;
    else if (row.offsetTop + row.offsetHeight > l.scrollTop + l.clientHeight) l.scrollTop = row.offsetTop + row.offsetHeight - l.clientHeight;
  }, [current, activeKey]);

  useLayoutEffect(() => {
    const p = panel.current, row = active.current, g = glide.current;
    if (g) Object.assign(g.style, { top: `${row?.offsetTop ?? 0}px`, height: `${row?.offsetHeight ?? 0}px`, opacity: row ? 1 : 0 });
    if (!p || !detail) return;
    const rect = p.getBoundingClientRect();
    const w = p.offsetWidth || width;
    const side = rect.left + w + CARD_WIDTH + 2 * EDGE <= innerWidth ? "right" : rect.left >= CARD_WIDTH + 2 * EDGE ? "left" : "inside";
    const ceiling = EDGE - rect.top;
    const height = Math.round(Math.max(1, Math.min(400, p.offsetHeight - ceiling)));
    const rowTop = (list.current?.offsetTop ?? 0) + (row?.offsetTop ?? 0) - (list.current?.scrollTop ?? 0);
    const top = Math.round(Math.max(ceiling, Math.min(rowTop, p.offsetHeight - Math.min(card.current?.scrollHeight ?? 0, height))));
    const label = row?.querySelector<HTMLElement>("[data-picker-title]");
    const clippedTitle = !!label && label.scrollWidth > label.clientWidth;
    setPlace(old => old.side === side && old.top === top && old.height === height && old.clippedTitle === clippedTitle
      ? old : { side, top, height, clippedTitle });
  });

  return (
    <div ref={root} data-composer-panel={mode === "slash" ? undefined : mode} data-slash-palette={mode === "slash" || undefined}
      className={`absolute left-0 z-20 max-w-full ${mode === "plus" ? "bottom-0" : "bottom-full mb-2"}`}
      style={{ width, animation: `pop-in 150ms ${EASE} both`, transformOrigin: "bottom left" }}
      onBlur={onBlur} onMouseDown={e => { if (!(e.target as Element).closest("input")) e.preventDefault(); }}>
      <div ref={panel} className="relative flex max-h-[min(400px,calc(var(--kb-h)*0.5),var(--picker-room,400px))] flex-col rounded-xl border border-line bg-surface p-1.5 shadow-overlay">
        {header}
        {items.length > 0 && (
          <div ref={list} role="listbox" aria-label={label} onScroll={detail ? measure : undefined} className="relative min-h-0 overflow-y-auto overscroll-contain">
            <span ref={glide} aria-hidden className="pointer-events-none absolute inset-x-0 rounded-control bg-hover" style={{ transition: `top 160ms ${EASE}, height 160ms ${EASE}` }} />
            {items.map((item, i) => {
              const heading = group?.(item);
              const hover = () => onHover(i);
              return <Fragment key={itemKey(item)}>
                {heading && (i === 0 || heading !== group?.(items[i - 1])) && (
                  <div data-picker-group className="flex h-6 items-center px-2.5 text-xs font-medium text-ink-3">{heading}</div>
                )}
                {renderRow(item, {
                  ref: i === current ? active : undefined, type: "button", role: "option",
                  "aria-selected": i === current, "aria-describedby": describedBy?.(item, i) ?? (detail && i === current ? detailId : undefined),
                  [moveOnly ? "onMouseMove" : "onMouseEnter"]: hover, onClick: () => onPick(item),
                })}
              </Fragment>;
            })}
          </div>
        )}
        {empty}
        {detail && place.side === "inside" && (
          <div id={detailId} data-picker-detail data-slash-card={mode === "slash" || undefined} className="mt-1 max-h-[min(8rem,18dvh,calc(var(--picker-room,400px)*0.34))] shrink-0 overflow-y-auto overscroll-contain border-t border-line px-2.5 pt-2 pb-1 text-meta leading-normal [overflow-wrap:anywhere]">
            {place.clippedTitle && <div className="font-medium text-ink">{title}</div>}
            {detail}
          </div>
        )}
        {footer}
      </div>
      {detail && place.side !== "inside" && (
        <div ref={card} id={detailId} data-picker-detail data-slash-card={mode === "slash" || undefined}
          className="absolute overflow-y-auto overscroll-contain rounded-xl border border-line bg-surface p-3.5 shadow-overlay [overflow-wrap:anywhere]"
          style={{ width: CARD_WIDTH, left: place.side === "right" ? (panel.current?.offsetWidth || width) + EDGE : -(CARD_WIDTH + EDGE), top: place.top, maxHeight: place.height }}>
          <div className="text-sm font-medium text-ink">{title}</div>
          <div className="mt-2 text-meta leading-normal">{detail}</div>
        </div>
      )}
    </div>
  );
}
