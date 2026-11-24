// @vitest-environment jsdom
// Settings › Appearance › Sidebar icons (#10822): Monochrome, the default, draws every page's icon exactly as before
// (the same classes, no style), an extension page's icon_color included; Colored gives each built-in page its color and
// an extension page the one its manifest names, in the sidebar's rows, its More, the icon rail and a phone's grid, while
// the open page keeps the open row's ink.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// three made-up extension pages: one naming a color, one naming none, one naming a color no client knows
const PLACES = vi.hoisted(() => [
  { extension: "example-garden", extension_name: "Example Garden", id: "garden", title: "Garden", placement: "nav_page", icon: "calendar", icon_color: "green" },
  { extension: "example-notes", extension_name: "Example Notes", id: "notes", title: "Notes", placement: "nav_page", icon: "list" },
  { extension: "example-odd", extension_name: "Example Odd", id: "odd", title: "Odd", placement: "nav_page", icon: "chart", icon_color: "brown" },
]);
vi.mock("../lib/extension-slots", async (original) => ({
  ...(await original<typeof import("../lib/extension-slots")>()),
  useExtensionSlot: (placement: string) => (placement === "nav_page" ? PLACES : []),
}));
import { MODULES } from "../modules";
import { setSidebarCollapsed } from "../lib/layout";
import { setPageShown } from "../lib/sidebar-pages";
import { BUILTIN_PAGE_COLORS, setSidebarIconMode } from "../lib/sidebar-icons";
import { reset } from "../lib/store";
import { NavGrid, Sidebar, SidebarRail, useNavPages } from "./Sidebar";
// the health module, when this run has it (the core-only lane, #13932, runs with no module)
const HEALTH = MODULES.some((m) => m.id === "health");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const link = (label: string) => [...host.querySelectorAll<HTMLAnchorElement>("a")].find((a) => (a.getAttribute("aria-label") ?? a.textContent?.trim()) === label || a.title === label)!;
/** A page row's (or a grid tile's) icon box: the span around its svg. */
const iconBox = (label: string) => link(label).querySelector("svg")!.parentElement as HTMLElement;
// the built-in pages' colors and the health module's own (src/modules/health/module.ts)
const PAGE_COLORS = { ...BUILTIN_PAGE_COLORS, health: "pink" } as const;
const ALL_PAGES = { 总览: "home", 收件箱: "inbox", 健康: "health", 待办: "todos", 定时任务: "routines", 知识库: "kb", 机器: "machines", 设置: "settings" } as const;
const BUILT_IN: Partial<typeof ALL_PAGES> = HEALTH ? ALL_PAGES : Object.fromEntries(Object.entries(ALL_PAGES).filter(([, key]) => key !== "health"));
const hues = () => [...host.querySelectorAll<HTMLElement>("[style]")].map((el) => el.style.color).filter((c) => c.includes("--hue-"));

function DrawerPages({ active = "home" }: { active?: string }) {
  const { pages, more } = useNavPages();
  return <NavGrid pages={pages} more={more} activeKey={active} go={() => () => {}} />;
}

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
  reset();
  vi.unstubAllGlobals();
});

