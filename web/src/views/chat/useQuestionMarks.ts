// A Persona chat's question states for its rail (#13021): the marks from lib/question-states.ts over the loaded lines
// and the hub's read marks, an answer marked read once it has been on screen (or jumped to), and the marks published
// for the question summary (lib/question-reads.ts).
import { useCallback, useEffect, useMemo, useRef } from "react";
import type { NavRailTick } from "../../lib/nav-rail";
import { publishQuestionMarks, useQuestionReads } from "../../lib/question-reads";
import { questionMarks, type QuestionMark } from "../../lib/question-states";
import type { ChatLine } from "../../lib/turns";

/** How much of an answer's exchange has to be on screen to count as opened: this share of it, or of the view when it is
 *  taller than the view. */
export const READ_VISIBLE_SHARE = 0.5;

/** Whether a row whose box is `row` counts as opened in a view whose box is `view`. */
export function rowOpened(row: { top: number; bottom: number }, view: { top: number; bottom: number }): boolean {
  const shown = Math.min(row.bottom, view.bottom) - Math.max(row.top, view.top);
  const height = row.bottom - row.top;
  return height > 0 && shown > 0 && shown >= Math.min(height, view.bottom - view.top) * READ_VISIBLE_SHARE;
}

export function useQuestionMarks({ mid, sid, on, lines, busy, ticks, scroller }: {
  mid: string;
  sid: string;
  on: boolean;
  lines: ChatLine[];
  busy: string | undefined;
  ticks: NavRailTick[];
  scroller: HTMLElement | null;
}): { marks: ReadonlyMap<string, QuestionMark> | undefined; opened: (tickId: string) => void } {
  const { reads, markRead } = useQuestionReads(mid, sid, on);
  const marks = useMemo(() => (on ? questionMarks(lines, busy, reads) : undefined), [on, lines, busy, reads]);
  const scope = `${mid}/${sid}`;
  useEffect(() => {
    publishQuestionMarks(scope, marks);
  }, [scope, marks]);
  useEffect(() => () => publishQuestionMarks(scope, undefined), [scope]);

  const now = useRef({ marks, ticks });
  now.current = { marks, ticks };
  /** A jump to a question opens its answer. */
  const opened = useCallback((tickId: string) => {
    const tick = now.current.ticks.find((x) => x.id === tickId);
    const m = now.current.marks?.get(tick?.lineId ?? tickId);
    if (m?.state === "unread") markRead([m.key]);
  }, [markRead]);

  // an unread answer on screen while the page is in front is opened
  useEffect(() => {
    if (!scroller || !marks) return;
    const unread = [...marks].filter(([, m]) => m.state === "unread");
    if (!unread.length) return;
    let frame = 0;
    const check = () => {
      frame = 0;
      if (document.visibilityState !== "visible") return;
      const view = scroller.getBoundingClientRect();
      const seen: string[] = [];
      const want = new Map(unread);
      // the line wrappers are the content element's children (as NavRail reads them), so no selector escaping; a
      // wrapper may draw no box of its own: its first child is the exchange
      const content = scroller.querySelector("[data-chat-content]") ?? scroller;
      for (const el of content.children) {
        const m = el instanceof HTMLElement && el.dataset.line !== undefined ? want.get(el.dataset.line) : undefined;
        const row = m && el.firstElementChild;
        if (m && row && rowOpened(row.getBoundingClientRect(), view)) seen.push(m.key);
      }
      if (seen.length) markRead(seen);
    };
    const schedule = () => { if (!frame) frame = requestAnimationFrame(check); };
    schedule();
    scroller.addEventListener("scroll", schedule, { passive: true });
    document.addEventListener("visibilitychange", schedule);
    // the chat lays out after the marks arrive (a first page, pictures loading): look again when it changes size
    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(schedule);
    resize?.observe(scroller);
    if (scroller.firstElementChild) resize?.observe(scroller.firstElementChild);
    return () => {
      resize?.disconnect();
      if (frame) cancelAnimationFrame(frame);
      scroller.removeEventListener("scroll", schedule);
      document.removeEventListener("visibilitychange", schedule);
    };
  }, [scroller, marks, markRead]);

  return { marks, opened };
}
