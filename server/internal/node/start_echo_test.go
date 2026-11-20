package node

import (
	"github.com/nwverse/persona/server/internal/proto"
	"path/filepath"
	"testing"
)

func TestStartEchoCorrelatesItsFirstInput(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	named := "0e000000-0000-4000-8000-0000000000bb"
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.screenSteps("w1:p2", isKeys(), screenFixture(t, "claude-2.1.312-trust-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.txt"))
	promptLands(t, f, home, cwd)
	_, err := request(t, n, "session.start", map[string]any{"session_id": named, "cwd": "~/proj", "prompt": "fix the build", "client_msg_id": "inline-first"})
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, it := range pageItems(t, n, named) {
		if it.ClientMsgID == "inline-first" && it.Role == "user" {
			matched = true
		}
	}
	if !matched {
		t.Fatal("first transcript input did not receive the start correlator")
	}
	rec, ok := n.echoes.record(named, "inline-first")
	if !ok || rec.Text != "fix the build" || rec.AfterSeq != 0 {
		t.Fatalf("record %+v present=%v", rec, ok)
	}
}
