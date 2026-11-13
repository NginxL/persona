// @vitest-environment jsdom
// Back from an Inbox item (#3329): a row opened from the list remembers the list under it, so ‹ and Done on the item go
// back to that entry, as the browser's Back would, instead of stacking another copy of the list on top of the item.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: query.includes("max-width: 1023px"), media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const calls = vi.hoisted(() => ({ items: [] as unknown[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [] });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({
  useInboxItemPlace: (source?: string) => source === "docs" ? { extension: "document-extension", extension_name: "Document extension", id: "event", title: "Event", placement: "inbox_item", size: "full", connector: "docs" } : undefined,
}));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("./LarkThread", () => ({ LarkThread: () => null, chatOfItem: () => ({}) }));

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { onOverlayEntry, overlayDepth } from "../../lib/back-close";
import { stepsToInbox } from "../../lib/inbox-nav";
import { settleRosterDefault } from "../../lib/manager-choice";
import { t } from "../../i18n";
import { Inbox } from "./Inbox";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string): InboxItem => ({
  id, source: "lark", kind: "group", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "text", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier: "action", version: `v-${id}`, lark: { feed_id: id, is_group: true },
});

const documentItem = (id: string): InboxItem => ({
  id, source: "docs", kind: "doc", account: "docs", account_name: "Documents", title: "Release plan", preview: "Document preview", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier: "action", version: `v-${id}`,
  doc: { url: "https://example.com/document", type: "doc", notice: "mention", rows: [{ ref: "row-1", title: "Original document row" }] },
  mutes: [{ tag: "sender", value: "u1", name: "Someone" }],
  chain: [{ id: "chain-1", kind: "received", text: "Open the document", link: { type: "source" } }],
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
/** Runs `go`, then waits for the Back it causes (history.go() is asynchronous). */
const popped = (go: () => void) =>
  new Promise<void>((done, fail) => {
    const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
    addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
    go();
  });
/** The item page, as the app draws it once the address is the item's. */
const openItem = async (id: string) => {
  act(() => root.render(<Inbox id={id} onToggleSidebar={() => {}} />));
  await flush();
};
const rowLink = (id: string) => host.querySelector<HTMLAnchorElement>(`[data-item="${id}"] a`)!;
const buttonNamed = (label: string) => [...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() === label).at(-1)!;
const detailText = () => host.querySelector<HTMLElement>(`section[aria-label="${t("inbox-inbox.detail")}"]`)!.textContent ?? "";
const openDocument = async () => {
  act(() => rowLink("d1").click());
  await openItem("d1");
  expect(host.querySelector("[data-event-body]")).not.toBeNull();
};
const openOriginalAndMenu = async () => {
  await openDocument();
  act(() => buttonNamed(t("inbox-inboxdetail.view_source")).click());
  act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inboxdetail.mute")}"]`)!.click());
  await flush();
  expect(document.querySelectorAll('[role="menuitem"]').length).toBeGreaterThan(0);
  expect(overlayDepth()).toBe(2);
};

beforeEach(async () => {
  calls.items = [item("a1"), item("a2"), documentItem("d1")];
  resetInboxForTests();
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollIntoView = vi.fn();
  history.pushState(null, "", "/inbox");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
  await flush();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

describe("Back from an Inbox item (#3329)", () => {
  it("‹ on an item opened from the list goes back to the list's entry", async () => {
    const start = history.length;
    act(() => rowLink("a2").click());
    expect(location.pathname).toBe("/inbox/a2");
    expect(stepsToInbox(history.state)).toBe(1);
    await openItem("a2");
    // the other row, beside the item on a wide screen: one more step from the list
    act(() => rowLink("a1").click());
    expect(stepsToInbox(history.state)).toBe(2);
    await openItem("a1");
    await popped(() => host.querySelector<HTMLButtonElement>('button[aria-label="返回收件箱"]')!.click());
    expect(location.pathname).toBe("/inbox");
    expect(history.length - start).toBe(2);
  });

  it("Done on an item opened from the list goes back to the list's entry", async () => {
    const start = history.length;
    act(() => rowLink("a2").click());
    await openItem("a2");
    const done = [...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "已处理").at(-1)!;
    await act(() => popped(() => done.click())); // marking it done settles after the click
    await flush();
    expect(location.pathname).toBe("/inbox");
    expect(history.length - start).toBe(1);
  });

  it("‹ on an item reached from elsewhere puts the list in its place", async () => {
    history.replaceState(null, "", "/inbox/a2");
    const start = history.length;
    await openItem("a2");
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="返回收件箱"]')!.click());
    expect(location.pathname).toBe("/inbox");
    expect(history.length).toBe(start);
  });

  it("Original pushes one same-address entry and Back returns to the event", async () => {
    await openDocument();
    const entries = history.length;
    act(() => buttonNamed(t("inbox-inboxdetail.view_source")).click());
    expect(location.pathname).toBe("/inbox/d1");
    expect(history.length).toBe(entries + 1);
    expect(onOverlayEntry()).toBe(true);
    expect(host.querySelector("[data-event-body]")).toBeNull();
    expect(detailText()).toContain("Original document row");
    expect(host.querySelector("[data-extension-inbox]")).toBeNull(); // an event goes straight to its own original

    await act(() => popped(() => history.back()));
    await flush();
    expect(location.pathname).toBe("/inbox/d1");
    expect(onOverlayEntry()).toBe(false);
    expect(host.querySelector("[data-event-body]")).not.toBeNull();
    expect(detailText()).not.toContain("Original document row");
  });

  it("a chain's source entry uses the same history step back to the event", async () => {
    await openDocument();
    const entries = history.length;
    act(() => buttonNamed("Open the document").click());
    expect(history.length).toBe(entries + 1);
    expect(onOverlayEntry()).toBe(true);
    expect(detailText()).toContain("Original document row");

    await act(() => popped(() => history.back()));
    await flush();
    expect(host.querySelector("[data-event-body]")).not.toBeNull();
    expect(onOverlayEntry()).toBe(false);
  });

  it("Handling closes Original and takes its history entry back off", async () => {
    await openDocument();
    act(() => buttonNamed(t("inbox-inboxdetail.view_source")).click());
    expect(onOverlayEntry()).toBe(true);

    await act(() => popped(() => buttonNamed(t("inbox-inboxdetail.view_event")).click()));
    await flush();
    expect(location.pathname).toBe("/inbox/d1");
    expect(host.querySelector("[data-event-body]")).not.toBeNull();
    expect(onOverlayEntry()).toBe(false);
    expect(stepsToInbox(history.state)).toBe(1);
  });

  it("‹ with Original open still goes back to the list", async () => {
    const start = history.length;
    await openDocument();
    act(() => buttonNamed(t("inbox-inboxdetail.view_source")).click());
    expect(onOverlayEntry()).toBe(true);

    await act(() => popped(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inbox.back_to_inbox")}"]`)!.click()));
    await flush();
    expect(location.pathname).toBe("/inbox");
    expect(onOverlayEntry()).toBe(false);
    expect(history.length - start).toBe(2);
  });

  it("Done with Original open still goes back to the list", async () => {
    const start = history.length;
    await openDocument();
    act(() => buttonNamed(t("inbox-inboxdetail.view_source")).click());
    expect(onOverlayEntry()).toBe(true);

    await act(() => popped(() => buttonNamed(t("inbox-inboxdetail.done")).click()));
    await flush();
    expect(location.pathname).toBe("/inbox");
    expect(onOverlayEntry()).toBe(false);
    expect(history.length - start).toBe(2);
  });

  it("‹ with Original and a menu open goes back past both to the list", async () => {
    const start = history.length;
    await openOriginalAndMenu();
    expect(history.length - start).toBe(3);

    await act(() => popped(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inbox.back_to_inbox")}"]`)!.click()));
    await flush();
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
    expect(overlayDepth()).toBe(0);
    expect(document.querySelectorAll('[role="menuitem"]')).toHaveLength(0);
    expect(history.length - start).toBe(3);
  });

  it("Done with Original and a menu open goes back past both to the list", async () => {
    const start = history.length;
    await openOriginalAndMenu();
    expect(history.length - start).toBe(3);

    await act(() => popped(() => buttonNamed(t("inbox-inboxdetail.done")).click()));
    await flush();
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
    expect(overlayDepth()).toBe(0);
    expect(document.querySelectorAll('[role="menuitem"]')).toHaveLength(0);
    expect(history.length - start).toBe(3);
  });

  it("opening another item takes Original's entry without an extra Back", async () => {
    await openDocument();
    act(() => buttonNamed(t("inbox-inboxdetail.view_source")).click());
    const entries = history.length;

    act(() => rowLink("a1").click());
    await openItem("a1");
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
    expect(location.pathname).toBe("/inbox/a1");
    expect(history.length).toBe(entries);
    expect(stepsToInbox(history.state)).toBe(2);
    expect(onOverlayEntry()).toBe(false);

    await act(() => popped(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inbox.back_to_inbox")}"]`)!.click()));
    expect(location.pathname).toBe("/inbox");
  });
});
