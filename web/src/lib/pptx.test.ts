import { DOMParser } from "@xmldom/xmldom";
import JSZip from "jszip";
import { describe, expect, it } from "vitest";
import { CHART_UNSUPPORTED, chartSVG, colorOf, deckHTML, readChart, readPptx, resolvePart } from "./pptx";

const parse = (s: string) => new DOMParser().parseFromString(s, "text/xml") as unknown as Document;
const NS = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"`;
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const rels = (list: [string, string, string][]) =>
  `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${list.map(([id, type, target]) => `<Relationship Id="${id}" Type="${REL}/${type}" Target="${target}"/>`).join("")}</Relationships>`;
// a 1×1 PNG
const PNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

async function deck() {
  const z = new JSZip();
  z.file("ppt/presentation.xml", `<p:presentation ${NS}><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId3"/></p:sldIdLst><p:sldSz cx="12192000" cy="6858000"/></p:presentation>`);
  z.file("ppt/_rels/presentation.xml.rels", rels([["rId2", "slide", "slides/slide1.xml"], ["rId3", "slide", "slides/slide2.xml"]]));
  z.file("ppt/theme/theme1.xml", `<a:theme ${NS}><a:themeElements><a:clrScheme name="t"><a:dk1><a:sysClr val="windowText" lastClr="111111"/></a:dk1><a:lt1><a:srgbClr val="FFFFFF"/></a:lt1><a:accent1><a:srgbClr val="1F6FEB"/></a:accent1></a:clrScheme></a:themeElements></a:theme>`);
  z.file("ppt/slideMasters/slideMaster1.xml", `<p:sldMaster ${NS}><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="F4F4F4"/></a:solidFill></p:bgPr></p:bg><p:spTree>
    <p:sp><p:nvSpPr><p:cNvPr id="2" name="t"/><p:cNvSpPr/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="100" cy="100"/></a:xfrm></p:spPr></p:sp>
    <p:sp><p:nvSpPr><p:cNvPr id="3" name="logo bar"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="0" y="6667500"/><a:ext cx="12192000" cy="190500"/></a:xfrm><a:solidFill><a:schemeClr val="accent1"/></a:solidFill></p:spPr></p:sp>
  </p:spTree></p:cSld><p:txStyles><p:titleStyle><a:lvl1pPr algn="ctr"><a:defRPr sz="4400" b="1"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill></a:defRPr></a:lvl1pPr></p:titleStyle><p:bodyStyle><a:lvl1pPr><a:buChar char="•"/><a:defRPr sz="2800"/></a:lvl1pPr></p:bodyStyle></p:txStyles></p:sldMaster>`);
  z.file("ppt/slideMasters/_rels/slideMaster1.xml.rels", rels([["rId1", "theme", "../theme/theme1.xml"]]));
  z.file("ppt/slideLayouts/slideLayout1.xml", `<p:sldLayout ${NS}><p:cSld><p:spTree>
    <p:sp><p:nvSpPr><p:cNvPr id="2" name="Title"/><p:cNvSpPr/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr><a:xfrm><a:off x="952500" y="476250"/><a:ext cx="10287000" cy="952500"/></a:xfrm></p:spPr></p:sp>
    <p:sp><p:nvSpPr><p:cNvPr id="3" name="Body"/><p:cNvSpPr/><p:nvPr><p:ph idx="1"/></p:nvPr></p:nvSpPr><p:spPr><a:xfrm><a:off x="952500" y="1905000"/><a:ext cx="10287000" cy="3810000"/></a:xfrm></p:spPr></p:sp>
  </p:spTree></p:cSld></p:sldLayout>`);
  z.file("ppt/slideLayouts/_rels/slideLayout1.xml.rels", rels([["rId1", "slideMaster", "../slideMasters/slideMaster1.xml"]]));
  z.file("ppt/slides/slide1.xml", `<p:sld ${NS}><p:cSld><p:spTree>
    <p:sp><p:nvSpPr><p:cNvPr id="2" name="Title 1"/><p:cNvSpPr/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="zh-CN"/><a:t>季度回顾 &lt;script&gt;</a:t></a:r></a:p></p:txBody></p:sp>
    <p:sp><p:nvSpPr><p:cNvPr id="3" name="Content"/><p:cNvSpPr/><p:nvPr><p:ph idx="1"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:p><a:r><a:t>收入增长 </a:t></a:r><a:r><a:rPr b="1" sz="3200"><a:solidFill><a:srgbClr val="C00000"/></a:solidFill></a:rPr><a:t>32%</a:t></a:r></a:p><a:p><a:pPr lvl="1"/><a:r><a:t>海外</a:t></a:r></a:p></p:txBody></p:sp>
    <p:grpSp><p:grpSpPr><a:xfrm><a:off x="9525000" y="4762500"/><a:ext cx="1905000" cy="952500"/><a:chOff x="0" y="0"/><a:chExt cx="3810000" cy="1905000"/></a:xfrm></p:grpSpPr>
      <p:sp><p:nvSpPr><p:cNvPr id="5" name="o"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="1905000" y="0"/><a:ext cx="1905000" cy="1905000"/></a:xfrm><a:prstGeom prst="ellipse"/><a:solidFill><a:schemeClr val="accent1"><a:lumMod val="50000"/></a:schemeClr></a:solidFill></p:spPr></p:sp>
    </p:grpSp>
    <p:pic><p:nvPicPr><p:cNvPr id="6" name="img"/><p:cNvPicPr/><p:nvPr/></p:nvPicPr><p:blipFill><a:blip r:embed="rId5"/></p:blipFill><p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="952500" cy="952500"/></a:xfrm></p:spPr></p:pic>
    <p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="7" name="tbl"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="0" y="5715000"/><a:ext cx="1905000" cy="476250"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl><a:tblGrid><a:gridCol w="952500"/><a:gridCol w="952500"/></a:tblGrid><a:tr h="238125"><a:tc><a:txBody><a:bodyPr/><a:p><a:r><a:t>区域</a:t></a:r></a:p></a:txBody><a:tcPr><a:solidFill><a:srgbClr val="DDEBF7"/></a:solidFill></a:tcPr></a:tc><a:tc><a:txBody><a:bodyPr/><a:p><a:r><a:t>金额</a:t></a:r></a:p></a:txBody></a:tc></a:tr></a:tbl></a:graphicData></a:graphic></p:graphicFrame>
    <p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="8" name="chart"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="4762500" y="5715000"/><a:ext cx="1905000" cy="952500"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/chart"/></a:graphic></p:graphicFrame>
  </p:spTree></p:cSld></p:sld>`);
  z.file("ppt/slides/_rels/slide1.xml.rels", rels([["rId1", "slideLayout", "../slideLayouts/slideLayout1.xml"], ["rId5", "image", "../media/image1.png"], ["rId9", "notesSlide", "../notesSlides/notesSlide1.xml"]]));
  z.file("ppt/media/image1.png", PNG, { base64: true });
  z.file("ppt/notesSlides/notesSlide1.xml", `<p:notes ${NS}><p:cSld><p:spTree><p:sp><p:nvSpPr><p:cNvPr id="2" name="n"/><p:cNvSpPr/><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:p><a:r><a:t>先讲海外</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:notes>`);
  z.file("ppt/slides/slide2.xml", `<p:sld ${NS} show="0" showMasterSp="0"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="000000"/></a:solidFill></p:bgPr></p:bg><p:spTree/></p:cSld></p:sld>`);
  z.file("ppt/slides/_rels/slide2.xml.rels", rels([["rId1", "slideLayout", "../slideLayouts/slideLayout1.xml"]]));
  return new JSZip().loadAsync(await z.generateAsync({ type: "uint8array" }));
}

