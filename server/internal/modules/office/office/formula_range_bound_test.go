package office

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormulaRangesRespectWorkbookBudget(t *testing.T) {
	sh := &xsheet{cells: map[[2]int]*xcell{{0, 0}: {v: num(2)}, {0, 2}: {v: num(3)}}}
	book := &xbook{sheets: map[string]*xsheet{"sheet": sh}}
	eval := func(formula string) (value, error) {
		t.Helper()
		e, err := parseFormula(formula)
		if err != nil {
			t.Fatal(err)
		}
		return e.eval(book, sh)
	}
	v, err := eval("SUM(A1:A3)")
	if err != nil || v.kind != 'n' || v.num != 5 {
		t.Fatalf("ordinary SUM = %+v, %v", v, err)
	}
	before := book.rangeCells
	if _, err := eval("SUM(A1:A1000000)"); err != errUnsupported {
		t.Fatalf("large range: %v", err)
	}
	if book.rangeCells != before {
		t.Fatalf("rejected range spent budget: %d -> %d", before, book.rangeCells)
	}
	// Individually acceptable ranges must share one workbook budget.
	for book.rangeCells+maxCachedRangeCells <= maxCachedWorkbookRangeCells {
		if _, err := eval("SUM(A1:A65536)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eval("SUM(A1:A65536)"); err != errUnsupported {
		t.Fatalf("total budget: %v", err)
	}
}

func TestFormulaRangeBudgetPreservesLazyBranchesAndDependencies(t *testing.T) {
	sh := &xsheet{cells: map[[2]int]*xcell{
		{0, 0}: {v: num(2)},
		{1, 0}: {formula: "SUM(A1:A2)"},
	}}
	book := &xbook{sheets: map[string]*xsheet{"sheet": sh}, rangeCells: maxCachedWorkbookRangeCells - 1}
	for _, tc := range []struct {
		formula string
		want    float64
	}{
		{"IF(TRUE,7,SUM(A1:A1000000))", 7},
		{"SUM(A1)", 2},
	} {
		e, err := parseFormula(tc.formula)
		if err != nil {
			t.Fatal(err)
		}
		v, err := e.eval(book, sh)
		if err != nil || v.kind != 'n' || v.num != tc.want {
			t.Fatalf("%s = %+v, %v", tc.formula, v, err)
		}
	}
	if book.rangeCells != maxCachedWorkbookRangeCells {
		t.Fatalf("used %d cells", book.rangeCells)
	}
	// An unsupported dependency is not an Excel error that IFERROR can replace with a misleading cached value.
	e, err := parseFormula("IFERROR(B1,9)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.eval(book, sh); err != errUnsupported {
		t.Fatalf("uncached dependency: %v", err)
	}
}

func TestFormulaRangeBoundsDoNotDropFormulas(t *testing.T) {
	for _, formula := range []string{"SUM(A1:A1000000)", "SUM(A1:A1048577)", "SUM(A1:A9223372036854775807)", "SUM(A1:XFE1)"} {
		t.Run(formula, func(t *testing.T) {
			src, err := json.Marshal(workbookSpec{Sheets: []sheetSpec{{Rows: [][]any{{"=" + formula}}}}})
			if err != nil {
				t.Fatal(err)
			}
			data, err := Xlsx(src, "")
			if err != nil {
				t.Fatal(err)
			}
			sheet := parts(t, data)["xl/worksheets/sheet1.xml"]
			if !strings.Contains(sheet, "<f>"+formula+"</f>") || strings.Contains(sheet, "<v>") {
				t.Fatalf("formula/cache: %s", sheet)
			}
			if !strings.Contains(parts(t, data)["xl/workbook.xml"], `fullCalcOnLoad="1"`) {
				t.Fatal("missing recalculation")
			}
		})
	}
}
