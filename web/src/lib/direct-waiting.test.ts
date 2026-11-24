import { describe, expect, it } from "vitest";
import { decisionLaterKey, type NeedsYouItem } from "./assistant-entries";
import { directUserInteraction, directUserWaiting, directWaitingCount } from "./direct-waiting";
import type { FeedItem } from "./feed";
import { hubNeedsYou } from "./todo-triage";
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

  it("includes screen, permission and plan cards escalated by ask_escalate, and always blocks delegating cards", () => {
    const escalated = { delegate: { reason: "Needs owner confirmation", recommend: "Allow" } };
    expect(directUserInteraction(card("scr-escalated", "screen", escalated))).toBe(true);
    expect(directUserInteraction(card("perm-escalated", "permission", escalated))).toBe(true);
    expect(directUserInteraction(card("plan-escalated", "plan", escalated))).toBe(true);
    // empty or whitespace-only delegate context does not turn routine cards into escalations
    expect(directUserInteraction(card("scr-blank", "screen", { delegate: { reason: "   " } }))).toBe(false);
    expect(directUserInteraction(card("perm-empty", "permission", { delegate: {} }))).toBe(false);
    // cards still held by a supervisor stay hidden even if delegate or owner_proposal is present
    expect(directUserInteraction(card("scr-held", "screen", { delegating: true, ...escalated }))).toBe(false);
    expect(directUserInteraction(card("perm-held", "permission", { delegating: true, ...escalated }))).toBe(false);
    expect(directUserInteraction(card("plan-held", "plan", { delegating: true, ...escalated }))).toBe(false);
    expect(directUserInteraction(card("owner-held", "permission", { delegating: true, owner_proposal: true }))).toBe(false);
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
    const view = (section: string, reason = section === "needs_you" ? "failed_start" : "persona_working") => ({ phase: section, section, reason, rank: 0 });
    // a failed start the hub files under Needs you is the owner's; one its supervisor handles (working) is not
    expect(directUserWaiting([task("failed", "failed", [], { view: view("needs_you") })]).map((i) => i.key)).toEqual(["task:failed"]);
    expect(directUserWaiting([task("held", "needs_you", [card("q", "question", { delegating: true })], { view: view("working") })])).toEqual([]);
  });

  it("retains escalated and hub needs_you screen, permission and plan cards while excluding delegating ones", () => {
    const agentView = (section: string, reason = section === "needs_you" ? "agent_asks" : "persona_working") => ({ phase: section, section, reason, rank: 0 });
    const escalated = { delegate: { reason: "Command requires owner sign-off", recommend: "Allow" } };
    const screenEscalated = card("scr_1", "screen", escalated);
    const screenCommand = card("scr_cmd", "screen", { dialog: { command: "rm -rf dist", question: "Do you want to proceed?", options: [{ key: "1", label: "Yes" }, { key: "2", label: "No" }] } });
    const permEscalated = card("ix_perm_esc", "permission", escalated);
    const permDirect = card("ix_perm_hub", "permission", { tool_name: "Bash" });
    const planEscalated = card("ix_plan_esc", "plan", escalated);
    const screenHeld = card("scr_held", "screen", { delegating: true });
    const permHeld = card("ix_perm_held", "permission", { delegating: true });

    // Without hub view (older hub): explicitly escalated screen/permission/plan cards show; unescalated or delegating do not.
    const legacy = directUserWaiting([
      task("t-scr-esc", "needs_you", [screenEscalated]),
      task("t-perm-esc", "needs_you", [permEscalated]),
      task("t-plan-esc", "needs_you", [planEscalated]),
      task("t-scr-plain", "needs_you", [screenCommand]),
      task("t-perm-plain", "needs_you", [permDirect]),
      task("t-held", "needs_you", [screenHeld, permHeld]),
    ]);
    expect(legacy.map((i) => [i.key, i.kind === "task" ? i.asks.map((a) => a.id) : []])).toEqual([
      ["task:t-scr-esc", ["scr_1"]],
      ["task:t-perm-esc", ["ix_perm_esc"]],
      ["task:t-plan-esc", ["ix_plan_esc"]],
    ]);
    expect(directWaitingCount(legacy, [card("own-perm-esc", "permission", escalated), card("own-perm-held", "permission", { delegating: true })])).toBe(4);

    // With hub view in needs_you / agent_asks:
    // - Non-quiet tasks counted by hubNeedsYou stay counted (keeping tray, To-dos badge and hub on one number),
    //   attaching non-delegating screen/permission/plan cards while always stripping delegating cards.
    // - Quiet tasks (hubNeedsYou === false) only surface explicitly escalated cards (hasEscalationContext), so
    //   unescalated terminal dialogs/permissions/plans stay with the supervisor and do not inflate main-chat count (D19).
    const heldOnlyTask = task("t-hub-only-held", "needs_you", [screenHeld], { view: agentView("needs_you") });
    const quietPlainScreenTask = task("t-quiet-scr", "needs_you", [screenCommand], { quiet: true, view: agentView("needs_you") });
    expect(hubNeedsYou(heldOnlyTask.todo, () => false)).toBe(true);
    expect(hubNeedsYou(quietPlainScreenTask.todo, () => false)).toBe(false);

    const withView = directUserWaiting([
      task("t-hub-scr", "needs_you", [screenHeld, screenCommand], { view: agentView("needs_you") }),
      task("t-hub-perm", "needs_you", [permHeld, permDirect], { view: agentView("needs_you") }),
      heldOnlyTask,
      task("t-hub-working", "needs_you", [screenHeld], { view: agentView("working") }),
      task("t-hub-lag", "needs_you", [screenEscalated], { view: agentView("working") }),
      quietPlainScreenTask,
      task("t-quiet-scr-esc", "needs_you", [screenEscalated], { quiet: true, view: agentView("needs_you") }),
      task("t-quiet-perm-esc", "needs_you", [permEscalated], { quiet: true, view: agentView("needs_you") }),
      task("t-quiet-plan-esc", "needs_you", [planEscalated], { quiet: true, view: agentView("needs_you") }),
      task("t-quiet-held", "needs_you", [screenHeld], { quiet: true, view: agentView("needs_you") }),
      task("t-quiet-working-plain", "needs_you", [permDirect], { quiet: true, view: agentView("working") }),
      // An owner-decision task does not get hijacked by an unrelated unescalated permission unless view.asks names it.
      task("t-decision", "needs_you", [permDirect], { view: agentView("needs_you", "owner_decides") }),
      task("t-decision-linked", "needs_you", [permDirect], {
        view: { phase: "needs_you", section: "needs_you", reason: "owner_decides", rank: 0, asks: [{ id: "run:1", kind: "tool", home: "run", interaction: "ix_perm_hub" }] },
      }),
    ]);
    expect(withView.map((i) => [i.key, i.kind === "task" ? i.asks.map((a) => a.id) : []])).toEqual([
      ["task:t-hub-scr", ["scr_cmd"]],
      ["task:t-hub-perm", ["ix_perm_hub"]],
      ["task:t-hub-only-held", []],
      ["task:t-hub-lag", ["scr_1"]],
      ["task:t-quiet-scr-esc", ["scr_1"]],
      ["task:t-quiet-perm-esc", ["ix_perm_esc"]],
      ["task:t-quiet-plan-esc", ["ix_plan_esc"]],
      ["task:t-decision", []],
      ["task:t-decision-linked", ["ix_perm_hub"]],
    ]);
    // Verify exact counts for the two D19 / one-number invariants on their own:
    expect(directWaitingCount(directUserWaiting([quietPlainScreenTask]), [])).toBe(0);
    expect(directWaitingCount(directUserWaiting([heldOnlyTask]), [])).toBe(1);
  });
});
