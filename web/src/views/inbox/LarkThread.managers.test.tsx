// @vitest-environment jsdom
// Several managers (docs/assistant/managers.md "Clients"): an inbox item's chat is its manager's Lark. Every request its view
// makes names that manager where it is made (no page-wide pin), so one still going out after the next item opened (the
// read report flushed as the chat closes) keeps its own; a chat of the default's names none.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LarkMessage } from "../../lib/inbox";
import { chosenManager, ItemManager, managerHeaderFor, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { LarkThread } from "./LarkThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const msg = (id: string): LarkMessage => ({
  id, position: id.replace(/\D/g, ""), sender_name: "示例同事", created_at: "2026-11-01T09:00:00Z", text: `消息 ${id}`,
});

let host: HTMLDivElement;
let root: Root;
const calls: { url: string; header: string | null; method: string }[] = [];
const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};

/** Every watched message is half in view at once: the list reports it seen. */
class SeenObserver {
  constructor(private cb: IntersectionObserverCallback) {}
  observe(el: Element) {
    const entry = { target: el, isIntersecting: true, intersectionRatio: 1, rootBounds: null, intersectionRect: { height: 0 } };
    this.cb([entry as unknown as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}

beforeEach(() => {
  calls.length = 0;
  resetManagerChoice();
  setRosterDefault("assistant");
  vi.stubGlobal("IntersectionObserver", SeenObserver);
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("CSS", { escape: (v: string) => v }); // jsdom has none
  Element.prototype.scrollIntoView = vi.fn();
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, header: new Headers(init?.headers).get("X-Persona-Manager"), method: init?.method ?? "GET" });
    let body: unknown = { error: "not found" };
    let status = 404;
    if (url.includes("/conversations/faces/messages")) {
      const account = new URL(url, "http://hub").searchParams.get("ol_account");
      body = { messages: [{ ...msg("m1"), sender_id: "person", sender_avatar: "/agent/v1/media/old-person", sender_avatar_url: `/api/v1/avatars/lark/c2VuZGVy?lark_account=${account}`,
        parts: [{ kind: "image", url: "/api/v1/lark/media/attachment" }] }] };
      status = 200;
    } else if (url.includes("/lark/users/person")) {
      const account = new URL(url, "http://hub").searchParams.get("ol_account");
      body = { displayName: "Example person", avatarUrl: "/agent/v1/media/old-profile", avatar_url: `/api/v1/avatars/lark/cHJvZmlsZQ?lark_account=${account}` };
      status = 200;
    } else if (/\/conversations\/f3\/messages\?/.test(url)) {
      // two pictures: the viewer pages through both
      const pic = (id: string, key: string): LarkMessage => ({ ...msg(id), text: "", parts: [{ kind: "image", url: `/api/v1/lark/media/${key}` }] });
      body = { messages: [pic("p1", "k1"), pic("p2", "k2")], read_cursor: "0" };
      status = 200;
    } else if (/\/conversations\/f[12]\/messages\?/.test(url)) {
      body = { messages: [msg("m1"), msg("m2")], read_cursor: "0" };
      status = 200;
    } else if (url.endsWith("/assistant/conversations")) {
      body = { session: { machine_id: "mac", session_id: "s-sum" } };
      status = 200;
    } else if (url.includes("/read-states")) {
      body = { states: [] };
      status = 200;
    } else if (url.includes("/read")) {
      body = {};
      status = 200;
    }
    return { ok: status < 400, status, text: async () => JSON.stringify(body), headers: new Headers() };
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

const lark = (feed: string) => calls.filter((c) => c.url.includes(`/lark/conversations/${feed}/`));
const named = (url: string) => new URL(url, "http://hub").searchParams.get("manager");

describe("an inbox item's chat of another manager", () => {
  it("uses the item's own manager and account for the sender and profile, including same-named people on another manager", async () => {
    const expectFace = (value: string, size: number, manager: string, account: number) => {
      const img = document.querySelector<HTMLImageElement>(`img[src*="/avatars/lark/${value}"]`);
      expect(img).not.toBeNull();
      const url = new URL(img!.src);
      expect([url.searchParams.get("lark_account"), url.searchParams.get("size"), url.searchParams.get("manager")]).toEqual([String(account), String(size), manager]);
      return img!;
    };
    for (const [manager, account] of [["first-copy", 7], ["second-copy", 7], ["second-copy", 8]] as const) {
      act(() => root.render(<LarkThread chat={{ feed: "faces", title: "Chat", account, manager }} />));
      await flush();
      expect(document.querySelector("[role=dialog]")).toBeNull();
      const sender = expectFace("c2VuZGVy", 32, manager, account);
      expect(host.querySelector("img[src*='/lark/media/attachment']")).not.toBeNull();
      act(() => sender.closest("button")!.click());
      await flush();
      expectFace("cHJvZmlsZQ", 56, manager, account);
    }
  });

  it("names the item's manager on its reads, and on the read report it flushes as the next item opens", async () => {
    // item B (m-2's chat f1, opened at its first unread) open and read, then item C (the default's chat f2) in its
    // place, as InboxDetail does
    act(() => root.render(<ItemManager.Provider value="m-2"><div key="b"><LarkThread chat={{ feed: "f1", title: "B", focus: "1" }} /></div></ItemManager.Provider>));
    await flush();
    act(() => void document.dispatchEvent(new Event("visibilitychange"))); // in front: what is in view counts as seen
    expect(lark("f1").length).toBeGreaterThan(0);
    act(() => root.render(<ItemManager.Provider value={undefined}><div key="c"><LarkThread chat={{ feed: "f2", title: "C" }} /></div></ItemManager.Provider>));
    await flush();

    // the report flushed as B's chat closed, after C's view took its place
    await vi.waitFor(() => expect(lark("f1").some((c) => c.method === "POST" && /\/f1\/read(\?|$)/.test(c.url))).toBe(true));
    const b = lark("f1");
    expect(b.map((c) => named(c.url) ?? c.header)).toEqual(b.map(() => "m-2"));
    const c = lark("f2");
    expect(c.length).toBeGreaterThan(0);
    expect(c.map((x) => named(x.url) ?? x.header)).toEqual(c.map(() => null));
  });

  it("asks the chat's manager's Persona when the chat itself names none (a direct chat opened from a profile in it)", async () => {
    act(() => root.render(<ItemManager.Provider value="m-1"><LarkThread chat={{ feed: "f1", title: "Direct" }} /></ItemManager.Provider>));
    await flush();
    const ask = [...host.querySelectorAll("button")].find((b) => b.title === "让 Persona 总结")!;
    await act(async () => ask.click());
    await vi.waitFor(() => expect(calls.some((c) => c.url.endsWith("/assistant/conversations"))).toBe(true));
    expect(calls.find((c) => c.url.endsWith("/assistant/conversations"))!.header).toBe("m-1");
    // the summary opens as itself: the current assistant stays
    expect(chosenManager()).toBeNull();
    // the conversation's own requests go there too
    expect(managerHeaderFor("/assistant/conversations/s-sum/feed")).toBe("m-1");
  });

  it("pages through another manager's chat's pictures from the one opened", async () => {
    act(() => root.render(<ItemManager.Provider value="m-1"><LarkThread chat={{ feed: "f3", title: "Pictures" }} /></ItemManager.Provider>));
    await flush();
    const pictures = [...host.querySelectorAll<HTMLButtonElement>('button[aria-label="查看图片"]')];
    expect(pictures).toHaveLength(2);
    act(() => pictures[1].click());
    // found in the gallery (its address names the manager as the gallery's do): 2 of 2, not a gallery of one
    expect(document.body.textContent).toContain("2 / 2");
  });

  it("a chat ref's own manager wins over the view's", async () => {
    act(() => root.render(<LarkThread chat={{ feed: "f1", title: "B", manager: "m-1" }} />));
    await flush();
    const b = lark("f1");
    expect(b.length).toBeGreaterThan(0);
    expect(b.every((c) => named(c.url) === "m-1")).toBe(true);
  });
});
