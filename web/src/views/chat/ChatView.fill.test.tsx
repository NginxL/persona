// @vitest-environment jsdom
// Older pages load when the reader reaches the top. A first page shorter than the view never scrolls, so that top was
// never reached and the chat stayed at its newest page: the main conversation's newest rounds are often two folded
// background rounds, a couple of lines. Older pages now load until the view is full.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";
import type { NavRail } from "./NavRail";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = (q: string) => ({ matches: q.includes("pointer: fine"), addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});
vi.mock("./Composer", async (importOriginal) => ({ ...await importOriginal<typeof import("./Composer")>(), Composer: () => null }));
const transcript = vi.hoisted(() => ({
  items: [] as Item[],
  loading: false,
  error: "",
  hasMore: true,
  loadingEarlier: false,
  earlierPage: undefined as symbol | undefined,
  streaming: "",
  loadEarlier: (() => Promise.resolve(undefined)) as () => Promise<symbol | undefined>,
}));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
const rail = vi.hoisted(() => ({ props: null as React.ComponentProps<typeof NavRail> | null }));
vi.mock("./NavRail", async (importOriginal) => ({
  ...await importOriginal<typeof import("./NavRail")>(),
  NavRail: (props: React.ComponentProps<typeof NavRail>) => { rail.props = props; return null; },
}));
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

import { applyCompleteSessions, reset } from "../../lib/store";
import { resetPullGatesForTests } from "../../lib/pull-gate";
const { ChatView } = await import("./ChatView");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let size = { scrollHeight: 0, clientHeight: 0 };
beforeEach(() => {
  vi.useFakeTimers();
  rail.props = null;
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { status: 200 }))));
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(() => size.scrollHeight);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockImplementation(() => size.clientHeight);
  // as a browser clamps it: a view with nothing to scroll stays at 0
  let top = 0;
  vi.spyOn(HTMLElement.prototype, "scrollTop", "get").mockImplementation(() => top);
  vi.spyOn(HTMLElement.prototype, "scrollTop", "set").mockImplementation((v: number) => { top = Math.max(0, Math.min(v, size.scrollHeight - size.clientHeight)); });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetPullGatesForTests(); // a case's failed index read must not hold the next case's back
  vi.restoreAllMocks();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const row = { machine_id: "mac", session_id: "s1", title: "s1", cwd: "/code", status: "idle", state: "idle" } as Session;
const background: Item[] = [
  { id: "u1#0", seq: 10, ts: "2026-11-11T08:00:00Z", role: "user", kind: "text", text: "a report from another conversation" },
  { id: "a1#0", seq: 11, ts: "2026-11-11T08:00:30Z", role: "assistant", kind: "text", text: "noted" },
];

async function open(view: { scrollHeight: number; clientHeight: number }, hasMore = true) {
  size = view;
  const loadEarlier = vi.fn(() => Promise.resolve(undefined));
  Object.assign(transcript, { items: [], hasMore: false, loading: true, loadingEarlier: false, earlierPage: undefined, streaming: "", loadEarlier });
  act(() => applyCompleteSessions([row]));
  act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
  // the hub's first page comes in
  Object.assign(transcript, { items: background, hasMore, loading: false });
  act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
  for (let i = 0; i < 3; i++) await act(async () => { await vi.advanceTimersByTimeAsync(100); });
  return loadEarlier;
}

