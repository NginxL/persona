// One automatic read of a session's items at a time, and after a failure the next waits longer (#13677).
//
// A chat page reads a session on its own: the stale-page safety net (`items?after_seq=`), a gap fill, the user-input
// index (`items?user_inputs=1`). Each kept its own state, so two views of one session (the chat and its background
// activity dialog) asked side by side, and a failure was asked again on the next tick: on a slow link to the computer
// every ask took longer than the one before (3.5 s, 10 s, 29.5 s) and the page kept the link busy with copies of the
// same read. The gate is shared by every view in this tab, keyed by what is read: a second ask while one is out is
// not sent, and after a failure the next waits `PULL_RETRY_MS`, doubling to `PULL_RETRY_MAX_MS` (it used to stop
// doubling at 30 s, and the stale pull asked again every 10 s however often it failed). A success resets it.
// What a person does (opening a chat, scrolling up, opening a fold) does not go through it.

/** The wait after the first failure (the index's retry always started here; the stale pull asks at most every 10 s
 *  anyway, so for it the gate starts to matter from the third failure in a row). */
export const PULL_RETRY_MS = 3000;
/** The longest wait between automatic asks that keep failing. */
export const PULL_RETRY_MAX_MS = 5 * 60_000;
/** How soon a view whose ask found another one out looks again. */
export const PULL_BUSY_MS = 1000;

export type PullKind = "after" | "inputs";

interface Gate {
  busy: boolean;
  failures: number;
  notBefore: number;
}

const gates = new Map<string, Gate>();

export const pullKey = (kind: PullKind, mid: string, sid: string) => `${kind}:${mid}/${sid}`;

function gate(key: string): Gate {
  let g = gates.get(key);
  if (!g) {
    g = { busy: false, failures: 0, notBefore: 0 };
    gates.set(key, g);
  }
  return g;
}

/** The wait after `failures` failures in a row. */
export function retryDelay(failures: number): number {
  if (failures <= 0) return 0;
  return Math.min(PULL_RETRY_MS * 2 ** (failures - 1), PULL_RETRY_MAX_MS);
}

/** How long an ask for key would wait now (0: it may go), without starting one. */
export function pullWait(key: string, now: number): number {
  const g = gates.get(key);
  if (!g) return 0;
  if (g.busy) return PULL_BUSY_MS;
  return Math.max(0, g.notBefore - now);
}

/** Starts an ask for key and returns 0, or returns how long to wait first: another ask is out, or the last failed. */
export function startPull(key: string, now: number): number {
  const g = gate(key);
  if (g.busy) return PULL_BUSY_MS;
  if (now < g.notBefore) return g.notBefore - now;
  g.busy = true;
  return 0;
}

/** Ends the ask startPull started. An aborted one (the view went away) neither resets nor counts. */
export function endPull(key: string, outcome: "ok" | "failed" | "aborted", now: number): void {
  const g = gate(key);
  g.busy = false;
  if (outcome === "ok") {
    g.failures = 0;
    g.notBefore = 0;
  } else if (outcome === "failed") {
    g.failures++;
    g.notBefore = now + retryDelay(g.failures);
  }
  if (!g.busy && g.failures === 0) gates.delete(key);
}

/** Tests only. */
export function resetPullGatesForTests() {
  gates.clear();
}
