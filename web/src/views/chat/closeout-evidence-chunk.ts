import { lazyPage } from "../../lib/lazy-page";
import { shownCloseout } from "../../lib/closeout";
import { getState, subscribe, type State } from "../../lib/store";

// The card and its store-driven preloader import this module, never the card's own lazy loader. This keeps Vite's
// circular-import recovery out of the card's initialization when tests reset modules while a lazy import is pending.
export const CloseoutEvidence = lazyPage(() => import("./CloseoutEvidence").then((m) => m.CloseoutEvidence));

type Chunk = "idle" | "loading" | "failed" | "retrying";
let chunk: Chunk = "idle";
const chunkListeners = new Set<() => void>();
const setChunk = (next: Chunk) => {
  chunk = next;
  chunkListeners.forEach((l) => l());
};
export const onCloseoutEvidenceChunk = (l: () => void) => {
  chunkListeners.add(l);
  return () => void chunkListeners.delete(l);
};
export const closeoutEvidenceChunk = (): Chunk | "in" => (CloseoutEvidence.ready() ? "in" : chunk);

/** Loads the evidence chunk (again after a failure); nothing while it is in or on its way. */
export function loadCloseoutEvidence() {
  if (CloseoutEvidence.ready() || chunk === "loading" || chunk === "retrying") return;
  setChunk(chunk === "failed" ? "retrying" : "loading");
  CloseoutEvidence.preload().then(() => setChunk("idle"), () => setChunk("failed"));
}

type SaverWindow = { navigator?: { connection?: { saveData?: boolean } } };

/** Starts the evidence chunk once the store holds a session whose closeout a card would show. Data Saver waits until
 *  the card opens. Returns the stop. */
export function preloadCloseoutEvidence(w: SaverWindow = globalThis as SaverWindow): () => void {
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
    loadCloseoutEvidence();
  };
  const stop = subscribe(check);
  check();
  return stop;
}
