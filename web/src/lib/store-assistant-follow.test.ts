import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { recentConversations } from "./conversation-origins";
import { chooseManager } from "./manager-choice";
import { applyCompleteSessions, connect, getState, refreshAssistant, reset } from "./store";
import type { Session } from "./types";

// #9049: on a socket in list mode the hub sends hints only, so the assistant's conversations (the Persona sidebar, All
// chats) must follow the rows the list reads after a hint, or a conversation read mid-turn keeps spinning.

class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) { Socket.last = this; }
  send() {}
  close() { this.readyState = 3; }
  push(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
}

const row = (id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "hub", session_id: id, title: id, cwd: "/home/me/.persona/assistant", status: "idle", state: "idle", updated_at: "2026-11-16T05:00:00Z", ...extra });
const working = (id: string, extra: Partial<Session> = {}) => row(id, { status: "working", state: "working", assistant: true, ...extra });
let changed: Session[];
let conversations: Session[];
let assistantReads: number;
let assistantFails: number; // the next reads of /assistant that answer 503
let assistantHeld: Promise<void> | null; // the next read of /assistant answers once this settles, with what it read when asked

beforeEach(() => {
  vi.useFakeTimers();
  changed = [];
  assistantReads = 0;
  assistantFails = 0;
  assistantHeld = null;
  conversations = [working("main"), working("leader", { turn_error: "API Error: 503", turn_error_kind: "server" })];
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("location", { protocol: "http:", host: "demo" });
  vi.stubGlobal("localStorage", { getItem: () => "demo-token" });
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const url = new URL(path, "http://demo");
    if (url.pathname === "/api/v1/assistant") {
      assistantReads++;
      if (assistantFails > 0 && assistantFails--) return Response.json({ error: "database down" }, { status: 503 });
      const answer = Response.json({ machine_id: "hub", main: "main", background_supervisor: "bg", conversations });
      const held = assistantHeld;
      assistantHeld = null;
      if (held) await held;
      return answer;
    }
    if (url.pathname === "/api/v1/sessions") {
      if (url.searchParams.has("counts")) return Response.json({ total: 3, machine: { hub: 3 }, project: {}, group: {}, state: {}, revision: "r1" });
      if (url.searchParams.has("since")) return Response.json({ sessions: changed, remove: [], total: 3, has_more: false, revision: "r2" });
      return Response.json({ sessions: [], remove: [], total: 3, has_more: false, revision: "r1" });
    }
    return Response.json({});
  }));
});
afterEach(() => { reset(); vi.useRealTimers(); vi.unstubAllGlobals(); });
const settle = () => vi.advanceTimersByTimeAsync(0);
const conversation = (id: string) => getState().assistant?.conversations.find((c) => c.session_id === id);
/** What the sidebar's Recent lists (views/Sidebar.tsx), by id. */
const recent = () => {
  const a = getState().assistant;
  return recentConversations(a?.conversations ?? [], a?.main, a?.leaders ?? [], a?.background_supervisor).map((c) => c.session_id);
};
const kept = (id: string) => getState().sessions.hub?.find((s) => s.session_id === id);

async function listMode() {
  connect();
  Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup"] });
  await settle();
  await settle();
  expect(conversation("leader")?.state).toBe("working");
}

