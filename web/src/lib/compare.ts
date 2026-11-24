// Comparing two versions of a document in the Git panel (docs/web/web.md "Compare", #1437; iOS / Mac: #1299): each side's text
// by page / paragraph / slide / sheet, then a line diff like code. Pure apart from the readers' parsers, which are
// passed in or imported lazily by the caller; tested in compare.test.ts.
import type JSZip from "jszip";
import type { WorkBook } from "xlsx";
import { t } from "../i18n";
import { diffLines, type Op } from "./diff-lines";

// The line diff lives in diff-lines.ts so a work group's edit rows (ActionRow, on the first screen) get it without
// this file's document readers and their lib-compare strings.
export { diffLines };
export type { Op };

/** One page / slide / sheet: what it is called and its lines of text. */
export interface Unit {
  label: string;
  lines: string[];
}

/** A table row whose cells changed: each cell before and after (equal when that cell did not change). */
export type RowChange = { kind: "row"; cells: { old: string; new: string }[] };

/**
 * Removed lines followed by as many added ones that are table rows with the same number of cells become one row each,
 * showing old → new only in the cells that changed (instead of the whole row twice).
 */
export function pairRows(ops: Op[]): (Op | RowChange)[] {
  const out: (Op | RowChange)[] = [];
  for (let i = 0; i < ops.length; ) {
    if (ops[i].kind !== "del") {
      out.push(ops[i++]);
      continue;
    }
    let d = i;
    while (d < ops.length && ops[d].kind === "del") d++;
    let a = d;
    while (a < ops.length && ops[a].kind === "add") a++;
    const dels = ops.slice(i, d);
    const adds = ops.slice(d, a);
    // pair the k-th removed line with the k-th added one when both are rows of the same width
    const isRowPair = (k: number) => k < adds.length && dels[k].text.includes(ROW_SEP) && dels[k].text.split(ROW_SEP).length === adds[k].text.split(ROW_SEP).length;
    const paired = new Set<number>();
    dels.forEach((o, k) => {
      if (!isRowPair(k)) return;
      paired.add(k);
      const after = adds[k].text.split(ROW_SEP);
      out.push({ kind: "row", cells: o.text.split(ROW_SEP).map((b, j) => ({ old: b, new: after[j] })) });
    });
    out.push(...dels.filter((_, k) => !paired.has(k)), ...adds.filter((_, k) => !paired.has(k)));
    i = a;
  }
  return out;
}

/** A shown stretch of the diff, with the unit (page, slide…) it starts in. */
export interface Hunk {
  unit: string;
  ops: Op[];
  skippedBefore: number; // unchanged lines folded away before it
}

const HEADER = "\u0000§";
/** Between the cells of a table row on one line. */
export const ROW_SEP = " | "; // marks a unit's start inside the flattened text; never in a document's own text

/** Units into one list of lines, each unit led by a header line, so a diff can say where a change is. */
export function flatten(units: Unit[]): string[] {
  return units.flatMap((u) => [HEADER + u.label, ...u.lines]);
}

/** The changed stretches with `context` unchanged lines around each, labeled by the unit they start in. */
export function hunks(ops: Op[], context = 2): Hunk[] {
  const out: Hunk[] = [];
  let unit = "";
  const unitAt: string[] = [];
  for (const o of ops) {
    if (o.text.startsWith(HEADER)) unit = o.text.slice(HEADER.length);
    unitAt.push(unit);
  }
  const isHeader = (o: Op) => o.text.startsWith(HEADER);
  const isSameHeader = (o: Op) => o.kind === "same" && isHeader(o);
  const changed = ops.map((o) => o.kind !== "same");
  let i = 0;
  let last = 0;
  while (i < ops.length) {
    if (!changed[i]) {
      i++;
      continue;
    }
    let start = i;
    while (start > last && i - start < context && !isSameHeader(ops[start - 1])) start--;
    let end = i;
    while (end < ops.length) {
      if (changed[end]) {
        end++;
        continue;
      }
      if (isSameHeader(ops[end])) break;
      let j = end;
      while (j < ops.length && !changed[j] && !isSameHeader(ops[j]) && j - end < context * 2) j++;
      if (j < ops.length && changed[j] && !isSameHeader(ops[j]) && j - end < context * 2) end = j;
      else break;
    }
    let stop = end;
    while (stop < ops.length && stop - end < context && !isSameHeader(ops[stop])) stop++;
    const slice = ops.slice(start, stop).filter((o) => !isSameHeader(o));
    let skippedBefore = 0;
    for (let k = last; k < start; k++) {
      if (!isHeader(ops[k])) skippedBefore++;
    }
    out.push({ unit: unitAt[start] || unitAt[i], ops: slice, skippedBefore });
    last = stop;
    i = stop;
  }
  return out;
}

/** A line of a diff, shown: unit headers that were added or removed read as the unit's name. */
export function shownText(text: string): string {
  return text.startsWith(HEADER) ? `—— ${text.slice(HEADER.length)} ——` : text;
}

const linesOf = (s: string) =>
  s
    .split(/\r?\n/)
    .map((l) => l.replace(/\s+$/, ""))
    .filter((l) => l.trim() !== "");

