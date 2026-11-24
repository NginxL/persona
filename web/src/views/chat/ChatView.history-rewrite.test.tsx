// @vitest-environment jsdom
// When a transcript's history token changes (e.g. compaction or rewind) and the replacement page
// shrinks to the latest turns, ChatView must not leave scrollTop clamped below 300px or trigger
// cascading loadEarlierNearTop() calls that pull the viewport to the start of the conversation.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = (q: string) => ({ matches: q.includes("pointer: fine"), addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});
vi.mock("./Composer", async (importOriginal) => ({ ...await importOriginal<typeof import("./Composer")>(), Composer: () => null }));
const transcript = vi.hoisted(() => ({
  items: [] as Item[],
  history: "" as string | undefined,
  replaceEpoch: 0,
  loading: false,
  error: "",
  hasMore: true,
  loadingEarlier: false,
  earlierPage: undefined as symbol | undefined,
  streaming: "",
  loadEarlier: (() => Promise.resolve(undefined)) as () => Promise<symbol | undefined>,
}));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("./NavRail", async (importOriginal) => ({
  ...await importOriginal<typeof import("./NavRail")>(),
  NavRail: () => null,
}));
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

import { applyCompleteSessions, reset } from "../../lib/store";
const { ChatView } = await import("./ChatView");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let size = { scrollHeight: 0, clientHeight: 0 };
let top = 0;

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { status: 200 }))));
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(() => size.scrollHeight);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockImplementation(() => size.clientHeight);
  top = 0;
  vi.spyOn(HTMLElement.prototype, "scrollTop", "get").mockImplementation(() => top);
  vi.spyOn(HTMLElement.prototype, "scrollTop", "set").mockImplementation((v: number) => {
    top = Math.max(0, Math.min(v, Math.max(0, size.scrollHeight - size.clientHeight)));
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.restoreAllMocks();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const row = { machine_id: "mac", session_id: "s1", title: "s1", cwd: "/code", status: "idle", state: "idle" } as Session;

const olderItems: Item[] = [
  { id: "u1#0", seq: 1, ts: "2026-11-21T08:00:00Z", role: "user", kind: "text", text: "first turn" },
  { id: "a1#0", seq: 2, ts: "2026-11-21T08:00:10Z", role: "assistant", kind: "text", text: "first reply" },
  { id: "u2#0", seq: 10, ts: "2026-11-21T08:01:00Z", role: "user", kind: "text", text: "older turn" },
  { id: "a2#0", seq: 11, ts: "2026-11-21T08:01:10Z", role: "assistant", kind: "text", text: "older reply" },
  { id: "u3#0", seq: 20, ts: "2026-11-21T08:02:00Z", role: "user", kind: "text", text: "latest turn" },
  { id: "a3#0", seq: 21, ts: "2026-11-21T08:02:10Z", role: "assistant", kind: "text", text: "latest reply" },
];

const latestOnly: Item[] = olderItems.slice(4);

describe("ChatView history rewrite scroll preservation", () => {
  it("settles at the bottom without triggering loadEarlier when a history rewrite drops the old anchor", async () => {
    size = { scrollHeight: 6000, clientHeight: 800 };
    const loadEarlier = vi.fn(() => Promise.resolve(undefined));
    Object.assign(transcript, {
      items: olderItems,
      history: "agy:conv:rev1",
      replaceEpoch: 0,
      hasMore: true,
      loading: false,
      loadingEarlier: false,
      earlierPage: undefined,
      streaming: "",
      loadEarlier,
    });
    act(() => applyCompleteSessions([row]));
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    const scroller = host.querySelector<HTMLElement>("[data-chat-scroll]")!;
    expect(scroller.scrollTop).toBe(5200);
    expect(loadEarlier).not.toHaveBeenCalled();

    // User scrolls up slightly while reading an older turn (leaving the bottom).
    act(() => {
      scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));
    });
    scroller.scrollTop = 4500;
    act(() => scroller.dispatchEvent(new Event("scroll")));
    expect(loadEarlier).not.toHaveBeenCalled();

    // History token changes and transcript shrinks to the latest 2 turns; browser clamps scrollTop to 200 (< 300).
    size = { scrollHeight: 1000, clientHeight: 800 };
    top = 200;
    Object.assign(transcript, {
      items: latestOnly,
      history: "agy:conv:rev2",
      replaceEpoch: 1,
      hasMore: true,
    });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });

    // Must settle at the bottom (200 = scrollHeight - clientHeight) and NOT trigger loadEarlierNearTop().
    expect(scroller.scrollTop).toBe(200);
    expect(loadEarlier).not.toHaveBeenCalled();

    // Once the user explicitly scrolls up near the top, loadEarlier works normally again.
    act(() => {
      scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));
    });
    scroller.scrollTop = 50;
    act(() => scroller.dispatchEvent(new Event("scroll")));
    expect(loadEarlier).toHaveBeenCalledOnce();
  });

  it("restores the visible message anchor on history rewrite without firing loadEarlierNearTop", async () => {
    size = { scrollHeight: 6000, clientHeight: 800 };
    const loadEarlier = vi.fn(() => Promise.resolve(undefined));
    Object.assign(transcript, {
      items: olderItems,
      history: "agy:conv:rev1",
      replaceEpoch: 0,
      hasMore: true,
      loading: false,
      loadingEarlier: false,
      earlierPage: undefined,
      streaming: "",
      loadEarlier,
    });
    act(() => applyCompleteSessions([row]));
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    const scroller = host.querySelector<HTMLElement>("[data-chat-scroll]")!;

    // Mock geometry so the latest user line (u3#0) is the first visible line at offset 40px below scroller top.
    const scrollerRect = { top: 100, bottom: 900, left: 0, right: 800, width: 800, height: 800, x: 0, y: 100, toJSON() {} };
    vi.spyOn(scroller, "getBoundingClientRect").mockReturnValue(scrollerRect as DOMRect);
    const findTargetLine = () => [...scroller.querySelectorAll<HTMLElement>("[data-line]")].find((el) => el.dataset.line?.includes("u3#0"));
    const targetBefore = findTargetLine();
    expect(targetBefore).toBeTruthy();
    const targetLineId = targetBefore!.dataset.line!;
    for (const el of scroller.querySelectorAll<HTMLElement>("[data-line]")) {
      const child = el.firstElementChild as HTMLElement | null;
      if (!child) continue;
      const isTarget = el.dataset.line === targetLineId;
      const y = isTarget ? 140 : -500;
      vi.spyOn(child, "getBoundingClientRect").mockReturnValue({
        top: y, bottom: y + 120, left: 0, right: 800, width: 800, height: 120, x: 0, y, toJSON() {},
      } as DOMRect);
    }

    act(() => {
      scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));
    });
    scroller.scrollTop = 4800;
    act(() => scroller.dispatchEvent(new Event("scroll")));

    // Rewrite replaces items with latestOnly (targetLineId is still present!), shrinking scrollHeight.
    size = { scrollHeight: 1600, clientHeight: 800 };
    top = 150; // browser-clamped < 300
    Object.assign(transcript, {
      items: latestOnly,
      history: "agy:conv:rev2",
      replaceEpoch: 1,
      hasMore: true,
    });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    const u3 = findTargetLine()?.firstElementChild as HTMLElement | null;
    expect(u3).toBeTruthy();
    expect(loadEarlier).not.toHaveBeenCalled();

    // Now simulate a second rewrite where u3 is displaced by +100px relative to its saved 40px offset.
    vi.spyOn(u3!, "getBoundingClientRect").mockReturnValue({
      top: 140, bottom: 260, left: 0, right: 800, width: 800, height: 120, x: 0, y: 140, toJSON() {},
    } as DOMRect);
    scroller.scrollTop = 400;
    act(() => scroller.dispatchEvent(new Event("scroll")));
    expect(loadEarlier).not.toHaveBeenCalled();

    // Browser clamps scrollTop to 180 (< 300) when the replacement shrinks height, and u3 lands at y=240.
    top = 180;
    vi.spyOn(u3!, "getBoundingClientRect").mockReturnValue({
      top: 240, bottom: 360, left: 0, right: 800, width: 800, height: 120, x: 0, y: 240, toJSON() {},
    } as DOMRect);
    Object.assign(transcript, {
      items: [...latestOnly],
      history: "agy:conv:rev3",
      replaceEpoch: 2,
      hasMore: true,
    });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });

    // scrollTop moved from 180 by (240 - 100 - 40) = +100 -> 280, and loadEarlier did NOT fire even though 280 < 300.
    expect(scroller.scrollTop).toBe(280);
    expect(loadEarlier).not.toHaveBeenCalled();
  });

  it("does not trigger cascading loadEarlier when a short rewritten page lands while following the bottom", async () => {
    size = { scrollHeight: 5000, clientHeight: 800 };
    const loadEarlier = vi.fn(() => Promise.resolve(undefined));
    Object.assign(transcript, {
      items: olderItems,
      history: "agy:conv:rev1",
      replaceEpoch: 0,
      hasMore: true,
      loading: false,
      loadingEarlier: false,
      earlierPage: undefined,
      streaming: "",
      loadEarlier,
    });
    act(() => applyCompleteSessions([row]));
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    expect(loadEarlier).not.toHaveBeenCalled();

    // History rewrites to a short page (scrollHeight < clientHeight, so scrollTop clamps to 0 < 300).
    size = { scrollHeight: 400, clientHeight: 800 };
    top = 0;
    Object.assign(transcript, {
      items: latestOnly,
      history: "agy:conv:rev2",
      replaceEpoch: 1,
      hasMore: true,
    });
    act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });

    expect(loadEarlier).not.toHaveBeenCalled();
  });
});
