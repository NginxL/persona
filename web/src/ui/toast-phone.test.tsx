// @vitest-environment jsdom
// Toasts on a phone (#11442): a finger's tap does not hold one (only a mouse over it does); on a narrow screen they sit
// in the visible frame, clear of the chat's dock or the footer of the sheet on top, measured only while one shows;
// Undo keeps a target a finger can reach past its word, and a wide screen keeps them at bottom-5.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toast, Toasts } from "./atoms";
import { ToastLift } from "./toast-lift";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let narrow = true;
let host: HTMLDivElement;
let root: Root;
let extra: HTMLElement[] = [];
let bounds: { mock: { contexts: unknown[] } };
const rects = new Map<Element, Partial<DOMRect>>();
const vv = Object.assign(new EventTarget(), { height: 768, width: 390, offsetTop: 0, offsetLeft: 0, scale: 1 });

const box = () => document.body.querySelector<HTMLElement>("[aria-live=polite]")!;
const rows = () => [...box().children] as HTMLElement[];
const lift = () => box().style.getPropertyValue("--toast-lift");
const show = (...args: Parameters<typeof toast>) => act(() => void toast(...args));
const advance = (ms: number) => act(() => void vi.advanceTimersByTime(ms));

function rect(top: number, bottom: number): Partial<DOMRect> {
  return { top, bottom, height: bottom - top, left: 0, right: 390, width: 390 };
}

function add(html: string, at: Record<string, [number, number]>): HTMLElement {
  const el = document.createElement("div");
  el.innerHTML = html;
  document.body.appendChild(el);
  extra.push(el);
  for (const [sel, [top, bottom]] of Object.entries(at)) rects.set(el.querySelector(sel) ?? el, rect(top, bottom));
  return el;
}

function pointer(el: Element, type: "pointerover" | "pointerout", pointerType: string) {
  act(() => void el.dispatchEvent(new PointerEvent(type, { bubbles: true, pointerType })));
}

let clock = Date.now();


beforeEach(() => {
  vi.useFakeTimers();
  // a minute on per test: the same line within a few seconds is folded into the last (FeedbackGate)
  clock += 60_000;
  vi.setSystemTime(clock);
  narrow = true;
  vv.height = 768;
  vv.offsetTop = 0;
  // the narrow layout (max-lg) follows `narrow`; nothing else matches
  vi.stubGlobal("matchMedia", (q: string) => ({ get matches() { return q === "(max-width: 1023px)" && narrow; }, media: q, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => setTimeout(() => cb(0), 16) as unknown as number);
  vi.stubGlobal("cancelAnimationFrame", (id: number) => clearTimeout(id));
  Object.defineProperty(window, "visualViewport", { configurable: true, get: () => vv });
  // the frame the container fills: --kb-top / --kb-h, the whole 768 px window by default
  rects.clear();
  bounds = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const r = this.matches("[aria-live=polite]") ? rect(vv.offsetTop, vv.offsetTop + vv.height) : rects.get(this);
    return { x: 0, y: 0, top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0, ...r, toJSON() {} } as DOMRect;
  });
  vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.parentElement?.matches("[aria-live=polite]") ? 38 : 0;
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  // the placement as the app hands it in once its chunk is in (App.tsx)
  act(() => root.render(<Toasts lift={ToastLift} />));
});

afterEach(() => {
  advance(10_000); // whatever a test left showing times out
  act(() => root.unmount());
  host.remove();
  extra.forEach((e) => e.remove());
  extra = [];
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  delete (window as { visualViewport?: unknown }).visualViewport;
});

