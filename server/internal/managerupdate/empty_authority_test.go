package managerupdate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

func emptyAuthorityFixture() Authority {
	return Authority{
		ManagerID:  "m-empty",
		Revision:   strings.Repeat("a", 64),
		ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Unassigned: true,
	}
}

func TestEmptyAuthorityRecordAcceptsPairedEmptyAndBindsRowRevision(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base, err := authorityRecord("m-empty", "", "", "10", at, at)
	if err != nil {
		t.Fatalf("paired-empty persisted identity rejected: %v", err)
	}
	if base.ManagerID != "m-empty" || base.Main != "" || base.MachineID != "" || !base.Unassigned || len(base.Revision) != 64 {
		t.Fatalf("empty authority identity was not preserved: %+v", base)
	}
	fresh, err := authorityRecord("m-empty", "", "", "10", at, at.Add(time.Second))
	if err != nil || fresh.Revision != base.Revision {
		t.Fatalf("stable empty authority revision changed with observation time: %+v %v", fresh, err)
	}
	changed, err := authorityRecord("m-empty", "", "", "11", at, at)
	if err != nil || changed.Revision == base.Revision {
		t.Fatalf("empty authority did not bind the existing-row revision: %+v %v", changed, err)
	}
}

func TestEmptyAuthorityRecordRejectsPartialIdentities(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, identity := range []struct{ main, machine string }{{main: "main"}, {machine: "box"}} {
		if _, err := authorityRecord("m-empty", identity.main, identity.machine, "10", at, at); err == nil {
			t.Fatalf("partial empty identity accepted: %+v", identity)
		}
	}
}

func TestEmptyAuthorityRecordRejectsUnsetSettingsRow(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, changed := range []time.Time{time.Time{}, at} {
		if _, err := authorityRecord("m-empty", "", "", "", changed, at); err == nil {
			t.Fatalf("unset settings row was accepted as explicit empty authority: changed=%v", changed)
		}
	}
}

func TestEmptyIdleEvidenceUsesTwoCompleteReadsWithoutNode(t *testing.T) {
	a := emptyAuthorityFixture()
	authorityReads, nodeReads := 0, 0
	result, err := readIdleEvidence(context.Background(), a.ManagerID, "", "", "", func(context.Context) (Authority, error) {
		authorityReads++
		return a, nil
	}, func(context.Context, nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error) {
		nodeReads++
		return nodeclient.UpdateInventory{}, nil
	}, func() time.Time { return a.ObservedAt })
	if err != nil {
		t.Fatalf("explicit empty/unassigned idle evidence rejected: %v", err)
	}
	if !result.Idle || result.CompleteReads != 2 || len(result.SHA256) != 64 || authorityReads != 3 || nodeReads != 0 {
		t.Fatalf("incomplete empty proof: %+v authority reads=%d node reads=%d", result, authorityReads, nodeReads)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Authority Authority                  `json:"authority"`
		Inventory nodeclient.UpdateInventory `json:"inventory"`
	}
	if err := json.Unmarshal(encoded, &proof); err != nil {
		t.Fatal(err)
	}
	expectedTarget := nodeclient.UpdateInventoryTarget{Manager: a.ManagerID, AuthorityRevision: a.Revision}
	if proof.Authority.ManagerID != a.ManagerID || proof.Authority.Main != "" || proof.Authority.MachineID != "" || proof.Authority.Revision != a.Revision {
		t.Fatalf("empty authority identity was fabricated or changed: %+v", proof.Authority)
	}
	if proof.Inventory.Schema != 1 || proof.Inventory.Capability != nodeproto.ManagerUpdateInventory || !proof.Inventory.Complete || len(proof.Inventory.Rows) != 0 || len(proof.Inventory.Revision) != 64 {
		t.Fatalf("empty runtime coverage is incomplete: %+v", proof.Inventory)
	}
	if proof.Inventory.Target != expectedTarget {
		t.Fatalf("empty runtime target was fabricated or not digest-bound: %+v", proof.Inventory.Target)
	}
	incomplete := proof.Inventory
	incomplete.Complete = false
	if err := validateIdleInventory(incomplete, expectedTarget, a.ObservedAt); err == nil {
		t.Fatal("incomplete empty runtime coverage was accepted")
	}
	active := proof.Inventory
	active.Rows = append([]nodeclient.UpdateActivity(nil), proof.Inventory.Rows...)
	active.Rows = append(active.Rows, nodeclient.UpdateActivity{Source: "owned", Session: "new-main", Live: true, Status: "working", Revision: strings.Repeat("c", 64)})
	active.Revision, err = nodeclient.InventoryRevision(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIdleInventory(active, expectedTarget, a.ObservedAt); err == nil {
		t.Fatal("runtime activity appeared in empty/unassigned evidence")
	}
}

func TestEmptyIdleEvidenceRejectsMainOrMachineOnLaterAuthorityRead(t *testing.T) {
	for _, change := range []struct {
		field string
		read  int
	}{{"main", 2}, {"main", 3}, {"machine", 2}, {"machine", 3}, {"revision", 2}, {"revision", 3}} {
		t.Run(change.field+"_on_later_read", func(t *testing.T) {
			a := emptyAuthorityFixture()
			authorityReads, nodeReads := 0, 0
			_, err := readIdleEvidence(context.Background(), a.ManagerID, "", "", "", func(context.Context) (Authority, error) {
				authorityReads++
				next := a
				if authorityReads == change.read {
					if change.field == "main" {
						next.Main = "new-main"
					} else if change.field == "machine" {
						next.MachineID = "new-box"
					} else {
						next.Revision = strings.Repeat("b", 64)
					}
				}
				return next, nil
			}, func(context.Context, nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error) {
				nodeReads++
				return nodeclient.UpdateInventory{}, nil
			}, func() time.Time { return a.ObservedAt })
			if err == nil {
				t.Fatalf("newly assigned %s accepted", change.field)
			}
			if authorityReads < change.read {
				t.Fatalf("later %s assignment was never evaluated: authority reads=%d err=%v", change.field, authorityReads, err)
			}
			if nodeReads != 0 {
				t.Fatalf("empty/unassigned proof dialed a node %d times", nodeReads)
			}
		})
	}
}

func TestEmptyIdleEvidencePopulatedFixtureRemainsValid(t *testing.T) {
	a, in, now := idleReaderFixture()
	authorityReads, nodeReads := 0, 0
	result, err := readIdleEvidence(context.Background(), a.ManagerID, a.Main, a.MachineID, "uid", func(context.Context) (Authority, error) {
		authorityReads++
		return a, nil
	}, func(_ context.Context, target nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error) {
		nodeReads++
		if target != in.Target {
			t.Fatal("populated target changed")
		}
		return in, nil
	}, now)
	if err != nil || !result.Idle || result.CompleteReads != 2 || authorityReads != 3 || nodeReads != 2 || len(result.SHA256) != 64 {
		t.Fatalf("populated idle fixture regressed: %+v authority reads=%d node reads=%d err=%v", result, authorityReads, nodeReads, err)
	}
}
