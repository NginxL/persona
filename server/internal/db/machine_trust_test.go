package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMachineTrust(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if list, err := d.PendingMachines(ctx); err != nil || len(list) != 0 {
		t.Fatalf("no rows: every computer is confirmed: %v %v", list, err)
	}
	if err := d.SetMachinePending(ctx, "laptop", "dev-1"); err != nil {
		t.Fatal(err)
	}
	list, err := d.PendingMachines(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "laptop" || list[0].AddedBy != "dev-1" || list[0].CreatedAt.IsZero() {
		t.Fatalf("pending %+v %v", list, err)
	}
	if ok, err := d.ConfirmMachine(ctx, "other"); err != nil || ok {
		t.Fatalf("confirming one that is not pending: %v %v", ok, err)
	}
	if ok, err := d.ConfirmMachine(ctx, "laptop"); err != nil || !ok {
		t.Fatalf("confirm %v %v", ok, err)
	}
	if ok, _ := d.ConfirmMachine(ctx, "laptop"); ok {
		t.Fatal("confirmed twice")
	}
	if list, _ := d.PendingMachines(ctx); len(list) != 0 {
		t.Fatalf("confirmed is not pending: %+v", list)
	}
	if err := d.SetMachinePending(ctx, "laptop", "dev-2"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.PendingMachines(ctx); len(list) != 1 || list[0].AddedBy != "dev-2" {
		t.Fatalf("pending again %+v", list)
	}
	if err := d.ForgetMachineTrust(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.PendingMachines(ctx); len(list) != 0 {
		t.Fatalf("forgotten %+v", list)
	}
}

func TestRevokeMachineCredentials(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	for _, tok := range []DeviceToken{
		{ID: "link-1", Name: "laptop", Kind: "link", Role: "link", MachineID: "laptop"},
		{ID: "link-2", Name: "desk", Kind: "link", Role: "link", MachineID: "desk"},
		{ID: "phone-1", Name: "phone", Kind: "phone", Role: "full"},
		{ID: "peer-1", Name: "hub", Kind: "peer", Role: "peer", MachineID: "laptop"}, // a peer hub whose id is the same
	} {
		if err := d.CreateDeviceToken(ctx, tok, "hash-"+tok.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.CreatePairToken(ctx, "pair-laptop", time.Now().Add(10*time.Minute), "laptop"); err != nil {
		t.Fatal(err)
	}
	ids, err := d.RevokeMachineCredentials(ctx, "laptop")
	if err != nil || len(ids) != 1 || ids[0] != "link-1" {
		t.Fatalf("revoked %v %v", ids, err)
	}
	if tok, _ := d.DeviceTokenByHash(ctx, "hash-link-1"); tok != nil {
		t.Fatal("the laptop's credential still works")
	}
	for _, h := range []string{"hash-link-2", "hash-phone-1", "hash-peer-1"} {
		if tok, _ := d.DeviceTokenByHash(ctx, h); tok == nil {
			t.Fatalf("%s was revoked too", h)
		}
	}
	if p, _ := d.PairTokenByHash(ctx, "pair-laptop"); p == nil || p.ExpiresAt.After(time.Now()) {
		t.Fatalf("the laptop's pairing code still works: %+v", p)
	}
}

// Whether a computer has a Persona Link already (a switch from ssh refuses one that has): a live link credential or
// a pairing code for it that is still open counts; a revoked credential, a used or expired code, another computer's,
// and a peer hub's credential that binds the same id do not.
func TestMachineLinkInUse(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	inUse := func(machine string) bool {
		t.Helper()
		got, err := d.MachineLinkInUse(ctx, machine)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if inUse("laptop") {
		t.Fatal("nothing yet")
	}
	if err := d.CreateDeviceToken(ctx, DeviceToken{ID: "peer-1", Name: "hub", Kind: "peer", Role: "peer", MachineID: "laptop"}, "hash-peer"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateDeviceToken(ctx, DeviceToken{ID: "link-desk", Name: "desk", Kind: "link", Role: "link", MachineID: "desk"}, "hash-desk"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreatePairToken(ctx, "pair-desk", time.Now().Add(time.Hour), "desk"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreatePairToken(ctx, "pair-over", time.Now().Add(-time.Minute), "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreatePairToken(ctx, "pair-app", time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if inUse("laptop") {
		t.Fatal("a peer's credential, another computer's, an expired code or an app's code counts")
	}
	if err := d.CreatePairToken(ctx, "pair-open", time.Now().Add(time.Hour), "laptop"); err != nil {
		t.Fatal(err)
	}
	if !inUse("laptop") {
		t.Fatal("an open pairing code does not count")
	}
	link := DeviceToken{ID: "link-1", Name: "laptop", Kind: "link", Role: "link", MachineID: "laptop"}
	if ok, err := d.RedeemPairToken(ctx, "pair-open", link, "hash-link-1"); err != nil || !ok {
		t.Fatalf("redeem: %v %v", ok, err)
	}
	if !inUse("laptop") {
		t.Fatal("a live credential does not count")
	}
	if ok, err := d.RevokeDeviceToken(ctx, "link-1"); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if inUse("laptop") {
		t.Fatal("a revoked credential or a used code counts")
	}
	if !inUse("desk") {
		t.Fatal("desk's credential and code do not count")
	}
	if _, err := d.MachineLinkInUse(ctx, ""); err == nil {
		t.Fatal("no machine accepted")
	}
}

// A switch's audit rows: the computer's latest, and one operation's history, oldest first; another action's rows and
// another computer's are not in them. The audit is not cleared between tests: the computers are this run's.
func TestLinkSwitchRows(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	run := fmt.Sprintf("%x", time.Now().UnixNano())
	laptop, desk := "rows-laptop-"+run, "rows-desk-"+run
	if latest, err := d.LinkSwitchLatest(ctx, laptop); err != nil || latest != nil {
		t.Fatalf("never switched: %+v %v", latest, err)
	}
	for _, a := range []struct{ machine, action, detail string }{
		{laptop, "link_switch", `{"operation":"ls_a","step":"begin","by":"device"}`},
		{laptop, "link_switch", `{"operation":"ls_a","step":"begun","pair_hash":"h-a"}`},
		{desk, "link_switch", `{"operation":"ls_a","step":"begin"}`},
		{laptop, "code", `{"operation":"ls_a","step":"rolled_back"}`},
		{laptop, "link_switch", `{"operation":"ls_a","step":"succeeded"}`},
		{laptop, "link_switch", `{"operation":"ls_b","step":"begin"}`},
	} {
		if err := d.AddMachineAudit(ctx, a.machine, a.action, "manager:assistant", json.RawMessage(a.detail)); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := d.LinkSwitchLatest(ctx, laptop)
	if err != nil || latest == nil || latest.Operation != "ls_b" || latest.Step != "begin" || latest.Actor != "manager:assistant" || latest.At.IsZero() {
		t.Fatalf("latest %+v %v", latest, err)
	}
	history, err := d.LinkSwitchHistory(ctx, laptop, "ls_a")
	var steps []string
	for _, r := range history {
		steps = append(steps, r.Step+":"+r.PairHash)
	}
	if err != nil || strings.Join(steps, " ") != "begin: begun:h-a succeeded:" {
		t.Fatalf("history %v %v", steps, err)
	}
	if history, err := d.LinkSwitchHistory(ctx, laptop, "ls_none"); err != nil || len(history) != 0 {
		t.Fatalf("an operation that never began: %+v %v", history, err)
	}
	if _, err := d.LinkSwitchHistory(ctx, laptop, ""); err == nil {
		t.Fatal("no operation accepted")
	}
}

func TestMachineAudit(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if err := d.AddMachineAudit(ctx, "laptop", "confirm", "dev-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.AddMachineAudit(ctx, "laptop", "reject", "dev-1", []byte(`{"why":"x"}`)); err != nil {
		t.Fatal(err)
	}
	list, err := d.MachineAudits(ctx, "laptop")
	if err != nil || len(list) != 2 || list[0].Action != "confirm" || list[1].Action != "reject" || string(list[1].Detail) != `{"why": "x"}` {
		t.Fatalf("audit %+v %v", list, err)
	}
}
