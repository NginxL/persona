// @vitest-environment jsdom
// Settings › Labs › Terminal renderer in a Single pane terminal (feed.ts, lib/labs.ts): a view reads the choice once when
// it is made, so a change in Labs leaves open terminals (and what is typed into them) alone and applies to the next one;
// WebGL failing in a view falls back to the DOM renderer in that view, repaints it, keeps input flowing and sets Labs back
// to Standard, saying so (and saying when that couldn't be saved).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({
  open: true,
  conn: "online",
  sent: [] as Record<string, unknown>[],
  machines: [] as { id: string; name: string; online: boolean }[],
  listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>(),
}));

vi.mock("../../../lib/store", () => ({
  getState: () => ({ features: ["terminal_stream", "terminal_control"], conn: hub.conn, machines: hub.machines }),
  isOpen: () => hub.open,
  send: (msg: Record<string, unknown>) => {
    if (hub.open) hub.sent.push(msg); // the real store drops what it cannot send
  },
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    const set = hub.listeners.get(type) ?? new Set();
    set.add(l);
    hub.listeners.set(type, set);
    return () => set.delete(l);
  },
}));

const typed = vi.hoisted(() => ({ onData: null as ((d: string) => void) | null, refreshed: [] as [number, number][] }));

vi.mock("./xterm", () => {
  const disposable = { dispose() {} };
  class Terminal {
    options: Record<string, unknown>;
    cols = 80;
    rows = 24;
    element: HTMLElement | undefined;
    modes = { applicationCursorKeysMode: false };
    constructor(options: Record<string, unknown>) {
      this.options = { ...options };
    }
    loadAddon(addon: { activate?: (t: unknown) => void }) {
      addon.activate?.(this);
    }
    refresh(start: number, end: number) {
      typed.refreshed.push([start, end]);
    }
    open(host: HTMLElement) {
      this.element = host;
    }
    onData(cb: (d: string) => void) {
      typed.onData = cb;
      return disposable;
    }
    onResize() {
      return disposable;
    }
    onWriteParsed() {
      return disposable;
    }
    onSelectionChange() {
      return disposable;
    }
    attachCustomKeyEventHandler() {}
    attachCustomWheelEventHandler() {}
    resize(cols: number, rows: number) {
      this.cols = cols;
      this.rows = rows;
    }
    reset() {}
    write() {}
    focus() {}
    blur() {}
    hasSelection() {
      return false;
    }
    clearSelection() {}
    dispose() {}
  }
  class FitAddon {
    proposeDimensions() {
      return { cols: 100, rows: 30 };
    }
  }
  class SearchAddon {
    onDidChangeResults() {
      return disposable;
    }
  }
  return { Terminal, FitAddon, SearchAddon, ClipboardAddon: class {}, WebLinksAddon: class {}, matchHerdrWidths: () => {} };
});

vi.mock("./cjk", () => ({ cjkFontStack: () => Promise.resolve(null), terminalFontReady: () => Promise.resolve() }));

const gl = vi.hoisted(() => ({
  made: [] as { lose: () => void; disposed: boolean }[],
  fail: false as false | "import" | "activate",
}));

vi.mock("@xterm/addon-webgl", () => {
  if (gl.fail === "import") throw new Error("no chunk");
  class WebglAddon {
    disposed = false;
    private loss: (() => void) | null = null;
    constructor() {
      gl.made.push(this as unknown as { lose: () => void; disposed: boolean });
    }
    activate() {
      if (gl.fail === "activate") throw new Error("WebGL2 not supported");
    }
    onContextLoss(cb: () => void) {
      this.loss = cb;
      return { dispose() {} };
    }
    lose() {
      this.loss?.();
    }
    dispose() {
      this.disposed = true;
    }
  }
  return { WebglAddon };
});

const toasts = vi.hoisted(() => [] as string[]);
vi.mock("../../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../../ui/atoms")>()), toast: (text: string) => toasts.push(text) }));

import { TerminalFeed } from "./feed";
import { readTerminalRenderer, writeTerminalRenderer } from "../../../lib/labs";
import { t } from "../../../i18n";

const KEY = "persona.terminal.webgl";

function emit(type: string, msg: Record<string, unknown>) {
  hub.listeners.get(type)?.forEach((l) => l(msg));
}

const inputs = () => hub.sent.filter((m) => m.type === "terminal_input" && "text" in m);
/** Lets the addon's dynamic import and its loading finish. */
const settle = async () => {
  for (let i = 0; i < 5; i++) await Promise.resolve();
  await vi.dynamicImportSettled();
};

