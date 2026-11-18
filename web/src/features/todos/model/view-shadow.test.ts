// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DEV_MODE_KEY } from "../../../lib/dev-probe";
import { resetRestoredCache, restoreTodo } from "../../../lib/todo-restore";
import * as todoTriage from "../../../lib/todo-triage";
import type { TodoSummary } from "../../../lib/todo-view-wire";
import type { Todo } from "../../../lib/todos";
import type { Session } from "../../../lib/types";
import { localRestores, QUIET_MS, RECHECK_MS, SETTLE_MAX_MS, SUMMARY_MS, startViewShadow } from "./view-shadow";

const START = Date.parse("2026-11-10T12:00:00Z");
const ago = (hours: number) => new Date(START - hours * 3_600_000).toISOString();
const view = (section: string, reason: string, more: Partial<TodoSummary> = {}): TodoSummary => ({ phase: "todo", section, reason, rank: 0, ...more });
const todo = (id: string, more: Partial<Todo> = {}): Todo => ({ id, title: "Call the dentist about Tuesday", created_at: ago(2), updated_at: ago(1), ...more });

// today's triage files these at rest (Other open, or Shelved for a task idle for 60 hours); their views say:
const same = todo("todo_same", { view: view("todo", "rest") });
const decided = todo("todo_someday", { origin: "conv_main", created_at: ago(60), updated_at: ago(60), view: view("someday", "deferred", { phase: "someday" }) });
const unexplained = todo("todo_odd", { view: view("working", "agent_running", { phase: "working" }) });

type State = { todos: Todo[]; features: string[]; sessions: Record<string, Session[]>; sessionSummaries: never[]; taskRows: Record<string, never> };

let state: State;
let listeners: Set<() => void>;
let note: ReturnType<typeof vi.fn<(kind: string, fields: Record<string, unknown>) => void>>;
let refresh: ReturnType<typeof vi.fn<() => Promise<void>>>;
let stop: (() => void) | undefined;

const set = (patch: Partial<State>) => {
  state = { ...state, ...patch };
  for (const l of listeners) l();
};
const start = (triage: () => Promise<typeof todoTriage> = async () => todoTriage) => {
  stop = startViewShadow({
    note: (kind, fields) => note(kind, fields),
    state: () => state,
    subscribe: (l) => (listeners.add(l), () => listeners.delete(l)),
    restores: () => new Map(),
    idle: () => Promise.resolve(),
    triage,
    refresh: () => refresh(),
  });
};
const lines = (kind: string) => note.mock.calls.filter(([k]) => k === kind).map(([, fields]) => fields as Record<string, unknown>);

beforeEach(() => {
  vi.useFakeTimers({ now: START });
  localStorage.setItem(DEV_MODE_KEY, ago(1));
  state = { todos: [], features: ["todo_view"], sessions: {}, sessionSummaries: [], taskRows: {} };
  listeners = new Set();
  note = vi.fn();
  refresh = vi.fn(async () => {});
});
afterEach(() => {
  stop?.();
  stop = undefined;
  localStorage.clear();
  resetRestoredCache();
  vi.useRealTimers();
});

describe("the gates", () => {
  it("writes nothing while the Developing Persona switch is off", async () => {
    localStorage.removeItem(DEV_MODE_KEY);
    state.todos = [unexplained];
    start();
    await vi.advanceTimersByTimeAsync(3 * SUMMARY_MS);
    expect(note).not.toHaveBeenCalled();
  });

  it("writes nothing while the hub does not list todo_view, and starts once it does", async () => {
    state = { ...state, features: ["todo_seen"], todos: [same] };
    start();
    await vi.advanceTimersByTimeAsync(3 * SUMMARY_MS);
    expect(note).not.toHaveBeenCalled();
    set({ features: ["todo_seen", "todo_view"] });
    await vi.advanceTimersByTimeAsync(QUIET_MS);
    expect(lines("todo_view")).toHaveLength(1);
  });

  it("stops with the recorder", async () => {
    state.todos = [same];
    start();
    stop?.();
    await vi.advanceTimersByTimeAsync(3 * SUMMARY_MS);
    expect(note).not.toHaveBeenCalled();
  });
});

