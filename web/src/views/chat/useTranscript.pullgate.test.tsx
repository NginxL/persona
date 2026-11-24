// @vitest-environment jsdom
// The stale-page safety net on a slow link (#13677): an ask that fails is asked again later each time, never twice at
// once for one session across its open views, and not at all for an archived session.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { listenSession } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";
import { ApiError } from "../../lib/api";

const calls: string[] = [];
type Listener = Parameters<typeof listenSession>[2];
let mode: "fail" | "hang" | "ok" = "fail";

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn(async (path: string): Promise<ItemsPage> => {
    calls.push(path);
    if (path.includes("after_seq=")) {
      if (mode === "fail") throw new ApiError(502, "the computer did not answer in time", { code: "timeout" });
      if (mode === "hang") return new Promise<ItemsPage>(() => {});
      return { items: [item(101)], to_seq: 101 } as ItemsPage;
    }
    return { items: [item(100)], to_seq: 100 } as ItemsPage;
  }),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  send: vi.fn(),
  listenSession: (_mid: string, _sid: string, _l: Listener) => () => {},
}));

const { useTranscript, resetStalePullsForTests } = await import("./useTranscript");
const { noteArchived } = await import("../../lib/agent-archives");

function item(seq: number): Item {
  return { id: `i${seq}`, seq, role: "assistant", kind: "text", text: `row ${seq}`, ts: "2026-11-06T00:00:00Z" } as Item;
}

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function Probe({ sid, rev, readOnly }: { sid: string; rev?: number; readOnly?: boolean }) {
  useTranscript("m1", sid, { rev, readOnly });
  return null;
}

let root: Root;
beforeEach(() => {
  vi.useFakeTimers();
  calls.length = 0;
  mode = "fail";
  resetStalePullsForTests();
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root.unmount());
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const pulls = () => calls.filter((c) => c.includes("after_seq=")).length;
const wait = async (ms: number) => {
  for (let t = 0; t < ms; t += 1000) await act(async () => void vi.advanceTimersByTime(1000)); // asks settle in between
};

describe("the stale pull on a slow link (#13677)", () => {
  it("asks less and less often while its asks fail", async () => {
    act(() => root.render(<Probe sid="slow" rev={100} />));
    await act(async () => {});
    act(() => root.render(<Probe sid="slow" rev={110} />)); // the list moved on; no push came
    const at: number[] = [];
    for (let t = 0; t < 10 * 60_000; t += 1000) {
      const before = pulls();
      await wait(1000);
      if (pulls() > before) at.push(t);
    }
    // Before the gate it asked every 10 s for as long as asks failed: 60 in ten minutes.
    expect(at.length).toBeGreaterThan(3);
    expect(at.length).toBeLessThanOrEqual(10);
    const gaps = at.slice(1).map((t, i) => t - at[i]);
    expect(gaps[gaps.length - 1]).toBeGreaterThanOrEqual(gaps[0] * 4); // backing off
    expect(Math.max(...gaps)).toBeLessThanOrEqual(5 * 60_000 + 2000); // and capped
  });

  it("starts over once an ask works", async () => {
    act(() => root.render(<Probe sid="recovers" rev={100} />));
    await act(async () => {});
    act(() => root.render(<Probe sid="recovers" rev={110} />));
    await wait(60_000);
    const failed = pulls();
    expect(failed).toBeGreaterThan(1);
    mode = "ok";
    await wait(5 * 60_000);
    const after = pulls();
    expect(after).toBe(failed + 1); // one ask brought it; nothing more to ask about
  });

  it("never has two asks out for one session across its views", async () => {
    mode = "hang";
    act(() => root.render(<><Probe sid="twice" rev={100} /><Probe sid="twice" rev={100} readOnly /></>));
    await act(async () => {});
    act(() => root.render(<><Probe sid="twice" rev={110} /><Probe sid="twice" rev={110} readOnly /></>));
    await wait(120_000);
    expect(pulls()).toBe(1);
  });

  it("does not ask about an archived session", async () => {
    noteArchived({ machine_id: "m1", session_id: "archived" } as Parameters<typeof noteArchived>[0], true);
    act(() => root.render(<Probe sid="archived" rev={100} />));
    await act(async () => {});
    expect(calls.length).toBe(1); // opening it still reads its page
    act(() => root.render(<Probe sid="archived" rev={110} />));
    await wait(120_000);
    expect(pulls()).toBe(0);
    noteArchived({ machine_id: "m1", session_id: "archived" } as Parameters<typeof noteArchived>[0], false);
  });
});
