// The sidebar's To-dos badge and the To-dos page's marks (#11085, #11838): one set of to-dos, each with why it counts
// (todo-triage ownerNeeds), worked out from the same inputs for both, so the number and the marked rows cannot disagree:
// the current assistant's to-dos, their Agents read as the page's sections read them (useTodoLookup), the shelved to-dos
// brought back in this browser, and one minute clock that moves to-dos across the 24 h and 48 h lines for both at once.

import { useMemo, useSyncExternalStore } from "react";
import { answeredLookup } from "../views/assistant/answered-lookup";
import { useScopedTodos } from "./current-assistant";
import { useAgentSessions } from "./sessions";
import { useStore, withTaskRows } from "./store";
import { ownerNeeds, type OwnerNeed } from "./todo-triage";
import { useRestoredTodos } from "./todo-restore";
import { makeSessionLookup, type SessionLookup } from "./todos";

const MINUTE_MS = 60_000;
let minute = Date.now();
let timer: ReturnType<typeof setInterval> | undefined;
const listeners = new Set<() => void>();

function onMinute(listener: () => void): () => void {
  listeners.add(listener);
  if (!timer) {
    // the first reader since none: the time it reads is now, not when the last one left
    minute = Date.now();
    timer = setInterval(() => {
      minute = Date.now();
      for (const l of listeners) l();
    }, MINUTE_MS);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size || !timer) return;
    clearInterval(timer);
    timer = undefined;
  };
}

/** The minute the To-dos page places its to-dos at and the badge counts them at: one clock for both, ticking once a
 *  minute while either is drawn. */
export function useTodoMinute(): number {
  return useSyncExternalStore(onMinute, () => minute, () => minute);
}

/** The to-dos' Agents as the To-dos page reads them: the Agents list's rows, an open task's Agent outside the list's
 *  window (`taskRows`), and an Agent whose card was just answered here counted as back at work until the hub catches
 *  up (views/assistant/answered-lookup.ts). */
export function useTodoLookup(): SessionLookup {
  const sessions = useAgentSessions();
  const taskRows = useStore((s) => s.taskRows);
  const interactions = useStore((s) => s.interactions);
  const answered = useStore((s) => s.answered);
  return useMemo(() => answeredLookup(withTaskRows(makeSessionLookup(sessions), taskRows), interactions, answered), [sessions, taskRows, interactions, answered]);
}

/** The to-dos the sidebar's badge counts, each with why (ownerNeeds): its size is the badge, and the To-dos page marks
 *  exactly these rows. The current assistant's to-dos only, as the badge has always counted. */
export function useOwnerNeeds(): ReadonlyMap<string, OwnerNeed> {
  const todos = useScopedTodos();
  const lookup = useTodoLookup();
  const [restored] = useRestoredTodos();
  // placed again on each minute's tick as on each change, at the moment it is placed (as the page's sections are)
  const minute = useTodoMinute();
  return useMemo(() => ownerNeeds(todos, lookup, Date.now(), restored), [todos, lookup, restored, minute]);
}
