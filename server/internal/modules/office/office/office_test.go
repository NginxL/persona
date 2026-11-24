package office

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writePNG makes a w×h picture in dir.
func writePNG(t *testing.T, dir, name string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: 120, B: uint8(y), A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// parts are the package's files, every XML one checked to be well-formed.
func parts(t *testing.T, data []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if z.File[0].Name != "[Content_Types].xml" {
		t.Errorf("first part %s", z.File[0].Name)
	}
	out := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		r.Close()
		out[f.Name] = string(b)
		if strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".rels") {
			d := xml.NewDecoder(bytes.NewReader(b))
			for {
				if _, err := d.Token(); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					t.Fatalf("%s is not well-formed: %v", f.Name, err)
				}
			}
		}
	}
	return out
}

func check(t *testing.T, data []byte, ext string) map[string]any {
	t.Helper()
	f := filepath.Join(t.TempDir(), "out"+ext)
	if err := os.WriteFile(f, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Check(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(info)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

const report = "# 示例季度报告\n\n" +
	"这是**加粗**、*斜体*、`代码` 和 [一个链接](https://example.com/a?b=1&c=2) 的段落，还有 <标记> & 符号\x01。\n\n" +
	"## 要点\n\n- 第一点\n  - 第一点下面的一点\n- 第二点\n\n" +
	"3. 第三步\n4. 第四步\n\n" +
	"1) 另一个列表\n\n" +
	"| 项目 | 数量 |\n| :--- | ---: |\n| 示例一 | 12 |\n| 示例二 | 30 |\n\n" +
	"```go\nfmt.Println(\"示例\")\n```\n\n> 一句引用\n\n![示例图](chart.png)\n\n---\n\n### 附录\n\n最后一段。\n"

func TestDocx(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "chart.png", 1600, 900)
	data, err := Docx([]byte(report), dir, "示例季度报告")
	if err != nil {
		t.Fatal(err)
	}
	p := parts(t, data)
	doc := p["word/document.xml"]
	for _, want := range []string{
		`<w:pStyle w:val="Heading1"/>`, `<w:b/>`, `<w:i/>`, `w:ascii="Consolas"`, `&lt;标记&gt; &amp; 符号。`, `<w:pageBreakBefore/>`, `<w:tblW w:w="9026" w:type="dxa"/>`,
		`<w:jc w:val="right"/>`, `<w:pStyle w:val="Quote"/>`, `<w:pStyle w:val="Code"/>`, `<w:numId w:val="1"/>`, `<w:ilvl w:val="1"/>`,
		`<w:numId w:val="2"/>`, `<w:numId w:val="3"/>`, `<wp:extent cx="5731510" cy="3223974"/>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document has no %s", want)
		}
	}
	if num := p["word/numbering.xml"]; !strings.Contains(num, `<w:abstractNum w:abstractNumId="1"><w:multiLevelType w:val="hybridMultilevel"/><w:lvl w:ilvl="0"><w:start w:val="3"/>`) ||
		!strings.Contains(num, `<w:abstractNum w:abstractNumId="2"><w:multiLevelType w:val="hybridMultilevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/>`) ||
		!strings.Contains(num, `<w:num w:numId="3"><w:abstractNumId w:val="2"/></w:num>`) {
		t.Errorf("numbering %s", p["word/numbering.xml"])
	}
	if rels := p["word/_rels/document.xml.rels"]; !strings.Contains(rels, `Target="https://example.com/a?b=1&amp;c=2" TargetMode="External"`) ||
		!strings.Contains(rels, `Target="media/image1.png"`) {
		t.Errorf("rels %s", rels)
	}
	if _, ok := p["word/media/image1.png"]; !ok {
		t.Error("no picture")
	}
	info := check(t, data, ".docx")
	if fmt.Sprint(info["headings"]) != "[示例季度报告 要点 附录]" || info["tables"] != 1.0 || info["pictures"] != 1.0 || info["list_items"] != 6.0 {
		t.Fatalf("check %v", info)
	}
}

func TestDocxRefusesWhatItCannotPut(t *testing.T) {
	dir := t.TempDir()
	if _, err := Docx([]byte("![没有的图](missing.png)\n"), dir, ""); err == nil || !strings.Contains(err.Error(), "missing.png") {
		t.Errorf("a missing picture: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "x.svg"), []byte("<svg/>"), 0o644)
	if _, err := Docx([]byte("![矢量图](x.svg)\n"), dir, ""); err == nil || !strings.Contains(err.Error(), "PNG, JPEG and GIF") {
		t.Errorf("an SVG: %v", err)
	}
	if _, err := Docx([]byte("  \n"), dir, ""); !errors.Is(err, errEmpty) {
		t.Errorf("nothing: %v", err)
	}
	if data, err := Docx([]byte("![网上的图](https://example.com/x.png)\n"), dir, ""); err != nil || !strings.Contains(parts(t, data)["word/document.xml"], "网上的图") {
		t.Errorf("a picture on the web stays its words: %v", err)
	}
}

const deck = "# 示例发布会\n\n2026 年 11 月\n\n---\n\n## 进展\n\n- 完成了第一阶段\n  - 包括示例模块\n- 开始第二阶段\n\n1. 第一步\n2. 第二步\n\n---\n\n## 架构图\n\n" +
	"一句说明\n\n![架构](arch.png)\n\n---\n\n## 数据\n\n| 指标 | 值 |\n| --- | ---: |\n| 示例 A | 1 |\n| 示例 B | 2 |\n\n---\n\n## 结束\n"

func TestPptx(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "arch.png", 800, 600)
	data, err := Pptx([]byte(deck), dir, "示例发布会")
	if err != nil {
		t.Fatal(err)
	}
	p := parts(t, data)
	for _, name := range []string{"ppt/slideMasters/slideMaster1.xml", "ppt/theme/theme1.xml", "ppt/slideLayouts/slideLayout2.xml", "ppt/slides/slide5.xml", "ppt/media/image1.png"} {
		if _, ok := p[name]; !ok {
			t.Errorf("no %s", name)
		}
	}
	if !strings.Contains(p["ppt/slides/_rels/slide1.xml.rels"], "slideLayout1.xml") || !strings.Contains(p["ppt/slides/slide1.xml"], `type="ctrTitle"`) ||
		!strings.Contains(p["ppt/slides/slide1.xml"], "2026 年 11 月") {
		t.Errorf("title slide %s", p["ppt/slides/slide1.xml"])
	}
	if s := p["ppt/slides/slide2.xml"]; !strings.Contains(s, `<a:pPr lvl="1"/>`) || !strings.Contains(s, `<a:buAutoNum type="arabicPeriod" startAt="1"/>`) {
		t.Errorf("bullets %s", s)
	}
	if !strings.Contains(p["[Content_Types].xml"], `PartName="/ppt/slides/slide5.xml"`) {
		t.Error("slide 5 has no content type")
	}
	info := check(t, data, ".pptx")
	slides := info["slides"].([]any)
	var titles []string
	for _, s := range slides {
		titles = append(titles, s.(map[string]any)["title"].(string))
	}
	if fmt.Sprint(titles) != "[示例发布会 进展 架构图 数据 结束]" || slides[2].(map[string]any)["pictures"] != 1.0 || slides[3].(map[string]any)["tables"] != 1.0 {
		t.Fatalf("check %v", info)
	}
	// Without ---, a # or ## heading starts a slide.
	data, err = Pptx([]byte("# 一\n\n- 甲\n\n## 二\n\n- 乙\n\n### 小标题\n\n- 丙\n"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if slides := check(t, data, ".pptx")["slides"].([]any); len(slides) != 2 || slides[1].(map[string]any)["paragraphs"] != 3.0 {
		t.Fatalf("by headings %v", slides)
	}
}

// Backslash escapes and entity references read as the characters they stand for (CommonMark 2.4, 2.5) in text,
// table cells, link targets and picture paths; a code span keeps its words as written, its line ending a space.
func TestMarkdownEscapesAndReferences(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "q3_chart.png", 40, 30)
	src := []byte("AT&amp;T &copy; 2026 &#x4E2D; 5 \\* 3 \\_name\\_ &lt;tag&gt;\n\n" +
		"| a | b |\n| --- | --- |\n| x \\| y | `a\\*b` |\n\n" +
		"[link \\[1\\]](https://example.com/a\\_b) and `one\ntwo`\n\n---\n\n![c](q3\\_chart.png)\n")
	want := []string{"AT&T © 2026 中 5 * 3 _name_ <tag>", "x | y", `a\*b`, "link [1]", "one two"}

	data, err := Docx(src, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	p := parts(t, data)
	got := elementTexts(t, p["word/document.xml"], xml.Name{Space: "http://schemas.openxmlformats.org/wordprocessingml/2006/main", Local: "t"})
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("document has no w:t %q: %q", w, got)
		}
	}
	if rels := p["word/_rels/document.xml.rels"]; !strings.Contains(rels, `Target="https://example.com/a_b" TargetMode="External"`) {
		t.Errorf("rels %s", rels)
	}
	if _, ok := p["word/media/image1.png"]; !ok {
		t.Error("no picture")
	}

	data, err = Pptx(src, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	p = parts(t, data)
	got = elementTexts(t, p["ppt/slides/slide1.xml"], xml.Name{Space: "http://schemas.openxmlformats.org/drawingml/2006/main", Local: "t"})
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("slide has no a:t %q: %q", w, got)
		}
	}
	if rels := p["ppt/slides/_rels/slide1.xml.rels"]; !strings.Contains(rels, `Target="https://example.com/a_b" TargetMode="External"`) {
		t.Errorf("slide rels %s", rels)
	}
	if _, ok := p["ppt/media/image1.png"]; !ok {
		t.Error("no picture in the deck")
	}
}

// elementTexts are the words of each element called name in an XML part, in order.
func elementTexts(t *testing.T, part string, name xml.Name) []string {
	t.Helper()
	var out []string
	var b strings.Builder
	inside := false
	d := xml.NewDecoder(strings.NewReader(part))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if tok.Name == name {
				inside = true
				b.Reset()
			}
		case xml.CharData:
			if inside {
				b.Write(tok)
			}
		case xml.EndElement:
			if inside && tok.Name == name {
				inside = false
				out = append(out, b.String())
			}
		}
	}
}

func TestXlsx(t *testing.T) {
	spec := `{"sheets":[{"name":"销售/汇总","header":["月份","收入","日期","达标"],"rows":[["1月",1200.5,"2026-01-31",true],["2月",1350,"2026-02-28",false],` +
		`["合计","=SUM(B2:B3)",null,null]],"formats":["","#,##0.00","yyyy-mm-dd",""]},{"name":"销售/汇总","rows":[["无表头",1]]}]}`
	data, err := Xlsx([]byte(spec), "示例")
	if err != nil {
		t.Fatal(err)
	}
	p := parts(t, data)
	s1 := p["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`<f>SUM(B2:B3)</f>`, `<c r="C2" s="3"><v>46053</v></c>`, `<c r="D2" t="b"><v>1</v></c>`, `state="frozen"`,
		`<c r="B2" s="2"><v>1200.5</v></c>`, `<dimension ref="A1:D4"/>`} {
		if !strings.Contains(s1, want) {
			t.Errorf("sheet 1 has no %s", want)
		}
	}
	if strings.Contains(p["xl/worksheets/sheet2.xml"], "frozen") {
		t.Error("a sheet without a header is frozen")
	}
	if !strings.Contains(p["xl/workbook.xml"], `name="销售_汇总"`) || !strings.Contains(p["xl/workbook.xml"], `name="销售_汇总 (2)"`) ||
		!strings.Contains(p["xl/workbook.xml"], `fullCalcOnLoad="1"`) {
		t.Errorf("workbook %s", p["xl/workbook.xml"])
	}
	if !strings.Contains(p["xl/styles.xml"], `formatCode="#,##0.00"`) || !strings.Contains(p["xl/styles.xml"], `formatCode="yyyy-mm-dd"`) {
		t.Errorf("styles %s", p["xl/styles.xml"])
	}
	info := check(t, data, ".xlsx")
	sheets := info["sheets"].([]any)
	first := sheets[0].(map[string]any)
	if first["range"] != "A1:D4" || first["formulas"] != 1.0 || fmt.Sprint(first["first_row"]) != "[月份 收入 日期 达标]" {
		t.Fatalf("check %v", info)
	}
	if _, err := Xlsx([]byte(`{"sheets":[{"rows":[[{"a":1}]]}]}`), ""); err == nil {
		t.Error("an object in a cell")
	}
	if _, err := Xlsx([]byte(`{}`), ""); err == nil {
		t.Error("no sheets")
	}
}

func TestXlsxFromCSV(t *testing.T) {
	data, err := XlsxFromCSV([]byte("\xef\xbb\xbf编号,名称,数量\n007,示例,12\n008,\"带,逗号\",3.5\n"), "示例", "")
	if err != nil {
		t.Fatal(err)
	}
	s := parts(t, data)["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`<c r="A2" t="inlineStr"><is><t xml:space="preserve">007</t></is></c>`, `<c r="C2"><v>12</v></c>`,
		`<t xml:space="preserve">带,逗号</t>`, `<c r="C3"><v>3.5</v></c>`, `<c r="A1" s="1" t="inlineStr"><is><t xml:space="preserve">编号</t>`} {
		if !strings.Contains(s, want) {
			t.Errorf("sheet has no %s", want)
		}
	}
}

// Excel's limits ("Excel specifications and limits"): a cell holds 32,767 characters counted in UTF-16, a formula
// 8,192 after its =, a sheet A1:XFD1048576. Past them Excel repairs the file and cuts or drops what does not fit, so
// the workbook is refused instead, naming the sheet and the cell. Text a formula joins past a cell's limit is
// #VALUE!, as in Excel.
func TestXlsxRefusesWhatExcelCannotHold(t *testing.T) {
	sheet := func(header, rows string) []byte {
		return []byte(`{"sheets":[{"name":"Data","header":` + header + `,"rows":` + rows + `}]}`)
	}
	text := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	full := strings.Repeat("a", 32767)
	longest := "=" + strings.Repeat("1+", 4095) + "11" // 8,192 characters after the =
	data, err := Xlsx(sheet(`[]`, `[[`+text(full)+`,`+text(longest)+`,`+text(strings.Repeat("\U0001F600", 16383)+"a")+`,`+text(strings.Repeat("b", 20000))+`,"=A1&D1"]]`), "")
	if err != nil {
		t.Fatal(err)
	}
	s := parts(t, data)["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`<c r="A1" t="inlineStr"><is><t xml:space="preserve">` + full + `</t>`, `<c r="B1"><f>` + longest[1:] + `</f><v>4106</v></c>`,
		`<c r="E1" t="e"><f>A1&amp;D1</f><v>#VALUE!</v></c>`} {
		if !strings.Contains(s, want) {
			t.Errorf("sheet has no %.80s…", want)
		}
	}
	for _, c := range []struct {
		name string
		src  []byte
		want []string
	}{
		{"a cell of 32768 characters", sheet(`[]`, `[[`+text(full+"a")+`]]`), []string{`sheet "Data": A1:`, "32768", "32767"}},
		{"a header of 32768 characters", sheet(`["x",`+text(full+"a")+`]`, `[]`), []string{"B1", "32767"}},
		{"16384 characters outside the BMP", sheet(`[]`, `[[1],[2,`+text(strings.Repeat("\U0001F600", 16384))+`]]`), []string{"B2", "32768", "32767"}},
		{"a formula of 8193 characters", sheet(`[]`, `[[`+text(longest+"1")+`]]`), []string{"A1", "8193", "8192"}},
		{"16385 cells in a row", sheet(`[]`, `[[`+strings.Repeat("1,", 16384)+`1]]`), []string{"XFE1", "16385", "16384"}},
		{"1048577 rows", sheet(`["x"]`, `[`+strings.Repeat("[],", 1048575)+`[]]`), []string{"1048577", "1048576"}},
	} {
		_, err := Xlsx(c.src, "")
		if err == nil {
			t.Errorf("%s: written", c.name)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v has no %s", c.name, err, want)
			}
		}
	}
	if _, err := XlsxFromCSV([]byte("Name\n"+full+"a\n"), "Data", ""); err == nil || !strings.Contains(err.Error(), `sheet "Data": A2:`) {
		t.Errorf("a CSV field of 32768 characters: %v", err)
	}
}