describe("readPptx", () => {
  it("reads slides with placeholders, theme colors, groups, pictures, tables and notes", async () => {
    const d = await readPptx(await deck(), parse);
    expect([d.width, d.height]).toEqual([1280, 720]);
    expect(d.slides).toHaveLength(2);
    const [s1, s2] = d.slides;
    expect(s1.bg).toBe("#f4f4f4"); // the master's
    expect(s1.notes).toBe("先讲海外");
    // the master's bar is drawn first, its title placeholder is not
    expect(s1.shapes[0]).toMatchObject({ fill: "#1f6feb", y: 700 });
    const title = s1.shapes.find((s) => s.paras?.[0].runs[0].text.startsWith("季度回顾"))!;
    expect(title).toMatchObject({ x: 100, y: 50, w: 1080, h: 100 }); // from the layout
    expect(title.paras![0]).toMatchObject({ align: "ctr" });
    expect(title.paras![0].runs[0]).toMatchObject({ size: 44, bold: true, color: "#111111" }); // master titleStyle, tx1
    const body = s1.shapes.find((s) => s.paras?.[0].runs[0].text === "收入增长 ")!;
    expect(body.paras![0].bullet).toBe("•");
    expect(body.paras![0].runs[1]).toMatchObject({ text: "32%", size: 32, bold: true, color: "#c00000" });
    expect(body.paras![0].runs[0].size).toBe(28);
    expect(body.paras![1].level).toBe(1);
    const circle = s1.shapes.find((s) => s.geom === "ellipse")!;
    expect(circle).toMatchObject({ x: 1100, y: 500, w: 100, h: 100 }); // group child space scaled by half
    expect(circle.fill).toBe("#0b377a"); // accent1 at half lightness
    expect(s1.shapes.find((s) => s.image)?.image).toMatch(/^data:image\/png;base64,/);
    const table = s1.shapes.find((s) => s.table)!.table!;
    expect(table.cols).toEqual([100, 100]);
    expect(table.rows[0].cells.map((c) => c.paras[0].runs[0].text)).toEqual(["区域", "金额"]);
    expect(table.rows[0].cells[0].fill).toBe("#ddebf7");
    expect(s1.shapes.find((s) => s.placeholder)?.placeholder).toBe(CHART_UNSUPPORTED); // no chart part to read
    expect(s2).toMatchObject({ hidden: true, bg: "#000000", shapes: [] }); // its own background, no master drawings
  });

  it("renders escaped HTML", async () => {
    const html = deckHTML(await readPptx(await deck(), parse));
    expect(html).toContain("季度回顾 &lt;script&gt;");
    expect(html).not.toContain("<script");
    expect(html).toContain("border-radius:50%");
    expect(html).toContain('<td style="background:#ddebf7">');
    expect(html).toContain("2 · 已隐藏");
  });

  it("refuses what is not a deck", async () => {
    const z = new JSZip();
    z.file("word/document.xml", "<w/>");
    await expect(readPptx(z, parse)).rejects.toThrow("PowerPoint");
  });
});

