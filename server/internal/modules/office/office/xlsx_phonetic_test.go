package office

import (
	"fmt"
	"testing"
)

func TestCheckXlsxIgnoresPhoneticRuns(t *testing.T) {
	types := map[string]string{
		"xl/workbook.xml":          "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml",
		"xl/sharedStrings.xml":     "application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml",
		"xl/worksheets/sheet1.xml": "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml",
	}
	var wbRels rels
	sheetRel := wbRels.add("worksheet", "worksheets/sheet1.xml", false)
	wbRels.add("sharedStrings", "sharedStrings.xml", false)

	workbook := xmlHead + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<sheets><sheet name="Sheet1" sheetId="1" r:id="` + sheetRel + `"/></sheets></workbook>`

	shared := xmlHead + `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2">` +
		`<si><t>漢字</t><rPh sb="0" eb="2"><t>カンジ</t></rPh><phoneticPr fontId="1"/></si>` +
		`<si><r><t>東</t></r><r><t>京</t></r><rPh sb="0" eb="2"><t>トウキョウ</t></rPh><phoneticPr fontId="1"/></si>` +
		`</sst>`

	sheet := xmlHead + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		`<sheetData><row r="1">` +
		`<c r="A1" t="s"><v>0</v></c>` +
		`<c r="B1" t="s"><v>1</v></c>` +
		`<c r="C1" t="inlineStr"><is><t>大阪</t><rPh sb="0" eb="2"><t>オオサカ</t></rPh><phoneticPr fontId="1"/></is></c>` +
		`</row></sheetData></worksheet>`

	data, err := pack([]part{
		{"[Content_Types].xml", contentTypes(types)},
		{"_rels/.rels", rootRels(officeDocRel, "xl/workbook.xml")},
		{"docProps/core.xml", coreProps("Phonetic")},
		{"docProps/app.xml", appProps("")},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", wbRels.xml()},
		{"xl/sharedStrings.xml", shared},
		{"xl/worksheets/sheet1.xml", sheet},
	})
	if err != nil {
		t.Fatal(err)
	}

	info := check(t, data, ".xlsx")
	sheets := info["sheets"].([]any)
	first := sheets[0].(map[string]any)
	if got := fmt.Sprint(first["first_row"]); got != "[漢字 東京 大阪]" {
		t.Fatalf("first_row = %s, want [漢字 東京 大阪]", got)
	}
}
