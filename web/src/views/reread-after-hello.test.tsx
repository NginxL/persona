// @vitest-environment jsdom
// The hub replays the broadcasts a page missed only when it resumes the stream (hello `resumed`, docs/operations/hub-restart.md
// "Event resume"). After a hello it could not resume, what these pages follow by a push is read again: "Scheduled tasks"
// (assistant_schedule), the looks and the chosen one (personas, look), Settings › hub (hub_update), and Settings › Extensions' list and
// an extension's sign-in (extension_auth). A resumed hello reads nothing again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({
  listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(),
  reads: [] as string[],
  /** When set, GET /looks waits for it (a read still on its way when the socket dropped). */
  holdLooks: null as Promise<void> | null,
}));
vi.mock("../lib/api", async (original) => {
  const real = await original<typeof import("../lib/api")>();
  return {
    ...real,
    getToken: () => "",
    api: vi.fn(async (path: string, init?: { method?: string }) => {
      if (init?.method && init.method !== "GET") return {};
      hub.reads.push(path);
      if (path === "/assistant/tasks") return { tasks: [] };
      if (path === "/assistant/runs") return { runs: [] };
      if (path === "/looks") await hub.holdLooks;
      if (path === "/looks") return { looks: [{ id: "persona", source: "builtin", name: "persona", app_icon: "persona" }], current: { id: null, effective: "persona" } };
      if (path === "/hub/update") return { version: "1.0.0", build: 1, channel: "release", can_update: true, state: "idle" };
      if (path === "/hub/update/history") return { history: [] };
      if (path === "/extensions" || path === "/plugins") return { extensions: [] };
      if (/^\/(extensions|plugins)\/[^/]+\/-\/\?entry_lists=1$/.test(path)) return { id: "example-ext", name: "示例扩展", kind: "process", status: "running", source: "installed", enabled: true, auth: { title: "Account", state: "signed_in" } };
      if (/^\/(extensions|plugins)\/[^/]+\/-\/auth$/.test(path)) return { state: "signed_in" };
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
vi.mock("../lib/extension-ui", async (original) => ({
  ...(await original<typeof import("../lib/extension-ui")>()),
  renderUI: async () => {
    hub.reads.push("render");
    return { sections: [{ items: [{ type: "text", text: "正文" }] }] };
  },
}));
vi.mock("../ui/atoms", async (original) => ({ ...(await original<typeof import("../ui/atoms")>()), toast: vi.fn() }));
vi.stubGlobal("Image", class { onload: (() => void) | null = null; onerror: (() => void) | null = null; set src(_: string) { queueMicrotask(() => this.onerror?.()); } });
import { Routines } from "./assistant/Routines";
import { refreshPersonas } from "./assistant/Persona";
import { HubSettings } from "./settings/HubSettings";
import { Extensions } from "./settings/Extensions";
import { ExtensionUI } from "./settings/ExtensionView";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const hello = async (resumed: boolean) => {
  await act(async () => hub.listeners.get("hello")?.forEach((l) => l({ type: "hello", resumed })));
  // past a page's push debounce (300 ms), on fake timers rather than the clock
  await act(async () => { await vi.advanceTimersByTimeAsync(450); });
  await flush();
};
const count = (path: string | RegExp) => hub.reads.filter((p) => (typeof path === "string" ? p === path : path.test(p))).length;

beforeEach(() => {
  // only the debounces' timers: Date stays real (an extension view's at-most-once-a-second check), and promises,
  // microtasks (the Image stub) and intervals run as before
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  hub.reads = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("reads again after a hello the hub could not resume", () => {
  it("定时任务: the tasks and the runs", async () => {
    act(() => root.render(<Routines onToggleSidebar={() => {}} />));
    await flush();
    expect([count("/assistant/tasks"), count("/assistant/runs")]).toEqual([1, 1]);
    await hello(true);
    expect([count("/assistant/tasks"), count("/assistant/runs")]).toEqual([1, 1]);
    await hello(false);
    expect([count("/assistant/tasks"), count("/assistant/runs")]).toEqual([2, 2]);
  });

  it("the looks and the chosen one", async () => {
    await refreshPersonas();
    expect(count("/looks")).toBe(1);
    await hello(true);
    expect(count("/looks")).toBe(1);
    await hello(false);
    expect(count("/looks")).toBe(2);
  });

  it("the looks: a read already on its way when the socket dropped is followed by another", async () => {
    await refreshPersonas();
    let release!: () => void;
    hub.holdLooks = new Promise<void>((r) => (release = r));
    void refreshPersonas(); // a personas push's read, on its way
    await flush();
    expect(count("/looks")).toBe(2);
    await hello(false);
    hub.holdLooks = null;
    await act(async () => release());
    await flush();
    expect(count("/looks")).toBe(3);
  });

  it("an extension's view: drawn again", async () => {
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
    act(() => root.render(<ExtensionUI extension="example-ext" ui="main" history={false} />));
    await flush();
    expect(count("render")).toBe(1);
    await hello(true);
    expect(count("render")).toBe(1);
    await hello(false);
    expect(count("render")).toBe(2);
  });

  it("设置 › hub: the update's state and the history", async () => {
    act(() => root.render(<HubSettings />));
    await flush();
    expect([count("/hub/update"), count("/hub/update/history")]).toEqual([1, 1]);
    await hello(true);
    expect([count("/hub/update"), count("/hub/update/history")]).toEqual([1, 1]);
    await hello(false);
    expect([count("/hub/update"), count("/hub/update/history")]).toEqual([2, 2]);
  });

  it("设置 › 扩展: the list", async () => {
    act(() => root.render(<Extensions open={null} onOpen={() => {}} />));
    await flush();
    const list = /^\/(extensions|plugins)$/;
    expect(count(list)).toBe(1);
    await hello(true);
    expect(count(list)).toBe(1);
    await hello(false);
    expect(count(list)).toBe(2);
  });

  it("an extension's page: its sign-in, not the whole page (what is typed into its settings stays)", async () => {
    act(() => root.render(<Extensions open="example-ext" onOpen={() => {}} />));
    await flush();
    const detail = /^\/(extensions|plugins)\/example-ext\/-\/\?entry_lists=1$/;
    const auth = /^\/(extensions|plugins)\/example-ext\/-\/auth$/;
    const before = [count(detail), count(auth)];
    expect(before[0]).toBe(1);
    await hello(true);
    expect([count(detail), count(auth)]).toEqual(before);
    await hello(false);
    expect([count(detail), count(auth)]).toEqual([before[0], before[1] + 1]);
  });
});
