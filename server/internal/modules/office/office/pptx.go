package office

import (
	"fmt"
	"strings"

	east "github.com/yuin/goldmark/extension/ast"
)

// A 16:9 deck from Markdown. A slide starts at --- (when there is one) or else at each # / ## heading; its first
// heading is its title. A first slide that is a # heading and paragraphs only is the title slide (the paragraphs its
// subtitle). Lists are bullets (numbered ones numbered), other paragraphs plain lines. A slide keeps its first picture
// and its first table: the table goes under the words, and a picture next to words or a table goes on the right while
// they share the left column. Text gets smaller as there is more of it on a slide: a file's viewers do not shrink it
// to fit the way PowerPoint does once you edit.

const (
	slideW, slideH = 12192000, 6858000
	bodyX, bodyY   = 838200, 1825625
	bodyW, bodyH   = 10515600, 4351338
	pptNS          = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
		`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"`
	groupProps = `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>` +
		`<p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`
)

type slide struct {
	title      []run
	titleSlide bool
	body       []block
	img        *picture
	table      *table
}

// Pptx is the deck Markdown src makes, its pictures relative to dir.
func Pptx(src []byte, dir, title string) ([]byte, error) {
	blocks, err := parse(src, dir)
	if err != nil {
		return nil, err
	}
	slides := split(blocks)
	if len(slides) == 0 {
		return nil, errEmpty
	}
	types := map[string]string{
		"ppt/presentation.xml":              "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml",
		"ppt/slideMasters/slideMaster1.xml": "application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml",
		"ppt/slideLayouts/slideLayout1.xml": "application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml",
		"ppt/slideLayouts/slideLayout2.xml": "application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml",
		"ppt/theme/theme1.xml":              "application/vnd.openxmlformats-officedocument.theme+xml",
		"ppt/presProps.xml":                 "application/vnd.openxmlformats-officedocument.presentationml.presProps+xml",
		"ppt/viewProps.xml":                 "application/vnd.openxmlformats-officedocument.presentationml.viewProps+xml",
		"ppt/tableStyles.xml":               "application/vnd.openxmlformats-officedocument.presentationml.tableStyles+xml",
	}
	var pres rels
	pres.add("slideMaster", "slideMasters/slideMaster1.xml", false)
	pres.add("theme", "theme/theme1.xml", false)
	pres.add("presProps", "presProps.xml", false)
	pres.add("viewProps", "viewProps.xml", false)
	pres.add("tableStyles", "tableStyles.xml", false)
	var ids strings.Builder
	var parts []part
	media := 0
	for i, s := range slides {
		name := fmt.Sprintf("slide%d.xml", i+1)
		types["ppt/slides/"+name] = "application/vnd.openxmlformats-officedocument.presentationml.slide+xml"
		fmt.Fprintf(&ids, `<p:sldId id="%d" r:id="%s"/>`, 256+i, pres.add("slide", "slides/"+name, false))
		w := &slideWriter{}
		layout := "slideLayout2.xml"
		if s.titleSlide {
			layout = "slideLayout1.xml"
		}
		w.rels.add("slideLayout", "../slideLayouts/"+layout, false)
		var pic string
		if s.img != nil {
			media++
			file := fmt.Sprintf("image%d.%s", media, s.img.ext)
			parts = append(parts, part{"ppt/media/" + file, string(s.img.data)})
			pic = w.rels.add("image", "../media/"+file, false)
		}
		parts = append(parts, part{"ppt/slides/" + name, w.slide(s, pic)}, part{"ppt/slides/_rels/" + name + ".rels", w.rels.xml()})
	}
	presentation := xmlHead + `<p:presentation ` + pptNS + ` saveSubsetFonts="1">` +
		`<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>` +
		`<p:sldIdLst>` + ids.String() + `</p:sldIdLst>` +
		fmt.Sprintf(`<p:sldSz cx="%d" cy="%d"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`, slideW, slideH)
	return pack(append([]part{
		{"[Content_Types].xml", contentTypes(types)},
		{"_rels/.rels", rootRels(officeDocRel, "ppt/presentation.xml")},
		{"docProps/core.xml", coreProps(title)},
		{"docProps/app.xml", appProps(fmt.Sprintf("<Slides>%d</Slides>", len(slides)))},
		{"ppt/presentation.xml", presentation},
		{"ppt/_rels/presentation.xml.rels", pres.xml()},
		{"ppt/presProps.xml", xmlHead + `<p:presentationPr ` + pptNS + `/>`},
		{"ppt/viewProps.xml", xmlHead + `<p:viewPr ` + pptNS + `/>`},
		{"ppt/tableStyles.xml", xmlHead + `<a:tblStyleLst xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" def="{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}"/>`},
		{"ppt/slideMasters/slideMaster1.xml", slideMaster},
		{"ppt/slideMasters/_rels/slideMaster1.xml.rels", masterRels},
		{"ppt/slideLayouts/slideLayout1.xml", layoutTitle},
		{"ppt/slideLayouts/_rels/slideLayout1.xml.rels", layoutRels},
		{"ppt/slideLayouts/slideLayout2.xml", layoutContent},
		{"ppt/slideLayouts/_rels/slideLayout2.xml.rels", layoutRels},
		{"ppt/theme/theme1.xml", theme},
	}, parts...))
}

