// @vitest-environment jsdom
// The main conversation's page keeps its chat while the computer it runs on goes from the cached first view's to the
// hub's answer (#11237): a remount would drop the words being typed, the pictures in the box and the focus.
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setToken } from "../lib/api";
import { feedCacheScope, writeFeedCache } from "../lib/feed-cache";
import { resetManagerChoice } from "../lib/manager-choice";
import { getState, reset } from "../lib/store";
import { PersonaAssistant } from "./PersonaAssistant";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
vi.hoisted(() => {
  Object.assign(globalThis, {
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    ResizeObserver: class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
});
const mounts = vi.hoisted(() => ({ count: 0 }));
vi.mock("./chat/ChatView", () => ({
  ChatView: (p: { mid: string; sid: string }) => {
    useEffect(() => { mounts.count++; }, []);
    return <div data-chat={`${p.mid}/${p.sid}`} />;
  },
}));
vi.mock("./assistant/Readiness", () => ({
  useReadinessGate: () => ({ readiness: null }), ReadinessCard: () => null, ChatOnlyNote: () => null, FailureCard: () => null,
}));
let host: HTMLDivElement;
let root: Root;
const render = () => act(() => root.render(<PersonaAssistant onToggleSidebar={() => {}} />));
beforeEach(() => {
  mounts.count = 0;
  localStorage.clear();
  resetManagerChoice();
  reset();
  setToken("test-credential");
  writeFeedCache(feedCacheScope()!, { machineID: "cached-hub", sessionID: "main", items: [], hasMore: false });
  vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); reset(); resetManagerChoice(); vi.unstubAllGlobals(); });

it("keeps one chat page while the main conversation's computer becomes known", () => {
  render();
  expect(host.querySelector('[data-chat="cached-hub/main"]')).not.toBeNull();
  Object.assign(getState(), { assistant: { machine_id: "hub", main: "main", conversations: [] } });
  render();
  expect(host.querySelector('[data-chat="hub/main"]')).not.toBeNull();
  expect(mounts.count).toBe(1);
});

it("still opens a new page for another conversation", () => {
  render();
  Object.assign(getState(), { assistant: { machine_id: "hub", main: "another-main", conversations: [] } });
  render();
  expect(host.querySelector('[data-chat="hub/another-main"]')).not.toBeNull();
  expect(mounts.count).toBe(2);
});
