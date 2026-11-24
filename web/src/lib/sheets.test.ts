import { describe, expect, it } from "vitest";
import * as XLSX from "xlsx";
import {
  cellName,
  columnAt,
  columnName,
  columnWidths,
  coveredCells,
  delimitedSheet,
  expandToMerges,
  looksNumeric,
  MAX_COL,
  MIN_COL,
  normalize,
  offsetsOf,
  parseDelimited,
  rangeSummary,
  rangeTSV,
  separatorOf,
  workbookSheets,
} from "./sheets";

describe("parseDelimited", () => {
  it("reads RFC 4180 CSV, values exactly as written", () => {
    const text = '﻿id,name,note\r\n001,"Smith, J","said ""hi""\nthen left"\n2,,\n';
    const { rows, cols, cut } = parseDelimited(text, ",");
    expect(rows).toEqual([
      ["id", "name", "note"],
      ["001", "Smith, J", 'said "hi"\nthen left'],
      ["2", "", ""],
    ]);
    expect(cols).toBe(3);
    expect(cut).toBe(false);
  });

  it("keeps a last line without newline, a trailing separator, and ragged rows", () => {
    expect(parseDelimited("a\tb\nc\t", "\t").rows).toEqual([
      ["a", "b"],
      ["c", ""],
    ]);
    const r = parseDelimited("a\nb,c,d", ",");
    expect(r.cols).toBe(3);
    expect(r.rows[0]).toEqual(["a"]);
  });

  it("stops at maxRows and says so", () => {
    const r = parseDelimited("1\n2\n3\n4\n", ",", 2);
    expect(r.rows).toEqual([["1"], ["2"]]);
    expect(r.cut).toBe(true);
  });

  it("guesses the separator", () => {
    expect(separatorOf("x.tsv", "a,b")).toBe("\t");
    expect(separatorOf("x.csv", "a;b;c\n1;2;3")).toBe(";");
    expect(separatorOf("x.csv", "a,b\n1,2")).toBe(",");
    expect(separatorOf("x.csv", "single")).toBe(",");
    expect(delimitedSheet("t.tsv", "a\tb\n1\t2").rows[1]).toEqual(["1", "2"]);
  });

  it.each([
    ["name;notes\nAlice;\"one,two,three,four\"\n", ";", "one,two,three,four"],
    ["name,notes\nAlice,\"one;two;three;four\"\n", ",", "one;two;three;four"],
    ['name;notes\nAlice;"say ""hello, friend"", please"\n', ";", 'say "hello, friend", please'],
    ['\uFEFF"name";"notes"\n"Alice";"one,two,three,four"\n', ";", "one,two,three,four"],
    ['name;notes\r\nAlice;"one,two\r\nthree,four"\r\n', ";", "one,two\r\nthree,four"],
  ])("does not count quoted punctuation as a separator in %j", (text, separator, note) => {
    expect.soft(separatorOf("data.csv", text)).toBe(separator);
    expect.soft(delimitedSheet("data.csv", text).rows).toEqual([["name", "notes"], ["Alice", note]]);
  });

  it("keeps literal quotes in unquoted fields and the existing format defaults", () => {
    const literal = 'a;b,c\ninch";2';
    expect(separatorOf("data.csv", literal)).toBe(";");
    expect(delimitedSheet("data.csv", literal).rows).toEqual([["a", "b,c"], ['inch"', "2"]]);
    expect(separatorOf("data.csv", "a,b;c")).toBe(",");
    expect(separatorOf("data.csv", "single")).toBe(",");
    expect(separatorOf("data.csv", "")).toBe(",");
    for (const name of ["data.tsv", "data.tab"]) expect(separatorOf(name, 'a;b\n"c,d,e,f"')).toBe("\t");
  });

  it("keeps the 4096-code-unit and first-five-physical-line sampling bounds", () => {
    expect(separatorOf("data.csv", "x".repeat(4095) + "," + ";".repeat(20))).toBe(",");
    expect(separatorOf("data.csv", "😀".repeat(2047) + ",;" + "\t".repeat(20))).toBe(",");
    expect(separatorOf("data.csv", "a,b\n".repeat(5) + ";".repeat(20))).toBe(",");
  });
});

