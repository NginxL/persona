// What the service worker (src/sw.ts, docs/web/web.md "Install as an app") does with each request, kept apart so it can be tested.
// Only the client's own files: the API, its WebSocket and images (/api/) and the admin page (/admin) always go to the
// hub untouched, so nothing live is ever served from a cache.

/** index.html, kept under "/" whatever path loaded it (a `?token=` sign-in link never becomes a cache key). */
export const SHELL_CACHE = "persona-shell-v2";
/** Vite's content-hashed files under /assets/: immutable, kept until the newest ASSET_LIMIT. */
export const ASSET_CACHE = "persona-assets-v1";
/** About three builds' worth of chunks (a build has ~130), so a redeploy never evicts the running one's. */
export const ASSET_LIMIT = 400;
/** How long a page load waits for the hub before starting from the cached shell. */
export const SHELL_TIMEOUT_MS = 3500;

export type Strategy =
  | "shell" // a page load: the hub's index.html, else the cached one
  | "asset" // a hashed file: the cache, else the hub
  | "static" // the icons and the manifest at the root: the hub, else the cache
  | "pass"; // everything else: not touched

export function strategy(url: URL, origin: string, method: string, mode: string): Strategy {
  if (method !== "GET" || url.origin !== origin) return "pass";
  const p = url.pathname;
  if (p === "/v1/projects" || p.startsWith("/v1/projects/")) return "pass";
  if (p === "/api" || p.startsWith("/api/") || p === "/admin" || p.startsWith("/admin/")) return "pass";
  if (p === "/sw.js" || p === "/share" || p.startsWith("/share/") || p === "/share-assets" || p.startsWith("/share-assets/") || p === "/public-share" || p.startsWith("/public-share/")) return "pass";
  if (mode === "navigate") return "shell";
  if (p.startsWith("/assets/")) return "asset";
  if (/^\/[^/]+\.(png|webmanifest)$/.test(p)) return "static";
  return "pass";
}

/** The /assets/ files a shell loads up front (its script, CSS and preloads), cached with it so it starts offline. The
 * build writes them relative to the page's <base> ("./assets/…", vite.config.ts), which is "/" wherever the worker runs
 * (lib/pwa.ts registers it only at the root). */
export function assetsIn(html: string): string[] {
  const out = new Set<string>();
  for (const m of html.matchAll(/(?:src|href)="(?:\.)?(\/assets\/[^"?#]+)"/g)) out.add(m[1]);
  return [...out];
}
