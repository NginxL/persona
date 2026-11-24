// A transient user-input navigator at the chat's right edge. Only this small component updates while scrolling;
// cached content offsets are rebuilt on transcript/layout changes, never once per tick on a scroll frame.
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { activeNavRailOffset, navRailMark, NAV_RAIL_MIN_HEIGHT, planNavRailTicks, type NavRailTick } from "../../lib/nav-rail";
import { useFinePointer } from "../../lib/viewport";
import { NAV_RAIL_METRICS as M } from "../../ui/nav-rail-metrics";
import { cx } from "../../ui/atoms";
import { t } from "../../i18n";
import type { QuestionMark, QuestionState } from "../../lib/question-states";

/** A question's state as its tick draws it (#13021): read keeps the plain rail's colors. */
const STATE_TICK: Record<QuestionState, string> = {
  pending: "bg-ink-3",
  running: "bg-accent animate-pulse busy-motion", // a busy indicator: slower, not frozen, under reduced motion (lib/busy-motion.ts)
  unread: "bg-ink",
  read: "",
};

const stateWord = (state: QuestionState) => t(`chat-navrail.state_${state}`);

/** Throttle observer-driven re-measurements so streaming DOM growth does not force full layout every frame. */
export const NAV_RAIL_OBSERVER_MS = 250;

function labelOf(tick: NavRailTick): string {
  return tick.preview || (tick.attachmentsOnly ? t("chat-navrail.attachments", { count: tick.attachmentsOnly }) : tick.ordinal ? t("chat-navrail.input", { index: tick.ordinal }) : "");
}

/** Where the chat's line wrappers (ChatLineView's `[data-line]` boxes) are: the content element's own children, or the
 *  scroller's when it has no content element. A measure reads those children instead of querying the whole transcript
 *  subtree, which grows with every message's markup. */
function lineParent(scroller: HTMLElement): Element {
  for (const el of scroller.children) if (el instanceof HTMLElement && el.dataset.chatContent !== undefined) return el;
  return scroller;
}

const EMPTY_TRACKED: ReadonlySet<string> = new Set();

function isTrackedLine(node: Node, trackedIds: ReadonlySet<string>): boolean {
  return node instanceof HTMLElement && node.dataset.line !== undefined && trackedIds.has(node.dataset.line);
}

