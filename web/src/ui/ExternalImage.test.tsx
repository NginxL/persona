// @vitest-environment jsdom
// External inline Markdown images stay unloaded until the reader taps the placeholder (#8162): the host gets no request on
// mount, a tap loads the picture without a Referer header, a failed load offers a retry, and the hub's own images
// render inline.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Markdown } from "./Markdown";
import { forgetMarkdown } from "./markdown-cache";
import { loadReplyImages } from "./reply-images-lazy";
// Block pictures belong to ReplyImages once its chunk loads. Keep that plugin loaded so these fixtures must exercise
// the inline gate regardless of when the page gets an idle moment.
beforeAll(() => loadReplyImages());

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const external = "https://tracker.example/banner.png";
// Protocol-relative: react-markdown passes the raw //host/x through, which new URL() rejects without a base.
const protocolRelative = "//tracker.example/pixel.png";
const own = `${location.origin}/api/v1/extensions/notes/-/image/token`;

let host: HTMLDivElement;
let root: Root;
let srcSets: string[];
let srcDesc: PropertyDescriptor | undefined;
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  forgetMarkdown();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  // An <img> only contacts a host once its src is set, so recording every src assignment proves whether a request
  // could start (jsdom never performs the download itself).
  srcSets = [];
  srcDesc = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, "src");
  Object.defineProperty(HTMLImageElement.prototype, "src", {
    configurable: true,
    enumerable: srcDesc?.enumerable,
    get: srcDesc?.get,
    set(v: string) {
      srcSets.push(v);
      srcDesc?.set?.call(this, v);
    },
  });
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  if (srcDesc) Object.defineProperty(HTMLImageElement.prototype, "src", srcDesc);
});

const draw = async (text: string) => {
  await act(async () => root.render(<Markdown text={`before ${text} after`} />));
  // The render starts React.lazy's import. A separate direct import can finish before that request settles.
  await act(async () => { await vi.dynamicImportSettled(); });
};
const gate = () => host.querySelector<HTMLButtonElement>("[data-external-image=gate]");
const retry = () => host.querySelector<HTMLButtonElement>("[data-external-image=retry]");
const img = () => host.querySelector<HTMLImageElement>("img");

describe("external Markdown image gate (#8162)", () => {
  it("sends no request on mount and names the host in a placeholder", async () => {
    await draw(`![banner](${external})`);
    expect(img()).toBeNull();
    expect(srcSets).not.toContain(external);
    const button = gate();
    expect(button).not.toBeNull();
    expect(button!.textContent).toContain("tracker.example");
    expect(button!.title).toBe("banner");
  });

  it("loads the image without a referrer only after a tap", async () => {
    await draw(`![banner](${external})`);
    await act(async () => gate()!.click());
    await flush();
    const picture = img();
    expect(picture).not.toBeNull();
    expect(picture!.getAttribute("src")).toBe(external);
    // jsdom does not reflect the attribute to the IDL property; React writes it as an attribute browsers honor.
    expect(picture!.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(srcSets.filter((s) => s === external)).toHaveLength(1);
    expect(gate()).toBeNull();
  });

  it("shows a working retry placeholder when the load fails", async () => {
    await draw(`![banner](${external})`);
    await act(async () => gate()!.click());
    await flush();
    act(() => img()!.dispatchEvent(new Event("error")));
    await flush();
    expect(img()).toBeNull();
    const again = retry();
    expect(again).not.toBeNull();
    expect(again!.textContent).toContain("tracker.example");
    // a retry starts one more load instead of staying broken
    await act(async () => again!.click());
    await flush();
    expect(img()?.getAttribute("src")).toBe(external);
    expect(srcSets.filter((s) => s === external)).toHaveLength(2);
  });

  it("gates a protocol-relative image on mount with no src and loads it without a referrer after a tap", async () => {
    await draw(`![pixel](${protocolRelative})`);
    // Without the page-origin base this URL fails to parse, is treated as the hub's own, and an <img> leaks the request on mount.
    expect(img()).toBeNull();
    expect(srcSets.some((s) => s.includes("tracker.example"))).toBe(false);
    const button = gate();
    expect(button).not.toBeNull();
    expect(button!.textContent).toContain("tracker.example");
    expect(button!.title).toBe("pixel");
    await act(async () => gate()!.click());
    await flush();
    const picture = img();
    expect(picture).not.toBeNull();
    expect(picture!.getAttribute("src")).toBe(protocolRelative);
    expect(picture!.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(srcSets.filter((s) => s === protocolRelative)).toHaveLength(1);
    expect(gate()).toBeNull();
  });

  it("renders a same-origin image inline with no placeholder", async () => {
    await draw(`![attachment](${own})`);
    expect(gate()).toBeNull();
    const picture = img();
    expect(picture).not.toBeNull();
    expect(picture!.getAttribute("src")).toBe(own);
    expect(srcSets).toContain(own);
  });

  it("treats relative attachment URLs as the hub's own images", async () => {
    await draw(`![a](/api/v1/items/1/image) and ![b](${own})`);
    expect(gate()).toBeNull();
    expect(retry()).toBeNull();
    expect(host.querySelectorAll("img")).toHaveLength(2);
    expect(srcSets.some((s) => s.includes("tracker.example"))).toBe(false);
  });
});
