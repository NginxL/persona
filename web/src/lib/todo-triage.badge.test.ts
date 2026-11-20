// @vitest-environment jsdom
// The sidebar's To-dos badge (#11085, lib/todo-triage ownerBadge): it counts the open to-dos that need the owner, not
// those Agents are on. One case per line of the issue's acceptance, then the rules' edges: pieces at any depth, a
// `part_of` loop, quiet tasks, and the page's "Not done", which stays as it is. The data is made up.
import { describe, expect, it } from "vitest";
import type { Interaction, Session } from "./types";
import { makeSessionLookup, type Todo } from "./todos";
import { ownerBadge, ownerNeeds, triage, triageTodos, waitsOnOwner } from "./todo-triage";

const NOW = Date.parse("2026-11-10T12:00:00Z");
const hoursAgo = (h: number) => new Date(NOW - h * 3600_000).toISOString();

const session = (sid: string, state: Session["state"], patch: Partial<Session> = {}): Session =>
  ({ machine_id: "m", session_id: sid, state, status: state === "working" ? "working" : "idle", updated_at: hoursAgo(1), ...patch }) as Session;
const lookup = makeSessionLookup([
  session("run", "working"),
  session("run2", "working"),
  session("run3", "working"),
  // its turn ended, its background commands still run and will report back: an Agent on it (task Waiting)
  session("bg", "working", { status: "idle", background_working: true }),
  session("ask", "needs_you"),
  session("idle", "idle"),
  session("cut", "idle", { turn_error: "the model service answered 500" }),
  session("old", "idle", { updated_at: hoursAgo(100) }),
]);
const agent = (sid: string) => ({ machine_id: "m", session_id: sid, linked_at: hoursAgo(5) });

/** A task a conversation handed out. */
const task = (id: string, patch: Partial<Todo> = {}): Todo => ({ id, title: `Task ${id}`, created_at: hoursAgo(10), updated_at: hoursAgo(1), done_at: null, agents: [], origin: "conv", ...patch });
/** The owner's own to-do: no Agent was ever on it, no conversation handed it out. */
const personal = (id: string, patch: Partial<Todo> = {}): Todo => ({ id, title: `Errand ${id}`, created_at: hoursAgo(10), updated_at: hoursAgo(1), done_at: null, ...patch });
const counted = (list: Todo[]) => [...ownerBadge(list, lookup, NOW)].sort();

describe("the sidebar's To-dos badge, the issue's acceptance (#11085)", () => {
  it("leaves out a goal whose pieces Agents are all doing, none waiting on the owner", () => {
    const list = [
      personal("goal"),
      task("p1", { part_of: "goal", agents: [agent("run")] }),
      task("p2", { part_of: "goal", agents: [agent("bg")] }),
    ];
    expect(counted(list)).toEqual([]);
  });

  it("adds 1 for the same goal with one piece waiting on the owner: that piece, not the goal again", () => {
    const list = [
      personal("goal"),
      task("p1", { part_of: "goal", agents: [agent("run")] }),
      task("p2", { part_of: "goal", agents: [agent("ask")] }),
    ];
    expect(counted(list)).toEqual(["p2"]);
  });

  it("leaves out a to-do whose own Agent is working, or waits for something other than the owner", () => {
    expect(counted([task("t", { agents: [agent("run")] })])).toEqual([]);
    expect(counted([task("t", { agents: [agent("bg")] })])).toEqual([]);
  });

  it("counts a failed start waiting for a retry", () => {
    const failed = task("f", { start_error: "no launcher on that computer", done_at: hoursAgo(1) });
    expect(counted([failed])).toEqual(["f"]);
    expect(waitsOnOwner(failed, triage(failed, lookup, NOW))).toBe("failed_start");
  });

  it("counts a pending question: an Agent's card, or a decision the task asks the owner for", () => {
    const asking = task("q", { agents: [agent("ask"), agent("run")] });
    const decide = task("d", { agents: [agent("idle")], ask: { outcome: "decide", verdict: "Ship it or wait?", asked_at: hoursAgo(1) } });
    expect(counted([asking, decide])).toEqual(["d", "q"]);
    expect(waitsOnOwner(asking, triage(asking, lookup, NOW))).toBe("agent_asks");
    expect(waitsOnOwner(decide, triage(decide, lookup, NOW))).toBe("owner_decides");
  });

  it("counts a personal to-do no Agent was ever on", () => {
    expect(counted([personal("own")])).toEqual(["own"]);
  });

  it("keeps one badge count and calls the task agent_asks only while its exact linked interaction is pending", () => {
    const ix = { id: "ix-owner", machine_id: "m", session_id: "worker", kind: "question", created_at: hoursAgo(1) } as Interaction;
    const linked = task("linked", {
      agents: [{ ...agent("worker"), released_at: hoursAgo(0) }],
      supervision: {
        supervisor: "conv", state: "parked",
        parked: { reason: "Owner permission", waiting_on: "external_event", until: hoursAgo(-4), count: 1,
          evidence: { kind: "external_event", target: "Read-only permission", wake: "The owner answers", interaction_id: ix.id } },
      },
    });
    const sessions = [session("worker", "idle")];
    const pendingTodo = { ...linked, view: { phase: "needs_you", section: "needs_you", reason: "agent_asks", rank: 0,
      asks: [{ id: "run:worker", kind: "question", home: "run", interaction: ix.id }] } } as Todo;
    const pending = makeSessionLookup(sessions);
    expect(waitsOnOwner(pendingTodo, triage(pendingTodo, pending, NOW))).toBe("agent_asks");
    expect([...ownerBadge([pendingTodo], pending, NOW)]).toEqual(["linked"]);
    const resolved = makeSessionLookup(sessions);
    expect(waitsOnOwner(pendingTodo, triage(pendingTodo, makeSessionLookup(sessions, []), NOW))).toBeNull();
    expect(waitsOnOwner(linked, triage(linked, resolved, NOW))).toBeNull();
    // The existing no-Agent-on-it badge rule still counts the parked task once; the link does not change that policy.
    expect([...ownerBadge([linked], resolved, NOW)]).toEqual(["linked"]);
  });
});

