// Messages held for an Agent while its computer was away (#10661): the row's `undelivered` list, as the store keeps it,
// and where Retry and Delete go. The hub's whole row is the list; a change built from an older copy of the row, or a
// node's row (which never carries the list), keeps it. A Persona chat's row is merged as before (#10194).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyCompleteSessions, connect, getSession, getState, reset, upsertSession } from "./store";
import { queueToSession } from "./outbox";
import { showKept } from "./undelivered";
import { deleteUndelivered, retryUndelivered } from "./undelivered-actions";
import type { Session, UndeliveredMessage } from "./types";

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor() {
    FakeSocket.all.push(this);
  }
  sent: Record<string, unknown>[] = [];
  send(data: string) {
    this.sent.push(JSON.parse(data) as Record<string, unknown>);
  }
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  push(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
}

function online() {
  connect();
  const s = FakeSocket.all[FakeSocket.all.length - 1];
  s.open();
  s.push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 1 });
  return s;
}

const held = (id: string, extra: Partial<UndeliveredMessage> = {}): UndeliveredMessage => ({
  id, text: `Synthetic message ${id}`, at: "2026-11-19T08:00:00Z", state: "waiting", error: "Will send when Laptop is back", computer: "Laptop", ...extra,
});
const agent = (extra: Partial<Session> = {}): Session => ({ machine_id: "m1", session_id: "s1", title: "Agent", cwd: "/code", status: "unknown", ...extra });
const ids = () => getSession("m1", "s1")?.undelivered?.map((m) => m.id);

