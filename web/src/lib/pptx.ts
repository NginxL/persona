// A small PowerPoint reader and renderer for the web's file viewer (docs/web/web.md "Office", #1345): slides with their
// text (sizes, bold / italic / underline, colors, alignment, bullets), shapes (fill, outline, rectangle / rounded /
// ellipse / line), pictures, tables, groups, backgrounds, placeholders that take their place and type size from the
// layout and master, and speaker notes. Charts and SmartArt show as a labeled box (LibreOffice's PDF draws them).
// Everything it emits is escaped text and values it checked (colors, numbers, data: URLs it made), and the viewer
// puts it in a script-less sandboxed frame anyway. Pure: the XML parser is passed in (DOMParser in the browser).
import type JSZip from "jszip";
import { t } from "../i18n";

export const EMU_PER_PX = 9525;

export interface Run {
  text: string;
  size?: number; // pt
  bold?: boolean;
  italic?: boolean;
  underline?: boolean;
  strike?: boolean;
  color?: string; // #rrggbb
  br?: boolean; // a line break, not text
}

export interface Para {
  runs: Run[];
  align?: "l" | "ctr" | "r" | "just";
  level: number;
  bullet?: string;
}

export interface Cell {
  paras: Para[];
  fill?: string;
  span?: number;
  rowSpan?: number;
  hidden?: boolean; // covered by a merge
}

export interface Shape {
  x: number;
  y: number;
  w: number;
  h: number; // px
  rot?: number;
  flipH?: boolean;
  flipV?: boolean;
  geom?: "rect" | "roundRect" | "ellipse" | "line" | "triangle";
  fill?: string;
  line?: { color: string; width: number };
  paras?: Para[];
  anchor?: "t" | "ctr" | "b";
  inset?: [number, number, number, number]; // l t r b, px
  vertical?: boolean;
  image?: string; // data: URL
  table?: { cols: number[]; rows: { h: number; cells: Cell[] }[] };
  chart?: Chart;
  placeholder?: string; // what a chart / diagram / unknown picture is, shown as a labeled box
}

export interface ChartSeries {
  name: string;
  values: (number | null)[];
  color: string;
  pointColors?: string[]; // pies: one color per slice
}

/** A chart this viewer draws itself (bar / column / line / area / pie / doughnut), read from the chart part's caches. */
export interface Chart {
  kind: "col" | "bar" | "line" | "area" | "pie" | "doughnut";
  title?: string;
  cats: string[];
  series: ChartSeries[];
  stacked?: boolean;
}

export interface Slide {
  bg?: string;
  bgImage?: string;
  shapes: Shape[];
  notes?: string;
  hidden?: boolean;
}

export interface Deck {
  width: number;
  height: number;
  slides: Slide[];
}

type ParseXML = (text: string) => Document;

// ── XML helpers (work on DOMParser and @xmldom/xmldom documents alike) ─────

const kids = (el: Element | null | undefined, name?: string): Element[] => {
  const out: Element[] = [];
  if (!el) return out;
  for (let n = el.firstChild; n; n = n.nextSibling) if (n.nodeType === 1 && (!name || (n as Element).localName === name)) out.push(n as Element);
  return out;
};
const kid = (el: Element | null | undefined, name: string): Element | null => kids(el, name)[0] ?? null;
const path = (el: Element | null | undefined, ...names: string[]): Element | null => {
  let cur: Element | null | undefined = el;
  for (const n of names) cur = kid(cur, n);
  return cur ?? null;
};
const attr = (el: Element | null | undefined, name: string): string | null => (el && el.hasAttribute(name) ? el.getAttribute(name) : null);
const num = (el: Element | null | undefined, name: string): number | undefined => {
  const v = attr(el, name);
  if (v === null) return undefined;
  const n = Number(v);
  return Number.isFinite(n) ? n : undefined;
};
const bool = (el: Element | null | undefined, name: string): boolean | undefined => {
  const v = attr(el, name);
  return v === null ? undefined : v === "1" || v === "true";
};

/** Resolves a relationship target against the part that names it: ("ppt/slides/slide1.xml", "../media/a.png"). */
export function resolvePart(from: string, target: string): string {
  if (target.startsWith("/")) return target.slice(1);
  const parts = from.split("/").slice(0, -1);
  for (const seg of target.split("/")) {
    if (seg === "..") parts.pop();
    else if (seg !== ".") parts.push(seg);
  }
  return parts.join("/");
}

const relsPath = (part: string) => {
  const i = part.lastIndexOf("/");
  return `${part.slice(0, i)}/_rels/${part.slice(i + 1)}.rels`;
};

// ── colors ────────────────────────────────────────────────

type Scheme = Record<string, string>;

const SCHEME_ALIAS: Record<string, string> = { tx1: "dk1", bg1: "lt1", tx2: "dk2", bg2: "lt2" };

const hex6 = (v: string | null) => (v && /^[0-9a-fA-F]{6}$/.test(v) ? `#${v.toLowerCase()}` : undefined);

function adjust(color: string, el: Element): string {
  let [r, g, b] = [1, 3, 5].map((i) => parseInt(color.slice(i, i + 2), 16) / 255);
  for (const m of kids(el)) {
    const v = (num(m, "val") ?? 100000) / 100000;
    if (m.localName === "lumMod" || m.localName === "lumOff") {
      // in HSL: lightness × lumMod + lumOff
      const max = Math.max(r, g, b);
      const min = Math.min(r, g, b);
      let l = (max + min) / 2;
      const d = max - min;
      const s = d === 0 ? 0 : d / (1 - Math.abs(2 * l - 1));
      let h = 0;
      if (d) h = max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
      l = m.localName === "lumMod" ? l * v : Math.min(1, l + v);
      const c = (1 - Math.abs(2 * l - 1)) * s;
      const x = c * (1 - Math.abs(((h % 6) + 6) % 2 - 1));
      const k = l - c / 2;
      const hh = ((h % 6) + 6) % 6;
      [r, g, b] = hh < 1 ? [c, x, 0] : hh < 2 ? [x, c, 0] : hh < 3 ? [0, c, x] : hh < 4 ? [0, x, c] : hh < 5 ? [x, 0, c] : [c, 0, x];
      [r, g, b] = [r + k, g + k, b + k];
    } else if (m.localName === "tint") [r, g, b] = [r, g, b].map((c) => c + (1 - c) * (1 - v));
    else if (m.localName === "shade") [r, g, b] = [r, g, b].map((c) => c * v);
  }
  return "#" + [r, g, b].map((c) => Math.round(Math.max(0, Math.min(1, c)) * 255).toString(16).padStart(2, "0")).join("");
}

