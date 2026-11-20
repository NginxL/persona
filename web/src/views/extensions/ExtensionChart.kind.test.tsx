// @vitest-environment jsdom
// An extension that draws the same chart (same key) with another kind: a line chart that becomes a candlestick chart
// and back. The chart stays mounted across the change, so what it calls on each draw must not depend on the kind.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as unknown as typeof window.matchMedia;
});
import type { UIItem } from "../../lib/extension-ui";
import { ExtensionChart } from "./ExtensionChart";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  // a chart draws once it knows its width
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(private cb: ResizeObserverCallback) {}
      observe() {
        this.cb([{ contentRect: { width: 320 } } as ResizeObserverEntry], this as unknown as ResizeObserver);
      }
      unobserve() {}
      disconnect() {}
    },
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const days = ["周一", "周二", "周三"];
const line: UIItem = { type: "chart", key: "price", kind: "line", title: "收盘价", series: [{ name: "价格", points: days.map((label, i) => ({ label, value: 10 + i })) }] };
const candles: UIItem = {
  type: "chart",
  key: "price",
  kind: "candlestick",
  title: "收盘价",
  series: [{ name: "价格", points: days.map((label, i) => ({ label, value: 11 + i, open: 10 + i, high: 12 + i, low: 9 + i, close: 11 + i })) }],
};

const draw = (item: UIItem) => act(() => root.render(<ExtensionChart item={item} />));

describe("a chart drawn again with another kind", () => {
  it("goes from a line to candles and back", () => {
    draw(line);
    expect(host.querySelector("svg polyline")).not.toBeNull();
    draw(candles);
    expect(host.querySelector("svg polyline")).toBeNull();
    expect(host.querySelectorAll("svg line").length).toBeGreaterThan(0);
    draw(line);
    expect(host.querySelector("svg polyline")).not.toBeNull();
  });
});
