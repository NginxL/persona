// @vitest-environment jsdom
// The health module (#13434, the web pilot of #13430; docs/web/web.md "Modules"): its page, its sidebar entry and its
// strings come from the module host. Switched off, or registered behind a hub flag the hub does not name, it is absent:
// no entry, its addresses are unknown ones, and neither its chunk (./Health) nor its catalog is ever requested. On, the
// page opens at /health as before, and its chunk and strings load when it is first drawn, the strings first.
import { act, Suspense } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// the page as a mark: what matters here is whether its chunk is requested, and what it is given
vi.mock("./Health", () => ({ HealthPage: ({ path }: { path: readonly string[] }) => <div data-testid="health-page">{path.join("/")}</div> }));
import { connectModuleHub, MODULES_FEATURE, moduleExtensionAt, registerModule, resetModulesForTest, type ModuleDescriptor } from "../../core/module-host";
import { ModuleExtensionPage, ModulePage } from "../../core/ModulePage";
import { preloadPages } from "../../lib/lazy-page";
import { forgetModuleSwitches, setModuleSwitchedOff } from "../../core/module-switch";
import { t } from "../../i18n";
import { setSidebarCollapsed } from "../../lib/layout";
import { href, parse } from "../../lib/router";
import { getState, reset, subscribe } from "../../lib/store";
import { Sidebar } from "../../views/Sidebar";
import { health } from "./module";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** The descriptor as registered, with its chunk's import and its catalog loaders counted. */
const chunk = vi.fn();
const catalog = vi.fn();
function counted(over: Partial<ModuleDescriptor> = {}): ModuleDescriptor {
  const locales = Object.fromEntries(Object.entries(health.locales!).map(([lang, load]) => [lang, () => (catalog(lang), load())]));
  return { ...health, locales, load: () => (chunk(), health.load()), ...over };
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const links = () => [...host.querySelectorAll<HTMLAnchorElement>("a")].map((a) => a.textContent?.trim() ?? "");
const sidebar = async () => {
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
};

beforeEach(() => {
  connectModuleHub(getState, subscribe);
  localStorage.clear();
  forgetModuleSwitches();
  resetModulesForTest();
  chunk.mockClear();
  catalog.mockClear();
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
  resetModulesForTest();
  localStorage.clear();
  forgetModuleSwitches();
  vi.unstubAllGlobals();
});

describe("the health module", () => {
  it("is on by default: its entry after Inbox, its addresses routed to it, its chunk and strings loaded when drawn", async () => {
    registerModule(counted());
    await sidebar();
    const pages = links();
    expect(pages).toContain(t("sidebar.nav_health"));
    expect(pages.indexOf(t("sidebar.nav_health"))).toBe(pages.indexOf(t("sidebar.nav_inbox")) + 1);
    // the sidebar drew it without its code or its strings
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();

    expect(parse("/health")).toEqual({ name: "module", module: "health", base: "health", path: [] });
    expect(parse("/health/new")).toEqual({ name: "module", module: "health", base: "health", path: ["new"] });
    expect(parse("/tracks/health/h1")).toEqual({ name: "module", module: "health", base: "health", path: ["h1"] });
    // and its links are the addresses they always were
    expect(href({ name: "module", module: "health", base: "health", path: [] })).toBe("/health");
    expect(href({ name: "module", module: "health", base: "health", path: ["new"] })).toBe("/health/new");
    expect(href({ name: "module", module: "health", base: "health", path: ["c 1"] })).toBe("/health/c%201");
    const route = parse("/health/h1");
    if (route.name !== "module") throw new Error("not a module route");
    act(() => root.render(<Suspense fallback={null}><ModulePage route={route} /></Suspense>));
    await vi.waitFor(async () => {
      await flush();
      expect(host.querySelector('[data-testid="health-page"]')?.textContent).toBe("h1");
    }, { timeout: 4000 });
    expect(catalog).toHaveBeenCalledTimes(1);
    expect(chunk).toHaveBeenCalledTimes(1);
  });

  it("switched off in this browser: no entry, /health is an unknown address, nothing of it requested", async () => {
    setModuleSwitchedOff("health", true);
    registerModule(counted());
    await sidebar();
    expect(links()).not.toContain(t("sidebar.nav_health"));
    expect(parse("/health")).toEqual({ name: "not-found", path: "/health" });
    expect(parse("/tracks/health")).toEqual({ name: "not-found", path: "/tracks/health" });
    act(() => root.render(<ModulePage route={{ name: "module", module: "health", base: "health", path: [] }} />));
    await flush();
    expect(host.querySelector('[data-testid="health-page"]')).toBeNull();
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
  });

  it("on a hub that publishes module flags without module.health: absent, nothing requested; there once the hello names it", async () => {
    registerModule(counted());
    Object.assign(getState(), { loaded: true, features: [MODULES_FEATURE, "other"] });
    await sidebar();
    expect(links()).not.toContain(t("sidebar.nav_health"));
    expect(parse("/health")).toEqual({ name: "not-found", path: "/health" });
    act(() => root.render(<ModulePage route={{ name: "module", module: "health", base: "health", path: [] }} />));
    await flush();
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();

    Object.assign(getState(), { features: [MODULES_FEATURE, "other", "module.health"] });
    act(() => root.render(<Suspense fallback={null}><ModulePage key="on" route={{ name: "module", module: "health", base: "health", path: [] }} /></Suspense>));
    await vi.waitFor(async () => {
      await flush();
      expect(host.querySelector('[data-testid="health-page"]')).not.toBeNull();
    }, { timeout: 4000 });
    expect(chunk).toHaveBeenCalledTimes(1);
  });

  describe("warming its page before it is opened: on link intent only, as on main", () => {
    /** The idle tier run, then a link to /health reached by the pointer, through lib/lazy-page.ts preloadPages: what
     *  each of them requested. */
    async function warm(loads: boolean) {
      let idle: (() => void) | undefined;
      const stop = preloadPages({ document, requestIdleCallback: (run) => ((idle = run), 1), cancelIdleCallback: () => {} });
      idle?.();
      await flush();
      const idled = { chunk: chunk.mock.calls.length, catalog: catalog.mock.calls.length };
      const link = document.createElement("a");
      link.href = "/health";
      document.body.append(link);
      link.dispatchEvent(new Event("pointerover", { bubbles: true }));
      // the strings, then the code: wait for both by time, not by a count of turns, which a loaded machine can outrun
      // (#13846); for a module that is not on, give it as long as a load takes here and see that nothing came
      if (loads) await vi.waitFor(() => expect(chunk).toHaveBeenCalled(), { timeout: 5000, interval: 10 });
      else for (let i = 0; i < 40; i++) await flush();
      const hovered = { chunk: chunk.mock.calls.length, catalog: catalog.mock.calls.length };
      stop();
      link.remove();
      return { idled, hovered };
    }

    it("on: nothing at idle; a link's hover loads its strings and chunk, once", async () => {
      registerModule(counted());
      expect(await warm(true)).toEqual({ idled: { chunk: 0, catalog: 0 }, hovered: { chunk: 1, catalog: 1 } });
    });

    it("off, or behind a flag the hub lacks: nothing ever, idle or hover", async () => {
      const none = { idled: { chunk: 0, catalog: 0 }, hovered: { chunk: 0, catalog: 0 } };
      setModuleSwitchedOff("health", true);
      registerModule(counted());
      expect(await warm(false)).toEqual(none);
      resetModulesForTest();
      setModuleSwitchedOff("health", false);
      registerModule(counted());
      Object.assign(getState(), { loaded: true, features: [MODULES_FEATURE, "other"] });
      expect(await warm(false)).toEqual(none);
    });
  });

  it("draws its water and protein pages (/extension/-health-intake/…) while on; off, they are unknown addresses", async () => {
    registerModule(counted());
    expect(parse("/extension/-health-intake/pages")).toEqual({ name: "extension", extension: "-health-intake", ui: "pages" });
    expect(moduleExtensionAt("-health-intake")).toMatchObject({ id: "health" });
    resetModulesForTest();
    setModuleSwitchedOff("health", true);
    registerModule(counted());
    expect(parse("/extension/-health-intake/pages")).toEqual({ name: "not-found", path: "/extension/-health-intake/pages" });
    act(() => root.render(<ModuleExtensionPage extension="-health-intake" ui="pages" />));
    await flush();
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
  });

  it("declares what it contributes, and its hub flag module.health", () => {
    expect(health.id).toBe("health");
    expect(health.feature).toBe("module.health");
    expect(health.i18nNamespaces).toEqual(["health"]);
    expect(health.chatSlots ?? []).toEqual([]);
    expect(health.settingsTabs ?? []).toEqual([]);
  });
});
