package wire

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each case in schema/extension/examples/persona-card is a card and the view it becomes (null: the apps keep the code
// block). The views hold to the schema. UPDATE=1 writes what View gives instead (then read the diff).
func TestPersonaCardCases(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "..", "schema", "extension", "examples", "persona-card", "*.json"))
	if len(files) < 5 {
		t.Fatalf("only %d cases", len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var c struct {
				Card json.RawMessage `json:"card"`
				View json.RawMessage `json:"view"`
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			if err := ValidateJSON("PersonaCard", c.Card); err != nil {
				t.Fatalf("card: %v", err)
			}
			var card PersonaCard
			if err := json.Unmarshal(c.Card, &card); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(card.View())
			if os.Getenv("UPDATE") != "" {
				var b bytes.Buffer
				enc := json.NewEncoder(&b)
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				_ = enc.Encode(map[string]json.RawMessage{"card": c.Card, "view": got})
				if err := os.WriteFile(f, b.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			var want, have any
			_ = json.Unmarshal(c.View, &want)
			_ = json.Unmarshal(got, &have)
			if string(mustJSON(want)) != string(mustJSON(have)) {
				t.Errorf("view\nwant %s\n got %s", mustJSON(want), mustJSON(have))
			}
			if string(c.View) != "null" {
				if err := ValidateJSON("View", c.View); err != nil {
					t.Errorf("view: %v", err)
				}
			}
		})
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestNumber(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 2250: "2,250", 7.25: "7.3", 7.04: "7", 1234567.89: "1,234,567.9", -1200.3: "-1,200.3", 999.96: "1,000", -0.01: "0"} {
		if got := Number(v); got != want {
			t.Errorf("Number(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestNumberExtremeAndNonFinite(t *testing.T) {
	for _, tc := range []struct {
		v      float64
		prefix string
	}{
		{1e308, "100,"},
		{-1e308, "-100,"},
		{math.MaxFloat64, "179,"},
	} {
		got := Number(tc.v)
		if strings.Contains(got, "Inf") || !strings.HasPrefix(got, tc.prefix) {
			t.Fatalf("Number(%v) = %q, want finite grouped digits starting with %q", tc.v, got, tc.prefix)
		}
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := Number(v); got != "0" {
			t.Fatalf("Number(%v) = %q, want \"0\"", v, got)
		}
	}
}

func TestPersonaCardViewRejectsOverflowingAndNonFiniteValues(t *testing.T) {
	nan, posInf := math.NaN(), math.Inf(1)
	invalid := []struct {
		name string
		card PersonaCard
	}{
		{
			name: "stacked_bar sum overflows float64",
			card: PersonaCard{
				Type: PersonaCardKindStackedBar,
				Segments: []CardPart{
					{Label: "a", Value: 1e308},
					{Label: "b", Value: 1e308},
					{Label: "c", Value: 1e308},
					{Label: "d", Value: 1e308},
					{Label: "e", Value: 1e308},
				},
			},
		},
		{
			name: "ring sum overflows float64",
			card: PersonaCard{
				Type: PersonaCardKindRing,
				Segments: []CardPart{
					{Label: "a", Value: 1e308},
					{Label: "b", Value: 1e308},
				},
			},
		},
		{
			name: "stat with NaN value",
			card: PersonaCard{Type: PersonaCardKindStat, Value: &nan},
		},
		{
			name: "stat with +Inf value",
			card: PersonaCard{Type: PersonaCardKindStat, Value: &posInf},
		},
		{
			name: "line with non-finite point",
			card: PersonaCard{
				Type:   PersonaCardKindLine,
				Points: []CardPart{{Label: "a", Value: 1}, {Label: "b", Value: math.Inf(-1)}},
			},
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.card.View(); got != nil {
				raw, err := json.Marshal(got)
				t.Fatalf("View() = %+v (marshal=%s, err=%v), want nil", got, raw, err)
			}
		})
	}

	extremeLine := PersonaCard{
		Type:   PersonaCardKindLine,
		Points: []CardPart{{Label: "a", Value: -1e308}, {Label: "b", Value: 1e308}, {Label: "c", Value: 0}},
	}
	view := extremeLine.View()
	if view == nil {
		t.Fatal("finite extreme line card returned nil View")
	}
	raw, err := json.Marshal(view)
	if err != nil || strings.Contains(string(raw), "Inf") {
		t.Fatalf("extreme line View JSON = %s (err=%v)", raw, err)
	}
}
