// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// the sidebar's modules read the colour scheme when they load
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

import { initI18n } from "../i18n";
import { setToken } from "../lib/api";
import type { AutoOrigin } from "../lib/conversation-origins";
import { resetManagerChoice } from "../lib/manager-choice";
import { forgetOpenerIcons } from "../lib/opener-icons";
import { getState } from "../lib/store";
import { emptyTaskIndex } from "../lib/tasks";
import { segments } from "../lib/turns";
import type { Item, Session } from "../lib/types";
import { NO_WORKFLOW_CALLS } from "../lib/workflows";
import { ChatContext } from "../views/chat/chat-context";
import { SegmentView } from "../views/chat/SegmentView";
import { SessionRow } from "../views/Sidebar";
import { OriginMark } from "./ChannelMark";

vi.mock("../lib/channels", async (original) => ({ ...(await original<typeof import("../lib/channels")>()), useChannels: () => [] }));

// A conversation an extension opened is marked with that extension's own icon and named after it (#10358): a
// third-party extension that reuses lark.agent is neither drawn nor named as Lark. Without its icon drawn (loading, none,
// not installed, failed) and for the Lark extension's own /agent the mark and its words stay as they were. Tests run in
// zh-Hans unless they say otherwise.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const icon = (id: string) => `/api/v1/extensions/${id}/-/icon?v=1.0.0`;
type Listed = { id: string; name: string; manager?: string; icon_url?: string; contributes?: Record<string, string[]> };
let installed: Listed[];
let iconStatus: number;
let host: HTMLDivElement, plain: HTMLDivElement, root: Root, plainRoot: Root;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); }); };
const urls = () => vi.mocked(fetch).mock.calls.map(([url]) => String(url));
/** The extension's own icon in a mark (Lark's tile is a picture too). */
const logo = (el: Element) => el.querySelector<HTMLImageElement>("[data-opener] [data-extension-logo] > img");
/** The icon's image comes in, or fails. */
const load = (el: Element, event: "load" | "error" = "load") => act(() => { logo(el)!.dispatchEvent(new Event(event)); });

// the lazy chunk's module, loaded once up front: a busy run's first import must not outlast the flushes below
beforeAll(async () => { await import("./OpenerTile"); });
beforeEach(async () => {
  await initI18n("zh-Hans");
  resetManagerChoice();
  forgetOpenerIcons();
  setToken(`synthetic-${Math.random()}`);
  Object.assign(getState(), { features: ["extensions"] });
  installed = [
    { id: "chat-bridge", name: "Chat Bridge", manager: "assistant", icon_url: icon("chat-bridge") },
    // the extension Lark's mark stands for: it brings the `lark` connector
    { id: "lark", name: "飞书", manager: "assistant", icon_url: icon("lark"), contributes: { connectors: ["lark"] } },
    { id: "no-icon", name: "No Icon", manager: "assistant" },
    { id: "unnamed", name: "", manager: "assistant", icon_url: icon("unnamed") },
  ];
  iconStatus = 200;
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (String(url).includes("/-/icon")) return iconStatus === 200 ? new Response("png", { headers: { "Content-Type": "image/png" } }) : new Response("", { status: iconStatus });
    if (String(url).endsWith("/api/v1/extensions")) return Response.json({ extensions: installed });
    return new Response("", { status: 404 });
  }));
  Object.defineProperty(URL, "createObjectURL", { configurable: true, value: vi.fn(() => "blob:synthetic-icon") });
  Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
  host = document.createElement("div"); plain = document.createElement("div");
  document.body.append(host, plain);
  root = createRoot(host); plainRoot = createRoot(plain);
});
afterEach(() => {
  act(() => { root.unmount(); plainRoot.unmount(); });
  host.remove(); plain.remove();
  vi.unstubAllGlobals();
  Object.assign(getState(), { features: [] });
});

/** The mark for a conversation of `origin` opened as `session` says, and today's mark (no opener) beside it. */
async function draw(origin: AutoOrigin, session?: Pick<Session, "opened_by" | "origin_ref" | "manager">) {
  act(() => {
    root.render(<OriginMark origin={origin} size={16} session={session} />);
    plainRoot.render(<OriginMark origin={origin} size={16} />);
  });
  await flush();
  return host.querySelector<HTMLElement>("[data-origin-mark]")!;
}
const today = () => plain.querySelector("[data-origin-mark]")!.outerHTML;
const named = (mark: Element) => [mark.getAttribute("aria-label"), mark.getAttribute("title")];

