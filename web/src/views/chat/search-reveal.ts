// Going to an in-conversation search hit (lib/chat-search.ts): the chat names the item, and the work groups, tool rows
// and thoughts that hold it open themselves (Android ChatSearchNavigation expands the rows on the way). What opened for
// a hit closes again with "Back to where you were".
import { useEffect, useSyncExternalStore } from "react";

interface Target {
  scope: string;
  itemId: string;
  /** Each request its own: the same item picked twice opens again. */
  n: number;
}

let target: Target | null = null;
let count = 0;
const listeners = new Set<() => void>();
/** How to put back what the hits opened, newest last. */
let undo: (() => void)[] = [];

const emit = () => listeners.forEach((l) => l());

export function revealItem(scope: string, itemId: string) {
  const request = { scope, itemId, n: ++count };
  target = request;
  emit();
  return () => { if (target === request) clearReveal(); };
}

export function clearReveal() {
  if (!target) return;
  target = null;
  emit();
}

/** Closes what the hits opened (the newest first, so inner rows close before their groups). */
export function undoReveals() {
  const fns = undo.reverse();
  undo = [];
  clearReveal();
  fns.forEach((f) => f());
}

/** Forgets what the hits opened without closing it (another chat). */
export function forgetReveals() {
  undo = [];
  clearReveal();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

const current = () => target;

/** Opens with `open` when the item asked for is one of `ids` in this chat; `open` returns how to close it again, or
 *  nothing when it was open already. Returns whether this row holds the current target, so a deferred row can mount
 *  before the chat looks for the exact hit. */
export function useReveal(scope: string, ids: readonly string[], open: () => (() => void) | void): boolean {
  const t = useSyncExternalStore(subscribe, current, current);
  const hit = Boolean(t && t.scope === scope && ids.includes(t.itemId));
  const n = hit ? t!.n : 0;
  useEffect(() => {
    if (!n) return;
    const close = open();
    if (close) undo.push(close);
    // only a new request opens: `open` changing with the row's state is not one
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [n]);
  return hit;
}
