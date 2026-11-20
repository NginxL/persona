// @vitest-environment jsdom
// A chat opened from Messages lands on its first unread message, the one after Feishu's read position its row carries,
// read once as it opens; a chat the loaded pages do not hold is looked for further down the list first.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LarkConversation } from "../../lib/lark-chats";

vi.mock("../../lib/store", async (original) => ({ ...(await original<typeof import("../../lib/store")>()), useLarkOn: () => true }));
// Collect the component graph before the tests' deadlines. page() still gives each test fresh list stores.
await import("./Messages");
// a fresh module per test: the chat list is the tab's own store
const page = async () => {
  vi.resetModules();
  (await import("../../lib/lark-chats")).setLarkHome(true);
  return (await import("./Messages")).Messages;
};

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const row = (id: string, more: Partial<LarkConversation> = {}): LarkConversation => ({
  id, name: id, is_group: true, unread: 0, at_me: false, muted: false, preview: "", is_mine: false, updated_at: "2026-11-04T10:00:00Z", ...more,
});

let host: HTMLDivElement;
let root: Root;
let pages: Record<string, { conversations: LarkConversation[]; next_cursor?: string }>;
const opened: string[] = [];
const landed = () => vi.waitFor(async () => {
  await act(async () => {});
  expect(opened).toHaveLength(1);
});

beforeEach(() => {
  opened.length = 0;
  localStorage.clear();
  vi.stubGlobal("IntersectionObserver", class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } });
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollTo = vi.fn();
  Element.prototype.scrollIntoView = vi.fn();
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    let body: unknown = {};
    if (url.includes("/lark/conversations?")) {
      body = { account_id: "acc", ...pages[new URL(url, "http://hub").searchParams.get("cursor") ?? ""] };
    } else if (url.includes("/lark/pinned")) body = { account_id: "acc", conversations: [] };
    else if (url.includes("/lark/accounts")) body = { accounts: [] };
    else if (/\/lark\/conversations\/[^/]+\/messages\?/.test(url)) {
      opened.push(url);
      body = { messages: [], has_older: false, has_newer: false };
    }
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const around = (url: string) => new URL(url, "http://hub").searchParams.get("around");

describe("opening a chat from Messages", () => {
  it("opens an unread chat at the message after its read position", async () => {
    pages = { "": { conversations: [row("oc_1", { unread: 2940, read_position: "164574" })] } };
    const Messages = await page();
    await act(async () => root.render(<Messages id="oc_1" onToggleSidebar={() => {}} />));
    await landed();
    expect(opened.map(around)).toEqual(["164575"]);
  });

  it("opens a chat with nothing unread at the newest", async () => {
    pages = { "": { conversations: [row("oc_1", { unread: 0, read_position: "500" })] } };
    const Messages = await page();
    await act(async () => root.render(<Messages id="oc_1" onToggleSidebar={() => {}} />));
    await landed();
    expect(opened.map(around)).toEqual([null]);
  });

  it("looks further down the list for a chat the loaded pages do not hold", async () => {
    pages = { "": { conversations: [row("oc_1")], next_cursor: "2" }, "2": { conversations: [row("oc_9", { unread: 5, read_position: "40" })] } };
    const Messages = await page();
    await act(async () => root.render(<Messages id="oc_9" onToggleSidebar={() => {}} />));
    await landed();
    expect(opened.map(around)).toEqual(["41"]);
  });
});
