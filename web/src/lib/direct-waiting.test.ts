import { describe, expect, it } from "vitest";
import { decisionLaterKey, type NeedsYouItem } from "./assistant-entries";
import { directUserInteraction, directUserWaiting, directWaitingCount } from "./direct-waiting";
import type { FeedItem } from "./feed";
import type { Todo, TodoStatus } from "./todos";
import type { Interaction, Session } from "./types";

const at = "2026-11-01T10:00:00Z";
const todo = (id: string, extra: Partial<Todo> = {}): Todo => ({ id, title: `Task ${id}`, created_at: at, updated_at: at, agents: [], ...extra });
const card = (id: string, kind: Interaction["kind"], extra: Partial<Interaction> = {}): Interaction => ({ id, kind, machine_id: "hub", session_id: "agent", created_at: at, ...extra });
const task = (id: string, status: TodoStatus, asks: Interaction[] = [], extra: Partial<Todo> = {}, countsOpen = true): Extract<NeedsYouItem, { kind: "task" }> => ({ kind: "task", key: `task:${id}`, todo: todo(id, extra), status, asks, countsOpen });
const agent = (asks: Interaction[]): Extract<NeedsYouItem, { kind: "agent" }> => ({ kind: "agent", key: "agent:hub/agent", session: { machine_id: "hub", session_id: "agent", title: "Agent" } as Session, asks });
const message: Extract<NeedsYouItem, { kind: "message" }> = { kind: "message", key: "message:draft", item: { id: "draft", kind: "message", conversation: "main", at, body: { draft: "A synthetic draft" } } as FeedItem };
const plan: NeedsYouItem = { kind: "plan", key: "plan:unstarted", plan: { id: "unstarted", conversation: "main", tasks: [], state: "open" } };

describe("main conversation direct-user waiting", () => {
  it("keeps current direct questions and user-only authorization, not Agent permissions or terminal dialogs", () => {
    expect(directUserInteraction(card("question", "question"))).toBe(true);
    expect(directUserInteraction(card("form", "elicitation"))).toBe(true);
    expect(directUserInteraction(card("owner", "permission", { owner_proposal: true }))).toBe(true);
    expect(directUserInteraction(card("permission", "permission"))).toBe(false);
    expect(directUserInteraction(card("plan", "plan"))).toBe(false);
    expect(directUserInteraction(card("screen", "screen"))).toBe(false);
    expect(directUserInteraction(card("delegating", "question", { delegating: true }))).toBe(false);
  });

  it("includes decisions, expired current ask_user questions, Send authorization and explicit user acceptance", () => {
    const items: NeedsYouItem[] = [
      task("decision", "needs_you", [], { ask: { verdict: "Choose a rollout", outcome: "decide", asked_at: at } }),
      task("expired", "needs_you", [], { ask: { verdict: "Answer the current question", asked_at: at, questions: [{ question: "Which one?", header: "Choice", options: [] }] } }),
      task("accept", "not_started", [], { delivery: { state: "pr", unblock: "user" } }),
      message,
    ];
    expect(directUserWaiting(items).map((item) => item.key)).toEqual(["task:decision", "task:expired", "task:accept", "message:draft"]);
  });

  it("keeps explicit delivery acceptance even when its older review decision was dismissed", () => {
    const row = task("delivery", "follow_up", [], {
      delivery: { state: "merged", unblock: "user" },
      decision_review: { review_at: at, dismissal_id: "dismissed" },
      supervision: { state: "needs_user", supervisor: "persona", reviews: [{ at, verdict: "needs_user" }] },
    });
    expect(directUserWaiting([row]).map((item) => item.key)).toEqual(["task:delivery"]);
  });

  it("keeps a plan waiting for Start, excludes failed work, generic supervisor states, deferred and superseded decisions", () => {
    const dismissed = todo("dismissed", {
      ask: { verdict: "An old question", outcome: "decide", asked_at: at },
      decision_review: { review_at: at, dismissal_id: "dismissed" },
      supervision: { state: "needs_user", supervisor: "persona", reviews: [{ at, verdict: "needs_user" }] },
    });
    const deferred = todo("deferred", { ask: { verdict: "Later", outcome: "decide", asked_at: at } });
    const rows: NeedsYouItem[] = [
      plan,
      task("prestart", "not_started", [], { ask: { verdict: "Only the owner can choose the destination", outcome: "decide", asked_at: at } }),
      task("failed", "failed"),
      task("interrupted", "interrupted"),
      task("review", "follow_up", [], { supervision: { state: "pending_review", supervisor: "persona", reviews: [] } }),
      task("dismissed", "needs_you", [], dismissed),
      task("deferred", "needs_you", [], deferred),
      agent([card("agent-permission", "permission"), card("terminal", "screen")]),
    ];
    // only the plan waiting for Start: the owner's own decision
    expect(directUserWaiting(rows, { [decisionLaterKey(deferred)]: true })).toEqual([plan]);
  });

  it("filters mixed Agent rows and counts exactly the rows and own cards that open", () => {
    const rows = directUserWaiting([
      agent([card("permission", "permission"), card("question", "question")]),
      task("open", "needs_you", [card("task-question", "question")], {}, false),
      plan,
    ]);
    const own = [card("own-question", "question"), card("own-permission", "permission")];
    expect(rows.map((item) => item.key)).toEqual(["agent:hub/agent", "task:open", "plan:unstarted"]);
    expect(rows[0].kind === "agent" && rows[0].asks.map((ix) => ix.id)).toEqual(["question"]);
    expect(directWaitingCount(rows, own)).toBe(4);
  });

  it("follows the hub's view of a to-do when there is one, as the To-dos badge does", () => {
    const view = (section: string) => ({ phase: section, section, reason: section === "needs_you" ? "failed_start" : "persona_working", rank: 0 });
    // a failed start the hub files under Needs you is the owner's; one its supervisor handles (working) is not
    expect(directUserWaiting([task("failed", "failed", [], { view: view("needs_you") })]).map((i) => i.key)).toEqual(["task:failed"]);
    expect(directUserWaiting([task("held", "needs_you", [card("q", "question", { delegating: true })], { view: view("working") })])).toEqual([]);
  });
});
