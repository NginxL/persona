// An extension page's last view (lib/extension-view-cache.ts, docs/web/web.md "Extension pages on reload"): kept under the
// feed cache's hub and sign-in scope, only ever given back for the same page, UI version and manager, size-capped and
// least recently drawn out, and gone with a sign-out, new credentials or another hub.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UIView } from "./extension-ui";
import type { KeptKey } from "./extension-view-cache";

let cache: typeof import("./extension-view-cache");
let feed: typeof import("./feed-cache");
let stored: Map<string, string>;
const token = "jd_example_browser_credential";
const page = { extension: "example-today", ui: "today", state: "", language: "en" };
const key = (patch: Partial<KeptKey> = {}): KeptKey => ({ ...page, version: 8, manager: "m-1", ...patch });
const view = (text = "Weekly sync", patch: Partial<UIView> = {}): UIView => ({ title: "Today", sections: [{ title: "Calendar", items: [{ type: "text", text }] }], ...patch });
const kept = () => [...stored.keys()].filter((k) => k.includes(":ext-view"));

async function load() {
  feed = await import("./feed-cache");
  cache = await import("./extension-view-cache");
}

beforeEach(async () => {
  vi.resetModules();
  stored = new Map([["persona.token", token], ["persona.tokenId", "device-demo"]]);
  vi.stubGlobal("localStorage", {
    get length() { return stored.size; },
    key: (index: number) => [...stored.keys()][index] ?? null,
    getItem: (k: string) => stored.get(k) ?? null,
    setItem: (k: string, value: string) => stored.set(k, value),
    removeItem: (k: string) => stored.delete(k),
  });
  vi.stubGlobal("location", { origin: "https://hub.example.com" });
  await load();
});
afterEach(() => vi.unstubAllGlobals());

