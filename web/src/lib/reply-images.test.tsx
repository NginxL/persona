// The rules for pictures in replies (#9475), the same numbers as iOS (PersonaKit ReplyImagesTests), and the step that
// lifts block pictures out of their paragraphs.
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import { describe, expect, it } from "vitest";
import { rehypeReplyImages } from "../ui/reply-images-plugin";
import { grid, picturesIn, remoteSize, single } from "./reply-image-layout";
import { isWebImage, remoteImageURL } from "./reply-images";

describe("lone picture", () => {
  it("is one height and its shape's width, the placeholder 4:3, never larger than itself", () => {
    expect(single(null, 361, 240)).toEqual({ w: 320, h: 240 });
    expect(single({ w: 1080, h: 1440 }, 361, 240)).toEqual({ w: 180, h: 240 });
    expect(single({ w: 1600, h: 1200 }, 361, 240)).toEqual({ w: 320, h: 240 });
    expect(single({ w: 3000, h: 1000 }, 361, 240)).toEqual({ w: 361, h: 240 });
    expect(single({ w: 300, h: 2000 }, 361, 240)).toEqual({ w: 180, h: 240 });
    expect(single({ w: 96, h: 96 }, 361, 240)).toEqual({ w: 96, h: 96 });
    expect(single(null, 680, 300)).toEqual({ w: 400, h: 300 });
  });
});

describe("runs", () => {
  it("are squares, three to a row, at most two rows, the rest counted", () => {
    expect(grid(2, 361)).toEqual({ columns: 2, tile: 178.5, shown: 2, more: 0, rows: 1 });
    expect(grid(2, 700).tile).toBe(238);
    const three = grid(3, 361);
    expect(three.columns).toBe(3);
    expect(three.tile).toBeCloseTo(117.67, 2);
    expect(grid(4, 361)).toMatchObject({ columns: 2, rows: 2, shown: 4, more: 0 });
    expect(grid(8, 361)).toMatchObject({ columns: 3, rows: 2, shown: 6, more: 2 });
  });

  it("ask the hub for a size that covers what is drawn", () => {
    expect(remoteSize(240, 3)).toBe(768);
    expect(remoteSize(400, 2)).toBe(1024);
    expect(remoteSize(5000, 2)).toBe(2048);
  });
});

describe("addresses", () => {
  it("are web pictures only when absolute http or https", () => {
    expect(isWebImage("https://img.example.com/a.jpg")).toBe(true);
    expect(isWebImage("HTTP://img.example.com/a.jpg")).toBe(true);
    for (const src of ["", "/api/v1/x", "//img.example.com/a.jpg", "data:image/png;base64,AA", "shot.png", "file:///a.png"]) expect(isWebImage(src)).toBe(false);
  });

  it("go to the hub escaped, plus signs and all", () => {
    expect(remoteImageURL("https://img.test/a+b.jpg?x=1&y=2#f", 768, "t k")).toBe(
      "/api/v1/remote-image?url=https%3A%2F%2Fimg.test%2Fa%2Bb.jpg%3Fx%3D1%26y%3D2%23f&size=768&token=t%20k",
    );
  });

  it("are listed in order outside code and quotes", () => {
    const md = "a ![one](https://x.test/1.png)\n```\n![no](https://x.test/n.png)\n```\n> ![no](https://x.test/q.png)\n`![no](https://x.test/c.png)`\n![two](<https://x.test/2.png> \"t\")\n![local](shot.png)";
    expect(picturesIn(md)).toEqual([
      { src: "https://x.test/1.png", alt: "one" },
      { src: "https://x.test/2.png", alt: "two" },
    ]);
  });
});

// React's static renderer puts a preload link before the first image: not the plugin's.
const html = (md: string, local = false) =>
  renderToStaticMarkup(<ReactMarkdown rehypePlugins={[[rehypeReplyImages, { local }]]}>{md}</ReactMarkdown>).replace(/<link [^>]*\/>/g, "");
const groups = (md: string, local = false) =>
  [...html(md, local).matchAll(/<reply-images data-kind="(web|local)"[^>]*>(.*?)<\/reply-images>/g)].map((m) => ({
    kind: m[1],
    pictures: [...m[2].matchAll(/src="([^"]+)"/g)].map((s) => s[1]),
  }));

describe("lifting block pictures", () => {
  it("takes the pictures under a line of text out of its paragraph, one run", () => {
    expect(html("**Kitchen** (same post)\n![a](https://x.test/a.png)\n![b](https://x.test/b.png)\nIt has a washer.")).toBe(
      '<p><strong>Kitchen</strong> (same post)</p><reply-images data-kind="web"><img src="https://x.test/a.png" alt="a"/><img src="https://x.test/b.png" alt="b"/></reply-images><p>It has a washer.</p>',
    );
  });

  it("makes one run of pictures that only blank lines keep apart, and of two on one line", () => {
    expect(groups("![a](https://x.test/a.png)\n\n![b](https://x.test/b.png) ![c](https://x.test/c.png)\n\ntext\n\n![d](https://x.test/d.png)")).toEqual([
      { kind: "web", pictures: ["https://x.test/a.png", "https://x.test/b.png", "https://x.test/c.png"] },
      { kind: "web", pictures: ["https://x.test/d.png"] },
    ]);
  });

  it("keeps local and web pictures in separate labelled runs", () => {
    expect(groups("![a](shot.png)\n\n![b](https://x.test/b.png)\n\n![c](other.png)", true)).toEqual([
      { kind: "local", pictures: ["shot.png"] },
      { kind: "web", pictures: ["https://x.test/b.png"] },
      { kind: "local", pictures: ["other.png"] },
    ]);
  });

  it("leaves pictures in a line of text, links, quotes, code and other addresses where they are", () => {
    for (const md of [
      "an icon ![i](https://x.test/i.png) in a line",
      "[![a](https://x.test/a.png)](https://x.test/page)",
      "> ![a](https://x.test/a.png)",
      "```\n![a](https://x.test/a.png)\n```",
      "![shot](shot.png)",
    ]) expect(groups(md)).toEqual([]);
  });

  it("keeps a hard break a hard break around a run", () => {
    const out = html("one  \ntwo\n![a](https://x.test/a.png)\nthree  \nfour");
    expect(out).toBe('<p>one<br/>\ntwo</p><reply-images data-kind="web"><img src="https://x.test/a.png" alt="a"/></reply-images><p>three<br/>\nfour</p>');
  });
});
