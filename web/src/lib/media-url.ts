// An address under /api/ that an <img> can load: the token in the query, under the page's sub-path. lib/inbox.ts
// re-exports it until its importers import it from here (#11538).
import { getToken } from "./api";
import { BASE } from "./base";

/** Media under /api/ for an <img>: the token goes in the query (an <img> can't send a header), and the path under the
 * sub-path the page is served at (lib/base.ts). */
export function withToken(url: string | undefined, token = getToken(), base = BASE): string | undefined {
  if (!url) return undefined;
  if (!url.startsWith("/api/")) return url;
  return `${base}${url}${url.includes("?") ? "&" : "?"}token=${encodeURIComponent(token)}`;
}