describe("the badge's rules", () => {
  it("walks pieces at any depth", () => {
    const list = [
      personal("goal"),
      task("mid", { part_of: "goal" }),
      task("leaf", { part_of: "mid", agents: [agent("run")] }),
    ];
    expect(counted(list)).toEqual([]);
    // the Agent at the bottom stops: nothing is on any of them any more
    const stopped = list.map((td) => (td.id === "leaf" ? { ...td, agents: [agent("idle")] } : td));
    expect(counted(stopped)).toEqual(["goal", "leaf", "mid"]);
    // the bottom one waits on the owner: it counts, the two above it do not again
    const asking = list.map((td) => (td.id === "leaf" ? { ...td, agents: [agent("ask")] } : td));
    expect(counted(asking)).toEqual(["leaf"]);
  });

  it("walks a part_of loop once", () => {
    const loop = [personal("a", { part_of: "b" }), personal("b", { part_of: "c" }), personal("c", { part_of: "a" })];
    expect(counted(loop)).toEqual(["a", "b", "c"]);
    const carried = [personal("a", { part_of: "b" }), task("b", { part_of: "a", agents: [agent("run")] })];
    expect(counted(carried)).toEqual([]);
    expect(counted([personal("self", { part_of: "self" })])).toEqual(["self"]);
  });

  it("counts what waits on the owner even while an Agent works on it", () => {
    // its supervisor wants the owner's decision while its Agent is still at work
    const decide = task("d", { agents: [agent("run")], supervision: { supervisor: "conv", state: "needs_user" } });
    // its finished work is held at a PR for the owner's OK; Agents are at work on the request's other pieces
    const held = task("h", { part_of: "goal", agents: [agent("idle")], delivery: { state: "pr", unblock: "user" } });
    const list = [decide, personal("goal"), held, task("p", { part_of: "goal", agents: [agent("run2")] })];
    expect(counted(list)).toEqual(["d", "h"]);
    expect(waitsOnOwner(held, triage(held, lookup, NOW))).toBe("delivery_ok");
    const heldForDecision = { ...held, supervision: decide.supervision };
    expect(waitsOnOwner(heldForDecision, triage(heldForDecision, lookup, NOW))).toBe("owner_decides");
    // held for an Agent or for others, it is nobody's to OK: it still counts, as nothing shows an Agent on it
    const forAgent = { ...held, delivery: { state: "pr", unblock: "agent" } };
    expect(waitsOnOwner(forAgent, triage(forAgent, lookup, NOW))).toBeNull();
    // held for the owner's OK while its own Agent is at work again: the hold still waits on them
    expect(counted([task("again", { agents: [agent("run")], delivery: { state: "merged", unblock: "user" } })])).toEqual(["again"]);
    // a goal that itself asks the owner a decision, its pieces at work: the decision is theirs
    const asks = personal("ask-goal", { ask: { outcome: "decide", verdict: "Which launch day?", asked_at: hoursAgo(1) } });
    expect(counted([asks, task("busy", { part_of: "ask-goal", agents: [agent("run3")] })])).toEqual(["ask-goal"]);
  });

  it("shelves an inactive external hold, and keeps an explicit owner hold visible and counted once", () => {
    const old = hoursAgo(100);
    const shelved = task("held", { part_of: "goal", created_at: hoursAgo(120), updated_at: old, agents: [{ ...agent("old"), linked_at: hoursAgo(110) }], delivery: { state: "pr", unblock: "other", at: old } });
    expect(triage(shelved, lookup, NOW).section).toBe("shelved");
    expect(waitsOnOwner(shelved, triage(shelved, lookup, NOW))).toBeNull();
    expect(counted([personal("goal"), shelved])).toEqual(["goal"]);
    const approval = { ...shelved, delivery: { state: "pr", unblock: "user", at: old } };
    expect(triage(approval, lookup, NOW).section).toBe("waiting");
    expect(waitsOnOwner(approval, triage(approval, lookup, NOW))).toBe("delivery_ok");
    expect(counted([personal("goal"), approval])).toEqual(["held"]);
  });

  it("counts a cut-off Agent's task, and keeps counting tasks no Agent is on", () => {
    expect(counted([task("cut", { agents: [agent("cut")] })])).toEqual(["cut"]);
    // its Agents stopped; a piece waiting for its turn with no Agent yet
    expect(counted([task("stopped", { agents: [agent("idle")] }), task("queued", { part_of: "x", after: ["y"] })])).toEqual(["queued", "stopped"]);
  });

  it("leaves quiet tasks out, while a quiet piece's Agent still carries its goal", () => {
    expect(counted([personal("q", { quiet: true }), task("qa", { quiet: true, agents: [agent("ask")] })])).toEqual([]);
    expect(counted([personal("goal"), task("p", { part_of: "goal", quiet: true, agents: [agent("run3")] })])).toEqual([]);
  });

  it("is never more than the To-dos page's Not done, which stays the open count", () => {
    const list = [
      personal("goal"),
      task("p1", { part_of: "goal", agents: [agent("run")] }),
      task("p2", { part_of: "goal", agents: [agent("ask")] }),
      task("run", { agents: [agent("run2")] }),
      personal("own"),
      personal("closed", { done_at: hoursAgo(3) }),
      task("stale", { updated_at: hoursAgo(100), created_at: hoursAgo(120), agents: [{ ...agent("old"), linked_at: hoursAgo(110) }] }),
    ];
    expect(triageTodos(list, lookup, NOW).open).toBe(5);
    expect(counted(list)).toEqual(["own", "p2"]);
    // a closed or shelved to-do waits on nobody
    expect(waitsOnOwner(list[5], triage(list[5], lookup, NOW))).toBeNull();
    expect(waitsOnOwner(list[6], triage(list[6], lookup, NOW))).toBeNull();
  });
});

