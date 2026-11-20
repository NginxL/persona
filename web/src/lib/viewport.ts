// Phones: the soft keyboard and touch input. iOS Safari does not shrink the page for the keyboard (the visual viewport
// does, the layout one stays), so the shell sizes itself to what is visible: the composer rides on the keyboard and
// the chat above it is not hidden under it. Chrome on Android resizes the page itself (`interactive-widget` in
// index.html), and 100dvh is enough there.
import { useCallback, useEffect, useSyncExternalStore, type RefObject } from "react";

const KEYBOARD_MIN = 120;

export interface ViewportFrame {
  top: number;
  height: number;
}

/** Where the visible part of the page is while something (the keyboard) covers its bottom; undefined when nothing
 *  does, or the page is pinch-zoomed (the visual viewport is small then too, and following it would jump around). */
export function keyboardFrame(innerHeight: number, vv: { height: number; offsetTop: number; scale: number } | null | undefined): ViewportFrame | undefined {
  if (!vv || Math.abs(vv.scale - 1) > 0.01) return undefined;
  // a scrollbar or a toolbar sliding away is not a keyboard
  if (innerHeight - vv.height < KEYBOARD_MIN) return undefined;
  return { top: Math.max(0, vv.offsetTop), height: Math.round(vv.height) };
}

const KB_VARS = ["--kb-top", "--kb-h"] as const;

/** The visible viewport as the variables fixed overlays on a phone sit in (styles.css `--kb-top` / `--kb-h`); null when
 *  nothing covers the page, and the stylesheet's defaults (0 and 100dvh) apply. */
export function keyboardVars(f: ViewportFrame | undefined): Record<(typeof KB_VARS)[number], string> | null {
  return f ? { "--kb-top": `${f.top}px`, "--kb-h": `${f.height}px` } : null;
}

const typesInto = (el: Element | null) => Boolean(el?.matches('input, textarea, [contenteditable]:not([contenteditable="false"])'));

/** Keeps `ref`'s element over the visible part while the keyboard is up, and `--kb-top` / `--kb-h` on the root element
 *  for the other fixed things (the side panel, sheets), with `data-kb` on it meanwhile for the stylesheet (a waiting
 *  card's own field, styles.css). It sets the styles itself: the keyboard sends a stream of frames as it slides, and a
 *  React state per frame would re-render the whole page. */
export function useKeyboardFrame(ref: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const vv = typeof window === "undefined" ? undefined : window.visualViewport;
    if (!vv) return;
    const html = document.documentElement;
    const root = html.style;
    let last = "";
    // Chrome on Android shrinks the page itself (no frame, 100dvh is right): its keyboard is the page standing well
    // short of the tallest it was at this width while something takes typing. Turning the phone starts over.
    let width = 0;
    let tallest = 0;
    const update = () => {
      if (window.innerWidth !== width) {
        width = window.innerWidth;
        tallest = 0;
      }
      tallest = Math.max(tallest, window.innerHeight);
      const f = keyboardFrame(window.innerHeight, vv);
      html.toggleAttribute("data-kb", Boolean(f) || (tallest - window.innerHeight >= KEYBOARD_MIN && typesInto(document.activeElement)));
      // the visual viewport scrolls while the keyboard stands still: a root variable restyles the whole page, so only
      // a frame that moved is written
      const key = f ? `${f.top}/${f.height}` : "";
      if (key === last) return;
      last = key;
      const vars = keyboardVars(f);
      KB_VARS.forEach((k) => (vars ? root.setProperty(k, vars[k]) : root.removeProperty(k)));
      const el = ref.current;
      if (!el) return;
      el.style.position = f ? "fixed" : "";
      el.style.insetInline = f ? "0" : "";
      el.style.top = f ? `${f.top}px` : "";
      el.style.height = f ? `${f.height}px` : "";
    };
    update();
    vv.addEventListener("resize", update);
    vv.addEventListener("scroll", update);
    window.addEventListener("resize", update);
    return () => {
      vv.removeEventListener("resize", update);
      vv.removeEventListener("scroll", update);
      window.removeEventListener("resize", update);
      KB_VARS.forEach((k) => root.removeProperty(k));
      html.removeAttribute("data-kb");
    };
  }, [ref]);
}

