package managerupdate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func identityOnly(p Plan) Plan {
	return Plan{Schema: p.Schema, ManagerID: p.ManagerID, Dir: p.Dir, Binary: p.Binary,
		Unit: p.Unit, PlistPath: p.PlistPath, UID: p.UID,
		PreviousSHA256: p.PreviousSHA256, ConfigSHA256: p.ConfigSHA256, PlistSHA256: p.PlistSHA256}
}

func TestAuthorityIdentityDoesNotRequireUnobservedIdleOrRelease(t *testing.T) {
	c, p := fixture(t)
	p = identityOnly(p)
	if err := p.validateIdentity(); err != nil {
		t.Fatal(err)
	}
	// Stop before a database connection, after proving identity-only input reaches
	// the actual read-only ownership checks rather than release-plan validation.
	stop := errors.New("ownership verification sentinel")
	c.FileCheck = func(string, int) error { return stop }
	before := tree(t, c.Home)
	if _, err := c.ReadAuthority(context.Background(), p); !errors.Is(err, stop) {
		t.Fatalf("identity-only read failed before ownership verification: %v", err)
	}
	if !reflect.DeepEqual(before, tree(t, c.Home)) {
		t.Fatal("identity-only authority preflight wrote state")
	}
	if err := p.validateShape(); err == nil {
		t.Fatal("identity-only input accepted as activation plan")
	}
}

func TestActivationShapeStillRequiresIdleDigest(t *testing.T) {
	_, p := fixture(t)
	p.IdleSnapshotSHA256 = ""
	if err := p.validateShape(); err == nil {
		t.Fatal("release without idle digest accepted")
	}
}

func TestIdentityRejectsMissingOrConflictingTargetFields(t *testing.T) {
	for _, change := range []func(*Plan){
		func(p *Plan) { p.ManagerID = "assistant" },
		func(p *Plan) { p.UID = -1 },
		func(p *Plan) { p.ConfigSHA256 = "" },
		func(p *Plan) { p.PreviousSHA256 = "" },
		func(p *Plan) { p.PlistSHA256 = "" },
		func(p *Plan) { p.Dir = "relative" },
		func(p *Plan) { p.Binary = "/shared/personad" },
		func(p *Plan) { p.PlistPath = "relative.plist" },
		func(p *Plan) { p.Unit = "other" },
	} {
		_, p := fixture(t)
		p = identityOnly(p)
		change(&p)
		if err := p.validateIdentity(); err == nil {
			t.Fatalf("invalid identity accepted: %+v", p)
		}
	}
}

func TestEmptyInventoryRequiresExplicitUnassignedPlan(t *testing.T) {
	c, p := fixture(t)
	p = identityOnly(p)
	stop := errors.New("ownership verification sentinel")
	calls := 0
	c.FileCheck = func(string, int) error { calls++; return stop }
	before := tree(t, c.Home)
	if _, err := c.ReadIdleEvidence(context.Background(), p, "", "", ""); err == nil || calls != 0 {
		t.Fatalf("empty flags without an unassigned plan reached authority: calls=%d err=%v", calls, err)
	}
	p.Unassigned = true
	if _, err := c.ReadIdleEvidence(context.Background(), p, "", "", ""); !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("explicit unassigned plan did not reach its own authority read: calls=%d err=%v", calls, err)
	}
	if !reflect.DeepEqual(before, tree(t, c.Home)) {
		t.Fatal("empty inventory preflight wrote state")
	}
}

func TestInventoryOnlyIdentityCannotActivate(t *testing.T) {
	for _, change := range []func(*Plan){
		func(p *Plan) { p.Unassigned = true },
		func(p *Plan) { p.Main, p.MachineID, p.MachineUID = "main", "box", "uid" },
	} {
		_, p := fixture(t)
		change(&p)
		if err := p.validateShape(); err == nil {
			t.Fatal("inventory-only identity fields were accepted in an activation plan")
		}
	}
}

func TestInventoryMainRequiresDeliberatelyEmptyFlags(t *testing.T) {
	c, _ := fixture(t)
	err := Main(context.Background(), c.Build, []string{"inventory", "--plan", "-"}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "deliberately empty") {
		t.Fatalf("omitted identity flags were not rejected explicitly: %v", err)
	}
}

func TestInventoryAcceptsStrictIdentityOnStdinOnly(t *testing.T) {
	_, full := fixture(t)
	want := identityOnly(full)
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readActionPlan("inventory", "-", strings.NewReader(string(data)))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory stdin: %+v %v", got, err)
	}
	for _, action := range []string{"check", "start", "run", "status", "confirm", "rollback"} {
		if _, err := readActionPlan(action, "-", nil); err == nil {
			t.Fatalf("%s accepted stdin identity", action)
		}
	}
	for _, invalid := range []string{
		`{"schema":1,"unknown":true}`,
		`{"schema":1} {"schema":1}`,
		"{",
		"{}" + strings.Repeat(" ", 64*1024),
	} {
		if _, err := readActionPlan("inventory", "-", strings.NewReader(invalid)); err == nil {
			t.Fatal("malformed, extra, oversized or unknown-field input accepted")
		}
	}
}
