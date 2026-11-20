// Where the toasts sit on a narrow screen (Toasts, ui/atoms.tsx): in the visible frame above the keyboard, clear of
// the chat's dock or the footer of the sheet on top. A chunk of its own, since only a narrow screen with a toast showing
// needs it: the app loads it once the screen is narrow (App.tsx), so in practice it is in before the first toast.
// It imports nothing of the app's own: a module it shared with the first screen would split out into a chunk of
// its own, which costs the first screen more than moving this off it saves.
import { useLayoutEffect, useRef, type RefObject } from "react";

const TOAST_EDGE = 20; // bottom-5: the toasts' room under them, above the frame's edge or what they keep clear of
const TOAST_GAP = 8; // gap-2

// The frame --kb-top / --kb-h (lib/viewport.ts) leaves, the stack at its bottom, lifted by --toast-lift (or by the home
// indicator's inset when nothing is there).
const PLACE = {
  top: "var(--kb-top)",
  bottom: "auto",
  height: "var(--kb-h)",
  justifyContent: "flex-end",
  paddingBottom: `calc(var(--toast-lift, env(safe-area-inset-bottom)) + ${TOAST_EDGE}px)`,
} as const;

/** How far the toasts sit above the bottom of the visible frame (`--toast-lift`): clear of what reaches into their place
 *  there, the chat's dock, or the footer of the sheet on top (the page under a modal is covered, so only its own footer
 *  counts). Null: nothing there, the home indicator's inset applies; with the keyboard up there is none. Also returns
 *  what it measured, for the caller to watch. */
function toastLift(box: HTMLElement, rows: Iterable<HTMLElement>, keyboardUp: () => boolean): { lift: string | null; watched: HTMLElement[] } {
  const frame = box.getBoundingClientRect();
  let stack = -TOAST_GAP;
  for (const row of rows) stack += row.offsetHeight + TOAST_GAP;
  stack = Math.max(0, stack);
  const reach = frame.bottom - TOAST_EDGE - stack; // the stack's top with nothing to keep clear of
  const modals = [...document.querySelectorAll<HTMLElement>('[role="dialog"][aria-modal="true"]')].filter((m) => m.getClientRects().length);
  const top = modals[modals.length - 1];
  const watched = [...(top ?? document).querySelectorAll<HTMLElement>(top ? "[data-dialog-footer]" : "[data-chat-dock]")];
  let edge = Infinity;
  for (const el of watched) {
    const r = el.getBoundingClientRect();
    if (r.height && r.bottom > reach && r.top < frame.bottom) edge = Math.min(edge, r.top);
  }
  if (edge === Infinity) return { lift: keyboardUp() ? "0px" : null, watched };
  // never pushed out of the top of the frame: a dock as tall as what the keyboard leaves gets them over its top part
  const lift = Math.max(0, Math.min(frame.bottom - edge, frame.height - TOAST_EDGE - stack - TOAST_GAP));
  return { lift: `${Math.round(lift)}px`, watched };
}

// what toastLift measures, and what holds it (a sheet's backdrop holds its dialog)
const LIFT_NODES = '[data-chat-dock], [role="dialog"], [data-dialog-footer]';

/** Whether a mutation can move what the toasts keep clear of: an aria-modal change, or such a node added or removed. */
function liftChanged(records: MutationRecord[]): boolean {
  for (const r of records) {
    if (r.type === "attributes") return true;
    for (const nodes of [r.addedNodes, r.removedNodes]) {
      for (const n of nodes) if (n instanceof Element && (n.matches(LIFT_NODES) || n.querySelector(LIFT_NODES))) return true;
    }
  }
  return false;
}

/** Places `box` (the toasts' stack) while mounted: Toasts mounts it while a toast shows on a narrow screen, and only
 *  then. What it keeps clear of may grow (the composer's lines), come or go (a sheet, a chat), or move with the keyboard.
 *  Writes the style itself, at most once a frame, with no React state; a change to the list is measured at once. */
export interface ToastLiftProps {
  box: RefObject<HTMLDivElement | null>;
  rows: RefObject<Map<number, HTMLDivElement>>;
  /** Whether the keyboard covers the page's bottom (lib/viewport.ts keyboardFrame). */
  keyboardUp: () => boolean;
}

export function ToastLift({ box, rows, keyboardUp }: ToastLiftProps) {
  const measure = useRef<(() => void) | null>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    Object.assign(el.style, PLACE);
    let frame = 0;
    let seen: HTMLElement[] = [];
    const later = () => {
      if (!frame) frame = requestAnimationFrame(now);
    };
    const resize = typeof ResizeObserver === "function" ? new ResizeObserver(later) : null;
    const now = () => {
      cancelAnimationFrame(frame);
      frame = 0;
      const { lift, watched } = toastLift(el, rows.current.values(), keyboardUp);
      if ((lift ?? "") !== el.style.getPropertyValue("--toast-lift")) {
        if (lift === null) el.style.removeProperty("--toast-lift");
        else el.style.setProperty("--toast-lift", lift);
      }
      if (resize && (watched.length !== seen.length || watched.some((w, i) => w !== seen[i]))) {
        resize.disconnect();
        // its border box: the lift written into its padding does not come back as a resize to measure again
        resize.observe(el, { box: "border-box" });
        watched.forEach((w) => resize.observe(w));
        seen = watched;
      }
    };
    // a chat or a sheet coming or going; a modal closing (it drops aria-modal at once, and leaves after its exit). Only
    // those wake it: a sticky toast can stay all session, over a reply streaming in a node at a time.
    const changes = new MutationObserver((records) => liftChanged(records) && later());
    changes.observe(document.body, { childList: true, subtree: true, attributeFilter: ["aria-modal"] });
    const vv = window.visualViewport;
    vv?.addEventListener("resize", later);
    vv?.addEventListener("scroll", later);
    window.addEventListener("resize", later);
    // a sheet sliding up is measured where it ends
    document.addEventListener("transitionend", later, true);
    document.addEventListener("animationend", later, true);
    measure.current = now;
    return () => {
      measure.current = null;
      cancelAnimationFrame(frame);
      resize?.disconnect();
      changes.disconnect();
      vv?.removeEventListener("resize", later);
      vv?.removeEventListener("scroll", later);
      window.removeEventListener("resize", later);
      document.removeEventListener("transitionend", later, true);
      document.removeEventListener("animationend", later, true);
      el.style.removeProperty("--toast-lift");
      for (const k of Object.keys(PLACE) as (keyof typeof PLACE)[]) el.style[k] = "";
    };
  }, [box, rows, keyboardUp]);
  // every render of Toasts, ahead of its glides (a child's layout effects run first): a new toast shows clear of the
  // composer from its first frame, and the glides see where it went
  useLayoutEffect(() => {
    measure.current?.();
  });
  return null;
}