// split cuts the blocks into slides.
func split(blocks []block) []slide {
	breaks := false
	for _, b := range blocks {
		breaks = breaks || b.kind == bBreak
	}
	var chunks [][]block
	var cur []block
	for _, b := range blocks {
		starts := b.kind == bBreak || !breaks && b.kind == bHeading && b.level <= 2
		if starts && len(cur) > 0 {
			chunks = append(chunks, cur)
			cur = nil
		}
		if b.kind != bBreak {
			cur = append(cur, b)
		}
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	var out []slide
	for i, c := range chunks {
		var s slide
		plainOnly, top := true, false
		for _, b := range c {
			switch {
			case b.kind == bHeading && s.title == nil:
				s.title, top = b.runs, b.level == 1
			case b.kind == bHeading:
				s.body = append(s.body, block{kind: bPara, runs: bolded(b.runs)})
			case b.kind == bImage && s.img == nil:
				s.img, plainOnly = b.img, false
			case b.kind == bTable && s.table == nil:
				s.table, plainOnly = b.table, false
			case b.kind == bImage || b.kind == bTable:
				// one of each a slide
			default:
				s.body = append(s.body, b)
				plainOnly = plainOnly && b.kind == bPara
			}
		}
		s.titleSlide = i == 0 && top && plainOnly && len(chunks) > 1
		if s.title != nil || len(s.body) > 0 || s.img != nil || s.table != nil {
			out = append(out, s)
		}
	}
	return out
}

type slideWriter struct {
	rels rels
}

func (w *slideWriter) slide(s slide, pic string) string {
	var tree strings.Builder
	if s.titleSlide {
		tree.WriteString(placeholder(2, "Title 1", `<p:ph type="ctrTitle"/>`, "", `<a:p>`+w.runs(s.title, 0, "")+`</a:p>`))
		if len(s.body) > 0 {
			var paras strings.Builder
			for _, b := range s.body {
				paras.WriteString(`<a:p>` + w.runs(b.runs, 0, "") + `</a:p>`)
			}
			tree.WriteString(placeholder(3, "Subtitle 2", `<p:ph type="subTitle" idx="1"/>`, "", paras.String()))
		}
	} else {
		if s.title != nil {
			tree.WriteString(placeholder(2, "Title 1", `<p:ph type="title"/>`, "", `<a:p>`+w.runs(s.title, 0, "")+`</a:p>`))
		}
		x, y, cx, cy := int64(bodyX), int64(bodyY), int64(bodyW), int64(bodyH)
		text := len(s.body) > 0
		size := textSize(s.body, s.img != nil || s.table != nil)
		// The words and the table share a left column, the words on top; a picture next to either takes the right.
		colW := cx
		if s.img != nil {
			colW = cx * 55 / 100
		}
		if text {
			th := cy
			if s.table != nil {
				th = cy * 35 / 100
			}
			xfrm := ""
			if colW != cx || th != cy {
				xfrm = fmt.Sprintf(`<a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm>`, x, y, colW, th)
			}
			tree.WriteString(placeholder(3, "Content 2", `<p:ph idx="1"/>`, xfrm, w.body(s.body, size)))
		}
		if s.img != nil {
			ax, aw := x, cx
			if text || s.table != nil {
				ax, aw = x+cx*58/100, cx*42/100
			}
			pw, ph := fit(s.img.w, s.img.h, aw, cy)
			fmt.Fprintf(&tree, `<p:pic><p:nvPicPr><p:cNvPr id="4" name="Picture 3" descr="%s"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr>`+
				`<p:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></p:blipFill>`+
				`<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr></p:pic>`,
				esc(s.img.alt), pic, ax+(aw-pw)/2, y+(cy-ph)/2, pw, ph)
		}
		if s.table != nil {
			ty, th := y, cy
			if text {
				ty, th = y+cy*38/100, cy*62/100
			}
			tree.WriteString(w.table(s.table, x, ty, colW, th))
		}
	}
	return xmlHead + `<p:sld ` + pptNS + `><p:cSld><p:spTree>` + groupProps + tree.String() +
		`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`
}

func placeholder(id int, name, ph, xfrm, paras string) string {
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr>%s</p:nvPr></p:nvSpPr>`+
		`<p:spPr>%s</p:spPr><p:txBody><a:bodyPr><a:normAutofit/></a:bodyPr><a:lstStyle/>%s</p:txBody></p:sp>`, id, name, ph, xfrm, paras)
}

// textSize is the body's size in hundredths of a point: smaller the more there is (and when it shares the slide).
func textSize(body []block, shared bool) int {
	lines, chars := 0, 0
	for _, b := range body {
		if b.kind == bCode {
			lines += len(b.lines)
			for _, l := range b.lines {
				chars += len([]rune(l))
			}
			continue
		}
		lines++
		chars += len([]rune(plain(b.runs)))
	}
	if shared {
		chars *= 2
	}
	switch {
	case lines <= 5 && chars <= 300:
		return 2400
	case lines <= 8 && chars <= 500:
		return 2000
	case lines <= 12 && chars <= 800:
		return 1800
	}
	return 1400
}

func (w *slideWriter) body(blocks []block, size int) string {
	var b strings.Builder
	for _, bl := range blocks {
		lvl := min(bl.level, 4)
		sz := max(size-200*lvl, 1200)
		switch bl.kind {
		case bItem:
			pPr := fmt.Sprintf(`<a:pPr lvl="%d"/>`, lvl)
			if bl.ordered {
				pPr = fmt.Sprintf(`<a:pPr lvl="%d" marL="%d" indent="-342900"><a:buFont typeface="+mj-lt"/><a:buAutoNum type="arabicPeriod" startAt="%d"/></a:pPr>`,
					lvl, 342900+457200*lvl, max(bl.start, 1))
			}
			b.WriteString(`<a:p>` + pPr + w.runs(bl.runs, sz, "") + `</a:p>`)
		case bCode:
			for _, l := range bl.lines {
				b.WriteString(`<a:p><a:pPr marL="0" indent="0"><a:spcBef><a:spcPts val="0"/></a:spcBef><a:buNone/></a:pPr>` +
					w.runs([]run{{text: l, code: true}}, max(size-600, 1200), "") + `</a:p>`)
			}
		default: // a paragraph, a quote: a line of its own
			runs := bl.runs
			if bl.kind == bQuote {
				runs = italics(runs)
			}
			marL := 0
			if bl.cont {
				marL = 342900 + 457200*lvl
			}
			b.WriteString(fmt.Sprintf(`<a:p><a:pPr marL="%d" indent="0"><a:buNone/></a:pPr>`, marL) + w.runs(runs, sz, "") + `</a:p>`)
		}
	}
	return b.String()
}

func italics(runs []run) []run {
	out := make([]run, len(runs))
	for i, r := range runs {
		r.italic = true
		out[i] = r
	}
	return out
}

// runs are a paragraph's runs; size 0 keeps the placeholder's; fill a color for them all ("" none).
func (w *slideWriter) runs(runs []run, size int, fill string) string {
	var b strings.Builder
	for _, r := range runs {
		if r.br {
			b.WriteString(`<a:br><a:rPr lang="zh-CN" altLang="en-US"/></a:br>`)
			continue
		}
		attrs := ` lang="zh-CN" altLang="en-US" dirty="0"`
		if size > 0 {
			attrs += fmt.Sprintf(` sz="%d"`, size)
		}
		if r.bold {
			attrs += ` b="1"`
		}
		if r.italic {
			attrs += ` i="1"`
		}
		if r.strike {
			attrs += ` strike="sngStrike"`
		}
		inner := fill
		if r.code {
			inner += `<a:latin typeface="Consolas"/><a:cs typeface="Consolas"/>`
		}
		if r.link != "" && !strings.HasPrefix(r.link, "#") {
			inner += `<a:hlinkClick r:id="` + w.rels.add("hyperlink", r.link, true) + `"/>`
		}
		b.WriteString(`<a:r><a:rPr` + attrs + `>` + inner + `</a:rPr><a:t>` + esc(r.text) + `</a:t></a:r>`)
	}
	return b.String()
}

func (w *slideWriter) table(t *table, x, y, cx, cy int64) string {
	cols := 0
	for _, r := range t.rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return ""
	}
	rowH := min(int64(370840), cy/int64(len(t.rows)))
	size := 1400
	if len(t.rows) > 8 {
		size = 1100
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="5" name="Table 4"/><p:cNvGraphicFramePr><a:graphicFrameLocks noGrp="1"/></p:cNvGraphicFramePr><p:nvPr/></p:nvGraphicFramePr>`+
		`<p:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table">`+
		`<a:tbl><a:tblPr firstRow="1" bandRow="1"/><a:tblGrid>`, x, y, cx, rowH*int64(len(t.rows)))
	for range cols {
		fmt.Fprintf(&b, `<a:gridCol w="%d"/>`, cx/int64(cols))
	}
	b.WriteString(`</a:tblGrid>`)
	line := `<a:solidFill><a:srgbClr val="BFBFBF"/></a:solidFill>`
	for i, row := range t.rows {
		fmt.Fprintf(&b, `<a:tr h="%d">`, rowH)
		for c := range cols {
			var runs []run
			if c < len(row) {
				runs = row[c]
			}
			algn := ""
			if c < len(t.align) {
				algn = map[east.Alignment]string{east.AlignCenter: ` algn="ctr"`, east.AlignRight: ` algn="r"`}[t.align[c]]
			}
			fill, color := `<a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill>`, ""
			if i == 0 {
				fill, color, runs = `<a:solidFill><a:schemeClr val="accent1"/></a:solidFill>`, `<a:solidFill><a:schemeClr val="bg1"/></a:solidFill>`, bolded(runs)
			} else if i%2 == 0 {
				fill = `<a:solidFill><a:srgbClr val="F2F2F2"/></a:solidFill>`
			}
			b.WriteString(`<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p>`)
			if algn != "" {
				b.WriteString(`<a:pPr` + algn + `/>`)
			}
			b.WriteString(w.runs(runs, size, color) + fmt.Sprintf(`<a:endParaRPr lang="zh-CN" sz="%d" dirty="0"/></a:p></a:txBody><a:tcPr>`, size) +
				`<a:lnL w="9525">` + line + `</a:lnL><a:lnR w="9525">` + line + `</a:lnR><a:lnT w="9525">` + line + `</a:lnT><a:lnB w="9525">` + line + `</a:lnB>` +
				fill + `</a:tcPr></a:tc>`)
		}
		b.WriteString(`</a:tr>`)
	}
	b.WriteString(`</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`)
	return b.String()
}