describe("an extension page's last view", () => {
  it("is given back after a reload for the same page, UI version and manager", async () => {
    expect(cache.keepView(key(), view())).toBe(true);
    vi.resetModules();
    await load();
    expect(cache.readKeptView(page, [7, 8], "m-1")).toEqual({ key: key(), view: view() });
    // another page, state or language has none
    expect(cache.readKeptView({ ...page, ui: "calendar" }, [7, 8], "m-1")).toBeNull();
    expect(cache.readKeptView({ ...page, state: "calendar" }, [7, 8], "m-1")).toBeNull();
    expect(cache.readKeptView({ ...page, language: "ja" }, [7, 8], "m-1")).toBeNull();
    // the keys say nothing of the credential
    expect(JSON.stringify([...stored.keys()])).not.toContain(token);
  });

  it("preserves View-level tabs, filter, search, and split layout across cache round-trip", async () => {
    const rich: UIView = {
      title: "Fleet Board",
      layout: "split",
      tabs: {
        name: "tab",
        value: "active",
        items: [
          { value: "active", title: "Active", badge: "3" },
          { value: "archived", title: "Archived" },
        ],
        action: { id: "switch-tab" },
      },
      search: { placeholder: "Filter nodes", value: "east", action: { id: "search" } },
      filter: {
        name: "env",
        title: "Environment",
        value: "prod",
        choices: [
          { value: "all", title: "All" },
          { value: "prod", title: "Production" },
        ],
        action: { id: "filter-env" },
      },
      sections: [
        { pane: "side", title: "Clusters", items: [{ type: "row", title: "us-east" }] },
        { title: "Nodes", items: [{ type: "text", text: "3 healthy" }] },
      ],
    };
    expect(cache.keepView(key(), rich)).toBe(true);
    vi.resetModules();
    await load();
    expect(cache.readKeptView(page, [8], "m-1")?.view).toEqual(rich);
  });

  it("is never shown for another manager, nor for a UI version the page is not drawn with now", () => {
    cache.keepView(key({ manager: "m-1" }), view("of m-1"));
    expect(cache.readKeptView(page, [7, 8], "m-2")).toBeNull();
    // before the roster names the default: the last one drawn
    expect(cache.readKeptView(page, [7, 8])?.view.sections[0].items[0]).toMatchObject({ text: "of m-1" });

    cache.keepView(key({ version: 7, manager: "m-2" }), view("native of m-2"));
    // a hub drawing over HTTP only: the socket's drawing is not its
    expect(cache.readKeptView(page, [8], "m-2")).toBeNull();
    expect(cache.readKeptView(page, [7, 8], "m-2")?.key.version).toBe(7);
    // an older or newer web's drawing never
    cache.keepView(key({ version: 6, manager: "m-3" }), view("old"));
    cache.keepView(key({ version: 9, manager: "m-3" }), view("new"));
    expect(cache.readKeptView(page, [7, 8], "m-3")).toBeNull();
  });

  it("prefers the most recently drawn of the versions the page may be drawn with", () => {
    cache.keepView(key({ version: 8 }), view("over HTTP"));
    cache.keepView(key({ version: 7 }), view("over the socket"));
    expect(cache.readKeptView(page, [7, 8], "m-1")?.key.version).toBe(7);
    cache.keepView(key({ version: 8 }), view("over HTTP again"));
    expect(cache.readKeptView(page, [7, 8], "m-1")?.key.version).toBe(8);
  });

  it("keeps at most VIEW_MAX_ENTRIES views, the least recently drawn going first", () => {
    for (let i = 0; i < cache.VIEW_MAX_ENTRIES; i++) cache.keepView(key({ ui: `page-${i}` }), view(`page ${i}`));
    // page-0 is drawn again: it is the most recent now, and page-1 the oldest
    cache.keepView(key({ ui: "page-0" }), view("page 0 again"));
    cache.keepView(key({ ui: "one-more" }), view("one more"));
    expect(kept().filter((k) => !k.endsWith(":ext-views"))).toHaveLength(cache.VIEW_MAX_ENTRIES);
    expect(cache.readKeptView({ ...page, ui: "page-1" }, [8], "m-1")).toBeNull();
    expect(cache.readKeptView({ ...page, ui: "page-0" }, [8], "m-1")?.view.sections[0].items[0]).toMatchObject({ text: "page 0 again" });
    expect(cache.readKeptView({ ...page, ui: "page-2" }, [8], "m-1")).not.toBeNull();
    expect(cache.readKeptView({ ...page, ui: "one-more" }, [8], "m-1")).not.toBeNull();
  });

  it("does not keep a view over VIEW_MAX_BYTES, and drops the older copy it would replace", () => {
    cache.keepView(key(), view("small"));
    expect(cache.keepView(key(), view("x".repeat(cache.VIEW_MAX_BYTES)))).toBe(false);
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();
    expect(kept()).toEqual([expect.stringMatching(/:ext-views$/)]);
    // UTF-8 bytes, not characters
    expect(cache.keepView(key(), view("界".repeat(Math.ceil(cache.VIEW_MAX_BYTES / 3))))).toBe(false);
  });

  it("never keeps a secret field's value or the sign-in token", () => {
    const form: UIView = { sections: [{ items: [{ type: "field", kind: "secret", name: "key", value: "sk-example" }, { type: "field", kind: "text", name: "user", value: "robin" }] }] };
    cache.keepView(key(), form);
    expect(JSON.stringify([...stored.values()])).not.toContain("sk-example");
    expect(JSON.stringify([...stored.values()])).toContain("robin");
    expect(cache.keepView(key(), view(`https://hub.example.com/img?token=${token}`))).toBe(false);
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();
  });

  it("is gone after signing out, with new credentials, and on another hub", async () => {
    cache.keepView(key(), view());
    feed.clearFeedCache();
    expect(kept()).toHaveLength(0);
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();

    cache.keepView(key(), view());
    stored.set("persona.token", "jd_other_account");
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();
    expect(kept()).toHaveLength(0);

    cache.keepView(key(), view("of hub one"));
    vi.stubGlobal("location", { origin: "https://other.example.com" });
    vi.resetModules();
    await load();
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();
    expect(cache.keepView(key(), view("of hub two"))).toBe(true);
    vi.stubGlobal("location", { origin: "https://hub.example.com" });
    vi.resetModules();
    await load();
    expect(cache.readKeptView(page, [8], "m-1")?.view.sections[0].items[0]).toMatchObject({ text: "of hub one" });
  });

  it("does not answer for a writer from before a sign-out", () => {
    stored.delete("persona.token");
    expect(cache.keepView(key(), view())).toBe(false);
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();
    expect(kept()).toHaveLength(0);
  });

  it("drops an unreadable entry and degrades quietly without storage", () => {
    cache.keepView(key(), view());
    const entry = kept().find((k) => !k.endsWith(":ext-views"))!;
    stored.set(entry, "{not json");
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();
    expect(kept()).toEqual([expect.stringMatching(/:ext-views$/)]);

    vi.stubGlobal("localStorage", {
      get length() { return 0; },
      key: () => null,
      getItem: (k: string) => (k === "persona.token" ? token : null),
      setItem: () => { throw new Error("QuotaExceededError"); },
      removeItem: () => {},
    });
    expect(cache.keepView(key(), view())).toBe(false);
    expect(cache.readKeptView(page, [8], "m-1")).toBeNull();
  });
});
