// @vitest-environment jsdom
// A Codex visualization fragment's card (#10321): the fragment is fetched only once on screen, run in the offline
// HTML runtime, torn down off screen, and its fetch / runtime failures offer Retry. The runtime itself (QuickJS in a
// worker, WASM) does not exist in jsdom: host.ts is mocked here, the mock captures what the card asked for.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const io = vi.hoisted(() => ({ instances: [] as Array<{ cb: (entries: { isIntersecting: boolean }[]) => void }> }));
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver = class {
    cb: (entries: { isIntersecting: boolean }[]) => void;
    constructor(cb: (entries: { isIntersecting: boolean }[]) => void) {
      this.cb = cb;
      io.instances.push(this as unknown as (typeof io.instances)[number]);
    }
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() { return []; }
  } as unknown as typeof IntersectionObserver;
});

const apiState = vi.hoisted(() => ({ html: "<!doctype html><p>hi</p>", fail: false, calls: 0 }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  visualizationHTML: vi.fn(async () => {
    apiState.calls += 1;
    if (apiState.fail) throw new Error("hub unreachable");
    return apiState.html;
  }),
}));

const host = vi.hoisted(() => ({
  options: [] as import("../../lib/html-runtime/host").RunOptions[],
  stopped: 0,
  released: 0,
  noSlotOnce: false,
}));
vi.mock("../../lib/html-runtime/host", async (orig) => ({
  ...(await orig<typeof import("../../lib/html-runtime/host")>()),
  claimSlot: vi.fn(() => {
    if (host.noSlotOnce) {
      host.noSlotOnce = false;
      return null;
    }
    return { at: 0, evict: () => {} };
  }),
  releaseSlot: vi.fn(() => {
    host.released += 1;
  }),
  runCard: vi.fn((o: import("../../lib/html-runtime/host").RunOptions) => {
    host.options.push(o);
    return { stop: () => { host.stopped += 1; } };
  }),
}));

import { visualizationHTML } from "../../lib/api";
import { FRAME_SANDBOX } from "../../lib/html-runtime/sanitize";
import type { Item } from "../../lib/types";
import VisualizationCard from "./VisualizationCard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (over: Partial<NonNullable<Item["visualization"]>> = {}): Item => ({
  id: "a1#1",
  seq: 1,
  role: "assistant",
  kind: "visualization",
  visualization: { path: "/home/u/.codex/visualizations/2026/11/18/sess/chart.html", ...over },
});

let root: Root;
let el: HTMLElement;

beforeEach(() => {
  io.instances.length = 0;
  host.options.length = 0;
  host.stopped = 0;
  host.released = 0;
  host.noSlotOnce = false;
  apiState.fail = false;
  apiState.calls = 0;
  el = document.createElement("div");
  document.body.appendChild(el);
  root = createRoot(el);
});

afterEach(() => {
  act(() => root.unmount());
  el.remove();
});

const lastIO = () => io.instances[io.instances.length - 1];
const show = () => act(async () => lastIO().cb([{ isIntersecting: true }]));
const renderCard = (it: Item) => act(() => root.render(<VisualizationCard item={it} mid="m" sid="s" />));
const card = () => el.querySelector("[data-visualization]") as HTMLElement;
const flush = () => act(async () => {});

describe("VisualizationCard", () => {
  it("fetches the fragment on screen and runs it in the sandboxed frame", async () => {
    await renderCard(item({ title: "Sales chart" }));
    expect(card().dataset.visualization).toBe("regular");
    expect(card().style.maxWidth).toBe("736px");
    expect(el.textContent).toContain("正在加载");
    expect(visualizationHTML).not.toHaveBeenCalled();

    await show();
    await flush();

    expect(visualizationHTML).toHaveBeenCalledWith("m", "s", "a1#1");
    expect(el.textContent).toContain("Sales chart");
    const frameEl = el.querySelector("iframe");
    expect(frameEl?.getAttribute("sandbox")).toBe(FRAME_SANDBOX);
    expect(host.options).toHaveLength(1);
    expect(host.options[0].html).toBe(apiState.html);
    // fragments get no bridge: every call is refused
    await expect(host.options[0].onBridge("open", "https://example.test")).resolves.toBe(false);

    await act(() => host.options[0].onState?.({ kind: "running" }));
    await act(() => host.options[0].onHeight?.(120));
    expect(card().dataset.visualizationState).toBe("running");
    expect((el.querySelector("[style*='height']") as HTMLElement)?.style.height).toBe("120px");
  });

  it("marks a wide fragment and falls back to the generic frame label", async () => {
    await renderCard(item({ mode: "wide" }));
    await show();
    await flush();

    expect(card().dataset.visualization).toBe("wide");
    expect(card().style.maxWidth).toBe("1024px");
    expect(el.textContent).toContain("可视化");
  });

  it("waits for a runtime slot, then starts", async () => {
    vi.useFakeTimers();
    try {
      host.noSlotOnce = true;
      await renderCard(item());
      await show();
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(host.options).toHaveLength(0);
      // the slot retry fires after a second
      await act(() => vi.advanceTimersByTimeAsync(1000));
      expect(host.options).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows the failure with the file name and Retry refetches", async () => {
    apiState.fail = true;
    await renderCard(item());
    await show();
    await flush();

    expect(el.textContent).toContain("可视化内容加载失败");
    expect(el.textContent).toContain("chart.html");
    expect(apiState.calls).toBe(1);

    apiState.fail = false;
    const retry = el.querySelector("button") as HTMLButtonElement;
    await act(async () => {
      retry.click();
    });
    await flush();

    expect(apiState.calls).toBe(2);
    expect(el.querySelector("iframe")).not.toBeNull();
  });

  it("tears the runtime down off screen and restarts it on return", async () => {
    vi.useFakeTimers();
    try {
      await renderCard(item());
      await show();
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(host.options).toHaveLength(1);
      expect(el.querySelector("iframe")).not.toBeNull();

      await act(async () => {
        lastIO().cb([{ isIntersecting: false }]);
        await vi.advanceTimersByTimeAsync(1000);
      });
      expect(host.stopped).toBe(1);
      expect(host.released).toBeGreaterThan(0);
      // the frame unmounts: a re-run gets a fresh frame and a new runtime
      expect(el.querySelector("iframe")).toBeNull();

      await show();
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(host.options).toHaveLength(2);
      expect(el.querySelector("iframe")).not.toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});
