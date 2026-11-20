// Input-method (IME) composition for text fields whose Enter does something: picking a CJK candidate is not a send.
import { useCallback, useRef, useState } from "react";
import { composing } from "./send-history";

/** How long after `compositionend` a key still belongs to the composition. macOS Safari and WKWebView fire
 *  `compositionend` just before the `keydown` of the Enter that committed the candidate, with `isComposing` false. */
export const COMPOSITION_TAIL_MS = 80;

export interface ImeState {
  /** Between `compositionstart` and `compositionend`. */
  active: boolean;
  /** `performance.now()` at the last `compositionend`; -Infinity before any. */
  endedAt: number;
}

/** Whether a key event is the input method's own: composing, Safari's 229, or right after the composition ended. */
export function imeKey(e: { isComposing?: boolean; keyCode?: number }, state: ImeState, now = performance.now()): boolean {
  return composing(e) || state.active || now - state.endedAt < COMPOSITION_TAIL_MS;
}

/** A field's composition tracking: spread `handlers` on it and ask `busy(e.nativeEvent)` before acting on a key.
 *  `composing` re-renders while a composition is open (palettes wait for it to end); `tracker` is the live state. */
export function useComposition() {
  const tracker = useRef<ImeState>({ active: false, endedAt: -Infinity });
  const [open, setOpen] = useState(false);
  const onCompositionStart = useCallback(() => {
    tracker.current.active = true;
    setOpen(true);
  }, []);
  const onCompositionEnd = useCallback(() => {
    tracker.current = { active: false, endedAt: performance.now() };
    setOpen(false);
  }, []);
  // a field that loses focus mid-composition may never get its compositionend
  const onBlur = useCallback(() => {
    if (!tracker.current.active) return;
    tracker.current.active = false;
    setOpen(false);
  }, []);
  const busy = useCallback((e: { isComposing?: boolean; keyCode?: number }) => imeKey(e, tracker.current), []);
  return { composing: open, busy, tracker, handlers: { onCompositionStart, onCompositionEnd }, onBlur };
}