const masterRels = xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout2.xml"/>` +
	`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>` +
	`</Relationships>`

const layoutRels = xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>` +
	`</Relationships>`

func level(n, marL, sz int, char string) string {
	return fmt.Sprintf(`<a:lvl%dpPr marL="%d" indent="-228600" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1">`+
		`<a:lnSpc><a:spcPct val="90000"/></a:lnSpc><a:spcBef><a:spcPts val="1000"/></a:spcBef><a:buFont typeface="Arial"/><a:buChar char="%s"/>`+
		`<a:defRPr sz="%d" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl%dpPr>`,
		n, marL, char, sz, n)
}

var slideMaster = xmlHead + `<p:sldMaster ` + pptNS + `><p:cSld><p:bg><p:bgRef idx="1001"><a:schemeClr val="bg1"/></p:bgRef></p:bg><p:spTree>` + groupProps +
	`<p:sp><p:nvSpPr><p:cNvPr id="2" name="Title Placeholder 1"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>` +
	`<p:spPr><a:xfrm><a:off x="838200" y="365125"/><a:ext cx="10515600" cy="1325563"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>` +
	`<p:txBody><a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0" anchor="ctr"><a:normAutofit/></a:bodyPr><a:lstStyle/>` +
	`<a:p><a:r><a:rPr lang="zh-CN" altLang="en-US"/><a:t>标题</a:t></a:r></a:p></p:txBody></p:sp>` +
	`<p:sp><p:nvSpPr><p:cNvPr id="3" name="Text Placeholder 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr>` +
	`<p:spPr><a:xfrm><a:off x="838200" y="1825625"/><a:ext cx="10515600" cy="4351338"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>` +
	`<p:txBody><a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0"><a:normAutofit/></a:bodyPr><a:lstStyle/>` +
	`<a:p><a:pPr lvl="0"/><a:r><a:rPr lang="zh-CN" altLang="en-US"/><a:t>正文</a:t></a:r></a:p></p:txBody></p:sp>` +
	`</p:spTree></p:cSld>` +
	`<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>` +
	`<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/><p:sldLayoutId id="2147483650" r:id="rId2"/></p:sldLayoutIdLst>` +
	`<p:txStyles><p:titleStyle><a:lvl1pPr algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1">` +
	`<a:lnSpc><a:spcPct val="90000"/></a:lnSpc><a:spcBef><a:spcPct val="0"/></a:spcBef><a:buNone/>` +
	`<a:defRPr sz="3600" b="1" kern="1200"><a:solidFill><a:schemeClr val="tx2"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr>` +
	`</a:lvl1pPr></p:titleStyle><p:bodyStyle>` +
	level(1, 228600, 2400, "•") + level(2, 685800, 2000, "–") + level(3, 1143000, 1800, "•") + level(4, 1600200, 1600, "–") + level(5, 2057400, 1600, "•") +
	`</p:bodyStyle><p:otherStyle><a:defPPr><a:defRPr lang="zh-CN"/></a:defPPr></p:otherStyle></p:txStyles></p:sldMaster>`

