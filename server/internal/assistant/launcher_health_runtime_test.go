package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A runtime's launchers as a node lists them (node/runtimes_registry.go: the source is the runtime's id): agy, which
// can run the main chat, and a DSH one.
var (
	agyRuntimeLauncher = proto.Launcher{Name: "agy", Source: "antigravity", Agent: "agy", Assistant: true}
	dshRuntimeLauncher = proto.Launcher{Name: "dsh", Source: "dsh", Agent: "dsh"}
)

const agyRouteError = `API Error: 400 no account match for model example-model`

// launcherHealthRuntimeHub is launcherHealthHub whose machines also list agy and dsh, and whose main chat runs on agy.
func launcherHealthRuntimeHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := launcherHealthHub(t)
	f.mu.Lock()
	f.launcherList = []proto.Launcher{{Name: "work"}, agyRuntimeLauncher, dshRuntimeLauncher}
	f.mu.Unlock()
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.agy"})
	return s, f
}

// The main chat's launcher is a runtime's (agy) and failed on the main chat's computer 45 minutes ago (#10369): the
// periodic round checks it again like any launcher, once per interval, through launchers.check; a failure says it still
// fails now, and a pass clears it. A runtime's launcher the main chat and Agents do not run on is still never checked,
// nor is agy where it never failed, and the record a pass leaves stays without being checked again.
func TestLauncherHealthRoundRechecksTheMainChatsRuntime(t *testing.T) {
	s, f := launcherHealthRuntimeHub(t)
	ctx := context.Background()
	long := time.Now().Add(-45 * time.Minute)
	s.recordHealth(ctx, "server", "agy", healthResult{err: agyRouteError, source: healthSourceStart, at: long})
	s.recordHealth(ctx, "server", "dsh", healthResult{err: agyRouteError, source: healthSourceStart, at: long})
	f.mu.Lock()
	f.checks["server/agy"] = proto.LauncherCheck{Agent: "agy", Error: agyRouteError}
	f.mu.Unlock()

	s.healthRound(ctx, true)
	checked := checkedNow(f)
	if !slices.Contains(checked, "server/agy") || slices.Contains(checked, "server/dsh") ||
		slices.ContainsFunc(checked, func(c string) bool { return c == "l4/agy" || c == "box/agy" || c == "l4/dsh" || c == "box/dsh" }) {
		t.Fatalf("checked %v: want the failing main chat runtime launcher only, not other runtimes' nor agy where it never failed", checked)
	}
	r := healthState(s, "server", "agy")
	if !r.unusable() || r.Kind != errKindModelRoute || r.Fails != 2 || r.Source != healthSourceCheck || time.Since(r.CheckedAt) > time.Minute {
		t.Fatalf("a failing recheck should say it fails now: %+v", r)
	}

	n := len(checkedNow(f))
	s.healthRound(ctx, true)
	if got := checkedNow(f)[n:]; slices.Contains(got, "server/agy") {
		t.Fatalf("rechecked within the interval: %v", got)
	}

	f.mu.Lock()
	delete(f.checks, "server/agy") // the account fixed
	f.mu.Unlock()
	editHealth(s, "server", "agy", func(r *LauncherHealth) { r.CheckedAt = time.Now().Add(-defaultHealthInterval - time.Minute) })
	s.healthRound(ctx, true)
	if r := healthState(s, "server", "agy"); r.State != healthOK || r.Fails != 0 || r.Source != healthSourceCheck {
		t.Fatalf("a passing recheck should clear the record: %+v", r)
	}

	editHealth(s, "server", "agy", func(r *LauncherHealth) { r.CheckedAt = time.Now().Add(-defaultHealthInterval - time.Minute) })
	n = len(checkedNow(f))
	s.healthRound(ctx, true)
	if got := checkedNow(f)[n:]; slices.Contains(got, "server/agy") {
		t.Fatalf("a runtime's launcher that works was checked again: %v", got)
	}
	if r, ok := s.launcherHealth(ctx, "server", "agy"); !ok || r.State != healthOK {
		t.Fatalf("the passing record should stay while the machine lists the launcher: %+v %v", r, ok)
	}
}

// Agents' default launcher is a runtime's too: its failure is rechecked the same way; the main chat's launcher, not
// failing anywhere, is not checked.
func TestLauncherHealthRoundRechecksAgentsDefaultRuntime(t *testing.T) {
	s, f := launcherHealthRuntimeHub(t)
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.dsh"})
	ctx := context.Background()
	s.recordHealth(ctx, "box", "dsh", healthResult{err: agyRouteError, source: healthSourceStart, at: time.Now().Add(-time.Hour)})

	s.healthRound(ctx, true)
	checked := checkedNow(f)
	if !slices.Contains(checked, "box/dsh") || slices.ContainsFunc(checked, func(c string) bool { return c == "server/dsh" || c == "l4/dsh" || strings.HasSuffix(c, "/agy") }) {
		t.Fatalf("checked %v: want only dsh where it failed", checked)
	}
	if r := healthState(s, "box", "dsh"); r.State != healthOK {
		t.Fatalf("a passing recheck should clear Agents' default: %+v", r)
	}
}

