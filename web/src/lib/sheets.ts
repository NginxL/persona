// Pure logic of the table viewer (CSV / TSV / xlsx / xls / ods / numbers; docs/web/web.md "Sheets", iOS / Mac: #1294):
// parsing into sheets of display text, column letters and widths, and what a selection copies. No DOM: the worker
// (sheets.worker.ts) runs the parsing, SheetView.tsx draws; tested in sheets.test.ts.
import type { WorkBook } from "xlsx";

/** A merged block of cells, inclusive on both ends (0-based). */
export interface Merge {
  r0: number;
  c0: number;
  r1: number;
  c1: number;
}

export interface Sheet {
  name: string;
  /** Display text by row; rows may be shorter than `cols`. */
  rows: string[][];
  cols: number;
  merges: Merge[];
  /** Column widths the file sets (px); missing ones are estimated from the text. */
  widths: (number | undefined)[];
  /** Rows were dropped after MAX_ROWS. */
  cut: boolean;
}

/** Rows kept per sheet: beyond this the viewer says it shows only the start. */
export const MAX_ROWS = 500_000;
/** Cells kept per workbook, across sheets (a wide sheet hits this before MAX_ROWS). */
export const MAX_CELLS = 20_000_000;

/** Which separator a delimited text file uses: TSV by name, else the commonest of , ; tab on the first lines. */
export function separatorOf(name: string, text: string): string {
  const ext = name.toLowerCase().split(".").pop() ?? "";
  if (ext === "tsv" || ext === "tab") return "\t";
  const head = text.slice(0, 4096).split(/\r?\n/).slice(0, 5).join("\n");
  const counts: Record<string, number> = { ",": 0, ";": 0, "\t": 0 };
  let quoted = false, fieldStart = true;
  for (let i = head.charCodeAt(0) === 0xfeff ? 1 : 0; i < head.length; i++) {
    const c = head[i];
    if (quoted) {
      if (c === '"') {
        if (head[i + 1] === '"') i++;
        else quoted = false;
      }
    } else if (c === '"' && fieldStart) {
      quoted = true;
      fieldStart = false;
    } else {
      const separator = c === "," || c === ";" || c === "\t";
      if (separator) counts[c]++;
      // A quote inside an unquoted value (inch") is literal, as in parseDelimited.
      fieldStart = separator || c === "\n" || c === "\r";
    }
  }
  const [best] = [",", ";", "\t"].map((c) => [c, counts[c]] as const).sort((a, b) => b[1] - a[1]);
  return best[1] > 0 ? best[0] : ",";
}

/**
 * Delimited text into rows (RFC 4180: quoted fields, "" inside quotes, newlines inside quotes, CRLF). Values stay
 * exactly as written: no number or date guessing (a spreadsheet's "001" → 1 would change what the file says).
 */
export function parseDelimited(text: string, sep: string, maxRows = MAX_ROWS): { rows: string[][]; cols: number; cut: boolean } {
  const rows: string[][] = [];
  let row: string[] = [];
  let cols = 0;
  let i = 0;
  const n = text.length;
  if (text.charCodeAt(0) === 0xfeff) i = 1; // BOM
  const endRow = () => {
    rows.push(row);
    cols = Math.max(cols, row.length);
    row = [];
  };
  while (i < n) {
    if (rows.length >= maxRows) return { rows, cols, cut: true };
    let field: string;
    if (text[i] === '"') {
      let out = "";
      i++;
      for (;;) {
        const q = text.indexOf('"', i);
        if (q < 0) {
          out += text.slice(i);
          i = n;
          break;
        }
        out += text.slice(i, q);
        if (text[q + 1] === '"') {
          out += '"';
          i = q + 2;
        } else {
          i = q + 1;
          break;
        }
      }
      // anything between the closing quote and the separator is kept, as spreadsheets do
      let j = i;
      while (j < n && text[j] !== sep && text[j] !== "\n" && text[j] !== "\r") j++;
      field = out + text.slice(i, j);
      i = j;
    } else {
      let j = i;
      while (j < n && text[j] !== sep && text[j] !== "\n" && text[j] !== "\r") j++;
      field = text.slice(i, j);
      i = j;
    }
    row.push(field);
    if (i >= n) break;
    if (text[i] === sep) {
      i++;
      if (i >= n) row.push("");
    } else {
      if (text[i] === "\r" && text[i + 1] === "\n") i++;
      i++;
      endRow();
    }
  }
  if (row.length) endRow();
  return { rows, cols, cut: false };
}

