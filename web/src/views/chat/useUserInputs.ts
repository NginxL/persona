import { useCallback, useEffect, useRef, useState } from "react";
import { api, sessionHistoryNotFound, sessionPath } from "../../lib/api";
import { validUserInputIndex, type UserInputIndex } from "../../lib/user-input-index";
import { endPull, pullKey, pullWait, startPull } from "../../lib/pull-gate";

export function useRevealedInputSeq(scope: string, oldestSeq: number | undefined): { seq: number | undefined; reveal: () => void } {
  const [range, setRange] = useState<{ scope: string; oldest: number; revealed?: number } | null>(null);
  useEffect(() => {
    if (oldestSeq === undefined) return;
    setRange((previous) => {
      if (previous?.scope !== scope) return { scope, oldest: oldestSeq };
      if (oldestSeq >= previous.oldest) return previous;
      return { scope, oldest: oldestSeq, revealed: oldestSeq };
    });
  }, [scope, oldestSeq]);
  const reveal = useCallback(() => {
    if (oldestSeq === undefined) return;
    setRange((previous) => previous?.scope === scope && previous.revealed === oldestSeq ? previous : { scope, oldest: oldestSeq, revealed: oldestSeq });
  }, [scope, oldestSeq]);
  return { seq: range?.scope === scope ? range.revealed : undefined, reveal };
}

/** Separate lightweight metadata from transcript loading; never fetch older bodies for a hover or a scroll frame. */
export function useUserInputs(mid: string, sid: string, history: string | undefined, newestInput: string | undefined, enabled: boolean, reload?: () => void): UserInputIndex | null {
  const scope = `${mid}/${sid}/${history ?? ""}`;
  const scopeNow = useRef(scope);
  scopeNow.current = scope;
  const reloadNow = useRef(reload);
  reloadNow.current = reload;
  const [state, setState] = useState<{ scope: string; index: UserInputIndex } | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    const ctl = new AbortController();
    let timer: number | undefined;
    // One read of the index at a time across this tab's views of the session, and after a failure the next waits
    // longer, up to five minutes (lib/pull-gate.ts, #13677): it reads the whole transcript on the computer.
    const key = pullKey("inputs", mid, sid);
    let holding = false; // this view's read is out: it ends the gate's turn, at the latest when the view goes away
    const finish = (outcome: "ok" | "failed" | "aborted") => {
      if (!holding) return;
      holding = false;
      endPull(key, outcome, Date.now());
    };
    const load = () => {
      if (!alive) return;
      const wait = startPull(key, Date.now());
      if (wait > 0) {
        timer = window.setTimeout(load, wait);
        return;
      }
      holding = true;
      void api<unknown>(`${sessionPath(mid, sid)}/items?user_inputs=1&lazy_images=1`, { signal: ctl.signal }).then((index) => {
        if (!alive) return;
        if (!validUserInputIndex(index)) throw new Error("incomplete user input index");
        finish("ok");
        if (scopeNow.current !== scope) return;
        if (history && index.history && index.history !== history) {
          // A complete scan can discover a historical rewrite outside the tail. Refresh bodies before accepting it.
          reloadNow.current?.();
          return;
        }
        setState({ scope, index });
      }).catch((e: unknown) => {
        if (!alive) return;
        // Its computer says this history is gone for good only with a reason; a detail-only answer may recover (#11470).
        if (sessionHistoryNotFound(e) && typeof e.body.reason === "string") {
          finish("ok");
          return;
        }
        // Keep the loaded-only compatibility rail while an older/offline node catches up; failures remain retryable,
        // after the gate's wait.
        finish("failed");
        timer = window.setTimeout(load, pullWait(key, Date.now()));
      });
    };
    load();
    return () => { alive = false; finish("aborted"); ctl.abort(); window.clearTimeout(timer); };
  }, [mid, sid, history, newestInput, enabled, scope]);
  return enabled && state?.scope === scope ? state.index : null;
}
