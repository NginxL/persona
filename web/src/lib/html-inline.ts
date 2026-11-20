// Files a previewed HTML page uses from beside it (pictures, style sheets and their fonts / imports, scripts, icons),
// read from the machine through the hub and put into the page itself (data: URLs, inline <style> / <script>): the
// preview frame has no address to load them from and no network (docs/web/web.md "HTML preview", #1413). The page is parsed
// with DOMParser, whose documents load nothing and run nothing.
import { cssImports, isRelativeRef, resolveRelative, rewriteCssUrls } from "./html-preview";

export type ReadFile = (path: string) => Promise<Blob | null>;

export const INLINE_LIMITS = { files: 150, fileBytes: 10 << 20, totalBytes: 40 << 20 };

const MIME: Record<string, string> = {
  png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", svg: "image/svg+xml", ico: "image/x-icon", avif: "image/avif", bmp: "image/bmp",
  woff: "font/woff", woff2: "font/woff2", ttf: "font/ttf", otf: "font/otf", mp4: "video/mp4", webm: "video/webm", mp3: "audio/mpeg", m4a: "audio/mp4", wav: "audio/wav", ogg: "audio/ogg",
};

const dirOf = (p: string) => p.slice(0, p.lastIndexOf("/")) || "/";

function dataURL(blob: Blob, path: string): Promise<string> {
  const ext = path.split(".").pop()?.toLowerCase() ?? "";
  const typed = MIME[ext] && blob.type !== MIME[ext] ? new Blob([blob], { type: MIME[ext] }) : blob;
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error);
    r.readAsDataURL(typed);
  });
}

/** The page with what it uses from beside it inlined; how many files that took and how many could not be read. */
export async function inlineSiblings(html: string, pagePath: string, read: ReadFile): Promise<{ html: string; loaded: number; missing: number }> {
  const doc = new DOMParser().parseFromString(html, "text/html");
  const blobs = new Map<string, Promise<Blob | null>>();
  let total = 0;
  let loaded = 0;
  let missing = 0;
  const fetchOnce = (path: string): Promise<Blob | null> => {
    let p = blobs.get(path);
    if (!p) {
      p =
        blobs.size >= INLINE_LIMITS.files
          ? Promise.resolve(null)
          : read(path).then(
              (b) => {
                if (!b || b.size > INLINE_LIMITS.fileBytes || total + b.size > INLINE_LIMITS.totalBytes) return null;
                total += b.size;
                return b;
              },
              () => null,
            );
      blobs.set(path, p);
      void p.then((b) => (b ? loaded++ : missing++));
    }
    return p;
  };
  const asData = async (path: string) => {
    const b = await fetchOnce(path);
    return b ? dataURL(b, path) : null;
  };
  const asText = async (path: string) => (await fetchOnce(path))?.text() ?? null;

  /** A style sheet with its imports pulled in and its url()s inlined, relative to the folder it came from. */
  const sheet = async (css: string, dir: string, depth = 0): Promise<string> => {
    if (depth < 3) {
      for (const imp of cssImports(css)) {
        if (!isRelativeRef(imp)) continue;
        const path = resolveRelative(dir, imp);
        const text = await asText(path);
        const body = text === null ? "" : await sheet(text, dirOf(path), depth + 1);
        css = css.replace(new RegExp(`@import\\s+(?:url\\(\\s*)?['"]?${imp.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}['"]?\\s*\\)?[^;]*;`), () => body); // a function: a string would read $$, $& and $' in the sheet as patterns
      }
    }
    const urls = [...css.matchAll(/url\(\s*(['"]?)([^'")]+)\1\s*\)/gi)].map((m) => m[2]).filter(isRelativeRef);
    const found = new Map<string, string | null>();
    await Promise.all(urls.map(async (u) => found.set(u, await asData(resolveRelative(dir, u)))));
    return rewriteCssUrls(css, (u) => (isRelativeRef(u) ? (found.get(u) ?? null) : null));
  };

  const dir = dirOf(pagePath);
  const jobs: Promise<unknown>[] = [];
  const attr = (sel: string, name: string) => {
    for (const el of doc.querySelectorAll(sel)) {
      const v = el.getAttribute(name);
      if (!v || !isRelativeRef(v)) continue;
      jobs.push(asData(resolveRelative(dir, v)).then((d) => d && el.setAttribute(name, d)));
    }
  };
  attr("img[src]", "src");
  attr("source[src]", "src");
  attr("video[src]", "src");
  attr("audio[src]", "src");
  attr("video[poster]", "poster");
  attr("input[type=image][src]", "src");
  attr("link[rel~=icon][href]", "href");
  attr("image[href]", "href");
  for (const img of doc.querySelectorAll("img[srcset], source[srcset]")) if (img.getAttribute("srcset")!.split(",").some((c) => isRelativeRef(c.trim().split(/\s+/)[0]))) img.removeAttribute("srcset");
  for (const link of doc.querySelectorAll("link[rel~=stylesheet][href]")) {
    const href = link.getAttribute("href")!;
    if (!isRelativeRef(href)) continue;
    const path = resolveRelative(dir, href);
    jobs.push(
      asText(path).then(async (text) => {
        if (text === null) return;
        const style = doc.createElement("style");
        const media = link.getAttribute("media"); // the sheet still applies only where the page asked for it
        if (media) style.setAttribute("media", media);
        style.textContent = await sheet(text, dirOf(path));
        link.replaceWith(style);
      }),
    );
  }
  for (const style of doc.querySelectorAll("style")) jobs.push(sheet(style.textContent ?? "", dir).then((t) => (style.textContent = t)));
  for (const el of doc.querySelectorAll("[style*='url(']")) jobs.push(sheet(el.getAttribute("style")!, dir).then((t) => el.setAttribute("style", t)));
  for (const script of doc.querySelectorAll("script[src]")) {
    const src = script.getAttribute("src")!;
    if (!isRelativeRef(src)) continue;
    jobs.push(
      asText(resolveRelative(dir, src)).then((text) => {
        if (text === null) return;
        script.removeAttribute("src");
        script.textContent = text.replace(/<\/script/gi, "<\\/script"); // it must not end the element early
      }),
    );
  }
  await Promise.all(jobs);
  await Promise.all(blobs.values());
  const doctype = /^\s*(?:<\?xml[^>]*>\s*)?<!doctype/i.test(html) ? "<!DOCTYPE html>" : "";
  return { html: doctype + doc.documentElement.outerHTML, loaded, missing };
}
