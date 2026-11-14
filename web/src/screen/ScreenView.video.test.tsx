// @vitest-environment jsdom
// The live screen's picture switcher (#3954): the bar offers Video / VNC and, for video, a quality bias — but only when
// this Mac's node advertises the hardware video channel, so no Mac shows a control it can't use. The choice is kept per
// Mac in this browser. A page an app embeds has no bar to choose in.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";

type FakeRFB = EventTarget & { canvas: HTMLCanvasElement };
const rfbs = vi.hoisted(() => [] as FakeRFB[]);
const status = vi.hoisted(() => ({ video: null as unknown }));
vi.mock("@novnc/novnc", () => ({
  default: class extends EventTarget {
    viewOnly = true;
    canvas = document.createElement("canvas");
    constructor(target: HTMLElement) {
      super();
      target.append(this.canvas);
      rfbs.push(this as unknown as FakeRFB);
    }
    sendCredentials() {}
    disconnect() {
      this.canvas.remove();
    }
  },
}));
vi.mock("../views/theme", () => ({}));
vi.mock("./aes", () => ({ ensureSubtleAES: () => {} }));
vi.mock("../lib/api", async (actual) => ({
  ...(await actual<typeof import("../lib/api")>()),
  api: async (path: string) =>
    path === "/machines"
      ? { machines: [{ id: "macbook", name: "MacBook", online: true }] }
      : { enabled: true, screen_sharing: true, displays: [], screen_video: status.video },
}));
import { ScreenView } from "./ScreenView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const open = async (embedded = false) => {
  act(() => root.render(<ScreenView target={{ mid: "macbook", token: "tok", embedded, name: "MacBook", keep: "", unlock: false }} />));
  await flush();
};
// live; the picture menu, a lazy component, appears a moment later
const connect = async () => {
  act(() => void rfbs.at(-1)!.dispatchEvent(new CustomEvent("connect", { detail: {} })));
  await flush();
};
const button = () => host.querySelector<HTMLButtonElement>('[data-testid="screen-video"]');
const rows = () => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')];
const menuAppears = () => vi.waitFor(async () => {
  await act(async () => {});
  expect(button()).not.toBeNull();
}, { timeout: 3000 });

// Preload the chunk; each mounted page still commits its own import callback.
// Positive cases therefore wait for the actual button rather than another fixed flush.
beforeAll(async () => {
  await import("./ScreenMenu");
});

beforeEach(() => {
  rfbs.length = 0;
  status.video = { version: 1, codecs: ["hevc", "h264"] };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("the picture switcher when the Mac advertises video", () => {
  it("offers Video / VNC, adds Quality for video, and keeps the choice per Mac", async () => {
    await open();
    await connect();
    await menuAppears();
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-video.picture_current", { current: t("screen-video.picture_vnc") }));

    act(() => button()!.click());
    // VNC is the default until the video path lands, so VNC is checked and there is no Quality group yet.
    expect(rows().map((r) => [r.textContent, r.getAttribute("aria-checked")])).toEqual([
      [t("screen-video.picture_video"), "false"],
      [t("screen-video.picture_vnc"), "true"],
    ]);

    act(() => rows()[0].click()); // choose Video
    await flush();
    expect(localStorage.getItem("persona.screen.picture.macbook")).toBe("video");
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-video.picture_current", { current: t("screen-video.picture_video") }));

    act(() => button()!.click());
    expect(rows().map((r) => r.getAttribute("aria-checked")), "Video checked, the quality bias defaults to Auto").toEqual([
      "true", // Video
      "false", // VNC
      "true", // Auto
      "false", // Sharper
      "false", // Smoother
    ]);
    act(() => rows()[3].click()); // Sharper
    await flush();
    expect(localStorage.getItem("persona.screen.quality.macbook")).toBe("sharper");
  });

  it("comes back on the kept picture on the next page", async () => {
    localStorage.setItem("persona.screen.picture.macbook", "video");
    await open();
    await connect();
    await menuAppears();
    expect(button()?.getAttribute("aria-label")).toBe(t("screen-video.picture_current", { current: t("screen-video.picture_video") }));
    act(() => button()!.click());
    expect(rows(), "Video, VNC, and the three quality biases").toHaveLength(5);
  });
});

describe("the picture switcher otherwise", () => {
  it("is not offered when the Mac does not advertise the video channel", async () => {
    status.video = null; // an older node, or no helper
    await open();
    await connect();
    expect(button(), "no control a Mac can't use").toBeNull();
  });

  it("is not offered for version 0", async () => {
    status.video = { version: 0 };
    await open();
    await connect();
    expect(button()).toBeNull();
  });

  it("is not offered on a page an app embeds", async () => {
    await open(true);
    await connect();
    expect(button()).toBeNull();
  });
});
