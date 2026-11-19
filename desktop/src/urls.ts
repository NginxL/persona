export interface HubAddress {
  readonly url: URL;
  /** Path prefix, including a trailing slash; empty at the root. */
  readonly prefix: string;
}

/** The hub address a person entered. Never accepts a credential, fragment, non-network scheme or opaque URL. */
export function parseHubAddress(input: string): HubAddress {
  const trimmed = input.trim();
  if (!trimmed || trimmed.length > 2048 || /[\u0000- \u007f]/.test(trimmed)) throw new Error("Enter the hub's web address.");
  const withScheme = /^[a-z][a-z\d+.-]*:/i.test(trimmed) ? trimmed : `https://${trimmed}`;
  const url = new URL(withScheme);
  if (url.protocol !== "https:" && url.protocol !== "http:") throw new Error("The hub address must use HTTP or HTTPS.");
  if (!url.hostname || url.username || url.password || url.hash) throw new Error("The hub address cannot contain credentials or a fragment.");
  url.pathname = `${url.pathname.replace(/\/+$/, "")}/`;
  url.search = "";
  return { url, prefix: url.pathname === "/" ? "" : url.pathname };
}

/** A page of the configured hub, including when a reverse proxy mounts it below the origin root. */
export function inHub(hub: HubAddress, candidate: string | URL): boolean {
  let url: URL;
  try { url = typeof candidate === "string" ? new URL(candidate) : candidate; } catch { return false; }
  return url.origin === hub.url.origin && url.pathname.startsWith(hub.url.pathname);
}

/** An external link is opened only by the system browser, never given a new Electron window. */
export function externalBrowserURL(candidate: string): string | undefined {
  try {
    const url = new URL(candidate);
    return (url.protocol === "https:" || url.protocol === "http:") && !url.username && !url.password ? url.href : undefined;
  } catch { return undefined; }
}

export interface DesktopDeepLink {
  readonly kind: "open" | "pair-link" | "pair-app";
  readonly url: string;
  readonly route?: string;
}

/** The query keys a page of the route table reads (docs/protocol/deep-links.md); an app link's others (`hub`, and
 *  anything that would act, such as a token) are not handed on. */
const ROUTE_KEYS = new Set(["at", "view", "work", "machine", "manager", "id", "mode", "date", "range", "tab", "purpose", "channel", "extension", "connector", "plugin", "state"]);

/** A route to hand to the web app: one leading "/", never "//", no backslash or control character, staying on the hub. */
function safeRoute(route: string): boolean {
  if (!route.startsWith("/") || route.startsWith("//") || route.includes("\\") || /[\u0000-\u001f\u007f]/.test(route)) return false;
  try {
    return new URL(route, "https://desktop.invalid/").origin === "https://desktop.invalid";
  } catch { return false; }
}

/** The path with only the query keys a page reads, or undefined when it is no route to hand on. */
function pageRoute(path: string, query: URLSearchParams): string | undefined {
  const kept = new URLSearchParams();
  for (const [key, value] of query) if (ROUTE_KEYS.has(key) && value) kept.append(key, value);
  const route = path + (kept.size ? `?${kept}` : "");
  return safeRoute(route) ? route : undefined;
}

/** Deep links are data, never automatic pairing or navigation to an arbitrary web origin. A page link is
 *  persona://open<path> (the route table's app link: the hub's web path after persona://open) or the first form,
 *  persona://open?route=<path>; the web app in the window opens the path (and refuses what is no page of it). */
export function parseDeepLink(raw: string): DesktopDeepLink | undefined {
  let url: URL;
  try { url = new URL(raw); } catch { return undefined; }
  if (url.protocol !== "persona:" && url.protocol !== "jarvis:") return undefined;
  if (url.username || url.password || url.port) return undefined;
  if (url.host === "link") return { kind: "pair-link", url: raw };
  if (url.host === "pair") return { kind: "pair-app", url: raw };
  if (url.host !== "open" || url.hash) return undefined;
  if (url.pathname) {
    const route = pageRoute(url.pathname, url.searchParams);
    return route ? { kind: "open", url: raw, route } : undefined;
  }
  if (url.searchParams.size !== 1) return undefined;
  // The first form carries a whole path with its query: the same keys go on as from the path form, so no `add=1`
  // or token gets through it either (#10281). Its fragment is ignored, as the web's reading of it does.
  const carried = url.searchParams.get("route");
  if (!carried || !safeRoute(carried)) return undefined;
  const inner = new URL(carried, "https://desktop.invalid/");
  const route = pageRoute(inner.pathname, inner.searchParams);
  return route ? { kind: "open", url: raw, route } : undefined;
}

export function hubRoute(hub: HubAddress, route: string): string {
  const target = new URL(hub.url);
  const clean = route.replace(/^\/+/, "");
  target.pathname = `${target.pathname}${clean}`;
  return target.href;
}