describe("the assistant's conversations follow the list (#9049)", () => {
  it("a conversation read mid-turn shows finished once a hint's read says its turn ended", async () => {
    await listMode();
    changed = [row("leader", { assistant: true, waiting: false, last_text: "Done." })];
    Socket.last.push({ type: "sessions_list", revision: "r2" });
    await settle();
    await settle();
    expect(conversation("leader")).toMatchObject({ state: "idle", status: "idle", last_text: "Done." });
    // a summary row says "none" by leaving a marker out: the failed turn before is no longer this one's
    expect(conversation("leader")?.turn_error).toBeUndefined();
    expect(conversation("leader")?.turn_error_kind).toBeUndefined();
    expect(conversation("main")?.state).toBe("working"); // not in the change: as it was
    expect(assistantReads).toBe(1); // a listed conversation's change is laid over it: the assistant is not read again
  });

  it("the detail kept of a conversation takes the change and keeps what a summary never carries", async () => {
    await listMode();
    applyCompleteSessions([working("leader", { queued: ["next"], background: [{ id: "bg", kind: "shell", description: "watch" }] }), row("agent", { state: "working", status: "working" })]);
    changed = [row("leader", { assistant: true, waiting: false }), row("agent", { waiting: false })];
    Socket.last.push({ type: "sessions_list", revision: "r2" });
    await settle();
    await settle();
    expect(kept("leader")).toMatchObject({ state: "idle", queued: ["next"], background: [{ id: "bg" }] });
    // an Agent's kept detail is not the assistant's: its own chat reads it (summaries never replace a chat's detail)
    expect(kept("agent")?.state).toBe("working");
  });

  it("rows of another machine or not among the conversations change nothing", async () => {
    await listMode();
    const before = getState().assistant;
    changed = [row("leader", { machine_id: "laptop", waiting: false }), row("stranger", { waiting: false }),
      // an assistant conversation of another machine's, and a new Agent: neither is this assistant's to read about
      row("elsewhere", { machine_id: "laptop", assistant: true, origin: "lark_agent", waiting: false }),
      row("agent", { assistant: false, waiting: false })];
    Socket.last.push({ type: "sessions_list", revision: "r2" });
    await settle();
    await settle();
    expect(getState().assistant).toBe(before);
    expect(assistantReads).toBe(1);
  });
});

