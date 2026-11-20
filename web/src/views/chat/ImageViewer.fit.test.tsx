// @vitest-environment jsdom
// The viewer fits its picture to the room (#11511): one the browser already has (complete before its load event, the
// first open from the lazy chunk), one whose load lands before the mount's effects and one that loads later, each fitted
// in every commit it shows in, with a spinner while one is still coming (never for one complete); another picture starts
// at 1× and is never drawn at the last one's size; two fingers zoom; the round and bar buttons carry the 44 px target.
import { act, Profiler, useLayoutEffect, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { ImageViewer, type ViewerImage } from "./ImageViewer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// jsdom has no layout: every element is a 358 × 600 box (the room, the area has no padding without the stylesheet),
// and a picture's state comes from `pictures` by its address
const ROOM = { width: 358, height: 600 };
let pictures: Record<string, { complete: boolean; width: number; height: number }>;
let root: Root;
let host: HTMLDivElement;
// the picture as each commit left it, while it shows, and the commits that rendered the loading spinner
let drawn: { src: string | null; width: string; height: string; scale: string }[];
let spun: number;

const picture = () => document.querySelector<HTMLImageElement>('[role="dialog"] img')!;
const spinner = () => document.querySelector('[role="dialog"] [role="status"]');
const area = () => picture().parentElement!;

beforeEach(() => {
  vi.stubGlobal("matchMedia", (media: string) => ({ matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }));
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(ROOM.width);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(ROOM.height);
  const state = (img: HTMLImageElement) => pictures[img.getAttribute("src") ?? ""] ?? { complete: false, width: 0, height: 0 };
  vi.spyOn(HTMLImageElement.prototype, "complete", "get").mockImplementation(function (this: HTMLImageElement) { return state(this).complete; });
  vi.spyOn(HTMLImageElement.prototype, "naturalWidth", "get").mockImplementation(function (this: HTMLImageElement) { return state(this).complete ? state(this).width : 0; });
  vi.spyOn(HTMLImageElement.prototype, "naturalHeight", "get").mockImplementation(function (this: HTMLImageElement) { return state(this).complete ? state(this).height : 0; });
  Element.prototype.setPointerCapture ??= () => {};
  pictures = {};
  drawn = [];
  spun = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function record() {
  const img = document.querySelector<HTMLImageElement>('[role="dialog"] img');
  if (img && img.style.visibility !== "hidden") drawn.push({ src: img.getAttribute("src"), width: img.style.width, height: img.style.height, scale: img.style.scale });
  if (spinner()) spun++;
}

async function open(images: ViewerImage[], extra?: ReactNode) {
  await act(async () =>
    root.render(
      <Profiler id="viewer" onRender={record}>
        <ImageViewer images={images} start={0} onClose={() => {}} />
        {extra}
      </Profiler>,
    ),
  );
}

async function load(src: string, width: number, height: number) {
  pictures[src] = { complete: true, width, height };
  await act(async () => picture().dispatchEvent(new Event("load")));
}

const pointer = (type: string, pointerId: number, clientX: number, clientY: number) =>
  act(() => void area().dispatchEvent(new PointerEvent(type, { pointerId, clientX, clientY, isPrimary: pointerId === 1, pointerType: "touch", bubbles: true })));

it("fits a picture the browser already has, with no load event for it, from its first commit", async () => {
  pictures["/a.png"] = { complete: true, width: 256, height: 256 };
  await open([{ id: "a", src: "/a.png" }]);
  const img = picture();
  expect(img.style.visibility).not.toBe("hidden");
  expect(img.style.animation).toContain("pop-in");
  // 256 × 256 scaled up into 358 × 600: as wide as the room, centred in its height
  expect([img.style.width, img.style.height, img.style.left, img.style.top, img.style.scale]).toEqual(["358px", "358px", "0px", "121px", "1"]);
  expect(drawn.length).toBeGreaterThan(0);
  expect(drawn.every((d) => d.width === "358px" && d.height === "358px")).toBe(true);
  expect(spun, "no commit renders the spinner for a picture already complete").toBe(0);
});

it("keeps a picture fitted when its load lands before the mount's effects (the first open from the lazy chunk)", async () => {
  // fires the load in the mount's own commit, before its passive effects run, as a cached picture's does when the
  // viewer mounts from a Suspense retry
  function LoadsAtMount() {
    useLayoutEffect(() => {
      pictures["/a.png"] = { complete: true, width: 256, height: 256 };
      picture().dispatchEvent(new Event("load"));
    }, []);
    return null;
  }
  await open([{ id: "a", src: "/a.png" }], <LoadsAtMount />);
  expect([picture().style.width, picture().style.scale]).toEqual(["358px", "1"]);
});

it("shows a spinner for a picture that loads later, hidden until its size is known, then the picture fitted", async () => {
  await open([{ id: "a", src: "/a.png" }]);
  expect(picture().style.visibility).toBe("hidden");
  expect(picture().style.width).toBe("");
  expect(drawn).toEqual([]);
  expect(spinner()?.textContent).toBe(t("common.loading"));
  await load("/a.png", 512, 256);
  expect(spinner()).toBeNull();
  expect(picture().style.visibility).not.toBe("hidden");
  expect(picture().style.animation).toContain("pop-in");
  expect([picture().style.width, picture().style.height, picture().style.top]).toEqual(["358px", "179px", "210.5px"]);
  expect(drawn.every((d) => d.width === "358px" && d.height === "179px")).toBe(true);
});

it("says a picture that would not load could not be read, without the spinner", async () => {
  await open([{ id: "a", src: "/a.png" }]);
  expect(spinner()).not.toBeNull();
  await act(async () => picture().dispatchEvent(new Event("error")));
  expect(spinner()).toBeNull();
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(t("chat-imageviewer.unreadable"));
});

it("zooms the fitted picture with two fingers", async () => {
  pictures["/a.png"] = { complete: true, width: 256, height: 256 };
  await open([{ id: "a", src: "/a.png" }]);
  await pointer("pointerdown", 1, 100, 300);
  await pointer("pointerdown", 2, 200, 300);
  await pointer("pointermove", 2, 300, 300);
  expect(Number(picture().style.scale)).toBeCloseTo(2);
});

it("starts the next picture at 1× and its own fit, never drawn at the last one's size", async () => {
  pictures["/a.png"] = { complete: true, width: 256, height: 256 };
  pictures["/b.png"] = { complete: true, width: 512, height: 256 };
  await open([{ id: "a", src: "/a.png" }, { id: "b", src: "/b.png" }]);
  await pointer("pointerdown", 1, 100, 300);
  await pointer("pointerdown", 2, 200, 300);
  await pointer("pointermove", 2, 300, 300);
  await pointer("pointerup", 2, 300, 300);
  await pointer("pointerup", 1, 100, 300);
  expect(Number(picture().style.scale)).toBeCloseTo(2);
  drawn = [];
  await act(async () => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" })));
  expect(picture().getAttribute("src")).toBe("/b.png");
  expect([picture().style.width, picture().style.height, picture().style.scale, picture().style.translate]).toEqual(["358px", "179px", "1", "0px 0px"]);
  const next = drawn.filter((d) => d.src === "/b.png");
  expect(next.length).toBeGreaterThan(0);
  expect(next).toEqual(next.map(() => ({ src: "/b.png", width: "358px", height: "179px", scale: "1" })));
  expect(spun, "no spinner for either picture, both complete").toBe(0);
});

it("gives the round buttons and the bar's buttons the touch target", async () => {
  await open([{ id: "a", src: "/a.png" }]);
  const button = (name: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) => b.getAttribute("aria-label") === name || b.textContent?.trim() === name)!;
  for (const name of [t("chat-imageviewer.close"), t("chat-imageviewer.open_in_new_tab"), t("chat-imageviewer.download")]) expect(button(name).classList).toContain("touch-hit");
});