// one list per query: the chat reads them on every render (a streaming reply re-renders it per delta)
const lists = new Map<string, MediaQueryList>();
function mediaList(q: string): MediaQueryList | undefined {
  if (typeof matchMedia !== "function") return undefined;
  let l = lists.get(q);
  if (!l) lists.set(q, (l = matchMedia(q)));
  return l;
}
const matches = (q: string) => Boolean(mediaList(q)?.matches);

const FINE = "(any-pointer: fine)";
const MOUSE_EVENTS = ["pointermove", "pointerdown"] as const;
let sawMouse = false;
let watchingMouse = false;
const fineListeners = new Set<() => void>();

function stopMouseWatch() {
  if (!watchingMouse) return;
  watchingMouse = false;
  MOUSE_EVENTS.forEach((type) => window.removeEventListener(type, mousePointer, true));
  mediaList(FINE)?.removeEventListener("change", mousePointer);
}

function mousePointer(e?: PointerEvent | MediaQueryListEvent) {
  // The media listener also covers an imperative hasFinePointer() caller with no hook mounted.
  // Only a mouse proves the query wrong; a correctly reported stylus already matches fine, and touch must not promote.
  if (e && "pointerType" in e) {
    if (e.pointerType !== "mouse") return;
    sawMouse = true;
  } else if (!matches(FINE)) return;
  stopMouseWatch();
  fineListeners.forEach((listener) => listener());
}

function watchMouse() {
  if (watchingMouse || typeof window === "undefined") return;
  watchingMouse = true;
  // A click need not move the mouse first. Capture before a surface stops propagation; passive because this only observes.
  MOUSE_EVENTS.forEach((type) => window.addEventListener(type, mousePointer, { capture: true, passive: true }));
  mediaList(FINE)?.addEventListener("change", mousePointer);
}

/** Some pointer is precise (a mouse, a trackpad; an iPad with its keyboard and trackpad): Enter sends there. Once a real
 *  mouse pointer is seen it stays true for this page, even on Windows browsers that misreport the media query. */
export function hasFinePointer(): boolean {
  if (sawMouse) return true;
  const fine = matches(FINE);
  if (fine) stopMouseWatch();
  else watchMouse();
  return fine;
}

/** Whether Enter (without Shift) sends: with a precise pointer, yes; on a touch screen alone it is a new line there and
 *  only the button sends. Mod+Enter always sends, but Mod+Alt+Enter is left out: that is the waiting-card shortcut
 *  (lib/shortcuts.ts), and the composer must not swallow the key before the shortcut dispatcher sees it. */
export function enterSends(e: { key: string; shiftKey: boolean; altKey?: boolean; ctrlKey?: boolean; metaKey?: boolean }, fine: boolean): boolean {
  return e.key === "Enter" && !e.shiftKey && (e.ctrlKey || e.metaKey ? !e.altKey : fine);
}

/** The narrow layout (below lg: the sidebar is a drawer, the side panel covers the page): Tailwind's `max-lg:`, kept in step. */
export const NARROW = "(max-width: 1023px)";
/** Below md (Tailwind's `max-md:`, kept in step): a list and its detail take turns instead of sitting side by side. */
export const ONE_PANE = "(max-width: 767px)";
/** A phone on its side: little height for anything above the composer. */
export const SHORT = "(max-height: 500px)";
/** A phone-width screen (Tailwind's max-sm): Dialog is a bottom sheet, Settings a list of categories. */
export const PHONE = "(max-width: 639px)";
/** No hover (a touch screen): what shows on hover elsewhere stays shown. */
export const NO_HOVER = "(hover: none)";
/** Whether a media query matches now (once, not following it). */
export const mediaMatches = (q: string) => matches(q);

/** Whether a media query matches, following it. */
export function useMedia(query: string): boolean {
  const subscribe = useCallback((l: () => void) => {
    const q = mediaList(query);
    q?.addEventListener("change", l);
    return () => q?.removeEventListener("change", l);
  }, [query]);
  return useSyncExternalStore(subscribe, () => matches(query), () => false);
}

function subscribeFine(listener: () => void) {
  fineListeners.add(listener);
  hasFinePointer();
  return () => {
    fineListeners.delete(listener);
  };
}

/** Whether a precise pointer is there, following the media query and the one-time mouse promotion. */
export function useFinePointer(): boolean {
  const fine = useMedia(FINE);
  const mouse = useSyncExternalStore(subscribeFine, () => sawMouse, () => false);
  if (!fine && !mouse) watchMouse();
  return fine || mouse;
}
