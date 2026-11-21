// @vitest-environment jsdom
// Local-path Markdown images and file links in chat (#12288, aligned with iOS PR #12102):
// - Absolute (/..., ~/..., Windows drive) and cwd-relative paths load via authenticated fileDownloadURL.
// - Click opens full-screen ImageViewer; error surfaces a clear reason + manual Retry without auto-looping.
// - Local Markdown file links open in the file panel; http(s), //, file:, data:, blob:, mailto:, custom schemes,
//   and hub /api/v1/... routes stay strictly excluded from machine-file resolution.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../lib/api";
import { isMachineRef, resolveRef } from "../lib/files";
import { closePanel, getPanel } from "../lib/panel";
import { resetPathPresence } from "../lib/path-presence";
import { getState } from "../lib/store";
import { Markdown } from "./Markdown";
import { forgetMarkdown } from "./markdown-cache";
import { clearLocalImageFailures } from "./ReplyImages";
import { loadReplyImages } from "./reply-images-lazy";

// Path-presence lookups use a mocked API so they do not share the local-image probe's global fetch spy.
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: async () => ({ stats: [] }),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
(globalThis as { ResizeObserver?: unknown }).ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

let host: HTMLDivElement;
let root: Root;
let srcSets: string[];
let srcDesc: PropertyDescriptor | undefined;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const until = async (ready: () => boolean) => {
  for (let i = 0; i < 300 && !ready(); i++) await act(async () => { await new Promise((r) => setTimeout(r, 10)); });
};

beforeEach(async () => {
  resetPathPresence();
  forgetMarkdown();
  clearLocalImageFailures();
  closePanel();
  setToken("tok-test");
  await loadReplyImages();
  await import("./ReplyImages");
  await import("./ExternalImageGate");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
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
  closePanel();
  act(() => root.unmount());
  host.remove();
  if (srcDesc) Object.defineProperty(HTMLImageElement.prototype, "src", srcDesc);
  Object.assign(getState(), { features: [] });
  resetPathPresence();
  vi.restoreAllMocks();
});

describe("local path classification and resolution", () => {
  it("accepts absolute, home, Windows drive, and relative paths", () => {
    expect(isMachineRef("/tmp/report.png")).toBe(true);
    expect(isMachineRef("~/charts/aa.png")).toBe(true);
    expect(isMachineRef("C:/Users/me/report.png")).toBe(true);
    expect(isMachineRef("C:\\Users\\me\\report.png")).toBe(true);
    expect(isMachineRef("charts/aa.png")).toBe(true);
    expect(isMachineRef("./sub/fig.png")).toBe(true);
    expect(isMachineRef("../out/fig.png")).toBe(true);
  });

  it("resolves absolute, relative, and Windows drive paths cleanly", () => {
    expect(resolveRef("/repo/app", "/tmp/report.png")).toBe("/tmp/report.png");
    expect(resolveRef("/repo/app", "~/charts/aa.png")).toBe("~/charts/aa.png");
    expect(resolveRef("/repo/app", "charts/aa.png")).toBe("/repo/app/charts/aa.png");
    expect(resolveRef("/repo/app", "../out/fig.png?v=1#top")).toBe("/repo/out/fig.png");
    expect(resolveRef("/repo/app", "C:/Users/me/report.png")).toBe("C:\\Users\\me\\report.png");
    expect(resolveRef("C:\\work\\proj", "charts/aa.png")).toBe("C:\\work\\proj\\charts\\aa.png");
  });

  it("strictly excludes web, protocol-relative, non-local schemes, and hub internal routes", () => {
    for (const ref of [
      "http://example.com/a.png",
      "https://example.com/a.png",
      "//cdn.example.com/a.png",
      "file:///etc/passwd",
      "data:image/png;base64,AAAA",
      "blob:https://example.com/123",
      "mailto:user@example.com",
      "javascript:alert(1)",
      "persona://session/m1/s1",
      "attachment:chart.png",
      "/api/v1/machines/m1/sessions/s1/items/i1/image",
      "/api/v1/remote-image?url=https%3A%2F%2Fexample.com%2Fa.png",
      "#section-1",
    ]) {
      expect(isMachineRef(ref), ref).toBe(false);
    }
  });
});

