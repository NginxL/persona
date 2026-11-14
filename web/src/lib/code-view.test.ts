// @vitest-environment node
import { describe, expect, it } from "vitest";
import { buildTree, filterEntries, gapBefore, hunkRows, hunkSides, languageOf, placeThreads, splitRows, tokenLines, wideView, type Hunk } from "./code-view";

describe("文件树", () => {
  it("builds directories from paths, folds single chains, sorts directories first", () => {
    const tree = buildTree([{ path: "src/upload/retry.go" }, { path: "src/upload/client.go" }, { path: "README.md" }, { path: "docs/a/b.md" }]);
    expect(tree.map((n) => n.name)).toEqual(["docs/a", "src/upload", "README.md"]);
    const src = tree[1];
    expect(src.path).toBe("src/upload");
    expect(src.children.map((n) => n.name)).toEqual(["client.go", "retry.go"]);
    expect(src.files).toBe(2);
  });

  it("keeps a directory given with nothing under it, to load", () => {
    const tree = buildTree([{ path: "src", kind: "dir" }, { path: "docs", kind: "dir" }, { path: "docs/x.md" }, { path: "go.mod" }]);
    const src = tree.find((n) => n.name === "src")!;
    expect(src.unloaded).toBe(true);
    expect(tree.find((n) => n.name === "docs")!.unloaded).toBe(false);
  });

  it("filters files by path", () => {
    expect(filterEntries([{ path: "a/Retry.go" }, { path: "b.go" }, { path: "a", kind: "dir" }], "retry").map((e) => e.path)).toEqual(["a/Retry.go"]);
  });
});

const hunk: Hunk = { old_start: 20, new_start: 20, lines: [" a", "-b", "-c", "+B", " d", "\\ No newline at end of file"] };

describe("diff", () => {
  it("numbers the lines of a hunk on both sides", () => {
    const rows = hunkRows(hunk);
    expect(rows.map((r) => [r.kind, r.old, r.new])).toEqual([
      ["context", 20, 20], ["del", 21, undefined], ["del", 22, undefined], ["add", undefined, 21], ["context", 23, 22], ["note", undefined, undefined],
    ]);
    expect(rows[3].newAt).toBe(1);
    expect(hunkSides(hunk)).toEqual({ old: "a\nb\nc\nd", new: "a\nB\nd" });
  });

  it("pairs deletions with the additions after them side by side", () => {
    const split = splitRows(hunkRows(hunk));
    expect(split.map((r) => r.note ?? `${r.old?.text ?? "_"}|${r.new?.text ?? "_"}`)).toEqual(["a|a", "b|B", "c|_", "d|d", "No newline at end of file"]);
  });

  it("knows the lines hidden between hunks", () => {
    const hs: Hunk[] = [{ old_start: 1, new_start: 5, lines: [" a", "+b"] }, { old_start: 30, new_start: 31, lines: [" z"] }];
    expect(gapBefore(hs, 0)).toBe(4);
    expect(gapBefore(hs, 1)).toBe(24);
  });

  it("puts threads at their lines, the rest at the end", () => {
    const c = [{ author: "a" }];
    const { at, rest } = placeThreads(
      [{ side: "new", line: 21, comments: c }, { side: "old", line: 22, comments: c }, { side: "new", line: 99, comments: c }, { side: "new", line: 20, outdated: true, comments: c }],
      [hunk],
    );
    expect([...at.keys()].sort()).toEqual(["new:21", "old:22"]);
    expect(rest.map((t) => t.line)).toEqual([99, 20]);
  });
});

describe("高亮", () => {
  it("picks a grammar from the language or the path", () => {
    expect(languageOf("Go")).toBe("go");
    expect(languageOf(undefined, "src/a.tsx")).toBe("typescript");
    expect(languageOf(undefined, "src/app.ts")).toBe("typescript"); // a .ts the hub sent as text (#7974)
    expect(languageOf(undefined, "Makefile")).toBe("makefile");
    expect(languageOf(undefined, "x.unknown")).toBe("");
  });

  it("splits a highlighted tree into lines, keeping what is open across a break", () => {
    const tree = {
      type: "root",
      children: [
        { type: "element", properties: { className: ["hljs-comment"] }, children: [{ type: "text", value: "/* a\nb */" }] },
        { type: "text", value: " x\ny" },
      ],
    };
    expect(tokenLines(tree)).toEqual([[{ text: "/* a", cls: "hljs-comment" }], [{ text: "b */", cls: "hljs-comment" }, { text: " x", cls: "" }], [{ text: "y", cls: "" }]]);
  });

  it("highlights with lowlight's grammars", async () => {
    const { createLowlight, common } = await import("lowlight");
    const lines = tokenLines(createLowlight(common).highlight("go", "package a\n// hi") as never);
    expect(lines[0][0]).toEqual({ text: "package", cls: "hljs-keyword" });
    expect(lines[1][0].cls).toBe("hljs-comment");
  });
});

it("wants a wide page for split views, code and diffs", () => {
  expect(wideView({ layout: "split", sections: [] })).toBe(true);
  expect(wideView({ sections: [{ items: [{ type: "diff" }] }] })).toBe(true);
  expect(wideView({ sections: [{ items: [{ type: "row" }] }] })).toBe(false);
});