/** The color a fill-like element names (srgbClr, schemeClr, sysClr, prstClr), with its tints and shades. */
export function colorOf(el: Element | null | undefined, scheme: Scheme, placeholderColor?: string): string | undefined {
  if (!el) return undefined;
  for (const c of kids(el)) {
    let base: string | undefined;
    if (c.localName === "srgbClr") base = hex6(attr(c, "val"));
    else if (c.localName === "sysClr") base = hex6(attr(c, "lastClr")) ?? (attr(c, "val") === "window" ? "#ffffff" : "#000000");
    else if (c.localName === "schemeClr") {
      const v = attr(c, "val") ?? "";
      base = v === "phClr" ? placeholderColor : scheme[SCHEME_ALIAS[v] ?? v];
    } else if (c.localName === "prstClr") base = PRESET[attr(c, "val") ?? ""];
    if (base) return adjust(base, c);
  }
  return undefined;
}

const PRESET: Record<string, string> = { black: "#000000", white: "#ffffff", red: "#ff0000", green: "#008000", blue: "#0000ff", yellow: "#ffff00", gray: "#808080" };

function readScheme(theme: Document | null): Scheme {
  const out: Scheme = { dk1: "#000000", lt1: "#ffffff", dk2: "#44546a", lt2: "#e7e6e6", accent1: "#4472c4", accent2: "#ed7d31", accent3: "#a5a5a5", accent4: "#ffc000", accent5: "#5b9bd5", accent6: "#70ad47", hlink: "#0563c1", folHlink: "#954f72" };
  const cs = theme ? path(theme.documentElement, "themeElements", "clrScheme") : null;
  for (const c of kids(cs)) {
    const v = colorOf(c, {});
    if (v) out[c.localName] = v;
  }
  return out;
}

// ── parts of the deck ─────────────────────────────────────

interface Part {
  name: string;
  doc: Document;
  rels: Record<string, { target: string; type: string; external: boolean }>;
}

interface Layered {
  slide: Part;
  layout: Part | null;
  master: Part | null;
}

const MIME: Record<string, string> = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", bmp: "image/bmp", webp: "image/webp", svg: "image/svg+xml", tif: "image/tiff", tiff: "image/tiff" };

class Reader {
  images = new Map<string, string | null>();
  scheme: Scheme = {};
  constructor(
    private zip: JSZip,
    private parse: ParseXML,
  ) {}

  async xml(name: string): Promise<Document | null> {
    const f = this.zip.file(name);
    return f ? this.parse(await f.async("string")) : null;
  }

  async part(name: string): Promise<Part | null> {
    const doc = await this.xml(name);
    if (!doc) return null;
    const rels: Part["rels"] = {};
    const r = await this.xml(relsPath(name));
    for (const rel of kids(r?.documentElement, "Relationship")) {
      const id = attr(rel, "Id");
      const target = attr(rel, "Target");
      if (id && target) {
        const external = attr(rel, "TargetMode") === "External";
        rels[id] = { target: external ? target : resolvePart(name, target), type: attr(rel, "Type") ?? "", external };
      }
    }
    return { name, doc, rels };
  }

  /** A picture a relationship points at, as a data: URL; null for what a browser cannot show (EMF / WMF) or links. */
  async image(p: Part, rid: string | null): Promise<string | null> {
    const rel = rid ? p.rels[rid] : undefined;
    if (!rel || rel.external) return null;
    if (this.images.has(rel.target)) return this.images.get(rel.target)!;
    const ext = rel.target.split(".").pop()!.toLowerCase();
    const mime = MIME[ext];
    const f = this.zip.file(rel.target);
    const out = mime && f ? `data:${mime};base64,${await f.async("base64")}` : null;
    this.images.set(rel.target, out);
    return out;
  }
}

const relOfType = (p: Part, type: string) => Object.values(p.rels).find((r) => r.type.endsWith("/" + type));

/** Reads a .pptx (already unzipped) into slides. */
export async function readPptx(zip: JSZip, parse: ParseXML): Promise<Deck> {
  const rd = new Reader(zip, parse);
  const pres = await rd.part("ppt/presentation.xml");
  if (!pres) throw new Error(t("lib-pptx.invalid"));
  const size = kid(pres.doc.documentElement, "sldSz");
  const width = Math.round((num(size, "cx") ?? 9144000) / EMU_PER_PX);
  const height = Math.round((num(size, "cy") ?? 6858000) / EMU_PER_PX);
  const masters = new Map<string, Part | null>();
  const layouts = new Map<string, Part | null>();
  const slides: Slide[] = [];
  for (const id of kids(kid(pres.doc.documentElement, "sldIdLst"), "sldId")) {
    const rid = attr(id, "r:id") ?? attr(id, "id");
    const rel = rid ? pres.rels[rid] : undefined;
    if (!rel) continue;
    const slide = await rd.part(rel.target);
    if (!slide) continue;
    const lrel = relOfType(slide, "slideLayout");
    if (lrel && !layouts.has(lrel.target)) layouts.set(lrel.target, await rd.part(lrel.target));
    const layout = lrel ? (layouts.get(lrel.target) ?? null) : null;
    const mrel = layout ? relOfType(layout, "slideMaster") : undefined;
    if (mrel && !masters.has(mrel.target)) {
      const master = await rd.part(mrel.target);
      masters.set(mrel.target, master);
      const trel = master ? relOfType(master, "theme") : undefined;
      if (trel && !Object.keys(rd.scheme).length) rd.scheme = readScheme(await rd.xml(trel.target));
    }
    if (!Object.keys(rd.scheme).length) rd.scheme = readScheme(null);
    const master = mrel ? (masters.get(mrel.target) ?? null) : null;
    slides.push(await readSlide(rd, { slide, layout, master }));
  }
  return { width, height, slides };
}

// ── one slide ─────────────────────────────────────────────

interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
  rot?: number;
  flipH?: boolean;
  flipV?: boolean;
}

