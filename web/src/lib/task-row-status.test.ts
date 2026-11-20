import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import { agentRowKey, agentRowLabel, agentRowMark, todoChipState } from "./task-row-status";
import type { Supervision } from "./supervision";
import type { Session } from "./types";

const session = (state: Session["state"]): Session => ({ machine_id: "m", session_id: "s", title: "", cwd: "/", state, status: state === "offline" ? "offline" : "idle", updated_at: "2026-11-13T00:00:00Z" });
const parked: Supervision = { state: "parked", supervisor: "c", reviews: [], parked: { reason: "Waiting on review", until: "2026-11-20T00:00:00Z", waiting_on: "someone_else", count: 1 } };

describe("a task row's status column says only what its Agent is doing (#6492)", () => {
  it("reads the Agent's own state, not the to-do's", () => {
    // a parked or follow-up to-do: the column says what its lead session is, stopped or idle
    expect(agentRowKey({ live: false, status: "follow_up", lead: session("offline"), todo: true, starting: false })).toBe("stopped");
    expect(agentRowKey({ live: false, status: "follow_up", lead: session("idle"), todo: true, starting: false })).toBe("idle");
    expect(agentRowKey({ live: false, status: "done", lead: undefined, todo: true, starting: false })).toBe("stopped");
    expect(agentRowKey({ live: false, status: "follow_up", lead: session("unread"), todo: true, starting: false })).toBe("unread");
    // the states that are the Agent's own stay as they are
    expect(agentRowKey({ live: false, status: "needs_you", lead: session("needs_you"), todo: true, starting: false })).toBe("needs_you");
    expect(agentRowKey({ live: false, status: "usage_limit", todo: true, starting: false })).toBe("usage_limit");
    expect(agentRowKey({ live: true, status: "follow_up", lead: session("idle"), todo: true, starting: false })).toBe("working");
    // nothing started yet: a to-do whose Agent is coming up, or a plan card without a to-do
    expect(agentRowKey({ live: false, status: "not_started", todo: true, starting: true })).toBe("starting");
    // a to-do nobody dispatched (todo_create, no plan) is not starting (#7594)
    expect(agentRowKey({ live: false, status: "not_started", todo: true, starting: false })).toBe("not_started");
    expect(agentRowKey({ live: false, status: "accepted", todo: false, starting: true })).toBe("not_started");
  });

  it.each(["follow_up", "parked"])("folds working and asking children into an idle lead's %s row", (status) => {
    expect(agentRowKey({ live: false, status, lead: { ...session("idle"), children: { total: 1, working: 1 } }, todo: true, starting: false })).toBe("working");
    expect(agentRowKey({ live: false, status, lead: { ...session("idle"), children: { total: 1, needs_you: 1 } }, todo: true, starting: false })).toBe("needs_you");
  });

  it("draws every key with the Agents' own marks and names stopped and idle apart", () => {
    expect(agentRowMark("stopped")).toBe("offline");
    expect(agentRowMark("idle")).toBe("idle");
    expect(agentRowMark("working")).toBe("working");
    expect(agentRowLabel("stopped")).toBe(t("assistant-tasks.agent_stopped"));
    expect(agentRowLabel("idle")).toBe(t("assistant-tasks.agent_idle"));
    expect(agentRowLabel("stopped")).not.toBe(t("assistant-tasks.review_parked"));
  });
});

describe("the to-do's own state is a chip of its own (#6492)", () => {
  it("names parked, waiting to merge, accepted and waiting to start; nothing for a to-do with nothing to add", () => {
    expect(todoChipState({ toStart: false, status: "follow_up", done: false, todo: { done_at: null, supervision: parked } })).toBe(t("assistant-tasks.review_parked"));
    expect(todoChipState({ toStart: false, status: "follow_up", done: false, stage: "merge", pr: "https://example.com/pr/1", todo: { done_at: null } })).toBe(t("assistant-tasks.todo_waiting_merge"));
    expect(todoChipState({ toStart: false, status: "follow_up", done: false, stage: "merge", todo: { done_at: null } })).toBeUndefined();
    expect(todoChipState({ toStart: true, status: "not_started", done: false })).toBe(t("assistant-tasks.plan_waiting"));
    expect(todoChipState({ toStart: false, status: "accepted", done: true })).toBe(t("assistant-tasks.review_accepted"));
    expect(todoChipState({ toStart: false, status: "working", done: false, todo: { done_at: null } })).toBeUndefined();
  });

  it("says awaiting review and rework only while the Agent is not working or asking", () => {
    const review = (state: Supervision["state"]): Supervision => ({ state, supervisor: "c", reviews: [], round: 2 });
    for (const status of ["working", "waiting", "needs_you", "unread"]) {
      expect(todoChipState({ toStart: false, status, done: false, todo: { done_at: null, supervision: review("pending_review") } })).toBeUndefined();
      expect(todoChipState({ toStart: false, status, done: false, todo: { done_at: null, supervision: review("rework") } })).toBeUndefined();
    }
    expect(todoChipState({ toStart: false, status: "follow_up", done: false, todo: { done_at: null, supervision: review("pending_review") } })).toBe(t("assistant-tasks.review_pending"));
    expect(todoChipState({ toStart: false, status: "follow_up", done: false, todo: { done_at: null, supervision: review("rework") } })).toBe(t("assistant-tasks.review_rework", { round: 2 }));
  });

  it("has no Accepted chip for a continuation's new round, only for an acceptance that stands (#11553)", () => {
    const earlier = { at: "2026-11-20T02:23:57Z", verdict: "accepted" as const };
    // task_plan continues: the hub kept the earlier acceptance in the reviews and put the ledger back to going on
    const continued: Supervision = { state: "", supervisor: "c", reviews: [earlier], round: 0 };
    for (const status of ["working", "follow_up"]) {
      expect(todoChipState({ toStart: false, status, done: false, todo: { done_at: null, supervision: continued } })).toBeUndefined();
    }
    // more work sent to the accepted task's Agent (#3156): its acceptance stands until the next review
    const standing: Supervision = { ...continued, state: "accepted" };
    expect(todoChipState({ toStart: false, status: "working", done: false, todo: { done_at: null, supervision: standing } })).toBe(t("assistant-tasks.review_accepted"));
  });
});
