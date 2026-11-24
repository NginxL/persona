package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentListsExcludeFoldedAndOrphanSubagents(t *testing.T) {
	for _, source := range []string{"codex_history", "codex_app_server"} {
		for _, state := range []string{proto.StateWorking, proto.StateUnread, proto.StateNeedsYou, "done"} {
			t.Run(source+"/"+state, func(t *testing.T) {
				f := newFakeHub()
				now := time.Now()
				status := state
				var work *proto.WorkState
				if state == "done" {
					status = proto.StateIdle
					work = &proto.WorkState{Phase: proto.WorkDone}
				}
				rows := []proto.Session{
					{SessionID: "parent", Title: "Parent conversation", State: status, UpdatedAt: now, Work: work},
					{SessionID: "helper", Title: "Known child", State: status, UpdatedAt: now, Work: work,
						SourceKind: source, IsSubagent: true, ParentSessionID: "parent"},
					{SessionID: "orphan", Title: "Missing parent", State: status, UpdatedAt: now, Work: work,
						SourceKind: source, IsSubagent: true, ParentSessionID: "not-listed"},
					{SessionID: "unknown", Title: "Read-only sidecar in-session helper", State: status, UpdatedAt: now, Work: work,
						SourceKind: source},
				}
				if state == proto.StateWorking || state == proto.StateNeedsYou {
					rows[0].State = proto.StateIdle // The child's live state must remain visible on the parent.
				}
				foldOf(rows)
				f.setSessions("box", rows...)
				s := newStorelessService(t, f)

				for _, filter := range []string{state, "active", "all"} {
					if state == "done" && filter == "active" {
						continue
					}
					got := s.describeAgents(context.Background(), filter, "", "")
					if strings.Contains(got, "session_id=helper ") || strings.Contains(got, "session_id=orphan ") || strings.Count(got, "session_id=") != 2 {
						t.Fatalf("state=%s must list only two non-subagent roots (parent + unknown), not folded or orphan subagents:\n%s", filter, got)
					}
					for _, id := range []string{"parent", "unknown"} {
						if !strings.Contains(got, "session_id="+id+" ") {
							t.Errorf("state=%s lost root %s:\n%s", filter, id, got)
						}
					}
				}

				ctx := context.Background()
				needs, unread, awaiting, waiting, done, muted, working := s.snapshotAgents(ctx, s.devices(ctx), "")
				got := working
				switch state {
				case proto.StateNeedsYou:
					got = needs
				case proto.StateUnread:
					got = unread
				case "done":
					got = done
				}
				if len(got) != 2 || len(needs)+len(unread)+len(awaiting)+len(waiting)+len(done)+len(muted)+len(working) != 2 {
					t.Fatalf("snapshot must count the same two roots: needs=%v unread=%v done=%v working=%v", needs, unread, done, working)
				}
				for _, row := range got {
					if row.Label == "Known child" || row.Label == "Missing parent" {
						t.Errorf("subagent leaked into the snapshot: %+v", row)
					}
				}
			})
		}
	}
}

func TestAgentsListKeepsFoldedHelperApprovalOnParent(t *testing.T) {
	f := newFakeHub()
	rows := []proto.Session{
		{SessionID: "parent", Title: "Parent conversation", State: proto.StateIdle},
		{SessionID: "helper", Title: "Known child", State: proto.StateNeedsYou, IsSubagent: true, ParentSessionID: "parent"},
	}
	foldOf(rows)
	f.setSessions("box", rows...)
	f.hubCards = []proto.Interaction{{
		ID: "child-approval", MachineID: "box", SessionID: "helper", FoldedUnder: "parent",
		Kind: proto.IxPermission, ToolName: "shell", Summary: "Read the test fixture",
	}}
	s := newStorelessService(t, f)

	got := s.describeAgents(context.Background(), proto.StateNeedsYou, "", "")
	if strings.Count(got, "session_id=") != 1 || !strings.Contains(got, "session_id=parent ") ||
		!strings.Contains(got, "interaction_id=child-approval") {
		t.Fatalf("the parent must keep the child's answerable approval without a second row:\n%s", got)
	}
}

func TestAgentsListKeepsCrossMachineFoldedApprovalOnParent(t *testing.T) {
	f := newFakeHub()
	parent := proto.Session{MachineID: "mac", SessionID: "parent", Title: "Mac lead", State: proto.StateIdle}
	child := proto.Session{MachineID: "box", SessionID: "a2a-kid", Title: "Box worker", State: proto.StateNeedsYou,
		IsSubagent: true, ParentSessionID: "parent"}
	child.SetSubagentProvenance("mac", "task-1", proto.SubagentOriginA2A)
	lists := map[string][]proto.Session{"mac": {parent}, "box": {child}}
	folds := foldFleet(lists, nil)
	folds["mac"].apply(lists["mac"])
	folds["box"].apply(lists["box"])
	f.setSessions("mac", lists["mac"]...)
	f.setSessions("box", lists["box"]...)
	f.hubCards = []proto.Interaction{{
		ID: "cross-approval", MachineID: "box", SessionID: "a2a-kid",
		FoldedUnder: "parent", FoldedUnderMachineID: "mac",
		Kind: proto.IxPermission, ToolName: "shell", Summary: "Run tests on box",
	}}
	s := newStorelessService(t, f)

	got := s.describeAgents(context.Background(), proto.StateNeedsYou, "", "")
	if strings.Count(got, "session_id=") != 1 || !strings.Contains(got, "session_id=parent ") ||
		!strings.Contains(got, "interaction_id=cross-approval") {
		t.Fatalf("cross-machine folded child approval must attach to parent on mac without listing child:\n%s", got)
	}
	// Filtering by parent's machine ("mac") must STILL retain the cross-machine child's interaction_id.
	byParentMachine := s.describeAgents(context.Background(), proto.StateNeedsYou, "mac", "")
	if strings.Count(byParentMachine, "session_id=") != 1 || !strings.Contains(byParentMachine, "session_id=parent ") ||
		!strings.Contains(byParentMachine, "interaction_id=cross-approval") {
		t.Fatalf("agents_list(machine=mac) dropped cross-machine folded child approval:\n%s", byParentMachine)
	}
}
