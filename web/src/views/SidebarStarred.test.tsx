// @vitest-environment jsdom
// Starred is off until Settings › General › Appearance turns it on (this browser's switch): the sidebar's Starred page
// is absent by default and shows, with its count, once the switch is on. The saved messages are not touched by it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/starred", () => ({ startStarred: vi.fn() }));
import { getState, reset } from "../lib/store";
import { setStarredEnabled, setStarredSummary, starredEnabled } from "../lib/starred-summary";
import { initI18n } from "../i18n";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const starredLink = () => host.querySelector<HTMLAnchorElement>('a[href="/starred"]');

beforeEach(async () => {
  localStorage.clear();
  await initI18n("en");
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { headers: { "Content-Type": "application/json" } }))));
  Object.assign(getState() as unknown as Record<string, unknown>, { features: ["starred_messages"] });
  setStarredSummary(3, true);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  reset();
  setStarredSummary(undefined, false);
  vi.unstubAllGlobals();
  await initI18n("zh-Hans");
});

const draw = async () => {
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
};

describe("the sidebar's Starred page switch", () => {
  it("is off by default and shows no Starred page", async () => {
    expect(starredEnabled()).toBe(false);
    await draw();
    expect(starredLink()).toBeNull();
  });

  it("shows the Starred page with its count once the switch is on, and drops it again when off", async () => {
    await draw();
    act(() => setStarredEnabled(true));
    await flush();
    expect(starredLink()).not.toBeNull();
    expect(starredLink()!.textContent).toContain("3");
    act(() => setStarredEnabled(false));
    await flush();
    expect(starredLink()).toBeNull();
  });
});
