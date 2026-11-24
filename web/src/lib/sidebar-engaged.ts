// Holding the Agents list still while someone is using it (Settings › Agents › "Sort Agents by recent activity", #11801):
// the activity order only moves while the pointer is away from the sidebar and nothing in it has keyboard focus, so a row
// never slides out from under a hand that is about to click it.
import { useEffect, useMemo, useRef, useState, type RefObject } from "react";

/** The sidebar's root; a block drawn outside one (a test) holds still for its own element. */
const ROOT = "[data-sidebar-root]";

/** Whether the pointer is over the sidebar `inside` belongs to, or focus the keyboard put there is in it. A touch is not a
 *  pointer over it (it lifts), and a row focused by a click is not keyboard focus (`:focus-visible`). */
export function useSidebarEngaged(inside: RefObject<HTMLElement | null>): boolean {
  const [engaged, setEngaged] = useState(false);
  useEffect(() => {
    const root = inside.current?.closest<HTMLElement>(ROOT) ?? inside.current;
    if (!root) return;
    let over = false, focused = false;
    const sync = () => setEngaged(over || focused);
    const enter = (e: PointerEvent) => { if (e.pointerType !== "touch") { over = true; sync(); } };
    const leave = () => { over = false; sync(); };
    const focusIn = (e: FocusEvent) => {
      let visible = true;
      try { visible = (e.target as HTMLElement).matches(":focus-visible"); } catch { /* no :focus-visible: any focus holds */ }
      focused = visible; sync();
    };
    const focusOut = (e: FocusEvent) => { if (!(e.relatedTarget instanceof Node && root.contains(e.relatedTarget))) { focused = false; sync(); } };
    root.addEventListener("pointerenter", enter);
    root.addEventListener("pointerleave", leave);
    root.addEventListener("focusin", focusIn);
    root.addEventListener("focusout", focusOut);
    return () => {
      root.removeEventListener("pointerenter", enter);
      root.removeEventListener("pointerleave", leave);
      root.removeEventListener("focusin", focusIn);
      root.removeEventListener("focusout", focusOut);
    };
  }, [inside]);
  return engaged;
}

/** `items` in the order they had when `frozen` began: the ones still there keep their relative places, a new one comes
 *  first (it is the newest), a gone one is gone. Not frozen it is `items` and remembers their order. */
export function useHeldOrder<T>(items: readonly T[], id: (item: T) => string, frozen: boolean): readonly T[] {
  const order = useRef<ReadonlyMap<string, number>>(new Map());
  if (!frozen) order.current = new Map(items.map((item, i) => [id(item), i]));
  // `id` is a stable function of the caller's list: the memo follows the items and the hold
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => {
    if (!frozen) return items;
    const held = order.current;
    return items.map((item, i) => ({ item, at: held.get(id(item)) ?? -1, i })).sort((a, b) => a.at - b.at || a.i - b.i).map((x) => x.item);
  }, [items, frozen]);
}
