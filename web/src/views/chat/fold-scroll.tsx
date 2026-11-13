// A folded block (a long message's first ten lines, a subagent's prompt, a recap) scrolls inside its fold: the wheel, a
// trackpad or a finger over it shows what is folded away, and at its top or end the chat scrolls on (the browser's
// default overscroll chain, no `overscroll-behavior: contain`). Its bottom fades while there is more below.
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode, type RefObject } from "react";
import { cx } from "../../ui/atoms";

/** The attribute on a fold that scrolls; the chat's scroller looks for it so a wheel inside a fold does not count as
 *  the reader leaving the end. */
export const FOLD_SCROLL = "data-fold-scroll";

/** The fade at a fold's bottom while there is more below it. */
export const FOLD_FADE = "linear-gradient(180deg,#000 calc(100% - 28px),transparent)";

interface Box {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
}

/** Whether a box has more below what it shows. */
export function moreBelow(b: Box): boolean {
  return b.scrollTop + b.clientHeight < b.scrollHeight - 2;
}

/** Whether a box can still scroll by `dy` (down when positive): if not, the wheel goes on to the chat. */
export function scrollsBy(b: Box, dy: number): boolean {
  if (dy > 0) return moreBelow(b);
  if (dy < 0) return b.scrollTop > 0;
  return false;
}

/** Whether the wheel or a finger at `target` scrolls a fold (inside `root`) rather than the chat. */
export function scrollsAFold(target: EventTarget | null, root: Element, dy: number): boolean {
  const el = target && typeof (target as Element).closest === "function" ? (target as Element).closest(`[${FOLD_SCROLL}]`) : null;
  return !!el && root.contains(el) && scrollsBy(el, dy);
}

/** The props for a fold that scrolls while `folded`: its onScroll, the fade while there is more below, the attribute.
 *  It measures again when it resizes and when any of `deps` changes. */
export function useFoldScroll(ref: RefObject<HTMLElement | null>, folded: boolean, deps: unknown[]) {
  // before it is measured (and drawn on the server) a fold has more below: it is at its top
  const [more, setMore] = useState(true);
  const measure = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const m = moreBelow(el);
    setMore((v) => (v === m ? v : m));
  }, [ref]);
  // only while folded: an open block draws no fade (reading every bubble's height on a chat's first draw forced a layout)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useLayoutEffect(() => void (folded && measure()), [measure, folded, ...deps]);
  useEffect(() => {
    const el = ref.current;
    // no ResizeObserver (jsdom): measured on each change of `deps` only, as CommandScreenCard's fold does
    if (!el || !folded || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref, measure, folded]);
  if (!folded) return {};
  const mask = more ? FOLD_FADE : undefined;
  const style: CSSProperties | undefined = mask ? { maskImage: mask, WebkitMaskImage: mask } : undefined;
  return { onScroll: measure, style, [FOLD_SCROLL]: "" };
}

/** A block always folded to `className`'s max-height (a subagent's prompt, a recap), scrolling inside it. */
export function ScrollFold({ className, children, ...rest }: { className: string; children: ReactNode } & Record<`data-${string}`, string | boolean | undefined>) {
  const ref = useRef<HTMLDivElement>(null);
  const fold = useFoldScroll(ref, true, [children]);
  return (
    <div ref={ref} {...rest} {...fold} className={cx("overflow-y-auto", className)}>
      {children}
    </div>
  );
}
