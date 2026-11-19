package exttest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

var manifest = []byte(`{
  "manifest_version": 1, "id": "example", "name": "Example", "version": "1.0.0",
  "engines": {"extension_protocol": 1},
  "runtime": {"command": "bin/example"},
  "contributes": {"ui": [
    {"id": "home", "title": "示例", "placement": "nav_page"},
    {"id": "today", "title": "今天", "placement": "overview_card", "opens": "home"},
    {"id": "glance", "title": "一眼", "placement": "widget", "families": ["small", "accessory_inline"], "opens": "home"}
  ],
  "shortcuts": [{"id": "count", "title": "数一数", "params": [{"name": "what", "title": "数什么", "kind": "text"}]}]}
}`)

type state struct {
	Page int `json:"page"`
}

func program(t *testing.T) ext.Program {
	return ext.Program{ID: "example", Version: "1.0.0", Manifest: manifest,
		Init: func(ctx context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
			if in.Info().ID != "example" {
				t.Errorf("initialize for %q", in.Info().ID)
			}
			return nil, nil
		},
		UI: map[string]ext.Page{
			"home": {
				Render: func(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
					var s state
					if err := r.DecodeState(&s); err != nil {
						return nil, err
					}
					return &wire.View{Title: "示例", State: ext.EncodeState(s), Sections: []wire.Section{{Items: []wire.Component{
						{Type: wire.ComponentText, Text: "第 " + string(rune('1'+s.Page)) + " 页"},
						{Type: wire.ComponentButton, Title: "下一页", Action: &wire.Action{ID: "next"}},
					}}}}, nil
				},
				Action: func(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
					if r.Action != "next" {
						return nil, nil
					}
					var s state
					_ = r.DecodeState(&s)
					s.Page++
					r.State = ext.EncodeState(s)
					return &wire.ActionResult{Toast: "翻页了"}, nil
				},
			},
			"glance": {Render: func(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
				if !r.Widget() || r.Family() == "" {
					t.Errorf("a widget drawn as %q %q", r.Placement, r.Size)
				}
				return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentMetric, Label: r.Family(), Value: "3"}}}}}, nil
			}},
			"today": {Render: func(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
				return &wire.View{Sections: []wire.Section{{Items: []wire.Component{
					{Type: wire.ComponentStatGrid, Stats: []wire.Stat{{Label: "今天", Value: "3"}}},
				}}}}, nil
			}},
		},
		Shortcuts: map[string]ext.Shortcut{"count": func(ctx context.Context, r *ext.ShortcutRequest) (*wire.ShortcutResult, error) {
			if r.Values["what"] == "" {
				return nil, errors.New("说一下数什么")
			}
			return &wire.ShortcutResult{Text: "3 个"}, nil
		}},
	}
}

func TestCheck(t *testing.T) {
	res := exttest.Check(t, manifest, exttest.Serve(t, program(t)))
	if res.ID != "example" {
		t.Fatalf("initialize answered %+v", res)
	}
}

func TestCheckView(t *testing.T) {
	nodes := make([]string, 300)
	for k := range nodes {
		nodes[k] = fmt.Sprintf(`{"id":"n%d","title":"n%d"}`, k, k)
	}
	for _, c := range []struct {
		view    string
		compact bool
		bad     string
	}{
		{`{"sections": [{"items": [{"type": "text", "text": "x"}]}]}`, false, ""},
		{`{"sections": [{"items": [{"type": "metric", "label": "a"}]}]}`, false, "value"},
		{`{"sections": [{"items": [{"type": "hologram"}]}]}`, false, "no variant"},
		{`{"sections": [{"items": [{"type": "metric", "label": "a", "value": "1", "tone": "pink"}]}]}`, false, "tone"},
		{`{"sections": [{"items": [{"type": "link", "title": "a", "url": "http://example.com"}]}]}`, false, "https"},
		{`{"sections": [{"items": [{"type": "field", "name": "a", "label": "a", "kind": "text"}]}]}`, true, "no fields"},
		{`{"sections": [{"items": [{"type": "text", "text": "` + strings.Repeat("长", 2001) + `"}]}]}`, false, "2000"},
		{`{"sections": [{"items": [{"type": "graph", "nodes": [{"id": "a", "title": "a"}]}]}]}`, false, ""},
		{`{"sections": [{"items": [{"type": "graph", "nodes": [{"id": "a", "title": "a"}, {"id": "b", "title": "b"}], "edges": [{"from": "a", "to": "b"}, {"from": "b", "to": "a"}]}]}]}`, false, "cycle"},
		{`{"sections": [{"items": [{"type": "graph", "nodes": [{"id": "a", "title": "a"}, {"id": "a", "title": "b"}]}]}]}`, false, "twice"},
		{`{"sections": [{"items": [{"type": "graph", "nodes": [{"id": "a", "title": "a", "tone": "positive", "color": "green"}]}]}]}`, false, "a tone or a color"},
		{`{"sections": [{"items": [{"type": "graph", "nodes": [` + strings.Join(nodes, ",") + `]}, {"type": "text", "text": "one more"}]}]}`, false, "300"},
		// a card draws the graph as one of its 12, however many nodes
		{`{"sections": [{"items": [{"type": "graph", "nodes": [` + strings.Join(nodes[:13], ",") + `]}]}]}`, true, ""},
	} {
		err := exttest.CheckView(json.RawMessage(c.view), c.compact)
		if c.bad == "" && err != nil || c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)) {
			t.Errorf("%.60s: want %q, got %v", c.view, c.bad, err)
		}
	}
}

// The view examples of the graph pass the kit.
func TestCheckViewGraphExamples(t *testing.T) {
	for _, name := range []string{"View.v10-work-tree.json", "View.v10-plan-layered.json"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "extension", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := exttest.CheckView(raw, false); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
