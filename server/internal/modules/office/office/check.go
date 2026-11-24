package office

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Check says what is in a .docx, .xlsx or .pptx (Persona's or anyone's), for whoever made it to look over: a
// document's headings and how many paragraphs, list items, tables and pictures; a workbook's sheets with their range,
// cells, formulas and first row; a deck's slides with their titles, pictures and tables.
func Check(file string) (map[string]any, error) {
	z, err := zip.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("%s is not an Office file: %w", filepath.Base(file), err)
	}
	defer z.Close()
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
	}
	switch {
	case files["word/document.xml"] != nil:
		return checkDocx(files)
	case files["xl/workbook.xml"] != nil:
		return checkXlsx(files)
	case files["ppt/presentation.xml"] != nil:
		return checkPptx(files)
	}
	return nil, fmt.Errorf("%s is not a Word, Excel or PowerPoint file", filepath.Base(file))
}

func open(files map[string]*zip.File, name string) (*xml.Decoder, func(), error) {
	f := files[name]
	if f == nil {
		return nil, nil, fmt.Errorf("%s is missing", name)
	}
	r, err := f.Open()
	if err != nil {
		return nil, nil, err
	}
	return xml.NewDecoder(r), func() { r.Close() }, nil
}

func checkDocx(files map[string]*zip.File) (map[string]any, error) {
	d, done, err := open(files, "word/document.xml")
	if err != nil {
		return nil, err
	}
	defer done()
	var headings []string
	paras, items, tables, pictures, chars := 0, 0, 0, 0, 0
	var text strings.Builder
	style, inPara, numbered, depth := "", false, false, 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("word/document.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tbl":
				if depth == 0 {
					tables++
				}
				depth++
			case "p":
				inPara, style, numbered = true, "", false
				text.Reset()
			case "pStyle":
				style = attr(t, "val")
			case "numPr":
				numbered = true
			case "drawing", "pict":
				pictures++
			}
		case xml.CharData:
			if inPara {
				text.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "tbl":
				depth--
			case "p":
				inPara = false
				s := strings.TrimSpace(text.String())
				chars += len([]rune(s))
				if s == "" {
					continue
				}
				switch l := strings.ToLower(style); {
				case strings.HasPrefix(l, "heading") || l == "title":
					headings = append(headings, s)
				case numbered:
					items++
				case depth == 0:
					paras++
				}
			}
		}
	}
	return map[string]any{"type": "docx", "headings": capped(headings, 40), "paragraphs": paras, "list_items": items,
		"tables": tables, "pictures": pictures, "characters": chars}, nil
}

var refRE = lazyre.New(`^([A-Z]+)(\d+)$`)

func checkXlsx(files map[string]*zip.File) (map[string]any, error) {
	type sheet struct {
		Name string `xml:"name,attr"`
		ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	}
	var wb struct {
		Sheets []sheet `xml:"sheets>sheet"`
	}
	if err := decode(files, "xl/workbook.xml", &wb); err != nil {
		return nil, err
	}
	targets, err := relTargets(files, "xl/_rels/workbook.xml.rels", "xl")
	if err != nil {
		return nil, err
	}
	shared, err := sharedStrings(files)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, s := range wb.Sheets {
		info, err := checkSheet(files, targets[s.ID], shared)
		if err != nil {
			return nil, fmt.Errorf("sheet %q: %w", s.Name, err)
		}
		info["name"] = s.Name
		out = append(out, info)
	}
	return map[string]any{"type": "xlsx", "sheets": out}, nil
}