function xfrmOf(spPr: Element | null): Box | null {
  const x = kid(spPr, "xfrm");
  const off = kid(x, "off");
  const ext = kid(x, "ext");
  if (!off || !ext) return null;
  return {
    x: (num(off, "x") ?? 0) / EMU_PER_PX,
    y: (num(off, "y") ?? 0) / EMU_PER_PX,
    w: (num(ext, "cx") ?? 0) / EMU_PER_PX,
    h: (num(ext, "cy") ?? 0) / EMU_PER_PX,
    rot: (num(x, "rot") ?? 0) / 60000 || undefined,
    flipH: bool(x, "flipH"),
    flipV: bool(x, "flipV"),
  };
}

/** A placeholder's key: its type (body when unnamed) and index. */
function phOf(sp: Element): { type: string; idx: string | null } | null {
  const nv = kids(sp).find((k) => k.localName.startsWith("nv"));
  const ph = path(nv, "nvPr", "ph");
  if (!ph) return null;
  return { type: attr(ph, "type") ?? "body", idx: attr(ph, "idx") };
}

/** The shape in a layout or master that a slide's placeholder takes after: same index, else same type. */
function findPh(p: Part | null, ph: { type: string; idx: string | null }): Element | null {
  if (!p) return null;
  const all = kids(path(p.doc.documentElement, "cSld", "spTree")).filter((s) => s.localName === "sp");
  const same = (t: string) => (t === "ctrTitle" ? "title" : t === "subTitle" || t === "obj" ? "body" : t);
  return (
    (ph.idx !== null ? all.find((s) => phOf(s)?.idx === ph.idx) : undefined) ??
    all.find((s) => phOf(s)?.type === ph.type) ??
    all.find((s) => same(phOf(s)?.type ?? "") === same(ph.type)) ??
    null
  );
}

interface TextDefaults {
  size?: number;
  bold?: boolean;
  italic?: boolean;
  color?: string;
  align?: Para["align"];
  bullet?: string | null; // null: none
}

/** Paragraph defaults at a level, from one lstStyle-like element (lvl1pPr …). */
function levelDefaults(list: Element | null, level: number, scheme: Scheme): TextDefaults {
  const pPr = kid(list, `lvl${level + 1}pPr`);
  if (!pPr) return {};
  const d: TextDefaults = {};
  const algn = attr(pPr, "algn");
  if (algn === "l" || algn === "ctr" || algn === "r" || algn === "just") d.align = algn;
  const rPr = kid(pPr, "defRPr");
  if (num(rPr, "sz")) d.size = num(rPr, "sz")! / 100;
  if (bool(rPr, "b") !== undefined) d.bold = bool(rPr, "b");
  if (bool(rPr, "i") !== undefined) d.italic = bool(rPr, "i");
  const c = colorOf(kid(rPr, "solidFill"), scheme);
  if (c) d.color = c;
  if (kid(pPr, "buNone")) d.bullet = null;
  else if (kid(pPr, "buChar")) d.bullet = attr(kid(pPr, "buChar"), "char") ?? "•";
  else if (kid(pPr, "buAutoNum")) d.bullet = "#";
  return d;
}

function merge(chain: TextDefaults[]): TextDefaults {
  const out: TextDefaults = {};
  for (const d of chain) for (const [k, v] of Object.entries(d)) if (v !== undefined) (out as Record<string, unknown>)[k] = v;
  return out;
}

async function readSlide(rd: Reader, parts: Layered): Promise<Slide> {
  const root = parts.slide.doc.documentElement;
  const out: Slide = { shapes: [], hidden: attr(root, "show") === "0" || undefined };
  // background: the slide's, else the layout's, else the master's
  for (const p of [parts.slide, parts.layout, parts.master]) {
    if (!p) continue;
    const bgPr = path(p.doc.documentElement, "cSld", "bg", "bgPr");
    const bgRef = path(p.doc.documentElement, "cSld", "bg", "bgRef");
    if (bgPr) {
      const blip = path(bgPr, "blipFill", "blip");
      if (blip) out.bgImage = (await rd.image(p, attr(blip, "r:embed"))) ?? undefined;
      out.bg = colorOf(kid(bgPr, "solidFill"), rd.scheme) ?? colorOf(path(bgPr, "gradFill", "gsLst", "gs"), rd.scheme);
      break;
    }
    if (bgRef) {
      out.bg = colorOf(bgRef, rd.scheme);
      break;
    }
  }
  // the master's and layout's own drawings (logos, bars) under the slide's, unless the slide hides them
  const showMaster = (p: Part | null) => attr(p?.doc.documentElement, "showMasterSp") !== "0";
  if (parts.master && showMaster(parts.slide) && showMaster(parts.layout)) await walk(rd, parts.master, { ...parts, slide: parts.master, layout: null, master: null }, path(parts.master.doc.documentElement, "cSld", "spTree"), out.shapes, null, true);
  if (parts.layout && showMaster(parts.slide)) await walk(rd, parts.layout, { slide: parts.layout, layout: null, master: parts.master }, path(parts.layout.doc.documentElement, "cSld", "spTree"), out.shapes, null, true);
  await walk(rd, parts.slide, parts, path(root, "cSld", "spTree"), out.shapes, null, false);
  // speaker notes
  const nrel = relOfType(parts.slide, "notesSlide");
  if (nrel) {
    const notes = await rd.xml(nrel.target);
    const body = kids(path(notes?.documentElement, "cSld", "spTree"), "sp").find((s) => phOf(s)?.type === "body");
    const text = kids(kid(body, "txBody"), "p")
      .map((p) => textOf(p))
      .join("\n")
      .trim();
    if (text) out.notes = text;
  }
  return out;
}

const textOf = (p: Element): string =>
  kids(p)
    .map((r) => (r.localName === "r" || r.localName === "fld" ? (kid(r, "t")?.textContent ?? "") : r.localName === "br" ? "\n" : ""))
    .join("");

type Group = { off: [number, number]; ext: [number, number]; chOff: [number, number]; chExt: [number, number] } | null;

function place(b: Box, g: Group): Box {
  if (!g) return b;
  const sx = g.chExt[0] ? g.ext[0] / g.chExt[0] : 1;
  const sy = g.chExt[1] ? g.ext[1] / g.chExt[1] : 1;
  return { ...b, x: g.off[0] + (b.x - g.chOff[0]) * sx, y: g.off[1] + (b.y - g.chOff[1]) * sy, w: b.w * sx, h: b.h * sy };
}

