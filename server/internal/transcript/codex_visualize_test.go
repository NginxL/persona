package transcript

import (
	"strings"
	"testing"
)

func vizMarker(raw string) string {
	return string(vizStart) + vizTag + string(vizSep) + raw + string(vizEnd)
}

func TestSplitVisualizations(t *testing.T) {
	t.Run("no marker passes through", func(t *testing.T) {
		in := "just prose, no markers"
		clean, refs := splitVisualizations(in)
		if clean != in || refs != nil {
			t.Fatalf("got %q %v", clean, refs)
		}
	})

	t.Run("single wide marker with title", func(t *testing.T) {
		marker := vizMarker(`{"path":"/home/u/.codex/visualizations/2026/10/28/sess/a.html","mode":"wide","title":"Chart"}`)
		in := "Before prose.\n\n" + marker + "\n\nAfter prose."
		clean, refs := splitVisualizations(in)
		if clean != "Before prose.\n\nAfter prose." {
			t.Fatalf("clean = %q", clean)
		}
		if len(refs) != 1 {
			t.Fatalf("refs = %v", refs)
		}
		r := refs[0]
		want := VisualizationRef{
			Path:  "/home/u/.codex/visualizations/2026/10/28/sess/a.html",
			Mode:  VisualizationModeWide,
			Title: "Chart",
		}
		if r != want {
			t.Fatalf("ref = %+v, want %+v", r, want)
		}
	})

	t.Run("default mode is empty", func(t *testing.T) {
		marker := vizMarker(`{"path":"/tmp/a.html"}`)
		clean, refs := splitVisualizations(marker)
		if clean != "" || len(refs) != 1 || refs[0].Mode != "" || refs[0].Path != "/tmp/a.html" {
			t.Fatalf("got %q %+v", clean, refs)
		}
	})

	t.Run("multiple markers stay in order", func(t *testing.T) {
		m1 := vizMarker(`{"path":"/tmp/a.html"}`)
		m2 := vizMarker(`{"path":"/tmp/b.html"}`)
		in := m1 + "\ntext between\n" + m2
		clean, refs := splitVisualizations(in)
		if clean != "text between" {
			t.Fatalf("clean = %q", clean)
		}
		if len(refs) != 2 || refs[0].Path != "/tmp/a.html" || refs[1].Path != "/tmp/b.html" {
			t.Fatalf("refs = %+v", refs)
		}
	})

	t.Run("marker only message yields no prose", func(t *testing.T) {
		in := vizMarker(`{"path":"/tmp/a.html"}`)
		clean, refs := splitVisualizations(in)
		if clean != "" || len(refs) != 1 {
			t.Fatalf("got %q %v", clean, refs)
		}
	})
}

func TestSplitVisualizationsMalformedKeptVerbatim(t *testing.T) {
	cases := map[string]string{
		"bad json":       vizMarker(`{not json}`),
		"relative path":  vizMarker(`{"path":"relative/a.html"}`),
		"unknown mode":   vizMarker(`{"path":"/tmp/a.html","mode":"narrow"}`),
		"oversize title": vizMarker(`{"path":"/tmp/a.html","title":"` + strings.Repeat("x", 257) + `"}`),
		"unclosed":       string(vizStart) + vizTag + string(vizSep) + `{"path":"/tmp/a.html"}`,
		"stray rune":     string(vizStart) + "something else",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			clean, refs := splitVisualizations("prose\n" + in)
			if refs != nil {
				t.Fatalf("refs = %v", refs)
			}
			if want := "prose\n" + in; clean != want {
				t.Fatalf("clean = %q, want %q", clean, want)
			}
		})
	}
}
