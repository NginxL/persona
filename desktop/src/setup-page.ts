import { join } from "node:path";
import { pathToFileURL } from "node:url";

// The setup page comes from the shell's own scheme, not from file://: a packaged app's fuses
// (grantFileProtocolExtraPrivileges off, electron-builder.yml) keep file:// out of its asar, where a clean first run
// (or Settings > General > This computer) fails with ERR_FILE_NOT_FOUND. No Electron here: tests run it as is.

export const SETUP_SCHEME = "persona-setup";
export const SETUP_URL = `${SETUP_SCHEME}://app/setup.html`;

export interface SetupAsset {
  readonly file: string;
  readonly contentType: string;
}

/** The page's three files, which copy-assets.mjs puts in dist/setup/, and nothing else. */
const FILES = new Map<string, readonly [string, string]>([
  ["/setup.html", ["setup.html", "text/html; charset=utf-8"]],
  ["/setup.css", ["setup.css", "text/css; charset=utf-8"]],
  ["/setup.cjs", ["setup.cjs", "text/javascript; charset=utf-8"]],
]);

/** The file and content type behind a request of the setup scheme: only a GET or HEAD of one of the page's three
 *  files, so no path a page asks for reaches the filesystem or another file of the app. */
export function setupAsset(raw: string, method = "GET"): SetupAsset | undefined {
  if (method !== "GET" && method !== "HEAD") return undefined;
  let url: URL;
  try { url = new URL(raw); } catch { return undefined; }
  const entry = FILES.get(url.pathname);
  if (url.protocol !== `${SETUP_SCHEME}:` || url.hostname !== "app" || !entry
    || url.username || url.password || url.port || url.search || url.hash || /[\\%]/.test(raw)) return undefined;
  return { file: pathToFileURL(join(import.meta.dirname, "setup", entry[0])).href, contentType: entry[1] };
}

export function setupFile(raw: string, method = "GET"): string | undefined {
  return setupAsset(raw, method)?.file;
}
