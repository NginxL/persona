// @vitest-environment jsdom
// Where the app opens (#9071, lib/start-page.ts): the main chat by default; the start page the user chose at an opening
// of the root address only; the hub's choice read at the hello and followed on its pushes; a choice kept here handed
// to a hub that has none; a hub without the route leaving the choice in this browser.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { OverviewEntry } from "./overview-cards";

// start-page listens to two event types. The mock below keeps that boundary without rebuilding store's entire module
// graph on every simulated reload; the rest of start-page, sidebar-pages and overview-cards stays real.
const hubListeners = vi.hoisted(() => new Map<string, Set<(msg: Record<string, unknown>) => void>>());
vi.mock("./store", () => ({
  onHubEvent: (type: string, listener: (msg: Record<string, unknown>) => void) => {
    if (!hubListeners.has(type)) hubListeners.set(type, new Set());
    hubListeners.get(type)!.add(listener);
    return () => void hubListeners.get(type)?.delete(listener);
  },
  useStore: (select: (state: { conn: string; features: string[] }) => unknown) => select({ conn: "online", features: [] }),
}));
// The modules each test loads afresh (load(), the Settings row's), transformed and evaluated while the file is
// collected, outside the test timeout (as highlight-lazy.test.tsx, #4955). Each test still resets and runs them again;
// only the initial cold module-graph work must not consume the five-second deadline.
import "./start-page";
import "../views/settings/StartPage";
vi.hoisted(() => {
  // the Settings row's module draws with the sidebar's pages, which read these
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) {
    Socket.last = this;
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  push(value: unknown) {
    const message = value as Record<string, unknown>;
    hubListeners.get(String(message.type))?.forEach((listener) => listener(message));
  }
}

const TODAY = "p:example-today/today";
// what the made-up hub keeps: undefined until something is stored; "missing" for a hub without the route
let hub: { start?: string; pages?: unknown; overview?: unknown } | "missing";
let puts: unknown[];

beforeEach(() => {
  vi.resetModules();
  hubListeners.clear();
  localStorage.clear();
  localStorage.setItem("persona.token", "test-token");
  history.replaceState(null, "", "/");
  hub = {};
  puts = [];
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = new URL(String(input), location.href);
      if (url.pathname !== "/api/v1/navigation") return Response.json({});
      if (hub === "missing") return Response.json({ error: "not found" }, { status: 404 });
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { start?: string; pages?: unknown };
        puts.push(body);
        hub = { ...hub, ...body };
      }
      return Response.json(hub);
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const flush = async () => {
  for (let i = 0; i < 6; i++) await Promise.resolve();
  await new Promise((r) => setTimeout(r, 0));
};

/** This page's modules as a fresh load has them, with `stored` as this browser's copy. */
async function load(stored?: string) {
  if (stored !== undefined) localStorage.setItem("persona.startPage", stored);
  const page = await import("./start-page");
  const hello = async (extra: Record<string, unknown> = {}) => {
    const message = { type: "hello", features: [], ...extra };
    new Socket("ws://test");
    hubListeners.get("hello")?.forEach((listener) => listener(message));
    await flush();
  };
  return { ...page, store: { disconnect: () => { Socket.last.close(); hubListeners.clear(); } }, hello };
}

// Warm the exact dynamic-import path too: static imports transform these modules, but the first full-suite call to
// load() can still spend seconds evaluating the graph, and the two-device test calls it three times. No socket is
// connected, no request is made and no state is asserted here; every test resets these modules before its own load.
beforeAll(async () => {
  await Promise.all([import("./start-page"), import("./store"), import("../views/settings/StartPage")]);
});

describe("startRoute", () => {
  it("knows the sidebar's pages and an extension's, and nothing else", async () => {
    const { startRoute } = await load();
    expect(startRoute("")).toBeNull();
    expect(startRoute("home")).toEqual({ name: "home" });
    expect(startRoute("inbox")).toEqual({ name: "inbox" });
    expect(startRoute("workspace-board")).toEqual({ name: "workspace-board" });
    expect(startRoute(TODAY)).toEqual({ name: "extension", extension: "example-today", ui: "today" });
    for (const k of ["settings", "persona", "unknown", "p:", "p:/today", "p:example-today/", "p:example-today"]) expect(startRoute(k)).toBeNull();
  });
});

describe("opening the app", () => {
  it("opens on the main chat when nothing was chosen", async () => {
    const { openStartPage } = await load();
    openStartPage();
    expect(location.pathname).toBe("/");
  });

  it("opens on the chosen page, an extension's Today", async () => {
    const { openStartPage } = await load(TODAY);
    openStartPage();
    expect(location.pathname).toBe("/extension/example-today/today");
  });

  it("opens on Overview when that is chosen", async () => {
    const { openStartPage } = await load("home");
    openStartPage();
    expect(location.pathname).toBe("/overview");
  });

  it("leaves a reload, Back / Forward, another page's address and a query where they are", async () => {
    const { openStartPage } = await load(TODAY);
    for (const type of ["reload", "back_forward"]) {
      vi.spyOn(performance, "getEntriesByType").mockReturnValue([{ type } as PerformanceNavigationTiming]);
      openStartPage();
      expect(location.pathname).toBe("/");
    }
    vi.restoreAllMocks();
    for (const at of ["/inbox", "/persona", "/?machine=x"]) {
      history.replaceState(null, "", at);
      openStartPage();
      expect(location.pathname + location.search).toBe(at);
    }
  });

  it("opens on the main chat for a key this version does not know", async () => {
    const { openStartPage } = await load("p:broken");
    openStartPage();
    expect(location.pathname).toBe("/");
  });
});

describe("startPageGone", () => {
  it("says gone only once it can be told", async () => {
    const { startPageGone } = await load();
    const none = { extensions: false, board: false, starred: false };
    const all = { extensions: true, board: true, starred: true };
    expect(startPageGone(TODAY, true, none)).toBe(false);
    expect(startPageGone(TODAY, false, none)).toBeUndefined();
    expect(startPageGone(TODAY, false, all)).toBe(true);
    expect(startPageGone("workspace-board", false, { ...all, board: false })).toBeUndefined();
    expect(startPageGone("workspace-board", false, all)).toBe(true);
    expect(startPageGone("messages", false, none)).toBe(true);
    expect(startPageGone("starred", false, none)).toBeUndefined();
    expect(startPageGone("starred", false, all)).toBe(true);
    expect(startPageGone("starred", true, all)).toBe(false);
  });
});

describe("the hub's choice", () => {
  it("is read at the hello, kept for the next opening, and followed on its pushes", async () => {
    hub = { start: TODAY };
    const { hello, startPage, startPageSynced } = await load();
    expect(startPage()).toBe("");
    await hello();
    expect(startPage()).toBe(TODAY);
    expect(startPageSynced()).toBe(true);
    expect(localStorage.getItem("persona.startPage")).toBe(TODAY);
    Socket.last.push({ type: "navigation", start: "" });
    expect(startPage()).toBe("");
    expect(localStorage.getItem("persona.startPage")).toBeNull();
    expect(puts).toEqual([]);
  });

  it("is not read again at a hello that resumed", async () => {
    const { hello, store } = await load();
    await hello();
    const reads = vi.mocked(fetch).mock.calls.filter(([u]) => String(u).includes("/navigation")).length;
    Socket.last.push({ type: "hello", features: [], resumed: true });
    await flush();
    expect(vi.mocked(fetch).mock.calls.filter(([u]) => String(u).includes("/navigation")).length).toBe(reads);
    store.disconnect();
  });

  it("an empty start is the main chat chosen: it wins over this browser's older copy", async () => {
    hub = { start: "" };
    const { hello, startPage } = await load(TODAY);
    await hello();
    expect(startPage()).toBe("");
    expect(puts).toEqual([]);
  });

  it("is handed the choice this browser kept while the hub had none", async () => {
    const { hello, startPage } = await load("kb");
    await hello();
    expect(puts).toEqual([{ start: "kb" }]);
    expect(startPage()).toBe("kb");
  });

  it("two devices: what one chooses, the other opens on from its next opening", async () => {
    // the laptop chooses Today
    const laptop = await load();
    await laptop.hello();
    const { saveStartPage } = await import("./start-page");
    await saveStartPage(TODAY);
    expect(puts).toEqual([{ start: TODAY }]);
    laptop.store.disconnect();
    // the phone: another browser, nothing kept yet; its first opening is the main chat, and it reads the hub's choice
    vi.resetModules();
    localStorage.clear();
    localStorage.setItem("persona.token", "test-token");
    const phone = await load();
    phone.openStartPage();
    expect(location.pathname).toBe("/");
    await phone.hello();
    expect(phone.startPage()).toBe(TODAY);
    phone.store.disconnect();
    // its next opening
    vi.resetModules();
    const next = await load();
    next.openStartPage();
    expect(location.pathname).toBe("/extension/example-today/today");
  });
});

describe("the sidebar's arrangement (groups, #9071)", () => {
  const arrangement = { order: ["routines"], shown: [], hidden: [], groups: [{ id: "u:life", title: "生活" }], assign: { routines: "u:life" } };

  it("two devices: what one arranges, the other draws from its hello and on the push", async () => {
    const laptop = await load();
    await laptop.hello();
    const pages = await import("./sidebar-pages");
    const life = pages.addPageGroup([""], "生活");
    pages.setPageGroup("routines", life);
    await flush();
    expect(hub).toMatchObject({ pages: { groups: [{ id: "" }, { id: life, title: "生活" }], assign: { routines: life } } });
    laptop.store.disconnect();
    vi.resetModules();
    localStorage.clear();
    localStorage.setItem("persona.token", "test-token");
    const phone = await load();
    await phone.hello();
    const phonePages = await import("./sidebar-pages");
    expect(phonePages.getSidebarPrefs().assign).toEqual({ routines: life });
    expect(JSON.parse(localStorage.getItem("sidebar.pages")!).groups[1]).toEqual({ id: life, title: "生活" });
    // another device renames it: the push brings it here
    Socket.last.push({ type: "navigation", pages: { ...arrangement, groups: [{ id: life, title: "Life" }], assign: { routines: life } } });
    expect(phonePages.getSidebarPrefs().groups).toEqual([{ id: life, title: "Life" }]);
  });

  it("is handed this browser's arrangement when the hub has none", async () => {
    hub = { start: "" };
    localStorage.setItem("sidebar.pages", JSON.stringify(arrangement));
    const { hello } = await load();
    await hello();
    expect(puts).toEqual([{ pages: arrangement }]);
  });

  it("keeps a change the hub did not take and sends it again at the next hello", async () => {
    hub = { pages: { order: [], shown: [], hidden: [] } };
    const { hello } = await load();
    await hello();
    const pages = await import("./sidebar-pages");
    vi.mocked(fetch).mockImplementationOnce(async () => Response.json({ error: "database down" }, { status: 503 }));
    pages.setPageShown("kb", false);
    await flush();
    expect(pages.getSidebarPrefs().hidden).toEqual(["kb"]);
    await hello();
    expect(pages.getSidebarPrefs().hidden).toEqual(["kb"]);
    expect(hub).toMatchObject({ pages: { hidden: ["kb"] } });
  });

  it("stays in this browser on a hub without the route", async () => {
    hub = "missing";
    const { hello } = await load();
    await hello();
    const pages = await import("./sidebar-pages");
    pages.setPageShown("kb", false);
    await flush();
    expect(JSON.parse(localStorage.getItem("sidebar.pages")!).hidden).toEqual(["kb"]);
    expect(vi.mocked(fetch).mock.calls.filter(([, i]) => (i as RequestInit | undefined)?.method === "PUT")).toEqual([]);
  });
});

describe("a hub without the route", () => {
  it("keeps the choice in this browser and opens on it", async () => {
    hub = "missing";
    const { hello, startPage, startPageSynced } = await load();
    await hello();
    expect(startPageSynced()).toBe(false);
    const { saveStartPage } = await import("./start-page");
    await saveStartPage("inbox");
    expect(startPage()).toBe("inbox");
    expect(localStorage.getItem("persona.startPage")).toBe("inbox");
    // no PUT after the hub said it has no route
    expect(vi.mocked(fetch).mock.calls.filter(([, i]) => (i as RequestInit | undefined)?.method === "PUT")).toEqual([]);
    vi.resetModules();
    const next = await load();
    next.openStartPage();
    expect(location.pathname).toBe("/inbox");
  });

  it("a refused save puts the last choice back", async () => {
    hub = { start: "inbox" };
    const { hello, startPage } = await load();
    await hello();
    vi.mocked(fetch).mockImplementationOnce(async () => Response.json({ error: "database down" }, { status: 503 }));
    const { saveStartPage } = await import("./start-page");
    await saveStartPage("kb");
    expect(startPage()).toBe("inbox");
  });
});

describe("Overview's cards (#9745)", () => {
  const arrangement: OverviewEntry[] = [{ extension: "example-quota", id: "left", span: "full" }, { extension: "example-gateway", id: "today", hidden: true }];
  const newer: OverviewEntry[] = [{ extension: "example-today", id: "needs", hidden: true }];
  const puts$ = () => vi.mocked(fetch).mock.calls.filter(([, i]) => (i as RequestInit | undefined)?.method === "PUT");
  // this browser's copy is kept per hub and sign-in (lib/feed-cache.ts scopedKey): its key under the current one
  const copyKey = async () => {
    const f = await import("./feed-cache");
    return f.scopedKey(f.feedCacheScope(), "overview.cards")!;
  };
  const keepHere = async (list: OverviewEntry[]) => localStorage.setItem(await copyKey(), JSON.stringify(list));
  const answer = (status: number, body: unknown) => async () => Response.json(body, { status });

  it("two devices: what one arranges, the other draws from its hello and on the push", async () => {
    const laptop = await load();
    await laptop.hello();
    const cards = await import("./overview-cards");
    cards.saveOverview(arrangement);
    await flush();
    expect(puts).toEqual([{ overview: arrangement }]);
    expect(hub).toMatchObject({ overview: arrangement });
    expect(cards.overviewKept()).toBe(true);
    laptop.store.disconnect();
    vi.resetModules();
    localStorage.clear();
    localStorage.setItem("persona.token", "test-token");
    const phone = await load();
    await phone.hello();
    const phoneCards = await import("./overview-cards");
    expect(phoneCards.getOverviewPrefs()).toEqual(arrangement);
    expect(JSON.parse(localStorage.getItem(await copyKey())!)).toEqual(arrangement);
    // another device puts the cards back as they were: the push brings it here
    Socket.last.push({ type: "navigation", overview: [] });
    expect(phoneCards.getOverviewPrefs()).toEqual([]);
    expect(localStorage.getItem(await copyKey())).toBeNull();
  });

  it("a push of another field leaves the cards alone", async () => {
    hub = { overview: arrangement };
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    Socket.last.push({ type: "navigation", start: "inbox" });
    expect(cards.getOverviewPrefs()).toEqual(arrangement);
  });

  it("is handed this browser's arrangement when the hub has none, on its own", async () => {
    hub = { start: "" };
    await keepHere(arrangement);
    const { hello } = await load();
    await hello();
    expect(puts).toEqual([{ overview: arrangement }]);
  });

  it("the hub's arrangement wins over this browser's older copy, an empty one too", async () => {
    hub = { overview: [] };
    await keepHere(arrangement);
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    expect(cards.getOverviewPrefs()).toEqual([]);
    expect(puts).toEqual([]);
  });

  // review of #9778, P1: the copy was one for every hub and account of the browser
  it("another account or hub in this browser neither draws this one's copy nor hands it to its hub", async () => {
    hub = { start: "" };
    await keepHere(arrangement);
    const { hello } = await load();
    const cards = await import("./overview-cards");
    expect(cards.getOverviewPrefs()).toEqual(arrangement);
    // signed in again as someone else (or on another hub behind this origin)
    localStorage.setItem("persona.token", "another-token");
    expect(cards.getOverviewPrefs()).toEqual([]);
    await hello();
    expect(puts).toEqual([]);
  });

  it("keeps a change the hub did not take (5xx, 403, the network) and sends it again at the next hello", async () => {
    hub = { overview: [] };
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    for (const fail of [answer(503, { error: "database down" }), answer(403, { error: "only a device with full access" }), async () => Promise.reject(new TypeError("Failed to fetch"))]) {
      vi.mocked(fetch).mockImplementationOnce(fail as never);
      cards.saveOverview(arrangement);
      await flush();
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      expect(cards.overviewKept()).not.toBe(false);
      await hello();
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      expect(hub).toMatchObject({ overview: arrangement });
      hub = { overview: [] };
      await hello();
    }
  });

  // review of #9778, P1: a push heard after the failure took the place of the change, and the next hello sent the push's
  it("a push heard after a change the hub did not take leaves the change, which goes again at the next hello", async () => {
    hub = { overview: [] };
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    vi.mocked(fetch).mockImplementationOnce(answer(503, { error: "database down" }) as never);
    cards.saveOverview(arrangement);
    await flush();
    Socket.last.push({ type: "navigation", overview: newer }); // an older change's push, late, or another device's
    expect(cards.getOverviewPrefs()).toEqual(arrangement);
    hub = { overview: newer };
    await hello();
    expect(cards.getOverviewPrefs()).toEqual(arrangement);
    expect(hub).toMatchObject({ overview: arrangement });
  });

  it("stays in this browser on a hub without the route", async () => {
    hub = "missing";
    const { hello, startPageSynced } = await load();
    await hello();
    const cards = await import("./overview-cards");
    cards.saveOverview(arrangement);
    await flush();
    expect(startPageSynced()).toBe(false);
    expect(JSON.parse(localStorage.getItem(await copyKey())!)).toEqual(arrangement);
    expect(puts$()).toEqual([]);
  });

  // a default manager of #9351: start and pages, no overview; a PUT of neither is its own 400
  const oldManager = (words = "start or pages is required") =>
    vi.mocked(fetch).mockImplementation(async (input: string | URL | Request, init?: RequestInit) => {
      const body = init?.method === "PUT" ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null;
      if (body && body.start === undefined && body.pages === undefined) return Response.json({ error: words }, { status: 400 });
      return Response.json(new URL(String(input), location.href).pathname === "/api/v1/navigation" ? hub : {});
    });

  it("stays in this browser with a default manager from before the field: its own 400 for a change of only it", async () => {
    hub = { start: "inbox" };
    oldManager();
    const { hello, startPageSynced } = await load();
    await hello();
    const cards = await import("./overview-cards");
    cards.saveOverview(arrangement);
    await flush();
    expect(cards.overviewKept()).toBe(false);
    expect(startPageSynced()).toBe(true); // the start page and the sidebar are still kept there
    expect(cards.getOverviewPrefs()).toEqual(arrangement);
    // no more tries for each change in this load
    cards.saveOverview([...arrangement].reverse());
    await flush();
    expect(puts$()).toHaveLength(1);
  });

  it("knows #9328's words too", async () => {
    hub = { start: "inbox" };
    oldManager("start is required");
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    cards.saveOverview(arrangement);
    await flush();
    expect(cards.overviewKept()).toBe(false);
  });

  // review of #9778, P1: one refusal kept the arrangement off the hub until the page was reloaded
  it("asks again at the next hello, and a manager that has the field by then takes the arrangement", async () => {
    hub = { start: "inbox" };
    oldManager();
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    cards.saveOverview(arrangement);
    await flush();
    expect(cards.overviewKept()).toBe(false);
    // the manager is updated meanwhile; the socket comes back with a hello that did not resume
    vi.mocked(fetch).mockImplementation(async (input: string | URL | Request, init?: RequestInit) => {
      if (new URL(String(input), location.href).pathname !== "/api/v1/navigation" || hub === "missing") return Response.json({});
      if (init?.method === "PUT") hub = { ...hub, ...(JSON.parse(String(init.body)) as object) };
      return Response.json(hub);
    });
    await hello();
    expect(hub).toMatchObject({ overview: arrangement });
    expect(cards.overviewKept()).toBe(true);
  });

  // review of #9778, P2: any 400 was taken for a manager from before the field
  it("another 400 is not a manager without the field: the change goes again at the next hello", async () => {
    hub = { overview: [] };
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    vi.mocked(fetch).mockImplementationOnce(answer(400, { error: "overview: a card's span is half or full" }) as never);
    cards.saveOverview(arrangement);
    await flush();
    expect(cards.overviewKept()).not.toBe(false);
    await hello();
    expect(hub).toMatchObject({ overview: arrangement });
  });

  it("stays in this browser when the manager answers a change without the field", async () => {
    hub = { start: "inbox" };
    vi.mocked(fetch).mockImplementation(async () => Response.json(hub)); // takes the PUT, never says overview
    await keepHere(arrangement);
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    expect(puts$()).toHaveLength(1); // the hand-over
    expect(cards.overviewKept()).toBe(false);
    expect(cards.getOverviewPrefs()).toEqual(arrangement);
  });

  describe("pushes heard while a change of it is on its way (review of #9778, P1)", () => {
    // the PUT's answer waits until `release`, as the hub's push and its answer race
    async function inFlight() {
      const { hello } = await load();
      hub = { overview: [] };
      await hello();
      const cards = await import("./overview-cards");
      let release!: () => void;
      const held = new Promise<void>((r) => (release = r));
      vi.mocked(fetch).mockImplementationOnce(async (_input, init) => {
        hub = { overview: JSON.parse(String((init as RequestInit).body)).overview };
        const answerNow = { ...(hub as object) };
        await held;
        return Response.json(answerNow);
      });
      cards.saveOverview(arrangement);
      await flush();
      return { cards, hello, release: async () => (release(), await flush()) };
    }
    // a list as the hub passes a push on: read into a map and written again, each card's keys in sorted order
    const asPassedOn = (list: OverviewEntry[]) => list.map((c) => Object.fromEntries(Object.entries(c).sort(([a], [b]) => (a < b ? -1 : 1))));

    it("a change from another device that lands after ours is taken, not replaced by our answer", async () => {
      const { cards, release } = await inFlight();
      Socket.last.push({ type: "navigation", overview: arrangement }); // ours, as the hub pushes it
      Socket.last.push({ type: "navigation", overview: newer }); // the phone's, after ours
      expect(cards.getOverviewPrefs()).toEqual(arrangement); // nothing jumps while ours is on its way
      await release();
      expect(cards.getOverviewPrefs()).toEqual(newer);
    });

    it("one that landed before ours does not undo ours", async () => {
      const { cards, release } = await inFlight();
      Socket.last.push({ type: "navigation", overview: newer }); // the phone's, before ours: ours is not heard yet
      await release();
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      Socket.last.push({ type: "navigation", overview: arrangement }); // ours comes after it
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
    });

    it("one pushed after our last with the value of an earlier change of ours is taken (review of #9778, P2)", async () => {
      const { cards, release } = await inFlight();
      let releaseSecond!: () => void;
      vi.mocked(fetch).mockImplementationOnce(async (_input, init) => {
        hub = { overview: JSON.parse(String((init as RequestInit).body)).overview };
        const answerNow = { ...(hub as object) };
        await new Promise<void>((r) => (releaseSecond = r));
        return Response.json(answerNow);
      });
      cards.saveOverview(newer);
      await flush();
      Socket.last.push({ type: "navigation", overview: arrangement }); // our first change, as the hub pushes it
      await release(); // the second goes now
      Socket.last.push({ type: "navigation", overview: newer }); // our second
      Socket.last.push({ type: "navigation", overview: arrangement }); // the phone puts the first back, after ours
      releaseSecond();
      await flush();
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
    });

    it("a push of other fields meanwhile is taken field by field", async () => {
      const { cards, release } = await inFlight();
      Socket.last.push({ type: "navigation", start: "inbox", pages: { order: ["kb"], shown: [], hidden: [] } });
      const page = await import("./start-page");
      const pages = await import("./sidebar-pages");
      expect(page.startPage()).toBe("inbox");
      expect(pages.getSidebarPrefs().order).toEqual(["kb"]);
      await release();
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
    });

    // review of #9778, P1: the hub's answer can come before its push of the same change
    it("one that landed before ours, heard after our answer, does not undo ours", async () => {
      const { cards, release } = await inFlight();
      await release(); // answered; the push of ours is still to come
      Socket.last.push({ type: "navigation", overview: newer }); // the phone's, before ours
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      Socket.last.push({ type: "navigation", overview: asPassedOn(arrangement) }); // ours
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      Socket.last.push({ type: "navigation", overview: newer }); // the phone's again, after ours
      expect(cards.getOverviewPrefs()).toEqual(newer);
    });

    it("one that lands after ours is taken once ours is heard, after our answer", async () => {
      const { cards, release } = await inFlight();
      Socket.last.push({ type: "navigation", overview: newer }); // the phone's, before ours
      await release();
      Socket.last.push({ type: "navigation", overview: asPassedOn(arrangement) }); // ours
      Socket.last.push({ type: "navigation", overview: [] }); // the phone's, after ours
      expect(cards.getOverviewPrefs()).toEqual([]);
    });

    it("ours is known by its cards, whatever order the hub writes their keys in", async () => {
      const { cards, release } = await inFlight();
      Socket.last.push({ type: "navigation", overview: asPassedOn(arrangement) }); // ours
      Socket.last.push({ type: "navigation", overview: newer }); // the phone's, after ours
      await release();
      expect(cards.getOverviewPrefs()).toEqual(newer);
    });

    it("a hello that did not resume ends the wait: the pushes of ours and the phone's went with the socket", async () => {
      const { cards, release, hello } = await inFlight();
      await release();
      hub = { overview: newer }; // the phone's, after ours, while the socket was down
      await hello();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      Socket.last.push({ type: "navigation", overview: [] }); // the phone's again
      expect(cards.getOverviewPrefs()).toEqual([]);
    });

    it("nor does a change on its way at such a hello wait for its push", async () => {
      const { cards, release, hello } = await inFlight();
      await hello(); // the socket came back while ours was on its way: its push may have gone with the old one
      await release(); // answered, and sent again (it was on its way when the hub was read)
      Socket.last.push({ type: "navigation", overview: asPassedOn(arrangement) }); // the push of the one sent again
      Socket.last.push({ type: "navigation", overview: newer }); // the phone's
      expect(cards.getOverviewPrefs()).toEqual(newer);
    });
  });

  // review of #9778, P1: two changes on their way at once could be answered out of order, an older answer standing for
  // the newest; and a hello's read answered after a change made here put the older arrangement back
  describe("changes made here, one after another and against a hello's read", () => {
    const body = (i: unknown) => JSON.parse(String((i as RequestInit).body)) as unknown;
    // what the next navigation requests get, in order (other requests, and these once it is empty, as the made-up hub)
    let queue: ((init?: RequestInit) => Promise<Response>)[] = [];
    // the next one waits until the returned function is called: a PUT is then taken (or answered 503 without being
    // taken), a GET answered with what the hub had when it was asked
    const holdNext = (fails = false) => {
      let release!: () => void;
      const held = new Promise<void>((r) => (release = r));
      queue.push(async (init) => {
        const then = { ...(hub as object) };
        await held;
        if (fails) return Response.json({ error: "database down" }, { status: 503 });
        if (init?.method !== "PUT") return Response.json(then);
        hub = { ...(hub as object), overview: (body(init) as { overview: unknown }).overview };
        return Response.json(hub);
      });
      return async () => (release(), await flush());
    };
    async function loaded() {
      hub = { overview: [] };
      const { hello } = await load();
      await hello();
      queue = [];
      const made = vi.mocked(fetch).getMockImplementation()!;
      vi.mocked(fetch).mockImplementation(async (input: string | URL | Request, init?: RequestInit) =>
        new URL(String(input), location.href).pathname === "/api/v1/navigation" && queue.length ? queue.shift()!(init) : made(input as string, init),
      );
      return { hello, cards: await import("./overview-cards") };
    }

    it("one made while another is on its way goes once that one is answered, the newest as it is then", async () => {
      const { cards } = await loaded();
      const first = holdNext();
      cards.saveOverview(arrangement);
      await flush();
      cards.saveOverview([...arrangement].reverse());
      cards.saveOverview(newer);
      await flush();
      expect(puts$()).toHaveLength(1);
      await first();
      expect(puts$().map(([, i]) => body(i))).toEqual([{ overview: arrangement }, { overview: newer }]);
      expect(hub).toMatchObject({ overview: newer });
    });

    it("the first one's failure does not stand for a newer one the hub took", async () => {
      const { cards, hello } = await loaded();
      const first = holdNext(true);
      cards.saveOverview(arrangement);
      await flush();
      cards.saveOverview(newer);
      await first();
      expect(hub).toMatchObject({ overview: newer });
      const sent = puts$().length;
      await hello();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      expect(puts$()).toHaveLength(sent); // nothing to send again
    });

    it("the first one's answer does not stand for a newer one the hub did not take", async () => {
      const { cards, hello } = await loaded();
      const first = holdNext();
      queue.push(answer(503, { error: "database down" }));
      cards.saveOverview(arrangement);
      await flush();
      cards.saveOverview(newer);
      await first();
      expect(hub).toMatchObject({ overview: arrangement });
      await hello(); // sent again, not replaced by the hub's
      expect(cards.getOverviewPrefs()).toEqual(newer);
      expect(hub).toMatchObject({ overview: newer });
    });

    it("a hello's read answered after a change made here does not undo it", async () => {
      const { cards, hello } = await loaded();
      const read = holdNext();
      await hello();
      cards.saveOverview(arrangement);
      await flush();
      expect(hub).toMatchObject({ overview: arrangement });
      await read(); // the hub's arrangement from before the change
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      expect(hub).toMatchObject({ overview: arrangement });
    });

    it("nor one answered while the change is on its way", async () => {
      const { cards, hello } = await loaded();
      const read = holdNext();
      const put = holdNext();
      await hello();
      cards.saveOverview(arrangement);
      await flush();
      Socket.last.push({ type: "navigation", overview: arrangement }); // ours, as the hub pushes it
      await read(); // the hub's arrangement from before the change
      await put();
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      expect(hub).toMatchObject({ overview: arrangement });
    });

    it("nor an earlier hello's read: the latest one sends a change the hub did not take", async () => {
      const { cards, hello } = await loaded();
      queue.push(answer(503, { error: "database down" }));
      cards.saveOverview(arrangement);
      await flush();
      const first = holdNext();
      const second = holdNext();
      await hello();
      await hello(); // the socket came back twice before either read was answered
      const sent = puts$().length;
      await first(); // overtaken by the second hello: left to it
      expect(puts$()).toHaveLength(sent);
      expect(hub).toMatchObject({ overview: [] });
      await second(); // the hub's arrangement from before the change: the change goes again
      expect(cards.getOverviewPrefs()).toEqual(arrangement);
      expect(hub).toMatchObject({ overview: arrangement });
    });

    // review of #9778, P1: a read answered late put back what the hub had at an earlier hello, or under another sign-in
    it("an earlier hello's read answered after a later one's does not put the older arrangement back", async () => {
      const { cards, hello } = await loaded();
      hub = { overview: arrangement };
      const first = holdNext(); // answered with the hub's arrangement when it was asked
      await hello();
      hub = { overview: newer }; // changed on another device meanwhile
      await hello(); // its read answered at once
      expect(cards.getOverviewPrefs()).toEqual(newer);
      await first();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      expect(puts$()).toEqual([]);
    });

    // review of #9778, P2: the start page, the sidebar's pages and the hand-over were still taken from such a read
    it("nor are an overtaken read's start page and sidebar pages taken, nor its failure", async () => {
      const { hello } = await loaded();
      const page = await import("./start-page");
      const pages = await import("./sidebar-pages");
      hub = { overview: [], start: "inbox", pages: { order: ["kb"], shown: [], hidden: [] } };
      const first = holdNext();
      let refuse!: () => void;
      const refused = new Promise<void>((r) => (refuse = r));
      queue.push(async () => (await refused, Response.json({ error: "not found" }, { status: 404 })));
      await hello();
      await hello(); // its read is refused later
      hub = { overview: [], start: "todos", pages: { order: ["inbox"], shown: [], hidden: [] } };
      await hello();
      expect(page.startPage()).toBe("todos");
      await first();
      refuse();
      await flush();
      expect(page.startPage()).toBe("todos");
      expect(pages.getSidebarPrefs().order).toEqual(["inbox"]);
      expect(page.startPageSynced()).toBe(true);
    });

    it("nor is anything handed over from a read answered after signing in as someone else", async () => {
      const { hello } = await loaded();
      const page = await import("./start-page");
      page.keepStartPage("inbox"); // this browser's choice; the hub has none to hand it to
      const read = holdNext();
      await hello();
      localStorage.setItem("persona.token", "another-token");
      const sent = puts$().length;
      await read();
      expect(puts$()).toHaveLength(sent);
    });

    // review of #9778, P2: a change answered after a hello that did not resume left a newer push from another device unseen
    it("a change answered after a hello that did not resume still lets the newer pushes heard meanwhile be taken", async () => {
      const { cards, hello } = await loaded();
      let release!: () => void;
      const held = new Promise<void>((r) => (release = r));
      queue.push(async () => (await held, Response.json({ overview: arrangement }))); // taken; its push went with the old socket
      cards.saveOverview(arrangement);
      await flush();
      const read = holdNext(); // the hello's read, answered later
      await hello();
      hub = { overview: newer };
      Socket.last.push({ type: "navigation", overview: newer }); // another device's, after ours
      release();
      await flush();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      await read();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      expect(hub).toMatchObject({ overview: newer });
    });

    // review of #9778, P1: a hello's read began before a newer push, or another tab's change, and was answered after it
    it("a hello's read answered after a newer push from another device does not put the older arrangement back", async () => {
      const { cards, hello } = await loaded();
      hub = { overview: arrangement };
      const read = holdNext(); // the hub's arrangement as it was when the read began
      await hello();
      hub = { overview: newer };
      Socket.last.push({ type: "navigation", overview: newer });
      await flush();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      await read();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      expect(puts$()).toEqual([]);
    });

    it("nor after another tab took a newer one", async () => {
      // the storage listeners of this load's modules only: an earlier test's, still on the window, would read the copy
      // under its own sign-in and clear this one's
      const tabs: EventListener[] = [];
      const add = window.addEventListener.bind(window);
      vi.spyOn(window, "addEventListener").mockImplementation((type: string, l: EventListenerOrEventListenerObject, o?: boolean | AddEventListenerOptions) =>
        type === "storage" ? void tabs.push(l as EventListener) : add(type, l, o),
      );
      const { cards, hello } = await loaded();
      hub = { overview: arrangement };
      const read = holdNext();
      await hello();
      const key = await copyKey();
      localStorage.setItem(key, JSON.stringify(newer));
      const e = new StorageEvent("storage", { key, oldValue: null, newValue: JSON.stringify(newer) });
      tabs.forEach((l) => l(e));
      expect(cards.getOverviewPrefs()).toEqual(newer);
      await read();
      expect(cards.getOverviewPrefs()).toEqual(newer);
      expect(puts$()).toEqual([]);
    });

    // review of #9778, P1: what was on its way was this browser's, not the sign-in's
    describe("signing in as someone else while a change is on its way", () => {
      const signInAgain = () => localStorage.setItem("persona.token", "another-token");

      it("a change queued under the first is not sent under the second, nor is its answer waited for there", async () => {
        const { cards, hello } = await loaded();
        const first = holdNext();
        cards.saveOverview(arrangement);
        await flush();
        cards.saveOverview(newer); // queued behind the first
        await flush();
        signInAgain();
        await first();
        expect(puts$().map(([, i]) => body(i))).toEqual([{ overview: arrangement }]);
        expect(cards.getOverviewPrefs()).toEqual([]);
        expect(localStorage.getItem(await copyKey())).toBeNull();
        // the second's hello and pushes are taken at once, and a change made under it goes at once
        await hello();
        expect(cards.getOverviewPrefs()).toEqual(arrangement);
        Socket.last.push({ type: "navigation", overview: newer });
        await flush();
        expect(cards.getOverviewPrefs()).toEqual(newer);
        cards.saveOverview([...arrangement].reverse());
        await flush();
        expect(puts$().map(([, i]) => body(i))).toEqual([{ overview: arrangement }, { overview: [...arrangement].reverse() }]);
      });

      it("the second's hello while the first's change is still on its way reads the second's arrangement", async () => {
        const { cards, hello } = await loaded();
        const first = holdNext();
        cards.saveOverview(arrangement);
        await flush();
        signInAgain();
        hub = { overview: newer };
        await hello();
        expect(cards.getOverviewPrefs()).toEqual(newer);
        await first();
        expect(cards.getOverviewPrefs()).toEqual(newer);
        expect(puts$()).toHaveLength(1);
      });

      it("a change made under the second while the first's is on its way goes at once, and stays when the first's ends", async () => {
        const { cards } = await loaded();
        const first = holdNext();
        cards.saveOverview(arrangement);
        await flush();
        cards.saveOverview([...arrangement].reverse()); // queued under the first: not sent under the second either
        await flush();
        signInAgain();
        cards.saveOverview(newer);
        await flush();
        expect(puts$().map(([, i]) => body(i))).toEqual([{ overview: arrangement }, { overview: newer }]);
        await first();
        expect(puts$()).toHaveLength(2);
        expect(cards.getOverviewPrefs()).toEqual(newer);
      });

      it("nor does the first's failure leave a change for the second to send again", async () => {
        const { cards, hello } = await loaded();
        const first = holdNext(true);
        cards.saveOverview(arrangement);
        await flush();
        signInAgain();
        await first();
        hub = { overview: newer };
        await hello();
        expect(cards.getOverviewPrefs()).toEqual(newer);
        expect(puts$()).toHaveLength(1);
        expect(hub).toMatchObject({ overview: newer });
      });

      it("nor does a change the first's hub did not take go again under the second", async () => {
        const { cards, hello } = await loaded();
        const first = holdNext(true);
        cards.saveOverview(arrangement);
        await first(); // not taken: it would go again at the first's next hello
        signInAgain();
        hub = { overview: newer };
        await hello();
        expect(cards.getOverviewPrefs()).toEqual(newer);
        expect(puts$()).toHaveLength(1);
        expect(hub).toMatchObject({ overview: newer });
      });

      it("nor does the first's answer say for the second whether its manager keeps the arrangement", async () => {
        const { cards } = await loaded();
        let release!: () => void;
        const held = new Promise<void>((r) => (release = r));
        queue.push(async () => (await held, Response.json({}))); // a manager from before the field: no field in its answer
        cards.saveOverview(arrangement);
        await flush();
        signInAgain();
        release();
        await flush();
        expect(cards.overviewKept()).toBeNull();
        cards.saveOverview(newer);
        await flush();
        expect(puts$().map(([, i]) => body(i))).toEqual([{ overview: arrangement }, { overview: newer }]);
      });

      it("nor what the first's manager said before: the second's changes go until its own hub says otherwise", async () => {
        const { cards } = await loaded();
        queue.push(async () => Response.json({})); // a manager from before the field
        cards.saveOverview(arrangement);
        await flush();
        expect(cards.overviewKept()).toBe(false);
        signInAgain();
        cards.saveOverview(newer);
        await flush();
        expect(puts$().map(([, i]) => body(i))).toEqual([{ overview: arrangement }, { overview: newer }]);
      });

      it("a push the first's socket passes on before the second's hello is not taken into the second's copy", async () => {
        const { cards, hello } = await loaded();
        signInAgain();
        Socket.last.push({ type: "navigation", overview: arrangement });
        await flush();
        expect(cards.getOverviewPrefs()).toEqual([]);
        expect(localStorage.getItem(await copyKey())).toBeNull();
        hub = { overview: newer };
        await hello();
        expect(cards.getOverviewPrefs()).toEqual(newer);
      });
    });

    it("nor one answered after signing in as someone else, under that account's copy", async () => {
      const { cards, hello } = await loaded();
      hub = { overview: arrangement };
      const read = holdNext();
      await hello();
      localStorage.setItem("persona.token", "another-token"); // signed in again as someone else
      await read();
      expect(cards.getOverviewPrefs()).toEqual([]);
      expect(localStorage.getItem(await copyKey())).toBeNull();
      expect(puts$()).toEqual([]);
    });
  });
});

describe("Overview's own sections (#10905)", () => {
  // In progress first, then the cards and the other sections: the arrangement the issue asks to keep on every device
  const IN_PROGRESS_FIRST: OverviewEntry[] = [
    { section: "in_progress" },
    { extension: "example-quota", id: "left" },
    { section: "needs_you" },
    { section: "waiting_on_computer" },
    { section: "working", hidden: true },
    { section: "unread" },
    { section: "computers" },
  ];
  const copyKey = async () => {
    const f = await import("./feed-cache");
    return f.scopedKey(f.feedCacheScope(), "overview.cards")!;
  };
  const puts$ = () => vi.mocked(fetch).mock.calls.filter(([, i]) => (i as RequestInit | undefined)?.method === "PUT");
  // a default manager of #9745: it keeps cards only, and refuses an entry without an extension's ids in its own words
  const cardsOnlyManager = () =>
    vi.mocked(fetch).mockImplementation(async (input: string | URL | Request, init?: RequestInit) => {
      if (new URL(String(input), location.href).pathname !== "/api/v1/navigation" || hub === "missing") return Response.json({});
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { overview?: OverviewEntry[] };
        if (body.overview?.some((e) => !("extension" in e))) return Response.json({ error: 'overview: "" / "" is not an extension\'s card' }, { status: 400 });
        hub = { ...hub, ...body };
      }
      return Response.json(hub);
    });

  it("two devices: In progress moved first on one, the other draws it from its hello, and a change on the push", async () => {
    const laptop = await load();
    await laptop.hello();
    const cards = await import("./overview-cards");
    cards.saveOverview(IN_PROGRESS_FIRST);
    await flush();
    expect(puts).toEqual([{ overview: IN_PROGRESS_FIRST }]);
    expect(cards.overviewKept()).toBe(true);
    laptop.store.disconnect();
    vi.resetModules();
    localStorage.clear();
    localStorage.setItem("persona.token", "test-token");
    const phone = await load();
    await phone.hello();
    const phoneCards = await import("./overview-cards");
    expect(phoneCards.getOverviewPrefs()).toEqual(IN_PROGRESS_FIRST);
    expect(JSON.parse(localStorage.getItem(await copyKey())!)).toEqual(IN_PROGRESS_FIRST);
    const items = phoneCards.arrangeOverview([{ extension: "example-quota", id: "left" }], phoneCards.getOverviewPrefs());
    expect(items.map((i) => i.key)[0]).toBe("section:in_progress");
    // the laptop shows Working again: the push brings it here
    const shown = IN_PROGRESS_FIRST.map((e) => ("section" in e && e.section === "working" ? { section: "working" } : e));
    Socket.last.push({ type: "navigation", overview: shown });
    expect(phoneCards.getOverviewPrefs()).toEqual(shown);
  });

  it("is handed over like the cards: an arrangement kept here goes to a hub that has none", async () => {
    hub = { start: "" };
    localStorage.setItem(await copyKey(), JSON.stringify(IN_PROGRESS_FIRST));
    const { hello } = await load();
    await hello();
    expect(puts).toEqual([{ overview: IN_PROGRESS_FIRST }]);
  });

  const QUOTA = { extension: "example-quota", id: "left" };
  const GATEWAY = { extension: "example-gateway", id: "today" };
  const body = (i: unknown) => JSON.parse(String((i as RequestInit).body)) as { overview: OverviewEntry[] };
  // In progress first, refused by a default manager of #9745 and kept as cards
  async function refused() {
    hub = { overview: [] };
    cardsOnlyManager();
    const page = await load();
    await page.hello();
    const cards = await import("./overview-cards");
    cards.saveOverview(IN_PROGRESS_FIRST);
    await flush();
    Socket.last.push({ type: "navigation", overview: [QUOTA] }); // the hub's push of the cards it took
    return { ...page, cards };
  }
  // the sections where IN_PROGRESS_FIRST has them, the cards as the hub has them
  const under = (list: OverviewEntry[]) => [IN_PROGRESS_FIRST[0], ...list, ...IN_PROGRESS_FIRST.slice(2)];

  // a mixed deployment: the cards follow on every device while the manager can't keep the sections
  it("a default manager of #9745 refuses one that names a section: the cards go on their own at once, the sections stay in this browser", async () => {
    const { cards, startPageSynced } = await refused();
    expect(puts$().map(([, i]) => body(i))).toEqual([{ overview: IN_PROGRESS_FIRST }, { overview: [QUOTA] }]);
    expect(hub).toMatchObject({ overview: [QUOTA] });
    expect(cards.overviewKept()).toBe(true);
    expect(cards.sectionsLocal()).toBe(true);
    expect(startPageSynced()).toBe(true);
    expect(cards.getOverviewPrefs()).toEqual(IN_PROGRESS_FIRST);
  });

  it("then a change of the cards goes without the sections, and another device's cards are taken under them", async () => {
    const { cards } = await refused();
    const wide = under([{ ...QUOTA, span: "full" }]);
    cards.saveOverview(wide);
    await flush();
    expect(body(puts$().at(-1)![1])).toEqual({ overview: [{ ...QUOTA, span: "full" }] });
    expect(hub).toMatchObject({ overview: [{ ...QUOTA, span: "full" }] });
    expect(cards.getOverviewPrefs()).toEqual(wide);
    Socket.last.push({ type: "navigation", overview: [{ ...QUOTA, span: "full" }] }); // ours
    // another device puts the gateway's card first: the hub pushes its cards
    hub = { overview: [GATEWAY, QUOTA] };
    Socket.last.push({ type: "navigation", overview: [GATEWAY, QUOTA] });
    expect(cards.getOverviewPrefs()).toEqual(under([GATEWAY, QUOTA]));
    // its Reset is everything's, as the manager's own rule has it
    Socket.last.push({ type: "navigation", overview: [] });
    expect(cards.getOverviewPrefs()).toEqual([]);
  });

  it("each hello asks once more with the sections, after a reload too, and an updated manager is handed them", async () => {
    const { cards, hello, store } = await refused();
    hub = { overview: [GATEWAY, QUOTA] }; // another device's cards, while the socket was down
    let sent = puts$().length;
    await hello();
    expect(cards.getOverviewPrefs()).toEqual(under([GATEWAY, QUOTA]));
    expect(puts$().slice(sent).map(([, i]) => body(i))).toEqual([{ overview: under([GATEWAY, QUOTA]) }]); // refused, nothing more
    expect(hub).toMatchObject({ overview: [GATEWAY, QUOTA] });
    // the page is loaded again: the note stays with the copy
    store.disconnect();
    vi.resetModules();
    const again = await load();
    await again.hello();
    const reloaded = await import("./overview-cards");
    expect(reloaded.sectionsLocal()).toBe(true);
    expect(reloaded.getOverviewPrefs()).toEqual(under([GATEWAY, QUOTA]));
    // the manager is updated meanwhile; the next hello hands it the sections
    vi.mocked(fetch).mockImplementation(async (input: string | URL | Request, init?: RequestInit) => {
      if (new URL(String(input), location.href).pathname !== "/api/v1/navigation" || hub === "missing") return Response.json({});
      if (init?.method === "PUT") hub = { ...hub, ...(JSON.parse(String(init.body)) as object) };
      return Response.json(hub);
    });
    sent = puts$().length;
    await again.hello();
    expect(hub).toMatchObject({ overview: under([GATEWAY, QUOTA]) });
    expect(reloaded.sectionsLocal()).toBe(false);
    expect(reloaded.overviewKept()).toBe(true);
    expect(puts$()).toHaveLength(sent + 1);
  });

  it("a push naming sections says the manager keeps them now: taken as it is, and the next change goes with them", async () => {
    const { cards } = await refused();
    // the manager is updated and another device puts Working first
    vi.mocked(fetch).mockImplementation(async (input: string | URL | Request, init?: RequestInit) => {
      if (new URL(String(input), location.href).pathname !== "/api/v1/navigation" || hub === "missing") return Response.json({});
      if (init?.method === "PUT") hub = { ...hub, ...(JSON.parse(String(init.body)) as object) };
      return Response.json(hub);
    });
    const theirs: OverviewEntry[] = [{ section: "working" }, QUOTA, ...IN_PROGRESS_FIRST.filter((e) => "section" in e && e.section !== "working")];
    Socket.last.push({ type: "navigation", overview: theirs });
    expect(cards.getOverviewPrefs()).toEqual(theirs);
    expect(cards.sectionsLocal()).toBe(false);
    const mine = [...theirs].reverse();
    cards.saveOverview(mine);
    await flush();
    expect(body(puts$().at(-1)![1])).toEqual({ overview: mine });
    expect(hub).toMatchObject({ overview: mine });
  });

  it("the same manager still keeps a change of only cards (the sections where they are by default write none)", async () => {
    hub = { overview: [] };
    cardsOnlyManager();
    const { hello } = await load();
    await hello();
    const cards = await import("./overview-cards");
    cards.saveOverview([{ extension: "example-quota", id: "left", span: "full" }]);
    await flush();
    expect(cards.overviewKept()).toBe(true);
    expect(hub).toMatchObject({ overview: [{ extension: "example-quota", id: "left", span: "full" }] });
    expect(puts$()).toHaveLength(1);
  });
});