// ── readers ───────────────────────────────────────────────

type ParseXML = (s: string) => Document;

/** A Word document's paragraphs (tables' cells one per line), split into pages where the document breaks them. */
export async function docxUnits(zip: JSZip, parse: ParseXML): Promise<Unit[]> {
  const f = zip.file("word/document.xml");
  if (!f) throw new Error(t("lib-compare.invalid_docx"));
  const doc = parse(await f.async("string"));
  const units: Unit[] = [{ label: t("lib-compare.page", { n: 1 }), lines: [] }];
  const walk = (el: Element) => {
    for (let c = el.firstChild; c; c = c.nextSibling) {
      if (c.nodeType !== 1) continue;
      const e = c as Element;
      if (e.localName === "tbl") {
        // a table row reads as one line, its cells joined with " | ", so a change shows the row it is in
        for (const tr of [...e.getElementsByTagName("w:tr")].filter((r) => r.parentNode === e)) {
          const cells = [...tr.getElementsByTagName("w:tc")]
            .filter((tc) => tc.parentNode === tr)
            .map((tc) =>
              [...tc.getElementsByTagName("w:t")]
                .map((w) => w.textContent ?? "")
                .join("")
                .trim(),
            );
          if (cells.some(Boolean)) units[units.length - 1].lines.push(cells.join(ROW_SEP));
        }
      } else if (e.localName === "p") {
        let text = "";
        let pageBreak = false;
        const collect = (n: Element) => {
          for (let k = n.firstChild; k; k = k.nextSibling) {
            if (k.nodeType !== 1) continue;
            const ke = k as Element;
            if (ke.localName === "t") text += ke.textContent ?? "";
            else if (ke.localName === "tab") text += "\t";
            else if (ke.localName === "br" && ke.getAttribute("w:type") === "page") pageBreak = true;
            else if (ke.localName === "lastRenderedPageBreak") pageBreak = true;
            else if (ke.localName !== "del" && ke.localName !== "instrText") collect(ke);
          }
        };
        collect(e);
        if (pageBreak) units.push({ label: t("lib-compare.page", { n: units.length + 1 }), lines: [] });
        if (text.trim()) units[units.length - 1].lines.push(text.replace(/\s+$/, ""));
      } else walk(e);
    }
  };
  const body = doc.getElementsByTagName("w:body")[0] ?? doc.documentElement;
  walk(body);
  return units.filter((u, i) => u.lines.length || i === 0);
}

/** A deck's text, slide by slide (notes after a marker). */
export async function pptxUnits(zip: JSZip, parse: ParseXML): Promise<Unit[]> {
  const { readPptx } = await import("./pptx"); // with Office previews' own chunk, not the Git panel's
  const deck = await readPptx(zip, parse);
  return deck.slides.map((s, i) => {
    const lines: string[] = [];
    for (const sh of s.shapes) {
      for (const p of sh.paras ?? []) {
        const para = p.runs.map((r) => (r.br ? "\n" : r.text)).join("");
        lines.push(...linesOf(para).map((l) => "  ".repeat(p.level) + l));
      }
      for (const row of sh.table?.rows ?? []) lines.push(row.cells.filter((c) => !c.hidden).map((c) => c.paras.map((p) => p.runs.map((r) => r.text).join("")).join(" ")).join(" | "));
      if (sh.chart) lines.push(`[${sh.chart.title ? t("lib-compare.chart_titled", { title: sh.chart.title }) : t("lib-compare.chart")}] ${sh.chart.series.map((x) => `${x.name} = ${x.values.map((v) => v ?? "").join(", ")}`).join(t("lib-compare.series_separator"))}`);
    }
    if (s.notes) lines.push(t("lib-compare.notes"), ...linesOf(s.notes));
    return { label: t("lib-compare.slide", { n: i + 1 }), lines };
  });
}

/** A workbook's sheets as rows of tab-separated cells. */
export function workbookUnits(XLSX: typeof import("xlsx"), wb: WorkBook): Unit[] {
  return wb.SheetNames.map((name) => ({ label: t("lib-compare.sheet", { name }), lines: linesOf(XLSX.utils.sheet_to_csv(wb.Sheets[name], { FS: "\t", blankrows: false })) }));
}

/** A PDF's text page by page, lines as pdf.js sees them end. */
export async function pdfUnits(data: ArrayBuffer): Promise<Unit[]> {
  const pdfjs = await import("pdfjs-dist");
  const { default: worker } = await import("pdfjs-dist/build/pdf.worker.min.mjs?url");
  pdfjs.GlobalWorkerOptions.workerSrc = worker;
  const task = pdfjs.getDocument({ data: new Uint8Array(data) });
  const doc = await task.promise;
  const units: Unit[] = [];
  for (let i = 1; i <= Math.min(doc.numPages, 500); i++) {
    const page = await doc.getPage(i);
    const content = await page.getTextContent();
    let text = "";
    for (const it of content.items) if ("str" in it) text += it.str + (it.hasEOL ? "\n" : "");
    units.push({ label: t("lib-compare.page", { n: i }), lines: linesOf(text) });
  }
  await task.destroy();
  return units;
}
