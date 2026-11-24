// @vitest-environment jsdom
import { Blob as NodeBlob } from "node:buffer";
import { describe, expect, it } from "vitest";
import { inlineSiblings, type ReadFile } from "./html-inline";

// jsdom's Blob has no text(), which the inliner reads style sheets with: Node's Blob stands in for it.
const files = (entries: Record<string, string>): ReadFile => async (path) => (path in entries ? (new NodeBlob([entries[path]]) as unknown as Blob) : null);

describe("a previewed page's style sheets beside it", () => {
  it("keeps the media a linked style sheet is limited to", async () => {
    const read = files({ "/site/print.css": "body{display:none}", "/site/wide.css": "main{width:80%}", "/site/all.css": "p{margin:0}" });
    const page = `<html><head>
      <link rel="stylesheet" href="print.css" media="print">
      <link rel="stylesheet" href="wide.css" media="(min-width: 900px)">
      <link rel="stylesheet" href="all.css">
    </head><body><p>hi</p></body></html>`;
    const { html } = await inlineSiblings(page, "/site/index.html", read);
    const doc = new DOMParser().parseFromString(html, "text/html");
    const styles = [...doc.querySelectorAll("style")].map((s) => [s.textContent, s.getAttribute("media")]);
    expect(styles).toEqual([
      ["body{display:none}", "print"],
      ["main{width:80%}", "(min-width: 900px)"],
      ["p{margin:0}", null],
    ]);
  });

  it("inlines an imported style sheet exactly as it is", async () => {
    const imported = `.tier::after{content:"$$$"}\n.price::before{content:"$&"}\n.a::after{content:"$'"}\n.b::after{content:"$\`"}`;
    const read = files({ "/site/tier.css": imported });
    const { html } = await inlineSiblings(`<style>@import "tier.css";\np{margin:0}</style><p>x</p>`, "/site/index.html", read);
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(doc.querySelector("style")?.textContent).toBe(`${imported}\np{margin:0}`);
  });

  it("preserves @import media, supports, and layer conditions and escapes </style> inside inlined sheets", async () => {
    const read = files({
      "/site/print.css": "body{display:none}",
      "/site/dark.css": "html{background:#000}",
      "/site/layered.css": ".btn{color:red}",
      "/site/tricky.css": `.note::after{content:"</style><div id=leaked>x</div>"}`,
    });
    const page = `<html><head>
      <link rel="stylesheet" href="tricky.css">
      <style>
        @import "print.css" print;
        @IMPORT url("dark.css") supports(display: grid) screen and (prefers-color-scheme: dark);
        @import "layered.css" layer(components);
      </style>
    </head><body><p>ok</p></body></html>`;
    const { html } = await inlineSiblings(page, "/site/index.html", read);
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(doc.getElementById("leaked")).toBeNull();
    const styles = [...doc.querySelectorAll("style")].map((s) => s.textContent ?? "");
    expect(styles[0]).toContain("<\\/style");
    expect(styles[1]).toContain("@media print {\nbody{display:none}\n}");
    expect(styles[1]).toContain("@media screen and (prefers-color-scheme: dark) {\n@supports (display: grid) {\nhtml{background:#000}\n}\n}");
    expect(styles[1]).toContain("@layer components {\n.btn{color:red}\n}");
  });
});
