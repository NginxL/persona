import { describe, expect, it } from "vitest";
import { homeTasks } from "./home-tasks";
import { makeSessionLookup, sortTodos, type Todo } from "./todos";
import type { Session } from "./types";

const todo = (id: string, o: Partial<Todo> = {}): Todo => ({ id, title: id, created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z", ...o });
const on = (sid: string) => ({ machine_id: "mac", session_id: sid, linked_at: "x" });
const agent = (sid: string, state: Session["state"]): Session => ({ machine_id: "mac", session_id: sid, title: sid, cwd: "/c", status: "idle", state }) as Session;

describe("homeTasks", () => {
  it("lists open tasks only, manual ones too, and counts the agents not released", () => {
    const { rows, total } = homeTasks(
      [todo("done", { done_at: "2026-11-02T00:00:00Z" }), todo("manual"), todo("run", { origin: "c1", agents: [on("s1"), { ...on("s2"), released_at: "y" }] })],
      makeSessionLookup([agent("s1", "working")]),
    );
    expect(total).toBe(2);
    expect(rows.find((r) => r.todo.id === "run")).toMatchObject({ status: "working", agents: 1 });
    expect(rows.some((r) => r.todo.id === "done")).toBe(false);
  });

  it("orders as the Tasks page does and caps the list", () => {
    const todos = [
      todo("old", { created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-09T00:00:00Z" }), // changed last, made first
      todo("new", { created_at: "2026-11-03T00:00:00Z" }),
      todo("ask", { created_at: "2026-10-01T00:00:00Z", agents: [on("s1")] }),
    ];
    const lookup = makeSessionLookup([agent("s1", "needs_you")]);
    const { rows, total } = homeTasks(todos, lookup, 2);
    expect(total).toBe(3);
    expect(rows.map((r) => r.todo.id)).toEqual(["ask", "new"]);
    expect(homeTasks(todos, lookup).rows.map((r) => r.todo.id)).toEqual(sortTodos(todos, lookup).map((x) => x.id));
  });

  it("names the agent working, else the one that moved last, of the rows read (#10904)", () => {
    const at = (s: Session, updated_at: string, state_since?: string) => ({ ...s, updated_at, state_since });
    const lookup = makeSessionLookup([
      at(agent("idle", "idle"), "2026-11-01T09:00:00Z"),
      at(agent("work", "working"), "2026-11-01T08:00:00Z"),
      at(agent("late", "unread"), "2026-11-01T08:00:00Z", "2026-11-01T10:00:00Z"),
      at(agent("gone", "working"), "2026-11-01T11:00:00Z"),
      { ...at(agent("deleted", "working"), "2026-11-01T11:00:00Z"), deleted: true },
    ]);
    const lead = (...sids: string[]) => homeTasks([todo("t", { agents: sids.map(on) })], lookup).rows[0]!.lead?.session_id;
    expect(lead("idle", "work", "late")).toBe("work");
    expect(lead("idle", "late")).toBe("late");
    expect(lead("idle", "missing")).toBe("idle");
    expect(lead("missing", "deleted")).toBeUndefined();
    // a released agent is not the task's any more
    expect(homeTasks([todo("t", { agents: [on("idle"), { ...on("gone"), released_at: "y" }] })], lookup).rows[0]).toMatchObject({ agents: 1, lead: { session_id: "idle" } });
  });

  it("reads an agent on an offline computer as the Tasks page does", () => {
    const { rows } = homeTasks([todo("t", { agents: [on("gone")] })], makeSessionLookup([]));
    expect(rows[0]).toMatchObject({ status: "follow_up", agents: 1 });
  });
});
