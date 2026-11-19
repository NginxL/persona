package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentListsFoldOnlyHelpersWithListedParents(t *testing.T) {
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
					if strings.Contains(got, "session_id=helper ") || strings.Count(got, "session_id=") != 3 {
						t.Fatalf("state=%s must list three roots, not the folded helper:\n%s", filter, got)
					}
					for _, id := range []string{"parent", "orphan", "unknown"} {
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
				if len(got) != 3 || len(needs)+len(unread)+len(awaiting)+len(waiting)+len(done)+len(muted)+len(working) != 3 {
					t.Fatalf("snapshot must count the same three roots: needs=%v unread=%v done=%v working=%v", needs, unread, done, working)
				}
				for _, row := range got {
					if row.Label == "Known child" {
						t.Errorf("folded helper leaked into the snapshot: %+v", row)
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