// "Check now" of the main chat's runtime launcher (POST /api/v1/launchers/health/check?launcher=agy) checks it while it
// fails, with the periodic checks off as by default, and answers its new record. Asked for by name, a runtime's launcher
// the machine lists is checked whatever its record says (#14534): agy once it works, dsh that nobody runs on.
func TestLauncherHealthCheckNowRechecksTheMainChatsRuntime(t *testing.T) {
	s, f := launcherHealthRuntimeHub(t)
	f.mu.Lock()
	f.hello.Agents.LauncherHealth.Enabled = nil
	f.mu.Unlock()
	ctx := context.Background()
	s.recordHealth(ctx, "server", "agy", healthResult{err: agyRouteError, source: healthSourceStart, at: time.Now().Add(-45 * time.Minute)})
	s.recordHealth(ctx, "server", "dsh", healthResult{err: agyRouteError, source: healthSourceStart})

	var out struct {
		Records []LauncherHealth `json:"records"`
	}
	if err := json.Unmarshal(postJSON(t, s, "/api/v1/launchers/health/check?machine=server&launcher=agy"), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Records) != 1 || out.Records[0].Launcher != "agy" || out.Records[0].State != healthOK {
		t.Fatalf("check now of agy: %+v", out.Records)
	}
	if r := healthState(s, "server", "agy"); r.State != healthOK {
		t.Fatalf("record after check now: %+v", r)
	}

	n := len(checkedNow(f))
	for _, launcher := range []string{"agy", "dsh"} {
		if err := json.Unmarshal(postJSON(t, s, "/api/v1/launchers/health/check?machine=server&launcher="+launcher), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Records) != 1 || out.Records[0].Launcher != launcher || out.Records[0].State != healthOK {
			t.Fatalf("check now of %s: %+v", launcher, out.Records)
		}
	}
	if got := checkedNow(f)[n:]; !slices.Equal(got, []string{"server/agy", "server/dsh"}) {
		t.Fatalf("checked %v: want the two launchers asked for, once each", got)
	}
	if r := healthState(s, "server", "dsh"); r.State != healthOK {
		t.Fatalf("a passing check now should clear dsh's record: %+v", r)
	}
}

// The case #14534 met: agy is neither the main chat's nor the Agents' launcher, and a failed start left it failing.
// "Test again" checks it and answers its new record (still failing, then passing); a check of one launcher keeps the
// records of the others the machine lists, checked or not, and drops only those of a launcher it no longer lists.
func TestLauncherHealthCheckNowChecksAnyListedRuntimeLauncher(t *testing.T) {
	s, f := launcherHealthHub(t)
	f.mu.Lock()
	f.launcherList = []proto.Launcher{{Name: "work"}, agyRuntimeLauncher, dshRuntimeLauncher}
	f.hello.Agents.LauncherHealth.Enabled = nil
	f.checks["server/agy"] = proto.LauncherCheck{Agent: "agy", Error: "Method not found: session/set_mode"}
	f.mu.Unlock()
	ctx := context.Background()
	s.recordHealth(ctx, "server", "agy", healthResult{err: "Method not found: session/set_mode", source: healthSourceStart, at: time.Now().Add(-30 * time.Minute)})
	s.recordHealth(ctx, "server", "dsh", healthResult{err: agyRouteError, source: healthSourceStart})
	s.recordHealth(ctx, "server", "gone", healthResult{err: agyRouteError, source: healthSourceStart})

	check := func() []LauncherHealth {
		t.Helper()
		var out struct {
			Records []LauncherHealth `json:"records"`
		}
		if err := json.Unmarshal(postJSON(t, s, "/api/v1/launchers/health/check?machine=server&launcher=agy"), &out); err != nil {
			t.Fatal(err)
		}
		return out.Records
	}
	got := check()
	if len(got) != 1 || got[0].Launcher != "agy" || got[0].State == healthOK || got[0].Kind != errKindCLIOutdated || got[0].Source != healthSourceCheck {
		t.Fatalf("test again of a failing agy: %+v", got)
	}
	if r, ok := s.launcherHealth(ctx, "server", "dsh"); !ok || !r.unusable() {
		t.Fatalf("checking agy dropped dsh's record, which the machine still lists: %+v %v", r, ok)
	}
	if r, ok := s.launcherHealth(ctx, "server", "gone"); ok {
		t.Fatalf("the record of a launcher the machine no longer lists stayed: %+v", r)
	}

	f.mu.Lock()
	delete(f.checks, "server/agy") // agy updated
	f.mu.Unlock()
	if got := check(); len(got) != 1 || got[0].State != healthOK {
		t.Fatalf("test again of a fixed agy: %+v", got)
	}
	if r := healthState(s, "server", "agy"); r.State != healthOK || r.unusable() {
		t.Fatalf("a passing test again should clear agy's record: %+v", r)
	}
	if checked := checkedNow(f); slices.ContainsFunc(checked, func(c string) bool { return c != "server/agy" }) {
		t.Fatalf("checked %v: test again of agy checks agy alone", checked)
	}
}

