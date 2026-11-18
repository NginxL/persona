package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// "Try it" checks the main conversation's next turn (readiness_probe_target.go, #10365): the launcher and model a
// message would run, shaped as a turn of that conversation; a pass on another model, or from a node that ran the plain
// check, never clears what the conversation's own turn said.

const routeText = "There's an issue with the selected model (model-a). It may not exist or you may not have access to it."

// lastCheck is the last launchers.check's params.
func lastCheck(t *testing.T, f *fakeHub) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.checkParams) == 0 {
		t.Fatal("no launchers.check")
	}
	return f.checkParams[len(f.checkParams)-1]
}

func checkCount(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.checkParams)
}

// The check runs Main chat's own model on its launcher, as an assistant turn of the main conversation in the profile
// Main chat starts new conversations in, not the launcher's default.
func TestReadinessProbeUsesMainTurnModel(t *testing.T) {
	f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", Assistant: true, Launcher: "claude", State: proto.StateIdle})
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "bad-model", Effort: "high", ConfigDir: "legacy"})
	r := getReadiness(t, s, "probe=1", "en")
	if r.Probe == nil || !r.Probe.OK || !r.Probe.AssistantTurn {
		t.Fatalf("probe: %+v", r.Probe)
	}
	p := lastCheck(t, f)
	want := map[string]any{"name": "claude", "model": "bad-model", "effort": "high", "purpose": "assistant",
		"assistant_turn": true, "conversation": "m1", "config_dir": "legacy"}
	for k, v := range want {
		if p[k] != v {
			t.Fatalf("launchers.check %s = %v, want %v (%v)", k, p[k], v, p)
		}
	}
	kept := s.readinessRecordNow(context.Background()).Probe
	if kept == nil || !kept.Targeted || kept.Launcher != "claude" || kept.Model != "bad-model" || kept.Effort != "high" {
		t.Fatalf("kept probe: %+v", kept)
	}
}