/** The shapes of a tree (a slide's, a group's), in drawing order. Layout / master trees skip their placeholders. */
async function walk(rd: Reader, p: Part, parts: Layered, tree: Element | null, out: Shape[], group: Group, isTemplate: boolean): Promise<void> {
  for (const el of kids(tree)) {
    const name = el.localName;
    if (name === "grpSp") {
      const x = path(el, "grpSpPr", "xfrm");
      const v = (k: string, a: string, b: string): [number, number] => [(num(kid(x, k), a) ?? 0) / EMU_PER_PX, (num(kid(x, k), b) ?? 0) / EMU_PER_PX];
      const own: Group = { off: v("off", "x", "y"), ext: v("ext", "cx", "cy"), chOff: v("chOff", "x", "y"), chExt: v("chExt", "cx", "cy") };
      // nested groups: the inner group's frame is in the outer group's child space
      const placed = place({ x: own.off[0], y: own.off[1], w: own.ext[0], h: own.ext[1] }, group);
      await walk(rd, p, parts, el, out, { ...own, off: [placed.x, placed.y], ext: [placed.w, placed.h] }, isTemplate);
      continue;
    }
    if (isTemplate && (name === "sp" || name === "pic") && phOf(el)) continue;
    let shape: Shape | null = null;
    if (name === "sp" || name === "cxnSp") shape = await readShape(rd, p, parts, el, name === "cxnSp");
    else if (name === "pic") shape = await readPicture(rd, p, el);
    else if (name === "graphicFrame") shape = await readFrame(rd, p, el);
    if (shape) {
      const b = place(shape, group);
      out.push({ ...shape, ...b });
    }
  }
}

async function readShape(rd: Reader, p: Part, parts: Layered, sp: Element, connector: boolean): Promise<Shape | null> {
  const spPr = kid(sp, "spPr");
  const ph = phOf(sp);
  const fromLayout = ph ? findPh(parts.layout, ph) : null;
  const fromMaster = ph ? findPh(parts.master, ph) : null;
  const box = xfrmOf(spPr) ?? xfrmOf(kid(fromLayout, "spPr")) ?? xfrmOf(kid(fromMaster, "spPr"));
  if (!box) return null;
  const shape: Shape = { ...box };
  const prst = attr(kid(spPr, "prstGeom"), "prst");
  shape.geom = connector || prst === "line" || prst?.startsWith("straightConnector") ? "line" : prst === "ellipse" ? "ellipse" : prst === "roundRect" ? "roundRect" : prst === "triangle" ? "triangle" : "rect";
  // fill and outline: the shape's own, else its style's references to the theme
  const style = kid(sp, "style");
  if (kid(spPr, "noFill")) shape.fill = undefined;
  else {
    shape.fill = colorOf(kid(spPr, "solidFill"), rd.scheme) ?? colorOf(path(spPr, "gradFill", "gsLst", "gs"), rd.scheme);
    const blip = path(spPr, "blipFill", "blip");
    if (blip) shape.image = (await rd.image(p, attr(blip, "r:embed"))) ?? undefined;
    if (!shape.fill && !blip && style && (num(kid(style, "fillRef"), "idx") ?? 0) > 0) shape.fill = colorOf(kid(style, "fillRef"), rd.scheme);
  }
  const ln = kid(spPr, "ln");
  if (!kid(ln, "noFill")) {
    const color = colorOf(kid(ln, "solidFill"), rd.scheme) ?? (style && (num(kid(style, "lnRef"), "idx") ?? 0) > 0 ? colorOf(kid(style, "lnRef"), rd.scheme) : undefined);
    if (color) shape.line = { color, width: Math.max(0.75, (num(ln, "w") ?? 12700) / EMU_PER_PX) };
  }
  // text, with defaults from the placeholder chain and the master's text styles
  const body = kid(sp, "txBody");
  if (body) {
    const bodyPr = kid(body, "bodyPr");
    const inheritedPr = kid(kid(fromLayout, "txBody"), "bodyPr") ?? kid(kid(fromMaster, "txBody"), "bodyPr");
    const anchor = attr(bodyPr, "anchor") ?? attr(inheritedPr, "anchor");
    shape.anchor = anchor === "ctr" ? "ctr" : anchor === "b" ? "b" : "t";
    const ins = (k: string, d: number) => (num(bodyPr, k) ?? num(inheritedPr, k) ?? d) / EMU_PER_PX;
    shape.inset = [ins("lIns", 91440), ins("tIns", 45720), ins("rIns", 91440), ins("bIns", 45720)];
    if ((attr(bodyPr, "vert") ?? "horz").startsWith("vert")) shape.vertical = true;
    const scale = (num(kid(bodyPr, "normAutofit"), "fontScale") ?? 100000) / 100000;
    const txStyles = kid(parts.master?.doc.documentElement, "txStyles");
    const masterStyle = !ph ? kid(txStyles, "otherStyle") : ph.type === "title" || ph.type === "ctrTitle" ? kid(txStyles, "titleStyle") : kid(txStyles, "bodyStyle");
    const lists = [masterStyle, kid(kid(fromMaster, "txBody"), "lstStyle"), kid(kid(fromLayout, "txBody"), "lstStyle"), kid(body, "lstStyle")];
    const fontColor = style ? colorOf(kid(style, "fontRef"), rd.scheme) : undefined;
    shape.paras = kids(body, "p").map((pEl) => readPara(pEl, lists, rd.scheme, scale, fontColor, !!ph));
    if (shape.paras.every((q) => q.runs.every((r) => r.br || !r.text))) shape.paras = undefined;
  }
  if (!shape.fill && !shape.line && !shape.paras && !shape.image) return null;
  return shape;
}