func checkSheet(files map[string]*zip.File, name string, shared []string) (map[string]any, error) {
	d, done, err := open(files, name)
	if err != nil {
		return nil, err
	}
	defer done()
	cells, formulas, maxCol, maxRow := 0, 0, 0, 0
	var first []string
	var ref, typ string
	var val, inline strings.Builder
	in, rPh := "", 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				ref, typ, rPh = attr(t, "r"), attr(t, "t"), 0
				val.Reset()
				inline.Reset()
			case "f":
				formulas++
			case "rPh":
				rPh++
			case "v":
				in = "v"
			case "t":
				if rPh == 0 {
					in = "t"
				}
			}
		case xml.CharData:
			switch in {
			case "v":
				val.Write(t)
			case "t":
				inline.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "rPh":
				if rPh > 0 {
					rPh--
				}
			case "v", "t":
				in = ""
			case "c":
				cells++
				m := refRE.FindStringSubmatch(ref)
				if m == nil {
					continue
				}
				col, row := colNumber(m[1]), atoi(m[2])
				maxCol, maxRow = max(maxCol, col), max(maxRow, row)
				if row == 1 {
					s := val.String()
					switch typ {
					case "s":
						if i := atoi(s); i >= 0 && i < len(shared) {
							s = shared[i]
						}
					case "inlineStr":
						s = inline.String()
					}
					for len(first) < col-1 {
						first = append(first, "")
					}
					first = append(first, s)
				}
			}
		}
	}
	info := map[string]any{"cells": cells, "formulas": formulas, "first_row": capped(first, 30)}
	if maxRow > 0 {
		info["range"] = "A1:" + cellRef(maxCol-1, maxRow-1)
	}
	return info, nil
}

func colNumber(s string) int {
	n := 0
	for _, r := range s {
		n = n*26 + int(r-'A'+1)
	}
	return n
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func sharedStrings(files map[string]*zip.File) ([]string, error) {
	if files["xl/sharedStrings.xml"] == nil {
		return nil, nil
	}
	d, done, err := open(files, "xl/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	defer done()
	var out []string
	var cur strings.Builder
	inT, rPh := false, 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
				rPh = 0
			case "rPh":
				rPh++
			}
			inT = t.Name.Local == "t" && rPh == 0
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		case xml.EndElement:
			inT = false
			switch t.Name.Local {
			case "rPh":
				if rPh > 0 {
					rPh--
				}
			case "si":
				out = append(out, cur.String())
			}
		}
	}
}

func checkPptx(files map[string]*zip.File) (map[string]any, error) {
	type sldID struct {
		ID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	}
	var pres struct {
		Slides []sldID `xml:"sldIdLst>sldId"`
	}
	if err := decode(files, "ppt/presentation.xml", &pres); err != nil {
		return nil, err
	}
	targets, err := relTargets(files, "ppt/_rels/presentation.xml.rels", "ppt")
	if err != nil {
		return nil, err
	}
	var slides []map[string]any
	for i, s := range pres.Slides {
		info, err := checkSlide(files, targets[s.ID])
		if err != nil {
			return nil, fmt.Errorf("slide %d: %w", i+1, err)
		}
		slides = append(slides, info)
	}
	return map[string]any{"type": "pptx", "slides": slides}, nil
}

func checkSlide(files map[string]*zip.File, name string) (map[string]any, error) {
	d, done, err := open(files, name)
	if err != nil {
		return nil, err
	}
	defer done()
	title, pictures, tables, paras := "", 0, 0, 0
	var text strings.Builder
	inShape, isTitle, inT := false, false, false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sp":
				inShape, isTitle = true, false
				text.Reset()
			case "ph":
				typ := attr(t, "type")
				isTitle = typ == "title" || typ == "ctrTitle"
			case "pic":
				pictures++
			case "tbl":
				tables++
			case "t":
				inT = true
			case "p":
				if inShape && !isTitle {
					paras++
				}
			}
		case xml.CharData:
			if inT && inShape {
				text.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "sp":
				if isTitle && title == "" {
					title = strings.TrimSpace(text.String())
				}
				inShape = false
			}
		}
	}
	return map[string]any{"title": title, "paragraphs": paras, "pictures": pictures, "tables": tables}, nil
}

func decode(files map[string]*zip.File, name string, v any) error {
	d, done, err := open(files, name)
	if err != nil {
		return err
	}
	defer done()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// relTargets are a relationships file's targets by id, as part names (relative ones from dir).
func relTargets(files map[string]*zip.File, name, dir string) (map[string]string, error) {
	var rs struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := decode(files, name, &rs); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rs.Rels {
		t := r.Target
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Join(dir, t)
		}
		out[r.ID] = t
	}
	return out, nil
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func capped(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("…（还有 %d 个）", len(s)-n))
	}
	return s
}
