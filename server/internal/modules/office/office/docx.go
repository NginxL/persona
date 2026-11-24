package office

import (
	"fmt"
	"strings"

	east "github.com/yuin/goldmark/extension/ast"
)

// A Word document from Markdown: A4, Normal / Heading 1–3 / Code / Quote styles, bullets and numbers (each numbered
// list from its own first number), tables with a shaded header row, pictures as wide as the page at most, --- a page
// break.

const (
	docxNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
		`xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" ` +
		`xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"`
	pageWidthEMU = 5731510 // A4 less 2.54 cm margins: 15.92 cm
	textWidthTw  = 9026    // the same in twentieths of a point
	emuPerPx     = 9525    // at 96 dpi
)

// Docx is the Word document Markdown src makes, its pictures relative to dir.
func Docx(src []byte, dir, title string) ([]byte, error) {
	blocks, err := parse(src, dir)
	if err != nil {
		return nil, err
	}
	d := &docxWriter{}
	d.rels.add("styles", "styles.xml", false)
	d.rels.add("numbering", "numbering.xml", false)
	var body strings.Builder
	for _, b := range blocks {
		body.WriteString(d.block(b))
	}
	doc := xmlHead + `<w:document ` + docxNS + `><w:body>` + body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/>` +
		`<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr>` +
		`</w:body></w:document>`
	parts := []part{
		{"[Content_Types].xml", contentTypes(map[string]string{
			"word/document.xml":  "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml",
			"word/styles.xml":    "application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml",
			"word/numbering.xml": "application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml",
		})},
		{"_rels/.rels", rootRels(officeDocRel, "word/document.xml")},
		{"docProps/core.xml", coreProps(title)},
		{"docProps/app.xml", appProps("")},
		{"word/document.xml", doc},
		{"word/_rels/document.xml.rels", d.rels.xml()},
		{"word/styles.xml", docxStyles},
		{"word/numbering.xml", d.numbering()},
	}
	for i, m := range d.media {
		parts = append(parts, part{fmt.Sprintf("word/media/image%d.%s", i+1, m.ext), string(m.data)})
	}
	return pack(parts)
}

type docxWriter struct {
	rels      rels
	media     []*picture
	ordered   []int // the first number of each numbered list, by its num id - 2
	numOf     map[int]int
	pageBreak bool // the next paragraph starts a page (---)
}

func (d *docxWriter) block(b block) string {
	switch b.kind {
	case bHeading:
		return d.para(fmt.Sprintf("Heading%d", min(b.level, 3)), "", b.runs)
	case bItem:
		return d.para("ListParagraph", fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, min(b.level, 8), d.num(b)), b.runs)
	case bPara:
		if b.cont {
			return d.para("ListParagraph", fmt.Sprintf(`<w:ind w:left="%d"/>`, 720*(min(b.level, 8)+1)), b.runs)
		}
		return d.para("", "", b.runs)
	case bQuote:
		return d.para("Quote", "", b.runs)
	case bCode:
		var out strings.Builder
		for _, l := range b.lines {
			out.WriteString(d.para("Code", "", []run{{text: l}}))
		}
		return out.String()
	case bBreak:
		// The next paragraph starts the page (a break run shows as a box in some viewers).
		d.pageBreak = true
		return ""
	case bImage:
		return d.paraXML("", `<w:jc w:val="center"/>`, d.drawing(b.img))
	case bTable:
		return d.table(b.table)
	}
	return ""
}

// num is the numbering an item takes: bullets share one, each numbered list has its own (from its first number).
func (d *docxWriter) num(b block) int {
	if !b.ordered {
		return 1
	}
	if d.numOf == nil {
		d.numOf = map[int]int{}
	}
	if n, ok := d.numOf[b.list]; ok {
		return n
	}
	d.ordered = append(d.ordered, max(b.start, 1))
	d.numOf[b.list] = len(d.ordered) + 1
	return d.numOf[b.list]
}

func (d *docxWriter) para(style, extra string, runs []run) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(d.run(r))
	}
	return d.paraXML(style, extra, b.String())
}

// paraXML is a paragraph of style with extra properties (after the style and a page break, in the schema's order)
// and runs already written.
func (d *docxWriter) paraXML(style, extra, runs string) string {
	if d.pageBreak {
		extra = `<w:pageBreakBefore/>` + extra
		d.pageBreak = false
	}
	pPr := ""
	if style != "" || extra != "" {
		pPr = `<w:pPr>`
		if style != "" {
			pPr += `<w:pStyle w:val="` + style + `"/>`
		}
		pPr += extra + `</w:pPr>`
	}
	return `<w:p>` + pPr + runs + `</w:p>`
}

func (d *docxWriter) run(r run) string {
	if r.br {
		return `<w:r><w:br/></w:r>`
	}
	var pr strings.Builder
	if r.link != "" {
		pr.WriteString(`<w:rStyle w:val="Hyperlink"/>`)
	}
	if r.code {
		pr.WriteString(`<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/>`)
	}
	if r.bold {
		pr.WriteString(`<w:b/>`)
	}
	if r.italic {
		pr.WriteString(`<w:i/>`)
	}
	if r.strike {
		pr.WriteString(`<w:strike/>`)
	}
	if r.code {
		pr.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="F2F2F2"/>`)
	}
	out := `<w:r>`
	if pr.Len() > 0 {
		out += `<w:rPr>` + pr.String() + `</w:rPr>`
	}
	out += `<w:t xml:space="preserve">` + esc(r.text) + `</w:t></w:r>`
	if r.link != "" {
		if strings.HasPrefix(r.link, "#") {
			return `<w:hyperlink w:anchor="` + esc(strings.TrimPrefix(r.link, "#")) + `">` + out + `</w:hyperlink>`
		}
		id := d.rels.add("hyperlink", r.link, true)
		return `<w:hyperlink r:id="` + id + `" w:history="1">` + out + `</w:hyperlink>`
	}
	return out
}

