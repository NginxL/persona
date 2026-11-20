// The route table every client follows (docs/protocol/deep-links.md, #9926): the shared cases of
// spec/fixtures/deep-links/cases.json read the same here as on the iPhone (HubPageLinkTests) and Android. The web is one
// hub's page, so it reads the cases whose only paired hub is its own (that hub's addresses are its origins).
import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/deep-links/cases.json";
import { appPage, appPath, DEEP_KB_KINDS, DEEP_SETTINGS_TABS, pageOfPath, pagePath } from "./deep-links";
import { linkRoute, type LinkRoute } from "./links";
import { KB_KINDS } from "./kb";
import { parse as parseRoute } from "./router";
import { TABS } from "../views/settings/nav";

interface Hub {
  id: string;
  addresses: string[];
}
interface ParseCase {
  name: string;
  url: string;
  hubs: string[];
  page: Record<string, string> | null;
  reason?: string;
}
const hubs = fixture.hubs as Record<string, Hub>;
const cases = fixture.parse as ParseCase[];

/** A page as the fixture writes it, without the values the web does not carry in a link it opens (an Agent's `view`, a
 *  computer's `range`: the pages read those from the address bar themselves). */
function kept(page: Record<string, string> | null): Record<string, string> | null {
  if (!page) return null;
  const { view: _view, range: _range, ...rest } = page;
  void _view;
  void _range;
  return rest;
}

function clean(o: Record<string, string | undefined>): Record<string, string> {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined)) as Record<string, string>;
}

/** The page a link this page opens goes to, as the fixture writes a page. */
function pageOfRoute(r: LinkRoute): Record<string, string> | null {
  switch (r.kind) {
    case "agent":
      return { page: "session", machine: r.mid, session: r.sid };
    case "conversation":
      return clean({ page: "conversation", id: r.sid, machine: r.mid, manager: r.manager });
    case "task":
      return { page: "todo", id: r.id };
    case "page": {
      const [p, q = ""] = r.path.split("?");
      return pageOfPath(p!, q ? `?${q}` : "") as Record<string, string> | null;
    }
    default:
      return null;
  }
}

/** What this page opens for a link when `hub` is the hub it is served by, as the fixture writes a page: an app link as
 *  lib/links.ts reads it when opened (appPath → appPage), a web link as linkRoute opens it. */
function opened(url: string, hub: Hub): Record<string, string> | null {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return null;
  }
  const app = appPage(u);
  if (app) return { ...app.page, ...(app.hub ? { hub: app.hub } : {}) } as Record<string, string>;
  // each address is an origin, and the sub-path the hub is served at (lib/base.ts) is its path
  for (const a of hub.addresses) {
    const base = new URL(a);
    const found = pageOfRoute(linkRoute(url, false, [base.origin], base.pathname.replace(/\/$/, "")));
    if (found) return { ...found, hub: hub.id };
  }
  return null;
}

describe("deep links: the shared cases", () => {
  it("has the cases", () => {
    expect(fixture.version).toBe(1);
    expect(cases.length).toBeGreaterThan(60);
  });
  for (const c of cases.filter((c) => c.hubs.length === 1)) {
    it(c.name, () => {
      expect(kept(opened(c.url, hubs[c.hubs[0]!]!))).toEqual(kept(c.page));
      expect(c.page === null).toBe(c.reason !== undefined);
    });
  }
});

describe("deep links: pages and paths", () => {
  it("every page's path reads back as the same page, and the router opens it", () => {
    for (const c of cases) {
      if (!c.page) continue;
      const { hub: _hub, ...page } = c.page;
      void _hub;
      const path = pagePath(page as never);
      const [p, q = ""] = path.split("?");
      expect(pageOfPath(p!, q ? `?${q}` : ""), `${c.name}: ${path}`).toEqual(page);
      expect(parseRoute(p!, q).name, path).not.toBe("not-found");
    }
  });

  it("opens an app link as the path of the page it names, or nothing", () => {
    expect(appPath("persona://open/inbox/i-1?manager=m2&token=x")).toBe("/inbox/i-1?manager=m2");
    expect(appPath("persona://kb/docs/a/b")).toBe("/kb/docs/a%2Fb");
    expect(appPath("persona://settings/extensions/gh")).toBe("/settings?tab=extensions&extension=gh");
    expect(appPath("persona://open?route=%2Fpersona%2Fc-1")).toBe("/persona/c-1");
    expect(appPath("persona://task/t-1")).toBe("/todos/t-1");
    expect(appPath("persona://session/m/s?at=x")).toBe("/s/m/s?at=x");
    for (const none of ["persona://open//evil.example/x", "persona://open/admin", "persona://open?route=https%3A%2F%2Fevil.example", "persona://pair?url=x"]) {
      expect(appPath(none), none).toBeNull();
    }
  });

  it("opens Labs settings from web and app links", () => {
    expect(pageOfPath("/settings", "tab=labs")).toEqual({ page: "settings", tab: "labs" });
    expect(appPage(new URL("persona://open/settings?tab=labs"))?.page).toEqual({ page: "settings", tab: "labs" });
  });

  it("opens Labs without carrying setting changes or credentials from the link", () => {
    expect(appPath("persona://open/settings?tab=labs&enabled=1&token=x")).toBe("/settings?tab=labs");
    expect(pageOfPath("/settings", "?tab=labs&enabled=1&token=x")).toEqual({ page: "settings", tab: "labs" });
  });

  it("keeps the knowledge kinds and settings tabs of the pages that define them", () => {
    expect(DEEP_KB_KINDS).toEqual([...KB_KINDS]);
    expect(Object.keys(DEEP_SETTINGS_TABS).sort()).toEqual([...TABS].sort());
  });
});