// The command writes next to its input (or -o), prints the file's absolute path with what check says, and says
// what went wrong on one line.
func TestMain(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "notes.md")
	os.WriteFile(in, []byte("# 示例\n\n一段。\n"), 0o644)
	var out, errOut bytes.Buffer
	if code := Main([]string{"docx", in, "--title", "示例"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var res struct {
		File  string         `json:"file"`
		Check map[string]any `json:"check"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.File != filepath.Join(dir, "notes.docx") || res.Check["type"] != "docx" {
		t.Fatalf("%v %s", err, out.String())
	}
	out.Reset()
	if code := Main([]string{"pptx", "-o", filepath.Join(dir, "deck.pptx"), in}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "deck.pptx") {
		t.Fatalf("exit %d: %s %s", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := Main([]string{"check", filepath.Join(dir, "deck.pptx")}, &out, &errOut); code != 0 || !strings.Contains(out.String(), `"type": "pptx"`) {
		t.Fatalf("check: %d %s", code, out.String())
	}
	// -o is a file of its kind only: run without asking, it must not write over anything else.
	if code := Main([]string{"docx", in, "-o", filepath.Join(dir, ".zshrc")}, io.Discard, &errOut); code != 2 || !strings.Contains(errOut.String(), "writes a .docx file") {
		t.Errorf("-o .zshrc: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".zshrc")); !os.IsNotExist(err) {
		t.Error("wrote .zshrc")
	}
	for _, args := range [][]string{nil, {"pdf", in}, {"docx"}, {"docx", in, in}} {
		if code := Main(args, io.Discard, io.Discard); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	errOut.Reset()
	if code := Main([]string{"check", in}, io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), "not an Office file") {
		t.Errorf("checking Markdown: %d %s", code, errOut.String())
	}
}

// The command never writes over its input: `office docx notes.docx`, or -o naming the input (itself, a hard link or a
// symbolic link), would read the document as Markdown and replace it with what that makes. Into another file, an
// Office file is no input either.
func TestMainRefusesToWriteOverItsInput(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(md, []byte("# Plan\n\nOne paragraph.\n\n## Next\n\nAnother one.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"docx", md}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	doc := filepath.Join(dir, "notes.docx")
	before, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "same.docx")
	if err := os.Link(doc, hard); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{{"docx", doc}, {"docx", doc, "-o", doc}, {"docx", "-o", hard, doc}}
	if link := filepath.Join(dir, "link.docx"); os.Symlink(doc, link) == nil {
		cases = append(cases, []string{"docx", doc, "-o", link})
	}
	for _, args := range cases {
		var errOut bytes.Buffer
		if code := Main(args, io.Discard, &errOut); code != 2 || !strings.Contains(errOut.String(), "would write over its input") {
			t.Errorf("%q: exit %d: %s", args, code, errOut.String())
		}
		if after, err := os.ReadFile(doc); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("%q changed the document (%d bytes, was %d): %v", args, len(after), len(before), err)
		}
	}
	if info, err := Check(doc); err != nil || fmt.Sprint(info["headings"]) != "[Plan Next]" {
		t.Errorf("check: %v %v", info, err)
	}
	other := filepath.Join(dir, "other.docx")
	var errOut bytes.Buffer
	if code := Main([]string{"docx", doc, "-o", other}, io.Discard, &errOut); code != 2 || !strings.Contains(errOut.String(), "is an Office file") {
		t.Errorf("an Office file as input: exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("wrote %s: %v", other, err)
	}
}

// Formulas carry their values (for viewers that do not calculate), worked out as Excel does; what is not worked out
// here is left for Excel.
func TestFormulas(t *testing.T) {
	spec := `{"sheets":[{"name":"数据","rows":[[10,20,"示例",true],[1.5,"=A1+B1*2","=A2*100%","=2^3^2"],` +
		`["=SUM(A1:B2)","=AVERAGE(A1:B1)","=MIN(A1:B2)","=MAX(A1:B2)"],["=COUNT(A1:D1)","=COUNTA(A1:D1)","=ROUND(2.345,2)","=ABS(-3)"],` +
		`["=IF(A1>5,\"大\",\"小\")","=IFERROR(1/0,\"无\")","=AND(D1,A1>1)","=NOT(D1)"],["=1/0","=C1&A1","=-2^2","=VLOOKUP(1,A1:B2,2)"],` +
		`["=E1","=E2","='说 明'!A1*2","=其他!A1+1"]]},` +
		`{"name":"说 明","rows":[[21]]},{"name":"其他","rows":[["=数据!A1"]]},{"name":"空","rows":[["=A2"]]}]}`
	data, err := Xlsx([]byte(spec), "")
	if err != nil {
		t.Fatal(err)
	}
	p := parts(t, data)
	s := p["xl/worksheets/sheet1.xml"]
	for _, want := range []string{
		`<c r="B2"><f>A1+B1*2</f><v>50</v></c>`, `<c r="C2"><f>A2*100%</f><v>1.5</v></c>`, `<c r="D2"><f>2^3^2</f><v>64</v></c>`,
		`<c r="A3"><f>SUM(A1:B2)</f><v>81.5</v></c>`, `<c r="B3"><f>AVERAGE(A1:B1)</f><v>15</v></c>`, `<c r="C3"><f>MIN(A1:B2)</f><v>1.5</v></c>`,
		`<c r="D3"><f>MAX(A1:B2)</f><v>50</v></c>`, `<c r="A4"><f>COUNT(A1:D1)</f><v>2</v></c>`, `<c r="B4"><f>COUNTA(A1:D1)</f><v>4</v></c>`,
		`<c r="C4"><f>ROUND(2.345,2)</f><v>2.35</v></c>`, `<c r="D4"><f>ABS(-3)</f><v>3</v></c>`,
		`<c r="A5" t="str"><f>IF(A1&gt;5,&quot;大&quot;,&quot;小&quot;)</f><v>大</v></c>`, `<c r="B5" t="str"><f>IFERROR(1/0,&quot;无&quot;)</f><v>无</v></c>`,
		`<c r="C5" t="b"><f>AND(D1,A1&gt;1)</f><v>1</v></c>`, `<c r="D5" t="b"><f>NOT(D1)</f><v>0</v></c>`,
		`<c r="A6" t="e"><f>1/0</f><v>#DIV/0!</v></c>`, `<c r="B6" t="str"><f>C1&amp;A1</f><v>示例10</v></c>`, `<c r="C6"><f>-2^2</f><v>4</v></c>`,
		`<c r="D6"><f>VLOOKUP(1,A1:B2,2)</f></c>`, // left for Excel
		`<c r="A7"><f>E1</f><v>0</v></c>`, `<c r="C7"><f>&#39;说 明&#39;!A1*2</f><v>42</v></c>`, `<c r="D7"><f>其他!A1+1</f><v>11</v></c>`,
	} {
		if !strings.Contains(s, strings.ReplaceAll(want, "&#39;", "'")) {
			t.Errorf("no %s", want)
		}
	}
	if !strings.Contains(p["xl/worksheets/sheet4.xml"], `<c r="A1"><f>A2</f><v>0</v></c>`) {
		t.Errorf("a blank: %s", p["xl/worksheets/sheet4.xml"])
	}
	// A circle is an error, not a hang.
	data, err = Xlsx([]byte(`{"sheets":[{"rows":[["=B1","=A1"]]}]}`), "")
	if err != nil || !strings.Contains(parts(t, data)["xl/worksheets/sheet1.xml"], `#REF!`) {
		t.Fatalf("a circle: %v", err)
	}
}

// As in Excel, typed-in arguments and references count differently: COUNT counts typed-in true / false and text that
// reads as a number, but only numbers in a reference; AND and OR skip a reference's blanks and text, and with nothing
// left to judge are #VALUE!.
func TestFormulaArgumentsTypedOrReferenced(t *testing.T) {
	spec := `{"sheets":[{"rows":[[true,null,"x",1],` +
		`["=COUNT(1,\"2\",TRUE,\"x\")","=COUNT(A1:D1)","=AND(A1:C1)","=OR(B1)","=OR(B1,FALSE)"]]}]}`
	data, err := Xlsx([]byte(spec), "")
	if err != nil {
		t.Fatal(err)
	}
	s := parts(t, data)["xl/worksheets/sheet1.xml"]
	for _, want := range []string{
		`<c r="A2"><f>COUNT(1,&quot;2&quot;,TRUE,&quot;x&quot;)</f><v>3</v></c>`, `<c r="B2"><f>COUNT(A1:D1)</f><v>1</v></c>`,
		`<c r="C2" t="b"><f>AND(A1:C1)</f><v>1</v></c>`, `<c r="D2" t="e"><f>OR(B1)</f><v>#VALUE!</v></c>`,
		`<c r="E2" t="b"><f>OR(B1,FALSE)</f><v>0</v></c>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %s", want)
		}
	}
}