describe("why the badge counts a to-do (#11838, ownerNeeds)", () => {
  const list = [
    task("failed", { start_error: "no launcher on that computer", done_at: hoursAgo(1) }),
    task("asking", { agents: [agent("ask")] }),
    task("decide", { agents: [agent("idle")], ask: { outcome: "decide", verdict: "Ship it or wait?", asked_at: hoursAgo(1) } }),
    task("held", { agents: [agent("run")], delivery: { state: "merged", unblock: "user" } }),
    personal("errand"),
    task("stopped", { agents: [agent("idle")] }),
    // not counted: its own Agent at work, a goal its pieces carry, a quiet task whose Agent works
    task("working", { agents: [agent("run2")] }),
    personal("goal"),
    task("piece", { part_of: "goal", quiet: true, agents: [agent("run3")] }),
    task("quiet", { quiet: true }),
  ];

  it("says what each counted to-do waits on the owner for, or that no Agent is on it", () => {
    expect(Object.fromEntries(ownerNeeds(list, lookup, NOW))).toEqual({
      failed: "failed_start",
      asking: "agent_asks",
      decide: "owner_decides",
      held: "delivery_ok",
      errand: "unattended",
      stopped: "unattended",
    });
  });

  it("is the badge: the same to-dos, so its size is the badge's number", () => {
    expect([...ownerBadge(list, lookup, NOW)].sort()).toEqual([...ownerNeeds(list, lookup, NOW).keys()].sort());
    expect(ownerBadge(list, lookup, NOW).size).toBe(6);
  });
});