function readPara(pEl: Element, lists: (Element | null)[], scheme: Scheme, scale: number, fontColor: string | undefined, inPlaceholder: boolean): Para {
  const pPr = kid(pEl, "pPr");
  const level = Math.min(8, num(pPr, "lvl") ?? 0);
  const own: TextDefaults = {};
  const algn = attr(pPr, "algn");
  if (algn === "l" || algn === "ctr" || algn === "r" || algn === "just") own.align = algn;
  if (kid(pPr, "buNone")) own.bullet = null;
  else if (kid(pPr, "buChar")) own.bullet = attr(kid(pPr, "buChar"), "char") ?? "•";
  else if (kid(pPr, "buAutoNum")) own.bullet = "#";
  const d = merge([{ size: 18, color: fontColor }, ...lists.map((l) => levelDefaults(l, level, scheme)), own]);
  const runs: Run[] = [];
  for (const r of kids(pEl)) {
    if (r.localName === "br") {
      runs.push({ text: "", br: true });
      continue;
    }
    if (r.localName !== "r" && r.localName !== "fld") continue;
    const rPr = kid(r, "rPr");
    const sz = num(rPr, "sz");
    const u = attr(rPr, "u");
    runs.push({
      text: kid(r, "t")?.textContent ?? "",
      size: Math.round((sz ? sz / 100 : (d.size ?? 18)) * scale * 10) / 10,
      bold: bool(rPr, "b") ?? d.bold,
      italic: bool(rPr, "i") ?? d.italic,
      underline: !!u && u !== "none",
      strike: !!attr(rPr, "strike") && attr(rPr, "strike") !== "noStrike",
      color: colorOf(kid(rPr, "solidFill"), scheme) ?? d.color,
    });
  }
  // text-less paragraphs keep their line height
  if (!runs.length) runs.push({ text: "", size: Math.round((num(kid(pEl, "endParaRPr"), "sz") ?? (d.size ?? 18) * 100) / 100 * scale * 10) / 10 });
  const hasText = runs.some((r) => r.text);
  const bullet = hasText && d.bullet && (inPlaceholder || own.bullet) ? d.bullet : undefined;
  return { runs, align: d.align, level, bullet };
}

async function readPicture(rd: Reader, p: Part, pic: Element): Promise<Shape | null> {
  const box = xfrmOf(kid(pic, "spPr"));
  if (!box) return null;
  const blip = path(pic, "blipFill", "blip");
  const image = await rd.image(p, attr(blip, "r:embed"));
  return image ? { ...box, image } : { ...box, placeholder: t("lib-pptx.image") };
}

async function readFrame(rd: Reader, p: Part, frame: Element): Promise<Shape | null> {
  const x = kid(frame, "xfrm");
  const off = kid(x, "off");
  const ext = kid(x, "ext");
  if (!off || !ext) return null;
  const box = { x: (num(off, "x") ?? 0) / EMU_PER_PX, y: (num(off, "y") ?? 0) / EMU_PER_PX, w: (num(ext, "cx") ?? 0) / EMU_PER_PX, h: (num(ext, "cy") ?? 0) / EMU_PER_PX };
  const data = path(frame, "graphic", "graphicData");
  const uri = attr(data, "uri") ?? "";
  const tbl = kid(data, "tbl");
  if (tbl) {
    const cols = kids(kid(tbl, "tblGrid"), "gridCol").map((c) => (num(c, "w") ?? 0) / EMU_PER_PX);
    const rows = kids(tbl, "tr").map((tr) => ({
      h: (num(tr, "h") ?? 0) / EMU_PER_PX,
      cells: kids(tr, "tc").map((tc) => {
        const tcPr = kid(tc, "tcPr");
        return {
          paras: kids(kid(tc, "txBody"), "p").map((pEl) => readPara(pEl, [], rd.scheme, 1, undefined, false)),
          fill: colorOf(kid(tcPr, "solidFill"), rd.scheme),
          span: num(tc, "gridSpan"),
          rowSpan: num(tc, "rowSpan"),
          hidden: bool(tc, "hMerge") || bool(tc, "vMerge") || undefined,
        };
      }),
    }));
    return { ...box, table: { cols, rows } };
  }
  if (uri.endsWith("/chart")) {
    const rel = p.rels[attr(kid(data, "chart"), "r:id") ?? ""];
    const doc = rel && !rel.external ? await rd.xml(rel.target) : null;
    const chart = doc ? readChart(doc, rd.scheme) : null;
    return chart ? { ...box, chart } : { ...box, placeholder: CHART_UNSUPPORTED };
  }
  return { ...box, placeholder: uri.endsWith("/diagram") ? t("lib-pptx.smartart_unsupported") : t("lib-pptx.object_unsupported") };
}

export const CHART_UNSUPPORTED = t("lib-pptx.chart_unsupported");

const ACCENTS = ["accent1", "accent2", "accent3", "accent4", "accent5", "accent6"];

/** Cached points of a chart reference (strRef / numRef / strLit / numLit), by index. */
function points(el: Element | null): string[] {
  const cache = kid(kid(el, "strRef"), "strCache") ?? kid(kid(el, "numRef"), "numCache") ?? kid(el, "strLit") ?? kid(el, "numLit") ?? kid(kid(el, "multiLvlStrRef"), "multiLvlStrCache");
  if (!cache) return [];
  const out: string[] = [];
  const count = Math.min(10000, Math.max(0, Math.trunc(num(kid(cache, "ptCount"), "val") ?? 0)));
  for (let i = 0; i < count; i++) out.push("");
  const pts = kid(cache, "lvl") ? kids(kid(cache, "lvl"), "pt") : kids(cache, "pt");
  for (const pt of pts) {
    const raw = num(pt, "idx");
    const i = raw === undefined ? out.length : Math.trunc(raw);
    if (i >= 0 && i < 10000) {
      while (out.length <= i) out.push("");
      out[i] = kid(pt, "v")?.textContent ?? "";
    }
  }
  return out;
}