describe("ChatView older pages", () => {
  it("loads an older page when the newest one does not fill the view", async () => {
    const loadEarlier = await open({ scrollHeight: 400, clientHeight: 800 });
    expect(loadEarlier).toHaveBeenCalled();
  });

  it("waits for the reader to scroll up when the page fills the view", async () => {
    const loadEarlier = await open({ scrollHeight: 6000, clientHeight: 800 });
    expect(loadEarlier).not.toHaveBeenCalled();
  });

  it("asks for nothing once the history is complete", async () => {
    const loadEarlier = await open({ scrollHeight: 400, clientHeight: 800 }, false);
    expect(loadEarlier).not.toHaveBeenCalled();
  });

  it("keeps the same line at the same offset when a prepend and new activity commit together", async () => {
    await open({ scrollHeight: 6_000, clientHeight: 800 });
    let release!: (page: symbol) => void;
    transcript.loadEarlier = vi.fn(() => new Promise<symbol>((resolve) => { release = resolve; }));
    const render = () => act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    render();
    const sc = host.querySelector<HTMLElement>("[data-chat-scroll]")!;
    const line = sc.querySelector('[data-line="seg:u1#0"]')!.firstElementChild!;
    let lineTop = 180;
    vi.spyOn(line, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, lineTop - sc.scrollTop, 600, 300));
    act(() => sc.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 })));
    sc.scrollTop = 220;
    act(() => sc.dispatchEvent(new Event("scroll")));
    expect(transcript.loadEarlier).toHaveBeenCalledOnce();
    const offset = line.getBoundingClientRect().top;

    const page = Symbol();
    await act(async () => release(page));
    lineTop += 800;
    size.scrollHeight += 800 + 150;
    Object.assign(transcript, { earlierPage: page, hasMore: false, items: [
      { ...background[0], id: "earlier", seq: 1 }, ...background,
      { id: "compact", seq: 12, role: "system", kind: "notice", compact: { trigger: "manual" } },
      { ...background[0], id: "supervision", seq: 13, injected: { kind: "system", state: "supervise", line: "Check tasks" } },
    ] });
    render();
    expect(line.isConnected).toBe(true);
    expect(line.getBoundingClientRect().top).toBe(offset);
  });

  it("anchors a prepended page when the rail joins a read started elsewhere", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(
      String(input).includes("user_inputs=1") ? { total: 3, history: "", inputs: [
        { id: "distant#0", seq: 1 }, { id: "intermediate#0", seq: 5 }, { id: "u1#0", seq: 10 },
      ] } : {},
    ), { headers: { "Content-Type": "application/json" } })));
    await open({ scrollHeight: 6_000, clientHeight: 800 });
    // Advancing the viewport timers does not wait for the lazy rail module.
    await act(async () => { await vi.dynamicImportSettled(); });
    expect(rail.props?.ticks.map((tick) => tick.id)).toContain("seg:distant#0");
    const scroller = host.querySelector<HTMLElement>("[data-chat-scroll]")!;
    scroller.scrollTop = 3_200;
    let release!: (page: symbol) => void;
    const pending = new Promise<symbol>((resolve) => { release = resolve; });
    const loadEarlier = vi.fn(() => pending);
    // The underlying read started outside the scroll loader, so it did not take an anchor.
    Object.assign(transcript, { loadEarlier, loadingEarlier: true });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    act(() => { void rail.props!.onJump("seg:distant#0"); });
    expect(loadEarlier).toHaveBeenCalledOnce();
    expect(scroller.scrollTop).toBe(3_200);

    // New text below the reading position must neither consume the anchor nor count as a prepend.
    size.scrollHeight = 6_150;
    transcript.streaming = "A live reply";
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    expect(scroller.scrollTop).toBe(3_200);
    scroller.scrollTop = 3_250;
    act(() => scroller.dispatchEvent(new Event("scroll")));

    // Complete the HTTP read in a separate commit while React still shows the old rows.
    const page = Symbol();
    await act(async () => { release(page); });
    expect(scroller.scrollTop).toBe(3_250);
    await act(async () => { await vi.advanceTimersByTimeAsync(16); });

    // A live render can still arrive between HTTP completion and the older rows' layout commit.
    size.scrollHeight = 6_300;
    transcript.streaming = "A live reply that has grown";
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    expect(scroller.scrollTop).toBe(3_250);

    // Only the intermediate page has arrived. Its prepended height must be offset before
    // navigation is allowed to jump to the (still absent) selected input.
    size.scrollHeight = 6_850;
    Object.assign(transcript, {
      items: [{ ...background[0], id: "intermediate#0", seq: 5 }, ...background],
      hasMore: false,
      earlierPage: page,
      loadingEarlier: false,
    });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    expect(scroller.scrollTop).toBe(3_800);
  });
});