// A kept pick of the main conversation is what its next turn runs: the check goes to the pick's launcher and model.
func TestReadinessProbeUsesConversationPick(t *testing.T) {
	f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", Assistant: true, Launcher: "gw", State: proto.StateIdle})
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", BaseURL: "https://gw.example", Model: "m1"}})
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "opus"})
	if err := s.setModelPick(context.Background(), "m1", &modelPick{MachineID: "server", Launcher: "gw", Model: "vendor/m2", Effort: "low", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := getReadiness(t, s, "probe=1", "en")
	if r.Probe == nil || !r.Probe.OK {
		t.Fatalf("probe: %+v", r.Probe)
	}
	p := lastCheck(t, f)
	if p["name"] != "gw" || p["model"] != "vendor/m2" || p["effort"] != "low" || p["conversation"] != "m1" || p["assistant_turn"] != true {
		t.Fatalf("the check did not run the pick: %v", p)
	}
	// Main chat's launcher is still the one readiness names; the model step is the pick's, which the next turn runs.
	if m := readinessStep(t, r, StepModel); r.Launcher != "claude" || m.State != StepOK || m.Launcher != "gw" {
		t.Fatalf("readiness launcher %s, model step %+v", r.Launcher, m)
	}
}

// A pick on another launcher than Main chat's, after the main conversation's turn failed with model_route there: the
// pick alone clears nothing, and "Try it", which runs the pick, does: its pass of the picked model on the pick's
// launcher is what the next turn runs, so the chat can send again. A pass of anything else (another model of the
// pick's launcher, Main chat's own model there before the pick, a plain check from an older node) clears nothing, and
// a failure of the picked model's own turn is cleared only by a pass of that model.
func TestReadinessProbePickOnAnotherLauncherClearsModelRoute(t *testing.T) {
	failedTurn := proto.Session{SessionID: "m1", Assistant: true, Launcher: "claude", State: proto.StateIdle, LastText: routeText,
		UpdatedAt: time.Now().Add(-time.Minute)}
	setup := func(t *testing.T) (*fakeHub, *Service) {
		f, s, _ := readyMachine(t, failedTurn)
		putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", BaseURL: "https://gw.example", Model: "m1"}})
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "model-a"})
		if err := s.setModelPick(context.Background(), "m1", &modelPick{MachineID: "server", Launcher: "gw", Model: "vendor/m2", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
		return f, s
	}
	seed := func(t *testing.T, s *Service, p readinessProbe) {
		t.Helper()
		if err := s.readinessUpdate(context.Background(), func(r *readinessRecord) { r.Probe = &p }); err != nil {
			t.Fatal(err)
		}
	}
	blocked := func(t *testing.T, r Readiness, why string) {
		t.Helper()
		if m := readinessStep(t, r, StepModel); r.Mode != ReadinessSetup || m.State != StepProblem || m.Code != proto.TurnErrorModelRoute {
			t.Fatalf("%s: %s %+v", why, r.Mode, m)
		}
	}

	t.Run("try it", func(t *testing.T) {
		f, s := setup(t)
		blocked(t, getReadiness(t, s, "", "en"), "the pick alone")
		r := getReadiness(t, s, "probe=1", "en")
		if p := lastCheck(t, f); p["name"] != "gw" || p["model"] != "vendor/m2" || r.Probe == nil || !r.Probe.OK || !r.Probe.AssistantTurn {
			t.Fatalf("the try did not run the pick: %v %+v", p, r.Probe)
		}
		if m := readinessStep(t, r, StepModel); r.Mode != ReadinessFull || m.State != StepOK || m.Launcher != "gw" {
			t.Fatalf("the pass of the picked model left the chat blocked: %s %+v", r.Mode, m)
		}
	})
	t.Run("a pass of something else", func(t *testing.T) {
		for _, p := range []readinessProbe{
			{Machine: "server", Launcher: "gw", Model: "vendor/m1", Targeted: true, Check: proto.LauncherCheck{OK: true, AssistantTurn: true}},
			{Machine: "server", Launcher: "claude", Model: "model-a", Targeted: true, Check: proto.LauncherCheck{OK: true, AssistantTurn: true}},
			{Machine: "other", Launcher: "gw", Model: "vendor/m2", Targeted: true, Check: proto.LauncherCheck{OK: true, AssistantTurn: true}},
			{Machine: "server", Launcher: "gw", Model: "vendor/m2", Targeted: true, Check: proto.LauncherCheck{OK: true}}, // a plain check's
		} {
			_, s := setup(t)
			p.At = time.Now()
			seed(t, s, p)
			blocked(t, getReadiness(t, s, "", "en"), p.Machine+"/"+p.Launcher+"/"+p.Model)
		}
	})
	t.Run("the picked model's own turn failed", func(t *testing.T) {
		f, s := setup(t)
		turn := failedTurn
		turn.Launcher, turn.Model, turn.UpdatedAt = "gw", "vendor/m2", time.Now().Add(-time.Second)
		f.setSessions("server", turn)
		seed(t, s, readinessProbe{Machine: "server", Launcher: "claude", Model: "model-a", Targeted: true, At: time.Now(),
			Check: proto.LauncherCheck{OK: true, AssistantTurn: true}})
		blocked(t, getReadiness(t, s, "", "en"), "a pass of Main chat's own model")
		seed(t, s, readinessProbe{Machine: "server", Launcher: "gw", Model: "vendor/m2", Targeted: true, At: time.Now(),
			Check: proto.LauncherCheck{OK: true, AssistantTurn: true}})
		if m := readinessStep(t, getReadiness(t, s, "", "en"), StepModel); m.State != StepOK {
			t.Fatalf("a pass of the picked model did not clear its turn's failure: %+v", m)
		}
	})
}

// A pass on another model of the launcher, or one an older manager kept for the launcher's default, never clears the
// main conversation's model_route; nor is either reused for a probe of the model the turn runs. A pass of that model
// from a node that shaped the check as the turn does; one from a node that ran the plain check does not.
func TestReadinessProbeOtherModelDoesNotClearModelRoute(t *testing.T) {
	for _, seeded := range []struct {
		name  string
		probe readinessProbe
	}{
		{"other model", readinessProbe{Machine: "server", Launcher: "claude", Model: "model-b", Targeted: true,
			Check: proto.LauncherCheck{OK: true, AssistantTurn: true}}},
		{"legacy record", readinessProbe{Machine: "server", Launcher: "claude", Check: proto.LauncherCheck{OK: true}}},
	} {
		t.Run(seeded.name, func(t *testing.T) {
			f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", Assistant: true, Launcher: "claude", State: proto.StateIdle,
				LastText: routeText, UpdatedAt: time.Now().Add(-time.Minute)})
			setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "model-a"})
			if err := s.readinessUpdate(context.Background(), func(r *readinessRecord) {
				p := seeded.probe
				p.At = time.Now()
				r.Probe = &p
			}); err != nil {
				t.Fatal(err)
			}
			if m := readinessStep(t, getReadiness(t, s, "", "en"), StepModel); m.State != StepProblem || m.Code != proto.TurnErrorModelRoute {
				t.Fatalf("a pass of something else cleared model_route: %+v", m)
			}
			// Asked now, within the reuse window, it checks model-a rather than answering the pass it has.
			f.mu.Lock()
			f.oldCheck = true // a node that runs the plain check: its pass is no answer to the turn's failure
			f.mu.Unlock()
			r := getReadiness(t, s, "probe=1", "en")
			if checkCount(f) != 1 || lastCheck(t, f)["model"] != "model-a" {
				t.Fatalf("reused another model's probe: %v", f.checkParams)
			}
			if m := readinessStep(t, r, StepModel); r.Probe == nil || !r.Probe.OK || m.Code != proto.TurnErrorModelRoute {
				t.Fatalf("the plain check's pass cleared model_route: %+v %+v", r.Probe, m)
			}
			f.mu.Lock()
			f.oldCheck = false
			f.mu.Unlock()
			if err := s.readinessUpdate(context.Background(), func(r *readinessRecord) { r.Probe.At = time.Now().Add(-time.Hour) }); err != nil {
				t.Fatal(err)
			}
			r = getReadiness(t, s, "probe=1", "en")
			if m := readinessStep(t, r, StepModel); r.Probe == nil || !r.Probe.AssistantTurn || m.State != StepOK {
				t.Fatalf("a pass of the turn itself did not clear model_route: %+v %+v", r.Probe, m)
			}
		})
	}
}

