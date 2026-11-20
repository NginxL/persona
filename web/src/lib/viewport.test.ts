// @vitest-environment jsdom
import { act, createElement, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { enterSends, hasFinePointer, keyboardFrame, keyboardVars, useFinePointer, useKeyboardFrame } from "./viewport";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement | undefined;
let root: Root | undefined;

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  host?.remove();
  host = undefined;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("keyboardFrame", () => {
  it("follows the visible part while the keyboard covers the bottom", () => {
    expect(keyboardFrame(844, { height: 508.4, offsetTop: 120, scale: 1 })).toEqual({ top: 120, height: 508 });
  });

  it("is nothing without a keyboard, without visualViewport, or while pinch-zoomed", () => {
    expect(keyboardFrame(844, { height: 844, offsetTop: 0, scale: 1 })).toBeUndefined();
    expect(keyboardFrame(844, { height: 829, offsetTop: 0, scale: 1 })).toBeUndefined(); // a scrollbar
    expect(keyboardFrame(844, undefined)).toBeUndefined();
    expect(keyboardFrame(844, { height: 400, offsetTop: 200, scale: 2 })).toBeUndefined();
  });
});

describe("keyboardVars", () => {
  it("puts the visible part in --kb-top / --kb-h while the keyboard is up", () => {
    expect(keyboardVars({ top: 120, height: 508 })).toEqual({ "--kb-top": "120px", "--kb-h": "508px" });
  });

  it("leaves them to the stylesheet without a keyboard", () => {
    expect(keyboardVars(undefined)).toBeNull();
  });
});

describe("useKeyboardFrame", () => {
  const html = document.documentElement;
  const startWidth = window.innerWidth;
  const startHeight = window.innerHeight;
  const setInner = (w: number, h: number) => {
    Object.defineProperty(window, "innerWidth", { configurable: true, writable: true, value: w });
    Object.defineProperty(window, "innerHeight", { configurable: true, writable: true, value: h });
  };
  afterEach(() => {
    delete (window as { visualViewport?: unknown }).visualViewport;
    setInner(startWidth, startHeight);
    document.querySelectorAll("[data-kb-test]").forEach((e) => e.remove());
  });
  /** A visual viewport (jsdom has none) and the shell on it. */
  function mount() {
    const vv = Object.assign(new EventTarget(), { height: window.innerHeight, offsetTop: 0, scale: 1 });
    Object.defineProperty(window, "visualViewport", { configurable: true, value: vv });
    function Shell() {
      const ref = useRef<HTMLDivElement>(null);
      useKeyboardFrame(ref);
      return createElement("div", { ref });
    }
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(createElement(Shell)));
    return vv;
  }
  /** Chrome on Android: the whole page (and its visual viewport with it) is resized; the window's resize says so. */
  const page = (vv: { height: number }, w: number, h: number) => act(() => {
    setInner(w, h);
    vv.height = h;
    window.dispatchEvent(new Event("resize"));
  });
  const field = () => {
    const input = document.createElement("input");
    input.setAttribute("data-kb-test", "");
    document.body.append(input);
    input.focus();
    return input;
  };

  it("marks the root with data-kb while the keyboard is up, and clears it when it goes or the shell unmounts", () => {
    // iOS: only the visual viewport shrinks (jsdom's innerHeight is 768)
    const vv = mount();
    const keyboard = (height: number) => act(() => {
      vv.height = height;
      vv.dispatchEvent(new Event("resize"));
    });
    expect(html.hasAttribute("data-kb")).toBe(false);

    keyboard(400);
    expect(html.hasAttribute("data-kb")).toBe(true);
    expect(html.style.getPropertyValue("--kb-h")).toBe("400px");
    keyboard(380); // still sliding
    expect(html.hasAttribute("data-kb")).toBe(true);

    keyboard(window.innerHeight);
    expect(html.hasAttribute("data-kb")).toBe(false);
    expect(html.style.getPropertyValue("--kb-h")).toBe("");

    keyboard(400);
    act(() => root!.unmount());
    root = undefined;
    expect(html.hasAttribute("data-kb")).toBe(false);
    expect(html.style.getPropertyValue("--kb-h")).toBe("");
  });

  it("marks it for a keyboard that resizes the whole page while a field is focused, leaving --kb-h to 100dvh", () => {
    setInner(390, 844);
    const vv = mount();
    field();
    page(vv, 390, 508);
    expect(html.hasAttribute("data-kb")).toBe(true);
    expect(html.style.getPropertyValue("--kb-h")).toBe("");
    expect(html.style.getPropertyValue("--kb-top")).toBe("");
    page(vv, 390, 844);
    expect(html.hasAttribute("data-kb")).toBe(false);

    page(vv, 390, 508);
    act(() => root!.unmount());
    root = undefined;
    expect(html.hasAttribute("data-kb")).toBe(false);
  });

  it("is not a keyboard with nothing taking typing, nor a page that only turned", () => {
    setInner(390, 844);
    const vv = mount();
    page(vv, 390, 508); // the page got shorter, but no field has the focus
    expect(html.hasAttribute("data-kb")).toBe(false);
    page(vv, 390, 844);

    // turned: the tallest height starts over at the new width
    const input = field();
    page(vv, 844, 390);
    expect(html.hasAttribute("data-kb")).toBe(false);
    page(vv, 844, 250);
    expect(html.hasAttribute("data-kb")).toBe(true);
    input.blur();
    page(vv, 844, 240);
    expect(html.hasAttribute("data-kb")).toBe(false);
  });
});