/** The chart in a chart part, if it is one this viewer draws; null for the rest (scatter, radar, combinations…). */
export function readChart(doc: Document, scheme: Scheme): Chart | null {
  const chartEl = kid(doc.documentElement, "chart");
  const plot = kid(chartEl, "plotArea");
  const types = kids(plot).filter((k) => k.localName.endsWith("Chart"));
  if (types.length !== 1) return null; // none, or a combination
  const chartType = types[0];
  const kind: Chart["kind"] | null = /^bar(3D)?Chart$/.test(chartType.localName)
    ? attr(kid(chartType, "barDir"), "val") === "bar"
      ? "bar"
      : "col"
    : /^line(3D)?Chart$/.test(chartType.localName)
      ? "line"
      : /^area(3D)?Chart$/.test(chartType.localName)
        ? "area"
        : /^(pie|pie3D|ofPie)Chart$/.test(chartType.localName)
          ? "pie"
          : chartType.localName === "doughnutChart"
            ? "doughnut"
            : null;
  if (!kind) return null;
  const sers = kids(chartType, "ser");
  if (!sers.length) return null;
  let cats: string[] = [];
  const series: ChartSeries[] = sers.map((ser, i) => {
    const tx = kid(ser, "tx");
    const name = points(tx)[0] ?? kid(tx, "v")?.textContent ?? t("lib-pptx.series", { n: i + 1 });
    const c = points(kid(ser, "cat"));
    if (c.length > cats.length) cats = c;
    const values = points(kid(ser, "val")).map((v) => (v === "" || !Number.isFinite(Number(v)) ? null : Number(v)));
    const color = colorOf(kid(kid(ser, "spPr"), "solidFill"), scheme) ?? colorOf(path(ser, "spPr", "ln", "solidFill"), scheme) ?? scheme[ACCENTS[i % 6]] ?? "#4472c4";
    const out: ChartSeries = { name, values, color };
    if (kind === "pie" || kind === "doughnut") {
      out.pointColors = values.map((_, j) => scheme[ACCENTS[j % 6]] ?? "#4472c4");
      for (const dPt of kids(ser, "dPt")) {
        const j = num(kid(dPt, "idx"), "val");
        const pc = colorOf(path(dPt, "spPr", "solidFill"), scheme);
        if (j !== undefined && Number.isInteger(j) && j >= 0 && pc && j < out.pointColors.length) out.pointColors[j] = pc;
      }
    }
    return out;
  });
  const n = Math.max(cats.length, ...series.map((x) => x.values.length));
  while (cats.length < n) cats.push(String(cats.length + 1));
  const rich = path(chartEl, "title", "tx", "rich");
  let title = rich
    ? kids(rich, "p")
        .map((q) => textOf(q))
        .join(" ")
        .trim()
    : undefined;
  if (!title && kid(chartEl, "title") && series.length === 1 && attr(kid(chartEl, "autoTitleDeleted"), "val") !== "1") title = series[0].name;
  const grouping = attr(kid(chartType, "grouping"), "val") ?? "";
  return { kind, title: title || undefined, cats, series, stacked: grouping === "stacked" || grouping === "percentStacked" || undefined };
}

// ── HTML ──────────────────────────────────────────────────

export const escapeHTML = (s: string) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
const px = (n: number) => `${Math.round(n * 100) / 100}px`;
const safeColor = (c: string | undefined) => (c && /^#[0-9a-f]{6}$/.test(c) ? c : undefined);
const safeImage = (s: string | undefined) => (s && /^data:image\/(png|jpeg|gif|bmp|webp|tiff|svg\+xml);base64,[A-Za-z0-9+/=]+$/.test(s) ? s : undefined);

function runsHTML(p: Para, n: number): string {
  const first = p.runs.find((r) => !r.br);
  const bullet = p.bullet ? `<span class="b"${first?.size ? ` style="font-size:${px((first.size * 96) / 72)}"` : ""}>${escapeHTML(p.bullet === "#" ? `${n}.` : p.bullet)}</span>` : "";
  const runs = p.runs
    .map((r) => {
      if (r.br) return "<br>";
      const st = [
        r.size ? `font-size:${px((r.size * 96) / 72)}` : "",
        r.bold ? "font-weight:700" : "",
        r.italic ? "font-style:italic" : "",
        r.underline || r.strike ? `text-decoration:${[r.underline ? "underline" : "", r.strike ? "line-through" : ""].join(" ").trim()}` : "",
        safeColor(r.color) ? `color:${r.color}` : "",
      ]
        .filter(Boolean)
        .join(";");
      return r.text ? `<span style="${st}">${escapeHTML(r.text)}</span>` : `<span style="${st}">&#8203;</span>`;
    })
    .join("");
  const align = { l: "left", ctr: "center", r: "right", just: "justify" }[p.align ?? "l"];
  return `<p style="text-align:${align};padding-left:${p.level * 24 + (p.bullet ? 20 : 0)}px">${bullet}${runs}</p>`;
}

function parasHTML(paras: Para[]): string {
  let n = 0;
  return paras.map((p) => runsHTML(p, p.bullet === "#" ? ++n : (n = 0))).join("");
}

function shapeHTML(s: Shape): string {
  const transform = [s.rot ? `rotate(${s.rot}deg)` : "", s.flipH ? "scaleX(-1)" : "", s.flipV ? "scaleY(-1)" : ""].filter(Boolean).join(" ");
  const box = `left:${px(s.x)};top:${px(s.y)};width:${px(Math.max(s.w, 1))};height:${px(Math.max(s.h, 1))}${transform ? `;transform:${transform}` : ""}`;
  if (s.geom === "line") {
    const c = safeColor(s.line?.color) ?? "#000000";
    return `<svg class="s" style="${box};overflow:visible" viewBox="0 0 ${Math.max(s.w, 1)} ${Math.max(s.h, 1)}" preserveAspectRatio="none"><line x1="0" y1="0" x2="${Math.max(s.w, 1)}" y2="${s.h}" stroke="${c}" stroke-width="${s.line?.width ?? 1}" vector-effect="non-scaling-stroke"/></svg>`;
  }
  const img = safeImage(s.image);
  if (s.table) {
    const t = s.table;
    const cols = t.cols.map((w) => `<col style="width:${px(w)}">`).join("");
    const rows = t.rows
      .map(
        (r) =>
          `<tr style="height:${px(r.h)}">${r.cells
            .filter((c) => !c.hidden)
            .map((c) => `<td${c.span ? ` colspan="${c.span}"` : ""}${c.rowSpan ? ` rowspan="${c.rowSpan}"` : ""}${safeColor(c.fill) ? ` style="background:${c.fill}"` : ""}>${parasHTML(c.paras)}</td>`)
            .join("")}</tr>`,
      )
      .join("");
    return `<div class="s" style="${box};height:auto"><table class="t"><colgroup>${cols}</colgroup>${rows}</table></div>`;
  }
  if (s.chart) return `<div class="s" style="${box}">${chartSVG(s.chart, Math.max(s.w, 1), Math.max(s.h, 1))}</div>`;
  if (s.placeholder) return `<div class="s ph" style="${box}"><span>${escapeHTML(s.placeholder)}</span></div>`;
  if (img && !s.paras) return `<img class="s" style="${box};object-fit:fill" src="${img}" alt="">`;
  const look = [
    safeColor(s.fill) ? `background:${s.fill}` : "",
    img ? `background-image:url('${img}');background-size:cover` : "",
    s.line && safeColor(s.line.color) ? `border:${px(s.line.width)} solid ${s.line.color}` : "",
    s.geom === "ellipse" ? "border-radius:50%" : s.geom === "roundRect" ? `border-radius:${px(Math.min(s.w, s.h) * 0.16)}` : "",
    s.geom === "triangle" ? "clip-path:polygon(50% 0,100% 100%,0 100%)" : "",
  ]
    .filter(Boolean)
    .join(";");
  const [l, t, r, b] = s.inset ?? [0, 0, 0, 0];
  const text = s.paras
    ? `<div class="tx" style="padding:${px(t)} ${px(r)} ${px(b)} ${px(l)};justify-content:${{ t: "flex-start", ctr: "center", b: "flex-end" }[s.anchor ?? "t"]}${s.vertical ? ";writing-mode:vertical-rl" : ""}">${parasHTML(s.paras)}</div>`
    : "";
  return `<div class="s" style="${box}${look ? ";" + look : ""}">${text}</div>`;
}

const fmtNum = (v: number) => (Math.abs(v) >= 10000 ? new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(v) : String(Math.round(v * 100) / 100));

/** A tick step near range / 4 that is 1, 2 or 5 times a power of ten. */
function niceStep(range: number): number {
  if (!(range > 0)) return 1;
  const raw = range / 4;
  const p = 10 ** Math.floor(Math.log10(raw));
  const m = raw / p;
  return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10) * p;
}