describe("toasts and the pointer", () => {
  it("a finger's tap does not hold a toast: it goes after its time", () => {
    show("已复制", "ok");
    // what a tap sends: the touch pointer in and out, then the mouse's compatibility mouseover, and no mouseout
    pointer(rows()[0], "pointerover", "touch");
    pointer(rows()[0], "pointerout", "touch");
    act(() => void rows()[0].dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    advance(2800);
    expect(rows()).toHaveLength(0);
  });

  it("a mouse over a toast holds it, and it runs on once the mouse leaves", () => {
    show("已复制", "ok");
    pointer(rows()[0], "pointerover", "mouse");
    advance(9000);
    expect(rows()).toHaveLength(1);
    pointer(rows()[0], "pointerout", "mouse");
    advance(2800);
    expect(rows()).toHaveLength(0);
  });
});

describe("toasts on a narrow screen", () => {
  it("sit in the visible frame, lifted at once over the chat's dock", () => {
    add("<div data-chat-dock></div>", { "[data-chat-dock]": [560, 768] });
    show("没发出去", "error");
    // the frame's bottom (768) less the dock's top (560)
    expect(lift()).toBe("208px");
    const { top, bottom, height, justifyContent, paddingBottom } = box().style;
    expect([top, bottom, height, justifyContent]).toEqual(["var(--kb-top)", "auto", "var(--kb-h)", "flex-end"]);
    expect(paddingBottom).toBe("calc(var(--toast-lift, env(safe-area-inset-bottom)) + 20px)");
  });

  it("follow the keyboard: in the frame it leaves, still clear of the dock above it", () => {
    const dock = add("<div data-chat-dock></div>", { "[data-chat-dock]": [560, 768] });
    show("没发出去", "error");
    vv.height = 470;
    rects.set(dock.firstElementChild!, rect(330, 470));
    act(() => void vv.dispatchEvent(new Event("resize")));
    advance(20);
    expect(lift()).toBe("140px");
  });

  it("over an open sheet, only its footer counts; the page's dock under it does not", async () => {
    add("<div data-chat-dock></div>", { "[data-chat-dock]": [400, 768] });
    show("没发出去", "error");
    expect(lift()).toBe("368px");
    const sheet = add('<div role="dialog" aria-modal="true"><div data-dialog-footer></div></div>', { "[data-dialog-footer]": [700, 768] });
    await act(async () => {}); // the sheet came: the body's children changed (a mutation record, then a frame)
    advance(20);
    expect(lift()).toBe("68px");
    // closing, it drops aria-modal at once: back over the dock
    sheet.querySelector("[role=dialog]")!.removeAttribute("aria-modal");
    await act(async () => {});
    advance(20);
    expect(lift()).toBe("368px");
  });

  it("is not woken by changes that cannot move it: a reply streaming into the chat measures nothing", async () => {
    const page = add("<div data-chat-dock></div><div data-chat-log><p></p></div>", { "[data-chat-dock]": [560, 768] });
    const dock = page.querySelector("[data-chat-dock]");
    const log = page.querySelector("[data-chat-log]")!;
    show("没发出去", "error");
    const measured = () => bounds.mock.contexts.filter((c) => c === dock).length;
    const before = measured();
    for (let i = 0; i < 20; i++) {
      log.appendChild(document.createElement("p")).textContent = `第 ${i} 段`;
      log.firstElementChild!.appendChild(document.createTextNode("…"));
      await act(async () => {});
      advance(20);
    }
    log.firstElementChild!.remove();
    await act(async () => {});
    advance(20);
    expect(measured()).toBe(before);
    expect(lift()).toBe("208px");
  });

  it("a footer or dock that does not reach down to where they are (a card in the middle) does not move them", () => {
    add('<div role="dialog" aria-modal="true"><div data-dialog-footer></div></div>', { "[data-dialog-footer]": [400, 450] });
    show("没发出去", "error");
    expect(lift()).toBe("");
  });

  it("never go above the top of the frame, whatever covers it", () => {
    add("<div data-chat-dock></div>", { "[data-chat-dock]": [10, 768] });
    show("没发出去", "error");
    // 768 less the bottom room (20), one row (38) and a gap (8)
    expect(lift()).toBe("702px");
  });

  it("with nothing under them sit over the home indicator, or right on the keyboard", () => {
    show("没发出去", "error");
    expect(lift()).toBe(""); // the safe-area inset (the class's fallback)
    vv.height = 470;
    act(() => void vv.dispatchEvent(new Event("resize")));
    advance(20);
    expect(lift()).toBe("0px");
  });

  it("measure only while a toast shows: the listeners go with the last one", () => {
    const on = vi.spyOn(vv, "addEventListener");
    const off = vi.spyOn(vv, "removeEventListener");
    add("<div data-chat-dock></div>", { "[data-chat-dock]": [560, 768] });
    expect(on).not.toHaveBeenCalled();
    show("已复制", "ok");
    expect(on).toHaveBeenCalledWith("resize", expect.any(Function));
    advance(3500);
    expect(rows()).toHaveLength(0);
    expect(off).toHaveBeenCalledWith("resize", expect.any(Function));
    expect(lift()).toBe("");
    expect([box().style.top, box().style.paddingBottom]).toEqual(["", ""]); // back at bottom-5 for the next one
  });
});

describe("toasts on a wide screen", () => {
  it("stay at bottom-5 with nothing measured", () => {
    narrow = false;
    add("<div data-chat-dock></div>", { "[data-chat-dock]": [560, 768] });
    show("没发出去", "error");
    expect(box().className).toContain("fixed inset-x-0 bottom-5");
    expect(box().getAttribute("style")).toBeNull();
  });
});

describe("a toast's button on a touch screen", () => {
  it("reaches at least 8 px past its word, and the close button makes room only beside one", () => {
    show("已归档", "ok", { label: "撤销", run() {} });
    show("已复制", "ok");
    const [withAction, plain] = rows();
    const [undo, close] = withAction.querySelectorAll("button");
    // touch-hit's square, widened to 8 px either side of a word wider than 28 px; the button itself as before
    for (const c of ["touch-hit", "ml-1", "[@media(pointer:coarse)]:before:inset-x-[min(--spacing(-2),calc(50%-22px))]"]) expect(undo.className.split(" ")).toContain(c);
    // no taller or wider toast: its only spacing is the ml-1 it always had
    expect(undo.className.split(" ").filter((c) => /(^|:)-?(p[xytblr]?|m[xytblr]?|min-h|h)-/.test(c))).toEqual(["ml-1"]);
    expect(close.className).toContain("[@media(pointer:coarse)]:ml-3");
    expect(plain.querySelector("button")!.className).not.toContain("ml-3");
  });
});