describe("helpers", () => {
  it("resolves relationship targets", () => {
    expect(resolvePart("ppt/slides/slide1.xml", "../media/a.png")).toBe("ppt/media/a.png");
    expect(resolvePart("ppt/presentation.xml", "slides/slide1.xml")).toBe("ppt/slides/slide1.xml");
    expect(resolvePart("ppt/slides/slide1.xml", "/ppt/x.xml")).toBe("ppt/x.xml");
  });

  it("applies tints and shades", () => {
    const el = (xml: string) => parse(`<f xmlns:a="a">${xml}</f>`).documentElement;
    expect(colorOf(el(`<a:srgbClr val="FF0000"><a:tint val="50000"/></a:srgbClr>`), {})).toBe("#ff8080");
    expect(colorOf(el(`<a:srgbClr val="FF0000"><a:shade val="50000"/></a:srgbClr>`), {})).toBe("#800000");
    expect(colorOf(el(`<a:schemeClr val="bg1"/>`), { lt1: "#fafafa" })).toBe("#fafafa");
    expect(colorOf(el(`<a:srgbClr val="not-hex"/>`), {})).toBeUndefined();
  });
});

const C = `xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`;
const strs = (vals: string[]) => `<c:strRef><c:f>S!A1</c:f><c:strCache><c:ptCount val="${vals.length}"/>${vals.map((v, i) => `<c:pt idx="${i}"><c:v>${v}</c:v></c:pt>`).join("")}</c:strCache></c:strRef>`;
const nums = (vals: (number | null)[]) =>
  `<c:numRef><c:f>S!B1</c:f><c:numCache><c:ptCount val="${vals.length}"/>${vals.map((v, i) => (v === null ? "" : `<c:pt idx="${i}"><c:v>${v}</c:v></c:pt>`)).join("")}</c:numCache></c:numRef>`;
const ser = (name: string, cats: string[], vals: (number | null)[], extra = "") => `<c:ser><c:idx val="0"/><c:tx>${strs([name])}</c:tx>${extra}<c:cat>${strs(cats)}</c:cat><c:val>${nums(vals)}</c:val></c:ser>`;
const chartDoc = (plot: string, title = "") => parse(`<c:chartSpace ${C}><c:chart>${title}<c:plotArea>${plot}</c:plotArea></c:chart></c:chartSpace>`);
const scheme = { accent1: "#1f6feb", accent2: "#ed7d31", accent3: "#2da44e" };

