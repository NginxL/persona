import { describe, expect, it } from "vitest";
import { codePathRef, findPathRefs, splitAtRefs, uniquePaths } from "./path-refs";

const paths = (t: string) => findPathRefs(t).map((r) => (r.line ? `${r.path}:${r.line}${r.col ? `:${r.col}` : ""}` : r.path));

describe("findPathRefs", () => {
  it("finds absolute, ~ and relative paths with a folder, where a message names them", () => {
    expect(paths("读 /tmp/persona-tasks/1664-lark-test.md 并完整执行这个任务")).toEqual(["/tmp/persona-tasks/1664-lark-test.md"]);
    expect(paths("看 ~/code/app/README.md 和 web/src/lib/api.ts")).toEqual(["~/code/app/README.md", "web/src/lib/api.ts"]);
    expect(paths("目录 /var/log/ 里")).toEqual(["/var/log/"]);
  });

  it("takes a line: path:12, path:12:5, path:12-20, path#L12", () => {
    expect(paths("见 server/internal/node/files.go:63 和 web/src/ui/Markdown.tsx:322:7")).toEqual(["server/internal/node/files.go:63", "web/src/ui/Markdown.tsx:322:7"]);
    expect(paths("改了 src/a.ts:12-20，还有 /x/y/b.go#L8")).toEqual(["src/a.ts:12", "/x/y/b.go:8"]);
    const [r] = findPathRefs("at src/a.ts:12.");
    expect(r).toMatchObject({ path: "src/a.ts", line: 12 });
    expect("at src/a.ts:12.".slice(r.start, r.end)).toBe("src/a.ts:12");
  });

  it("ends an ASCII path at Chinese text, full-width punctuation and a sentence's full stop", () => {
    expect(paths("写到 /tmp/a.md里了")).toEqual(["/tmp/a.md"]);
    expect(paths("已写入 /tmp/out/report.md。")).toEqual(["/tmp/out/report.md"]);
    expect(paths("See /tmp/out/report.md.")).toEqual(["/tmp/out/report.md"]);
    expect(paths("（/tmp/a/b.txt）")).toEqual(["/tmp/a/b.txt"]);
  });

  it("takes names in any script outside backticks", () => {
    // a Chinese file name, Chinese folders, with and without an extension, a folder
    expect(paths("已保存到 /home/me/.persona/assistant/report/报表_2026H2.xlsx")).toEqual(["/home/me/.persona/assistant/report/报表_2026H2.xlsx"]);
    expect(paths("在 ~/文档/项目/notes.md 里")).toEqual(["~/文档/项目/notes.md"]);
    expect(paths("合同在 /home/me/资料/合同 和 /var/log/归档/")).toEqual(["/home/me/资料/合同", "/var/log/归档/"]);
    // Japanese, Korean, accents (a combining one too), mixed scripts
    expect(paths("~/ドキュメント/会議メモ.txt と /Users/me/デスクトップ/レポート.pdf")).toEqual(["~/ドキュメント/会議メモ.txt", "/Users/me/デスクトップ/レポート.pdf"]);
    expect(paths("~/문서/보고서.hwp, ~/Café/menu\u0301.txt")).toEqual(["~/문서/보고서.hwp", "~/Café/menu\u0301.txt"]);
    expect(paths("/srv/data/2026年/sales-売上_v2.csv")).toEqual(["/srv/data/2026年/sales-売上_v2.csv"]);
    // the line, with a Chinese word after it
    expect(paths("见 /tmp/报告/main.go:12行 和 ~/项目/说明.md#L3")).toEqual(["/tmp/报告/main.go:12", "~/项目/说明.md:3"]);
    // a relative one has an ASCII first folder in text, so the words before it stay out
    expect(paths("请看docs/设计/说明.md")).toEqual(["docs/设计/说明.md"]);
    expect(paths("请看文档/报告.md")).toEqual([]);
    // where it is: UTF-16, past a character outside the BMP
    const t = "𠀋 /tmp/𠀋/报告.md";
    const [r] = findPathRefs(t);
    expect(t.slice(r.start, r.end)).toBe("/tmp/𠀋/报告.md");
  });

  it("ends a path at Chinese and Japanese punctuation, not at the names", () => {
    expect(paths("已写入 /tmp/报告/第四季度.docx。")).toEqual(["/tmp/报告/第四季度.docx"]);
    expect(paths("先看 /tmp/报告.md，再看 /tmp/数据.csv、/tmp/图表.png；")).toEqual(["/tmp/报告.md", "/tmp/数据.csv", "/tmp/图表.png"]);
    expect(paths("（/tmp/资料/a.txt）「~/笔记/待办.md」『~/メモ/買い物.txt』")).toEqual(["/tmp/资料/a.txt", "~/笔记/待办.md", "~/メモ/買い物.txt"]);
    expect(paths("见：/tmp/报告.md！")).toEqual(["/tmp/报告.md"]);
  });

  it("ends a name before a Chinese or Japanese word right after its extension", () => {
    expect(paths("写到 /tmp/报告.md里了")).toEqual(["/tmp/报告.md"]);
    expect(paths("~/ドキュメント/メモ.txtを開いて")).toEqual(["~/ドキュメント/メモ.txt"]);
    // two paths joined by the Chinese word for "and"
    expect(paths("/tmp/报告.md和/tmp/数据.csv")).toEqual(["/tmp/报告.md", "/tmp/数据.csv"]);
  });

  it("leaves URLs, words with a slash, versions and lone roots alone", () => {
    expect(paths("https://example.com/a/b.md and //cdn/x/y.js")).toEqual([]);
    expect(paths("model deepseek/v4.1-flash, and/or, /tmp alone, 1/2")).toEqual([]);
    expect(paths("a ~ b / c")).toEqual([]);
    // a path right after a URL and its punctuation
    expect(paths("链接 https://example.com/a，文件在/tmp/报告/a.md")).toEqual(["/tmp/报告/a.md"]);
  });

  it("takes a path in backticks whole, non-ASCII names and spaces too", () => {
    expect(paths("生成了 `~/报告/第四季度.docx`")).toEqual(["~/报告/第四季度.docx"]);
    expect(paths("在 `~/My Notes/todo.md:3`")[0]).toBe("~/My Notes/todo.md:3");
    // both the whole span and the paths in it are candidates; what is there decides
    expect(paths("`/tmp/a.md 和 /tmp/b.md`")).toEqual(["/tmp/a.md 和 /tmp/b.md", "/tmp/a.md", "/tmp/b.md"]);
  });

  it("takes the line off a whole path in backticks", () => {
    // what fs.stat is asked about is the path alone; the line is where to open it
    expect(codePathRef("/tmp/x/a.md:12")).toMatchObject({ path: "/tmp/x/a.md", line: 12 });
    expect(codePathRef("~/报告/x.docx#L3")).toMatchObject({ path: "~/报告/x.docx", line: 3 });
    expect(findPathRefs("在 `~/My Notes/todo.md:3:5`")[0]).toMatchObject({ path: "~/My Notes/todo.md", line: 3, col: 5 });
    expect(uniquePaths(findPathRefs("见 `/tmp/x/a.md:12`"))).toEqual(["/tmp/x/a.md"]);
    expect(codePathRef("/tmp/x/a.md")).toMatchObject({ path: "/tmp/x/a.md", line: undefined });
  });

  it("finishes quickly on a long part without an extension, in backticks and in text (#8894)", () => {
    // a 32-character hash as the last part: the relative branch fails for want of an extension, and overlapping
    // branches in a name would try every way to split the hash before giving up
    const key = "bucket/2024/1/1/0123456789abcdef0123456789abcdef";
    const started = performance.now();
    expect(paths(`the image key is \`${key}\``)).toEqual([]);
    expect(paths(`the image key is ${key} in the store`)).toEqual([]);
    expect(paths(`saved to /srv/${key} and ~/${key}/`)).toEqual([`/srv/${key}`, `~/${key}/`]);
    expect(performance.now() - started).toBeLessThan(200);
  });

  it("asks about each path once", () => {
    expect(uniquePaths(findPathRefs("src/a.ts:1 src/a.ts:2 `src/a.ts`"))).toEqual(["src/a.ts"]);
  });
});