var layoutTitle = xmlHead + `<p:sldLayout ` + pptNS + ` type="title" preserve="1"><p:cSld name="Title Slide"><p:spTree>` + groupProps +
	`<p:sp><p:nvSpPr><p:cNvPr id="2" name="Title 1"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="ctrTitle"/></p:nvPr></p:nvSpPr>` +
	`<p:spPr><a:xfrm><a:off x="1524000" y="1122363"/><a:ext cx="9144000" cy="2387600"/></a:xfrm></p:spPr>` +
	`<p:txBody><a:bodyPr anchor="b"><a:normAutofit/></a:bodyPr><a:lstStyle><a:lvl1pPr algn="ctr"><a:defRPr sz="4800"/></a:lvl1pPr></a:lstStyle>` +
	`<a:p><a:r><a:rPr lang="zh-CN" altLang="en-US"/><a:t>标题</a:t></a:r></a:p></p:txBody></p:sp>` +
	`<p:sp><p:nvSpPr><p:cNvPr id="3" name="Subtitle 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="subTitle" idx="1"/></p:nvPr></p:nvSpPr>` +
	`<p:spPr><a:xfrm><a:off x="1524000" y="3602038"/><a:ext cx="9144000" cy="1655762"/></a:xfrm></p:spPr>` +
	`<p:txBody><a:bodyPr><a:normAutofit/></a:bodyPr><a:lstStyle><a:lvl1pPr marL="0" indent="0" algn="ctr"><a:buNone/>` +
	`<a:defRPr sz="2400"><a:solidFill><a:schemeClr val="tx1"><a:lumMod val="65000"/><a:lumOff val="35000"/></a:schemeClr></a:solidFill></a:defRPr></a:lvl1pPr></a:lstStyle>` +
	`<a:p><a:r><a:rPr lang="zh-CN" altLang="en-US"/><a:t>副标题</a:t></a:r></a:p></p:txBody></p:sp>` +
	`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`