describe("charts", () => {
  it("reads a column chart's series from the caches", () => {
    const c = readChart(
      chartDoc(
        `<c:barChart><c:barDir val="col"/><c:grouping val="clustered"/>${ser("7 月", ["华东", "华北", "海外"], [120, 151, null])}${ser("8 月", ["华东", "华北", "海外"], [128, 157, 244], `<c:spPr><a:solidFill><a:srgbClr val="C00000"/></a:solidFill></c:spPr>`)}</c:barChart>`,
        `<c:title><c:tx><c:rich><a:p><a:r><a:t>各区域收入</a:t></a:r></a:p></c:rich></c:tx></c:title>`,
      ),
      scheme,
    )!;
    expect(c).toMatchObject({ kind: "col", title: "各区域收入", cats: ["华东", "华北", "海外"] });
    expect(c.series.map((x) => [x.name, x.color])).toEqual([
      ["7 月", "#1f6feb"],
      ["8 月", "#c00000"],
    ]);
    expect(c.series[0].values).toEqual([120, 151, null]);
    const svg = chartSVG(c, 400, 300);
    expect(svg.match(/<rect /g)?.length).toBe(5 + 2); // five bars and two legend keys
    expect(svg).toContain("各区域收入");
  });

  it("reads pies with their slice colors, horizontal bars, lines", () => {
    const pie = readChart(chartDoc(`<c:pieChart>${ser("份额", ["A", "B", "C"], [50, 30, 20], `<c:dPt><c:idx val="1"/><c:spPr><a:solidFill><a:srgbClr val="00FF00"/></a:solidFill></c:spPr></c:dPt>`)}</c:pieChart>`, "<c:title/>"), scheme)!;
    expect(pie).toMatchObject({ kind: "pie", title: "份额" });
    expect(pie.series[0].pointColors).toEqual(["#1f6feb", "#00ff00", "#2da44e"]);
    const svg = chartSVG(pie, 300, 300);
    expect(svg.match(/<path /g)?.length).toBe(3);
    expect(svg).toContain(">50%<");
    expect(readChart(chartDoc(`<c:barChart><c:barDir val="bar"/><c:grouping val="stacked"/>${ser("x", ["a"], [1])}</c:barChart>`), scheme)).toMatchObject({ kind: "bar", stacked: true });
    expect(chartSVG(readChart(chartDoc(`<c:lineChart>${ser("x", ["a", "b"], [1, 3])}</c:lineChart>`), scheme)!, 300, 200)).toContain("<polyline");
  });

  it("leaves what it does not draw to the PDF", () => {
    expect(readChart(chartDoc(`<c:scatterChart>${ser("x", ["a"], [1])}</c:scatterChart>`), scheme)).toBeNull();
    expect(readChart(chartDoc(`<c:barChart>${ser("x", ["a"], [1])}</c:barChart><c:lineChart>${ser("y", ["a"], [2])}</c:lineChart>`), scheme)).toBeNull();
    expect(chartSVG({ kind: "col", cats: ["<b>"], series: [{ name: "<i>", values: [1], color: "#000000" }] }, 200, 100)).not.toContain("<b>");
  });

  it("fills omitted OOXML cache indices with null and renders horizontal bars with empty categories", () => {
    const sparse = `<c:barChart><c:barDir val="col"/><c:ser><c:idx val="0"/><c:tx>${strs(["S1"])}</c:tx><c:cat><c:strRef><c:strCache><c:ptCount val="2"/><c:pt idx="0"><c:v>Q1</c:v></c:pt><c:pt idx="2"><c:v>Q3</c:v></c:pt></c:strCache></c:strRef></c:cat><c:val><c:numRef><c:numCache><c:ptCount val="1"/><c:pt idx="-1"><c:v>99</c:v></c:pt><c:pt idx="2"><c:v>244</c:v></c:pt></c:numCache></c:numRef></c:val></c:ser></c:barChart>`;
    const c = readChart(chartDoc(sparse), scheme)!;
    expect(c.cats).toEqual(["Q1", "", "Q3"]);
    expect(c.series[0].values).toEqual([null, null, 244]);
    expect(1 in c.series[0].values).toBe(true);
    const emptyBar = chartSVG({ kind: "bar", cats: [], series: [{ name: "S1", values: [], color: "#1f6feb" }] }, 300, 200);
    expect(emptyBar).not.toContain("NaN");
    expect(emptyBar).not.toContain("Infinity");
  });
});