describe("enterSends", () => {
  it("sends on Enter with a precise pointer, never with Shift; Mod+Alt+Enter is the card shortcut's, not a send", () => {
    expect(enterSends({ key: "Enter", shiftKey: false }, true)).toBe(true);
    expect(enterSends({ key: "Enter", shiftKey: true }, true)).toBe(false);
    // Alt alone keeps sending as it always did; the shortcut carries Mod with it
    expect(enterSends({ key: "Enter", shiftKey: false, altKey: true }, true)).toBe(true);
    expect(enterSends({ key: "Enter", shiftKey: false, altKey: true, ctrlKey: true }, true)).toBe(false);
    expect(enterSends({ key: "Enter", shiftKey: false, altKey: true, metaKey: true }, true)).toBe(false);
    expect(enterSends({ key: "a", shiftKey: false }, true)).toBe(false);
  });

  it("makes a new line on a touch screen alone", () => {
    expect(enterSends({ key: "Enter", shiftKey: false }, false)).toBe(false);
    expect(enterSends({ key: "Enter", shiftKey: true }, false)).toBe(false);
  });

  it.each([
    [{ key: "Enter", shiftKey: false, ctrlKey: true }, false],
    [{ key: "Enter", shiftKey: false, metaKey: true }, false],
    [{ key: "Enter", shiftKey: false, ctrlKey: true }, true],
    [{ key: "Enter", shiftKey: false, metaKey: true }, true],
  ])("sends Mod+Enter whatever the pointer query says: %j, fine=%s", (event, fine) => {
    expect(enterSends(event, fine)).toBe(true);
  });

  it.each([
    [{ key: "Enter", shiftKey: true, ctrlKey: true }, false],
    [{ key: "Enter", shiftKey: true, metaKey: true }, false],
    [{ key: "Enter", shiftKey: true, ctrlKey: true }, true],
    [{ key: "Enter", shiftKey: true, metaKey: true }, true],
    [{ key: "Enter", shiftKey: false, altKey: true, ctrlKey: true }, false],
    [{ key: "Enter", shiftKey: false, altKey: true, metaKey: true }, false],
    [{ key: "Enter", shiftKey: false, altKey: true, ctrlKey: true }, true],
    [{ key: "Enter", shiftKey: false, altKey: true, metaKey: true }, true],
  ])("leaves Mod+Shift+Enter and Mod+Alt+Enter alone: %j, fine=%s", (event, fine) => {
    expect(enterSends(event, fine)).toBe(false);
  });
});

