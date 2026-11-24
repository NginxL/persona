// @vitest-environment jsdom
// Sidebar launch handoff (#12380): launching a new Agent with an initial prompt transitions cleanly from the
// "Starting" placeholder directly into "Working" without briefly flashing/counting into "Done unread" ("完成未看")
// in the "Not on a to-do" group.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  const g = globalThis as unknown as { WebSocket: typeof WebSocket; __socks: MockWS[] };
  g.__socks = [];
  class MockWS {
    static OPEN = 1;
    readyState = 0;
    onopen?: () => void;
    onmessage?: (e: { data: string }) => void;
    onclose?: () => void;
    onerror?: () => void;
    sent: string[] = [];
    constructor() { g.__socks.push(this); }
    open() { this.readyState = 1; this.onopen?.(); }
    send(d: string) { this.sent.push(d); }
    push(msg: unknown) { this.onmessage?.({ data: JSON.stringify(msg) }); }
    close() { this.readyState = 3; }
  }
  g.WebSocket = MockWS as unknown as typeof WebSocket;
});

import { deliverLaunch, launchAgent } from "../lib/new-agent";
import { connect, flushPushes, getState, reset } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Sock = { open(): void; push(m: unknown): void };
const lastSock = () => (globalThis as unknown as { __socks: Sock[] }).__socks.at(-1)!;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const at = "2026-11-21T08:00:00Z";
const mac: Machine = { id: "mac", name: "Mac", online: true };
const unassignedSessions = (): Session[] => [
  { machine_id: "mac", session_id: "u-0", title: "Unseen agent", cwd: "/code/app", status: "idle", state: "unread", updated_at: at },
  ...Array.from({ length: 9 }, (_, i) => ({
    machine_id: "mac",
    session_id: `seen-${i + 1}`,
    title: `Seen agent ${i + 1}`,
    cwd: "/code/app",
    status: "idle",
    state: "idle" as const,
    updated_at: at,
  })),
];

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { flushPushes(); await new Promise((r) => setTimeout(r, 0)); });
};
const todoNoneRow = () => host.querySelector('[data-tree-branch="todo:none"] [data-tree-branch-row]')?.textContent ?? "";
const startingSection = () => [...host.querySelectorAll("[data-sidebar-group-heading]")].find((el) => el.textContent?.includes("正在启动"));

