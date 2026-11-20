package projection

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func linkedParkFacts(id string) Facts {
	return Facts{ID: id, Title: id, Origin: "main", CreatedAt: hours(-4), UpdatedAt: hours(-1),
		Supervision: &Supervision{State: reviewParked, Parked: &Parked{Reason: "Owner permission", Until: hours(4), WaitingOn: parkExternalEvent,
			Evidence: &ParkEvidence{Kind: parkExternalEvent, InteractionID: "ix_read_only"}}}}
}

func TestLinkedInteractionKeepsOneHomeWhenWorkerIsShared(t *testing.T) {
	released := hours(-1)
	linked := linkedParkFacts("todo_a")
	linked.Agents = []Agent{{MachineID: "box", SessionID: "s1", ReleasedAt: &released}}
	other := linkedParkFacts("todo_b")
	other.Supervision = nil
	other.Agents = []Agent{{MachineID: "box", SessionID: "s1"}}
	session := proto.Session{MachineID: "box", SessionID: "s1", State: proto.StateNeedsYou, StateSince: hours(-2),
		Todo: &proto.TodoRef{ID: "todo_b", Title: "Other task"}}
	card := proto.Interaction{ID: "ix_read_only", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, CreatedAt: hours(-2)}
	linked.Sessions, other.Sessions = []proto.Session{session}, []proto.Session{session}
	linked.Interactions, other.Interactions = []proto.Interaction{card}, []proto.Interaction{card}

	views := Views([]Facts{linked, other}, testNow)
	if views[0].Section != assistantwire.TodoListSectionWaiting || views[0].Reason != assistantwire.TodoReasonParked || len(views[0].Asks) != 0 {
		t.Errorf("the non-owner kept a second owner request: %+v", views[0])
	}
	if views[1].Section != assistantwire.TodoListSectionNeedsYou || views[1].Reason != assistantwire.TodoReasonAgentAsks ||
		len(views[1].Asks) != 1 || views[1].Asks[0].Interaction != "ix_read_only" {
		t.Errorf("the tied task lost the existing request: %+v", views[1])
	}
	if got := linked.Supervision.Parked.Evidence.InteractionID; got != "ix_read_only" {
		t.Fatalf("routing changed the source evidence: %q", got)
	}
}

func TestResolvedLinkedSessionReturnsToParkedWithoutSyntheticAsk(t *testing.T) {
	f := linkedParkFacts("todo_a")
	f.Agents = []Agent{{MachineID: "box", SessionID: "s1"}}
	f.Sessions = []proto.Session{{MachineID: "box", SessionID: "s1", State: proto.StateNeedsYou, StateSince: hours(-1),
		Work: &proto.WorkState{InteractionID: "ix_read_only"}}}
	// The exact card is gone, but the session has not caught up yet.
	v := View(f, testNow)
	if v.Section != assistantwire.TodoListSectionWaiting || v.Reason != assistantwire.TodoReasonParked || len(v.Asks) != 0 {
		t.Fatalf("resolved interaction left a synthetic owner ask: %+v", v)
	}

	// A different current request is still real owner input, not the resolved card.
	f.Interactions = []proto.Interaction{{ID: "ix_new", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, CreatedAt: hours(-1)}}
	f.Sessions[0].Work.InteractionID = "ix_new"
	v = View(f, testNow)
	if v.Section != assistantwire.TodoListSectionNeedsYou || len(v.Asks) != 1 || v.Asks[0].Interaction != "ix_new" {
		t.Fatalf("a newer pending request was suppressed: %+v", v)
	}
}