describe("Monochrome (the default)", () => {
  it("draws the sidebar's page rows as before: the quiet ink, the open one's ink, no style", async () => {
    act(() => root.render(<Sidebar route={{ name: "inbox" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    for (const label of [...Object.keys(BUILT_IN), "Garden", "Notes", "Odd"]) {
      const box = iconBox(label);
      expect(box.className, label).toBe(label === "收件箱" ? "text-ink" : "text-ink-3");
      expect(box.hasAttribute("style"), label).toBe(false);
    }
    expect(hues()).toEqual([]);
    expect(localStorage.getItem("persona.sidebar-icons")).toBeNull();
  });

  it("draws the icon rail and a phone's grid as before", async () => {
    act(() => root.render(<SidebarRail route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    for (const label of ["收件箱", "待办", "Garden"]) {
      expect(link(label).className, label).toContain("text-ink-3 hover:bg-hover hover:text-ink");
      expect(link(label).hasAttribute("style"), label).toBe(false);
    }
    expect(link("总览").hasAttribute("style")).toBe(false);
    act(() => root.render(<DrawerPages />));
    await flush();
    for (const label of ["收件箱", "待办", "Garden"]) {
      expect(iconBox(label).className, label).toBe("relative text-ink-3");
      expect(iconBox(label).hasAttribute("style"), label).toBe(false);
    }
    expect(iconBox("总览").className).toBe("relative text-ink");
    expect(hues()).toEqual([]);
  });

  it("is what an unknown stored value, and switching back from Colored, draw", async () => {
    // the page rows as a fresh page load draws them (React's own ids, which count up across roots, left out)
    const draw = async () => {
      act(() => root.unmount());
      root = createRoot(host);
      act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
      await flush();
      return [...host.querySelectorAll("a")].map((a) => a.outerHTML.replace(/_r_\w+_/g, "")).join("\n");
    };
    const before = await draw();
    act(() => setSidebarIconMode("colored"));
    expect(await draw()).not.toBe(before);
    // back to Monochrome in place: no color is left on any icon (React keeps an empty style attribute), and the next
    // load draws what it drew before
    act(() => setSidebarIconMode("monochrome"));
    await flush();
    expect(hues()).toEqual([]);
    expect(localStorage.getItem("persona.sidebar-icons")).toBeNull();
    expect(await draw()).toBe(before);
    localStorage.setItem("persona.sidebar-icons", "rainbow");
    expect(await draw()).toBe(before);
  });
});

describe("Colored", () => {
  beforeEach(() => setSidebarIconMode("colored"));

  it("gives each built-in page its color and an extension page the one it names; the open page keeps its ink", async () => {
    act(() => root.render(<Sidebar route={{ name: "inbox" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    for (const [label, key] of Object.entries(BUILT_IN)) {
      const box = iconBox(label);
      if (key === "inbox") {
        expect(box.className).toBe("text-ink");
        expect(box.hasAttribute("style")).toBe(false);
      } else {
        expect(box.className, label).toBe("text-ink-3");
        expect(box.style.color, label).toBe(`var(--hue-${PAGE_COLORS[key]})`);
      }
    }
    expect(iconBox("Garden").style.color).toBe("var(--hue-green)");
    // no icon_color, or one that is not a named color: the neutral ink
    expect(iconBox("Notes").hasAttribute("style")).toBe(false);
    expect(iconBox("Odd").hasAttribute("style")).toBe(false);
  });

  it("uses the issue's anchors: Overview amber, Inbox orange, To-dos blue, each built-in page its own", () => {
    expect([BUILTIN_PAGE_COLORS.home, BUILTIN_PAGE_COLORS.inbox, BUILTIN_PAGE_COLORS.todos]).toEqual(["amber", "orange", "blue"]);
    const colors = Object.values(BUILTIN_PAGE_COLORS);
    expect(new Set(colors).size).toBe(colors.length);
    expect(colors).not.toContain("red");
  });

  it("colors the pages under More too", async () => {
    setPageShown("kb", false);
    act(() => root.render(<Sidebar route={{ name: "kb" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    // the open page under More keeps the open row's ink; another put away takes its color
    expect(iconBox("知识库").className).toBe("text-ink");
    expect(iconBox("知识库").hasAttribute("style")).toBe(false);
    if (HEALTH) {
      act(() => setPageShown("health", false));
      await flush();
      expect(iconBox("健康").style.color).toBe("var(--hue-pink)");
    }
  });

  it("colors the icon rail, kept under the pointer, and the open page keeps its ink", async () => {
    act(() => root.render(<SidebarRail route={{ name: "todos" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(link("收件箱").style.color).toBe("var(--hue-orange)");
    expect(link("收件箱").className).toContain("text-ink-3 hover:bg-hover hover:text-ink");
    expect(link("Garden").style.color).toBe("var(--hue-green)");
    expect(link("Notes").hasAttribute("style")).toBe(false);
    expect(link("待办").hasAttribute("style")).toBe(false);
    expect(link("待办").className).toContain("text-ink");
    expect(link("待办").className).not.toContain("text-ink-3");
  });

  it("colors a phone's grid of pages", async () => {
    act(() => root.render(<DrawerPages />));
    await flush();
    expect(iconBox("总览").hasAttribute("style")).toBe(false);
    expect(iconBox("收件箱").style.color).toBe("var(--hue-orange)");
    expect(iconBox("待办").style.color).toBe("var(--hue-blue)");
    expect(iconBox("Garden").style.color).toBe("var(--hue-green)");
    expect(iconBox("Notes").hasAttribute("style")).toBe(false);
  });

  it("follows another tab's choice at once", async () => {
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(iconBox("收件箱").style.color).toBe("var(--hue-orange)");
    localStorage.removeItem("persona.sidebar-icons");
    act(() => { dispatchEvent(new StorageEvent("storage", { key: "persona.sidebar-icons" })); });
    await flush();
    expect(iconBox("收件箱").style.color).toBe("");
  });
});