/** A delimited file as the one sheet it is. */
export function delimitedSheet(name: string, text: string): Sheet {
  const { rows, cols, cut } = parseDelimited(text, separatorOf(name, text));
  return { name: "", rows, cols, merges: [], widths: [], cut };
}

/** A parsed workbook's sheets as display text (each cell's formatted value, as the spreadsheet app shows it). */
export function workbookSheets(XLSX: typeof import("xlsx"), wb: WorkBook): Sheet[] {
  const out: Sheet[] = [];
  let cells = 0;
  for (const name of wb.SheetNames) {
    const ws = wb.Sheets[name];
    if (!ws) continue;
    const ref = ws["!ref"];
    if (!ref) {
      out.push({ name, rows: [], cols: 0, merges: [], widths: [], cut: false });
      continue;
    }
    const range = XLSX.utils.decode_range(ref);
    const cols = range.e.c + 1;
    const budget = Math.max(0, Math.min(MAX_ROWS, Math.floor((MAX_CELLS - cells) / Math.max(1, cols))));
    const all = range.e.r + 1;
    const keep = Math.min(all, budget);
    const rows: string[][] = [];
    const dense = (ws as unknown as { "!data"?: ({ w?: string; v?: unknown } | undefined)[][] })["!data"];
    for (let r = 0; r < keep; r++) {
      const line: string[] = [];
      let last = -1;
      for (let c = 0; c < cols; c++) {
        const cell = dense ? dense[r]?.[c] : (ws[XLSX.utils.encode_cell({ r, c })] as { w?: string; v?: unknown } | undefined);
        const text = cell ? (cell.w ?? (cell.v == null ? "" : String(cell.v))) : "";
        line.push(text);
        if (text !== "") last = c;
      }
      line.length = last + 1;
      rows.push(line);
    }
    cells += keep * cols;
    const merges = (ws["!merges"] ?? []).map((m) => ({ r0: m.s.r, c0: m.s.c, r1: m.e.r, c1: m.e.c })).filter((m) => m.r0 < keep);
    const widths = (ws["!cols"] ?? []).map((c) => (c?.wpx ? c.wpx : c?.wch ? Math.round(c.wch * 7 + 10) : undefined));
    out.push({ name, rows, cols, merges, widths, cut: keep < all });
  }
  return out;
}

/** A spreadsheet's column name: 0 → A, 25 → Z, 26 → AA. */
export function columnName(c: number): string {
  let s = "";
  for (let n = c + 1; n > 0; n = Math.floor((n - 1) / 26)) s = String.fromCharCode(65 + ((n - 1) % 26)) + s;
  return s;
}

/** A cell's name: (0, 0) → A1. */
export function cellName(r: number, c: number): string {
  return `${columnName(c)}${r + 1}`;
}

/** Display width of a text in columns (wide CJK / emoji count two). */
function textColumns(s: string): number {
  let n = 0;
  for (const ch of s) n += /[ᄀ-ᅟ⺀-꓏가-힣豈-﫿︰-﹏＀-｠￠-￦]|\p{Extended_Pictographic}/u.test(ch) ? 2 : 1;
  return n;
}

export const MIN_COL = 48;
export const MAX_COL = 360;
const CHAR_PX = 8; // 12.5px text: digits and capitals, the widest common glyphs
const CELL_PAD = 20;

/**
 * Column widths (px): fitted to the first rows' text within MIN_COL–MAX_COL, or the file's where it sets a wider one
 * (a spreadsheet shows ##### in a column too narrow for its number; a preview shows the number).
 */
export function columnWidths(sheet: Sheet, sample = 300): number[] {
  const out: number[] = [];
  for (let c = 0; c < sheet.cols; c++) {
    const set = sheet.widths[c] ?? 0;
    let w = textColumns(columnName(c));
    for (let r = 0; r < Math.min(sample, sheet.rows.length); r++) {
      const t = sheet.rows[r][c];
      if (t) w = Math.max(w, textColumns(t.length > 80 ? t.slice(0, 80) : t));
    }
    out.push(Math.max(MIN_COL, Math.min(MAX_COL, Math.round(w * CHAR_PX + CELL_PAD)), Math.min(MAX_COL * 2, set)));
  }
  return out;
}

