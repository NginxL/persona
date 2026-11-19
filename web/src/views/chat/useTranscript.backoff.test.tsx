// @vitest-environment jsdom
// A failed older page is not asked for again on every scroll tick: loadEarlier waits 3 s, then twice as long after each
// failure, up to 30 s; a page that comes back clears the wait.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemsPage } from "../../lib/types";
import { useTranscript, type Transcript } from "./useTranscript";

const api = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/api")>(), api }));
vi.mock("../../lib/store", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/store")>(),
  listenSession: () => () => {},
  send: () => {},
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const newest: ItemsPage = { items: [{ seq: 10, role: "user", kind: "text", text: "hi" }], has_more: true, to_seq: 10 } as unknown as ItemsPage;
const older = () => api.mock.calls.filter(([path]) => String(path).includes("before_seq=")).length;

let root: Root;
let host: HTMLDivElement;
const tr: { current: Transcript } = { current: null! };

function Probe({ sid = "s1" }: { sid?: string }) {
  tr.current = useTranscript("hub", sid);
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
