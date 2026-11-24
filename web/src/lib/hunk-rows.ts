// A hunk's lines as numbered rows. Its own module because the chat's first screen needs only this (session-review.ts
// quoting a turn's changes): importing it from code-view.ts put the whole code viewer on every first screen.
import type { Hunk } from "./code-view";

export type RowKind = "context" | "add" | "del" | "note";

/** One line of a hunk: its kind, its numbers in the old and the new file, its text without the prefix. */
export interface DiffRow {
  kind: RowKind;
  old?: number;
  new?: number;
  text: string;
  /** Its place among the old (or new) side's lines of the hunk: where its highlighting is. */
  oldAt?: number;
  newAt?: number;
}

export function hunkRows(h: Hunk): DiffRow[] {
  let o = h.old_start;
  let n = h.new_start;
  let oi = 0;
  let ni = 0;
  return h.lines.map((line): DiffRow => {
    const sign = line[0] ?? " ";
    const text = line.slice(1);
    if (sign === "+") return { kind: "add", new: n++, text, newAt: ni++ };
    if (sign === "-") return { kind: "del", old: o++, text, oldAt: oi++ };
    if (sign === "\\") return { kind: "note", text: line.slice(1).trim() };
    return { kind: "context", old: o++, new: n++, text, oldAt: oi++, newAt: ni++ };
  });
}
