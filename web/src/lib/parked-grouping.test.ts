import { describe, expect, it } from "vitest";
import { assistantEntries } from "./assistant-entries";
import { boardSections } from "./task-board";
import { triage, triageTodos } from "./todo-triage";
import { makeSessionLookup, todoStatus, type Todo } from "./todos";
import type { Session } from "./types";

const now = Date.parse("2026-11-16T12:00:00Z");
const at = "2026-11-16T11:00:00Z";
const task = (id: string, patch: Partial<Todo> = {}): Todo => ({
  id, title: `Example ${id}`, origin: "main", created_at: at, updated_at: at, ...patch,
});
const hold = (until = "2026-11-17T12:00:00Z") => ({
  supervisor: "main", state: "parked" as const,
  parked: { reason: "Waiting for the supplier's reply", waiting_on: "someone_else" as const, until, count: 1 },
});
const url = "https://github.com/example/demo/pull/42";
const facts = { url, state: "open" as const, draft: false, checked_at: at, review: "required" as const, checks: "success" as const, conflicts: false };
// The forge snapshot must be fresh, unlike the task's creation time.
facts.checked_at = "2026-11-16T11:59:00Z";
const worker: Session = { machine_id: "demo", session_id: "worker", title: "Example worker", cwd: "/demo", state: "working", status: "working", updated_at: at };
const lookup = makeSessionLookup([worker]);
const active = [{ machine_id: "demo", session_id: "worker", linked_at: at }];

describe("Parked grouping", () => {
  it("puts an elapsed fallback in Parked until the hub changes the actual state", () => {
    const overdue = task("supplier", { supervision: hold("2026-11-15T12:00:00Z"), updated_at: "2026-11-10T12:00:00Z" });
    expect(triage(overdue, lookup, now)).toMatchObject({ section: "parked", rule: "parked" });
    const page = triageTodos([overdue], lookup, now);
    expect(page.todoCounts.parked).toBe(1);
    expect([page.open, page.shelved, page.finished]).toEqual([1, 0, 0]);
  });

  it("shares one wait group and open count across the tray, To-dos and All board, even with multiple PRs", () => {
    const list = [
      task("supplier", { supervision: hold() }),
      task("review", { pr_url: url, pr_status: facts, other_prs: [{ ...facts, url: "https://github.com/example/demo/pull/41" }] }),
      task("active", { agents: active }),
      task("decision", { supervision: { supervisor: "main", state: "needs_user" } }),
    ];
    expect(triage(list[1], lookup, now)).toMatchObject({ section: "parked", rule: "pr_open" });
    const page = triageTodos(list, lookup, now);
    expect(page.todoCounts.parked).toBe(2);
    const entries = assistantEntries({ conversation: "main", main: "main", todos: list, sessions: [worker], interactions: [], plans: {}, feed: [], now });
    expect(entries).toHaveProperty("parked");
    expect(entries.parked.map((item) => item.key).sort()).toEqual(["task:review", "task:supplier"]);
    expect(entries.running.map((item) => item.key)).toEqual(["task:active"]);
    expect(entries.needsYou.map((item) => item.key)).toEqual(["task:decision"]);
    const counted = [...entries.needsYou, ...entries.running, ...entries.parked, ...entries.review].filter((item) => item.kind === "task" && item.countsOpen);
    expect(counted).toHaveLength(page.open);
    expect(new Set(counted.map((item) => item.key)).size).toBe(page.open);
    const board = boardSections(list.map((todo) => ({ todo, status: todoStatus(todo, lookup), parked: triage(todo, lookup, now).section === "parked" })), []);
    expect(board.find((section) => section.key === "parked")?.rows.map(({ todo }) => todo.id).sort()).toEqual(["review", "supplier"]);
  });

  it.each([
    ["live work", { agents: active }],
    ["owner decision", { supervision: { supervisor: "main", state: "needs_user" as const } }],
    ["closed", { done_at: at }],
    ["accepted", { supervision: { supervisor: "main", state: "accepted" as const, reviews: [{ at, verdict: "accepted" as const }] } }],
    ["deployed", { delivery: { state: "live" as const, at } }],
    ["draft", { pr_status: { ...facts, draft: true } }],
    ["stale facts", { pr_status: { ...facts, checked_at: "2026-11-16T11:00:00Z" } }],
    ["wrong PR", { pr_status: { ...facts, url: "https://github.com/example/other/pull/42" } }],
    ["supervisor review", { supervision: { supervisor: "main", state: "pending_review" as const } }],
    ["a seen supervisor review", { seen_at: "2026-11-16T12:00:00Z", supervision: { supervisor: "main", state: "pending_review" as const } }],
  ] satisfies [string, Partial<Todo>][])("does not let a ready PR hide %s", (_label, patch) => {
    const todo = task("review", { pr_url: url, pr_status: facts, ...patch });
    expect(triage(todo, lookup, now).section).not.toBe("parked");
    const entries = assistantEntries({ conversation: "main", main: "main", todos: [todo], sessions: [worker], interactions: [], plans: {}, feed: [], now });
    expect(entries).toHaveProperty("parked");
    expect(entries.parked).toEqual([]);
  });

  it("keeps a running Agent ahead of a seen supervisor review's PR classification", () => {
    const todo = task("review", { agents: active, pr_url: url, pr_status: facts, seen_at: "2026-11-16T12:00:00Z",
      supervision: { supervisor: "main", state: "pending_review" } });
    expect(triage(todo, lookup, now)).toMatchObject({ section: "working", rule: "agent_running" });
  });

  it("lets real work and owner decisions outrank an explicit hold", () => {
    const live = task("active", { supervision: hold(), agents: active });
    const decided = task("decision", { supervision: hold(), ask: { outcome: "decide", asked_at: at, verdict: "Choose a supplier", recommend: "Use the available supplier" } });
    expect(triage(live, lookup, now).section).toBe("working");
    expect(triage(decided, lookup, now).section).toBe("waiting");
    const entries = assistantEntries({ conversation: "main", main: "main", todos: [live, decided], sessions: [worker], interactions: [], plans: {}, feed: [], now });
    expect(entries).toHaveProperty("parked");
    expect(entries.parked).toEqual([]);
    expect(entries.running.map((item) => item.key)).toEqual(["task:active"]);
    expect(entries.needsYou.map((item) => item.key)).toEqual(["task:decision"]);
  });

  it("keeps a seen supervisor review open without calling it an external wait", () => {
    const todo = task("review", { pr_url: url, pr_status: facts, seen_at: "2026-11-16T12:00:00Z",
      supervision: { supervisor: "main", state: "pending_review" } });
    expect(triage(todo, lookup, now)).toMatchObject({ section: "just_done", rule: "finished_unseen" });
    expect(triageTodos([todo], lookup, now).open).toBe(1);
    const entries = assistantEntries({ conversation: "main", main: "main", todos: [todo], sessions: [worker], interactions: [], plans: {}, feed: [], now });
    expect(entries.parked).toEqual([]);
    expect(entries.review).toMatchObject([{ key: "task:review", countsOpen: true }]);
  });
});
