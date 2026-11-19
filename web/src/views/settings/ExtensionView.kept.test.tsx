// @vitest-environment jsdom
// An extension page on reload (docs/web/web.md "Extension pages on reload"): the full page draws its last view at once,
// inert and marked as updating, before the hello and while the extension draws it again, and the drawing replaces it
// and is kept for the next load. Never another manager's or another UI version's, never a form typed into.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { UIView } from "../../lib/extension-ui";

const gate = vi.hoisted(() => ({
  ready: false, features: [] as string[], listeners: new Set<() => void>(), draws: [] as Array<(v: unknown) => void>,
}));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    getState: () => ({ ...real.getState(), features: gate.features }),
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), features: gate.features } as ReturnType<typeof real.getState>),
    appSocketReady: () => gate.ready,
    onAppSocket: (listener: () => void) => { gate.listeners.add(listener); return () => { gate.listeners.delete(listener); }; },
  };
});
vi.mock("../../lib/extension-ui", async (original) => ({
  ...(await original<typeof import("../../lib/extension-ui")>()),
  // the extension answers when the test says
  renderUI: () => new Promise((resolve) => gate.draws.push(resolve)),
}));

import { ExtensionUI } from "./ExtensionView";
import { keepView, readKeptView } from "../../lib/extension-view-cache";
import { feedCacheScope, scopedKey } from "../../lib/feed-cache";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { loadPages } from "../../lib/lazy-page";
import { UI_VERSION } from "../../lib/extension-ui";
import { currentLanguage, t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// the language the page is drawn in (the test environment's)
const page = { extension: "example-today", ui: "today", state: "", language: currentLanguage() };
const view = (text: string): UIView => ({ title: "Today", sections: [{ title: "Calendar", items: [{ type: "text", text }] }] });
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
async function hello(features: string[]) {
  await act(async () => { gate.features = features; gate.ready = true; gate.listeners.forEach((fn) => fn()); });
  await flush();
}
async function answer(v: UIView) {
  await act(async () => gate.draws.shift()!(v));
  await flush();
}
const kept = () => host.querySelector("[data-kept-view]");
const mount = async (revision?: string) => {
  await act(async () => root.render(<ExtensionUI extension="example-today" ui="today" bare remember revision={revision} />));
  await flush();
};

// the extension page and the view renderer are their own chunks (lib/lazy-page.ts): in before anything is drawn
beforeAll(() => loadPages(), 60_000);

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("persona.token", "synthetic-token");
  gate.ready = false;
  gate.features = [];
  gate.listeners.clear();
  gate.draws = [];
  resetManagerChoice();
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("an extension page on reload", () => {
  it("draws its last view before the hello, inert and marked as updating, and the extension's drawing replaces it", async () => {
    keepView({ ...page, version: UI_VERSION, manager: "m-1" }, view("Weekly sync at 14:00"));
    await mount();
    expect(kept()?.textContent).toContain("Weekly sync at 14:00");
    expect(kept()?.querySelector("[role=status]")?.textContent).toBe(t("settings-extensionview.updating"));
    // nothing in it can be pressed or focused
    expect(kept()?.querySelector("[inert]")?.textContent).toContain("Weekly sync at 14:00");

    // a hub drawing over HTTP: still the kept view while the extension draws
    await hello(["extension_ui_slots"]);
    expect(gate.draws).toHaveLength(1);
    expect(kept()?.textContent).toContain("Weekly sync at 14:00");

    act(() => setRosterDefault("m-1"));
    await answer(view("Release review at 17:30"));
    expect(kept()).toBeNull();
    expect(host.textContent).toContain("Release review at 17:30");
    expect(host.querySelector("[role=status]")).toBeNull();
    // kept for the next load
    expect(readKeptView(page, [UI_VERSION], "m-1")?.view.sections[0].items[0]).toMatchObject({ text: "Release review at 17:30" });
  });

  it("never draws another manager's last view", async () => {
    keepView({ ...page, version: UI_VERSION, manager: "m-2" }, view("of m-2"));
    act(() => setRosterDefault("m-1"));
    await mount();
    expect(kept()).toBeNull();
    expect(host.textContent).not.toContain("of m-2");
  });

  it("withdraws a last view once the roster names another default", async () => {
    keepView({ ...page, version: UI_VERSION, manager: "m-2" }, view("of m-2"));
    await mount();
    expect(kept()?.textContent).toContain("of m-2");
    act(() => setRosterDefault("m-1"));
    await flush();
    expect(kept()).toBeNull();
  });

  it("never draws a last view of a UI version the page is not drawn with now", async () => {
    // drawn over the socket; this hub draws over HTTP only
    keepView({ ...page, version: 7, manager: "m-1" }, view("native drawing"));
    act(() => setRosterDefault("m-1"));
    await mount();
    expect(kept()?.textContent).toContain("native drawing"); // before the hello it may be either
    await hello(["extension_ui_slots"]);
    expect(kept()).toBeNull();
    expect(host.textContent).not.toContain("native drawing");
  });

  it("does not keep a form while it holds what was typed and not sent", async () => {
    act(() => setRosterDefault("m-1"));
    await mount();
    await hello(["extension_ui_slots"]);
    const form = (note: string): UIView => ({ sections: [{ items: [{ type: "field", kind: "text", name: "note", label: "Note", value: note }] }] });
    await answer(form("first"));
    expect(readKeptView(page, [UI_VERSION], "m-1")?.view.sections[0].items[0]).toMatchObject({ value: "first" });
    const input = host.querySelector("input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "typed, not sent");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    // the page is drawn again (what it is drawn for changed): what was typed stays, and nothing is kept meanwhile
    await mount("2");
    expect(gate.draws).toHaveLength(1);
    await answer(form("second"));
    expect(input.value).toBe("typed, not sent");
    expect(readKeptView(page, [UI_VERSION], "m-1")?.view.sections[0].items[0]).toMatchObject({ value: "first" });
  });

  it("draws the header from the kept places and the body from the kept view at first paint", async () => {
    // what the load before kept: the sidebar's list (lib/extension-slots.ts) and the page's view
    localStorage.setItem(scopedKey(feedCacheScope(), "ext-places")!, JSON.stringify({
      manager: "m-1", places: [{ extension: "example-today", id: "today", placement: "nav_page", title: "Today", icon: "calendar" }],
    }));
    keepView({ ...page, version: UI_VERSION, manager: "m-1" }, view("Weekly sync at 14:00"));
    vi.resetModules();
    const fresh = await import("../extensions/ExtensionSlots");
    await (await import("../../lib/lazy-page")).loadPages();
    await act(async () => root.render(<fresh.ExtensionNavPage extension="example-today" ui="today" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("header h1")?.textContent).toBe("Today");
    expect(host.querySelector("header svg")).not.toBeNull();
    expect(host.querySelector("[data-kept-view]")?.textContent).toContain("Weekly sync at 14:00");
  });

  it("is as wide as the drawing it stands for", async () => {
    keepView({ ...page, version: UI_VERSION, manager: "m-1" }, { ...view("Needs me"), layout: "columns" });
    const onWide = vi.fn();
    await act(async () => root.render(<ExtensionUI extension="example-today" ui="today" bare remember onWide={onWide} />));
    await flush();
    expect(kept()).not.toBeNull();
    expect(onWide).toHaveBeenLastCalledWith(true);
  });
});

describe("an Overview card on reload (#11091)", () => {
  const card = { extension: "example-today", ui: "card", state: "", language: currentLanguage() };
  const mountCard = async () => {
    await act(async () => root.render(<div data-card className="flex flex-col"><ExtensionUI extension="example-today" ui="card" context={{ size: "compact" }} compact remember /></div>));
    await flush();
  };

  it("draws its last view at once, inert, where the drawing's own sections go, and the extension's drawing replaces it", async () => {
    keepView({ ...card, version: UI_VERSION, manager: "m-1" }, view("3 reviews wait"));
    await hello(["extension_ui_slots"]);
    await mountCard();
    expect(gate.draws).toHaveLength(1);
    expect(kept()?.textContent).toContain("3 reviews wait");
    expect(kept()?.querySelector("[inert]")?.textContent).toContain("3 reviews wait");
    // no box of its own between the card's column and its sections, and no visible "Updating" line to move them
    expect(kept()?.className).toBe("contents");
    expect(kept()?.querySelector("[inert]")?.className).toBe("contents");
    const status = kept()?.querySelector("[role=status]");
    expect(status?.textContent).toBe(t("settings-extensionview.updating"));
    expect(status?.className).toBe("sr-only");
    // heard by a screen reader: not inside what is inert
    expect(status?.closest("[inert]")).toBeNull();

    act(() => setRosterDefault("m-1"));
    await answer(view("1 review waits"));
    expect(kept()).toBeNull();
    expect(host.textContent).toContain("1 review waits");
    expect(host.textContent).not.toContain("3 reviews wait");
    expect(readKeptView(card, [UI_VERSION], "m-1")?.view.sections[0].items[0]).toMatchObject({ text: "1 review waits" });
  });

  it("starts from Loading when the card was never drawn here, and keeps a page's view apart from the card's", async () => {
    keepView({ ...page, version: UI_VERSION, manager: "m-1" }, view("the page, not the card"));
    await hello(["extension_ui_slots"]);
    await mountCard();
    expect(kept()).toBeNull();
    expect(host.textContent).not.toContain("the page, not the card");
  });
});

