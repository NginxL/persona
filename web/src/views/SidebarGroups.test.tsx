// @vitest-environment jsdom
// The sidebar's groups (#9071): with none, Pages alone as before; a group the user made is a foldable section of its
// own, after Pages, holding the pages moved to it, in the wide sidebar and in a phone's drawer below its grid.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// the feature modules (the health page is one: docs/web/web.md "Modules")
import { MODULES } from "../modules";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { setSidebarCollapsed } from "../lib/layout";
import { addPageGroup, resetSidebarPrefs, setPageGroup } from "../lib/sidebar-pages";
import { reset } from "../lib/store";
import { Sidebar } from "./Sidebar";
// the health module, when this run has it (the core-only lane, #13932, runs with no module)
const HEALTH = MODULES.some((m) => m.id === "health");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const headings = () => [...host.querySelectorAll("[data-sidebar-group-heading] button[aria-expanded]")].map((b) => b.textContent?.trim() ?? "");
const linksUnder = (title: string) => {
  const heading = [...host.querySelectorAll("[data-sidebar-group-heading]")].find((h) => h.textContent?.trim() === title);
  return [...(heading?.parentElement?.querySelectorAll("a") ?? [])].map((a) => a.textContent?.trim() ?? "");
};

beforeEach(() => {
  localStorage.clear();
  setSidebarCollapsed(false);
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetSidebarPrefs();
  reset();
  vi.unstubAllGlobals();
});

describe("sidebar groups", () => {
  it("is Pages alone when no group is named", async () => {
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(headings().filter((h) => h === "页面" || h === "生活")).toEqual(["页面"]);
  });

  it.skipIf(!HEALTH)("draws the user's group after Pages, with the pages moved to it", async () => {
    const life = addPageGroup([""], "生活");
    setPageGroup("health", life);
    setPageGroup("routines", life);
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(headings().filter((h) => h === "页面" || h === "生活")).toEqual(["页面", "生活"]);
    expect(linksUnder("生活")).toEqual(["健康", "定时任务"]);
    expect(linksUnder("页面")).not.toContain("健康");
  });
});