/** Running left edges of columns: offsets[c] is where column c starts, offsets[cols] the total width. */
export function offsetsOf(widths: number[]): number[] {
  const o = [0];
  for (const w of widths) o.push(o[o.length - 1] + w);
  return o;
}

/** The column at x (px from the first column's left edge), by binary search over offsetsOf. */
export function columnAt(offsets: number[], x: number): number {
  let lo = 0;
  let hi = offsets.length - 2;
  if (hi < 0) return 0;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (offsets[mid] <= x) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

/** A cell that reads as a number (right-aligned, like spreadsheets do): 1,234.5 · -3 · 12% · $4 · ¥4 · 1e5. */
export function looksNumeric(s: string): boolean {
  return s.length > 0 && s.length < 40 && /^[-+(]?[$¥€£]?\s?\d[\d,]*(\.\d+)?([eE][-+]?\d+)?%?\)?$/.test(s.trim());
}

/** A rectangle of cells, normalized so r0 ≤ r1 and c0 ≤ c1. */
export interface Range {
  r0: number;
  c0: number;
  r1: number;
  c1: number;
}

export function normalize(a: { r: number; c: number }, b: { r: number; c: number }): Range {
  return { r0: Math.min(a.r, b.r), c0: Math.min(a.c, b.c), r1: Math.max(a.r, b.r), c1: Math.max(a.c, b.c) };
}

/** A range grown until no merged block sticks out of it (selecting part of a merge selects all of it). */
export function expandToMerges(r: Range, merges: Merge[]): Range {
  const out = { ...r };
  for (let changed = true; changed; ) {
    changed = false;
    for (const m of merges) {
      if (m.r1 < out.r0 || m.r0 > out.r1 || m.c1 < out.c0 || m.c0 > out.c1) continue;
      if (m.r0 < out.r0 || m.c0 < out.c0 || m.r1 > out.r1 || m.c1 > out.c1) {
        out.r0 = Math.min(out.r0, m.r0);
        out.c0 = Math.min(out.c0, m.c0);
        out.r1 = Math.max(out.r1, m.r1);
        out.c1 = Math.max(out.c1, m.c1);
        changed = true;
      }
    }
  }
  return out;
}

/** What copying a range puts on the clipboard: tab-separated rows, quoted where a value has a tab, newline or quote. */
export function rangeTSV(rows: string[][], r: Range): string {
  const quote = (v: string) => (/[\t\n\r"]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v);
  const lines: string[] = [];
  for (let i = r.r0; i <= r.r1; i++) {
    const line: string[] = [];
    for (let j = r.c0; j <= r.c1; j++) line.push(quote(rows[i]?.[j] ?? ""));
    lines.push(line.join("\t"));
  }
  return lines.join("\n");
}

/** A summary of a selection, as spreadsheets show it: count, and sum / average when the cells are numbers. */
export function rangeSummary(rows: string[][], r: Range): { count: number; sum?: number; avg?: number } {
  let count = 0;
  let nums = 0;
  let sum = 0;
  const cells = (r.r1 - r.r0 + 1) * (r.c1 - r.c0 + 1);
  if (cells > 200_000) return { count: cells };
  for (let i = r.r0; i <= r.r1; i++) {
    for (let j = r.c0; j <= r.c1; j++) {
      const v = rows[i]?.[j] ?? "";
      if (v === "") continue;
      count++;
      if (looksNumeric(v)) {
        const neg = /^\(.*\)$/.test(v.trim()) || v.trim().startsWith("-");
        const x = parseFloat(v.replace(/[^\d.eE+-]/g, "").replace(/^[-+]/, ""));
        if (Number.isFinite(x)) {
          sum += (neg ? -x : x) / (v.trim().replace(/^\((.*)\)$/, "$1").endsWith("%") ? 100 : 1);
          nums++;
        }
      }
    }
  }
  return nums > 1 ? { count, sum, avg: sum / nums } : { count };
}

/** Merged blocks by the cells they cover (but not their top-left cell), to skip those when drawing. */
export function coveredCells(merges: Merge[]): Set<string> {
  const s = new Set<string>();
  for (const m of merges) {
    if ((m.r1 - m.r0 + 1) * (m.c1 - m.c0 + 1) > 100_000) continue;
    for (let r = m.r0; r <= m.r1; r++) for (let c = m.c0; c <= m.c1; c++) if (r !== m.r0 || c !== m.c0) s.add(`${r}:${c}`);
  }
  return s;
}