describe("codePathRef", () => {
  it.each(["/tmp/report.", "/tmp/report...", "~/notes/result..", "/tmp/My Notes/report."])("keeps the exact code path %j including its final dots", (path) => {
    expect(codePathRef(path)).toMatchObject({ path, start: 0, end: path.length });
    const text = `See \`${path}\``;
    const refs = findPathRefs(text);
    expect(refs[0].path).toBe(path);
    expect(text.slice(refs[0].start, refs[0].end)).toBe(path);
    const shortened = path.replace(/\.+$/, "");
    expect(uniquePaths(refs)).not.toContain(shortened);
    expect(splitAtRefs(text, refs, (p) => p === shortened)).toEqual([text]);
    expect(splitAtRefs(text, refs, (p) => p === path)[1]).toMatchObject({ path });
  });

  it("keeps a code path's dots with its line while plain prose still trims punctuation", () => {
    expect(codePathRef(" /tmp/report.:12:5 ")).toMatchObject({ path: "/tmp/report.", line: 12, col: 5, start: 1 });
    expect(codePathRef("~/notes/result..#L8")).toMatchObject({ path: "~/notes/result..", line: 8 });
    expect(paths("See /tmp/report... and ~/notes/result..")).toEqual(["/tmp/report", "~/notes/result"]);
  });

  it("is a code span that is a whole path", () => {
    expect(codePathRef("src/a.ts:12")).toMatchObject({ path: "src/a.ts", line: 12 });
    expect(codePathRef(" /tmp/x/y.md ")).toMatchObject({ path: "/tmp/x/y.md", start: 1, end: 12 });
    expect(codePathRef("~/报告/第四季度.docx")).toMatchObject({ path: "~/报告/第四季度.docx" });
    expect(codePathRef("docs/设计/说明.md:7")).toMatchObject({ path: "docs/设计/说明.md", line: 7 });
    expect(codePathRef("和/或")).toBeUndefined();
    expect(codePathRef("ls /tmp/x/y.md")).toBeUndefined();
    expect(codePathRef("/tmp")).toBeUndefined();
    expect(codePathRef("https://example.com/a/b")).toBeUndefined();
  });
});

describe("splitAtRefs", () => {
  it("cuts at the refs that are there, the longer at the same place first, none overlapping", () => {
    const t = "`/tmp/a.md 和 /tmp/b.md`";
    const there = new Set(["/tmp/a.md", "/tmp/b.md"]);
    const parts = splitAtRefs(t, findPathRefs(t), (p) => there.has(p));
    expect(parts.map((p) => (typeof p === "string" ? p : `[${p.path}]`))).toEqual(["`", "[/tmp/a.md]", " 和 ", "[/tmp/b.md]", "`"]);
    expect(splitAtRefs("x /tmp/a/b.md", findPathRefs("x /tmp/a/b.md"), () => false)).toEqual(["x /tmp/a/b.md"]);
  });
});
