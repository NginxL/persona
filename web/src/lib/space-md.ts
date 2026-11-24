// A space's SPACE.md as the editor shows it (#13500, docs/web/web.md "Domain spaces"): the parts the user writes, which
// the editor lets them change, and the blocks Persona keeps up to date (`<!-- persona:managed:<name>:begin -->` …
// `:end -->`), shown read-only. The hub refuses a write that changes a managed block or its markers (400), so the
// editor never lets one change: it edits the human parts and puts the blocks back byte for byte. Pure, so tested
// without a DOM.

export type SpaceMdPart =
  | { kind: "human"; text: string }
  /** `text` is the whole block, markers included; `body` what is between them. */
  | { kind: "managed"; name: string; text: string; body: string };

const NAME = "[A-Za-z0-9_.-]+";
const BEGIN = new RegExp(`<!-- persona:managed:(${NAME}):begin -->`, "g");
const MARKER = new RegExp(`<!-- persona:managed:(${NAME}):(begin|end) -->`, "g");

/** Splits the file into human parts and managed blocks, in order; joining them gives the file back unchanged. A block
 *  whose end marker is missing runs to the end of the file, so nothing of it becomes editable. */
export function splitSpaceMd(content: string): SpaceMdPart[] {
  const parts: SpaceMdPart[] = [];
  let at = 0;
  BEGIN.lastIndex = 0;
  for (let m = BEGIN.exec(content); m; m = BEGIN.exec(content)) {
    const name = m[1]!;
    const endMarker = `<!-- persona:managed:${name}:end -->`;
    const endAt = content.indexOf(endMarker, m.index + m[0].length);
    const stop = endAt < 0 ? content.length : endAt + endMarker.length;
    if (m.index > at) parts.push({ kind: "human", text: content.slice(at, m.index) });
    const body = content.slice(m.index + m[0].length, endAt < 0 ? content.length : endAt);
    parts.push({ kind: "managed", name, text: content.slice(m.index, stop), body });
    at = stop;
    BEGIN.lastIndex = stop;
  }
  if (at < content.length) parts.push({ kind: "human", text: content.slice(at) });
  return parts;
}

/** The file again from its parts. */
export function joinSpaceMd(parts: readonly SpaceMdPart[]): string {
  return parts.map((p) => p.text).join("");
}

/** The parts the editor shows as fields: every human part with words in it; when there is none, one empty part at the
 *  start (added to `parts` when missing), so the user always has somewhere to write. Whitespace-only parts between
 *  blocks stay as they are, unshown. */
export function editableParts(parts: readonly SpaceMdPart[]): { parts: SpaceMdPart[]; editable: number[] } {
  const editable = parts.flatMap((p, i) => (p.kind === "human" && p.text.trim() !== "" ? [i] : []));
  if (editable.length) return { parts: [...parts], editable };
  if (parts[0]?.kind === "human") return { parts: [...parts], editable: [0] };
  return { parts: [{ kind: "human", text: "" }, ...parts], editable: [0] };
}

/** The file from `parts` with the fields' words (`edits`, by part index) in place of their human parts. A field written
 *  adjacent to a block keeps a line break between them, so the block's markers stay on lines of their own. */
export function assembleSpaceMd(parts: readonly SpaceMdPart[], edits: Readonly<Record<number, string>>): string {
  return parts
    .map((p, i) => {
      if (p.kind !== "human" || edits[i] === undefined) return p.text;
      let text = edits[i]!;
      if (text !== "") {
        if (parts[i - 1]?.kind === "managed" && !text.startsWith("\n")) text = `\n\n${text}`;
        if (parts[i + 1]?.kind === "managed" && !text.endsWith("\n")) text = `${text}\n\n`;
      }
      return text;
    })
    .join("");
}

function managedParts(s: string): string[] {
  const markers = [...s.matchAll(MARKER)];
  const out: string[] = [];
  for (let i = 0; i < markers.length; i++) {
    const m = markers[i]!;
    if (m[2] === "begin") {
      let j = i + 1;
      while (j < markers.length && !(markers[j]![1] === m[1] && markers[j]![2] === "end")) j++;
      if (j < markers.length) {
        const end = markers[j]!;
        out.push(s.slice(m.index, end.index + end[0].length));
        i = j;
        continue;
      }
    }
    out.push(m[0]);
  }
  return out;
}

/** Whether every managed block and marker of `after` is byte for byte one of `before`'s, in the same order (what the hub checks). */
export function sameManagedBlocks(before: string, after: string): boolean {
  const eq = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);
  const blocks = (s: string) => splitSpaceMd(s).flatMap((p) => (p.kind === "managed" ? [p.text] : []));
  return eq(blocks(before), blocks(after)) && eq(managedParts(before), managedParts(after));
}

/** SPACE.md without the blocks Persona keeps (`<!-- persona:managed:… -->`): what the user wrote, as the
 *  space's home shows it. */
export function humanSpaceMd(content: string): string {
  return splitSpaceMd(content)
    .flatMap((p) => (p.kind === "human" && p.text.trim() ? [p.text.trim()] : []))
    .join("\n\n");
}

/** The user's part of SPACE.md without a leading `# <title>` that only repeats the space's title shown above it. */
export function spaceMdBody(content: string, title: string): string {
  const text = humanSpaceMd(content);
  const m = /^#[ \t]+(.+?)[ \t]*#*[ \t]*(?:\n|$)/.exec(text);
  if (!m || m[1].trim().toLowerCase() !== title.trim().toLowerCase()) return text;
  return text.slice(m[0].length).trimStart();
}