/** A chart as SVG sized w × h: title, axes with ticks, bars / lines / slices, and a legend. */
export function chartSVG(c: Chart, w: number, h: number): string {
  const out: string[] = [];
  const font = `font-family="Calibri,'Segoe UI','PingFang SC',sans-serif"`;
  const k = Math.max(1, Math.min(w, h) / 200); // type grows with the chart, as it does in PowerPoint
  const label = (x: number, y: number, t: string, o: { size?: number; anchor?: string; color?: string; bold?: boolean } = {}) =>
    `<text x="${x.toFixed(1)}" y="${y.toFixed(1)}" font-size="${((o.size ?? 12) * k).toFixed(1)}" text-anchor="${o.anchor ?? "middle"}" fill="${o.color ?? "#595959"}"${o.bold ? ' font-weight="700"' : ""} ${font}>${escapeHTML(t)}</text>`;
  let top = 8;
  if (c.title) {
    out.push(label(w / 2, 24 * k, c.title, { size: 17, color: "#262626", bold: true }));
    top = 36 * k;
  }
  const pie = c.kind === "pie" || c.kind === "doughnut";
  const legendItems = pie ? c.cats.map((name, i) => ({ name, color: c.series[0].pointColors?.[i] ?? c.series[0].color })) : c.series.map((x) => ({ name: x.name, color: x.color }));
  const legendH = legendItems.length > 1 ? 26 * k : 0;
  if (legendH) {
    const widths = legendItems.map((it) => (22 + Math.min(14, it.name.length) * 11) * k);
    let x = Math.max(8, (w - widths.reduce((a, b) => a + b, 0)) / 2);
    legendItems.forEach((it, i) => {
      out.push(`<rect x="${x.toFixed(1)}" y="${(h - 19 * k).toFixed(1)}" width="${(10 * k).toFixed(1)}" height="${(10 * k).toFixed(1)}" rx="2" fill="${it.color}"/>`);
      out.push(label(x + 14 * k, h - 10 * k, it.name.length > 14 ? it.name.slice(0, 13) + "…" : it.name, { anchor: "start" }));
      x += widths[i];
    });
  }
  const bottom = h - legendH - 6;
  if (pie) {
    const vals = c.series[0].values.map((v) => Math.max(0, v ?? 0));
    const total = vals.reduce((a, b) => a + b, 0) || 1;
    const r = Math.max(10, Math.min(w - 16, bottom - top) / 2);
    const cx = w / 2;
    const cy = top + (bottom - top) / 2;
    const inner = c.kind === "doughnut" ? r * 0.55 : 0;
    let a = -Math.PI / 2;
    vals.forEach((v, i) => {
      const da = (v / total) * Math.PI * 2;
      if (da <= 0) return;
      const color = c.series[0].pointColors?.[i] ?? c.series[0].color;
      const pt = (rad: number, ang: number) => `${(cx + rad * Math.cos(ang)).toFixed(2)},${(cy + rad * Math.sin(ang)).toFixed(2)}`;
      const big = da > Math.PI ? 1 : 0;
      const end = a + Math.min(da, Math.PI * 2 - 1e-4);
      const d = inner
        ? `M${pt(r, a)}A${r},${r} 0 ${big} 1 ${pt(r, end)}L${pt(inner, end)}A${inner},${inner} 0 ${big} 0 ${pt(inner, a)}Z`
        : `M${cx},${cy}L${pt(r, a)}A${r},${r} 0 ${big} 1 ${pt(r, end)}Z`;
      out.push(`<path d="${d}" fill="${color}" stroke="#ffffff" stroke-width="1.5"/>`);
      if (v / total >= 0.04) {
        const mid = a + da / 2;
        const lr = inner ? (r + inner) / 2 : r * 0.62;
        out.push(label(cx + lr * Math.cos(mid), cy + lr * Math.sin(mid) + 4, `${Math.round((v / total) * 100)}%`, { color: "#ffffff", bold: true, size: 13 }));
      }
      a += da;
    });
    return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" xmlns="http://www.w3.org/2000/svg">${out.join("")}</svg>`;
  }
  const n = c.cats.length;
  const sums = c.cats.map((_, i) => c.series.reduce((a, x) => a + Math.max(0, x.values[i] ?? 0), 0));
  const all = c.stacked ? sums : c.series.flatMap((x) => x.values.filter((v): v is number => v !== null));
  const lo = Math.min(0, ...all);
  const step = niceStep(Math.max(...all, 0) - lo);
  const min = Math.floor(lo / step) * step;
  const max = Math.max(min + step, Math.ceil(Math.max(...all, 0) / step) * step);
  const horizontal = c.kind === "bar";
  const left = horizontal ? Math.min(w * 0.3, (12 + Math.max(0, ...c.cats.map((x) => x.length)) * 11) * k) : (14 + Math.max(...[min, max].map((v) => fmtNum(v).length)) * 7) * k;
  const plot = { x: left, y: top, w: Math.max(10, w - left - 14), h: Math.max(10, bottom - top - 20 * k) };
  const scale = (v: number) => (v - min) / (max - min);
  // grid and value labels
  for (let v = min; v <= max + step / 2; v += step) {
    if (horizontal) {
      const x = plot.x + scale(v) * plot.w;
      out.push(`<line x1="${x.toFixed(1)}" y1="${plot.y}" x2="${x.toFixed(1)}" y2="${plot.y + plot.h}" stroke="#d9d9d9"/>`, label(x, plot.y + plot.h + 15 * k, fmtNum(v), { size: 11 }));
    } else {
      const y = plot.y + plot.h - scale(v) * plot.h;
      out.push(`<line x1="${plot.x}" y1="${y.toFixed(1)}" x2="${plot.x + plot.w}" y2="${y.toFixed(1)}" stroke="#d9d9d9"/>`, label(plot.x - 6, y + 4 * k, fmtNum(v), { size: 11, anchor: "end" }));
    }
  }
  const band = (horizontal ? plot.h : plot.w) / Math.max(1, n);
  const zero = scale(Math.max(min, 0));
  c.cats.forEach((cat, i) => {
    const centre = (horizontal ? plot.y : plot.x) + band * (i + 0.5);
    const shown = cat.length > 10 && n > 6 ? cat.slice(0, 9) + "…" : cat;
    if (horizontal) out.push(label(plot.x - 6, centre + 4 * k, shown, { anchor: "end" }));
    else out.push(label(centre, plot.y + plot.h + 16 * k, shown));
  });
  if (c.kind === "col" || c.kind === "bar") {
    const groupW = band * 0.72;
    const barW = c.stacked ? groupW : groupW / c.series.length;
    c.cats.forEach((_, i) => {
      let acc = 0;
      c.series.forEach((x, k) => {
        const v = x.values[i];
        if (v === null || v === undefined) return;
        const from = c.stacked ? scale(acc) : zero;
        const to = c.stacked ? scale(acc + Math.max(0, v)) : scale(v);
        if (c.stacked) acc += Math.max(0, v);
        const start = band * i + (band - groupW) / 2 + (c.stacked ? 0 : k * barW);
        if (horizontal) {
          const x0 = plot.x + Math.min(from, to) * plot.w;
          out.push(`<rect x="${x0.toFixed(1)}" y="${(plot.y + start).toFixed(1)}" width="${(Math.abs(to - from) * plot.w).toFixed(1)}" height="${Math.max(1, barW - 2).toFixed(1)}" fill="${x.color}"/>`);
        } else {
          const y0 = plot.y + plot.h - Math.max(from, to) * plot.h;
          out.push(`<rect x="${(plot.x + start).toFixed(1)}" y="${y0.toFixed(1)}" width="${Math.max(1, barW - 2).toFixed(1)}" height="${(Math.abs(to - from) * plot.h).toFixed(1)}" fill="${x.color}"/>`);
        }
      });
    });
  } else {
    const acc = c.cats.map(() => 0);
    c.series.forEach((x) => {
      const pts = x.values.map((v, i) => {
        if (v === null) return null;
        const val = c.stacked ? (acc[i] += Math.max(0, v)) : v;
        return [plot.x + band * (i + 0.5), plot.y + plot.h - scale(val) * plot.h] as const;
      });
      const line = pts.filter((q): q is readonly [number, number] => !!q).map(([a, b]) => `${a.toFixed(1)},${b.toFixed(1)}`);
      if (c.kind === "area" && line.length) {
        const base = (plot.y + plot.h - zero * plot.h).toFixed(1);
        const first = line[0].split(",")[0];
        const last = line[line.length - 1].split(",")[0];
        out.push(`<polygon points="${first},${base} ${line.join(" ")} ${last},${base}" fill="${x.color}" fill-opacity="0.55" stroke="${x.color}" stroke-width="2"/>`);
      } else {
        out.push(`<polyline points="${line.join(" ")}" fill="none" stroke="${x.color}" stroke-width="2.5" stroke-linejoin="round"/>`);
        for (const q of pts) if (q) out.push(`<circle cx="${q[0].toFixed(1)}" cy="${q[1].toFixed(1)}" r="3.5" fill="${x.color}"/>`);
      }
    });
  }
  out.push(`<line x1="${plot.x}" y1="${plot.y + plot.h}" x2="${plot.x + plot.w}" y2="${plot.y + plot.h}" stroke="#a6a6a6"/>`);
  return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" xmlns="http://www.w3.org/2000/svg">${out.join("")}</svg>`;
}

