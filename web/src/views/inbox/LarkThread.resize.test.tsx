// @vitest-environment jsdom
// A Feishu chat whose box gets shorter (the keyboard, a longer draft) or whose messages grow: a reader at the newest
// message keeps it in view; one who scrolled up, or a jump of the list's own, is left where it is.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LarkMessage } from "../../lib/inbox";
import { LarkThread } from "./LarkThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const msg = (n: number): LarkMessage => ({ id: `m${n}`, position: String(n), sender_name: "示例同事", created_at: "2026-11-01T09:00:00Z", text: `消息 ${n}` });
const range = (a: number, b: number) => Array.from({ length: b - a + 1 }, (_, i) => msg(a + i));

/** A ResizeObserver the test fires by hand, for whatever watches the element. */
class FakeResize {
  static all: FakeResize[] = [];
  cb: ResizeObserverCallback;
  targets = new Set<Element>();
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb;
    FakeResize.all.push(this);
  }
  observe(el: Element) { this.targets.add(el); }
  unobserve(el: Element) { this.targets.delete(el); }
  disconnect() { this.targets.clear(); }
  static fire(el: Element) {
    for (const o of FakeResize.all) if (o.targets.has(el)) o.cb([], o as unknown as ResizeObserver);
  }
}

/** jsdom has no layout: the list's sizes as the test sets them, its scroll clamped as a browser's is. */
function sizes(el: HTMLElement, m: { scrollHeight: number; clientHeight: number; scrollTop: number }) {
  Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => m.scrollHeight });
  Object.defineProperty(el, "clientHeight", { configurable: true, get: () => m.clientHeight });
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => m.scrollTop,
    set: (v: number) => (m.scrollTop = Math.max(0, Math.min(v, m.scrollHeight - m.clientHeight))),
  });
  return m;
}

let host: HTMLDivElement;
let root: Root;
let gate: Promise<void> | null = null;
const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  FakeResize.all = [];
  gate = null;
  vi.stubGlobal("IntersectionObserver", class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } });
  vi.stubGlobal("ResizeObserver", FakeResize);
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollIntoView = vi.fn();
  Element.prototype.scrollTo = vi.fn();
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    let body: unknown = {};
    if (/\/conversations\/f1\/messages\?/.test(url) && (init?.method ?? "GET") === "GET") {
      const q = new URL(url, "http://hub").searchParams;
      const around = q.get("around");
      // a window read around a position waits for the test while one is held
      if (around && gate) await gate;
      body = q.get("before")
        ? { messages: [], has_older: true, has_newer: true }
        : around
        ? { messages: range(Math.max(1, Number(around) - 5), Number(around) + 5), has_older: true, has_newer: true, older_cursor: String(Math.max(1, Number(around) - 5)), newer_cursor: String(Number(around) + 5) }
        : { messages: range(181, 200), has_older: true, has_newer: false, older_cursor: "181" };
    } else if (url.includes("/read-states")) body = { states: [] };
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const list = () => host.querySelector<HTMLElement>('[data-mid="m200"], [data-mid="m150"]')!.closest<HTMLElement>(".overflow-y-auto")!;
const pill = (testId: string) => host.querySelector<HTMLButtonElement>(`[data-testid="${testId}"]`)!;
/** The reader scrolls the list to `top`. */
const scrollTo = (el: HTMLElement, m: { scrollTop: number }, top: number) => act(() => {
  m.scrollTop = top;
  el.dispatchEvent(new Event("scroll"));
});
const resize = (el: HTMLElement) => act(() => FakeResize.fire(el));

describe("a Feishu chat whose box changes size", () => {
  it("watches the list and its messages, and lets go when it closes", async () => {
    act(() => root.render(<LarkThread chat={{ feed: "f1", title: "群" }} />));
    await flush();
    const el = list();
    const watcher = FakeResize.all.find((o) => o.targets.has(el))!;
    expect(watcher.targets.has(el.firstElementChild!)).toBe(true);
    act(() => root.unmount());
    expect(watcher.targets.size).toBe(0);
    root = createRoot(host);
  });

  it("at the newest message keeps it in view as the box gets shorter or the messages grow", async () => {
    act(() => root.render(<LarkThread chat={{ feed: "f1", title: "群" }} />));
    await flush();
    const el = list();
    const m = sizes(el, { scrollHeight: 2000, clientHeight: 600, scrollTop: 0 });
    scrollTo(el, m, 1400);
    m.clientHeight = 300; // the keyboard up
    resize(el);
    expect(m.scrollTop).toBe(1700);
    m.clientHeight = 220; // a five-line draft
    resize(el);
    expect(m.scrollTop).toBe(1780);
    m.scrollHeight = 2200; // a picture loaded
    resize(el);
    expect(m.scrollTop).toBe(1980);
  });

  it("scrolled up, stays where the reader is", async () => {
    act(() => root.render(<LarkThread chat={{ feed: "f1", title: "群" }} />));
    await flush();
    const el = list();
    const m = sizes(el, { scrollHeight: 2000, clientHeight: 600, scrollTop: 0 });
    scrollTo(el, m, 800);
    m.clientHeight = 300;
    resize(el);
    expect(m.scrollTop).toBe(800);
  });

  it("leaves a jump to another window alone on its way, and where it lands", async () => {
    act(() => root.render(<LarkThread chat={{ feed: "f1", title: "群", unreadFrom: "150", mention: "60" }} />));
    await flush();
    const el = list();
    const m = sizes(el, { scrollHeight: 2000, clientHeight: 600, scrollTop: 0 });
    scrollTo(el, m, 1400);
    let open!: () => void;
    gate = new Promise((r) => (open = r));
    act(() => pill("lark-jump-mention").click());
    await flush();
    let during = -1;
    await act(async () => {
      open();
      for (let i = 0; i < 3; i++) await new Promise((r) => setTimeout(r, 0));
      // the window around the mention has come back and waits to be drawn: a resize now does not pull it to the end
      m.clientHeight = 300;
      FakeResize.fire(el);
      during = m.scrollTop;
    });
    expect(during).toBe(1400);
    await flush();
    expect(host.querySelector('[data-mid="m60"]')).not.toBeNull();
    const landed = m.scrollTop;
    expect(landed).toBeLessThan(1700);
    m.clientHeight = 220;
    resize(el);
    expect(m.scrollTop).toBe(landed);
  });

  it("leaves a jump within the window alone while it glides", async () => {
    act(() => root.render(<LarkThread chat={{ feed: "f1", title: "群", mention: "190" }} />));
    await flush();
    const el = list();
    const m = sizes(el, { scrollHeight: 2000, clientHeight: 600, scrollTop: 0 });
    scrollTo(el, m, 1400);
    act(() => pill("lark-jump-mention").click());
    expect(el.scrollTo).toHaveBeenCalled();
    m.clientHeight = 300; // the box changing under the glide
    resize(el);
    expect(m.scrollTop).toBe(1400);
  });
});
