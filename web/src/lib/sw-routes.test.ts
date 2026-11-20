import { describe, expect, it } from "vitest";
import { assetsIn, strategy } from "./sw-routes";
import manifestText from "../../public/manifest.webmanifest?raw";
import icon192 from "../../public/app-icon-192.png?inline";
import icon512 from "../../public/app-icon-512.png?inline";
import maskable192 from "../../public/app-icon-maskable-192.png?inline";
import maskable512 from "../../public/app-icon-maskable-512.png?inline";
import touchIcon from "../../public/apple-touch-icon.png?inline";

const origin = "https://hub.example.com";
const at = (path: string, mode = "no-cors", method = "GET", from = origin) => strategy(new URL(path, from), origin, method, mode);

describe("service worker routes", () => {
  it("serves page loads from the shell, whatever the client route", () => {
    for (const p of ["/", "/persona/new", "/s/laptop/abc", "/inbox/1?token=x", "/screen/laptop"]) expect(at(p, "navigate")).toBe("shell");
  });

  it("never touches the API, the admin page or itself", () => {
    for (const p of ["/api/v1/ws", "/api/v1/sessions", "/v1/projects", "/v1/projects/checkout/documents/refund/approved", "/api/v1/files/x.png?token=x", "/api", "/admin", "/admin/assets/a-1.js", "/sw.js"]) {
      expect(at(p)).toBe("pass");
      expect(at(p, "navigate")).toBe("pass");
    }
    expect(at("/assets/a-1.js", "cors", "POST")).toBe("pass");
    expect(at("/assets/a-1.js", "cors", "GET", "https://other.example.com")).toBe("pass");
  });

  it("passes public navigation and every standalone asset through without a shell or cache lookup", () => {
    for (const p of ["/share", "/share/" + "A".repeat(64), "/share-assets", "/share-assets/viewer-hash.js", "/share-assets/viewer-hash.css", "/public-share/index.html", "/api/public/agent-shares/" + "A".repeat(64)]) {
      expect(at(p)).toBe("pass"); expect(at(p, "navigate")).toBe("pass");
    }
  });
  it("keeps hashed assets and the root's icons and manifest", () => {
    expect(at("/assets/main-NaR2WL1g.js", "cors")).toBe("asset");
    expect(at("/assets/KaTeX_Main-Regular-B22Nviop.woff2", "cors")).toBe("asset");
    expect(at("/manifest.webmanifest", "cors")).toBe("static");
    expect(at("/app-icon-192.png")).toBe("static");
    expect(at("/kb/x.png")).toBe("pass");
  });

  it("finds the assets a built shell loads up front", () => {
    const html = `<script type="module" crossorigin src="/assets/main-1.js"></script>
      <link rel="modulepreload" crossorigin href="/assets/react-2.js">
      <link rel="stylesheet" crossorigin href="/assets/main-3.css">
      <link rel="icon" href="/app-icon-64.png"><link rel="stylesheet" href="/assets/main-3.css">`;
    expect(assetsIn(html)).toEqual(["/assets/main-1.js", "/assets/react-2.js", "/assets/main-3.css"]);
  });

  it("reads them relative to the shell's <base> too, as the build writes them", () => {
    const html = `<base href="/" /><script type="module" crossorigin src="./assets/main-1.js"></script>
      <link rel="modulepreload" crossorigin href="./assets/react-2.js"><link rel="icon" href="app-icon-64.png">`;
    expect(assetsIn(html)).toEqual(["/assets/main-1.js", "/assets/react-2.js"]);
  });
});

// public/manifest.webmanifest: what Chrome needs to install it, and every icon it names is there at its size.
describe("manifest", () => {
  const manifest = JSON.parse(manifestText);
  // relative to the manifest, so they hold under a sub-path too (docs/web/web.md "Subpaths")
  const files: Record<string, string> = {
    "app-icon-192.png": icon192,
    "app-icon-512.png": icon512,
    "app-icon-maskable-192.png": maskable192,
    "app-icon-maskable-512.png": maskable512,
    "apple-touch-icon.png": touchIcon,
  };
  // a PNG's width and height are big-endian at bytes 16 and 20 of its IHDR
  const pngSize = (src: string) => {
    expect(files[src], src).toMatch(/^data:image\/png;base64,/);
    const b = Uint8Array.from(atob(files[src].split(",")[1]), (c) => c.charCodeAt(0));
    const u32 = (i: number) => new DataView(b.buffer).getUint32(i);
    return `${u32(16)}x${u32(20)}`;
  };

  it("is installable", () => {
    expect(manifest).toMatchObject({ name: "Persona", start_url: "./", scope: "./", display: "standalone" });
    const sizes = (purpose: string) => manifest.icons.filter((i: { purpose: string }) => i.purpose === purpose).map((i: { sizes: string }) => i.sizes);
    expect(sizes("any")).toEqual(expect.arrayContaining(["192x192", "512x512"]));
    expect(sizes("maskable")).toEqual(expect.arrayContaining(["192x192", "512x512"]));
  });

  it("names icons that exist at their sizes", () => {
    const icons = [...manifest.icons, ...manifest.shortcuts.flatMap((s: { icons: unknown[] }) => s.icons)] as { src: string; sizes: string }[];
    for (const i of icons) expect(pngSize(i.src), i.src).toBe(i.sizes);
    expect(pngSize("apple-touch-icon.png")).toBe("180x180");
  });

  it("opens shortcuts on the client's routes", () => {
    for (const s of manifest.shortcuts) expect(s.url).toMatch(/^\.\/[a-z]/);
  });
});
