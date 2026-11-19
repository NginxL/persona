// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The hub link and the messages sent over it, under the test's control.
const hub = vi.hoisted(() => ({
  open: true,
  conn: "online",
  sent: [] as Record<string, unknown>[],
  listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>(),
}));

vi.mock("../../../lib/store", () => ({
  isOpen: () => hub.open,
  getState: () => ({ features: ["herdr_client"], conn: hub.conn, sessions: {} }),
  send: (msg: Record<string, unknown>) => void hub.sent.push(msg),
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));

vi.mock("../../../ui/atoms", () => ({ toast: vi.fn() }));

vi.mock("./cjk", () => ({
  terminalFontReady: () => Promise.resolve(),
  cjkFontStack: () => Promise.resolve(null),
}));

// A stand-in for xterm: only what FullFeed touches on the way to a live view and while typing.
const terms = vi.hoisted(() => [] as { typed: (d: string) => void; resets: number; options: Record<string, unknown> }[]);

vi.mock("./xterm", () => {
  const disposable = () => ({ dispose: () => {} });
  class Terminal {
    cols = 80;
    rows = 24;
    element: HTMLElement | null = null;
    options: Record<string, unknown>;
    modes = { applicationCursorKeysMode: false };
    parser = { registerCsiHandler: disposable };
    resets = 0;
    typed: (d: string) => void = () => {};
    constructor(options: Record<string, unknown>) {
      this.options = options;
      terms.push(this);
    }
    loadAddon() {}
    open(host: HTMLElement) {
      this.element = document.createElement("div");
      host.appendChild(this.element);
    }
    onData(cb: (d: string) => void) {
      this.typed = cb;
      return disposable();
    }
    onSelectionChange = disposable;
    attachCustomKeyEventHandler() {}
    hasSelection = () => false;
    focus() {}
    reset() {
      this.resets++;
    }
    resize(cols: number, rows: number) {
      this.cols = cols;
      this.rows = rows;
    }
    refresh() {}
    write(_data: unknown, cb?: () => void) {
      cb?.();
    }
    paste() {}
    dispose() {}
  }
  class FitAddon {
    proposeDimensions = () => ({ cols: 80, rows: 24 });
  }
  class SearchAddon {
    onDidChangeResults = disposable;
  }
  class ClipboardAddon {}
  class WebLinksAddon {}
  return { Terminal, FitAddon, SearchAddon, ClipboardAddon, WebLinksAddon, matchHerdrWidths: () => {} };
});

import { FullFeed } from "./full";

class FakeResizeObserver {
  observe() {}
  disconnect() {}
}

function deliver(type: string, msg: Record<string, unknown>) {
  hub.listeners.get(type)?.forEach((l) => l({ type, ...msg }));
}

const opens = () => hub.sent.filter((m) => m.type === "herdr_open");
const inputs = () => hub.sent.filter((m) => m.type === "herdr_input");

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  localStorage.setItem("persona.terminal.fullHint", "1");
  hub.open = true;
  hub.conn = "online";
  hub.sent.length = 0;
  hub.listeners.clear();
  terms.length = 0;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

async function liveFeed() {
  const feed = new FullFeed("m1", "s1");
  const host = document.body.appendChild(document.createElement("div"));
  await feed.mount(host);
  feed.claim({}, host, host, "s1");
  expect(opens()).toHaveLength(1);
  const id = opens()[0].id;
  deliver("herdr_state", { id, cols: 80, rows: 24 });
  expect(feed.snapshot().status).toBe("live");
  expect(feed.snapshot().control).toBe("active");
  return { feed, id, term: terms[0] };
}

describe("FullFeed and the hub link", () => {
  it("stops being live when the hub link drops, and opens a fresh view on the hub's hello", async () => {
    const { feed, id, term } = await liveFeed();
    term.typed("a");
    expect(inputs()).toHaveLength(1); // the control: keys go in while live

    hub.open = false;
    vi.advanceTimersByTime(2000);
    const v = feed.snapshot();
    expect(v.status).not.toBe("live");
    expect(v.status).toBe("connecting");
    expect(v.control).toBe("off");
    expect(v.note).not.toBe("");
    expect(feed.driving).toBe(false);

    hub.sent.length = 0;
    term.typed("b");
    feed.pressKey("enter");
    expect(hub.sent).toEqual([]); // nothing is typed into a client the hub has already stopped

    const resets = term.resets;
    hub.open = true;
    deliver("hello", {});
    expect(opens()).toHaveLength(1);
    expect(opens()[0].id).not.toBe(id);
    expect(term.resets).toBe(resets + 1); // the new client draws on a clean screen
    expect(feed.snapshot().note).toBe("");
    feed.dispose();
  });

  it("drops the view when the hub says it is offline though the socket is still open", async () => {
    const { feed } = await liveFeed();
    hub.conn = "offline";
    vi.advanceTimersByTime(2000);
    expect(feed.snapshot().status).toBe("connecting");
    expect(feed.snapshot().control).toBe("off");
    feed.dispose();
  });

  it("leaves a live view alone while the link stays up, and stops checking once disposed", async () => {
    const { feed } = await liveFeed();
    vi.advanceTimersByTime(5000);
    expect(feed.snapshot().status).toBe("live");
    feed.dispose();
    expect(vi.getTimerCount()).toBe(0);
  });
});

it("uses the current mono family on mount and when a retained feed attaches to a new surface, without changing size", async () => {
  const feed = new FullFeed("m1", "s1");
  const first = document.body.appendChild(document.createElement("div"));
  first.style.fontFamily = '"Menlo", monospace';
  await feed.mount(first);
  const term = terms[0];
  expect(term.options.fontFamily).toBe('"Menlo", monospace');
  const size = term.options.fontSize;
  const second = document.body.appendChild(document.createElement("div"));
  second.style.fontFamily = '"Consolas", monospace';
  feed.claim({}, second, second, "s1");
  await Promise.resolve(); await Promise.resolve();
  expect(term.options.fontFamily).toBe('"Consolas", monospace');
  expect(term.options.fontSize).toBe(size);
  feed.dispose();
});
