import { describe, expect, it } from "vitest";
import { parseAPIError, retryable } from "./api-errors";
import { assistantEntries } from "./assistant-entries";
import { interruption, makeSessionLookup, todoStatus, type Todo } from "./todos";
import type { Session } from "./types";

const sealed = "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account and can't go to it now";
const at = "2026-11-18T09:00:00Z";
const old: Session = { machine_id: "box", session_id: "old", title: "Previous attempt", cwd: "/code/app",
  state: "unread", status: "done", turn_error: sealed, turn_error_kind: "transient", updated_at: at } as Session;
const todo: Todo = { id: "task", title: "Fix the build", origin: "main", created_at: at, updated_at: at,
  agents: [{ machine_id: "box", session_id: "old", linked_at: at }] } as Todo;
const entries = (task: Todo, sessions: Session[] = [old]) => assistantEntries({
  conversation: "main", main: "main", todos: [task], sessions, interactions: [], plans: {}, feed: [],
  now: Date.parse(at),
});

describe("unrecoverable conversations", () => {
  it.each([sealed, `API Error: 503 upstream: ${sealed}`, sealed.toUpperCase()])("never offers Continue for %s", (text) => {
    expect(interruption({ ...old, turn_error: text })?.canContinue).toBe(false);
    expect(interruption({ ...old, turn_error: text })?.invalid).toBe(true);
    const error = parseAPIError(text);
    expect(error).not.toBeNull();
    expect(retryable(error!)).toBe(false);
  });

  it("keeps ordinary transient errors retryable", () => {
    expect(interruption({ ...old, turn_error: "API Error: 503 encrypted connection failed" })?.canContinue).toBe(true);
  });

  it.each(["no_output", "stopped"])("keeps sealed errors invalid even with the %s kind", (kind) => {
    expect(interruption({ ...old, turn_error_kind: kind })).toMatchObject({ invalid: true, canContinue: false });
  });

  it("keeps the invalid conversation's unfinished task visible when nothing replaces it", () => {
    expect(entries(todo).needsYou.map((item) => item.key)).toEqual(["task:task"]);
  });

  it("does not revive an interruption when the task has another active conversation", () => {
    const replacement = { ...old, session_id: "new", state: "working", status: "working", turn_error: undefined } as Session;
    const task = { ...todo, agents: [...todo.agents!, { machine_id: "box", session_id: "new", linked_at: at }] };
    const got = entries(task, [old, replacement]);
    expect(got.needsYou).toEqual([]);
    expect(got.running.map((item) => item.key)).toEqual(["task:task"]);
  });

  it("does not revive an old interruption for a transient replacement with its own recoverable error", () => {
    const staleInvalid = { ...old, state: "idle", status: "idle",
      work: { phase: "blocked", on: "transient", wait: "Stale retry" } } as Session;
    expect(todoStatus(todo, makeSessionLookup([staleInvalid]))).toBe("interrupted");

    const replacement = { ...old, session_id: "new", state: "idle", status: "idle",
      turn_error: "API Error: 503 Service Unavailable", turn_error_kind: "transient",
      work: { phase: "blocked", on: "transient", wait: "Retrying after a transient failure" } } as Session;
    const task = { ...todo, agents: [...todo.agents!, { machine_id: "box", session_id: "new", linked_at: at }] };
    const sessions = [old, replacement];
    expect(todoStatus(task, makeSessionLookup(sessions))).toBe("waiting");
    const got = entries(task, sessions);
    expect(got.needsYou).toEqual([]);
    expect(got.running.map((item) => item.key)).toEqual(["task:task"]);
  });

  it("does not revive an interruption after its task is parked, even with new transcript activity", () => {
    const task: Todo = { ...todo, supervision: { supervisor: "main", state: "parked", updated_at: at, reviews: [],
      parked: { reason: "Waiting for review", waiting_on: "someone_else", count: 1, until: "2026-11-19T09:00:00Z" } } };
    expect(entries(task, [{ ...old, updated_at: "2026-11-18T09:05:00Z" }]).needsYou).toEqual([]);
  });

  it("does not revive an interruption after its task closes", () => {
    expect(entries({ ...todo, done_at: at }).needsYou).toEqual([]);
  });
});
