// Jupyter notebooks for the web's file viewer (docs/web/web.md "Jupyter", #1407; iOS / Mac: #1296): nbformat 4 (and 3's
// worksheets) into cells, each output reduced to the one representation the viewer shows. Pure; notebook.test.ts.
import { t } from "../i18n";
import { stripAnsi } from "./ansi";

export { stripAnsi };

export type Output =
  | { kind: "stream"; name: "stdout" | "stderr"; text: string }
  | { kind: "image"; mime: string; src: string } // data: URL
  | { kind: "html"; html: string }
  | { kind: "markdown"; text: string }
  | { kind: "json"; text: string }
  | { kind: "text"; text: string }
  | { kind: "error"; name: string; value: string; traceback: string };

export interface Cell {
  type: "code" | "markdown" | "raw";
  source: string;
  count?: number | null;
  outputs: Output[];
  /** Markdown cells: pictures pasted into the cell, by name, as data: URLs. */
  attachments: Record<string, string>;
}

export interface Notebook {
  cells: Cell[];
  language: string; // python, r, julia… for coloring
  kernel?: string; // the kernel's display name
  format: string; // "4.5"
}

/** Lines of output kept per text output; beyond that the viewer says it cut them. */
export const MAX_OUTPUT_LINES = 2000;

type Json = Record<string, unknown>;
const str = (v: unknown): string => (Array.isArray(v) ? v.map((x) => (typeof x === "string" ? x : "")).join("") : typeof v === "string" ? v : "");

/** Text cut to MAX_OUTPUT_LINES, saying how many lines were left out. */
export function capLines(s: string, max = MAX_OUTPUT_LINES): string {
  let i = -1;
  for (let n = 0; n < max; n++) {
    i = s.indexOf("\n", i + 1);
    if (i < 0) return s;
  }
  const rest = s.slice(i + 1).replace(/\n$/, "").split("\n").length;
  return s.slice(0, i + 1) + t("lib-notebook.lines_hidden", { count: rest });
}

const IMAGE = ["image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"];

/** The one representation of a rich output the viewer shows, best first (as Jupyter's own front ends choose). */
function richOutput(data: Json): Output | null {
  for (const mime of IMAGE) {
    const v = data[mime];
    if (v === undefined) continue;
    const body = str(v);
    if (mime === "image/svg+xml") return { kind: "image", mime, src: `data:image/svg+xml;charset=utf-8,${encodeURIComponent(body)}` };
    return { kind: "image", mime, src: `data:${mime};base64,${body.replace(/\s+/g, "")}` };
  }
  if (data["text/html"] !== undefined) return { kind: "html", html: str(data["text/html"]) };
  if (data["text/markdown"] !== undefined) return { kind: "markdown", text: str(data["text/markdown"]) };
  if (data["text/latex"] !== undefined) {
    const tex = str(data["text/latex"]).trim();
    return { kind: "markdown", text: tex.startsWith("$") ? tex : `$$\n${tex}\n$$` };
  }
  if (data["application/json"] !== undefined) return { kind: "json", text: JSON.stringify(data["application/json"], null, 2) };
  if (data["text/plain"] !== undefined) return { kind: "text", text: capLines(stripAnsi(str(data["text/plain"]))) };
  return null;
}

function readOutput(o: Json): Output | null {
  switch (o.output_type) {
    case "stream":
      return { kind: "stream", name: o.name === "stderr" ? "stderr" : "stdout", text: capLines(stripAnsi(str(o.text))) };
    case "execute_result":
    case "display_data":
      return richOutput((o.data as Json) ?? {});
    case "pyout": // nbformat 3
    case "display_data_v3":
      return richOutput(o as Json);
    case "error":
    case "pyerr":
      return {
        kind: "error",
        name: str(o.ename),
        value: str(o.evalue),
        // whole: a traceback cut short loses the line that failed
        traceback: stripAnsi(Array.isArray(o.traceback) ? (o.traceback as string[]).join("\n") : str(o.traceback)),
      };
  }
  return null;
}

/** Consecutive stream outputs of the same name, as Jupyter shows them: one block. */
function mergeStreams(out: Output[]): Output[] {
  const merged: Output[] = [];
  for (const o of out) {
    const last = merged[merged.length - 1];
    if (o.kind === "stream" && last?.kind === "stream" && last.name === o.name) merged[merged.length - 1] = { ...last, text: last.text + o.text };
    else merged.push(o);
  }
  return merged;
}

/** A notebook's JSON into cells; throws with a readable reason for what is not one. */
export function parseNotebook(text: string): Notebook {
  let nb: Json;
  try {
    nb = JSON.parse(text) as Json;
  } catch (e) {
    throw new Error(t("lib-notebook.invalid_json", { reason: e instanceof Error ? e.message : String(e) }));
  }
  if (!nb || typeof nb !== "object") throw new Error(t("lib-notebook.invalid"));
  const meta = (nb.metadata as Json) ?? {};
  const kernelspec = (meta.kernelspec as Json) ?? {};
  const langInfo = (meta.language_info as Json) ?? {};
  const language = String(kernelspec.language ?? langInfo.name ?? meta.language ?? "python").toLowerCase();
  const raw: Json[] = Array.isArray(nb.cells)
    ? (nb.cells as Json[])
    : Array.isArray(nb.worksheets) // nbformat 3
      ? ((nb.worksheets as Json[])[0]?.cells as Json[]) ?? []
      : [];
  if (!Array.isArray(nb.cells) && !Array.isArray(nb.worksheets)) throw new Error(t("lib-notebook.no_cells"));
  const cells: Cell[] = raw.map((c) => {
    const type = c.cell_type === "code" ? "code" : c.cell_type === "markdown" || c.cell_type === "heading" ? "markdown" : "raw";
    let source = str(c.source ?? c.input);
    if (c.cell_type === "heading") source = `${"#".repeat(Number(c.level) || 1)} ${source}`;
    const attachments: Record<string, string> = {};
    for (const [name, bundle] of Object.entries((c.attachments as Json) ?? {})) {
      const out = richOutput((bundle as Json) ?? {});
      if (out?.kind === "image") attachments[name] = out.src;
    }
    const outputs = type === "code" ? mergeStreams(((c.outputs as Json[]) ?? []).map(readOutput).filter((o): o is Output => !!o)) : [];
    const count = (c.execution_count ?? c.prompt_number) as number | null | undefined;
    return { type, source, count: typeof count === "number" ? count : null, outputs, attachments };
  });
  return { cells, language, kernel: typeof kernelspec.display_name === "string" ? kernelspec.display_name : undefined, format: `${nb.nbformat ?? "?"}.${nb.nbformat_minor ?? 0}` };
}

/** Markdown with `attachment:name` images pointing at a fragment the viewer swaps for the picture (data: URLs are not
 * allowed through react-markdown's URL filter). */
export function withAttachmentRefs(source: string): string {
  return source.replace(/\(attachment:([^)\s]+)/g, (_, name: string) => `(#nb-attachment=${encodeURIComponent(name)}`);
}

/** The file name the code colorer goes by for a kernel language. */
export function languageFile(language: string): string {
  const ext: Record<string, string> = { python: "py", python3: "py", r: "r", julia: "jl", javascript: "js", typescript: "ts", scala: "scala", java: "java", go: "go", rust: "rs", ruby: "rb", bash: "sh", sh: "sh", sql: "sql", "c++": "cpp", cpp: "cpp", c: "c", kotlin: "kt", swift: "swift" };
  return `cell.${ext[language] ?? "py"}`;
}
