package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func codexAgentMessageLine(id, text string) string {
	content, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return `{"timestamp":"2026-10-28T09:00:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"` + id +
		`","phase":"answer","content":[{"type":"text","text":` + string(content) + `}]}}}`
}

// A visualize marker in a Codex final reply becomes a visualization item after the prose, and the marker itself is
// stripped from the text item.
func TestCodexAgentMessageVisualization(t *testing.T) {
	marker := string(vizStart) + vizTag + string(vizSep) +
		`{"path":"/data00/h/.codex/visualizations/2026/10/28/s/a.html","mode":"wide","title":"Trend"}` + string(vizEnd)
	items := ParseLine(3, []byte(codexAgentMessageLine("a1", "Here is the chart.\n\n"+marker)), &Meta{Codex: true})
	if len(items) != 2 {
		t.Fatalf("items = %d: %+v", len(items), items)
	}
	txt, viz := items[0], items[1]
	if txt.Kind != proto.KindText || txt.Text != "Here is the chart." || txt.ID != "a1#0" {
		t.Fatalf("text item = %+v", txt)
	}
	want := proto.Visualization{Path: "/data00/h/.codex/visualizations/2026/10/28/s/a.html", Mode: VisualizationModeWide, Title: "Trend"}
	if viz.Kind != proto.KindVisualization || viz.Visualization == nil || *viz.Visualization != want || viz.ID != "a1#1" {
		t.Fatalf("visualization item = %+v", viz)
	}
}

// A message holding only a marker yields a visualization item and no empty text item.
func TestCodexAgentMessageVisualizationOnly(t *testing.T) {
	marker := vizMarker(`{"path":"/tmp/a.html"}`)
	items := ParseLine(1, []byte(codexAgentMessageLine("a2", marker)), &Meta{Codex: true})
	if len(items) != 1 || items[0].Kind != proto.KindVisualization || items[0].Visualization == nil {
		t.Fatalf("items = %+v", items)
	}
}

// A malformed marker is kept in the text verbatim and produces no visualization item.
func TestCodexAgentMessageVisualizationMalformed(t *testing.T) {
	marker := vizMarker(`{"path":"relative/a.html"}`)
	in := "prose " + marker
	items := ParseLine(1, []byte(codexAgentMessageLine("a3", in)), &Meta{Codex: true})
	if len(items) != 1 || items[0].Kind != proto.KindText {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Text != in || !strings.Contains(items[0].Text, marker) {
		t.Fatalf("text = %q", items[0].Text)
	}
}