describe("an extension's conversation is marked with the extension that opened it (#10358)", () => {
  it("draws and names the opener of a lark_agent conversation another extension opened, once its icon is in", async () => {
    const mark = await draw("lark_agent", { opened_by: "chat-bridge", origin_ref: "chat-bridge" });
    const image = logo(mark)!;
    expect(mark.querySelector("[data-opener]")?.getAttribute("data-opener")).toBe("chat-bridge");
    expect(image.getAttribute("src")).toBe("blob:synthetic-icon");
    expect(urls()).toContain(icon("chat-bridge"));
    // until the icon is in, Lark's tile and Lark's words stay
    expect(image.className).toContain("opacity-0");
    expect(mark.querySelector(".shadow-hairline")).not.toBeNull();
    expect(named(mark)).toEqual(["来自 飞书 /agent", "来自 飞书 /agent"]);
    load(mark);
    // then the icon alone, in the tile's size and corner, named after the extension
    expect(mark.querySelector(".shadow-hairline")).toBeNull();
    expect(named(mark)).toEqual(["来自 Chat Bridge", "来自 Chat Bridge"]);
    const tile = mark.querySelector<HTMLElement>("[data-opener]")!;
    expect([tile.style.width, tile.style.height, tile.style.borderRadius]).toEqual(["16px", "16px", "4px"]);
    expect(mark.getAttribute("role")).toBe("img");
    expect(mark.style.width).toBe("16px");
  });

  it("asks the icon of the manager whose extension it is", async () => {
    installed = [{ id: "chat-bridge", name: "Chat Bridge", manager: "m-2", icon_url: icon("chat-bridge") }];
    await draw("lark_agent", { opened_by: "chat-bridge", manager: "m-2" });
    const call = vi.mocked(fetch).mock.calls.find(([url]) => String(url).includes("/-/icon"));
    expect(new Headers(call?.[1]?.headers).get("X-Persona-Manager")).toBe("m-2");
  });

  it("keeps Lark's mark and words for a lark_agent conversation whose opener has no icon, is not installed or fails", async () => {
    for (const session of [{ opened_by: "no-icon" }, { opened_by: "removed" }, { origin_ref: "removed" }]) {
      const mark = await draw("lark_agent", session);
      expect(mark.outerHTML).toBe(today());
      expect(logo(mark)).toBeNull();
    }
    // another manager's copy is not this conversation's opener
    installed = [{ id: "chat-bridge", name: "Chat Bridge", manager: "m-2", icon_url: icon("chat-bridge") }];
    forgetOpenerIcons();
    expect((await draw("lark_agent", { opened_by: "chat-bridge" })).outerHTML).toBe(today());
    // an icon the hub does not serve leaves Lark's tile and words
    installed = [{ id: "chat-bridge", name: "Chat Bridge", manager: "assistant", icon_url: icon("chat-bridge") }];
    forgetOpenerIcons();
    iconStatus = 404;
    const failed = await draw("lark_agent", { opened_by: "chat-bridge" });
    expect(logo(failed)).toBeNull();
    expect(failed.querySelector(".shadow-hairline")).not.toBeNull();
    expect(named(failed)).toEqual(["来自 飞书 /agent", "来自 飞书 /agent"]);
  });

  it("goes back to Lark's words when an image the browser cannot draw fails", async () => {
    const mark = await draw("lark_agent", { opened_by: "chat-bridge" });
    load(mark);
    expect(named(mark)[0]).toBe("来自 Chat Bridge");
    load(mark, "error");
    expect(logo(mark)).toBeNull();
    expect(mark.querySelector(".shadow-hairline")).not.toBeNull();
    expect(named(mark)).toEqual(["来自 飞书 /agent", "来自 飞书 /agent"]);
  });

  it("keeps the Lark extension's own /agent exactly as it was, without asking for its icon", async () => {
    const mark = await draw("lark_agent", { opened_by: "lark" });
    expect(mark.outerHTML).toBe(today());
    expect(named(mark)).toEqual(["来自 飞书 /agent", "来自 飞书 /agent"]);
    expect(urls().some((u) => u.includes("/-/icon"))).toBe(false);
  });

  it("draws and names the opener of an extension conversation, and keeps today's mark or words without its icon or name", async () => {
    const withIcon = await draw("extension", { opened_by: "chat-bridge" });
    expect(named(withIcon)[0]).toBe("来自扩展");
    load(withIcon);
    expect(logo(withIcon)?.getAttribute("src")).toBe("blob:synthetic-icon");
    expect(named(withIcon)).toEqual(["来自 Chat Bridge", "来自 Chat Bridge"]);
    // an extension with no name to say: its icon, the origin's words
    const unnamed = await draw("extension", { opened_by: "unnamed" });
    load(unnamed);
    expect(logo(unnamed)).not.toBeNull();
    expect(named(unnamed)).toEqual(["来自扩展", "来自扩展"]);
    for (const session of [{ opened_by: "no-icon" }, { origin_ref: "removed" }]) {
      expect((await draw("extension", session)).outerHTML).toBe(today());
    }
  });

  it("names the extension in every language with the existing \"From {name}\" words", async () => {
    for (const [lang, words] of [["en", "From Chat Bridge"], ["ja", "Chat Bridgeから"], ["zh-Hans", "来自 Chat Bridge"]] as const) {
      await initI18n(lang);
      const mark = await draw("lark_agent", { opened_by: "chat-bridge" });
      load(mark);
      expect(named(mark)).toEqual([words, words]);
    }
  });

  it("leaves an older conversation with no opener as it was, without reading the extensions", async () => {
    for (const origin of ["lark_agent", "extension"] as const) {
      expect((await draw(origin, {})).outerHTML).toBe(today());
    }
    // other origins' refs are not extensions
    expect((await draw("inbox", { origin_ref: "chat-bridge" })).outerHTML).toBe(today());
    expect(fetch).not.toHaveBeenCalled();
  });

  it("names the opener under the message that opened the conversation, as its rows", async () => {
    const msg: Item = { id: "u1#0", seq: 1, role: "user", kind: "text", text: "整理成周报草稿" };
    const show = (opened_by: string) => act(() => root.render(
      <ChatContext.Provider value={{ tasks: emptyTaskIndex(), subagents: [], scope: "", workflowCalls: NO_WORKFLOW_CALLS, opening: { id: "u1#0", origin: "lark_agent", opened_by } }}>
        <SegmentView seg={segments([msg])[0]} live={false} mid="m" sid="s" expandFold={async () => {}} />
      </ChatContext.Provider>,
    ));
    show("chat-bridge");
    await flush();
    const line = () => host.querySelector<HTMLElement>("[data-opened-by=lark_agent]")!;
    expect(line().textContent).toBe("来自 飞书 /agent");
    load(line());
    expect(line().querySelector("[data-origin-mark]")?.getAttribute("aria-hidden")).toBe("true");
    expect(line().textContent).toBe("来自 Chat Bridge");
    // the Lark extension's own: Lark's mark and words
    show("lark");
    await flush();
    expect(logo(line())).toBeNull();
    expect(line().textContent).toBe("来自 飞书 /agent");
  });

  it("names the opener in the sidebar row's tooltip while its icon is drawn", async () => {
    const s: Session = { machine_id: "hub", session_id: "c1", title: "1105 | 研究 | 群里的需求汇总", cwd: "", status: "idle", assistant: true, origin: "lark_agent", opened_by: "chat-bridge" };
    const props = { k: "j:c1", reg: () => () => {}, hover: () => {}, active: false, to: { name: "persona", sid: "c1" } as const, go: () => () => {}, showState: false };
    const row = () => host.querySelector<HTMLAnchorElement>("a")!;
    act(() => root.render(<SessionRow {...props} s={s} />));
    await flush();
    expect(row().title).toContain("来自 飞书 /agent");
    load(row());
    expect(row().title).toContain("来自 Chat Bridge");
    expect(row().title).not.toContain("飞书 /agent");
    // the Lark extension's own row says Lark /agent
    act(() => root.render(<SessionRow {...props} s={{ ...s, opened_by: "lark" }} />));
    await flush();
    expect(logo(row())).toBeNull();
    expect(row().title).toContain("来自 飞书 /agent");
  });
});