describe("comparing", () => {
  it("counts each row with a view, and notes a mismatch once a later run still sees it, by ids and enum words only", async () => {
    state.todos = [same, decided, unexplained, todo("todo_plain")];
    start();
    await vi.advanceTimersByTimeAsync(QUIET_MS);
    expect(lines("todo_view")).toEqual([
      { compared: 3, same: 1, by_design: { someday: 1 }, unknown: 0, client_gap: 0, boundary: 0, stale_view: 0, unsettled: 1, mismatch: 0, errors: 0 },
    ]);
    expect(lines("todo_view_mismatch")).toEqual([]);
    // the recheck compares a list read again
    expect(refresh).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(RECHECK_MS);
    const mismatch = { todo: "todo_odd", manager: "", local_section: "other", local_rule: "rest", phase: "working", section: "working", reason: "agent_running" };
    expect(lines("todo_view_mismatch")).toEqual([mismatch]);
    expect(JSON.stringify(note.mock.calls)).not.toContain("dentist");

    // the summary waits out its minute; the same mismatch is not noted again within the hour
    await vi.advanceTimersByTimeAsync(SUMMARY_MS);
    expect(lines("todo_view").at(-1)).toMatchObject({ unsettled: 0, mismatch: 1 });
    expect(lines("todo_view")).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(5 * SUMMARY_MS);
    expect(lines("todo_view_mismatch")).toHaveLength(1);
    expect(lines("todo_view")).toHaveLength(2);
  });

  it("never notes a mismatch that was gone by the next run", async () => {
    state.todos = [unexplained];
    start();
    await vi.advanceTimersByTimeAsync(QUIET_MS);
    expect(lines("todo_view").at(-1)).toMatchObject({ unsettled: 1 });
    // the hub's next list says Other open too
    set({ todos: [{ ...unexplained, view: view("todo", "rest") }] });
    await vi.advanceTimersByTimeAsync(3 * SUMMARY_MS);
    expect(lines("todo_view_mismatch")).toEqual([]);
  });

  it("names the manager whose view it is", async () => {
    state.todos = [{ ...unexplained, manager: "m-abcdef" }];
    start();
    await vi.advanceTimersByTimeAsync(QUIET_MS + RECHECK_MS);
    expect(lines("todo_view_mismatch")).toMatchObject([{ todo: "todo_odd", manager: "m-abcdef" }]);
  });

  it("counts apart a row whose runs moved since the list came, and reads the list again", async () => {
    const session = (state: string, since: string): Session => ({ machine_id: "m1", session_id: "s1", state, status: state === "working" ? "working" : "idle", state_since: since }) as Session;
    const agents = [{ machine_id: "m1", session_id: "s1", linked_at: ago(2) }];
    const run = todo("todo_run", { origin: "conv_main", agents, view: view("working", "agent_running", { phase: "working" }) });
    state = { ...state, todos: [run], sessions: { m1: [session("working", ago(1))] } };
    start();
    await vi.advanceTimersByTimeAsync(QUIET_MS);
    expect(lines("todo_view").at(-1)).toMatchObject({ same: 1, stale_view: 0 });

    // the run finished after the list was read: the web reads Just finished, the list's view still says Working
    set({ sessions: { m1: [session("unread", new Date(START + QUIET_MS).toISOString())] } });
    await vi.advanceTimersByTimeAsync(QUIET_MS + SUMMARY_MS);
    expect(lines("todo_view")).toHaveLength(2);
    expect(lines("todo_view").at(-1)).toMatchObject({ same: 0, stale_view: 1, unsettled: 0, mismatch: 0 });
    expect(lines("todo_view_mismatch")).toEqual([]);
    // once per run that finds it, at most every RECHECK_MS
    expect(refresh).toHaveBeenCalledTimes(2);

    // the list read again says Just finished as well
    set({ todos: [{ ...run, view: view("just_finished", "finished_unseen", { phase: "ready" }) }] });
    await vi.advanceTimersByTimeAsync(QUIET_MS + SUMMARY_MS);
    expect(lines("todo_view")).toHaveLength(3);
    expect(lines("todo_view").at(-1)).toMatchObject({ same: 1, stale_view: 0, mismatch: 0 });
    expect(lines("todo_view_mismatch")).toEqual([]);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("compares once changes pause, or at the latest SETTLE_MAX_MS after the first", async () => {
    state.todos = [same];
    start();
    // sessions change every two seconds, never three seconds of quiet
    for (let t = 0; t + 2_000 < SETTLE_MAX_MS; t += 2_000) {
      await vi.advanceTimersByTimeAsync(2_000);
      set({ sessions: { m1: [] } });
    }
    expect(lines("todo_view")).toEqual([]);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(lines("todo_view")).toHaveLength(1);
  });

  it("counts its own failures, with no text", async () => {
    state.todos = [same];
    start(async () => {
      throw new Error("the chunk did not load");
    });
    await vi.advanceTimersByTimeAsync(QUIET_MS);
    expect(lines("todo_view")).toEqual([{ errors: 1 }]);
    await vi.advanceTimersByTimeAsync(SUMMARY_MS);
    expect(lines("todo_view")).toEqual([{ errors: 1 }, { errors: 2 }]);
  });

  it("reads this browser's restores as lib/todo-restore.ts writes them", () => {
    restoreTodo("todo_r1", START - 1000);
    expect(localRestores()).toEqual(new Map([["todo_r1", START - 1000]]));
  });
});

describe("its chunk", () => {
  // every import of lib/todo-triage.ts in a source, static or dynamic, and whether it takes types only
  const triageImports = (source: string) =>
    [...source.matchAll(/import\s*(type\b)?[^;]*?["']([^"']*lib\/todo-triage)["']|import\(\s*["']([^"']*lib\/todo-triage)["']/g)].map((m) => ({ text: m[0], types: m[1] === "type" }));

  it("imports today's triage for its types only: the values come through App (a value import splits the first screen)", () => {
    expect(triageImports('import { triage } from "../../../lib/todo-triage";\nimport("../../../lib/todo-triage")').map((i) => i.types)).toEqual([false, false]);
    for (const file of ["./view-shadow.ts", "./view-legacy.ts", "./view-gates.ts"]) {
      const source = readFileSync(new URL(file, import.meta.url), "utf8");
      for (const i of triageImports(source)) expect(i.types, `${file}: ${i.text}`).toBe(true);
    }
  });
});
