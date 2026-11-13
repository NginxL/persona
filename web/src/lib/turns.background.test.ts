import { describe, expect, it } from "vitest";
import { needsYouBySupervisor, type NeedsYouItem } from "./assistant-entries";
import { foldsBackgroundRounds } from "./background-fold";
import { chatLines, type ChatLine } from "./turns";
import type { Todo } from "./todos";
import type { Item } from "./types";

// Background rounds in the main conversation (#4314, docs/assistant/assistant.md "Background rounds").

const source = (meta: Record<string, string>) => `<persona-context kind="message-source">${JSON.stringify(meta)}</persona-context>\n`;
const relayed = (seq: number, text = "Check the fictional release notes."): Item => ({
  id: `u${seq}#0`, seq, role: "user", kind: "text", text: source({ kind: "conversation", conversation_id: "side-1", title: "Release desk" }) + text,
});
const scheduled = (seq: number, state: "daily" | "once" = "daily"): Item => ({
  id: `u${seq}#0`, seq, role: "user", kind: "text", text: "Fictional scheduled prompt: read every inbox and summarize it.",
  injected: { kind: "followup", state, title: "Morning brief", line: "Scheduled task “Morning brief” started", detail: "Read every inbox and summarize it." },
  source: { kind: state === "daily" ? "patrol" : "followup", title: "Morning brief" },
});
const mine = (seq: number, text = "A real request", extra: Partial<Item> = {}): Item => ({ id: `m${seq}#0`, seq, role: "user", kind: "text", text, client_msg_id: `c${seq}`, ...extra });
const answer = (seq: number, text = "Done."): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text });
const call = (seq: number, name: string, tool_input: Record<string, unknown> = {}): Item => ({ id: `t${seq}#0`, seq, role: "assistant", kind: "tool_use", tool_use_id: `call${seq}`, tool_name: name, tool_input });
const result = (seq: number, use: Item, text = "ok", is_error = false): Item => ({ id: `r${seq}#0`, seq, role: "user", kind: "tool_result", tool_use_id: use.tool_use_id, text, is_error });

const on = { background: true };
const kinds = (lines: ChatLine[]) => lines.map((l) => l.kind);
const users = (lines: ChatLine[]) => lines.flatMap((l) => l.kind === "segment" ? [l.seg.user] : l.kind === "supervision" ? l.lines.flatMap((c) => c.kind === "segment" ? [c.seg.user] : []) : []);

