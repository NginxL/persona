// Developing Persona's shadow of the hub's to-do view (#10226; docs/web/web.md "Developing Persona"): its own chunk,
// started by the recorder (lib/dev-mode.ts startDevMode) and stopped with it. While the hub lists `todo_view` it reads
// every row the list carries with a view through today's triage (lib/todo-triage.ts, the tray's session lookup) and
// compares the two (view-legacy.ts classify). It draws nothing: a summary line at most once a minute, and one line per
// mismatch nothing explains, go to the recorder with ids and enum words only, never a title or any text.
//
// The triage comes through App's module (App.tsx re-exports it), never a value import of lib/todo-triage.ts: the
// recorder can load before App, so such an import would move the triage out of the first screen's chunk into its own
// (view-shadow.test.ts checks this file, view-legacy.ts and view-gates.ts import it for types only).

import { devWord } from "../../../lib/dev-scrub";
import { getState, refreshTodos, subscribe, taskSessionLookup } from "../../../lib/store";
import type * as TodoTriage from "../../../lib/todo-triage";
import type { SessionLookup, Todo } from "../../../lib/todos";
import type { Session } from "../../../lib/types";
import { classify, restoredAt, rowFacts } from "./view-legacy";
import { viewShadowOn } from "./view-gates";

/** A change of the list, the sessions or the hub's features is compared once this long passes without another... */
export const QUIET_MS = 3_000;
/** ...or this long after the first of them, however many follow. */
export const SETTLE_MAX_MS = 30_000;
/** A mismatch counts once a run this much later still sees it; the shadow reads the list again meanwhile (at most this
 *  often), so the second run compares a view the hub worked out after the first. */
export const RECHECK_MS = 10_000;
/** A run at least this often while nothing changes (time alone moves rows), and a summary line at most this often. */
export const SUMMARY_MS = 60_000;
/** The same mismatch is noted again after this long. */
export const REPEAT_MS = 3_600_000;
/** Rows compared between two idle moments. */
const SLICE = 50;

/** Where lib/todo-restore.ts keeps this browser's restores; view-shadow.test.ts checks the two read alike. */
const RESTORED_KEY = "persona.todos.restored";

/** This browser's own restores (to-do id → when), read as lib/todo-restore.ts writes them. */
export function localRestores(): ReadonlyMap<string, number> {
  const out = new Map<string, number>();
  try {
    const raw = JSON.parse(localStorage.getItem(RESTORED_KEY) ?? "{}") as Record<string, unknown>;
    for (const [id, at] of Object.entries(raw)) if (typeof at === "number" && Number.isFinite(at)) out.set(id, at);
  } catch {
    // none
  }
  return out;
}

type ShadowState = Pick<ReturnType<typeof getState>, "todos" | "features" | "sessions" | "sessionSummaries" | "taskRows">;

export interface ShadowDeps {
  /** Writes one recorder line (lib/dev-mode.ts DevRecorder.note). */
  note: (kind: string, fields: Record<string, unknown>) => void;
  state?: () => ShadowState;
  subscribe?: (listener: () => void) => () => void;
  now?: () => number;
  restores?: () => ReadonlyMap<string, number>;
  /** Resolves when the page is idle: the rows are compared in slices between. */
  idle?: () => Promise<void>;
  /** Today's triage (lib/todo-triage.ts), as App's module re-exports it. */
  triage?: () => Promise<TodayTriage>;
  /** Reads the list again (lib/store.ts refreshTodos). */
  refresh?: () => Promise<void>;
}

type TodayTriage = Pick<typeof TodoTriage, "triage" | "supersededFailedStarts" | "openMergePRs">;

function whenIdle(): Promise<void> {
  return new Promise((done) => (typeof requestIdleCallback === "function" ? requestIdleCallback(() => done(), { timeout: 1000 }) : setTimeout(done, 0)));
}

/** What of a session places its to-do: a list worked out before any of it changed may place the to-do elsewhere. */
function placing(s: Session | undefined): string {
  if (!s) return "";
  return JSON.stringify([s.state, s.status, s.state_since, s.work, s.turn_error_kind, Boolean(s.turn_error), s.awaits_reply, s.background_working, s.starting, s.pending_interactions]);
}

