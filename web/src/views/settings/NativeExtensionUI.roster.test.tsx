// @vitest-environment jsdom
// NativeExtensionUI uses the already-loaded managers roster instead of firing a blocking GET /managers on every page
// open, and pages that fall back from the native stream to HTTP reopen over HTTP immediately on revisit.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { UIStreamRequest } from "../../lib/extension-wire";

type Envelope = { type: string; manager: string; request: UIStreamRequest };
const seam = vi.hoisted(() => ({
  sent: [] as Envelope[],
  frames: new Set<(raw: string) => void>(),
  draws: vi.fn(),
  managersFetches: 0,
}));

vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  appSocketReady: () => true,
  onAppSocket: () => () => {},
  send: (envelope: Envelope) => seam.sent.push(envelope),
  onPrivateUIStream: (fn: (raw: string) => void) => {
    seam.frames.add(fn);
    return () => { seam.frames.delete(fn); };
  },
}));
vi.mock("../../lib/extension-ui", async (original) => ({
  ...(await original<typeof import("../../lib/extension-ui")>()),
  renderUI: async (_extension: string, ui: string) => {
    seam.draws(ui);
    return { sections: [{ items: [{ type: "text", text: `HTTP ${ui}` }] }] };
  },
}));

import { setToken } from "../../lib/api";
import { resetManagerChoice } from "../../lib/manager-choice";
import { resetRoster, setRoster } from "../../lib/managers";
import { loadPages } from "../../lib/lazy-page";
import { getState } from "../../lib/store";
import { ExtensionUI } from "./ExtensionView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const initial = { ...getState() };
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};

beforeAll(() => loadPages(), 60_000);

beforeEach(() => {
  seam.sent.length = 0;
  seam.frames.clear();
  seam.draws.mockClear();
  seam.managersFetches = 0;
  localStorage.clear();
  resetRoster();
  resetManagerChoice();
  setToken("synthetic-roster-token");
  Object.assign(getState(), { features: ["extensions", "extension_ui_slots", "extension_ui_stream_v1"], mesh: [], interactions: {} });
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (String(url).endsWith("/managers")) {
      seam.managersFetches++;
      return new Response(JSON.stringify({ default: "assistant", managers: [{ id: "assistant", name: "Personal", online: true, builtin: true }] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    throw new Error(`Unexpected fetch: ${url}`);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  resetRoster();
  resetManagerChoice();
  setToken("");
  Object.assign(getState(), initial);
  vi.unstubAllGlobals();
});

describe("extension nav_page immediate open without blocking on managers", () => {
  it("opens a native page immediately using the loaded roster without fetching /managers again", async () => {
    setRoster({ default: "assistant", managers: [{ id: "assistant", name: "Personal", online: true, builtin: true }] });
    await act(async () => root.render(<ExtensionUI extension="demo" ui="board" bare history={false} />));
    await flush();
    expect(seam.managersFetches).toBe(0);
    expect(seam.sent.filter((e) => e.request.request === "open")).toHaveLength(1);
    expect(seam.sent[0]).toMatchObject({ manager: "assistant", request: { request: "open", extension: "demo", contribution: "board" } });
  });

  it("remembers HTTP fallback for the session so revisiting the page dispatches renderUI immediately", async () => {
    setRoster({ default: "assistant", managers: [{ id: "assistant", name: "Personal", online: true, builtin: true }] });
    await act(async () => root.render(<ExtensionUI extension="demo" ui="fleet_board" bare history={false} />));
    await flush();
    const open = seam.sent.find((e) => e.request.request === "open")!;
    expect(open).toBeDefined();
    expect(seam.managersFetches).toBe(0);

    // Extension does not implement native stream mount -> falls back to HTTP renderUI.
    await act(async () => {
      seam.frames.forEach((fn) => fn(JSON.stringify({
        type: "extension_ui_stream",
        manager: open.manager,
        kind: "error",
        request_id: open.request.request_id,
        error: { code: "unsupported", message: "method not found" },
      })));
    });
    await flush();
    expect(seam.draws).toHaveBeenCalledTimes(1);
    expect(host.textContent).toContain("HTTP fleet_board");

    // Revisit the same page (even without localStorage cache): skips NativeExtensionUI and dispatches renderUI at once.
    seam.sent.length = 0;
    await act(async () => root.unmount());
    root = createRoot(host);
    await act(async () => root.render(<ExtensionUI extension="demo" ui="fleet_board" bare history={false} />));
    await flush();
    expect(seam.sent).toHaveLength(0);
    expect(seam.managersFetches).toBe(0);
    expect(seam.draws).toHaveBeenCalledTimes(2);
    expect(host.textContent).toContain("HTTP fleet_board");
  });
});
