package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPersistentOwnerProposalSurvivesFormerDeadlineAndRestart(t *testing.T) {
	d, ctx := open(t), context.Background()
	created := time.Now().Add(-3 * time.Minute)
	for _, ix := range []proto.Interaction{
		{ID: "owner_persistent", MachineID: "server", SessionID: "s", Kind: proto.IxPermission,
			CreatedAt: created, ApprovalBatchID: "oab_persistent", OwnerProposal: true, Resumable: true, NoExpiry: true},
		{ID: "owner_former_deadline", MachineID: "server", SessionID: "s", Kind: proto.IxPermission,
			CreatedAt: created, ExpiresAt: created.Add(time.Minute), ApprovalBatchID: "oab_former", OwnerProposal: true, Resumable: true, NoExpiry: true},
		{ID: "owner_not_persistent", MachineID: "server", SessionID: "s", Kind: proto.IxPermission,
			CreatedAt: created, ApprovalBatchID: "oab_short", OwnerProposal: true, Resumable: true},
		{ID: "ordinary_no_expiry_forgery", MachineID: "server", SessionID: "s", Kind: proto.IxPermission,
			CreatedAt: created, Resumable: true, NoExpiry: true},
	} {
		if err := d.InsertInteraction(ctx, ix); err != nil {
			t.Fatal(err)
		}
	}
	kept, cancelled, err := d.ExpireStaleInteractions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keptIDs := make([]string, 0, len(kept))
	for _, ix := range kept {
		keptIDs = append(keptIDs, ix.ID)
		if !ix.OwnerProposal || !ix.NoExpiry || !ix.Resumable {
			t.Errorf("restored proposal lost its metadata: %+v", ix)
		}
	}
	slices.Sort(keptIDs)
	slices.Sort(cancelled)
	if !slices.Equal(keptIDs, []string{"owner_former_deadline", "owner_persistent"}) ||
		!slices.Equal(cancelled, []string{"ordinary_no_expiry_forgery", "owner_not_persistent"}) {
		t.Fatalf("kept=%v cancelled=%v", keptIDs, cancelled)
	}
}