// The node shapes the check as the turn only for a Claude Code launcher it runs itself: a main chat on Codex, or on a
// runtime's launcher (the runtime's own probe), always gets the plain check, and its pass of the turn's model clears
// the turn's failure as it did before #10365, or "Try it" could never unblock that chat.
func TestReadinessProbeUnshapedLauncherClearsModelRoute(t *testing.T) {
	for _, l := range []proto.Launcher{
		{Name: "codex", Agent: "codex"},
		{Name: "rt", Source: "dsh", Agent: "claude", Assistant: true},
	} {
		t.Run(l.Name, func(t *testing.T) {
			f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", Assistant: true, Agent: l.Agent, Launcher: l.Name,
				State: proto.StateIdle, LastText: routeText, UpdatedAt: time.Now().Add(-time.Minute)})
			f.mu.Lock()
			f.zshrc = map[string][]proto.Launcher{"server": {l}}
			f.oldCheck = l.Assistant // the runtime's own probe runs it: never shaped as the turn
			f.mu.Unlock()
			setModel(t, s, "assistant", ModelBinding{Provider: launcherProvider(l.Name), Model: "model-a"})
			if m := readinessStep(t, getReadiness(t, s, "", "en"), StepModel); m.Code != proto.TurnErrorModelRoute {
				t.Fatalf("model step before the try: %+v", m)
			}
			r := getReadiness(t, s, "probe=1", "en")
			if p := lastCheck(t, f); p["name"] != l.Name || p["model"] != "model-a" {
				t.Fatalf("the check did not run the turn's model: %v", p)
			}
			if m := readinessStep(t, r, StepModel); r.Probe == nil || !r.Probe.OK || r.Probe.AssistantTurn || m.State != StepOK || r.Mode != ReadinessFull {
				t.Fatalf("the pass of the turn's model left the chat blocked: %s %+v %+v", r.Mode, r.Probe, m)
			}
		})
	}
}

// What a probe may take on the hub, which the clients wait past: web/src/lib/readiness.ts PROBE_HUB_MS and the probe's
// line in docs/protocol/protocol.md ("Main chat readiness") say 202 s. A change here needs them changed too.
func TestReadinessProbeBoundIsWhatClientsWait(t *testing.T) {
	if got := readinessTargetWait + healthCheckTimeout + readinessWaitGet; got != 202*time.Second {
		t.Fatalf("a probe takes up to %v on the hub: change PROBE_HUB_MS and the protocol doc to match", got)
	}
}

// A pick the conversation's next message would be refused on is the probe's answer: nothing runs, nothing is kept and
// the first message is not counted as answered.
func TestReadinessProbePickRefusal(t *testing.T) {
	s, f := pickFixture(t)
	if err := s.setModelPick(context.Background(), "main", &modelPick{MachineID: "server", Launcher: "far", Model: "m", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	res := s.readinessProbe(context.Background(), "server", s.settings(context.Background()))
	if res == nil || res.OK || !strings.Contains(res.Error, "far") {
		t.Fatalf("probe: %+v", res)
	}
	if n := checkCount(f); n != 0 {
		t.Fatalf("a refused turn was checked: %v", f.checkParams)
	}
	if rec := s.readinessRecordNow(context.Background()); rec.Probe != nil || rec.AnsweredAt != nil {
		t.Fatalf("a refused turn was kept: %+v", rec)
	}
}

// A kept pick on an external adapter's launcher runs through the adapter, which no launchers.check can try (the node
// refuses every external name, adapter_probe_unsupported): "Try it" says so without asking, keeps nothing and counts
// no first answer, rather than keeping a failure for a chat that works.
func TestReadinessProbeExternalPickIsNotChecked(t *testing.T) {
	s, f, l := externalPickFixture(t)
	pickExternal(t, s, l, "vendor/model-two")
	f.mu.Lock()
	f.checks = map[string]proto.LauncherCheck{"server/" + l.Name: {Agent: "claude", Error: "adapter_probe_unsupported"}} // the node's answer
	f.mu.Unlock()
	ctx := i18n.With(context.Background(), i18n.En)
	res := s.readinessProbe(ctx, "server", s.settings(ctx))
	if res == nil || res.OK || res.Error != i18n.T(i18n.En, "assistant-readiness.probe_external") {
		t.Fatalf("probe: %+v", res)
	}
	if n := checkCount(f); n != 0 {
		t.Fatalf("the adapter's launcher was checked: %v", f.checkParams)
	}
	if rec := s.readinessRecordNow(ctx); rec.Probe != nil || rec.AnsweredAt != nil {
		t.Fatalf("an untried pick was kept: %+v", rec)
	}
}