// The main chat's "Test" (readiness ?probe=1) on a launcher whose health record fails (#10369): one timeout leaves the
// record (the round's rule), another failure refreshes it, so it says the launcher fails now; a pass clears it, and the
// setup card with it, on that same answer and on the next read without a probe.
func TestReadinessProbeRefreshesTheMainChatsHealthRecord(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.mu.Lock()
	f.launcherList = []proto.Launcher{agyRuntimeLauncher}
	f.mu.Unlock()
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.agy"})
	ctx := context.Background()
	s.recordHealth(ctx, "server", "agy", healthResult{err: agyRouteError, source: healthSourceStart, at: time.Now().Add(-45 * time.Minute)})
	if r := getReadiness(t, s, "", "en"); r.Mode != ReadinessSetup || readinessStep(t, r, StepModel).Code != proto.TurnErrorModelRoute {
		t.Fatalf("before: %+v", r)
	}

	old := readinessProbeReuse
	readinessProbeReuse = 0
	t.Cleanup(func() { readinessProbeReuse = old })
	f.mu.Lock()
	f.checks = map[string]proto.LauncherCheck{"server/agy": {Agent: "agy", Error: "context deadline exceeded"}}
	f.mu.Unlock()
	getReadiness(t, s, "probe=1", "en")
	if h := healthState(s, "server", "agy"); h.Fails != 1 || h.Kind != errKindModelRoute || h.Source != healthSourceStart {
		t.Fatalf("one timeout should leave the record as it was: %+v", h)
	}

	f.mu.Lock()
	f.checks = map[string]proto.LauncherCheck{"server/agy": {Agent: "agy", Error: agyRouteError}}
	f.mu.Unlock()
	if r := getReadiness(t, s, "probe=1", "en"); r.Probe == nil || r.Probe.OK || r.Mode != ReadinessSetup {
		t.Fatalf("failing test: %+v", r)
	}
	if h := healthState(s, "server", "agy"); !h.unusable() || h.Fails != 2 || h.Source != healthSourceCheck || time.Since(h.CheckedAt) > time.Minute {
		t.Fatalf("a failing test should say the launcher fails now: %+v", h)
	}

	f.mu.Lock()
	f.checks = nil
	f.mu.Unlock()
	r := getReadiness(t, s, "probe=1", "en")
	if r.Probe == nil || !r.Probe.OK || r.Mode != ReadinessFull || readinessStep(t, r, StepModel).State != StepOK {
		t.Fatalf("passing test: %+v", r)
	}
	if h := healthState(s, "server", "agy"); h.State != healthOK || h.Fails != 0 {
		t.Fatalf("a passing test should clear the record: %+v", h)
	}
	if r := getReadiness(t, s, "", "en"); r.Mode != ReadinessFull {
		t.Fatalf("after: %+v", r)
	}
}

// A test of a launcher with no health record keeps none, and a failing one never starts a record (Test's rule).
func TestReadinessProbeKeepsNoNewHealthRecord(t *testing.T) {
	f, s, _ := readyMachine(t)
	f.mu.Lock()
	f.checks = map[string]proto.LauncherCheck{"server/claude": {Error: agyRouteError}}
	f.mu.Unlock()
	getReadiness(t, s, "probe=1", "")
	if r, ok := s.launcherHealth(context.Background(), "server", "claude"); ok {
		t.Fatalf("a failing test started a record: %+v", r)
	}
}

// Only a runtime's own launchers take the main chat's and Agents' recheck: not the desktop apps' (their check runs no
// prompt and always fails, so it would keep a failure alive for good), an external source's, or the shell's and
// Persona's own (checked every interval anyway).
func TestRuntimeLauncherForHealth(t *testing.T) {
	for _, tc := range []struct {
		l    proto.Launcher
		want bool
	}{
		{agyRuntimeLauncher, true},
		{dshRuntimeLauncher, true},
		{proto.Launcher{Name: "grok", Source: "paseo", Agent: "grok"}, true},
		{proto.Launcher{Name: "claude-desktop", Source: proto.SourceClaudeDesktop}, false},
		{proto.Launcher{Name: "codex-app", Source: "codex_app", Agent: "codex"}, false},
		{proto.Launcher{Name: "external_example", Source: proto.ExternalLauncherSource}, false},
		{proto.Launcher{Name: "work", Source: "zshrc"}, false},
		{proto.Launcher{Name: "wrap", Source: "path"}, false},
		{proto.Launcher{Name: "gw", Source: "persona"}, false},
		{proto.Launcher{Name: "claude", Source: "builtin"}, false},
		{proto.Launcher{Name: "old"}, false},
	} {
		if got := runtimeLauncher(tc.l); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.l, got, tc.want)
		}
	}
}