/** One slide as HTML at the deck's size (px). */
export function slideHTML(deck: Deck, slide: Slide): string {
  const bg = [safeColor(slide.bg) ? `background-color:${slide.bg}` : "background-color:#ffffff", safeImage(slide.bgImage) ? `background-image:url('${slide.bgImage}');background-size:cover` : ""]
    .filter(Boolean)
    .join(";");
  return `<section class="slide" style="width:${deck.width}px;height:${deck.height}px;${bg}">${slide.shapes.map(shapeHTML).join("")}</section>`;
}

/** The deck as the body of the viewer's frame: each slide with its number and its notes. */
export function deckHTML(deck: Deck): string {
  return deck.slides
    .map(
      (s, i) =>
        `<div class="page"><div class="num">${i + 1}${s.hidden ? ` · ${t("lib-pptx.hidden")}` : ""}</div>${slideHTML(deck, s)}${s.notes ? `<div class="notes" style="width:${deck.width}px">${escapeHTML(s.notes).replace(/\n/g, "<br>")}</div>` : ""}</div>`,
    )
    .join("");
}

/** Styles of the slides in the viewer's frame. */
export const DECK_CSS = `
.page{margin:0 auto 28px;width:max-content}
.num{font:600 22px/1.4 system-ui,sans-serif;color:var(--muted);margin:0 0 8px 2px}
.slide{position:relative;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,.18),0 8px 24px rgba(0,0,0,.12);font-family:Calibri,"Segoe UI","PingFang SC","Microsoft YaHei",system-ui,sans-serif;color:#000}
.slide .s{position:absolute;box-sizing:border-box}
.slide .tx{position:absolute;inset:0;display:flex;flex-direction:column;overflow:hidden;box-sizing:border-box}
.slide p{margin:0 0 .3em;line-height:1.2;white-space:pre-wrap;overflow-wrap:break-word}
.slide p:last-child{margin-bottom:0}
.slide .b{display:inline-block;width:20px;margin-left:-20px;line-height:1}
.slide .ph{display:flex;align-items:center;justify-content:center;border:1px dashed #9aa0a6;background:rgba(127,127,127,.08);color:#5f6368;font:24px/1.4 system-ui,sans-serif;text-align:center;padding:8px}
.slide .t{border-collapse:collapse;width:100%;table-layout:fixed}
.slide .t td{border:1px solid #9aa0a6;padding:4px 8px;vertical-align:top}
.notes{margin-top:12px;font:20px/1.55 system-ui,sans-serif;color:var(--muted)}
`;