describe("local Markdown images in chat", () => {
  it("renders absolute, relative, and Windows drive images via authenticated fileDownloadURL and opens ImageViewer on click", async () => {
    const md = [
      "Look at ![AA 榜单](/tmp/report.png) right here.",
      "",
      "![相对图](charts/aa.png)",
      "",
      "![Win 图](C:/Users/me/win.png)",
    ].join("\n");
    await act(async () => root.render(<Markdown text={md} mid="mac-1" cwd="/work/repo" lineBreaks />));
    await flush();
    await until(() => host.querySelectorAll("[data-local-image]").length === 3);

    expect(host.textContent).toContain("Look at");
    expect(host.textContent).toContain("right here.");
    const urls = srcSets.filter((s) => s.includes("/api/v1/machines/mac-1/file/download?"));
    expect(urls).toHaveLength(3);
    expect(urls[0]).toContain("path=%2Ftmp%2Freport.png");
    expect(urls[0]).toContain("inline=1");
    expect(urls[0]).toContain("token=tok-test");
    expect(urls[1]).toContain("path=%2Fwork%2Frepo%2Fcharts%2Faa.png");
    expect(urls[2]).toContain("path=C%3A%5CUsers%5Cme%5Cwin.png");

    // Clicking a loaded local image opens the full-screen ImageViewer
    await act(async () => host.querySelectorAll<HTMLElement>("[data-local-image]")[0].click());
    await act(async () => { await import("../views/chat/ImageViewer"); });
    await until(() => (document.body.textContent ?? "").includes("1 / 3"));
    expect(document.body.textContent).toContain("1 / 3");
    expect(document.body.textContent).toContain("AA 榜单");
  });

  it("preserves external image consent gate and hub attachment images without rewriting them", async () => {
    const md = [
      "![remote](https://third-party.example.com/pic.png)",
      "",
      "![att](/api/v1/machines/mac-1/sessions/s1/items/i1/image)",
    ].join("\n");
    await act(async () => root.render(<Markdown text={md} mid="mac-1" cwd="/work/repo" lineBreaks askImages />));
    await flush();
    await until(() => host.querySelector('[data-reply-picture="asked"]') !== null);

    // Third-party remote image stays gated
    expect(srcSets.some((s) => s.includes("third-party.example.com"))).toBe(false);
    // Hub attachment image renders directly, never wrapped in file/download
    const attImg = host.querySelector<HTMLImageElement>('img[alt="att"]');
    expect(attImg?.getAttribute("src")).toBe("/api/v1/machines/mac-1/sessions/s1/items/i1/image");
    expect(srcSets.some((s) => s.includes("/file/download?"))).toBe(false);
  });

  it("shows a clear failure reason and manual Retry button without auto-retrying on remount", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "file not found" }), {
        status: 404,
        headers: { "content-type": "application/json" },
      }),
    );

    const md = "![Missing](/tmp/missing.png)";
    await act(async () => root.render(<Markdown text={md} mid="mac-1" cwd="/work/repo" lineBreaks />));
    await flush();
    await until(() => host.querySelector("[data-local-image] img") !== null);

    const img = host.querySelector<HTMLImageElement>("[data-local-image] img")!;
    await act(async () => { img.dispatchEvent(new Event("error")); });
    await flush();
    await until(() => (host.querySelector("[data-local-image-error]")?.textContent ?? "").includes("file not found"));

    expect(host.querySelector("[data-local-image-error]")?.textContent).toContain("file not found");
    expect(host.querySelector("[data-local-image-retry]")).not.toBeNull();
    const setsBeforeRemount = srcSets.length;

    // Unmount and remount: failure state is remembered, NO automatic refetch
    act(() => root.unmount());
    root = createRoot(host);
    await act(async () => root.render(<Markdown text={md} mid="mac-1" cwd="/work/repo" lineBreaks />));
    await flush();
    expect(srcSets.length).toBe(setsBeforeRemount);
    expect(host.querySelector("[data-local-image-error]")?.textContent).toContain("file not found");

    // Manual Retry triggers exactly one new attempt with a cache-busting retry query
    await act(async () => host.querySelector<HTMLElement>("[data-local-image-retry]")!.click());
    await flush();
    expect(srcSets.length).toBe(setsBeforeRemount + 1);
    expect(srcSets[srcSets.length - 1]).toContain("retry=1");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });
});

describe("local Markdown file links in chat", () => {
  it("opens absolute, relative, and Windows local file links in the file panel while keeping web links unchanged", async () => {
    const md = [
      "[Abs](/tmp/report.md)",
      "[Rel](./docs/summary.md#L12)",
      "[Win](C:/Users/me/notes.md)",
      "[Web](https://example.com/page)",
    ].join(" · ");
    await act(async () => root.render(<Markdown text={md} mid="mac-1" cwd="/work/repo" />));
    await flush();

    const links = [...host.querySelectorAll<HTMLAnchorElement>("a")];
    expect(links).toHaveLength(4);

    await act(async () => links[0].click());
    expect(getPanel()).toEqual({ kind: "file", mid: "mac-1", path: "/tmp/report.md", cwd: "/work/repo", line: undefined });

    await act(async () => links[1].click());
    expect(getPanel()).toEqual({ kind: "file", mid: "mac-1", path: "/work/repo/docs/summary.md", cwd: "/work/repo", line: 12 });

    await act(async () => links[2].click());
    expect(getPanel()).toEqual({ kind: "file", mid: "mac-1", path: "C:\\Users\\me\\notes.md", cwd: "/work/repo", line: undefined });

    closePanel();
    await act(async () => links[3].click());
    expect(getPanel()).toBeNull();
    expect(links[3].getAttribute("href")).toBe("https://example.com/page");
  });
});
