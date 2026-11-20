// @vitest-environment jsdom
// Dialog as a phone's sheet (#11442): once the keyboard has made the sheet shorter, the field focused in it is scrolled
// back into sight inside the sheet's own body, not by moving the document; the footer is marked for the toasts.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Dialog } from "./atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let phone = true;
let host: HTMLDivElement;
let root: Root;
const vv = Object.assign(new EventTarget(), { height: 768, width: 390, offsetTop: 0, offsetLeft: 0, scale: 1 });
// the sheet's body shows 100..300 on screen; the field sits 500 px down its content, 60 px tall
const BODY = { top: 100, bottom: 300 };
const FIELD = { at: 500, height: 60 };

const body = () => document.querySelector<HTMLElement>("[role=dialog] .overflow-y-auto")!;
const field = () => document.querySelector<HTMLTextAreaElement>("[role=dialog] textarea")!;

function render(open = true) {
  act(() =>
    root.render(
      <Dialog open={open} onClose={() => {}} title="新建 Agent" footer={<button type="button">启动</button>}>
        <textarea aria-label="第一条消息" />
      </Dialog>,
    ),
  );
}

function keyboard(height: number) {
  vv.height = height;
  act(() => void vv.dispatchEvent(new Event("resize")));
  act(() => void vi.advanceTimersByTime(20)); // the frame after it
}

beforeEach(() => {
  vi.useFakeTimers();
  phone = true;
  vv.height = 768;
  vi.stubGlobal("matchMedia", (q: string) => ({ get matches() { return q === "(max-width: 639px)" && phone; }, media: q, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => setTimeout(() => cb(0), 16) as unknown as number);
  vi.stubGlobal("cancelAnimationFrame", (id: number) => clearTimeout(id));
  Object.defineProperty(window, "visualViewport", { configurable: true, get: () => vv });
  vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    let r = { top: 0, bottom: 0 };
    if (this === body()) r = BODY;
    else if (this.tagName === "TEXTAREA") {
      const top = BODY.top + FIELD.at - body().scrollTop;
      r = { top, bottom: top + FIELD.height };
    }
    return { x: 0, y: r.top, left: 0, right: 390, width: 390, height: r.bottom - r.top, ...r, toJSON() {} } as DOMRect;
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  delete (window as { visualViewport?: unknown }).visualViewport;
});

describe("a sheet and the keyboard", () => {
  it("scrolls its body so the focused field is inside it after the keyboard shrinks the sheet", () => {
    render();
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    act(() => field().focus());
    keyboard(470);
    // its bottom (500 + 60 down the content) 8 px above the body's bottom (200 tall)
    expect(body().scrollTop).toBe(500 + 60 - 200 + 8);
    const r = field().getBoundingClientRect();
    expect(r.top).toBeGreaterThanOrEqual(BODY.top);
    expect(r.bottom).toBeLessThanOrEqual(BODY.bottom);
    expect(scrollIntoView).not.toHaveBeenCalled(); // never the document: that pans iOS's visual viewport
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });

  it("brings back a field scrolled above the body, its top in sight", () => {
    render();
    act(() => field().focus());
    body().scrollTop = 700; // the field 200 px above the body's top
    keyboard(470);
    expect(body().scrollTop).toBe(500 - 8);
  });

  it("leaves the body alone when the field is in sight, or the focus is not on a field in it", () => {
    render();
    body().scrollTop = 450; // the field at 150..210
    act(() => field().focus());
    keyboard(470);
    expect(body().scrollTop).toBe(450);
    body().scrollTop = 0;
    act(() => document.querySelector<HTMLButtonElement>("[role=dialog] [data-dialog-footer] button")!.focus());
    keyboard(400);
    expect(body().scrollTop).toBe(0);
  });

  it("does nothing as the wide card", () => {
    phone = false;
    render();
    act(() => field().focus());
    keyboard(470);
    expect(body().scrollTop).toBe(0);
  });

  it("marks its footer, which a phone's toasts keep clear of", () => {
    render();
    expect(document.querySelector("[role=dialog] [data-dialog-footer]")?.textContent).toBe("启动");
  });
});
