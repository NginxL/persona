package node

import (
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// linkHerdrChildren links a child to its agent parent when that parent has a listed session, leaves a child without a
// listed ancestor alone, and never makes two rows each other's side.
func TestLinkHerdrChildren(t *testing.T) {
	pane := func(id string) string { return host.PaneRef{Kind: host.HerdrCore, ID: id}.String() }
	agent := func(paneID, parent string) host.Agent {
		a := host.Agent{PaneID: pane(paneID)}
		if parent != "" {
			a.ParentPaneID = pane(parent)
		}
		return a
	}
	list := []proto.Session{
		{SessionID: "lead", PaneID: pane("t1")},
		{SessionID: "child", PaneID: pane("t3")},
		{SessionID: "orphan", PaneID: pane("t4")},
		{SessionID: "loop-a", PaneID: pane("t5")},
		{SessionID: "loop-b", PaneID: pane("t6")},
		{SessionID: "kept", PaneID: pane("t7")},
	}
	list[5].SetParentAgentID("already")
	agents := map[string]host.Agent{
		"lead":   agent("t1", ""),
		"child":  agent("t3", "t1"),
		"orphan": agent("t4", "t9"), // t9 has no listed session
		"loop-a": agent("t5", "t6"),
		"loop-b": agent("t6", "t5"),
		"kept":   agent("t7", "t1"),
	}
	linkHerdrChildren(list, agents)
	got := map[string]string{}
	for i := range list {
		got[list[i].SessionID] = list[i].ParentAgentID()
	}
	want := map[string]string{"lead": "", "child": "lead", "orphan": "", "loop-a": "", "loop-b": "", "kept": "already"}
	for id, parent := range want {
		if got[id] != parent {
			t.Errorf("%s parent_agent_id = %q, want %q", id, got[id], parent)
		}
	}
}
