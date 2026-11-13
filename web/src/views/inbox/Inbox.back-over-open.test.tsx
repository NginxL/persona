// @vitest-environment jsdom
// ‹ and Done on an item while things are open over it, whatever the stack: nothing open, the item's original page
// (its own same-address entry, E1), and the original with a menu on top of it (a narrow screen puts the menu's entry
// on the stack too). Every one of them returns to the list's entry in a single press, never to the item again or to
// the page under the menu.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  // narrow: menus take a history entry, as on a phone or a small window
  window.matchMedia ??= ((query: string) => ({ matches: query.includes("max-width: 1023px"), media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
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
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("./InboxLine", () => ({ InboxLine: () => null }));

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { onOverlayEntry } from "../../lib/back-close";
import { stepsToInbox } from "../../lib/inbox-nav";
import { settleRosterDefault } from "../../lib/manager-choice";
import { t } from "../../i18n";
import { Inbox } from "./Inbox";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const documentItem = (id: string): InboxItem => ({
  id, source: "docs", kind: "doc", account: "docs", account_name: "Documents", title: "Release plan", preview: "Document preview", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier: "action", version: `v-${id}`,
  doc: { url: "https://example.com/document", type: "doc", notice: "mention", rows: [{ ref: "row-1", title: "A row" }] },
  chain: [{ id: "chain-1", kind: "received", text: "Open the document" }],
  mutes: [{ tag: "sender", value: "u1", name: "Someone" }],
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
/** Everything a close-from-the-page does happens a tick later (back-close's 0 ms timer, then its Back). */
const settleTimers = async () => {
  await act(async () => { await new Promise((r) => setTimeout(r, 80)); });
  await flush();
};
const button = (label: string) => [...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() === label).at(-1);
const byLabel = (label: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
const openItem = async () => {
  act(() => host.querySelector<HTMLAnchorElement>('[data-item="docs:d1"] a')!.click());
  await flush();
  act(() => root.render(<Inbox id="docs:d1" onToggleSidebar={() => {}} />));
  await flush();
};
const openOriginal = async () => {
  await act(async () => button(t("inbox-inboxdetail.view_source"))!.click());
  await flush();
};
const openMuteMenu = async () => {
  await act(async () => byLabel(t("inbox-inboxdetail.mute"))!.click());
  await flush();
  expect(document.querySelectorAll('[role="menuitem"]').length).toBeGreaterThan(0);
};
/** The header's ‹, the way a finger gets to it: a press elsewhere closes the menu first, then the button's click. */
const pressBack = async () => {
  await act(async () => { window.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })); });
  await act(async () => { byLabel(t("inbox-inbox.back_to_inbox"))!.click(); });
  await settleTimers();
};
const pressDone = async () => {
  await act(async () => { window.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })); });
  await act(async () => { button(t("inbox-inboxdetail.done"))!.click(); });
  await settleTimers();
};

beforeEach(async () => {
  calls.items = [documentItem("docs:d1")];
  resetInboxForTests();
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, text: async () => JSON.stringify({}) })));
  Element.prototype.scrollIntoView = vi.fn();
  history.pushState(null, "", "/inbox");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

describe("‹ and Done land on the list however many entries are open over the item", () => {
  it("nothing open: one press to the list", async () => {
    await openItem();
    expect(stepsToInbox(history.state)).toBe(1);
    await pressBack();
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
  });

  it("the original open: one press to the list", async () => {
    await openItem();
    await openOriginal();
    expect(onOverlayEntry(history.state)).toBe(true);
    await pressBack();
    expect(location.pathname).toBe("/inbox");
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("the original and a menu over it: one press to the list", async () => {
    await openItem();
    await openOriginal();
    await openMuteMenu();
    await pressBack();
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("Done, nothing open: one press to the list", async () => {
    await openItem();
    await pressDone();
    expect(location.pathname).toBe("/inbox");
  });

  it("Done with the original open: one press to the list", async () => {
    await openItem();
    await openOriginal();
    await pressDone();
    expect(location.pathname).toBe("/inbox");
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("Done with the original and a menu over it: one press to the list", async () => {
    await openItem();
    await openOriginal();
    await openMuteMenu();
    await pressDone();
    expect(location.pathname).toBe("/inbox");
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("Back afterwards does not reopen the item, for any of them", async () => {
    await openItem();
    await openOriginal();
    await openMuteMenu();
    await pressBack();
    expect(location.pathname).toBe("/inbox");
    // nothing ahead of the list was left behind: the entries are still there but the item's is not Back's next stop
    expect(stepsToInbox(history.state)).toBe(0);
  });
});
