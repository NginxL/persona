// @vitest-environment jsdom
// An Inbox history entry keeps the view that was on it when another route took over: list search, section folds, list
// scroll, the item's handling-chain expansion, and an embedded original's expansion. A new visit (including a reload)
// starts clean.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
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
vi.mock("./LarkThread", () => ({ LarkThread: () => <div data-chat-body />, chatOfItem: () => ({}) }));

import { reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { t } from "../../i18n";
import { resetInboxViewMemoryForTests } from "../../lib/inbox-view-memory";
import { settleRosterDefault } from "../../lib/manager-choice";
import { Inbox } from "./Inbox";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, tier: InboxItem["tier"]): InboxItem => ({
  id, source: "lark", kind: "group", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "text", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier, version: `v-${id}`, lark: { feed_id: id, is_group: true },
  chain: [
    { id: `${id}-received`, kind: "received", text: "received" },
    { id: `${id}-triaged`, kind: "triaged", text: "triaged" },
    { id: `${id}-handling`, kind: "handling", text: "handling" },
    { id: `${id}-waiting`, kind: "waiting", text: "waiting" },
  ],
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const page = (id?: string) => <StrictMode><Inbox id={id} onToggleSidebar={() => {}} /></StrictMode>;
const show = async (id?: string) => {
  act(() => root.render(page(id)));
  await flush();
};
const rowLink = (id: string) => host.querySelector<HTMLAnchorElement>(`[data-item="${id}"] a`)!;
const search = () => host.querySelector<HTMLInputElement>('input[aria-label]')!;
const fyiToggle = () => host.querySelector<HTMLButtonElement>('[data-section="fyi"] > div > button[aria-expanded]')!;
// the section bar (#9053): its choices, and the one chosen
const barChoice = (id: string) => host.querySelector<HTMLButtonElement>(`[data-bar-choice="${id}"]`)!;
const chosen = () => host.querySelector<HTMLElement>('[data-bar-choice][aria-checked="true"]')?.dataset.barChoice;
const sectionsShown = () => [...host.querySelectorAll<HTMLElement>("[data-section]")].map((el) => el.dataset.section);
const chainToggle = () => host.querySelector<HTMLButtonElement>("[data-chain-toggle]")!;
const chainRows = () => host.querySelectorAll("[data-chain-entry]");
const originalToggle = () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]')!;
const originalOpen = () => host.querySelector('[data-event-body="embedded"]') !== null;
const listScroll = () => host.querySelector<HTMLElement>("[data-inbox-list-scroll]")!;
const collapsedOpen = () => host.querySelector('[data-section="noise"] button[aria-expanded]')?.getAttribute("aria-expanded");
const backButton = () => host.querySelector<HTMLButtonElement>(`header button[aria-label="${t("inbox-inbox.back_to_inbox")}"]`)!;
const done = (id: string): InboxItem => ({ ...item(id, "action"), done_at: "2026-11-09T05:00:00Z", done_by: "user" });
const reload = async () => {
  await act(async () => { await reloadInbox(); });
  await flush();
};
// jsdom lays nothing out: what the page draws is what this says. Wide: the list beside the opened item; narrow: the
// item takes the list's place, and the list has it back when the item closes (as Inbox.keyboard.test.tsx)
const layout = (shape: "wide" | "narrow") => {
  const pane = (name: string) => host.querySelector(`section[aria-label="${t(name)}"]`);
  Object.defineProperty(Element.prototype, "checkVisibility", {
    configurable: true,
    value(this: Element) {
      if (shape === "wide") return true;
      return location.pathname !== "/inbox" ? !pane("inbox-inbox.list")?.contains(this) : !pane("inbox-inbox.detail")?.contains(this);
    },
  });
};
const setSearch = async (value: string) => {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(search(), value);
    search().dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const openItem = async (id: string) => {
  act(() => rowLink(id).click());
  expect(location.pathname).toBe(`/inbox/${id}`);
  await show(id);
};
// an item opened from inside the Inbox: the open item's Related line (as a line of its chain does)
const openRelated = async (id: string) => {
  const line = [...host.querySelectorAll<HTMLButtonElement>("[data-event-related] button")].find((b) => b.textContent === `chat ${id}`)!;
  act(() => line.click());
  expect(location.pathname).toBe(`/inbox/${id}`);
  await show(id);
};
const leaveForChat = async () => {
  await act(async () => {
    history.pushState(null, "", "/persona/c1");
    root.render(<div>chat</div>);
  });
  await flush();
};
const popped = (go: () => void) => new Promise<void>((done, fail) => {
  const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
  addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
  go();
});
const backToItem = async (id: string) => {
  await act(async () => popped(() => history.back()));
  await show(id);
};

beforeEach(async () => {
  calls.items = [item("a1", "action"), item("a2", "action"), item("f1", "fyi")];
  resetInboxForTests();
  resetInboxViewMemoryForTests();
  vi.stubGlobal("CSS", { escape: (value: string) => value });
  Element.prototype.scrollIntoView = vi.fn();
  history.replaceState(null, "", "/inbox");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await show();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  delete (Element.prototype as { checkVisibility?: unknown }).checkVisibility;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  resetInboxViewMemoryForTests();
  history.replaceState(null, "", "/");
});

describe("Inbox history-entry view memory", () => {
  it("restores search, section folds, list scroll, and chain expansion after leaving and Back", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => fyiToggle().click());
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("false");
    await act(async () => chainToggle().click());
    expect(chainRows()).toHaveLength(4);
    await act(async () => {
      listScroll().scrollTop = 180;
      listScroll().dispatchEvent(new Event("scroll"));
    });

    await leaveForChat();
    await backToItem("a1");

    expect(search().value).toBe("chat");
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("false");
    expect(chainRows()).toHaveLength(4);
    expect(listScroll().scrollTop).toBe(180);
  });

  it("restores an expanded original after leaving and Back, but starts collapsed on fresh navigation", async () => {
    await openItem("a1");
    expect(originalOpen()).toBe(false);
    expect(host.querySelector("[data-chat-body]")).toBeNull();
    await act(async () => originalToggle().click());
    expect(originalOpen()).toBe(true);
    expect(host.querySelector("[data-chat-body]")).not.toBeNull();

    await leaveForChat();
    await backToItem("a1");
    expect(originalOpen()).toBe(true);
    expect(host.querySelector("[data-chat-body]")).not.toBeNull();

    await leaveForChat();
    await act(async () => history.pushState(null, "", "/inbox/a1"));
    await show("a1");
    expect(originalOpen()).toBe(false);
    expect(host.querySelector("[data-chat-body]")).toBeNull();
  });

  it("starts clean on a fresh navigation and when the page memory is gone after a reload", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => fyiToggle().click());
    await act(async () => chainToggle().click());
    await leaveForChat();

    await act(async () => history.pushState(null, "", "/inbox/a1"));
    await show("a1");
    expect(search().value).toBe("");
    // on Needs you, the section bar's start (#9053); All has FYI open as at first
    expect(chosen()).toBe("action");
    await act(async () => barChoice("all").click());
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("true");
    expect(chainRows()).toHaveLength(3);
    expect(listScroll().scrollTop).toBe(0);

    await act(async () => root.unmount());
    resetInboxViewMemoryForTests(); // a reload drops the module map but keeps this entry's history.state
    root = createRoot(host);
    await show("a1");
    expect(search().value).toBe("");
    expect(chosen()).toBe("action");
    await act(async () => barChoice("all").click());
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("true");
    expect(chainRows()).toHaveLength(3);
  });

  it("restores an already-visited item immediately on an in-Inbox Back", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => chainToggle().click());
    await openItem("a2");
    await setSearch("chat a2");
    expect(chainRows()).toHaveLength(3);

    await act(async () => popped(() => history.back()));
    await show("a1");
    expect(search().value).toBe("chat");
    expect(chainRows()).toHaveLength(4);
  });

  it("does not let a post-reload entry reuse an old entry's identifier and snapshot", async () => {
    await setSearch("before reload");
    const oldEntry = (history.state as Record<string, unknown>).personaInboxView;
    await leaveForChat();

    resetInboxViewMemoryForTests(); // another page load: a new prefix, the entry counter starting again
    await act(async () => history.pushState(null, "", "/inbox"));
    await show();
    const newEntry = (history.state as Record<string, unknown>).personaInboxView;
    expect(newEntry).not.toBe(oldEntry);
    await setSearch("after reload");
    await act(async () => root.render(<div>outside</div>));
    await flush();

    await act(async () => popped(() => history.go(-2)));
    await show();
    expect(search().value).toBe("");
  });

  it("copies the current list view to a row's new entry without moving another item's chain expansion", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => fyiToggle().click());
    await act(async () => chainToggle().click());
    const firstEntry = (history.state as Record<string, unknown>).personaInboxView;

    await openItem("a2");
    expect(chainRows()).toHaveLength(3);
    expect((history.state as Record<string, unknown>).personaInboxSteps).toBe(2);
    expect((history.state as Record<string, unknown>).personaInboxView).not.toBe(firstEntry);

    await leaveForChat();
    await backToItem("a2");
    expect(search().value).toBe("chat");
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("false");
    expect(chainRows()).toHaveLength(3);
  });

  // the section bar (#9053): its choice is the entry's, as the search is
  it("keeps the section bar's choice with the entry: Back brings it back, a fresh navigation opens on Needs you", async () => {
    expect(chosen()).toBe("action");
    expect(sectionsShown()).toEqual(["action"]);
    await act(async () => barChoice("fyi").click());
    expect(sectionsShown()).toEqual(["fyi"]);
    await openItem("f1");
    expect(chosen()).toBe("fyi"); // kept while the Inbox is open

    await leaveForChat();
    await backToItem("f1");
    expect(chosen()).toBe("fyi");

    await leaveForChat();
    await act(async () => history.pushState(null, "", "/inbox"));
    await show();
    expect(chosen()).toBe("action");
  });

  // the section bar's landing (docs/assistant/inbox.md "Section bar"): an item from outside the Inbox (a link, ⌘K, a
  // Needs-you line elsewhere, a reload) lands the list where it lives, for good; one from inside (another item's chain or
  // relations) moves only a list beside it, and Back or closing returns that list to the choice it had
  it("lands the list on an item opened from outside the Inbox, and leaves it there when the item closes", async () => {
    layout("narrow");
    calls.items = [...calls.items, done("d1"), item("n1", "noise")];
    await reload();
    // a link from elsewhere, or a reload, on the item's address: no list under it
    await act(async () => root.render(<div>chat</div>));
    await act(async () => history.replaceState(null, "", "/inbox/d1"));
    await show("d1");
    expect(chosen()).toBe("done");
    await act(async () => backButton().click());
    expect(location.pathname).toBe("/inbox");
    await show();
    expect(chosen()).toBe("done"); // on a phone, closing it shows the list where the item lives
    expect(host.querySelector('[data-section="done"] [data-item="d1"]')).not.toBeNull();

    // ⌘K over the list to a collapsed one: All with Collapsed open, and so it stays after Back
    await act(async () => history.pushState(null, "", "/inbox/n1"));
    await show("n1");
    expect(chosen()).toBe("all");
    expect(collapsedOpen()).toBe("true");
    await act(async () => popped(() => history.back()));
    await show();
    expect(chosen()).toBe("all");
    expect(collapsedOpen()).toBe("true");
  });

  it("leaves a list hidden under the item alone for an item opened from inside, and Back finds it on its choice", async () => {
    layout("narrow");
    calls.items = [{ ...item("a1", "action"), related: [{ id: "d1", title: "chat d1" }] }, item("a2", "action"), item("f1", "fyi"), done("d1")];
    await reload();
    await openItem("a1");
    await openRelated("d1");
    expect(chosen()).toBe("action"); // not moved: it isn't shown
    expect(sectionsShown()).toEqual(["action"]);
    await act(async () => popped(() => history.back()));
    await show("a1");
    expect(chosen()).toBe("action");
    // closing the item instead: the list as it was
    await openRelated("d1");
    await act(async () => popped(() => backButton().click()));
    expect(location.pathname).toBe("/inbox");
    await show();
    expect(chosen()).toBe("action");
    expect(sectionsShown()).toEqual(["action"]);
  });

  it("moves a list beside the item to one opened from inside, and Back returns it to the choice it had", async () => {
    layout("wide");
    calls.items = [{ ...item("a1", "action"), related: [{ id: "d1", title: "chat d1" }] }, item("a2", "action"), item("f1", "fyi"), done("d1")];
    await reload();
    await openItem("a1");
    expect(chosen()).toBe("action"); // the list shows it: no move
    await openRelated("d1");
    expect(chosen()).toBe("done");
    expect(host.querySelector('[data-section="done"] [data-item="d1"] a')?.getAttribute("aria-current")).toBe("page");
    await act(async () => popped(() => history.back()));
    await show("a1");
    expect(chosen()).toBe("action");
    // Done in the item's header, or ‹: back to the list, on the choice it had
    await openRelated("d1");
    expect(chosen()).toBe("done");
    await act(async () => popped(() => backButton().click()));
    expect(location.pathname).toBe("/inbox");
    await show();
    expect(chosen()).toBe("action");
  });

  // an item a link opened has no list under it, yet what its Related line opens is from inside the Inbox
  it("takes an item opened from a linked item as opened from inside", async () => {
    layout("wide");
    calls.items = [{ ...item("a1", "action"), related: [{ id: "d1", title: "chat d1" }] }, item("a2", "action"), item("f1", "fyi"), done("d1")];
    await reload();
    await act(async () => root.render(<div>chat</div>));
    await act(async () => history.replaceState(null, "", "/inbox/a1"));
    await show("a1");
    expect(chosen()).toBe("action");
    await openRelated("d1");
    expect(chosen()).toBe("done");
    await act(async () => backButton().click()); // no list under either: the list takes the item's place
    expect(location.pathname).toBe("/inbox");
    await show();
    expect(chosen()).toBe("action");
  });

  // what an inside landing changed is undone with it: Collapsed folds again and the list scrolls back where it was
  it("folds Collapsed again and puts the scroll back when the list goes back from a landing", async () => {
    layout("wide");
    calls.items = [{ ...item("a1", "action"), related: [{ id: "n1", title: "chat n1" }] }, item("a2", "action"), item("f1", "fyi"), item("n1", "noise")];
    await reload();
    await act(async () => {
      listScroll().scrollTop = 120;
      listScroll().dispatchEvent(new Event("scroll"));
    });
    await openItem("a1");
    await openRelated("n1");
    expect(chosen()).toBe("all");
    expect(collapsedOpen()).toBe("true");
    await act(async () => {
      listScroll().scrollTop = 400;
      listScroll().dispatchEvent(new Event("scroll"));
    });
    await act(async () => popped(() => backButton().click()));
    await show();
    expect(chosen()).toBe("action");
    expect(listScroll().scrollTop).toBe(120);
    await act(async () => barChoice("all").click());
    expect(collapsedOpen()).toBe("false");
  });

  // a search typed after an inside landing takes over from it: closing the item leaves the search on All, and clearing
  // the search goes back to the choice the reader had, not to the one the item moved the list to
  it("lets a search typed after a landing put aside the choice the reader had before it", async () => {
    layout("wide");
    calls.items = [{ ...item("a1", "action"), related: [{ id: "d1", title: "chat d1" }] }, item("a2", "action"), item("f1", "fyi"), done("d1")];
    await reload();
    await openItem("a1");
    await openRelated("d1");
    expect(chosen()).toBe("done");
    await setSearch("chat");
    expect(chosen()).toBe("all");
    await act(async () => popped(() => backButton().click()));
    await show();
    expect(search().value).toBe("chat");
    expect(chosen()).toBe("all");
    await setSearch("");
    expect(chosen()).toBe("action");
  });

  // an old done item sits in a group Done keeps folded at first: the landing opens it, so its row is in sight, and from
  // outside the group stays open when the item closes
  it("opens the folded group a landed item is in", async () => {
    layout("narrow");
    const daysAgo = (n: number) => new Date(Date.now() - n * 86_400_000).toISOString();
    calls.items = [...calls.items, { ...item("d0", "action"), done_at: daysAgo(0), done_by: "user" }, { ...item("d40", "action"), done_at: daysAgo(40), done_by: "user" }];
    await reload();
    await act(async () => history.pushState(null, "", "/inbox/d40"));
    await show("d40");
    expect(chosen()).toBe("done");
    const groupOpen = () => {
      const row = host.querySelector('[data-item="d40"]')!;
      return row.closest("[inert]") === null && row.closest("[data-group]")!.querySelector("button[aria-expanded]")!.getAttribute("aria-expanded") === "true";
    };
    expect(groupOpen()).toBe(true);
    await act(async () => backButton().click());
    await show();
    expect(chosen()).toBe("done");
    expect(groupOpen()).toBe(true);
  });

  it("does not replace history for search, folds, scrolling, chain expansion, or original expansion", async () => {
    await openItem("a1");
    const replace = vi.spyOn(history, "replaceState");
    await setSearch("c");
    await setSearch("ch");
    await setSearch("chat");
    await act(async () => fyiToggle().click());
    await act(async () => chainToggle().click());
    await act(async () => originalToggle().click());
    expect(originalOpen()).toBe(true);
    await act(async () => {
      listScroll().scrollTop = 90;
      listScroll().dispatchEvent(new Event("scroll"));
    });
    expect(replace).not.toHaveBeenCalled();
  });
});