describe("background rounds in the main conversation", () => {
  it("folds a supervisor's conversation_send round into one line that keeps its whole transcript", () => {
    const read = call(2, "Read");
    const lines = chatLines([relayed(1), read, result(3, read), answer(4), mine(5), answer(6)], on);
    expect(kinds(lines)).toEqual(["supervision", "segment"]);
    const fold = lines[0];
    expect(fold.kind === "supervision" && fold.source).toMatchObject({ kind: "conversation", title: "Release desk" });
    // opening it shows everything, the supervisor's message included (super rule 2.4)
    expect(fold.kind === "supervision" && fold.lines.flatMap((l) => l.kind === "segment" ? [l.seg.user?.text, l.seg.answer?.text ?? l.seg.steps.length] : [])).toContain("Check the fictional release notes.");
    expect(lines[1].kind === "segment" && lines[1].seg.user?.client_msg_id).toBe("c5");
    // opened, the input is its source block, not the raw model input with its metadata
    expect(users([fold])[0]?.sourceInTrace).toBe(true);
  });

  it("folds a scheduled task's and a follow-up's round and never shows their prompt", () => {
    for (const state of ["daily", "once"] as const) {
      const lines = chatLines([scheduled(1, state), answer(2)], on);
      expect(kinds(lines)).toEqual(["supervision"]);
      const [user] = users(lines);
      expect(user?.promptHidden).toBe(true);
      expect(user?.text).toBe("Scheduled task “Morning brief” started");
    }
  });

  it("keeps a round that asked the user (ask_user) shown, its prompt still hidden", () => {
    const ask = call(2, "mcp__persona__ask_user", { question: "Send the brief?" });
    const lines = chatLines([scheduled(1), ask, result(3, ask, "Yes"), answer(4)], on);
    expect(kinds(lines)).toEqual(["segment"]);
    expect(users(lines)[0]?.promptHidden).toBe(true);
    const tell = call(2, "notify_user");
    expect(kinds(chatLines([relayed(1), tell, result(3, tell), answer(4)], on))).toEqual(["segment"]);
    // a pending ask_user while it runs stays visible too
    expect(kinds(chatLines([relayed(1), ask], { ...on, live: true }))).toEqual(["segment"]);
    // a failed ask_user does not promote the round
    expect(kinds(chatLines([relayed(1), ask, result(3, ask, "error", true), answer(4)], on))).toEqual(["supervision"]);
  });

  it("keeps a round with a task_review verdict card shown", () => {
    const review = call(2, "task_review", { verdict: "accepted" });
    expect(kinds(chatLines([relayed(1), review, result(3, review), answer(4)], on))).toEqual(["segment"]);
  });

  it("changes nothing with the switch off or outside the main conversation", () => {
    const items = [relayed(1), answer(2), scheduled(3), answer(4), mine(5), answer(6)];
    const off = chatLines(items);
    expect(kinds(off)).toEqual(["segment", "segment", "segment"]);
    expect(users(off).some((u) => u?.promptHidden)).toBe(false);
    expect(chatLines(items, { background: false })).toEqual(off);
  });

  it("keeps Persona continuing the user's own work shown: a usage-limit continuation, a wake-up, an untitled follow-up", () => {
    const question = mine(1, "Summarize the fictional report");
    const continued = (seq: number, injected?: Item["injected"]): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text: "Continue where you stopped.", injected, source: { kind: "followup", ...(injected?.title ? { title: injected.title } : {}) } });
    for (const input of [
      continued(3, { kind: "followup", state: "limit_continue", line: "The usage limit reset; Persona continues" }),
      continued(3, { kind: "followup", state: "wake", title: "the fictional build", line: "Woken for the fictional build" }),
      continued(3),
    ]) {
      const lines = chatLines([question, answer(2, "Starting."), input, answer(4, "The final summary.")], on);
      expect(kinds(lines)).not.toContain("supervision");
    }
  });

  it("keeps a queued real message out of a background round", () => {
    const lines = chatLines([scheduled(1), answer(2), mine(3, "Also check the calendar", { queued: true }), answer(4)], on);
    expect(kinds(lines)).toEqual(["supervision", "segment"]);
  });

  it("leaves the user's own rounds and supervision rounds as they were", () => {
    expect(kinds(chatLines([mine(1), answer(2)], on))).toEqual(["segment"]);
    const supervise: Item = { id: "s1#0", seq: 1, role: "user", kind: "text", text: "x", injected: { kind: "system", state: "supervise", level: "triage", line: "Check tasks" } };
    const [fold] = chatLines([supervise, answer(2)], on);
    expect(fold.kind === "supervision" && fold.source).toBeUndefined();
  });

  it("a leader conversation folds the same rounds the main chat does (#6595)", () => {
    // the gate the chat view uses: a leader id with the switch on enables the same background option
    const info = { machine_id: "m1", main: "main", leaders: ["lead-1"], main_background_folded: true };
    expect(foldsBackgroundRounds("lead-1", info)).toBe(true);
    expect(foldsBackgroundRounds("side-2", info)).toBe(false);
    // with that option the leader's patrol / relayed rounds fold, the owner's exchange stays open
    const items = [relayed(1), answer(2), scheduled(3), answer(4), mine(5), answer(6)];
    expect(kinds(chatLines(items, { background: foldsBackgroundRounds("lead-1", info) }))).toEqual(["supervision", "supervision", "segment"]);
    // a non-leader side chat keeps every round
    expect(kinds(chatLines(items, { background: foldsBackgroundRounds("side-2", info) }))).toEqual(["segment", "segment", "segment"]);
  });
});

describe("Needs you by supervisor", () => {
  const todo = (id: string, origin?: string): Todo => ({ id, title: id, origin, created_at: "2026-11-10T09:00:00Z", updated_at: "2026-11-10T09:00:00Z" });
  const task = (id: string, origin?: string): NeedsYouItem => ({ kind: "task", key: `task:${id}`, todo: todo(id, origin), status: "needs_you", asks: [], countsOpen: true });
  const plan: NeedsYouItem = { kind: "plan", key: "plan:p1", plan: { id: "p1", conversation: "main", state: "open", created_at: "2026-11-10T09:00:00Z", tasks: [] } as never };

  it("groups rows under the conversation that supervises them, this one first, rows and count unchanged", () => {
    const items = [task("a", "side-1"), task("b", "main"), plan, task("c", "side-2"), task("d", "side-1"), task("e")];
    const groups = needsYouBySupervisor(items, "main");
    expect(groups.map((g) => [g.supervisor, g.items.map((i) => i.key)])).toEqual([
      ["main", ["task:b", "plan:p1", "task:e"]],
      ["side-1", ["task:a", "task:d"]],
      ["side-2", ["task:c"]],
    ]);
    expect(groups.flatMap((g) => g.items).length).toBe(items.length);
  });

  it("is one group when every row is this conversation's", () => {
    const items = [task("a", "main"), plan];
    expect(needsYouBySupervisor(items, "main")).toEqual([{ supervisor: "main", items }]);
    expect(needsYouBySupervisor([], "main")).toEqual([{ supervisor: "main", items: [] }]);
  });
});
