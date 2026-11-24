package office

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFormulaRejectsNonFiniteText(t *testing.T) {
	inputs := []string{"NaN", "nan", "+Inf", "-Infinity", " Inf "}
	var rows [][]any
	for _, input := range inputs {
		rows = append(rows, []any{`=SUM("` + input + `")`, `=ABS("` + input + `")`, `=IFERROR(SUM("` + input + `"),0)`})
	}
	src, err := json.Marshal(workbookSpec{Sheets: []sheetSpec{{Name: "Numbers", Rows: rows}}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := Xlsx(src, "")
	if err != nil {
		t.Fatal(err)
	}
	sheet := parts(t, data)["xl/worksheets/sheet1.xml"]
	for i, input := range inputs {
		for _, function := range []struct{ column, name string }{{"A", "SUM"}, {"B", "ABS"}} {
			want := fmt.Sprintf(`<c r="%s%d" t="e"><f>%s(&quot;%s&quot;)</f><v>#VALUE!</v></c>`, function.column, i+1, function.name, input)
			if !strings.Contains(sheet, want) {
				t.Errorf("%s(%q): missing %s", function.name, input, want)
			}
		}
		want := fmt.Sprintf(`<c r="C%d"><f>IFERROR(SUM(&quot;%s&quot;),0)</f><v>0</v></c>`, i+1, input)
		if !strings.Contains(sheet, want) {
			t.Errorf("IFERROR for %q: missing %s", input, want)
		}
	}
}

func TestFormulaTextCoercionControls(t *testing.T) {
	src := `{"sheets":[{"rows":[["NaN","+Inf",true,2,null],["=SUM(A1:E1)","=SUM(\"2.5\")","=SUM(\" -2.5e1 \")","=SUM(TRUE,1)","=IF(FALSE,SUM(\"NaN\"),2)","=IFERROR(SUM(\"bad\"),7)"]]}]}`
	data, err := Xlsx([]byte(src), "")
	if err != nil {
		t.Fatal(err)
	}
	sheet := parts(t, data)["xl/worksheets/sheet1.xml"]
	for _, want := range []string{
		`<c r="A2"><f>SUM(A1:E1)</f><v>2</v></c>`,
		`<c r="B2"><f>SUM(&quot;2.5&quot;)</f><v>2.5</v></c>`,
		`<c r="C2"><f>SUM(&quot; -2.5e1 &quot;)</f><v>-25</v></c>`,
		`<c r="D2"><f>SUM(TRUE,1)</f><v>2</v></c>`,
		`<c r="E2"><f>IF(FALSE,SUM(&quot;NaN&quot;),2)</f><v>2</v></c>`,
		`<c r="F2"><f>IFERROR(SUM(&quot;bad&quot;),7)</f><v>7</v></c>`,
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("missing %s", want)
		}
	}
}
