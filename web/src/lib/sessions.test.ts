import { describe, expect, it } from "vitest";
import { othersWaiting, stateCounts } from "./sessions";
import type { AgentState, Interaction, Session } from "./types";

const session = (id: string, state: AgentState, patch: Partial<Session> = {}): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/tmp/p", status: state, state, updated_at: "2026-01-01T10:00:00Z", ...patch,
});

describe("侧边栏的需要你角标（#1731）", () => {
  it("安静任务的 Agent 照样按状态计数，但不进角标", () => {
    const all = [session("a", "needs_you"), session("q", "needs_you", { quiet: true }), session("w", "working", { quiet: true }), session("u", "unread")];
    expect(stateCounts(all)).toEqual({ needs: 2, working: 1, waiting: 0, unread: 1, badge: 1 });
  });
});

describe("stateCounts: waiting in the background (#10938)", () => {
  it("counts an agent that only waits on background commands apart from the working ones", () => {
    const all = [session("w", "working"), session("b", "working", { background_working: true, background_waiting: true }), session("c", "working", { background_working: true })];
    expect(stateCounts(all)).toMatchObject({ working: 2, waiting: 1 });
    // the field only says how a working agent works: an idle one is not waiting
    expect(stateCounts([session("i", "idle", { background_waiting: true })])).toMatchObject({ working: 0, waiting: 0 });
  });
});

describe("othersWaiting", () => {
  it("leaves out the open agent only when the badge counted it", () => {
    expect(othersWaiting(3, session("a", "needs_you"))).toBe(2);
    expect(othersWaiting(1, session("a", "needs_you"))).toBe(0);
    expect(othersWaiting(3, session("a", "working"))).toBe(3);
    expect(othersWaiting(3, session("q", "needs_you", { quiet: true }))).toBe(3);
    expect(othersWaiting(1, session("q", "needs_you", { quiet: true }), true)).toBe(0);
    expect(othersWaiting(3, undefined)).toBe(3);
  });
});

it("excludes directory-hidden sessions from every attention count", () => {
  const hidden = { hidden_by_directory: true };
  expect(stateCounts([session("n", "needs_you", hidden), session("u", "unread", hidden), session("w", "working", hidden), session("v", "needs_you")]))
    .toEqual({ needs: 1, working: 0, waiting: 0, unread: 0, badge: 1 });
  expect(othersWaiting(1, session("n", "needs_you", hidden))).toBe(1);
});

it("excludes P1-only needs_you sessions from badge when push grading is on while keeping list needs intact (#11771)", () => {
  const p1Session = session("p1", "needs_you");
  const p0Session = session("p0", "needs_you");
  const mixedSession = session("mix", "needs_you");
  const bareSession = session("bare", "needs_you");
  const cards: Interaction[] = [
    { id: "c1", machine_id: "m", session_id: "p1", kind: "permission", push_level: "P1" },
    { id: "c2", machine_id: "m", session_id: "p0", kind: "question", push_level: "P0" },
    { id: "c3", machine_id: "m", session_id: "mix", kind: "permission", push_level: "P1" },
    { id: "c4", machine_id: "m", session_id: "mix", kind: "permission" },
  ];
  expect(stateCounts([p1Session, p0Session, mixedSession, bareSession], cards, false)).toEqual({
    needs: 4, working: 0, waiting: 0, unread: 0, badge: 4,
  });
  expect(stateCounts([p1Session, p0Session, mixedSession, bareSession], cards, true)).toEqual({
    needs: 4, working: 0, waiting: 0, unread: 0, badge: 3,
  });
  expect(othersWaiting(1, p1Session, false, true)).toBe(1);
});
