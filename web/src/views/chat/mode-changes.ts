// Permission mode change lines in an Agent's chat (#10297, docs/protocol/protocol.md "Session mode changes"): the
// machine's manager keeps each change made through /modes (who, on which device, from which mode to which) and what
// became of a queued restart into Full access. They are not transcript items, so they join the chat after its day
// dividers, each before the first row that came after it; the transcript's lines, their keys and everything worked out
// from them stay as they were. ChatView loads this module, its divider and their words (chat-modes) when an Agent's
// chat opens, so none of it weighs on the first screen.
import { api, sessionPath } from "../../lib/api";
import { loadedFrom } from "../../lib/feed";
import type { Item } from "../../lib/types";
import { chatLineTime, type ChatDayRow } from "./day-dividers";
export { ModeChangeDivider } from "./ModeChangeDivider";

/** One row of GET …/mode-changes. */
export type ModeChange = {
  kind: "set" | "restart_done" | "restart_failed" | "restart_dropped";
  from?: string;
  to?: string;
  applied?: "now" | "restart" | "";
  actor_kind?: string;
  device_name?: string;
  /** What became of a restart, why: a code (docs/protocol/protocol.md "Session mode changes"). */
  reason?: string;
  at: string;
};

export type ModeChangeRow = { kind: "mode"; id: string; change: ModeChange };
export type ChatRowWithModes = ChatDayRow | ModeChangeRow;

/** What ChatView keeps of a session's changes: whose they are, this module (to merge and draw them) and the changes. */
export type ModeChanges = { scope: string; kit: typeof import("./mode-changes"); changes: ModeChange[] };

const KINDS = new Set(["set", "restart_done", "restart_failed", "restart_dropped"]);

/** The changes of an answer, the well-formed ones (an older hub's 404 and a refused token give none). */
export function modeChangesOf(body: unknown): ModeChange[] {
  const list = (body as { changes?: unknown } | null)?.changes;
  if (!Array.isArray(list)) return [];
  return list.filter((c): c is ModeChange =>
    !!c && typeof c === "object" && KINDS.has((c as ModeChange).kind) && typeof (c as ModeChange).at === "string" && !Number.isNaN(Date.parse((c as ModeChange).at)));
}

function rowTime(row: ChatDayRow): number | undefined {
  return row.kind === "day" ? row.date.getTime() : chatLineTime(row.line);
}

/** The rows with each change before the first row after it, the newest after the last. A change from before the
 *  first loaded item while older history is still to load (`loaded`: the items there are, given only then) waits for
 *  that page instead of piling up at the top. Keys come from each change's time and kind, numbered within the same
 *  moment, so a list that grows or loses its oldest entry keeps the others' keys. */
export function withModeChanges(rows: readonly ChatDayRow[], changes: readonly ModeChange[], loaded?: Item[]): ChatRowWithModes[] {
  if (changes.length === 0) return rows as ChatRowWithModes[];
  const earliest = loaded ? loadedFrom(loaded) ?? Number.POSITIVE_INFINITY : undefined;
  const sorted = changes
    .map((change) => ({ change, at: Date.parse(change.at) }))
    .filter((c) => earliest === undefined || c.at >= earliest)
    .sort((a, b) => a.at - b.at);
  const seen = new Map<string, number>();
  const keyed = sorted.map(({ change, at }) => {
    const base = `mode-${at}-${change.kind}-${change.to ?? ""}`;
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return { at, row: { kind: "mode" as const, id: n ? `${base}-${n}` : base, change } };
  });
  const out: ChatRowWithModes[] = [];
  let next = 0;
  for (const row of rows) {
    const t = rowTime(row);
    if (t !== undefined) {
      while (next < keyed.length && keyed[next].at < t) out.push(keyed[next++].row);
    }
    out.push(row);
  }
  while (next < keyed.length) out.push(keyed[next++].row);
  return out;
}

/** The mode key each chat's changes were last read for (`watchModeChanges`), while the page lives. */
const readFor = new Map<string, string>();

/** Loads a session's changes into `set` (#10297), until `signal` ends. ChatView asks when an Agent's chat opens and when
 *  its mode or a waiting restart changes (`modeKey`), never on a timer, a refresh or the page coming back: one request.
 *  After a change of mode it looks once more 2 s later, since the manager records the change as the node answers, which
 *  can be a moment after the session's row says the new mode. No changes, or the same answer again, keeps what `set`
 *  holds; a failure (an older hub, a refused token, offline) changes nothing. `kit` is this module as ChatView loaded
 *  it, kept with the changes to merge and draw them. */
export function watchModeChanges(mid: string, sid: string, modeKey: string, signal: AbortSignal, kit: ModeChanges["kit"],
  set: (update: (prev?: ModeChanges) => ModeChanges | undefined) => void): void {
  const scope = `${mid}/${sid}`;
  const before = readFor.get(scope);
  readFor.set(scope, modeKey);
  const load = () => void api<unknown>(`${sessionPath(mid, sid)}/mode-changes`, { signal })
    .then((body) => {
      if (signal.aborted) return;
      const changes = modeChangesOf(body);
      // a chat with none (most) never draws again for them; the same answer again neither
      set((prev) => {
        if (prev?.scope === scope) return JSON.stringify(prev.changes) === JSON.stringify(changes) ? prev : { scope, kit, changes };
        return changes.length ? { scope, kit, changes } : prev;
      });
    })
    .catch(() => {});
  load();
  if (before === undefined || before === modeKey) return;
  const again = setTimeout(load, 2000);
  signal.addEventListener("abort", () => clearTimeout(again));
}