// drawing is a picture in its run, at most as wide as the page.
func (d *docxWriter) drawing(p *picture) string {
	d.media = append(d.media, p)
	n := len(d.media)
	id := d.rels.add("image", fmt.Sprintf("media/image%d.%s", n, p.ext), false)
	cx, cy := fit(p.w, p.h, pageWidthEMU, pageWidthEMU*4/3)
	return fmt.Sprintf(`<w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/>`+
		`<wp:docPr id="%d" name="Picture %d" descr="%s"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr>`+
		`<a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic>`+
		`<pic:nvPicPr><pic:cNvPr id="%d" name="image%d.%s"/><pic:cNvPicPr/></pic:nvPicPr>`+
		`<pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>`+
		`<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>`+
		`</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`,
		cx, cy, n, n, esc(p.alt), n, n, p.ext, id, cx, cy)
}

// fit is a w×h pixel picture's size in EMU at 96 dpi, made smaller (never larger) to fit maxW×maxH.
func fit(w, h int, maxW, maxH int64) (int64, int64) {
	cx, cy := int64(max(w, 1))*emuPerPx, int64(max(h, 1))*emuPerPx
	if cx > maxW {
		cy, cx = cy*maxW/cx, maxW
	}
	if cy > maxH {
		cx, cy = cx*maxH/cy, maxH
	}
	return cx, cy
}

