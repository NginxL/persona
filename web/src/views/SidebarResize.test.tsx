// @vitest-environment jsdom
// The full sidebar's right edge (views/SidebarResize.tsx): it draws the column's width into the column's CSS variable,
// moves it on each pointer step without rendering anything above it, keeps the width at the end of the drag, and gives
// an open side panel and the page room.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { CHAT_MIN, getSidebarWidth, PANEL_GAP, PANEL_MIN, resetSidebarWidth, setSidebarWidth, SIDEBAR_DEFAULT, SIDEBAR_WIDTH_KEY } from "../lib/layout";
import { closePanel, openPanel } from "../lib/panel";
import { SIDEBAR_VAR, SidebarResize } from "./SidebarResize";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let renders = 0;

// stands in for the shell: counts its own renders, holds the column the handle sizes
function Column() {
  renders++;
  return (
    <div id="col" data-testid="col">
      <SidebarResize column="col" />
    </div>
  );
}

const col = () => host.querySelector<HTMLElement>('[data-testid="col"]')!;
const handle = () => host.querySelector<HTMLElement>('[role="separator"]')!;
const drawn = () => col().style.getPropertyValue(SIDEBAR_VAR);
const pointer = (target: EventTarget, type: string, clientX: number) =>
  act(() => void target.dispatchEvent(new PointerEvent(type, { clientX, pointerId: 1, button: 0, bubbles: true })));
const setWindow = (w: number) => {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: w });
  act(() => void dispatchEvent(new Event("resize")));
};

beforeEach(() => {
  localStorage.clear();
  resetSidebarWidth();
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
  renders = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => closePanel());
  act(() => root.unmount());
  host.remove();
});

describe("the sidebar's resize handle", () => {
  it("draws the default width, labelled and bound to the column", () => {
    act(() => root.render(<Column />));
    expect(drawn()).toBe(`${SIDEBAR_DEFAULT}px`);
    expect(handle().getAttribute("aria-valuenow")).toBe(String(SIDEBAR_DEFAULT));
    expect(handle().getAttribute("aria-controls")).toBe("col");
    // localized (tests run in Chinese)
    expect(handle().getAttribute("aria-label")).toBe("拖动调整侧边栏宽度");
  });

  it("moves the column on each step without rendering the shell, and keeps the width at the end", () => {
    act(() => root.render(<Column />));
    const before = renders;
    pointer(handle(), "pointerdown", 284);
    pointer(window, "pointermove", 300);
    pointer(window, "pointermove", 340);
    pointer(window, "pointermove", 360);
    expect(drawn()).toBe("360px");
    expect(getSidebarWidth()).toBe(SIDEBAR_DEFAULT);
    pointer(window, "pointerup", 360);
    expect(getSidebarWidth()).toBe(360);
    expect(localStorage.getItem(SIDEBAR_WIDTH_KEY)).toBe("360");
    expect(drawn()).toBe("360px");
    expect(renders).toBe(before);
  });

  it("starts from a remembered width", () => {
    setSidebarWidth(400);
    act(() => root.render(<Column />));
    expect(drawn()).toBe("400px");
  });

  it("gives way to the page and an open side panel, and comes back when there is room", () => {
    setSidebarWidth(480);
    act(() => root.render(<Column />));
    expect(drawn()).toBe("480px");
    // a 1180 window with a panel open: the page keeps CHAT_MIN and the panel its least
    setWindow(1180);
    act(() => openPanel({ kind: "files", mid: "m1", sid: "s1", cwd: "/work/demo" }));
    expect(drawn()).toBe(`${Math.max(SIDEBAR_DEFAULT, 1180 - CHAT_MIN - PANEL_MIN - 2 * PANEL_GAP)}px`);
    act(() => closePanel());
    expect(drawn()).toBe("480px");
    // the remembered width is still the one asked for
    expect(getSidebarWidth()).toBe(480);
  });

  it("resets on a double-click", () => {
    setSidebarWidth(420);
    act(() => root.render(<Column />));
    act(() => void handle().dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
    expect(drawn()).toBe(`${SIDEBAR_DEFAULT}px`);
    expect(localStorage.getItem(SIDEBAR_WIDTH_KEY)).toBeNull();
  });
});