/** Starts comparing; returns the stop. */
export function startViewShadow(deps: ShadowDeps): () => void {
  const state = deps.state ?? getState;
  const now = deps.now ?? Date.now;
  const restores = deps.restores ?? localRestores;
  const idle = deps.idle ?? whenIdle;
  const today = deps.triage ?? (() => import("../../../App"));
  const refresh = deps.refresh ?? refreshTodos;
  let stopped = false;
  let running = false;
  let again = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let changedAt: number | undefined; // the first change no run has compared yet
  // mismatches by key: when first seen (not yet confirmed), and when last noted
  const firstSeen = new Map<string, number>();
  const noted = new Map<string, number>();
  let summaryAt = -Infinity;
  let summaryText = "";
  let errors = 0;
  let refreshedAt = -Infinity;
  let seenInputs: unknown[] = [];
  // the sessions as they were when the current list came: the hub worked its views out from those, and the manager does
  // not yet push when a run changes a view (#10226 D8), so a row whose runs moved since waits for the list read again
  let listLookup: SessionLookup = () => undefined;

  const runAt = (at: number) => {
    if (stopped) return;
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(fire, Math.max(0, at - now()));
  };
  const fire = () => {
    timer = undefined;
    if (running) {
      again = true;
      return;
    }
    running = true;
    changedAt = undefined;
    void run()
      .catch(() => {
        // counted, never described: a shadow that fails must not read as one with nothing to report
        errors++;
        summarize({ errors }, now());
      })
      .finally(() => {
        running = false;
        if (stopped || timer !== undefined) return; // a change meanwhile set the next run
        const unsettled = [...firstSeen.values()].some((at) => now() - at < RECHECK_MS);
        runAt(now() + (again ? QUIET_MS : unsettled ? RECHECK_MS : SUMMARY_MS));
        again = false;
      });
  };

  function summarize(tally: Record<string, unknown>, at: number) {
    const text = JSON.stringify(tally);
    if (text === summaryText || at - summaryAt < SUMMARY_MS) return;
    summaryText = text;
    summaryAt = at;
    deps.note("todo_view", tally);
  }

  async function run() {
    const s = state();
    if (!viewShadowOn(s.features)) {
      firstSeen.clear();
      return;
    }
    const { triage, supersededFailedStarts, openMergePRs } = await today();
    if (stopped) return;
    const at = now();
    const rows = s.todos.filter((td) => td.view);
    const lookup = taskSessionLookup(s);
    const before = listLookup;
    const superseded = supersededFailedStarts(s.todos);
    const local = restores();
    const tally = {
      compared: 0,
      same: 0,
      by_design: {} as Record<string, number>,
      unknown: 0,
      client_gap: 0,
      boundary: 0,
      stale_view: 0,
      unsettled: 0,
      mismatch: 0,
      errors,
    };
    const found = new Map<string, Record<string, string>>();
    const moved = (td: Todo) =>
      (td.agents ?? []).some((a) => !a.released_at && !a.start_error && placing(before(a.machine_id, a.session_id)) !== placing(lookup(a.machine_id, a.session_id)));
    const compare = (td: Todo) => {
      const view = td.view!;
      const mine = local.get(td.id) ?? 0;
      const t = triage(td, lookup, at, restoredAt(td, mine, at), superseded.has(td.id));
      const v = classify(t, view, rowFacts(td, lookup, at, { restored: mine, prOpen: openMergePRs(td, at).length > 0 }), at);
      tally.compared++;
      if (v.kind === "by_design") tally.by_design[v.why] = (tally.by_design[v.why] ?? 0) + 1;
      else if (v.kind !== "mismatch") tally[v.kind]++;
      else if (moved(td)) tally.stale_view++;
      else {
        // the hub's words are open and the ids are the hub's: each recorded as a code word (anything else reads "other")
        const line = {
          todo: devWord(td.id),
          manager: td.manager ? devWord(td.manager) : "",
          local_section: t.section,
          local_rule: t.rule,
          phase: devWord(view.phase),
          section: devWord(view.section),
          reason: devWord(view.reason),
        };
        found.set(Object.values(line).join("|"), line);
      }
    };
    for (let i = 0; i < rows.length; i += SLICE) {
      if (i) await idle();
      if (stopped) return;
      for (const td of rows.slice(i, i + SLICE)) compare(td);
    }
    tally.by_design = Object.fromEntries(Object.entries(tally.by_design).sort(([a], [b]) => a.localeCompare(b)));
    for (const [key, when] of noted) if (at - when >= REPEAT_MS) noted.delete(key);
    // a mismatch counts once a run RECHECK_MS later still sees it; one that went away starts over
    for (const key of firstSeen.keys()) if (!found.has(key)) firstSeen.delete(key);
    for (const [key, line] of found) {
      const first = firstSeen.get(key) ?? at;
      firstSeen.set(key, first);
      if (at - first < RECHECK_MS) {
        tally.unsettled++;
        continue;
      }
      tally.mismatch++;
      if (at - (noted.get(key) ?? -Infinity) < REPEAT_MS) continue;
      noted.set(key, at);
      deps.note("todo_view_mismatch", line);
    }
    // what a fresh list may settle: read it again, so the recheck compares the hub's view of now
    if ((tally.stale_view || tally.unsettled) && at - refreshedAt >= RECHECK_MS) {
      refreshedAt = at;
      void refresh().catch(() => {});
    }
    summarize(tally, at);
  }

  // what a run reads: a new one of these is a change to compare
  const inputs = (s: ShadowState) => [s.todos, s.features, s.sessions, s.sessionSummaries, s.taskRows];
  const unsubscribe = (deps.subscribe ?? subscribe)(() => {
    const s = state();
    const next = inputs(s);
    if (next.every((v, i) => v === seenInputs[i])) return;
    if (next[0] !== seenInputs[0]) listLookup = taskSessionLookup(s);
    seenInputs = next;
    const t = now();
    changedAt ??= t;
    runAt(Math.min(t + QUIET_MS, changedAt + SETTLE_MAX_MS));
  });
  const s0 = state();
  seenInputs = inputs(s0);
  listLookup = taskSessionLookup(s0);
  changedAt = now();
  runAt(changedAt + QUIET_MS);
  return () => {
    stopped = true;
    unsubscribe();
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
}