func (d *docxWriter) table(t *table) string {
	cols := 0
	for _, r := range t.rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return ""
	}
	w := textWidthTw / cols
	var b strings.Builder
	if d.pageBreak { // a table cannot start a page itself
		b.WriteString(d.paraXML("", "", ""))
	}
	// Borders and width on the table itself, not only in its style: some viewers (QuickLook) read only these.
	fmt.Fprintf(&b, `<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="%d" w:type="dxa"/>%s<w:tblLayout w:type="fixed"/>`+
		`<w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="0" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`, w*cols, tableBorders)
	for range cols {
		fmt.Fprintf(&b, `<w:gridCol w:w="%d"/>`, w)
	}
	b.WriteString(`</w:tblGrid>`)
	for i, row := range t.rows {
		b.WriteString(`<w:tr>`)
		if i == 0 {
			b.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
		}
		for c := range cols {
			var runs []run
			if c < len(row) {
				runs = row[c]
			}
			fmt.Fprintf(&b, `<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>`, w)
			if i == 0 {
				b.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="E7ECF3"/>`)
				runs = bolded(runs)
			}
			b.WriteString(`</w:tcPr>`)
			jc := ""
			if c < len(t.align) {
				jc = map[east.Alignment]string{east.AlignLeft: "left", east.AlignCenter: "center", east.AlignRight: "right"}[t.align[c]]
			}
			extra := `<w:spacing w:before="40" w:after="40"/>`
			if jc != "" {
				extra += `<w:jc w:val="` + jc + `"/>`
			}
			b.WriteString(d.para("", extra, runs))
			b.WriteString(`</w:tc>`)
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl><w:p/>`) // Word wants a paragraph after a table
	return b.String()
}

func bolded(runs []run) []run {
	out := make([]run, len(runs))
	for i, r := range runs {
		r.bold = true
		out[i] = r
	}
	return out
}

// tableBorders are a table's lines.
const tableBorders = `<w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/><w:left w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/>` +
	`<w:bottom w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/><w:right w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/>` +
	`<w:insideH w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/></w:tblBorders>`

// numbering is numbering.xml: bullets (num 1) and a numbered list each (num 2…), from its own first number.
func (d *docxWriter) numbering() string {
	var b strings.Builder
	b.WriteString(xmlHead + `<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	b.WriteString(`<w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="hybridMultilevel"/>`)
	for i := range 9 {
		fmt.Fprintf(&b, `<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="%s"/><w:lvlJc w:val="left"/>`+
			`<w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, i, []string{"•", "◦", "▪"}[i%3], 720*(i+1))
	}
	b.WriteString(`</w:abstractNum>`)
	// A numbered list each, starting at its own first number (a startOverride is lost on some viewers).
	for n, start := range d.ordered {
		fmt.Fprintf(&b, `<w:abstractNum w:abstractNumId="%d"><w:multiLevelType w:val="hybridMultilevel"/>`, n+1)
		for i := range 9 {
			fmtName := []string{"decimal", "lowerLetter", "lowerRoman"}[i%3]
			first := 1
			if i == 0 {
				first = start
			}
			fmt.Fprintf(&b, `<w:lvl w:ilvl="%d"><w:start w:val="%d"/><w:numFmt w:val="%s"/><w:lvlText w:val="%%%d."/><w:lvlJc w:val="left"/>`+
				`<w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, i, first, fmtName, i+1, 720*(i+1))
		}
		b.WriteString(`</w:abstractNum>`)
	}
	b.WriteString(`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`)
	for n := range d.ordered {
		fmt.Fprintf(&b, `<w:num w:numId="%d"><w:abstractNumId w:val="%d"/></w:num>`, n+2, n+1)
	}
	b.WriteString(`</w:numbering>`)
	return b.String()
}

// docxStyles are the styles the document uses; East Asian text in Microsoft YaHei (PingFang where it is missing).
const docxStyles = xmlHead + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
	`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:eastAsia="Microsoft YaHei" w:hAnsi="Calibri" w:cs="Calibri"/>` +
	`<w:sz w:val="22"/><w:szCs w:val="22"/><w:lang w:val="en-US" w:eastAsia="zh-CN"/></w:rPr></w:rPrDefault>` +
	`<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="300" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>` +
	`<w:pPr><w:keepNext/><w:spacing w:before="360" w:after="160"/><w:outlineLvl w:val="0"/></w:pPr>` +
	`<w:rPr><w:b/><w:color w:val="1F3864"/><w:sz w:val="36"/><w:szCs w:val="36"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>` +
	`<w:pPr><w:keepNext/><w:spacing w:before="280" w:after="120"/><w:outlineLvl w:val="1"/></w:pPr>` +
	`<w:rPr><w:b/><w:color w:val="2F5496"/><w:sz w:val="30"/><w:szCs w:val="30"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>` +
	`<w:pPr><w:keepNext/><w:spacing w:before="240" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr>` +
	`<w:rPr><w:b/><w:color w:val="2F5496"/><w:sz w:val="26"/><w:szCs w:val="26"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:qFormat/>` +
	`<w:pPr><w:spacing w:after="60"/><w:contextualSpacing/></w:pPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:qFormat/>` +
	`<w:pPr><w:pBdr><w:left w:val="single" w:sz="18" w:space="8" w:color="BFBFBF"/></w:pBdr><w:ind w:left="567"/></w:pPr>` +
	`<w:rPr><w:i/><w:color w:val="595959"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/>` +
	`<w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F2F2F2"/><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr>` +
	`<w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/><w:sz w:val="19"/><w:szCs w:val="19"/></w:rPr></w:style>` +
	`<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/><w:rPr><w:color w:val="0563C1"/><w:u w:val="single"/></w:rPr></w:style>` +
	`<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:tblPr><w:tblBorders>` +
	`<w:top w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/><w:left w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/>` +
	`<w:bottom w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/><w:right w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/>` +
	`<w:insideH w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/>` +
	`</w:tblBorders><w:tblCellMar><w:left w:w="108" w:type="dxa"/><w:right w:w="108" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>` +
	`</w:styles>`