var layoutContent = xmlHead + `<p:sldLayout ` + pptNS + ` type="obj" preserve="1"><p:cSld name="Title and Content"><p:spTree>` + groupProps +
	`<p:sp><p:nvSpPr><p:cNvPr id="2" name="Title 1"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr/>` +
	`<p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="zh-CN" altLang="en-US"/><a:t>标题</a:t></a:r></a:p></p:txBody></p:sp>` +
	`<p:sp><p:nvSpPr><p:cNvPr id="3" name="Content Placeholder 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph idx="1"/></p:nvPr></p:nvSpPr><p:spPr/>` +
	`<p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:pPr lvl="0"/><a:r><a:rPr lang="zh-CN" altLang="en-US"/><a:t>正文</a:t></a:r></a:p></p:txBody></p:sp>` +
	`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`

const theme = xmlHead + `<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Persona"><a:themeElements>` +
	`<a:clrScheme name="Persona"><a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1><a:lt1><a:sysClr val="window" lastClr="FFFFFF"/></a:lt1>` +
	`<a:dk2><a:srgbClr val="1F3864"/></a:dk2><a:lt2><a:srgbClr val="E7E6E6"/></a:lt2><a:accent1><a:srgbClr val="2F5496"/></a:accent1>` +
	`<a:accent2><a:srgbClr val="ED7D31"/></a:accent2><a:accent3><a:srgbClr val="A5A5A5"/></a:accent3><a:accent4><a:srgbClr val="FFC000"/></a:accent4>` +
	`<a:accent5><a:srgbClr val="5B9BD5"/></a:accent5><a:accent6><a:srgbClr val="70AD47"/></a:accent6><a:hlink><a:srgbClr val="0563C1"/></a:hlink>` +
	`<a:folHlink><a:srgbClr val="954F72"/></a:folHlink></a:clrScheme>` +
	`<a:fontScheme name="Persona"><a:majorFont><a:latin typeface="Calibri Light"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface=""/></a:majorFont>` +
	`<a:minorFont><a:latin typeface="Calibri"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface=""/></a:minorFont></a:fontScheme>` +
	`<a:fmtScheme name="Persona"><a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill>` +
	`<a:solidFill><a:schemeClr val="phClr"><a:tint val="50000"/></a:schemeClr></a:solidFill><a:solidFill><a:schemeClr val="phClr"><a:shade val="80000"/></a:schemeClr></a:solidFill></a:fillStyleLst>` +
	`<a:lnStyleLst><a:ln w="6350"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln w="12700"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln>` +
	`<a:ln w="19050"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst>` +
	`<a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst>` +
	`<a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"><a:tint val="95000"/></a:schemeClr></a:solidFill>` +
	`<a:solidFill><a:schemeClr val="phClr"><a:shade val="90000"/></a:schemeClr></a:solidFill></a:bgFillStyleLst></a:fmtScheme>` +
	`</a:themeElements><a:objectDefaults/><a:extraClrSchemeLst/></a:theme>`
