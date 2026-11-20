// @vitest-environment jsdom
// The phone's Agents page keeps its place (#11511): open an Agent from far down the list and Back used to land at the
// top, in every grouping, since the page unmounts under the conversation. It remembers its scroller's spot as Overview,
// Settings and To-dos do (lib/scroll-memory.ts): Back / Forward restore it, a fresh visit starts at the top, and rows
// that arrive after the page mounts (the list read again) still bring it there.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const observers = vi.hoisted(() => {
  const all: { callback: () => void; targets: Element[] }[] = [];
  // every ResizeObserver, to report a size change by hand (jsdom lays nothing out)
  globalThis.ResizeObserver = class {
    entry: { callback: () => void; targets: Element[] };
    constructor(callback: () => void) { this.entry = { callback, targets: [] }; all.push(this.entry); }
    observe(el: Element) { this.entry.targets.push(el); }
    unobserve() {}
    disconnect() { this.entry.targets = []; }
  } as unknown as typeof ResizeObserver;
  globalThis.matchMedia = ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })) as unknown as typeof matchMedia;
  return all;
});
import { navigate, useRoute } from "../lib/router";
import { forgetScrolls } from "../lib/scroll-memory";
import { getState } from "../lib/store";
import { AgentsList } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** The page under the conversation it opens, as App swaps them. */
function Shell() {
  const route = useRoute();
  return route.name === "agent-list" ? <AgentsList route={route} onNewAgent={() => {}} /> : <p>conversation</p>;
}

let host: HTMLDivElement;
let root: Root;
const scroller = () => host.querySelector<HTMLElement>("[data-row-scroller]")!;
const scrollTo = (top: number) => {
  scroller().scrollTop = top;
  scroller().dispatchEvent(new Event("scroll"));
};
const back = () =>
  act(async () => {
    const popped = new Promise((r) => addEventListener("popstate", r, { once: true }));
    history.back();
    await popped;
  });

beforeEach(() => {
  forgetScrolls();
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}"))));
  Object.assign(getState(), { loaded: true, machines: [{ id: "mac", name: "MacBook", online: true }] });
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Object.assign(getState(), { loaded: false, machines: [] });
  vi.unstubAllGlobals();
});

describe("the Agents page's place", () => {
  it("comes back on Back from an Agent it opened", async () => {
    act(() => navigate("/agents"));
    act(() => root.render(<Shell />));
    scrollTo(600);
    act(() => navigate("/s/mac/s1"));
    expect(host.textContent).toBe("conversation");
    await back();
    expect(location.pathname).toBe("/agents");
    expect(scroller().scrollTop).toBe(600);
  });

  it("is the top on a fresh visit", async () => {
    act(() => navigate("/agents"));
    act(() => root.render(<Shell />));
    scrollTo(600);
    act(() => navigate("/s/mac/s1"));
    act(() => navigate("/agents"));
    expect(scroller().scrollTop).toBe(0);
  });

  it("is reached once the rows arrive, when the list came back shorter than the spot", async () => {
    act(() => navigate("/agents"));
    act(() => root.render(<Shell />));
    scrollTo(600);
    act(() => navigate("/s/mac/s1"));
    // the list comes back 200 px short of the spot: the scroller stops at its end until the rows grow it
    let room = 200;
    const own = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop")!;
    Object.defineProperty(Element.prototype, "scrollTop", {
      configurable: true,
      get: own.get,
      set(this: Element, v: number) { own.set!.call(this, this.hasAttribute("data-row-scroller") ? Math.min(v, room) : v); },
    });
    try {
      observers.length = 0;
      await back();
      expect(scroller().scrollTop).toBe(200);
      // the page's content is what the rows grow: the scroller's child the settle loop watches
      const watching = observers.filter((o) => o.targets.includes(scroller().firstElementChild!));
      expect(watching.length).toBeGreaterThan(0);
      room = 2000;
      act(() => { for (const o of watching) o.callback(); });
      expect(scroller().scrollTop).toBe(600);
    } finally {
      Object.defineProperty(Element.prototype, "scrollTop", own);
    }
  });
});
