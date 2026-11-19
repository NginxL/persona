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
});