describe("fine pointer detection", () => {
  it("follows the query, promotes only for a real mouse move or click, and removes its global listeners", () => {
    let fine = true;
    const mediaListeners = new Set<(event: { matches: boolean }) => void>();
    vi.stubGlobal("matchMedia", (query: string) => ({
      media: query,
      get matches() { return query === "(any-pointer: fine)" && fine; },
      addEventListener(type: string, listener: (event: { matches: boolean }) => void) {
        if (query === "(any-pointer: fine)" && type === "change") mediaListeners.add(listener);
      },
      removeEventListener(type: string, listener: (event: { matches: boolean }) => void) {
        if (query === "(any-pointer: fine)" && type === "change") mediaListeners.delete(listener);
      },
    }));
    const changeFine = (value: boolean) => {
      fine = value;
      act(() => mediaListeners.forEach((listener) => listener({ matches: value })));
    };
    const add = vi.spyOn(window, "addEventListener");
    const remove = vi.spyOn(window, "removeEventListener");
    const pointerCalls = (spy: typeof add, type: string) => spy.mock.calls.filter(([name]) => name === type);
    let renders = 0;
    const rendered: boolean[] = [];
    function Probe() {
      renders++;
      rendered.push(useFinePointer());
      return null;
    }

    host = document.createElement("div");
    document.body.append(host);
    expect(hasFinePointer()).toBe(true);
    root = createRoot(host);
    act(() => root!.render(createElement(Probe)));
    expect(rendered).toEqual([true]);
    expect(pointerCalls(add, "pointermove")).toHaveLength(0);
    expect(pointerCalls(add, "pointerdown")).toHaveLength(0);
    act(() => root!.unmount());
    root = undefined;

    // An imperative caller can start the watch with no hook mounted; a media flip still removes it.
    changeFine(false);
    expect(hasFinePointer()).toBe(false);
    expect(pointerCalls(add, "pointermove")).toHaveLength(1);
    expect(pointerCalls(add, "pointerdown")).toHaveLength(1);
    changeFine(true);
    expect(pointerCalls(remove, "pointermove")).toHaveLength(1);
    expect(pointerCalls(remove, "pointerdown")).toHaveLength(1);
    expect(mediaListeners.size).toBe(0);

    changeFine(false);
    expect(hasFinePointer()).toBe(false);
    root = createRoot(host);
    act(() => root!.render(createElement(Probe)));
    expect(rendered.at(-1)).toBe(false);
    expect(pointerCalls(add, "pointermove")).toHaveLength(2);
    expect(pointerCalls(add, "pointerdown")).toHaveLength(2);

    // A mounted hook follows both media changes and restarts the watch if the query becomes coarse again.
    changeFine(true);
    expect(rendered.at(-1)).toBe(true);
    expect(pointerCalls(remove, "pointermove")).toHaveLength(2);
    expect(pointerCalls(remove, "pointerdown")).toHaveLength(2);
    changeFine(false);
    expect(rendered.at(-1)).toBe(false);
    expect(pointerCalls(add, "pointermove")).toHaveLength(3);
    expect(pointerCalls(add, "pointerdown")).toHaveLength(3);

    const beforePointer = renders;
    for (const pointerType of ["touch", "pen"]) {
      act(() => window.dispatchEvent(new PointerEvent("pointermove", { pointerType })));
      expect(hasFinePointer()).toBe(false);
      expect(renders).toBe(beforePointer);
    }

    // A click can be the first mouse event. Window capture sees it before the target stops its bubble.
    const surface = document.createElement("button");
    surface.addEventListener("pointerdown", (event) => event.stopPropagation());
    host.append(surface);
    act(() => surface.dispatchEvent(new PointerEvent("pointerdown", { pointerType: "mouse", bubbles: true })));
    expect(hasFinePointer()).toBe(true);
    expect(rendered.at(-1)).toBe(true);
    expect(renders).toBe(beforePointer + 1);
    expect(pointerCalls(remove, "pointermove")).toHaveLength(3);
    expect(pointerCalls(remove, "pointerdown")).toHaveLength(3);
    const afterPromotion = renders;
    for (const type of ["pointermove", "pointerdown"]) for (const pointerType of ["touch", "mouse"]) {
      act(() => window.dispatchEvent(new PointerEvent(type, { pointerType })));
      expect(hasFinePointer()).toBe(true);
    }
    expect(renders).toBe(afterPromotion);
    expect(pointerCalls(add, "pointermove")).toHaveLength(3);
    expect(pointerCalls(add, "pointerdown")).toHaveLength(3);
    expect(pointerCalls(remove, "pointermove")).toHaveLength(3);
    expect(pointerCalls(remove, "pointerdown")).toHaveLength(3);
  });
});
