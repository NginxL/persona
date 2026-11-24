import { describe, expect, it } from "vitest";
import type { AnsweredCard } from "../lib/store";
import type { Interaction, Session } from "../lib/types";
import { sidebarPending } from "./sidebar-pending";

const session = (machine_id: string, session_id = "session"): Session => ({ machine_id, session_id, title: "Example", cwd: "/work/example", status: "blocked" });
const card = (id: string, machine_id: string, session_id = "session"): Interaction => ({ id, machine_id, session_id, kind: "permission" });
const hour = (h: number) => `2026-01-02T${String(h).padStart(2, "0")}:00:00Z`;

describe("sidebarPending", () => {
  it("counts a card and its blocked session once, keeping multiple cards and the machine boundary", () => {
    const first = card("first", "one");
    const next = { ...card("next", "one"), created_at: "2026-01-01T00:00:00Z" };
    const rows = sidebarPending([session("one"), session("two")], [first, next, first], {});
    expect(rows.map((r) => ({ machine: r.machineId, cards: r.cards.map((ix) => ix.id), to: r.to }))).toEqual([
      { machine: "one", cards: ["next", "first"], to: { name: "session", mid: "one", sid: "session", view: "chat", work: true } },
      { machine: "two", cards: [], to: { name: "session", mid: "two", sid: "session", view: "chat", work: true } },
    ]);
  });

  it("keeps each sessionless request and an interaction whose session is not loaded", () => {
    const rows = sidebarPending([], [card("a", "one", ""), card("b", "one", ""), card("c", "two")], {});
    expect(rows.map((r) => r.to)).toEqual([
      { name: "request", id: "a" },
      { name: "request", id: "b" },
      { name: "session", mid: "two", sid: "session", view: "chat", work: true },
    ]);
  });

  it("removes an answered session before the next push, unless a new card or a hand-back keeps it waiting", () => {
    const s = session("one");
    const old: AnsweredCard = { ix: card("old", "one"), resolution: { allow: true } };
    expect(sidebarPending([s], [], { old })).toEqual([]);
    expect(sidebarPending([s], [card("new", "one")], { old })[0].cards[0].id).toBe("new");
    expect(sidebarPending([s], [], { old: { ...old, handedBack: true } })[0].cards).toEqual([]);
  });

  it("does not treat unread, working, offline or idle sessions as pending", () => {
    const sessions = (["unread", "working", "offline", "idle"] as const).map((state) => ({ ...session("one", state), state }));
    expect(sidebarPending(sessions, [], {})).toEqual([]);
  });

  it("ranks urgency class first and the longest wait within a class (#8167)", () => {
    const sessions: Session[] = [
      { ...session("m-bare", "bare"), state: "needs_you", state_since: hour(2) },
      { ...session("m-error", "error"), state: "needs_you", state_since: hour(1), failure_reason: "stopped on an error" },
    ];
    const interactions: Interaction[] = [
      { ...card("card-new", "m-new", "new"), created_at: hour(9) },
      { ...card("card-old", "m-old", "old"), created_at: hour(4) },
    ];
    const rows = sidebarPending(sessions, interactions, {});
    // blocked-on-answer rows first, longest wait leading; the error-flagged stop without a card follows them
    expect(rows.map((r) => r.key)).toEqual([
      'session:["m-bare","bare"]',
      'session:["m-old","old"]',
      'session:["m-new","new"]',
      'session:["m-error","error"]',
    ]);
    // membership and the per-row card counts are what the old newest-first build produced
    expect(rows).toHaveLength(4);
    expect(rows.map((r) => r.cards.length)).toEqual([0, 1, 1, 0]);
  });

  it("puts an undated row after the dated ones of its class and breaks the rest by key", () => {
    const rows = sidebarPending([], [
      card("z", "m-z", ""),
      card("a", "m-a", ""),
      { ...card("dated", "m-d", ""), created_at: hour(5) },
    ], {});
    expect(rows.map((r) => r.key)).toEqual(["request:dated", "request:a", "request:z"]);
  });

  it("keeps a row's own cards newest first even though the rows now wait-order", () => {
    const rows = sidebarPending([session("one")], [
      { ...card("older", "one"), created_at: hour(2) },
      { ...card("newer", "one"), created_at: hour(8) },
    ], {});
    expect(rows).toHaveLength(1);
    expect(rows[0].cards.map((ix) => ix.id)).toEqual(["newer", "older"]);
  });

  it("leaves out a card the task's supervisor holds and its agent waiting only on it; keeps an unheld one", () => {
    const held: Interaction = { id: "held", machine_id: "one", session_id: "session", kind: "screen", delegating: true };
    expect(sidebarPending([session("one")], [held], {})).toEqual([]);
    const mixed = sidebarPending([session("one")], [held, card("mine", "one")], {});
    expect(mixed.map((r) => r.cards.map((ix) => ix.id))).toEqual([["mine"]]);
    const plain = sidebarPending([session("one")], [{ ...held, delegating: undefined }], {});
    expect(plain.map((r) => r.cards.map((ix) => ix.id))).toEqual([["held"]]);
    // a question the assistant is still looking at stays as before
    const asking = sidebarPending([session("one")], [{ ...held, kind: "question" }], {});
    expect(asking.map((r) => r.cards.map((ix) => ix.id))).toEqual([["held"]]);
  });
});