beforeEach(() => {
  (globalThis as unknown as { __socks: Sock[] }).__socks.length = 0;
  localStorage.clear();
  localStorage.setItem("persona.token", "tok");
  reset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("Sidebar launch handoff (#12380)", () => {
  it("transitions 'Not on a to-do' directly from 10 Done unread to 11 Working without flashing 11 Done unread", async () => {
    const base = unassignedSessions();
    const created: Session = { machine_id: "mac", session_id: "s-new", title: "Fix issue", cwd: "/code/app", status: "idle", state: "idle", updated_at: at };
    let resolveCreate!: (res: Response) => void;
    let resolveSend!: (res: Response) => void;

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.endsWith("/api/v1/sessions") && method === "POST") return new Promise<Response>((r) => { resolveCreate = r; });
      if (url.endsWith("/api/v1/sessions/mac/s-new/messages") && method === "POST") return new Promise<Response>((r) => { resolveSend = r; });
      if (url.includes("/api/v1/todos")) return json({ todos: [] });
      if (url.includes("/api/v1/assistant")) return json({ machine_id: "mac", main: "main", conversations: [] });
      if (url.includes("/api/v1/machines")) return json({ machines: [mac] });
      if (url.includes("/api/v1/sessions")) return json({ sessions: base });
      return json({});
    }));

    connect();
    const sock = lastSock();
    sock.open();
    sock.push({ type: "hello", server_version: "v1", features: [] });
    sock.push({ type: "machines", machines: [mac] });
    sock.push({ type: "sessions", machine_id: "mac", sessions: base });
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    // Baseline: 10 unassigned agents, with 1 Done unread.
    expect(todoNoneRow()).toMatch(/10 个 Agent完成未看$/);
    expect(startingSection()).toBeUndefined();

    // Phase 1: creating -> shown in Starting, unassigned stays 10 Done unread.
    let launchId = "";
    act(() => { launchId = launchAgent({ machineId: "mac", cwd: "/code/app", launcher: "claude", text: "Fix issue", attachments: [] }, { open: false }); });
    await flush();
    expect(startingSection()).toBeDefined();
    expect(todoNoneRow()).toMatch(/10 个 Agent完成未看$/);

    // Phase 2: POST /sessions returns an idle session while POST /messages is still in flight -> no duplicate in todo:none, no 11 Done unread flash.
    // Delivery loads its module on intent; wait until the real request has installed its resolver before completing it.
    await act(async () => vi.waitFor(() => expect(resolveCreate).toBeTypeOf("function")));
    await act(async () => { resolveCreate(json({ session: created })); });
    await flush();
    expect(getState().launches[launchId].phase).toBe("sending");
    expect(startingSection()).toBeDefined();
    expect(todoNoneRow()).toMatch(/10 个 Agent完成未看$/);

    // Phase 3: POST /messages succeeds -> phase becomes ready while hub has not yet pushed working (and even if a transient unread arrives with no reply).
    await act(async () => vi.waitFor(() => expect(resolveSend).toBeTypeOf("function")));
    await act(async () => { resolveSend(json({ ok: true })); });
    await flush();
    expect(getState().launches[launchId].phase).toBe("ready");
    expect(startingSection()).toBeUndefined();
    expect(todoNoneRow()).toMatch(/11 个 Agent工作中$/);
    expect(todoNoneRow()).not.toContain("完成未看");

    // Even if a transient hub push reports idle/unread with no reply before working arrives, it stays Working.
    act(() => sock.push({ type: "sessions", machine_id: "mac", sessions: [...base, { ...created, state: "unread", status: "idle" }] }));
    await flush();
    expect(todoNoneRow()).toMatch(/11 个 Agent工作中$/);

    // Phase 4: Hub confirms working -> stays Working and settles handoffAt.
    act(() => sock.push({ type: "sessions", machine_id: "mac", sessions: [...base, { ...created, state: "working", status: "working" }] }));
    await flush();
    expect(todoNoneRow()).toMatch(/11 个 Agent工作中$/);
    expect(getState().launches[launchId].handoffAt).toBeUndefined();

    // Phase 5: Turn finishes -> transitions cleanly from Working to Done unread.
    act(() => sock.push({ type: "sessions", machine_id: "mac", sessions: [...base, { ...created, state: "unread", status: "idle", last_text: "Fixed." }] }));
    await flush();
    expect(todoNoneRow()).toMatch(/11 个 Agent完成未看$/);
  });

  it("does not hold an empty-prompt launch or failed send in Working state", async () => {
    const base = unassignedSessions();
    const created: Session = { machine_id: "mac", session_id: "s-empty", title: "Empty", cwd: "/code/app", status: "idle", state: "idle", updated_at: at };
    const failedSession: Session = { machine_id: "mac", session_id: "s-fail", title: "Failed", cwd: "/code/app", status: "idle", state: "idle", updated_at: at };
    let postCount = 0;

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.endsWith("/api/v1/sessions") && method === "POST") {
        postCount++;
        return json({ session: postCount === 1 ? created : failedSession });
      }
      if (url.endsWith("/api/v1/sessions/mac/s-fail/messages") && method === "POST") {
        return json({ code: "input_draft", error: "draft", delivered: "none" }, 409);
      }
      if (url.includes("/api/v1/todos")) return json({ todos: [] });
      if (url.includes("/api/v1/assistant")) return json({ machine_id: "mac", main: "main", conversations: [] });
      if (url.includes("/api/v1/machines")) return json({ machines: [mac] });
      if (url.includes("/api/v1/sessions")) return json({ sessions: base });
      return json({});
    }));

    connect();
    const sock = lastSock();
    sock.open();
    sock.push({ type: "hello", server_version: "v1", features: [] });
    sock.push({ type: "machines", machines: [mac] });
    sock.push({ type: "sessions", machine_id: "mac", sessions: base });
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    // Empty-prompt start: no first turn was sent, so it is not forced into Working.
    let emptyId = "";
    act(() => { emptyId = launchAgent({ machineId: "mac", cwd: "/code/app", launcher: "claude", text: "", attachments: [] }, { open: false }); });
    await act(async () => { await deliverLaunch(emptyId); });
    await flush();
    expect(getState().launches[emptyId].phase).toBe("ready");
    expect(getState().launches[emptyId].handoffAt).toBeUndefined();
    expect(todoNoneRow()).toMatch(/11 个 Agent完成未看$/);

    // Failed first-message send: handoffAt is cleared, never stuck in Working.
    let failId = "";
    act(() => { failId = launchAgent({ machineId: "mac", cwd: "/code/app", launcher: "claude", text: "Run checks", attachments: [] }, { open: false }); });
    await act(async () => { await deliverLaunch(failId); });
    await flush();
    expect(getState().launches[failId].phase).toBe("failed");
    expect(getState().launches[failId].handoffAt).toBeUndefined();
    expect(todoNoneRow()).not.toContain("工作中");
  });
});
