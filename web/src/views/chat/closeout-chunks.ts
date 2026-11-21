import { use } from "react";
import { lazyPage } from "../../lib/lazy-page";
import { shownCloseout } from "../../lib/closeout";
import { getState, subscribe, type State } from "../../lib/store";
import { preloadCloseoutEvidence } from "./closeout-evidence-chunk";

let reportReader: typeof import("../../lib/closeout-text").stripCloseout | undefined;
export const LazyCloseoutCard = lazyPage(() => import("./CloseoutCard").then((module) => {
  reportReader = module.stripCloseout;
  return module.CloseoutCard;
}));

/** The card and its duplicate-text reader arrive together. Only the reply shown by that card waits for its chunk. */
export function useCloseoutReader(shown: boolean) {
  if (!shown) return undefined;
  if (!reportReader) use(LazyCloseoutCard.preload());
  return reportReader;
}

type SaverWindow = { navigator?: { connection?: { saveData?: boolean } } };

/** Preloads the card and its evidence once a session has a visible closeout. Data Saver waits until the card opens. */
export function preloadCloseout(w: SaverWindow = globalThis as SaverWindow): () => void {
  if (w.navigator?.connection?.saveData) return () => {};
  let sessions: State["sessions"] | undefined;
  let summaries: State["sessionSummaries"] | undefined;
  const check = () => {
    const s = getState();
    if (s.sessions === sessions && s.sessionSummaries === summaries) return;
    sessions = s.sessions;
    summaries = s.sessionSummaries;
    if (!Object.values(sessions).some((rows) => rows.some((row) => shownCloseout(row))) && !summaries.some((row) => shownCloseout(row))) return;
    stop();
    void LazyCloseoutCard.preload().catch(() => {});
    preloadCloseoutEvidence(w);
  };
  const stop = subscribe(check);
  check();
  return stop;
}
