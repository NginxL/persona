// Lifts a reply's block pictures (lib/reply-images.ts) out of their paragraphs: each run of lines that hold only web
// pictures becomes one <reply-images> element, which Markdown draws with ReplyImages.tsx (one picture, or squares).
// The lines before and after a run stay paragraphs, cut where they were (a <br> stays a <br>, a soft break a line
// break). Runs that only blank lines kept apart (separate paragraphs) are one. Quotes, code, links and tables are left
// as they are. Loaded with a reply's first picture (reply-images-lazy.ts), so others never carry it.
import type { Element, ElementContent, Root, RootContent } from "hast";
import { isMachineRef } from "../lib/files";
import { isWebImage } from "../lib/reply-images";

export const GROUP_TAG = "reply-images";

type Node = ElementContent;
type PictureKind = "web" | "local";
interface Line {
  nodes: Node[];
  /** What ended the line before this one: a <br>, or a line break in the text. */
  after: "br" | "nl" | null;
}

const imgSrc = (n: Node | RootContent): string =>
  n.type === "element" && n.tagName === "img" && typeof n.properties?.src === "string" ? n.properties.src : "";
const isWebPicture = (n: Node | RootContent): n is Element =>
  n.type === "element" && n.tagName === "img" && isWebImage(imgSrc(n));
const isLocalPicture = (n: Node | RootContent, local: boolean): n is Element =>
  local && n.type === "element" && n.tagName === "img" && isMachineRef(imgSrc(n));
const pictureKind = (n: Node | RootContent, local: boolean): PictureKind | null =>
  isWebPicture(n) ? "web" : isLocalPicture(n, local) ? "local" : null;
const isBlank = (n: Node | RootContent) => n.type === "text" && !n.value.trim();
const lineKind = (l: Line, local: boolean): PictureKind | null => {
  let kind: PictureKind | null = null;
  for (const n of l.nodes) {
    if (isBlank(n)) continue;
    const k = pictureKind(n, local);
    if (!k) return null;
    if (kind && kind !== k) return null;
    kind = k;
  }
  return kind;
};
const emptyLine = (l: Line) => l.nodes.every(isBlank);

/** Lift any local inline image out of a mixed line into its own block line while preserving surrounding text order
 *  (aligned with iOS PR #12102 so inline local image failures use the same feedback + manual retry path). */
function liftLocalInline(ls: Line[], local: boolean): Line[] {
  if (!local) return ls;
  const out: Line[] = [];
  for (const l of ls) {
    if (lineKind(l, true) !== null || !l.nodes.some((n) => isLocalPicture(n, true))) {
      out.push(l);
      continue;
    }
    let cur: Node[] = [];
    let after = l.after;
    const flush = () => {
      if (cur.length && !cur.every(isBlank)) {
        out.push({ nodes: cur, after });
        after = "nl";
      }
      cur = [];
    };
    for (const n of l.nodes) {
      if (isLocalPicture(n, true)) {
        flush();
        out.push({ nodes: [n], after: "nl" });
        after = "nl";
      } else {
        cur.push(n);
      }
    }
    flush();
  }
  return out;
}

/** A paragraph's children cut into lines, at each <br> (with the line break the parser writes after it) and at each
 *  line break inside its text. */
function lines(children: Node[]): Line[] {
  const out: Line[] = [{ nodes: [], after: null }];
  let afterBr = false;
  for (const c of children) {
    if (c.type === "element" && c.tagName === "br") {
      out.push({ nodes: [], after: "br" });
      afterBr = true;
      continue;
    }
    if (c.type === "text" && c.value.includes("\n")) {
      const value = afterBr && c.value.startsWith("\n") ? c.value.slice(1) : c.value;
      value.split("\n").forEach((part, i) => {
        if (i > 0) out.push({ nodes: [], after: "nl" });
        if (part) out[out.length - 1].nodes.push({ type: "text", value: part });
      });
    } else {
      out[out.length - 1].nodes.push(c);
    }
    afterBr = false;
  }
  return out;
}

/** Lines back into a paragraph's children, each cut as it was. */
function join(ls: Line[]): Node[] {
  const out: Node[] = [];
  ls.forEach((l, i) => {
    if (i > 0) {
      if (l.after === "br") out.push({ type: "element", tagName: "br", properties: {}, children: [] }, { type: "text", value: "\n" });
      else out.push({ type: "text", value: "\n" });
    }
    out.push(...l.nodes);
  });
  return out;
}

/** A paragraph with block pictures as paragraphs and runs, or null when it has none. */
function split(p: Element, local: boolean): Element[] | null {
  const ls = liftLocalInline(lines(p.children), local);
  if (!ls.some((l) => lineKind(l, local) !== null)) return null;
  const out: Element[] = [];
  let text: Line[] = [];
  let run: { kind: PictureKind; nodes: Element[] } | null = null;
  const endText = () => {
    while (text.length && emptyLine(text[0])) text.shift();
    while (text.length && emptyLine(text[text.length - 1])) text.pop();
    if (text.length) out.push({ ...p, children: join(text.map((l, i) => (i === 0 ? { ...l, after: null } : l))) });
    text = [];
  };
  const endRun = () => {
    if (run) out.push({ type: "element", tagName: GROUP_TAG, properties: { "data-kind": run.kind }, children: run.nodes });
    run = null;
  };
  for (const l of ls) {
    const kind = lineKind(l, local);
    if (kind !== null) {
      if (!run || run.kind !== kind) {
        endRun();
        endText();
        run = { kind, nodes: [] };
      }
      run.nodes.push(...l.nodes.filter((n): n is Element => pictureKind(n, local) === kind));
    } else if (!(run && emptyLine(l))) {
      endRun();
      text.push(l);
    }
  }
  endRun();
  endText();
  return out;
}

const SKIP = new Set(["blockquote", "pre", "code", "a", "table"]);

function walk(parent: Root | Element, local: boolean): void {
  const kids: (RootContent | Node)[] = [];
  for (const c of parent.children) {
    if (c.type !== "element" || SKIP.has(c.tagName)) {
      kids.push(c);
      continue;
    }
    if (c.tagName === "p") {
      const parts = split(c, local);
      if (parts) {
        kids.push(...parts);
        continue;
      }
    } else {
      walk(c, local);
    }
    kids.push(c);
  }
  // Runs of the same kind only blank text kept apart (pictures in paragraphs of their own) are one.
  const merged: (RootContent | Node)[] = [];
  for (const k of kids) {
    if (k.type === "element" && k.tagName === GROUP_TAG) {
      let j = merged.length - 1;
      while (j >= 0 && isBlank(merged[j])) j--;
      const prev = merged[j];
      if (prev && prev.type === "element" && prev.tagName === GROUP_TAG && prev.properties?.["data-kind"] === k.properties?.["data-kind"]) {
        prev.children.push(...k.children);
        merged.length = j + 1;
        continue;
      }
    }
    merged.push(k);
  }
  parent.children = merged as typeof parent.children;
}

/** The rehype plugin. */
export function rehypeReplyImages(opts?: { local?: boolean }) {
  const local = Boolean(opts?.local);
  return (tree: Root) => walk(tree, local);
}
