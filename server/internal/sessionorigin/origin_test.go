package sessionorigin

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestClientPaneSourceCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/pane_sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Want string
		Session    proto.Session
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := Of(c.Session); got != c.Want {
				t.Fatalf("origin=%q, want %q", got, c.Want)
			}
		})
	}
	if got := Of(proto.Session{Host: "cindy"}); got != "desktop" {
		t.Fatalf("cindy origin=%q, want desktop", got)
	}
	if got := Of(proto.Session{Host: "t3code"}); got != "desktop" {
		t.Fatalf("t3code origin=%q, want desktop", got)
	}
}

// ChatBridge is every pane Of calls "feishu", and a terminal pane of the herdr session a chat bridge runs its agents in
// on its herdr backend: its managed session, named as its own panes are (from the shared fixture), or an older
// version's "bmx-<id>" one. Other named herdr sessions, the own server's panes and desktop rows are not (#8845).
func TestChatBridge(t *testing.T) {
	raw, err := os.ReadFile("testdata/pane_sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Want string
		Session    proto.Session
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bridge := ""
	for _, c := range cases {
		if got := ChatBridge(c.Session); got != (c.Want == "feishu") {
			t.Errorf("%s: ChatBridge=%v, want %v", c.Name, got, c.Want == "feishu")
		}
		if c.Want == "feishu" && bridge == "" {
			bridge, _, _ = strings.Cut(c.Session.PaneID, ":")
		}
	}
	if bridge == "" {
		t.Fatal("no feishu row in the fixture")
	}
	for _, tc := range []struct {
		pane string
		want bool
	}{
		{"@" + bridge + ":wCD:p1", true},
		{"@bmx-0123abcd:w1:p2", true},
		{"@work:w1:p2", false},
		{"@" + bridge + "x:w1:p2", false},
		{"w1:p2", false},
		{"@:w1", false},
	} {
		if got := ChatBridge(proto.Session{PaneID: tc.pane}); got != tc.want {
			t.Errorf("pane %q: ChatBridge=%v, want %v", tc.pane, got, tc.want)
		}
	}
	if ChatBridge(proto.Session{PaneID: "@" + bridge + ":w1:p2", SpaceKind: "codex_app"}) {
		t.Error("a desktop row is not a chat bridge's")
	}
}
