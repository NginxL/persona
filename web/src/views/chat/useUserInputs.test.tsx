// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useRevealedInputSeq, useUserInputs } from "./useUserInputs";
import { ApiError } from "../../lib/api";
import type { UserInputIndex } from "../../lib/user-input-index";
import { resetPullGatesForTests } from "../../lib/pull-gate";

const pending = vi.hoisted(() => [] as { path: string; resolve: (value: unknown) => void; signal?: AbortSignal }[]);
vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  sessionPath: (mid: string, sid: string) => `/sessions/${mid}/${sid}`,
  api: (path: string, opts: { signal?: AbortSignal }) => new Promise((resolve) => pending.push({ path, resolve, signal: opts.signal })),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let destroy: (() => void) | undefined;
afterEach(() => { destroy?.(); destroy = undefined; pending.length = 0; resetPullGatesForTests(); vi.useRealTimers(); });
const answer = (history: string, total = 2): UserInputIndex => ({ total, history, inputs: Array.from({ length: total }, (_, i) => ({ id: `input-${i}#0`, seq: i + 1 })) });

describe("independent user-input metadata", () => {
  it("grows only after earlier transcript pages arrive, including a cached full first page", () => {
    const host = document.createElement("div"); const root = createRoot(host);
    let got: number | undefined;
    let reveal = () => {};
    function Probe({ scope, oldest }: { scope: string; oldest?: number }) { const range = useRevealedInputSeq(scope, oldest); got = range.seq; reveal = range.reveal; return null; }
    const render = (scope: string, oldest?: number) => act(() => root.render(<Probe scope={scope} oldest={oldest} />));
    destroy = () => act(() => root.unmount());
    render("a"); expect(got).toBeUndefined();
    render("a", 2501); expect(got).toBeUndefined();
    render("a", 2301); expect(got).toBe(2301);
    render("a", 2301); expect(got).toBe(2301);
    render("a", 2101); expect(got).toBe(2101);
    render("b", 1); expect(got).toBeUndefined();
    act(() => reveal()); expect(got).toBe(1);
    render("a-rewritten", 1); expect(got).toBeUndefined();
  });

  it("fences conversation/history changes and refreshes when a genuine input arrives", async () => {
    const host = document.createElement("div"); const root = createRoot(host);
    let got: UserInputIndex | null = null;
    const read = (): UserInputIndex | null => got;
    function Probe({ sid, history, newest }: { sid: string; history: string; newest: string }) { got = useUserInputs("example", sid, history, newest, true); return null; }
    const render = (sid: string, history: string, newest = "last") => act(() => root.render(<Probe sid={sid} history={history} newest={newest} />));
    destroy = () => act(() => root.unmount());
    render("a", "old"); render("b", "new");
    expect(pending[0].signal?.aborted).toBe(true);
    await act(async () => pending[0].resolve(answer("old")));
    expect(got).toBeNull();
    await act(async () => pending[1].resolve(answer("new")));
    expect(read()?.total).toBe(2);
    render("b", "new", "new-input");
    await act(async () => pending[2].resolve(answer("new", 3)));
    expect(read()?.total).toBe(3);
    render("b", "rewritten");
    expect(got).toBeNull();
    await act(async () => pending[3].resolve(answer("new", 3)));
    expect(got).toBeNull();
  });
  it("refreshes the transcript when the complete index discovers a different historical identity", async () => {
    const host = document.createElement("div"); const root = createRoot(host);
    const reload = vi.fn();
    function Probe() { useUserInputs("example", "synthetic", "tail-history", "last", true, reload); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    await act(async () => pending[0].resolve(answer("complete-history")));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  // #11450: a computer that can't find this conversation's history says so for good; the hub's own 404 (a computer it
  // does not know) is asked again like any failure.
  it("stops asking once its computer can't find the history", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div"); const root = createRoot(host);
    function Probe() { useUserInputs("example", "synthetic", "fixture", "last", true); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    await act(async () => pending[0].resolve(Promise.reject(new ApiError(404, "Persona can't find its history",
      { code: "not_found", reason: "no_transcript", detail: "transcript for session synthetic not found" }))));
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(pending).toHaveLength(1);
  });

  it("asks again after the hub's own 404", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div"); const root = createRoot(host);
    function Probe() { useUserInputs("example", "synthetic", "fixture", "last", true); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    await act(async () => pending[0].resolve(Promise.reject(new ApiError(404, "No such computer", { code: "not_found" }))));
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(pending).toHaveLength(2);
  });

  it("retries a detail-only history 404 after 3 seconds and recovers", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div"); const root = createRoot(host);
    let got: UserInputIndex | null = null;
    const read = (): UserInputIndex | null => got;
    function Probe() { got = useUserInputs("example", "synthetic", "fixture", "last", true); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    await act(async () => pending[0].resolve(Promise.reject(new ApiError(404, "Persona can't find its history",
      { code: "not_found", detail: "transcript for session synthetic not found" }))));
    await act(async () => { await vi.advanceTimersByTimeAsync(2999); });
    expect(pending).toHaveLength(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(pending).toHaveLength(2);
    await act(async () => pending[1].resolve(answer("fixture", 8)));
    expect(read()?.total).toBe(8);
  });

  it("retries unsupported or incomplete indexes instead of permanently freezing the tail count", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div"); const root = createRoot(host);
    let got: UserInputIndex | null = null;
    const read = (): UserInputIndex | null => got;
    function Probe() { got = useUserInputs("example", "synthetic", "fixture", "last", true); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    await act(async () => pending[0].resolve({ items: [], has_more: false }));
    expect(got).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(pending).toHaveLength(2);
    await act(async () => pending[1].resolve(answer("fixture", 8)));
    expect(read()?.total).toBe(8);
  });

  it("does not read indexes on touch-only or unmounted chats", async () => {
    const host = document.createElement("div"); const root = createRoot(host);
    function Probe() { useUserInputs("example", "synthetic", "fixture", "last", false); return null; }
    act(() => root.render(<Probe />)); destroy = () => act(() => root.unmount());
    expect(pending).toHaveLength(0);
  });
});