describe("workbookSheets", () => {
  it("reads every sheet's formatted text, merges and set widths", () => {
    const wb = XLSX.utils.book_new();
    const a = XLSX.utils.aoa_to_sheet([
      ["Region", "Q1", "Q2"],
      ["North", 1234.5, 0.25],
      ["Total", "", ""],
    ]);
    a["B2"].z = "#,##0.00";
    a["C2"].z = "0%";
    a["!merges"] = [{ s: { r: 2, c: 0 }, e: { r: 2, c: 2 } }];
    a["!cols"] = [{ wch: 20 }];
    XLSX.utils.book_append_sheet(wb, a, "Sales");
    XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([["only"]]), "Notes");
    const bytes = XLSX.write(wb, { type: "array", bookType: "xlsx" }) as ArrayBuffer;
    const sheets = workbookSheets(XLSX, XLSX.read(new Uint8Array(bytes), { type: "array", dense: true, cellDates: true, cellStyles: true }));
    expect(sheets.map((s) => s.name)).toEqual(["Sales", "Notes"]);
    expect(sheets[0].rows[1]).toEqual(["North", "1,234.50", "25%"]);
    expect(sheets[0].rows[2]).toEqual(["Total"]); // trailing empty cells dropped
    expect(sheets[0].cols).toBe(3);
    expect(sheets[0].merges).toEqual([{ r0: 2, c0: 0, r1: 2, c1: 2 }]);
    expect(sheets[0].widths[0]).toBeGreaterThan(100);
    expect(sheets[1].rows).toEqual([["only"]]);
  });

  it("reads an empty sheet", () => {
    const wb = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(wb, XLSX.utils.aoa_to_sheet([]), "Empty");
    const bytes = XLSX.write(wb, { type: "array", bookType: "xlsx" }) as ArrayBuffer;
    expect(workbookSheets(XLSX, XLSX.read(new Uint8Array(bytes), { type: "array", dense: true }))[0]).toMatchObject({ name: "Empty", rows: [], cols: 0 });
  });
});

describe("names and geometry", () => {
  it("names columns and cells like a spreadsheet", () => {
    expect([0, 25, 26, 51, 52, 701, 702].map(columnName)).toEqual(["A", "Z", "AA", "AZ", "BA", "ZZ", "AAA"]);
    expect(cellName(9, 2)).toBe("C10");
  });

  it("fits widths to the text within bounds, wide characters counting double", () => {
    const sheet = delimitedSheet("x.csv", `a,${"x".repeat(500)},中文中文中文中文中文\n`);
    const w = columnWidths(sheet);
    expect(w[0]).toBe(MIN_COL);
    expect(w[1]).toBe(MAX_COL);
    expect(w[2]).toBeGreaterThan(columnWidths(delimitedSheet("x.csv", "a,b,abcdefghij\n"))[2]);
  });

  it("finds the column at an x", () => {
    const o = offsetsOf([50, 100, 30]);
    expect(o).toEqual([0, 50, 150, 180]);
    expect([0, 49, 50, 149, 150, 179, 500].map((x) => columnAt(o, x))).toEqual([0, 0, 1, 1, 2, 2, 2]);
    expect(columnAt([0], 10)).toBe(0);
  });

  it("tells numbers from text", () => {
    for (const s of ["12", "-3.5", "1,234.50", "25%", "$4", "¥1,000", "1e5", "(12)"]) expect(looksNumeric(s), s).toBe(true);
    for (const s of ["", "abc", "12a", "2026-11-04", "v1.2"]) expect(looksNumeric(s), s).toBe(false);
  });

  it("rejects unmatched parentheses as text instead of accounting numbers", () => {
    for (const s of ["1)", "2)", "(12", "-5)", "+5)", "(-5)", "(+5)"]) expect(looksNumeric(s), s).toBe(false);
    expect(rangeSummary([["1)", "2)"]], { r0: 0, c0: 0, r1: 0, c1: 1 })).toEqual({ count: 2 });
  });
});

