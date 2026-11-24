package subscriptions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// readsConn is a connector without a catalog of its own, like the built-in ones: its read tools are the catalog
// (connector.ReadCatalog), and the ones in json answer subscriptions with that file (connector.ReadJSON).
type readsConn struct {
	tools []string
	json  map[string]string // tool → the file it answers with
}

func (*readsConn) ID() string   { return "builtin" }
func (*readsConn) Name() string { return "Built-in" }
func (*readsConn) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapTools}}
}
func (r *readsConn) Tools() []proto.Tool {
	var out []proto.Tool
	for _, n := range r.tools {
		out = append(out, proto.Tool{Name: "builtin_" + n, Description: n, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return out
}
func (*readsConn) Call(context.Context, string, json.RawMessage) (string, error) {
	return "the text the models read", nil
}
func (r *readsConn) CallJSON(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, bool, error) {
	file, ok := r.json[strings.TrimPrefix(name, "builtin_")]
	if !ok {
		return nil, false, nil
	}
	b, err := os.ReadFile(file)
	return b, true, err
}

// The built-in connectors' JSON answers (pinned in each one's testdata/readjson) can be followed item by item: a
// dry run without items_path finds their list, every item has an id (no weak dedup) and a title, the times parse
// and the same answer again brings nothing new. A tool without a JSON answer is still text, a snapshot only.
func TestBuiltinReadToolsFollowItemByItem(t *testing.T) {
	gh := func(f string) string {
		return filepath.Join("..", "modules", "github", "testdata", "readjson", f+".json")
	}
	im := func(f string) string {
		return filepath.Join("..", "modules", "imessage", "testdata", "readjson", f+".json")
	}
	ml := func(f string) string {
		return filepath.Join("..", "modules", "mail", "testdata", "readjson", f+".json")
	}
	fields := func(id, thread, title, sender, at, body, link string) *connector.SubscriptionFields {
		return &connector.SubscriptionFields{ID: id, Thread: thread, Title: title, Sender: sender, Time: at, Body: body, Link: link}
	}
	cases := []struct {
		tool, file, items string
		count             int
		fields            *connector.SubscriptionFields
	}{
		{"github_search", gh("search"), "$.items", 2, fields("$.id", "", "$.title", "$.author", "$.time", "$.body", "$.link")},
		{"github_search_code", gh("search_code"), "$.items", 1, fields("$.id", "", "$.title", "", "", "", "$.link")},
		{"github_read", gh("read"), "$.comments", 2, fields("$.id", "$.thread", "$.title", "$.author", "$.time", "$.body", "$.link")},
		{"github_diff", gh("diff"), "$.files", 3, fields("$.id", "", "$.title", "", "", "", "")},
		{"github_checks", gh("checks"), "$.checks", 2, fields("$.id", "", "$.title", "", "$.time", "$.body", "$.link")},
		{"imessage_chats", im("chats"), "$.chats", 5, fields("$.id", "$.thread", "$.title", "", "$.time", "$.body", "")},
		{"imessage_read", im("read"), "$.messages", 2, fields("$.id", "$.thread", "$.title", "$.sender", "$.time", "$.body", "")},
		{"imessage_search", im("search"), "$.hits", 1, fields("$.id", "$.thread", "$.title", "$.sender", "$.time", "$.body", "")},
		{"mail_accounts", ml("accounts"), "$.accounts", 1, fields("$.id", "", "$.name", "", "", "", "")},
		{"mail_search", ml("search"), "$.messages", 2, fields("$.id", "", "$.title", "$.sender", "$.time", "$.body", "")},
	}
	e := newEnv(t)
	conn := &readsConn{json: map[string]string{}}
	for _, tc := range cases {
		conn.tools = append(conn.tools, tc.tool)
		conn.json[tc.tool] = tc.file
	}
	conn.tools = append(conn.tools, "mail_read")
	e.conns.infos["builtin"] = &ConnectorInfo{ID: "builtin", Status: connector.StateOK, Enabled: true, Conn: conn}

	for _, tc := range cases {
		r := Recipe{Mode: "items", Tool: tc.tool, Fields: tc.fields, Kind: "sub_update", GroupBy: "source"}
		dr, err := e.svc.DryRun(e.ctx, DryRunRequest{Connector: "builtin", Source: "builtin", Recipe: &r})
		if err != nil || !dr.OK || dr.Error != nil {
			t.Errorf("%s: %v %+v %+v", tc.tool, err, dr.Error, dr.Checks)
			continue
		}
		if dr.ItemsPath != tc.items || dr.Count != tc.count || len(dr.Items) != min(tc.count, 3) || dr.WeakDedup {
			t.Errorf("%s: items at %q, %d of them (%d shown), weak dedup %v", tc.tool, dr.ItemsPath, dr.Count, len(dr.Items), dr.WeakDedup)
		}
		for _, it := range dr.Items {
			if it.Title == "" || it.UID == "" || tc.fields.Time != "" && it.Time == nil {
				t.Errorf("%s: an item %+v", tc.tool, it)
			}
		}
		if dr.Shape == nil || dr.Shape.Type != "json" {
			t.Errorf("%s: shape %+v", tc.tool, dr.Shape)
		}
		// The setup form's pickers (the first item's leaves that have a value) offer what every item has, named so
		// that their guesses find it.
		offered := map[string]bool{}
		for _, p := range dr.Paths {
			offered[p.Path] = true
		}
		for _, p := range []string{tc.fields.ID, tc.fields.Title, tc.fields.Time} {
			if p != "" && !offered[p] {
				t.Errorf("%s: the pickers do not offer %s (%v)", tc.tool, p, dr.Paths)
			}
		}
	}

	r := Recipe{Mode: "items", Tool: "mail_read", Kind: "sub_mail", GroupBy: "source"}
	dr, err := e.svc.DryRun(e.ctx, DryRunRequest{Connector: "builtin", Source: "builtin", Recipe: &r})
	if err != nil || dr.OK || dr.Error == nil || dr.Error.Code != CodeTextNeedsSnap {
		t.Errorf("a text tool: %v %+v", err, dr.Error)
	}
}