let calls: { url: string; method: string; features: string | null }[];
let answer: (url: string) => Response;
beforeEach(() => {
  answer = () => new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } });
  vi.useFakeTimers();
  FakeSocket.all = [];
  calls = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("location", { protocol: "http:", host: "hub" });
  vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal("window", { setTimeout: (f: () => void, ms: number) => setTimeout(f, ms), clearTimeout: (t: number) => clearTimeout(t) });
  vi.stubGlobal("fetch", vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), method: (init?.method ?? "GET").toUpperCase(), features: new Headers(init?.headers).get("X-Persona-Features") });
    return Promise.resolve(answer(String(input)));
  }));
});
afterEach(() => {
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("an Agent row's held messages in the store", () => {
  it("keeps them through a change made from an older copy of the row, or from a node's row", () => {
    const before = agent();
    applyCompleteSessions([before]);
    showKept("m1", "s1", held("k1"));
    expect(ids()).toEqual(["k1"]);
    // a rename built from the copy the page had before the message was held
    upsertSession({ ...before, title: "Renamed" });
    expect(getSession("m1", "s1")?.title).toBe("Renamed");
    expect(ids()).toEqual(["k1"]);
    // one built from a copy with an older list
    upsertSession({ ...before, undelivered: [], title: "Renamed again" });
    expect(ids()).toEqual(["k1"]);
    // a node's row (a takeover's, a start's answer): no list at all
    upsertSession({ machine_id: "m1", session_id: "s1", title: "From the node", cwd: "/code", status: "idle" });
    expect(ids()).toEqual(["k1"]);
    expect(getSession("m1", "s1")?.status).toBe("idle");
  });

  it("takes the hub's whole row as the list: a pushed row without one has none left", () => {
    const socket = online();
    applyCompleteSessions([agent({ undelivered: [held("k1"), held("k2")] })]);
    socket.push({ type: "session", session: agent({ undelivered: [held("k2")] }) });
    expect(ids()).toEqual(["k2"]);
    socket.push({ type: "session", session: agent({ title: "Delivered" }) });
    expect(getSession("m1", "s1")?.title).toBe("Delivered");
    expect(getSession("m1", "s1")?.undelivered).toBeUndefined();
  });

  it("a complete snapshot replaces the row, its list with it", () => {
    applyCompleteSessions([agent({ undelivered: [held("k1")] })]);
    applyCompleteSessions([agent({ title: "Snapshot" })]);
    expect(getSession("m1", "s1")?.undelivered).toBeUndefined();
  });

  it("leaves a Persona chat's row merged as before (#10194)", () => {
    const kept = held("a1", { computer: undefined, error: "The assistant could not start" });
    applyCompleteSessions([agent({ assistant: true, undelivered: [kept] })]);
    // the hub's row without the list: none left
    upsertSession(agent({ assistant: true, title: "Persona" }));
    expect(getSession("m1", "s1")?.undelivered).toBeUndefined();
    applyCompleteSessions([agent({ assistant: true, undelivered: [kept] })]);
    // a partial row without the assistant mark keeps what the spread keeps
    upsertSession({ machine_id: "m1", session_id: "s1", title: "Partial", cwd: "/code", status: "idle" });
    expect(ids()).toEqual(["a1"]);
    upsertSession({ machine_id: "m1", session_id: "s1", title: "Partial", cwd: "/code", status: "idle", undelivered: [] });
    expect(ids()).toEqual([]);
  });
});

describe("what the page declares", () => {
  it("lists agent_message_hold in client_hello", () => {
    const socket = online();
    const hello = socket.sent.find((m) => m.type === "client_hello");
    expect(hello?.features).toContain("agent_message_hold");
  });
});

describe("a send from another page (lib/outbox.ts)", () => {
  it("held by the hub, leaves the outbox and shows on the Agent's row at once", async () => {
    applyCompleteSessions([agent()]);
    answer = (url) => url.endsWith("/messages")
      ? new Response(JSON.stringify({ delivered: "later", undelivered: held("k1") }), { status: 202, headers: { "Content-Type": "application/json" } })
      : new Response("{}", { status: 200 });
    const r = queueToSession("m1", "s1", { text: "Synthetic message k1" });
    await expect(r.sent).resolves.toBe(true);
    // the send declares the feature itself: a page whose socket is down has no client_hello for the hub to go by
    expect(calls.find((c) => c.url.endsWith("/messages"))).toEqual({ url: "/api/v1/sessions/m1/s1/messages", method: "POST", features: "agent_message_hold" });
    expect(getState().outbox["m1/s1"] ?? []).toEqual([]);
    expect(ids()).toEqual(["k1"]);
    expect(getSession("m1", "s1")?.undelivered?.[0].computer).toBe("Laptop");
  });
});

describe("Retry and Delete", () => {
  it("go to the session's held routes for a held message, and change the row", async () => {
    applyCompleteSessions([agent({ machine_id: "m 1", session_id: "s/1", undelivered: [held("k/1", { state: "failed", error: "Not sent: Laptop was away for 24 hours" })] })]);
    await retryUndelivered("m 1", "s/1", { id: "k/1", computer: "Laptop" });
    expect(calls.at(-1)).toMatchObject({ url: "/api/v1/sessions/m%201/s%2F1/held/k%2F1/retry", method: "POST" });
    expect(getSession("m 1", "s/1")?.undelivered).toEqual([expect.objectContaining({ id: "k/1", state: "waiting", error: undefined })]);
    await deleteUndelivered("m 1", "s/1", { id: "k/1", computer: "Laptop" });
    expect(calls.at(-1)).toMatchObject({ url: "/api/v1/sessions/m%201/s%2F1/held/k%2F1", method: "DELETE" });
    expect(getSession("m 1", "s/1")?.undelivered).toEqual([]);
  });

  it("go to the assistant's routes for a Persona chat's kept message, as before", async () => {
    applyCompleteSessions([agent({ assistant: true, undelivered: [held("a1", { computer: undefined, state: "failed" })] })]);
    await retryUndelivered("m1", "s1", { id: "a1" });
    expect(calls.at(-1)).toMatchObject({ url: "/api/v1/assistant/conversations/s1/undelivered/a1/retry", method: "POST" });
    await deleteUndelivered("m1", "s1", { id: "a1" });
    expect(calls.at(-1)).toMatchObject({ url: "/api/v1/assistant/conversations/s1/undelivered/a1", method: "DELETE" });
    expect(getSession("m1", "s1")?.undelivered).toEqual([]);
  });
});