describe("selection", () => {
  const rows = [
    ["a", "b", "c"],
    ["1", "2", "x\ty"],
    ["3", '"q"', ""],
  ];

  it("copies a range as TSV a spreadsheet pastes back", () => {
    expect(rangeTSV(rows, normalize({ r: 2, c: 1 }, { r: 1, c: 0 }))).toBe('1\t2\n3\t"""q"""');
    expect(rangeTSV(rows, { r0: 1, c0: 2, r1: 1, c1: 2 })).toBe('"x\ty"');
  });

  it("sums the numbers it covers", () => {
    expect(rangeSummary(rows, { r0: 1, c0: 0, r1: 2, c1: 1 })).toEqual({ count: 4, sum: 6, avg: 2 });
    expect(rangeSummary(rows, { r0: 0, c0: 0, r1: 0, c1: 2 })).toEqual({ count: 3 });
    expect(rangeSummary([["10%", "(5)"]], { r0: 0, c0: 0, r1: 0, c1: 1 }).sum).toBeCloseTo(-4.9);
  });

  it.each([
    ["(5%)", 0.05],
    [" (5.0%) ", 0.05],
    ["(0%)", 0.1],
    ["5%", 0.15],
    ["-5%", 0.05],
    ["(5)", -4.9],
    ["5", 5.1],
  ] as const)("keeps the percent scale when summarizing %j", (value, sum) => {
    const rows = [["10%", value]];
    const range = { r0: 0, c0: 0, r1: 0, c1: 1 };
    const summary = rangeSummary(rows, range);
    expect(summary.count).toBe(2);
    expect(summary.sum).toBeCloseTo(sum);
    expect(summary.avg).toBeCloseTo(sum / 2);
    expect(rangeTSV(rows, range)).toBe(`10%\t${value}`);
  });

  it("summarizes the displayed accounting percentages from a real workbook", () => {
    const source = XLSX.utils.aoa_to_sheet([[0.1, -0.05]]);
    source.A1.z = source.B1.z = "0.0%;(0.0%)";
    const book = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(book, source, "Rates");
    const bytes = XLSX.write(book, { type: "array", bookType: "xlsx" });
    const sheet = workbookSheets(XLSX, XLSX.read(bytes, { type: "array", dense: true, cellStyles: true }))[0];
    expect(sheet.rows).toEqual([["10.0%", "(5.0%)"]]);
    const summary = rangeSummary(sheet.rows, { r0: 0, c0: 0, r1: 0, c1: 1 });
    expect(summary.sum).toBeCloseTo(0.05);
    expect(summary.avg).toBeCloseTo(0.025);
  });

  it("preserves nonnumeric counts, single-number and large-selection summaries", () => {
    expect(rangeSummary([["(5%)", "note", ""]], { r0: 0, c0: 0, r1: 0, c1: 2 })).toEqual({ count: 2 });
    expect(rangeSummary([["a", "b"]], { r0: 0, c0: 0, r1: 0, c1: 1 })).toEqual({ count: 2 });
    expect(rangeSummary([["(5%)"]], { r0: 0, c0: 0, r1: 200_000, c1: 0 })).toEqual({ count: 200_001 });
  });

  it("grows a selection over the merges it touches", () => {
    const merges = [
      { r0: 0, c0: 0, r1: 0, c1: 4 },
      { r0: 0, c0: 5, r1: 2, c1: 5 },
    ];
    expect(expandToMerges({ r0: 0, c0: 2, r1: 0, c1: 2 }, merges)).toEqual({ r0: 0, c0: 0, r1: 0, c1: 4 });
    expect(expandToMerges({ r0: 1, c0: 4, r1: 1, c1: 5 }, merges)).toEqual({ r0: 0, c0: 0, r1: 2, c1: 5 }); // chained
    expect(expandToMerges({ r0: 3, c0: 0, r1: 3, c1: 0 }, merges)).toEqual({ r0: 3, c0: 0, r1: 3, c1: 0 });
  });

  it("widens a column the file set too narrow for its text, keeps a wider one", () => {
    const sheet = { ...delimitedSheet("x.tsv", "120,000.00\ta\n"), widths: [40, 300] };
    const w = columnWidths(sheet);
    expect(w[0]).toBeGreaterThan(60);
    expect(w[1]).toBe(300);
  });

  it("knows which cells a merge hides", () => {
    const s = coveredCells([{ r0: 0, c0: 0, r1: 1, c1: 1 }]);
    expect([...s].sort()).toEqual(["0:1", "1:0", "1:1"]);
  });
});
