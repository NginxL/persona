/// <reference types="node" />
// session-review.ts numbers a turn's hunks; the code viewer (code-view.ts: trees, highlighting, split rows) is a page
// of its own. Importing hunkRows from code-view.ts put that whole module wherever session-review.ts loaded.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { hunkRows as fromCodeView } from "./code-view";
import { hunkRows } from "./hunk-rows";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");

describe("hunk rows", () => {
  it("keeps code-view.ts out of session-review.ts's runtime imports", () => {
    const source = read("./session-review.ts");
    expect(source).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/code-view["']/m);
    expect(source).toMatch(/^import\s+\{\s*hunkRows\s*\}\s+from\s+["']\.\/hunk-rows["']/m);
  });

  it("is the same function code-view.ts exports to the viewer", () => {
    expect(fromCodeView).toBe(hunkRows);
    expect(hunkRows({ old_start: 3, new_start: 7, lines: [" a", "-b", "+c", "\\ No newline at end of file"] })).toEqual([
      { kind: "context", old: 3, new: 7, text: "a", oldAt: 0, newAt: 0 },
      { kind: "del", old: 4, text: "b", oldAt: 1 },
      { kind: "add", new: 8, text: "c", newAt: 1 },
      { kind: "note", text: "No newline at end of file" },
    ]);
  });
});
