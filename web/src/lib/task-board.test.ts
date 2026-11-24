import { describe, expect, it } from "vitest";
import { BOARD_ORDER, boardSectionOf, boardSections, type BoardSectionKey } from "./task-board";
import type { ReviewState } from "./supervision";
import type { Session } from "./types";
import { todoRank, type Todo, type TodoStatus } from "./todos";

const todo = (id: string, created_at: string): Todo => ({ id, title: id, created_at, updated_at: created_at });
const session = (sid: string): Session => ({ machine_id: "m", session_id: sid, title: sid, cwd: "/w", status: "idle", state: "idle" }) as Session;

const supervised = (id: string, state: ReviewState, extra: Partial<Todo> = {}): Todo => ({
  ...todo(id, "2026-11-06T00:00:00Z"),
  supervision: { supervisor: "main", state },
  ...extra,
});

describe("the task board (iOS TaskBoardSheet)", () => {
  // Every status, unsupervised and awaiting review, and the review states that are not Awaiting review (#4896).
  const cases: [TodoStatus, ReviewState | null, BoardSectionKey][] = [
    ["needs_you", null, "waiting"],
    ["interrupted", null, "waiting"],
    ["usage_limit", null, "waiting"],
    ["working", null, "working"],
    ["not_started", null, "working"],
    ["unread", null, "unread"],
    ["follow_up", null, "stopped"],
    ["done", null, "done"],
    ["failed", null, "failed"],
    ["follow_up", "pending_review", "review"],
    ["unread", "pending_review", "review"],
    ["not_started", "pending_review", "review"],
    ["needs_you", "pending_review", "waiting"],
    ["interrupted", "pending_review", "waiting"],
    ["working", "pending_review", "working"],
    ["failed", "pending_review", "failed"],
    ["done", "pending_review", "done"],
    ["follow_up", "", "stopped"],
    ["follow_up", "rework", "stopped"],
    ["follow_up", "needs_user", "waiting"],
    ["follow_up", "accepted", "stopped"],
    ["unread", "cancelled", "unread"],
  ];
  it.each(cases)("puts a %s task (review %s) under %s", (status, review, key) => {
    const extra = status === "done" ? { done_at: "2026-11-06T01:00:00Z" } : {};
    const td = review === null ? { ...todo("t", "2026-11-06T00:00:00Z"), ...extra } : supervised("t", review, extra);
    expect(boardSectionOf({ todo: td, status })).toBe(key);
  });

  it("keeps cancelled tasks out of Done for both API representations", () => {
    const doneAt = "2026-11-06T01:00:00Z";
    const additive = { ...todo("additive-cancelled", "2026-11-06T00:00:00Z"), done_at: doneAt, done_by: "cancelled", cancelled_at: doneAt };
    const legacy = supervised("legacy-cancelled", "cancelled", { done_at: doneAt, done_by: "review" });
    const ordinaryDone = { ...todo("ordinary-done", "2026-11-06T00:00:00Z"), done_at: doneAt };
    for (const td of [additive, legacy]) {
      expect(boardSectionOf({ todo: td, status: "done" })).toBe("cancelled");
      expect(todoRank(td, "done")).toBeGreaterThan(todoRank(ordinaryDone, "done"));
    }
    expect(boardSectionOf({ todo: ordinaryDone, status: "done" })).toBe("done");
    expect(boardSections([
      { todo: ordinaryDone, status: "done" },
      { todo: additive, status: "done" },
      { todo: legacy, status: "done" },
    ], []).map((section) => [section.key, section.rows.map((row) => row.todo.id)])).toEqual([
      ["done", ["ordinary-done"]],
      ["cancelled", ["additive-cancelled", "legacy-cancelled"]],
    ]);
  });

  it("keeps an unaccepted live delivery awaiting review", () => {
    const td = { ...todo("live", "2026-11-06T00:00:00Z"), delivery: { state: "live" as const, at: "2026-11-06T01:00:00Z" } };
    expect(boardSectionOf({ todo: td, status: "unread" })).toBe("review");
    expect(boardSectionOf({ todo: { ...td, done_at: "2026-11-06T02:00:00Z" }, status: "done" })).toBe("done");
  });

  it("puts accepted and owner-waiting live deliveries before review", () => {
    const delivery = { state: "live" as const, at: "2026-11-06T01:00:00Z" };
    const accepted = {
      ...supervised("accepted", "accepted", { delivery }),
      supervision: { supervisor: "main", state: "accepted" as const, reviews: [{ at: "2026-11-06T02:00:00Z", verdict: "accepted" as const }] },
    };
    expect(boardSectionOf({ todo: accepted, status: "follow_up" })).toBe("done");
    expect(todoRank(accepted, "follow_up")).toBe(8);
    const waiting = supervised("waiting", "needs_user", { delivery });
    expect(boardSectionOf({ todo: waiting, status: "follow_up" })).toBe("waiting");
    expect(todoRank(waiting, "follow_up")).toBe(0);
  });

  it("puts what the user must act on first: 等你, 待验收, 没开起来, then 在做, 做完了, 停着, 已完成 (#4896)", () => {
    expect(BOARD_ORDER).toEqual(["waiting", "review", "failed", "working", "parked", "unread", "stopped", "done", "cancelled"]);
    const rows = [
      { todo: todo("done", "2026-11-06T07:00:00Z"), status: "done" as const },
      { todo: todo("stopped", "2026-11-06T06:00:00Z"), status: "follow_up" as const },
      { todo: todo("unread", "2026-11-06T05:00:00Z"), status: "unread" as const },
      { todo: todo("working", "2026-11-06T04:00:00Z"), status: "working" as const },
      { todo: todo("failed", "2026-11-06T03:00:00Z"), status: "failed" as const },
      { todo: supervised("review", "pending_review"), status: "follow_up" as const },
      { todo: todo("waiting", "2026-11-06T01:00:00Z"), status: "needs_you" as const },
    ];
    expect(boardSections(rows, []).map((s) => [s.key, s.rows.map((r) => r.todo.id)])).toEqual([
      ["waiting", ["waiting"]],
      ["review", ["review"]],
      ["failed", ["failed"]],
      ["working", ["working"]],
      ["unread", ["unread"]],
      ["stopped", ["stopped"]],
      ["done", ["done"]],
    ]);
  });

  it("orders the sections, keeps only the ones with something, newest task first, agents by their state", () => {
    const rows = [
      { todo: todo("old", "2026-11-01T00:00:00Z"), status: "working" as const },
      { todo: todo("new", "2026-11-03T00:00:00Z"), status: "not_started" as const },
      { todo: todo("bad", "2026-11-02T00:00:00Z"), status: "failed" as const },
    ];
    const sections = boardSections(rows, [{ session: session("a"), state: "needs_you" }, { session: session("b"), state: "working" }]);
    expect(sections.map((s) => s.key)).toEqual(["waiting", "failed", "working"]);
    expect(sections[0].agents.map((a) => a.session.session_id)).toEqual(["a"]);
    expect(sections[2].rows.map((r) => r.todo.id)).toEqual(["new", "old"]);
    expect(sections[2].agents.map((a) => a.session.session_id)).toEqual(["b"]);
  });

  it("puts the newest task first when the times are written differently (#3638)", () => {
    const rows = [
      { todo: todo("loaded", "2026-11-06T18:00:00.123456+08:00"), status: "working" as const },
      { todo: todo("created", "2026-11-06T10:30:00.1Z"), status: "working" as const },
    ];
    expect(boardSections(rows, [])[0].rows.map((r) => r.todo.id)).toEqual(["created", "loaded"]);
  });

  it("has no sections when the conversation has nothing", () => {
    expect(boardSections([], [])).toEqual([]);
  });
});
