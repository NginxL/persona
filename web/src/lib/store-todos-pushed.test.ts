// To-do pushes arrive in bursts. The web settles a short window, checks the hub-wide revision while one is available,
// and reads the complete list only when that revision changed. Hidden pages wait for visibility; old hubs keep the
// original full-read behavior.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, getState, refreshTodos, reset, todosPushed } from "./store";

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
  send() {}
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

const todo = (title: string) => ({ id: "t1", title, created_at: "", updated_at: "", spaces: [] });
const settle = async () => {
  await vi.dynamicImportSettled();
  await vi.advanceTimersByTimeAsync(0);
};
const pushedSettle = async () => {
  await settle(); // the lazy push module's import, before its 200 ms timer
  await vi.advanceTimersByTimeAsync(200);
};

let reads: ((title: string) => void)[] = [];
let revision = 1;
let revisionSupported = true;
let failFull = false;
let holdRevision = false;
let heldRevisionReads: (() => void)[] = [];

async function answerAll(title: string) {
  while (reads.length) {
    reads.shift()!(title);
    await settle();
  }
}

async function online() {
  connect();
  const s = FakeSocket.all[FakeSocket.all.length - 1];
  s.open();
  s.push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 1, features: revisionSupported ? ["todo_revision"] : [] });
  await settle();
  await answerAll("from hello");
  return s;
}

beforeEach(() => {
  vi.useFakeTimers();
  FakeSocket.all = [];
  reads = [];
  revision = 1;
  revisionSupported = true;
  failFull = false;
  holdRevision = false;
  heldRevisionReads = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("location", { protocol: "http:", host: "hub" });
  vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal("window", { setTimeout: (f: () => void, ms: number) => setTimeout(f, ms), clearTimeout: (t: number) => clearTimeout(t) });
  vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://hub").pathname;
    if (path.endsWith("/todos/revision")) {
      if (!revisionSupported) return new Response("not found", { status: 404 });
      if (holdRevision) await new Promise<void>((resolve) => heldRevisionReads.push(() => resolve()));
      const etag = `W/"r${revision}"`;
      const headers = { ETag: etag, "X-Persona-Todos-Revision": etag };
      if (new Headers(init?.headers).get("If-None-Match") === etag) return new Response(null, { status: 304, headers });
      return new Response(JSON.stringify({ revision: etag }), { status: 200, headers });
    }
    if (!path.endsWith("/todos")) return new Response("{}", { status: 200 });
    if (failFull) {
      failFull = false;
      return new Response(JSON.stringify({ error: "down" }), { status: 503 });
    }
    const title = await new Promise<string>((r) => reads.push(r));
    const etag = `W/"r${revision}"`;
    return new Response(JSON.stringify({ todos: [todo(title)] }), {
      status: 200,
      headers: { ETag: `W/"body${revision}"`, "X-Persona-Todos-Revision": etag },
    });
  }));
});

afterEach(() => {
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const calls = (suffix: string) => vi.mocked(fetch).mock.calls.filter(([url]) => new URL(String(url), "http://hub").pathname.endsWith(suffix));
const todoReads = () => calls("/todos").length;
const revisionReads = () => calls("/todos/revision").length;

describe("to-do pushes", () => {
  it("settles a burst into one unchanged revision read and no full list", async () => {
    const s = await online();
    const fullBefore = todoReads();
    const revisionBefore = revisionReads();
    for (let i = 0; i < 10; i++) s.push({ type: "todos" });
    await settle(); // load the push module before measuring its settle window
    await vi.advanceTimersByTimeAsync(199);
    expect(revisionReads() - revisionBefore).toBe(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(revisionReads() - revisionBefore).toBe(1);
    expect(todoReads() - fullBefore).toBe(0);
    expect(getState().todos.map((t) => t.title)).toEqual(["from hello"]);
  });

  it("reads the full list once when the revision changed", async () => {
    const s = await online();
    const before = todoReads();
    revision++;
    for (let i = 0; i < 10; i++) s.push({ type: "todos" });
    await pushedSettle();
    expect(todoReads() - before).toBe(1);
    await answerAll("after the burst");
    expect(getState().todos.map((t) => t.title)).toEqual(["after the burst"]);
    expect(todoReads() - before).toBe(1);
  });

  it("a push during the revision check is checked again after it", async () => {
    const s = await online();
    const before = revisionReads();
    holdRevision = true;
    s.push({ type: "todos" });
    await pushedSettle();
    expect(revisionReads() - before).toBe(1);
    s.push({ type: "todos" });
    heldRevisionReads.shift()!();
    await settle();
    await pushedSettle();
    expect(revisionReads() - before).toBe(2);
    heldRevisionReads.shift()!();
    await settle();
    expect(todoReads()).toBe(1); // only hello's full read
  });

  it("what the page changed itself reads at once without waiting for the push window", async () => {
    await online();
    const before = todoReads();
    const own = refreshTodos();
    await settle();
    expect(todoReads() - before).toBe(1);
    await answerAll("after my change");
    await own;
    expect(getState().todos.map((t) => t.title)).toEqual(["after my change"]);
  });

  it("a hidden page does not read; becoming visible reconciles once", async () => {
    const s = await online();
    vi.stubGlobal("document", { visibilityState: "hidden" });
    const fullBefore = todoReads();
    const revisionBefore = revisionReads();
    s.push({ type: "todos" });
    await pushedSettle();
    expect(todoReads() - fullBefore).toBe(0);
    expect(revisionReads() - revisionBefore).toBe(0);

    vi.stubGlobal("document", { visibilityState: "visible" });
    todosPushed(); // the production visibilitychange listener calls this same entry point
    await pushedSettle();
    expect(revisionReads() - revisionBefore).toBe(1);
    expect(todoReads() - fullBefore).toBe(0);
  });

  it("an old hub falls back to the original full read", async () => {
    revisionSupported = false;
    const s = await online();
    const before = todoReads();
    s.push({ type: "todos" });
    await pushedSettle();
    expect(revisionReads()).toBe(0);
    expect(todoReads() - before).toBe(1);
    await answerAll("old hub");
    expect(getState().todos.map((t) => t.title)).toEqual(["old hub"]);
  });

  it("a mixed fleet's 409 falls back to the complete list", async () => {
    const s = await online();
    const before = todoReads();
    vi.mocked(fetch).mockImplementationOnce(async () => new Response(JSON.stringify({ error: "old manager" }), { status: 409 }));
    s.push({ type: "todos" });
    await pushedSettle();
    expect(revisionReads()).toBe(1);
    expect(todoReads() - before).toBe(1);
    await answerAll("after fallback");
    expect(getState().todos.map((t) => t.title)).toEqual(["after fallback"]);
  });

  it("a failed changed read still lets the next push recover", async () => {
    const s = await online();
    const before = todoReads();
    revision++;
    failFull = true;
    s.push({ type: "todos" });
    await pushedSettle();
    await settle();
    expect(getState().todosError).toBeTruthy();
    s.push({ type: "todos" });
    await pushedSettle();
    await answerAll("back");
    expect(todoReads() - before).toBe(2);
    expect(getState().todosError).toBeNull();
    expect(getState().todos.map((t) => t.title)).toEqual(["back"]);
  });

  it("reset cancels a queued pushed read and its old validator", async () => {
    const s = await online();
    const before = revisionReads();
    s.push({ type: "todos" });
    reset();
    await pushedSettle();
    expect(revisionReads() - before).toBe(0);
    expect(todoReads()).toBe(1);
  });
});
