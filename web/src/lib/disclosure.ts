// Expanding and collapsing in a scrolled list without moving what the reader is looking at (#1124).
//
// The chat holds the row that was clicked (its top, or the bottom of a work row folded from its end) where it was on
// screen while the height runs: every time the content's size changes, the scroll offset moves by however far that
// edge moved. At the end (following the newest) it keeps following instead: the bottom stays pinned, unless that
// would push the clicked row off the top, or move it more than `FOLLOW_STEP` in one frame (a folded trace's steps
// arriving at once), where it stops following and the row stays put.

import { t } from "../i18n";

export interface ScrollAnchor {
  /** Which edge of the element is held: a header's top, or a work row's bottom when it folds from its "Collapse" button. */
  edge: "top" | "bottom";
  /** Where that edge was on screen (px from the viewport top) when it was clicked. */
  at: number;
  /** The list was at the end when it was clicked: keep the end in view. */
  follow: boolean;
  /** Folding from inside a trace: the furthest down the element's top may come on screen (#1486). Holding the end
   *  brings the header down from above; it stops here instead of going on past the view's bottom. */
  topLimit?: number;
}

export interface ScrollMetrics {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
  /** The scroll view's top on screen. */
  viewTop: number;
  /** The held edge's position on screen now. */
  edgeAt: number;
  /** The held element's top on screen now (following never pushes it above `viewTop + margin`). */
  elTop: number;
}

/** How close to the scroll view's top a followed row may go before following stops. */
export const FOLLOW_MARGIN = 12;
/** The most a followed row moves in one frame: an unfolding row moves ~25 px a frame; more is content landing at once. */
export const FOLLOW_STEP = 64;

/** Where to scroll so the anchor holds, and whether it still follows the end. */
export function anchoredScroll(a: ScrollAnchor, m: ScrollMetrics): { scrollTop: number; follow: boolean } {
  const max = Math.max(0, m.scrollHeight - m.clientHeight);
  if (a.follow) {
    // Pinning the end moves everything up by (max - scrollTop); stop where the row would leave the top.
    const move = max - m.scrollTop;
    const floor = m.viewTop + FOLLOW_MARGIN;
    if (move > FOLLOW_STEP) return { scrollTop: m.scrollTop, follow: false };
    if (m.elTop - move >= floor || m.elTop < floor) return { scrollTop: max, follow: true };
    return { scrollTop: Math.min(max, Math.max(0, m.scrollTop + (m.elTop - floor))), follow: false };
  }
  let move = m.edgeAt - a.at;
  if (a.topLimit !== undefined) move = Math.max(move, m.elTop - a.topLimit);
  return { scrollTop: Math.min(max, Math.max(0, m.scrollTop + move)), follow: false };
}

/** What a collapse renders: lazy ones mount their children when they open and keep them while the height runs back to
 *  zero (the content goes down with the height instead of vanishing), then drop them. */
export function collapseMounted(lazy: boolean, open: boolean, closing: boolean): boolean {
  return !lazy || open || closing;
}

// ── following the end (cindy's autoFollowIntent) ──
//
// Only the reader leaves the end, and only by saying so: the wheel or a trackpad going up, a finger pulling the list
// down, PageUp / ↑ / Home, dragging the scroll bar up. A scroll event alone never does (#1340: the chat's own pin whose
// event lands after more content did sees the end far away). Back near the end, following picks up again.

/** At most this far from the end is at the end: following again whichever way the reader scrolled. */
export const REPIN_AT_BOTTOM = 8;
/** Scrolling down within this far of the end follows again. */
export const REPIN_NEAR = 100;
/** A finger has to move this far before it counts as pulling the list. */
export const TOUCH_INTENT = 8;
/** A scroll bar drag moving the offset up by more than this leaves the end. */
export const SCROLLBAR_DEAD_ZONE = 2;
/** Unfolding a card holds off following this long, the clicked row held where it was. */
export const UNFOLD_PAUSE = 300;

const UP_KEYS = new Set(["PageUp", "ArrowUp", "Home"]);

/** Whether there is anything to scroll (a list that fits cannot leave its end). */
const scrollable = (m: { scrollHeight: number; clientHeight: number }) => m.scrollHeight - m.clientHeight > 1;

export function leavesOnWheel(m: { deltaX: number; deltaY: number; scrollHeight: number; clientHeight: number }): boolean {
  return m.deltaY < 0 && Math.abs(m.deltaY) >= Math.abs(m.deltaX) && scrollable(m);
}

/** A finger moving down (the list's older part coming into view) by more than TOUCH_INTENT. */
export function leavesOnTouch(m: { startY: number; y: number; scrollHeight: number; clientHeight: number }): boolean {
  return m.y - m.startY > TOUCH_INTENT && scrollable(m);
}

export function leavesOnKey(m: { key: string; scrollHeight: number; clientHeight: number }): boolean {
  return UP_KEYS.has(m.key) && scrollable(m);
}

/** A press on the view's own scroll bar: the target is the view itself and the point is past its content width. */
export function isScrollbarPress(m: { targetIsView: boolean }): boolean {
  // a press on the view itself, not on what is in it: overlay scroll bars (macOS, iOS) sit inside clientWidth, so
  // where the press landed horizontally says nothing
  return m.targetIsView;
}

export function leavesOnScrollbarDrag(m: { dragging: boolean; delta: number }): boolean {
  return m.dragging && m.delta < -SCROLLBAR_DEAD_ZONE;
}

/** Whether a scroll event while not following brings following back: the reader scrolls down near the end. (While
 *  following, a scroll event changes nothing: only the intents above leave.) */
export function followsAgain(m: { gap: number; delta: number }): boolean {
  return m.gap < REPIN_NEAR && m.delta > 0;
}

/** The pill while away from the end: "New messages" when something came in since leaving it, else "Jump to bottom". */
export function jumpLabel(newSince: boolean): string {
  return newSince ? t("lib-disclosure.new_messages") : t("lib-disclosure.jump_to_bottom");
}

/** After the pause an unfolded card holds following for: pick it up again only when the view was following, the
 *  reader did not move in the meantime, and the end is still near. */
export function followsAfterUnfold(m: { wasAtEnd: boolean; readerMoved: boolean; gap: number }): boolean {
  return m.wasAtEnd && !m.readerMoved && m.gap <= REPIN_NEAR;
}
