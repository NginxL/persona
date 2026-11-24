import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FeedCache } from "./feed-cache";
import type { Item } from "./types";

let cache: typeof import("./feed-cache");
let stored: Map<string, string>;
const token = "jd_example_browser_credential";
const item = (seq: number, role: Item["role"] = "assistant", patch: Partial<Item> = {}): Item => ({ id: `item-${seq}`, seq, role, kind: "text", text: `Message ${seq}`, ...patch });
const page = (patch: Partial<FeedCache> = {}): FeedCache => ({ machineID: "machine-demo", sessionID: "main-demo", items: [item(1, "user"), item(2)], hasMore: false, ...patch });
const entries = () => [...stored].filter(([key]) => key.startsWith("persona.feedCache."));

beforeEach(async () => {
  vi.resetModules();
  stored = new Map([["persona.token", token], ["persona.tokenId", "device-demo"]]);
  vi.stubGlobal("localStorage", {
    get length() { return stored.size; },
    key: (index: number) => [...stored.keys()][index] ?? null,
    getItem: (key: string) => stored.get(key) ?? null,
    setItem: (key: string, value: string) => stored.set(key, value),
    removeItem: (key: string) => stored.delete(key),
  });
  vi.stubGlobal("location", { origin: "https://hub.example.com" });
  cache = await import("./feed-cache");
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("main conversation feed cache", () => {
  it("partitions keys by hub, reverse-proxy prefix, device id and credential without exposing them", () => {
    const key = cache.feedCacheKey("https://hub.example.com", token, "device-demo")!;
    expect(new Set([
      key,
      cache.feedCacheKey("https://other.example.com", token, "device-demo"),
      cache.feedCacheKey("https://hub.example.com/other", token, "device-demo"),
      cache.feedCacheKey("https://hub.example.com", token, "other-device"),
      cache.feedCacheKey("https://hub.example.com", "jd_other_account", "device-demo"),
    ]).size).toBe(5);
    expect(key).not.toContain(token);
    expect(key).not.toContain("device-demo");
    expect(cache.feedCacheKey("https://hub.example.com", "")).toBeNull();
  });

  it("keeps fixed-width, stable keys distinct for short, long and Unicode credentials", () => {
    const secrets = [token, "x".repeat(65), "x".repeat(129), "fixture-\u{1f30d}-\u00e9", "fixture-\u{1f30e}-\u00e9"];
    const keys = secrets.map((secret) => cache.feedCacheKey("https://hub.example.com", secret)!);
    expect(new Set(keys).size).toBe(secrets.length);
    for (const [i, key] of keys.entries()) {
      expect(key).toMatch(/^persona\.feedCache\.v1:[0-9a-f]{16}:[0-9a-f]{16}$/);
      expect(key).toBe(cache.feedCacheKey("https://hub.example.com", secrets[i]));
      expect(key).not.toContain(secrets[i]);
    }
  });

  it("reads synchronously across a page reload, including manager and history", async () => {
    const value = page({ manager: "manager-demo", history: "history-demo", hasMore: true });
    expect(cache.writeFeedCache(cache.feedCacheScope(), value)).toBe(true);
    vi.resetModules();
    cache = await import("./feed-cache");
    expect(cache.readFeedCache()).toEqual(value);
    expect(JSON.stringify(entries())).not.toContain(token);
  });

  it("works without a device id on older hubs", () => {
    stored.delete("persona.tokenId");
    expect(cache.writeFeedCache(cache.feedCacheScope(), page())).toBe(true);
    expect(cache.readFeedCache()?.sessionID).toBe("main-demo");
    expect(JSON.stringify(entries())).not.toContain(token);
  });

  it("clears the old account and refuses its late writes even when the saved device id is stale", () => {
    const oldScope = cache.feedCacheScope();
    cache.writeFeedCache(oldScope, page());
    stored.set("persona.token", "jd_other_account");
    expect(cache.readFeedCache()).toBeNull();
    expect(entries()).toHaveLength(0);
    expect(cache.writeFeedCache(oldScope, page())).toBe(false);
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ sessionID: "new-main" }))).toBe(true);
    expect(cache.readFeedCache(oldScope)).toBeNull();
    expect(cache.readFeedCache()?.sessionID).toBe("new-main");
  });

  it("cannot read or write while signed out and does not restore cleared data on the same login", () => {
    const before = cache.feedCacheScope();
    cache.writeFeedCache(before, page());
    cache.clearFeedCache();
    stored.delete("persona.token");
    expect(cache.feedCacheScope()).toBeNull();
    expect(cache.readFeedCache()).toBeNull();
    expect(cache.writeFeedCache(before, page())).toBe(false);
    stored.set("persona.token", token);
    expect(cache.readFeedCache()).toBeNull();
    expect(cache.writeFeedCache(before, page())).toBe(false);
    expect(cache.writeFeedCache(cache.feedCacheScope(), page())).toBe(true);
  });

  it("invalidates an old writer when logout and the same login happen between two scope reads", () => {
    const before = cache.feedCacheScope();
    cache.clearFeedCache();
    expect(cache.writeFeedCache(before, page())).toBe(false);
    expect(cache.feedCacheScope()).not.toBe(before);
  });

  it("clears only feed keys, including cached feeds from another hub prefix", () => {
    cache.writeFeedCache(cache.feedCacheScope(), page());
    stored.set(cache.feedCacheKey("https://hub.example.com/other", token)!, "{}");
    stored.set("persona.language", "en");
    cache.clearFeedCache();
    expect(entries()).toHaveLength(0);
    expect(stored.get("persona.token")).toBe(token);
    expect(stored.get("persona.language")).toBe("en");
  });

  it("does not let supervision or compaction evict the last two user questions", () => {
    const items: Item[] = [];
    for (let question = 1; question <= 3; question++) {
      items.push(item(question * 10, "user", { text: `Question ${question}`, client_msg_id: `client-${question}` }));
      items.push(item(question * 10 + 1));
      if (question < 3) items.push(item(question * 10 + 2, "system", { kind: "notice", text: "Context compacted", compact: { trigger: "auto" } }));
      for (let n = 0; n < 3; n++) items.push(item(question * 10 + 3 + n, "user", {
        injected: { kind: "system", state: "supervise", line: "Review a synthetic task" }, text: "Automatic task supervision",
      }));
    }
    cache.writeFeedCache(cache.feedCacheScope(), page({ items }));
    const read = cache.readFeedCache()!;
    expect(read.items.filter((i) => i.client_msg_id).map((i) => i.text)).toEqual(["Question 2", "Question 3"]);
    expect(read.items.filter((i) => i.injected)).toHaveLength(6);
    expect(read.items.filter((i) => i.compact)).toHaveLength(1);
    expect(read.hasMore).toBe(true);
  });

  it("keeps only the newest two complete turns and their same-line image siblings", () => {
    const items = [item(1, "user"), item(2), item(3, "user", { id: "image-3", kind: "image", image: { mime: "image/png", lazy: true } }),
      item(3, "user"), item(4), item(5, "user"), item(6), item(7, "user", { queued: true })];
    cache.writeFeedCache(cache.feedCacheScope(), page({ items }));
    const read = cache.readFeedCache()!;
    expect(read.items.map((i) => i.id)).toEqual(items.slice(2).map((i) => i.id));
    expect(read.hasMore).toBe(true);
  });

  it.each(["tool_result", "notice"] as const)("retains an older launch and contiguous history referenced by a cached %s report", (kind) => {
    const launch = item(2, "assistant", { kind: "tool_use", tool_name: "Agent", tool_use_id: "example-launch" });
    const receipt = item(3, "user", { kind: "tool_result", tool_use_id: "example-launch", text: "Async agent launched successfully.\nagentId: example-agent" });
    const task = { kind: "agent", status: "completed", task_id: "example-agent", detail: "Synthetic report" };
    const report = item(22, kind === "notice" ? "system" : "user", { kind, wake: true, text: kind === "tool_result" ? "Synthetic report" : "Agent completed", task, ...(kind === "tool_result" ? { tool_use_id: "example-launch" } : {}) });
    const items = [item(1, "user"), launch, receipt, item(4), item(10, "user"), item(11), item(20, "user"), item(21), report];
    const scope = cache.feedCacheScope();
    cache.writeFeedCache(scope, page({ items }));
    expect(cache.readFeedCache()?.items.map((i) => i.id)).toEqual(items.map((i) => i.id));
    cache.writeMemoryPage(scope, page({ items }));
    expect(cache.readMemoryPage(scope, "machine-demo", "main-demo")?.items.map((i) => i.id)).toEqual(items.map((i) => i.id));
    const ordinary = items.slice(0, -1);
    cache.writeFeedCache(scope, page({ items: ordinary }));
    expect(cache.readFeedCache()?.items.map((i) => i.id)).toEqual(ordinary.slice(4).map((i) => i.id));
  });

  it("replaces the one cached main page instead of accumulating conversations", () => {
    const scope = cache.feedCacheScope();
    cache.writeFeedCache(scope, page());
    cache.writeFeedCache(scope, page({ sessionID: "other-main", items: [] }));
    expect(entries()).toHaveLength(1);
    expect(cache.readFeedCache()).toEqual(page({ sessionID: "other-main", items: [] }));
  });

  it("removes image bodies while preserving lazy image metadata and leaves the live items unchanged", () => {
    const items = [item(1, "user", { kind: "image", image: { mime: "image/png", data: "aGVsbG8=", width: 640, height: 480, size: 5 } }),
      item(2, "assistant", { kind: "tool_use", tool_input: { source: { type: "base64", media_type: "image/jpeg", data: "c2VjcmV0" } } }),
      item(3, "assistant", { text: "![image](data:image/png;base64,Ynl0ZXM=)" })];
    cache.writeFeedCache(cache.feedCacheScope(), page({ items }));
    const raw = JSON.stringify(entries());
    for (const body of ["aGVsbG8=", "c2VjcmV0", "Ynl0ZXM="]) expect(raw).not.toContain(body);
    expect(cache.readFeedCache()?.items[0].image).toEqual({ mime: "image/png", lazy: true, width: 640, height: 480, size: 5 });
    expect(items[0].image?.data).toBe("aGVsbG8=");
  });

  it("checks the size after removing image bytes", () => {
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ items: [item(1, "user", {
      kind: "image", image: { mime: "image/png", data: "a".repeat(cache.FEED_CACHE_MAX_BYTES * 2) },
    })] }))).toBe(true);
    expect(entries()[0][1].length).toBeLessThan(1000);
  });

  it("rejects a stored record containing image bytes rather than hydrating it", () => {
    cache.writeFeedCache(cache.feedCacheScope(), page());
    const key = entries()[0][0];
    stored.set(key, JSON.stringify(page({ items: [item(1, "user", { kind: "image", image: { mime: "image/png", data: "aGVsbG8=" } })] })));
    expect(cache.readFeedCache()).toBeNull();
    expect(entries()).toHaveLength(0);
  });

  it("rejects oversized UTF-8 pages and clears an older main rather than showing stale content", () => {
    const scope = cache.feedCacheScope();
    cache.writeFeedCache(scope, page());
    const text = "\u{1f30d}".repeat(cache.FEED_CACHE_MAX_BYTES / 3);
    expect(cache.writeFeedCache(scope, page({ items: [item(1, "user", { text })] }))).toBe(false);
    expect(cache.readFeedCache()).toBeNull();
    expect(entries()).toHaveLength(0);
  });

  it("rejects an enormous single turn instead of keeping a partial turn", () => {
    const items = Array.from({ length: cache.FEED_CACHE_MAX_ITEMS + 1 }, (_, i) => item(i + 1, i ? "assistant" : "user"));
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ items }))).toBe(false);
    expect(cache.readFeedCache()).toBeNull();
  });

  it("does not copy the authentication token from a message or URL into the cache", () => {
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ items: [item(1, "user", { text: `https://hub.example.com/image?token=${token}` })] }))).toBe(false);
    expect(entries()).toHaveLength(0);
  });

  it("ignores corrupt, oversized and incompatible stored data", () => {
    cache.writeFeedCache(cache.feedCacheScope(), page());
    const key = entries()[0][0];
    for (const raw of ["{", "null", "[]", JSON.stringify({ ...page(), items: [{ id: "a", seq: "bad" }] }),
      JSON.stringify({ ...page(), items: [item(1, "user", { text: {} as string })] }),
      JSON.stringify({ ...page(), items: [item(1, "user", { image: {} as Item["image"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { fold: {} as Item["fold"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { compact: { notes: {} } as Item["compact"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { injected: { line: {} } as Item["injected"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { fold: { items: 1, tools: 1, thoughts: 0, through_seq: 2, tasks: [{}] } as Item["fold"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { fold: { items: 1, tools: 1, thoughts: 0, through_seq: 2, changes: {} } as unknown as Item["fold"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { fold: { items: 1, tools: 1, thoughts: 0, through_seq: 2, changes: [{ path: 1, add: 1, del: 0, exact: true }] } as unknown as Item["fold"] })] }),
      JSON.stringify(page({ items: [item(1, "user", { text: "x".repeat(cache.FEED_CACHE_MAX_BYTES) })] }))]) {
      stored.set(key, raw);
      expect(cache.readFeedCache()).toBeNull();
    }
  });

  it("keeps a fold's changed files (#5157), and a fold without them stays without", () => {
    const changes = [{ path: "/home/user/project/a.ts", add: 3, del: 1, exact: true }];
    const folds = [changes, [], null, undefined].map((c, i) => item(i + 2, "assistant", { fold: { items: 4, tools: 2, thoughts: 0, through_seq: i + 2, ...(c === undefined ? {} : { changes: c }) } }));
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ items: [item(1, "user"), ...folds] }))).toBe(true);
    expect(cache.readFeedCache()!.items.slice(1).map((it) => it.fold?.changes)).toEqual([changes, [], null, undefined]);
  });

  it("degrades quietly when browser storage is unavailable or its quota is exhausted", () => {
    const scope = cache.feedCacheScope();
    vi.spyOn(localStorage, "setItem").mockImplementation(() => { throw new DOMException("Full", "QuotaExceededError"); });
    expect(cache.writeFeedCache(scope, page())).toBe(false);
    vi.spyOn(localStorage, "getItem").mockImplementation(() => { throw new DOMException("Disabled", "SecurityError"); });
    expect(cache.feedCacheScope()).toBeNull();
    expect(cache.readFeedCache()).toBeNull();
    expect(() => cache.clearFeedCache()).not.toThrow();
  });

  it("short-circuits oversized text turns without JSON.stringify or repeated removeItem calls", () => {
    const scope = cache.feedCacheScope();
    expect(cache.writeFeedCache(scope, page())).toBe(true);
    const removeSpy = vi.spyOn(localStorage, "removeItem");
    const stringifySpy = vi.spyOn(JSON, "stringify");
    const pageStringifies = () => stringifySpy.mock.calls.filter(([, replacer]) => typeof replacer === "function");
    const base = [item(1, "user", { text: "Question" }), item(2, "assistant", { text: "a".repeat(cache.FEED_CACHE_MAX_BYTES + 1024) })];
    expect(cache.writeFeedCache(scope, page({ items: base }))).toBe(false);
    expect(entries()).toHaveLength(0);
    expect(removeSpy).toHaveBeenCalledTimes(1);
    expect(pageStringifies()).toHaveLength(0);

    // Subsequent pushes onto the same oversized window skip both serialization and redundant removeItem.
    for (let seq = 3; seq <= 7; seq++) {
      expect(cache.writeFeedCache(scope, page({ items: [...base, item(seq, "assistant", { text: `follow-up ${seq}` })] }))).toBe(false);
    }
    expect(removeSpy).toHaveBeenCalledTimes(1);
    expect(pageStringifies()).toHaveLength(0);
  });

  it("memoizes serialized-oversized windows across appends and resumes caching once a newer or folded window fits", () => {
    const scope = cache.feedCacheScope();
    expect(cache.writeFeedCache(scope, page())).toBe(true);
    // Keep item.text short so the plain-text floor does not trigger; push over 256 KB via tool_input payload.
    const heavy = [
      item(1, "user", { text: "Analyze payload" }),
      item(2, "assistant", { kind: "tool_use", tool_name: "Read", tool_input: { blob: "b".repeat(cache.FEED_CACHE_MAX_BYTES + 512) } }),
    ];
    const removeSpy = vi.spyOn(localStorage, "removeItem");
    const stringifySpy = vi.spyOn(JSON, "stringify");
    const pageStringifies = () => stringifySpy.mock.calls.filter(([, replacer]) => typeof replacer === "function");
    expect(cache.writeFeedCache(scope, page({ items: heavy }))).toBe(false);
    expect(removeSpy).toHaveBeenCalledTimes(1);
    expect(pageStringifies()).toHaveLength(1);

    removeSpy.mockClear();
    stringifySpy.mockClear();
    // Appending items to the same 2-turn window reuses the oversized memo: no JSON.stringify, no removeItem.
    const extended = [...heavy, item(3, "assistant", { text: "step 3" }), item(4, "assistant", { text: "step 4" })];
    expect(cache.writeFeedCache(scope, page({ items: extended }))).toBe(false);
    expect(cache.writeFeedCache(scope, page({ items: [...extended, item(5, "assistant", { text: "step 5" })] }))).toBe(false);
    expect(pageStringifies()).toHaveLength(0);
    expect(removeSpy).not.toHaveBeenCalled();

    // Two new small user turns shift the 2-turn window past the oversized turn, so caching resumes.
    const recovered = [...extended, item(6, "user", { text: "Short Q1" }), item(7, "assistant", { text: "Short A1" }), item(8, "user", { text: "Short Q2" }), item(9, "assistant", { text: "Short A2" })];
    expect(cache.writeFeedCache(scope, page({ items: recovered }))).toBe(true);
    expect(cache.readFeedCache(scope)?.items.map((row) => row.seq)).toEqual([6, 7, 8, 9]);

    // Growing oversized again clears the newly cached entry once.
    expect(cache.writeFeedCache(scope, page({ items: heavy }))).toBe(false);
    expect(removeSpy).toHaveBeenCalledTimes(1);
    expect(cache.readFeedCache(scope)).toBeNull();
  });

  it("still strips oversized inline data-URI images and private refusal bodies before enforcing the byte limit", () => {
    const scope = cache.feedCacheScope();
    const withDataUri = item(1, "assistant", { text: `Preview ![img](data:image/png;base64,${"A".repeat(cache.FEED_CACHE_MAX_BYTES + 2048)})` });
    expect(cache.writeFeedCache(scope, page({ items: [withDataUri] }))).toBe(true);
    expect(cache.readFeedCache(scope)?.items[0].text).toBe("Preview ![img]([image omitted])");

    const withPrivateRefusal = item(2, "system", {
      kind: "notice",
      text: "x".repeat(cache.FEED_CACHE_MAX_BYTES + 2048),
      model_refusal: { subtype: "refusal", explanation: "Short public note" },
    });
    expect(cache.writeFeedCache(scope, page({ items: [withPrivateRefusal] }))).toBe(true);
    expect(cache.readFeedCache(scope)?.items[0].text).toBe("Short public note");
  });

  it("still blocks credentials after an oversized rejection and rejects oversized item counts without stringifying", () => {
    const scope = cache.feedCacheScope();
    const stringifySpy = vi.spyOn(JSON, "stringify");
    const pageStringifies = () => stringifySpy.mock.calls.filter(([, replacer]) => typeof replacer === "function");
    const many = Array.from({ length: cache.FEED_CACHE_MAX_ITEMS + 1 }, (_, i) => item(i + 1, i ? "assistant" : "user"));
    expect(cache.writeFeedCache(scope, page({ items: many }))).toBe(false);
    expect(pageStringifies()).toHaveLength(0);

    expect(cache.writeFeedCache(scope, page({ items: [item(1, "user", { text: `token=${token}` })] }))).toBe(false);
    expect(entries()).toHaveLength(0);
  });

  it("evicts oversized, corrupt, and incompatible records on readFeedCache without leaving unreadable entries behind", () => {
    const scope = cache.feedCacheScope();
    expect(cache.writeFeedCache(scope, page())).toBe(true);
    const key = entries()[0][0];

    for (const raw of ["", "{", JSON.stringify({ ...page(), items: [{ id: "a", seq: "bad" }] }),
      JSON.stringify(page({ items: [item(1, "user", { text: "x".repeat(cache.FEED_CACHE_MAX_BYTES + 128) })] }))]) {
      stored.set(key, raw);
      expect(cache.readFeedCache(scope)).toBeNull();
      expect(entries()).toHaveLength(0);
    }
  });

  it("still evicts stale or oversized entries re-introduced externally even when writeFeedCache hits the oversized memo", () => {
    const scope = cache.feedCacheScope();
    expect(cache.writeFeedCache(scope, page())).toBe(true);
    const key = entries()[0][0];
    const staleRaw = stored.get(key)!;

    const heavy = [
      item(1, "user", { text: "Analyze payload" }),
      item(2, "assistant", { kind: "tool_use", tool_name: "Read", tool_input: { blob: "b".repeat(cache.FEED_CACHE_MAX_BYTES + 512) } }),
    ];
    expect(cache.writeFeedCache(scope, page({ items: heavy }))).toBe(false);
    expect(entries()).toHaveLength(0);

    const removeSpy = vi.spyOn(localStorage, "removeItem");
    const stringifySpy = vi.spyOn(JSON, "stringify");
    const pageStringifies = () => stringifySpy.mock.calls.filter(([, replacer]) => typeof replacer === "function");

    // Another tab or external writer restores a stale valid entry after the oversized memo was recorded.
    stored.set(key, staleRaw);
    expect(cache.readFeedCache(scope)?.sessionID).toBe("main-demo");
    removeSpy.mockClear();
    stringifySpy.mockClear();
    const extended = [...heavy, item(3, "assistant", { text: "step 3" })];
    expect(cache.writeFeedCache(scope, page({ items: extended }))).toBe(false);
    expect(entries()).toHaveLength(0);
    expect(cache.readFeedCache(scope)).toBeNull();
    expect(removeSpy).toHaveBeenCalledTimes(1);
    expect(pageStringifies()).toHaveLength(0);

    // An external oversized/corrupt record is likewise evicted on the memoized shortcut, and once absent removeItem is skipped.
    removeSpy.mockClear();
    stored.set(key, JSON.stringify(page({ items: [item(1, "user", { text: "z".repeat(cache.FEED_CACHE_MAX_BYTES + 128) })] })));
    const further = [...extended, item(4, "assistant", { text: "step 4" })];
    expect(cache.writeFeedCache(scope, page({ items: further }))).toBe(false);
    expect(entries()).toHaveLength(0);
    expect(removeSpy).toHaveBeenCalledTimes(1);
    expect(pageStringifies()).toHaveLength(0);

    removeSpy.mockClear();
    expect(cache.writeFeedCache(scope, page({ items: [...further, item(5, "assistant", { text: "step 5" })] }))).toBe(false);
    expect(removeSpy).not.toHaveBeenCalled();
    expect(pageStringifies()).toHaveLength(0);
  });
});

describe("memory pages of other chats", () => {
  const scope = () => cache.feedCacheScope()!;
  const write = (sid: string) => cache.writeMemoryPage(scope(), page({ sessionID: sid }));
  const has = (sid: string) => cache.readMemoryPage(scope(), "machine-demo", sid) !== null;

  it("keeps the most recently used chats and never touches storage", () => {
    for (let n = 0; n < cache.MEMORY_PAGES; n++) write(`s${n}`);
    expect(has("s0")).toBe(true); // reading it makes s1 the oldest
    write("newest");
    expect([has("s0"), has("s1"), has("newest")]).toEqual([true, false, true]);
    expect(entries()).toEqual([]);
  });

  it("never answers for another hub, another login, or after sign-out", () => {
    write("agent");
    expect(cache.readMemoryPage(scope(), "another-machine", "agent")).toBeNull();
    const old = scope();
    stored.set("persona.token", "jd_another_browser_credential");
    expect(cache.readMemoryPage(scope(), "machine-demo", "agent")).toBeNull();
    expect(cache.readMemoryPage(old, "machine-demo", "agent")).toBeNull();
    write("agent");
    cache.clearFeedCache();
    expect(has("agent")).toBe(false);
  });

  it("keeps only the newest turns, and no page at all for an empty chat", () => {
    cache.writeMemoryPage(scope(), page({ sessionID: "long", items: [item(1, "user"), item(2, "user"), item(3, "user")] }));
    const kept = cache.readMemoryPage(scope(), "machine-demo", "long")!;
    expect(kept.items.map((row) => row.seq)).toEqual([2, 3]);
    expect(kept.hasMore).toBe(true);
    cache.writeMemoryPage(scope(), page({ sessionID: "long", items: [] }));
    expect(has("long")).toBe(false);
  });
});