function isAfter(lastLine: HTMLElement, node: Node, parent: Element): boolean {
  if (node.parentNode !== parent) return false;
  const pos = lastLine.compareDocumentPosition(node);
  return (pos & Node.DOCUMENT_POSITION_DISCONNECTED) === 0 && (pos & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
}

/** True when every mutation is strictly inside the last measured tick's body or in untracked tail siblings after it
 *  (including streaming assistant `[data-line]` replies below the last user tick), so no tracked tick offset can shift. */
function isTailOnlyMutation(
  records: MutationRecord[],
  parent: Element,
  lastLine: HTMLElement | null,
  trackedIds: ReadonlySet<string>,
): boolean {
  if (!lastLine || lastLine.parentElement !== parent) return false;
  for (const record of records) {
    if (record.target === parent) {
      for (const node of record.addedNodes) if (isTrackedLine(node, trackedIds)) return false;
      for (const node of record.removedNodes) if (isTrackedLine(node, trackedIds)) return false;
      const prev = record.previousSibling;
      if (!prev) return false;
      if (prev !== lastLine && !isAfter(lastLine, prev, parent)) return false;
      continue;
    }
    let child: Node | null = record.target;
    while (child && child.parentNode !== parent) child = child.parentNode;
    if (!child) return false;
    if (child === lastLine) {
      // Replacing the line wrapper's direct child swaps the measured row element itself.
      if (record.target === lastLine) return false;
      continue;
    }
    if (isTrackedLine(child, trackedIds)) return false;
    if (!isAfter(lastLine, child, parent)) return false;
  }
  return true;
}

export function NavRail({ ticks, total, scroller, onJump, reset, marks }: {
  ticks: NavRailTick[];
  total?: number;
  scroller: HTMLDivElement | null;
  onJump: (id: string) => void | Promise<void>;
  reset: string;
  /** Each question's state by line id (lib/question-states.ts), in Persona's chats: its tick shows it, a follow-up's is
   *  shorter, and the rail also works on touch screens, where a tap (or a drag and release) jumps. */
  marks?: ReadonlyMap<string, QuestionMark>;
}) {
  const finePointer = useFinePointer();
  /** A touch screen's rail: only with question states (the plain rail stays off without a precise pointer). */
  const touch = !finePointer && Boolean(marks);
  const fine = finePointer || touch;
  const markOf = (tick: NavRailTick | undefined) => (tick ? marks?.get(tick.lineId ?? tick.id) : undefined);
  /** Questions in progress or unread keep the rail shown: there is something to come back to. */
  const attention = Boolean(marks && [...marks.values()].some((m) => m.state !== "read"));
  const [height, setHeight] = useState(0);
  const [right, setRight] = useState(0);
  const [visible, setVisible] = useState(false);
  const [active, setActive] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [preview, setPreview] = useState<string | null>(null);
  const [windowCenter, setWindowCenter] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const rail = useRef<HTMLElement>(null);
  const card = useRef<HTMLDivElement>(null);
  const frame = useRef(0);
  const idleTimer = useRef(0);
  const jumpRun = useRef(0);
  const hovered = useRef(false);
  const focused = useRef(false);
  const drag = useRef<{ pointer: number; ids: string[]; pitch: number; top: number } | null>(null);
  const selected = useRef<string | null>(null);
  const previewOrigin = useRef<"pointer" | "keyboard" | "drag" | null>(null);
  const suppressClick = useRef(false);
  const list = useRef(ticks);
  list.current = ticks;
  const dirty = useRef(true);
  const checkResize = useRef(false);
  const pendingObserver = useRef<"mutation" | "height" | null>(null);
  const lastClearance = useRef<string | null>(null);
  const lastContentWidth = useRef<number | null>(null);
  const lastTick = useRef<{ id: string; line: HTMLElement; row: HTMLElement; top: number } | null>(null);
  const trackedIds = useRef<ReadonlySet<string>>(EMPTY_TRACKED);
  const observerTimer = useRef(0);
  const lastObserverRun = useRef(-Infinity);
  const offsets = useRef<{ index: number; top: number }[]>([]);
  const previewId = useId();

  const wake = useCallback(() => {
    setVisible(true);
    clearTimeout(idleTimer.current);
    idleTimer.current = window.setTimeout(() => {
      idleTimer.current = 0;
      if (hovered.current || focused.current || drag.current) return;
      setVisible(false);
      setPreview(null);
      previewOrigin.current = null;
      setWindowCenter(null);
    }, M.idleMs);
  }, []);

  const measure = useCallback(() => {
    frame.current = 0;
    if (!scroller || !fine || !list.current.length) return;
    if (!dirty.current && checkResize.current) {
      checkResize.current = false;
      const tail = lastTick.current;
      const last = list.current[list.current.length - 1];
      const lastId = last?.lineId ?? last?.id;
      const content = lineParent(scroller);
      if (
        !tail || tail.id !== lastId || tail.line.parentElement !== content || tail.line.firstElementChild !== tail.row
        || Math.abs((tail.row.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop) - tail.top) >= 0.5
      ) {
        dirty.current = true;
      }
    }
    if (dirty.current) {
      dirty.current = false;
      checkResize.current = false;
      pendingObserver.current = null;
      if (observerTimer.current) {
        clearTimeout(observerTimer.current);
        observerTimer.current = 0;
      }
      setHeight(Math.max(0, scroller.clientHeight - M.padding * 2));
      setRight(Math.max(0, scroller.offsetWidth - scroller.clientWidth) + M.edgeGap);
      const content = lineParent(scroller);
      lastContentWidth.current = content.clientWidth;
      // Wide chats already have a margin. Add only the missing clearance, keeping that column centered.
      const margin = Math.max(0, (scroller.clientWidth - content.clientWidth) / 2);
      const clearance = `${Math.max(0, M.width + M.edgeGap + M.endInset - margin)}px`;
      if (clearance !== lastClearance.current) {
        lastClearance.current = clearance;
        scroller.style.setProperty("--chat-nav-clearance", clearance);
      }
      const rows = new Map<string, { line: HTMLElement; row: HTMLElement }>();
      for (const el of content.children) {
        const child = el.firstElementChild;
        if (el instanceof HTMLElement && el.dataset.line !== undefined && child instanceof HTMLElement) rows.set(el.dataset.line, { line: el, row: child });
      }
      const top = scroller.getBoundingClientRect().top;
      let nextLast: { id: string; line: HTMLElement; row: HTMLElement; top: number } | null = null;
      const tracked = new Set<string>();
      const last = list.current[list.current.length - 1];
      const lastId = last?.lineId ?? last?.id;
      offsets.current = list.current.flatMap((tick, index) => {
        const key = tick.lineId ?? tick.id;
        tracked.add(key);
        const found = rows.get(key);
        if (!found) return [];
        const offsetTop = found.row.getBoundingClientRect().top - top + scroller.scrollTop;
        if (key === lastId) nextLast = { id: key, line: found.line, row: found.row, top: offsetTop };
        return [{ index, top: offsetTop }];
      });
      trackedIds.current = tracked;
      lastTick.current = nextLast;
    }
    const index = activeNavRailOffset(offsets.current, scroller.scrollTop + M.activeOffset);
    setActive(index === null ? null : list.current[index]?.id ?? null);
  }, [scroller, fine]);

  const schedule = useCallback(() => {
    if (!frame.current) frame.current = requestAnimationFrame(measure);
  }, [measure]);

  useLayoutEffect(() => {
    dirty.current = true;
    schedule();
  }, [ticks, scroller, schedule]);

  const hasTicks = ticks.length > 0;
  useEffect(() => {
    if (!scroller || !fine || !hasTicks) return;
    const content = scroller.querySelector<HTMLElement>("[data-chat-content]") ?? scroller.firstElementChild;
    const parent = lineParent(scroller);
    const runObserver = () => {
      const pending = pendingObserver.current;
      pendingObserver.current = null;
      if (!pending) return;
      lastObserverRun.current = performance.now();
      if (pending === "mutation") dirty.current = true;
      else checkResize.current = true;
      schedule();
    };
    const armObserver = (immediate: boolean) => {
      const elapsed = performance.now() - lastObserverRun.current;
      if (immediate && elapsed >= NAV_RAIL_OBSERVER_MS) {
        if (observerTimer.current) {
          clearTimeout(observerTimer.current);
          observerTimer.current = 0;
        }
        runObserver();
        return;
      }
      if (observerTimer.current) return;
      const wait = immediate ? Math.max(0, NAV_RAIL_OBSERVER_MS - elapsed) : NAV_RAIL_OBSERVER_MS;
      observerTimer.current = window.setTimeout(() => {
        observerTimer.current = 0;
        runObserver();
      }, wait);
    };
    const onResize: ResizeObserverCallback = (entries) => {
      let structural = !entries.length;
      for (const entry of entries) {
        if (entry.target === scroller) {
          structural = true;
          break;
        }
        if (entry.target === content) {
          const width = entry.contentRect?.width;
          if (width === undefined || lastContentWidth.current === null || Math.abs(width - lastContentWidth.current) >= 0.5) {
            structural = true;
            break;
          }
        }
      }
      if (structural) {
        dirty.current = true;
        schedule();
        return;
      }
      if (pendingObserver.current !== "mutation") pendingObserver.current = "height";
      armObserver(false);
    };
    const onMutation: MutationCallback = (records) => {
      if (isTailOnlyMutation(records, parent, lastTick.current?.line ?? null, trackedIds.current)) return;
      pendingObserver.current = "mutation";
      armObserver(true);
    };
    const scroll = () => { wake(); schedule(); };
    scroller.addEventListener("scroll", scroll, { passive: true });
    const resize = new ResizeObserver(onResize);
    resize.observe(scroller);
    if (content) resize.observe(content);
    // Disclosure and equal-height replacements can change anchors without changing the outer content height.
    const mutations = new MutationObserver(onMutation);
    if (content) mutations.observe(content, { childList: true, subtree: true });
    return () => {
      scroller.removeEventListener("scroll", scroll);
      scroller.style.removeProperty("--chat-nav-clearance");
      lastClearance.current = null;
      lastContentWidth.current = null;
      lastTick.current = null;
      trackedIds.current = EMPTY_TRACKED;
      checkResize.current = false;
      pendingObserver.current = null;
      resize.disconnect();
      mutations.disconnect();
      clearTimeout(observerTimer.current);
      observerTimer.current = 0;
      if (frame.current) cancelAnimationFrame(frame.current);
      frame.current = 0;
    };
  }, [scroller, fine, hasTicks, schedule, wake]);

  useLayoutEffect(() => {
    hovered.current = focused.current = false;
    drag.current = null;
    selected.current = null;
    previewOrigin.current = null;
    suppressClick.current = false;
    clearTimeout(idleTimer.current);
    jumpRun.current++;
    setActive(null);
    setPending(null);
    setPreview(null);
    setWindowCenter(null);
    setAnnouncement("");
    setVisible(false);
    setHeight(0);
    lastTick.current = null;
    trackedIds.current = EMPTY_TRACKED;
    checkResize.current = false;
    dirty.current = true;
    schedule();
  }, [reset, fine, schedule]);

  useEffect(() => () => {
    clearTimeout(idleTimer.current);
    clearTimeout(observerTimer.current);
    observerTimer.current = 0;
    jumpRun.current++;
    if (frame.current) cancelAnimationFrame(frame.current);
    frame.current = 0;
  }, []);

  const indexOf = (id: string | null) => id === null ? -1 : ticks.findIndex((tick) => tick.id === id);
  const marked = pending ?? active;
  const center = indexOf(windowCenter ?? marked);
  const plan = planNavRailTicks(ticks.length, height, center < 0 ? ticks.length - 1 : center);
  const shown = ticks.slice(plan.start, plan.start + plan.count);
  const previewTick = ticks.find((tick) => tick.id === preview);
  const currentIndex = indexOf(marked);
  const focusedMark = preview ?? pending;
  const previewIndex = indexOf(focusedMark);
  const previewMark = markOf(previewTick);

  useLayoutEffect(() => {
    if (!card.current || !rail.current || !previewTick) return;
    const i = ticks.findIndex((tick) => tick.id === previewTick.id);
    const y = M.padding + (height - plan.count * plan.pitch) / 2 + (i - plan.start + 0.5) * plan.pitch;
    card.current.style.top = `${Math.max(0, Math.min(y - card.current.offsetHeight / 2, height + M.padding * 2 - card.current.offsetHeight))}px`;
  }, [previewTick, ticks, height, plan.start, plan.count, plan.pitch]);

  const dismissPreview = () => {
    previewOrigin.current = null;
    selected.current = null;
    setPreview(null);
  };
  const pick = (id: string, origin: "pointer" | "keyboard" | "drag" = "pointer") => {
    previewOrigin.current = origin;
    selected.current = id;
    setPreview(id);
    wake();
  };
  const jump = async (id: string) => {
    if (!list.current.some((tick) => tick.id === id)) return;
    const run = ++jumpRun.current;
    setPending(id);
    wake();
    const result = onJump(id);
    if (!result) {
      setPending(null);
      return;
    }
    try {
      await result;
    } finally {
      if (run === jumpRun.current) setPending(null);
    }
  };
  const pickY = (y: number) => {
    const bounds = drag.current;
    if (!bounds?.ids.length) return;
    const i = Math.max(0, Math.min(bounds.ids.length - 1, Math.floor((y - bounds.top) / bounds.pitch)));
    const id = bounds.ids[i];
    if (list.current.some((tick) => tick.id === id)) pick(id, "drag");
  };

  if (!fine || !ticks.length || height < NAV_RAIL_MIN_HEIGHT) return null;

  return (
    <nav
      ref={rail}
      aria-label={t("chat-navrail.rail")}
      aria-description={t("chat-navrail.keys")}
      aria-describedby={previewTick ? previewId : undefined}
      data-nav-rail
      data-nav-total={total ?? ticks.length}
      data-nav-source={total === undefined ? "loaded" : "server"}
      data-visible={visible}
      tabIndex={0}
      data-nav-touch={touch || undefined}
      className={cx("absolute inset-y-0 z-10 flex flex-col justify-center py-6 transition-opacity duration-200 motion-reduce:transition-none", visible || pending || attention ? "opacity-100" : "opacity-0")}
      // a touch drag scrubs the ticks rather than scrolling the chat
      style={{ right, width: M.width, touchAction: touch ? "none" : undefined }}
      onMouseEnter={() => {
        if (touch) return;
        hovered.current = true;
        if (!drag.current) setWindowCenter(preview ?? active);
        wake();
      }}
      onMouseLeave={() => {
        if (touch) return;
        hovered.current = false;
        if (!drag.current) {
          if (previewOrigin.current === "pointer") dismissPreview();
          wake(); // Only the rail lingers; the hover card has already gone.
        }
      }}
      onFocus={() => {
        focused.current = true;
        if (touch) { wake(); return; } // a tap focuses the rail too: its release jumps, no preview first
        if (preview === null && !drag.current) pick(active ?? ticks[0].id, "keyboard");
        else wake();
      }}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
          focused.current = false;
          if (!drag.current) {
            if (hovered.current) previewOrigin.current = "pointer";
            else dismissPreview();
          }
          wake();
        }
      }}
      onKeyDown={(e) => {
        if (e.nativeEvent.isComposing || e.altKey || e.ctrlKey || e.metaKey) return;
        const old = indexOf(preview ?? active);
        let next: number | null = null;
        if (e.key === "ArrowDown" || e.key === "ArrowRight") next = Math.min(ticks.length - 1, Math.max(0, old + 1));
        if (e.key === "ArrowUp" || e.key === "ArrowLeft") next = Math.max(0, old - 1);
        if (e.key === "Home") next = 0;
        if (e.key === "End") next = ticks.length - 1;
        if (next !== null) {
          e.preventDefault();
          e.stopPropagation();
          pick(ticks[next].id, "keyboard");
          setWindowCenter(ticks[next].id);
          const m = markOf(ticks[next]);
          const said = t("chat-navrail.selection", { index: ticks[next].ordinal ?? next + 1, count: total ?? ticks.length, preview: labelOf(ticks[next]), answer: ticks[next].answerExcerpt ?? "" });
          setAnnouncement(m ? `${said} ${stateWord(m.state)}` : said);
        } else if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          e.stopPropagation();
          const id = preview ?? active ?? ticks[0].id;
          jump(id);
        } else if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          drag.current = null;
          dismissPreview();
          wake();
        }
      }}
      onPointerDown={(e) => {
        if (e.button !== 0 || card.current?.contains(e.target as Node)) return;
        e.preventDefault();
        const rect = e.currentTarget.getBoundingClientRect();
        drag.current = { pointer: e.pointerId, ids: shown.map((tick) => tick.id), pitch: plan.pitch, top: rect.top + M.padding + (height - plan.count * plan.pitch) / 2 };
        suppressClick.current = true;
        e.currentTarget.setPointerCapture(e.pointerId);
        pickY(e.clientY);
      }}
      onPointerMove={(e) => { if (drag.current?.pointer === e.pointerId) pickY(e.clientY); }}
      onPointerUp={(e) => {
        if (drag.current?.pointer !== e.pointerId) return;
        pickY(e.clientY);
        drag.current = null;
        e.currentTarget.releasePointerCapture(e.pointerId);
        if (selected.current) jump(selected.current);
        previewOrigin.current = "pointer";
        if (!hovered.current) dismissPreview();
      }}
      onPointerCancel={() => { drag.current = null; suppressClick.current = false; dismissPreview(); wake(); }}
      onLostPointerCapture={() => { if (drag.current) { drag.current = null; dismissPreview(); wake(); } }}
      onWheel={(e) => {
        if (plan.hidden) {
          const i = Math.max(0, Math.min(ticks.length - 1, (center < 0 ? 0 : center) + Math.sign(e.deltaY) * plan.count));
          setWindowCenter(ticks[i].id);
          pick(ticks[i].id);
        } else if (scroller) {
          const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? scroller.clientHeight : 1;
          scroller.scrollBy({ top: e.deltaY * unit, left: e.deltaX * unit });
        }
      }}
    >
      {pending && (
        <span role="status" data-nav-loading className="pointer-events-none absolute right-full top-0 whitespace-nowrap rounded-nav-preview border border-line bg-surface px-2 py-1 text-sm text-ink shadow-nav-preview">
          {t("chat-navrail.loading_target")}
        </span>
      )}
      <span className="sr-only" aria-live="polite" aria-atomic="true">{announcement}</span>
      {shown.map((tick, k) => {
        const i = plan.start + k;
        const isCurrent = tick.id === active;
        const isPreview = tick.id === focusedMark;
        const mark = navRailMark(i, ticks.length, currentIndex < 0 ? null : currentIndex, previewIndex < 0 ? null : previewIndex);
        const q = markOf(tick);
        const label = t("chat-navrail.jump", { index: tick.ordinal ?? i + 1, preview: labelOf(tick) });
        const stateTone = q && q.state !== "read" && tick.id !== marked ? STATE_TICK[q.state] : "";
        return (
          <button
            key={tick.id}
            type="button"
            data-tick={i}
            tabIndex={-1}
            aria-label={q ? t(q.follows ? "chat-navrail.jump_follow_state" : "chat-navrail.jump_state", { label, state: stateWord(q.state) }) : label}
            data-question-state={q?.state}
            data-question-follow={q?.follows ? "" : undefined}
            aria-current={isCurrent ? "true" : undefined}
            aria-describedby={tick.id === preview ? previewId : undefined}
            className="relative flex shrink-0 items-center justify-end"
            style={{ height: plan.pitch, paddingRight: M.endInset }}
            onMouseEnter={() => { if (!touch) pick(tick.id); }}
            onClick={() => {
              if (suppressClick.current) { suppressClick.current = false; return; }
              jump(tick.id);
            }}
          >
            <span
              aria-hidden
              data-nav-mark={mark.state}
              className={cx("shrink-0 rounded-full transition-[width,height,background-color,opacity] duration-150 motion-reduce:transition-none", stateTone || (tick.id === marked ? "bg-ink" : isPreview ? "bg-ink-2" : "bg-ink-3"))}
              style={{
                // a follow-up's tick is shorter, under the question it follows
                width: q?.follows ? mark.width * M.followWidth : mark.width,
                height: mark.height,
                // not started: fainter; in progress and unread: as strong as the current input's
                opacity: q?.state === "pending" ? mark.opacity * M.pendingOpacity : q && (q.state === "running" || q.state === "unread") ? Math.max(mark.opacity, M.stateOpacity) : mark.opacity,
              }}
            />
            {q?.state === "unread" && <span aria-hidden data-question-dot className="absolute right-0 top-1/2 -translate-y-1/2 rounded-full bg-red" style={{ width: M.dotSize, height: M.dotSize }} />}
          </button>
        );
      })}
      {previewTick && (
        <div
          ref={card}
          id={previewId}
          data-nav-preview
          data-nav-rail-preview
          className="pointer-events-auto absolute z-20"
          // Padding connects the card's hit box to the rail without a timer or an unhoverable gap.
          style={{ right: M.width, width: M.previewWidth + M.previewGap, paddingRight: M.previewGap, maxWidth: `calc(100vw - ${M.width * 2}px)` }}
        >
          <div data-nav-preview-surface className="rounded-nav-preview border border-line bg-surface shadow-nav-preview" style={{ padding: M.previewPadding }}>
            <p data-nav-preview-title className="truncate text-sm font-semibold text-ink">{labelOf(previewTick)}</p>
            {previewTick.answerExcerpt && <p data-nav-preview-excerpt className="line-clamp-2 text-sm leading-relaxed text-ink-2" style={{ marginTop: M.previewParagraphGap }}>{previewTick.answerExcerpt}</p>}
            {previewMark && <p data-nav-preview-state className="truncate text-sm text-ink-3" style={{ marginTop: M.previewParagraphGap }}>{previewMark.follows ? t("chat-navrail.follow_state", { state: stateWord(previewMark.state) }) : stateWord(previewMark.state)}</p>}
          </div>
        </div>
      )}
    </nav>
  );
}