// #10190: a conversation another client makes while the page is open (an integration's POST
// /api/v1/assistant/conversations, a chat started on the phone) reaches Recent without a reload. The list's row of it has
// the assistant read again, once for the conversation, and the answer brings it with everything it gives one.
describe("a conversation made elsewhere comes into Recent (#10190)", () => {
  const lark = (extra: Partial<Session> = {}) => row("lark", { assistant: true, origin: "lark_agent", manager: "m-1", updated_at: "2026-11-16T05:01:00Z", ...extra });

  /** A hint and the list's read after it; past the 2 s window the last read opened, so each hint reads. */
  async function hint(revision: string) {
    Socket.last.push({ type: "sessions_list", revision });
    await vi.advanceTimersByTimeAsync(2_000);
    await settle();
    await settle();
  }

  it("an integration's new conversation has the assistant read again and shows in Recent", async () => {
    await listMode();
    expect(recent()).toEqual(["leader"]);
    // the hub knows it now: the next answer lists it, with what the list's row lacks (its whole detail)
    conversations = [...conversations, lark({ title: "Weekly report", queued: ["next"] })];
    changed = [lark({ title: "Weekly report", waiting: false })];
    await hint("r2");
    expect(assistantReads).toBe(2);
    expect(recent()).toEqual(["lark", "leader"]);
    expect(conversation("lark")).toMatchObject({ title: "Weekly report", origin: "lark_agent", manager: "m-1", queued: ["next"] });
  });

  // review of #10199: the hub can list a new conversation a moment after its row comes
  it("asks again at its next rows while the answer does not list it, three reads in all", async () => {
    await listMode();
    for (const [i, reads] of [[2, 2], [3, 3], [4, 4], [5, 4], [6, 4]]) {
      changed = [lark({ state: i % 2 ? "working" : "idle", status: i % 2 ? "working" : "idle", waiting: false })];
      await hint(`r${i}`);
      expect(assistantReads).toBe(reads);
    }
    expect(recent()).toEqual(["leader"]);
  });

  it("once the answer lists it, its later rows read nothing more", async () => {
    await listMode();
    conversations = [...conversations, lark()];
    changed = [lark({ waiting: false })];
    await hint("r2");
    expect(assistantReads).toBe(2);
    expect(recent()).toEqual(["lark", "leader"]);
    changed = [lark({ state: "working", status: "working", waiting: false })];
    await hint("r3");
    expect(assistantReads).toBe(2);
  });

  // review of #10199: a row that comes while the read is on its way is newer than that read's answer
  it("a row of it that comes while its read is on its way has it read again: archived meanwhile, it leaves Recent", async () => {
    await listMode();
    conversations = [...conversations, lark()];
    let release!: () => void;
    assistantHeld = new Promise<void>((r) => (release = r));
    changed = [lark({ waiting: false })];
    await hint("r2"); // its read waits, with the conversation as the hub had it then
    expect(assistantReads).toBe(2);
    conversations = conversations.map((c) => (c.session_id === "lark" ? { ...c, archived: true } : c));
    changed = [lark({ archived: true, waiting: false })];
    await hint("r3"); // archived on another device
    expect(assistantReads).toBe(2);
    release();
    await settle();
    await settle();
    await settle();
    expect(assistantReads).toBe(3);
    expect(recent()).toEqual(["leader"]);
  });

  // review of #10199: the once-for-each guard was one for every manager's page, and for a read that failed
  it("one whose read failed is asked about again at its next row", async () => {
    await listMode();
    assistantFails = 1;
    conversations = [...conversations, lark()];
    changed = [lark({ waiting: false })];
    await hint("r2");
    expect(assistantReads).toBe(2);
    expect(recent()).toEqual(["leader"]);
    changed = [lark({ state: "working", status: "working", waiting: false })];
    await hint("r3");
    expect(assistantReads).toBe(3);
    expect(recent()).toEqual(["lark", "leader"]);
  });

  it("one whose answer was not taken (a later one was in place) is asked about again at its next row", async () => {
    await listMode();
    let release!: () => void;
    assistantHeld = new Promise<void>((r) => (release = r));
    changed = [lark({ waiting: false })];
    await hint("r2"); // its read waits
    expect(assistantReads).toBe(2);
    await refreshAssistant(); // a later read (a send, opening a chat) is answered first, before the hub listed it
    expect(assistantReads).toBe(3);
    release();
    await settle();
    await settle();
    expect(recent()).toEqual(["leader"]);
    conversations = [...conversations, lark()];
    changed = [lark({ state: "working", status: "working", waiting: false })];
    await hint("r3");
    expect(assistantReads).toBe(4);
    expect(recent()).toEqual(["lark", "leader"]);
  });

  it("another manager's page asks about it for its own answer", async () => {
    await listMode();
    changed = [lark({ waiting: false })]; // the default's page: its answer does not list it
    await hint("r2");
    expect(assistantReads).toBe(2);
    try {
      chooseManager("m-2"); // another manager's page, read before its hub listed the conversation
      await settle();
      await settle();
      expect(assistantReads).toBe(3);
      expect(recent()).toEqual(["leader"]);
      conversations = [...conversations, lark()];
      changed = [lark({ state: "working", status: "working", waiting: false })];
      await hint("r3");
      expect(assistantReads).toBe(4);
      expect(recent()).toEqual(["lark", "leader"]);
    } finally {
      chooseManager(null);
    }
  });

  it("the main conversation and the background supervisor read nothing and stay out of Recent", async () => {
    await listMode();
    changed = [row("main", { assistant: true, waiting: false }), row("bg", { assistant: true, origin: "task", waiting: false })];
    await hint("r2");
    expect(assistantReads).toBe(1);
    expect(recent()).toEqual(["leader"]);
  });

  it("a pushed new conversation does the same on a hub without the list (sessions_delta)", async () => {
    connect();
    Socket.last.push({ type: "hello", features: ["sessions_delta"] });
    await settle();
    await settle();
    expect(assistantReads).toBe(1);
    // a listed conversation's change is laid over it
    Socket.last.push({ type: "sessions_delta", machine_id: "hub", upsert: [row("leader", { assistant: true })], remove: [] });
    await settle();
    expect(conversation("leader")?.state).toBe("idle");
    expect(assistantReads).toBe(1);
    // another machine's and an Agent's read nothing
    Socket.last.push({ type: "sessions_delta", machine_id: "laptop", upsert: [row("elsewhere", { machine_id: "laptop", assistant: true })], remove: [] });
    Socket.last.push({ type: "sessions_delta", machine_id: "hub", upsert: [row("agent", { assistant: false })], remove: [] });
    await settle();
    expect(assistantReads).toBe(1);
    conversations = [...conversations, lark()];
    Socket.last.push({ type: "sessions_delta", machine_id: "hub", upsert: [lark()], remove: [] });
    await settle();
    await settle();
    expect(assistantReads).toBe(2);
    expect(recent()).toEqual(["lark", "leader"]);
    Socket.last.push({ type: "sessions_delta", machine_id: "hub", upsert: [lark({ state: "working", status: "working" })], remove: [] });
    await settle();
    expect(assistantReads).toBe(2);
  });
});
