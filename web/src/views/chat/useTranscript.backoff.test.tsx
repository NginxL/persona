// @vitest-environment jsdom
// A failed older page is not asked for again on every scroll tick: loadEarlier waits 3 s, then twice as long after each
// failure, up to 30 s; a page that comes back clears the wait.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/api";
import type { ItemsEvent } from "../../lib/store";
import type { ItemsPage } from "../../lib/types";
import { useTranscript, type Transcript } from "./useTranscript";

const api = vi.hoisted(() => vi.fn());
const session = vi.hoisted(() => ({
  reconnected: undefined as (() => void) | undefined,
  items: undefined as ((event: ItemsEvent) => void) | undefined,
}));
vi.mock("../../lib/api", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/api")>(), api }));
vi.mock("../../lib/store", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/store")>(),
  listenSession: (_mid: string, _sid: string, handlers: { reconnected?: () => void; items?: (event: ItemsEvent) => void }) => {
    session.reconnected = handlers.reconnected;
    session.items = handlers.items;
    return () => {};
  },
  send: () => {},
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const newest: ItemsPage = { items: [{ seq: 10, role: "user", kind: "text", text: "hi" }], has_more: true, to_seq: 10 } as unknown as ItemsPage;
const older = () => api.mock.calls.filter(([path]) => String(path).includes("before_seq=")).length;

let root: Root;
let host: HTMLDivElement;
const tr: { current: Transcript } = { current: null! };

function Probe({ sid = "s1", starting = false }: { sid?: string; starting?: boolean }) {
  tr.current = useTranscript("hub", sid, { starting });
  return null;
}

async function loadEarlier() {
  await act(async () => {
    await tr.current.loadEarlier();
  });
}

beforeEach(async () => {
  vi.useFakeTimers();
  api.mockReset();
  api.mockImplementation(async (path: string) => {
    if (path.includes("before_seq=")) throw new Error("hub timed out");
    return newest;
  });
  host = document.createElement("div");
  root = createRoot(host);
  await act(async () => root.render(<Probe />));
  expect(tr.current.items).toHaveLength(1);
  expect(tr.current.hasMore).toBe(true);
});

afterEach(() => {
  act(() => root.unmount());
  vi.useRealTimers();
});

describe("loading an older page that fails", () => {
  it("waits before asking again instead of asking on every call", async () => {
    await loadEarlier();
    await loadEarlier();
    await loadEarlier();
    expect(older()).toBe(1);
    expect(tr.current.error).toBe("hub timed out"); // kept for the caller to show

    await act(async () => void vi.advanceTimersByTime(3000));
    await loadEarlier();
    expect(older()).toBe(2);
  });

  it("doubles the wait after each failure, up to 30 s", async () => {
    const waits: number[] = [];
    for (let i = 0; i < 6; i++) {
      await loadEarlier();
      const asked = older();
      let waited = 0;
      while (older() === asked) {
        await act(async () => void vi.advanceTimersByTime(1000));
        waited += 1000;
        await loadEarlier();
      }
      waits.push(waited);
    }
    expect(waits).toEqual([3000, 6000, 12000, 24000, 30000, 30000]);
  });

  it("asks at once again after a page that comes back", async () => {
    await loadEarlier();
    await act(async () => void vi.advanceTimersByTime(3000));
    api.mockImplementation(async (path: string) => {
      if (path.includes("before_seq=")) return { items: [{ seq: 5, role: "user", kind: "text", text: "older" }], has_more: true };
      return newest;
    });
    await loadEarlier();
    expect(older()).toBe(2);
    await loadEarlier();
    expect(older()).toBe(3);
  });

  it("joins a retry requested by the rail during backoff", async () => {
    await loadEarlier();
    api.mockImplementation(async (path: string) => path.includes("before_seq=")
      ? { items: [{ id: "older", seq: 5, role: "user", kind: "text", text: "older" }], has_more: false }
      : newest);
    let first!: Promise<symbol | undefined>;
    let second!: Promise<symbol | undefined>;
    act(() => {
      first = tr.current.loadEarlier({ waitForRetry: true });
      second = tr.current.loadEarlier({ waitForRetry: true });
    });
    expect(second).toBe(first);
    vi.advanceTimersByTime(2999);
    expect(older()).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(await first).toBe(tr.current.earlierPage);
    expect(older()).toBe(2);
    expect(tr.current.items[0].seq).toBe(5);
    expect(tr.current.hasMore).toBe(false);
  });

  it("cancels a rail retry when its conversation changes", async () => {
    await loadEarlier();
    let pending!: Promise<symbol | undefined>;
    act(() => { pending = tr.current.loadEarlier({ waitForRetry: true }); });
    await act(async () => root.render(<Probe sid="s2" />));
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(await pending).toBeUndefined();
    expect(older()).toBe(1);
    expect(tr.current.error).toBe("");
  });

  it("asks at once in another session", async () => {
    await loadEarlier();
    await act(async () => root.render(<Probe sid="s2" />));
    expect(tr.current.items).toHaveLength(1);
    await loadEarlier();
    expect(older()).toBe(2);
  });
});

// #11450: once the latest page said its computer can't find this history, no older page is read. The node answers an
// older page of such a session with an empty one (released apps ask again on an error), which would end paging, draw
// the chat's start above what is cached and take the error away.
describe("a history its computer can't find", () => {
  const missingHistory = () => new ApiError(404, "Persona can't find its history", {
    error: "Persona can't find its history", code: "not_found", reason: "no_transcript",
    detail: "transcript for session s1 not found",
  });
  const olderPage = () => ({ items: [{ id: "older#0", seq: 5, role: "user", kind: "text", text: "older" }], has_more: true });

  it("does not start an older-page read while a latest-page retry is unanswered", async () => {
    let latest!: (page: ItemsPage) => void;
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? Promise.resolve(olderPage())
      : new Promise<ItemsPage>((resolve) => { latest = resolve; }));
    await act(async () => { tr.current.reload(); await Promise.resolve(); });
    await loadEarlier();
    expect(older()).toBe(0);

    await act(async () => { latest(newest); await Promise.resolve(); });
    await loadEarlier();
    expect(older()).toBe(1);
  });

  it("keeps an in-flight older page fenced during a reconnect until the latest page succeeds", async () => {
    let answerOlder!: (page: ItemsPage) => void;
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? new Promise<ItemsPage>((resolve) => { answerOlder = resolve; })
      : Promise.reject(missingHistory()));
    let read!: Promise<symbol | undefined>;
    act(() => { read = tr.current.loadEarlier(); });
    await act(async () => { tr.current.reload(); await Promise.resolve(); });
    const message = tr.current.error;
    expect(message).not.toBe("");

    let latest!: (page: ItemsPage) => void;
    let latestRequested = false;
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? Promise.resolve(olderPage())
      : new Promise<ItemsPage>((resolve) => { latest = resolve; latestRequested = true; }));
    await act(async () => { session.reconnected?.(); await Promise.resolve(); });
    expect(latestRequested).toBe(true);
    await act(async () => { answerOlder({ items: [], has_more: false }); await read; });
    expect({ error: tr.current.error, hasMore: tr.current.hasMore }).toEqual({ error: message, hasMore: true });
    await loadEarlier();
    expect(older()).toBe(1);

    await act(async () => { latest(newest); await Promise.resolve(); });
    expect(tr.current.error).toBe("");
    await loadEarlier();
    expect(older()).toBe(2);
  });

  it("does not apply an in-flight empty older page after the latest page reports missing history", async () => {
    let answerOlder!: (page: ItemsPage) => void;
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? new Promise<ItemsPage>((resolve) => { answerOlder = resolve; })
      : Promise.reject(missingHistory()));
    let read!: Promise<symbol | undefined>;
    act(() => { read = tr.current.loadEarlier(); });
    expect(older()).toBe(1);
    await act(async () => { tr.current.reload(); await Promise.resolve(); });
    const message = tr.current.error;
    expect(message).not.toBe("");

    await act(async () => { answerOlder({ items: [], has_more: false }); await read; });
    expect({ error: tr.current.error, hasMore: tr.current.hasMore }).toEqual({ error: message, hasMore: true });
  });

  it("keeps the missing-history error and older-page fence until a retry succeeds", async () => {
    api.mockImplementation(async () => { throw missingHistory(); });
    await act(async () => { tr.current.reload(); });
    const message = tr.current.error;
    expect(message).not.toBe("");

    let latest!: (page: ItemsPage) => void;
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? Promise.resolve(olderPage())
      : new Promise<ItemsPage>((resolve) => { latest = resolve; }));
    await act(async () => { tr.current.reload(); await Promise.resolve(); });
    expect({ error: tr.current.error, hasMore: tr.current.hasMore }).toEqual({ error: message, hasMore: true });
    await loadEarlier();
    expect(older()).toBe(0);

    await act(async () => { latest(newest); await Promise.resolve(); });
    expect(tr.current.error).toBe("");
    await loadEarlier();
    expect(older()).toBe(1);
  });

  it("unblocks older pages after an authoritative socket replay", async () => {
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? Promise.resolve(olderPage())
      : Promise.reject(new ApiError(404, "runtime away", { code: "not_found", detail: "runtime away" })));
    await act(async () => { tr.current.reload(); });
    expect(tr.current.error).not.toBe("");

    act(() => { session.items?.({ machine_id: "hub", session_id: "s1", items: newest.items, has_more: true, to_seq: 10 } as ItemsEvent); });
    expect(tr.current.error).toBe("");
    await loadEarlier();
    expect(older()).toBe(1);
  });

  it("unblocks older pages when a replacement replay recovers a failed read", async () => {
    api.mockResolvedValue({ ...newest, history: "v1" });
    await act(async () => { tr.current.reload(); });
    let failLatest!: (error: Error) => void;
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? Promise.resolve(olderPage())
      : new Promise<ItemsPage>((_resolve, reject) => { failLatest = reject; }));
    act(() => { session.items?.({ machine_id: "hub", session_id: "s1", history: "v2", items: [] } as ItemsEvent); });
    act(() => { session.items?.({ machine_id: "hub", session_id: "s1", history: "v2", items: newest.items, has_more: true, to_seq: 10 } as ItemsEvent); });
    await act(async () => { failLatest(new Error("offline")); await Promise.resolve(); });
    expect(tr.current.error).toBe("");

    await loadEarlier();
    expect(older()).toBe(1);
  });

  it("unblocks older pages after a transient read error while starting", async () => {
    await act(async () => root.render(<Probe sid="s2" starting />));
    api.mockImplementation((path: string) => path.includes("before_seq=")
      ? Promise.resolve(olderPage())
      : Promise.reject(new Error("offline")));
    await act(async () => { tr.current.reload(); });
    await act(async () => root.render(<Probe sid="s2" starting={false} />));

    await loadEarlier();
    expect(older()).toBe(1);
  });

  it("reads no older page and keeps the error", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.includes("before_seq=")) return { items: [], has_more: false };
      throw new ApiError(404, "Persona can't find its history", { error: "Persona can't find its history", code: "not_found",
        reason: "no_transcript", detail: "transcript for session s1 not found" });
    });
    await act(async () => { tr.current.reload(); });
    expect(tr.current.error).not.toBe("");
    await loadEarlier();
    expect(older()).toBe(0);
    expect(tr.current.error).not.toBe("");
    expect(tr.current.hasMore).toBe(true);
  });
});
