package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const vizThread = "01a20000-0000-7000-8000-000000000001"

func nodeVizMarker(raw string) string {
	return string(rune(0xe200)) + "visualize" + string(rune(0xe202)) + raw + string(rune(0xe201))
}

func vizAgentLine(t *testing.T, id, text string) string {
	t.Helper()
	content, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return `{"timestamp":"2026-11-01T00:00:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"` +
		id + `","phase":"answer","content":[{"type":"text","text":` + string(content) + `}]}}}`
}

// A visualization item's fragment is read only from the session's own visualizations directory.
func TestVisualizationHTMLGuard(t *testing.T) {
	n, _ := testNode(t)
	codexHome := n.codexHome()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	sessDir := filepath.Join(codexHome, "visualizations", "2026", "11", "01", vizThread)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(sessDir, "chart.html")
	if err := os.WriteFile(good, []byte("<div id=\"x\">hi</div>"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.html")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherSess := filepath.Join(codexHome, "visualizations", "2026", "11", "01", "01a20000-0000-7000-9000-000000000002", "x.html")
	if err := os.MkdirAll(filepath.Dir(otherSess), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherSess, []byte("other session"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(sessDir, "big.html")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", visualizationMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sessDir, "link.html")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	lines := []string{`{"type":"session_meta","payload":{"id":"` + vizThread + `"}}`}
	lines = append(lines,
		vizAgentLine(t, "a1", nodeVizMarker(`{"path":`+strQuote(good)+`}`)),
		vizAgentLine(t, "a2", "plain prose, no marker"),
		vizAgentLine(t, "a3", nodeVizMarker(`{"path":`+strQuote(outside)+`}`)),
		vizAgentLine(t, "a4", nodeVizMarker(`{"path":`+strQuote(otherSess)+`}`)),
		vizAgentLine(t, "a5", nodeVizMarker(`{"path":`+strQuote(big)+`}`)),
		vizAgentLine(t, "a6", nodeVizMarker(`{"path":`+strQuote(link)+`}`)),
	)
	rolloutAt(t, codexHome, now, vizThread, lines...)

	got, err := n.visualizationHTML(vizThread, "a1#0")
	if err != nil {
		t.Fatalf("good fragment: %v", err)
	}
	if got != "<div id=\"x\">hi</div>" {
		t.Fatalf("html = %q", got)
	}

	for _, tc := range []struct {
		name   string
		itemID string
	}{
		{"text item has no visualization", "a2#0"},
		{"path outside the visualizations tree", "a3#0"},
		{"another session's directory", "a4#0"},
		{"fragment over the size cap", "a5#0"},
		{"symlink escaping the tree", "a6#0"},
		{"unknown item id", "a9#0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := n.visualizationHTML(vizThread, tc.itemID); err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
		})
	}
}

// A home reached through a symlink ("/home/me" -> "/data/home/me") still serves a marker whose path is the
// physical one Codex recorded.
func TestVisualizationHTMLSymlinkedHome(t *testing.T) {
	n, physicalHome := testNode(t)
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(physicalHome, link); err != nil {
		t.Fatal(err)
	}
	n.home = link
	codexHome := n.codexHome()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	sessDir := filepath.Join(codexHome, "visualizations", "2026", "11", "01", vizThread)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "chart.html"), []byte("<div>hi</div>"), 0o600); err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(physicalHome, ".codex", "visualizations", "2026", "11", "01", vizThread, "chart.html")
	lines := []string{`{"type":"session_meta","payload":{"id":"` + vizThread + `"}}`,
		vizAgentLine(t, "a1", nodeVizMarker(`{"path":`+strQuote(physicalPath)+`}`))}
	rolloutAt(t, codexHome, now, vizThread, lines...)

	got, err := n.visualizationHTML(vizThread, "a1#0")
	if err != nil {
		t.Fatalf("fragment under a symlinked home: %v", err)
	}
	if got != "<div>hi</div>" {
		t.Fatalf("html = %q", got)
	}
}

func strQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
