import { DOMParser } from "@xmldom/xmldom";
import JSZip from "jszip";
import { describe, expect, it } from "vitest";
import * as XLSX from "xlsx";
import { diffLines, docxUnits, flatten, hunks, pairRows, shownText, workbookUnits } from "./compare";

const parse = (s: string) => new DOMParser().parseFromString(s, "text/xml") as unknown as Document;

describe("diffLines", () => {
  it("finds the smallest edit, like git", () => {
    const ops = diffLines(["a", "b", "c", "d"], ["a", "c", "d", "e"])!;
    expect(ops.map((o) => `${o.kind[0]}${o.text}`)).toEqual(["sa", "db", "sc", "sd", "ae"]);
    expect(diffLines([], ["x"])).toEqual([{ kind: "add", text: "x" }]);
    expect(diffLines(["x"], [])).toEqual([{ kind: "del", text: "x" }]);
    expect(diffLines(["same"], ["same"])).toEqual([{ kind: "same", text: "same" }]);
  });

  it("gives up on what is too big to show", () => {
    expect(diffLines(Array(10).fill("a"), Array(10).fill("b"), 15)).toBeNull();
    expect(diffLines(["a", "b", "c"], ["x", "y", "z"], 100, 2)).toBeNull();
  });
});

describe("hunks", () => {
  it("keeps context, folds the rest, and says which page a change is on", () => {
    const before = flatten([
      { label: "第 1 页", lines: ["标题", "一", "二", "三", "四", "五"] },
      { label: "第 2 页", lines: ["甲", "乙", "丙"] },
    ]);
    const after = flatten([
      { label: "第 1 页", lines: ["标题", "一", "二", "三", "四", "五"] },
      { label: "第 2 页", lines: ["甲", "乙（改）", "丙"] },
    ]);
    const hs = hunks(diffLines(before, after)!, 1);
    expect(hs).toHaveLength(1);
    expect(hs[0].unit).toBe("第 2 页");
    expect(hs[0].ops.map((o) => `${o.kind[0]}${o.text}`)).toEqual(["s甲", "d乙", "a乙（改）", "s丙"]);
    expect(hs[0].skippedBefore).toBeGreaterThan(5);
  });

  it("shows an added page's header as its name", () => {
    expect(shownText(flatten([{ label: "第 3 页", lines: [] }])[0])).toBe("—— 第 3 页 ——");
  });

  it("does not bleed context from an unchanged earlier page or count unit headers as skipped lines", () => {
    const before = flatten([
      { label: "Page 1", lines: ["title", "last of p1"] },
      { label: "Page 2", lines: ["first of p2", "body"] },
    ]);
    const after = flatten([
      { label: "Page 1", lines: ["title", "last of p1"] },
      { label: "Page 2", lines: ["first of p2 (edited)", "body"] },
    ]);
    const hs = hunks(diffLines(before, after)!, 2);
    expect(hs).toHaveLength(1);
    expect(hs[0].unit).toBe("Page 2");
    expect(hs[0].ops.map((o) => `${o.kind[0]}${o.text}`)).toEqual(["dfirst of p2", "afirst of p2 (edited)", "sbody"]);
    expect(hs[0].skippedBefore).toBe(2);
  });

  it("keeps changes at the boundary of two pages in separate hunks under each page's heading", () => {
    const before = flatten([
      { label: "Page 1", lines: ["intro", "end of p1"] },
      { label: "Page 2", lines: ["start of p2", "outro"] },
    ]);
    const after = flatten([
      { label: "Page 1", lines: ["intro", "end of p1 (edited)"] },
      { label: "Page 2", lines: ["start of p2 (edited)", "outro"] },
    ]);
    const hs = hunks(diffLines(before, after)!, 2);
    expect(hs.map((h) => h.unit)).toEqual(["Page 1", "Page 2"]);
    expect(hs[0].ops.map((o) => `${o.kind[0]}${o.text}`)).toEqual(["sintro", "dend of p1", "aend of p1 (edited)"]);
    expect(hs[0].skippedBefore).toBe(0);
    expect(hs[1].ops.map((o) => `${o.kind[0]}${o.text}`)).toEqual(["dstart of p2", "astart of p2 (edited)", "soutro"]);
    expect(hs[1].skippedBefore).toBe(0);

    const onlyP1 = flatten([
      { label: "Page 1", lines: ["intro", "end of p1 (edited)"] },
      { label: "Page 2", lines: ["start of p2", "outro"] },
    ]);
    const hsTail = hunks(diffLines(before, onlyP1)!, 2);
    expect(hsTail).toHaveLength(1);
    expect(hsTail[0].unit).toBe("Page 1");
    expect(hsTail[0].ops.map((o) => `${o.kind[0]}${o.text}`)).toEqual(["sintro", "dend of p1", "aend of p1 (edited)"]);
  });
});

describe("readers", () => {
  it("reads a Word document's paragraphs by page", async () => {
    const W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"';
    const z = new JSZip();
    z.file(
      "word/document.xml",
      `<w:document ${W}><w:body><w:p><w:r><w:t>第一段</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>格子</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
        `<w:p><w:r><w:br w:type="page"/><w:t>下一页</w:t></w:r><w:del><w:r><w:delText>删掉的</w:delText></w:r></w:del></w:p></w:body></w:document>`,
    );
    expect(await docxUnits(z, parse)).toEqual([
      { label: "第 1 页", lines: ["第一段", "格子"] }, // a one-cell row
      { label: "第 2 页", lines: ["下一页"] },
    ]);
  });

  it("reads a workbook's sheets as tab-separated rows", () => {
    const wb = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([["区域", "金额"], ["华东", 12]]), "汇总");
    expect(workbookUnits(XLSX, wb)).toEqual([{ label: "工作表「汇总」", lines: ["区域\t金额", "华东\t12"] }]);
  });
});

describe("table rows", () => {
  it("reads a Word table row per line and shows changed cells old → new", async () => {
    const W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"';
    const cell = (t: string) => `<w:tc><w:p><w:r><w:t>${t}</w:t></w:r></w:p></w:tc>`;
    const z = new JSZip();
    z.file("word/document.xml", `<w:document ${W}><w:body><w:tbl><w:tr>${cell("区域")}${cell("环比")}</w:tr><w:tr>${cell("华东")}${cell("+10.2%")}</w:tr></w:tbl></w:body></w:document>`);
    expect((await docxUnits(z, parse))[0].lines).toEqual(["区域 | 环比", "华东 | +10.2%"]);
    const ops = diffLines(["区域 | 环比", "华东 | +9.8%", "正文"], ["区域 | 环比", "华东 | +10.2%", "正文改"])!;
    expect(pairRows(ops)).toEqual([
      { kind: "same", text: "区域 | 环比" },
      { kind: "row", cells: [{ old: "华东", new: "华东" }, { old: "+9.8%", new: "+10.2%" }] },
      { kind: "del", text: "正文" },
      { kind: "add", text: "正文改" },
    ]);
  });
});
