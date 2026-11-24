// @vitest-environment jsdom
// The zoomed Mermaid diagram is a modal: the focus moves into it, and closing it (Esc or a click) gives it back to "Enlarge".
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// jsdom has no matchMedia (useMedia reads it); the diagram is drawn already, so Mermaid itself never loads
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/mermaid", () => ({
  cachedMermaid: () => '<svg data-drawn=""></svg>',
  renderMermaid: () => Promise.resolve('<svg data-drawn=""></svg>'),
  mermaidProblem: () => ({ summary: "", detail: "" }),
}));
import { t } from "../i18n";
import { MermaidBlock, MermaidViewer } from "./Mermaid";

// The zoomed view is its own chunk (Mermaid.tsx): in before the clicks, so each one draws it at once, as after a hover.
beforeAll(async () => {
  await MermaidViewer.preload();
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<MermaidBlock code="graph TD; A-->B" />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const zoomButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-mermaid.zoom")}"]`)!;
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');

function open() {
  zoomButton().focus();
  act(() => zoomButton().click());
  expect(document.activeElement).toBe(dialog()); // it left "Enlarge", so getting back there is the close's doing
}

describe("Mermaid zoom", () => {
  function openStage() {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(600);
    vi.spyOn(SVGElement.prototype, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 400, 100));
    open();
    const stage = dialog()!.querySelector<HTMLElement>("[data-mermaid-stage]")!;
    vi.spyOn(stage, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 800, 600));
    return { stage, drawing: stage.firstElementChild as HTMLElement };
  }

  it.each(["ctrlKey", "metaKey"] as const)("leaves %s wheel events to the browser", (modifier) => {
    const { stage, drawing } = openStage();
    const before = drawing.style.transform;
    const event = new WheelEvent("wheel", { [modifier]: true, deltaY: -100, bubbles: true, cancelable: true });
    stage.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(drawing.style.transform).toBe(before);
  });

  it("keeps an off-center drawing point under the pointer while zooming", () => {
    const { stage, drawing } = openStage();
    const event = new WheelEvent("wheel", {
      clientX: 700, clientY: 350, deltaY: -100, bubbles: true, cancelable: true,
    });
    const view = () => drawing.style.transform
      .match(/translate\(([^,]+)px, ([^)]+)px\) scale\(([^)]+)\)$/)!
      .slice(1)
      .map(Number);
    const [beforeX, beforeY, beforeZoom] = view();
    stage.dispatchEvent(event);
    const [x, y, zoom] = view();
    expect(event.defaultPrevented).toBe(true);
    expect(zoom).toBeGreaterThan(beforeZoom);
    expect((300 - x) / zoom).toBeCloseTo((300 - beforeX) / beforeZoom);
    expect((50 - y) / zoom).toBeCloseTo((50 - beforeY) / beforeZoom);
  });

  it("preserves pan and zoom through observer notifications and fits on request", () => {
    let callback: ResizeObserverCallback | undefined;
    vi.stubGlobal("ResizeObserver", class {
      constructor(cb: ResizeObserverCallback) { callback = cb; }
      observe() {}
      disconnect() {}
    });
    const { stage, drawing } = openStage();
    const notify = () => callback?.([], {} as ResizeObserver);
    const initial = drawing.style.transform;
    stage.dispatchEvent(new WheelEvent("wheel", {
      clientX: 700, clientY: 350, deltaY: -100, bubbles: true, cancelable: true,
    }));
    dialog()!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft" }));
    const adjusted = drawing.style.transform;
    expect(adjusted).not.toBe(initial);
    notify();
    expect(drawing.style.transform).toBe(adjusted);
    Object.defineProperties(stage, {
      clientWidth: { value: 580 },
      clientHeight: { value: 800 },
    });
    notify();
    expect(drawing.style.transform).toBe(adjusted);
    dialog()!.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-mermaid.fit")}"]`)!.click();
    expect(drawing.style.transform).toBe("translate(-50%, -50%) translate(0px, 0px) scale(1.37)");
  });

  it("opens as a modal dialog that has the focus", () => {
    open();
    const d = dialog()!;
    expect(d.getAttribute("aria-modal")).toBe("true");
    expect(d.getAttribute("aria-label")).toBe(t("ui-mermaid.diagram"));
    expect(document.activeElement).toBe(d);
  });

  it("gives the focus back to 放大 when Esc closes it", () => {
    open();
    act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(zoomButton());
  });

  it("gives the focus back to 放大 when a click outside the diagram closes it", () => {
    open();
    act(() => dialog()!.click());
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(zoomButton());
  });

  it("wraps Tab around the viewer controls and returns focus after Close", () => {
    open();
    const buttons = [...dialog()!.querySelectorAll("button")];
    for (const button of buttons) vi.spyOn(button, "getClientRects").mockReturnValue({ length: 1 } as DOMRectList);
    const close = buttons.at(-1)!;
    const first = buttons[0];
    const tab = (target: HTMLElement, shiftKey: boolean) => {
      const event = new KeyboardEvent("keydown", { key: "Tab", shiftKey, bubbles: true, cancelable: true });
      act(() => target.dispatchEvent(event));
      expect(event.defaultPrevented).toBe(true);
    };
    close.focus();
    tab(close, false);
    expect(document.activeElement).toBe(first);
    tab(first, true);
    expect(document.activeElement).toBe(close);
    act(() => close.click());
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(zoomButton());
  });
});