describe("the terminal renderer chosen in Labs", () => {
  const feeds: TerminalFeed[] = [];
  async function open(sid = "s1") {
    const feed = new TerminalFeed("m1", sid);
    feeds.push(feed);
    await feed.mount(document.createElement("div"), document.createElement("div"));
    await settle();
    return feed;
  }

  beforeEach(() => {
    hub.open = true;
    hub.conn = "online";
    hub.sent = [];
    hub.machines = [];
    hub.listeners.clear();
    typed.onData = null;
    typed.refreshed = [];
    gl.made = [];
    gl.fail = false;
    toasts.length = 0;
    localStorage.clear();
    globalThis.ResizeObserver ??= class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  });

  afterEach(() => {
    feeds.splice(0).forEach((f) => f.dispose());
    localStorage.clear();
  });

  it("draws with the standard renderer by default and loads no WebGL", async () => {
    const feed = await open();
    expect(feed.snapshot()).toMatchObject({ rendererChosen: "standard", renderer: "standard", rendererFellBack: false });
    expect(gl.made).toHaveLength(0);
  });

  it("uses WebGL in a terminal opened with the old Terminal settings switch saved", async () => {
    localStorage.setItem(KEY, "1");
    const feed = await open();
    expect(gl.made).toHaveLength(1);
    expect(feed.snapshot()).toMatchObject({ rendererChosen: "webgl", renderer: "webgl", rendererFellBack: false });
  });

  it("leaves an open terminal as it is when Labs changes, and the next terminal opened follows Labs", async () => {
    const first = await open("s1");
    emit("terminal_control", { machine_id: "m1", session_id: "s1", state: "active" });
    typed.onData!("echo hi");
    expect(writeTerminalRenderer("webgl").ok).toBe(true);
    await settle();
    expect(first.snapshot()).toMatchObject({ rendererChosen: "standard", renderer: "standard" });
    expect(gl.made).toHaveLength(0);
    typed.onData!("\r");
    expect(inputs().map((m) => m.text)).toEqual(["echo hi", "\r"]); // nothing typed was dropped or replayed

    const second = await open("s2");
    expect(second.snapshot()).toMatchObject({ rendererChosen: "webgl", renderer: "webgl" });
    expect(gl.made).toHaveLength(1);

    expect(writeTerminalRenderer("standard").ok).toBe(true);
    await settle();
    expect(second.snapshot().renderer).toBe("webgl"); // still WebGL until it is opened again
    expect(gl.made[0].disposed).toBe(false);
  });

  it("falls back in that view when the GPU context is lost: disposes, repaints, keeps input, sets Labs back to Standard", async () => {
    localStorage.setItem(KEY, "1");
    const feed = await open();
    emit("terminal_control", { machine_id: "m1", session_id: "s1", state: "active" });
    gl.made[0].lose();
    expect(gl.made[0].disposed).toBe(true);
    expect(typed.refreshed).toEqual([[0, 29]]); // the whole fitted grid (30 rows) repaints
    expect(feed.snapshot()).toMatchObject({ rendererChosen: "webgl", renderer: "standard", rendererFellBack: true });
    expect(readTerminalRenderer()).toEqual({ value: "standard", source: "default", raw: null });
    expect(toasts).toEqual([t("session-terminal-feed.webgl_lost")]);
    typed.onData!("still typing");
    expect(inputs().map((m) => m.text)).toEqual(["still typing"]);

    gl.made[0].lose(); // a second loss event says nothing more
    expect(toasts).toHaveLength(1);
  });

  it("falls back when the browser can't start WebGL, disposing the half-made addon", async () => {
    localStorage.setItem(KEY, "1");
    gl.fail = "activate";
    const feed = await open();
    expect(gl.made).toHaveLength(1);
    expect(gl.made[0].disposed).toBe(true);
    expect(feed.snapshot()).toMatchObject({ renderer: "standard", rendererFellBack: true });
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(toasts).toEqual([t("session-terminal-feed.webgl_unavailable")]);
  });

  it("still falls back, and says Standard wasn't saved, when the browser won't keep the change", async () => {
    localStorage.setItem(KEY, "1");
    const feed = await open();
    const remove = vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    gl.made[0].lose();
    remove.mockRestore();
    expect(feed.snapshot()).toMatchObject({ renderer: "standard", rendererFellBack: true });
    expect(localStorage.getItem(KEY)).toBe("1");
    expect(toasts).toEqual([t("session-terminal-feed.webgl_lost_unsaved")]);
  });

  it("an open terminal that fell back stays on Standard, even if Labs goes back to WebGL", async () => {
    localStorage.setItem(KEY, "1");
    const feed = await open();
    gl.made[0].lose();
    writeTerminalRenderer("webgl");
    await settle();
    expect(feed.snapshot()).toMatchObject({ renderer: "standard", rendererFellBack: true });
    expect(gl.made).toHaveLength(1);
  });
});
