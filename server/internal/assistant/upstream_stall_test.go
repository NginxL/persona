package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func newUpstreamFleetTest(t *testing.T) *fleetTest {
	t.Helper()
	ft := newFleetTestNoStore(t)
	ft.f.devices = []Device{{ID: "mac", Name: "Mac", Online: true}}
	return ft
}

func TestSingle5xxStaysUnmarkedAndRetriesQuietly(t *testing.T) {
	ft := newUpstreamFleetTest(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Minute)
	row := proto.Session{
		SessionID: "s1",
		Title:     "build-worker",
		Cwd:       "/work/app",
		Agent:     "claude",
		Status:    proto.StatusIdle,
		UpdatedAt: t0,
		Rev:       1,
		LastText:  "API Error: 503 Service Unavailable",
		TurnError: "API Error: 503 Service Unavailable",
	}
	ft.sessions("mac", row)

	sess := listed(t, ft.s, "mac", "s1")
	if sess.TurnErrorCode != "" || sess.TurnErrorKind == proto.TurnErrorUser {
		t.Fatalf("single 503 marked: code=%q kind=%q", sess.TurnErrorCode, sess.TurnErrorKind)
	}
	w, by, _ := triageRules(sess, time.UTC, i18n.ZhHans)
	if w == nil || w.On != proto.WaitTransient || by != "rule" {
		t.Fatalf("triage of single 503: %+v (%s)", w, by)
	}
	needs, _, _, _, _, _, _ := ft.s.snapshotAgents(ctx, []Device{{ID: "mac", Name: "mac", Online: true}}, "")
	if len(needs) != 0 {
		t.Fatalf("single 503 appeared in snapshot Needs: %+v", needs)
	}
	if list := ft.s.describeAgents(ctx, "active", "", ""); strings.Contains(list, "upstream_unavailable") {
		t.Fatalf("single 503 marked upstream_unavailable in agents_list:\n%s", list)
	}
}

func TestAccountExhaustionSurfacesImmediatelyForExternalLoopSession(t *testing.T) {
	ft := newUpstreamFleetTest(t)
	ctx := context.Background()
	const poolErr = "API Error: 503 no eligible upstream account is currently available"
	t0 := time.Now().Add(-30 * time.Second)
	// External herdr /loop session (StartedByPersona false, StatusIdle -> StateIdle).
	row := proto.Session{
		SessionID: "ext-loop-1",
		Title:     "nightly-loop",
		Cwd:       "/work/runner",
		Agent:     "claude",
		Host:      "herdr",
		Status:    proto.StatusIdle,
		UpdatedAt: t0,
		Rev:       10,
		LastText:  poolErr,
		TurnError: poolErr,
		Loop:      &proto.LoopInfo{Kind: "cron", Cron: "*/5 * * * *", Prompt: "check status"},
	}
	ft.sessions("mac", row)

	sess := listed(t, ft.s, "mac", "ext-loop-1")
	if sess.TurnErrorCode != proto.TurnErrorUpstreamUnavailable || sess.TurnErrorKind == proto.TurnErrorUser {
		t.Fatalf("pool exhaustion: code=%q kind=%q", sess.TurnErrorCode, sess.TurnErrorKind)
	}
	if sess.State != proto.StateIdle {
		t.Fatalf("wire State changed to %q, want idle", sess.State)
	}

	needs, _, _, waiting, _, _, _ := ft.s.snapshotAgents(ctx, []Device{{ID: "mac", Name: "mac", Online: true}}, "")
	if len(needs) != 1 || len(waiting) != 0 || !strings.Contains(needs[0].Line, poolErr) {
		t.Fatalf("snapshot Needs=%+v Waiting=%+v", needs, waiting)
	}

	active := ft.s.describeAgents(ctx, "active", "", "")
	if !strings.Contains(active, "[上游不可用]") || !strings.Contains(active, " · upstream_unavailable") ||
		!strings.Contains(active, "上游异常："+poolErr) {
		t.Fatalf("agents_list active missing upstream_unavailable visibility:\n%s", active)
	}
	needsList := ft.s.describeAgents(ctx, proto.StateNeedsYou, "", "")
	if !strings.Contains(needsList, "session_id=ext-loop-1") {
		t.Fatalf("agents_list state=needs_you omitted stalled external session:\n%s", needsList)
	}
}

func TestConsecutive5xxStallAndRecoveryAcrossHubAndCodexRows(t *testing.T) {
	ft := newUpstreamFleetTest(t)
	ctx := context.Background()
	t0 := time.Now().Add(-5 * time.Minute)

	// Turn 1: Codex/older-node style TurnError without "API Error:" prefix — single 503 is unmarked.
	r1 := proto.Session{
		SessionID: "cx-1",
		Title:     "codex-worker",
		Cwd:       "/work/app",
		Agent:     "codex",
		Status:    proto.StatusDone,
		UpdatedAt: t0,
		Rev:       1,
		TurnError: "unexpected status 503 Service Unavailable",
	}
	ft.sessions("mac", r1)
	// Re-delivering the exact same stopped row, or a trailing metadata append that bumps Rev without changing UpdatedAt/error,
	// must not double-count it as a second turn.
	ft.sessions("mac", r1)
	r1MetaAppend := r1
	r1MetaAppend.Rev = 2
	r1MetaAppend.Title = "codex-worker-renamed"
	ft.sessions("mac", r1MetaAppend)
	if sess := listed(t, ft.s, "mac", "cx-1"); sess.TurnErrorCode != "" {
		t.Fatalf("repeated/rev-bumped delivery of turn 1 marked %q", sess.TurnErrorCode)
	}

	// Working in between preserves the streak without showing an error while running.
	working := r1MetaAppend
	working.Status, working.Rev, working.TurnError = proto.StatusWorking, 3, ""
	working.UpdatedAt = t0.Add(time.Minute)
	ft.sessions("mac", working)
	if sess := listed(t, ft.s, "mac", "cx-1"); sess.TurnError != "" || sess.TurnErrorCode != "" {
		t.Fatalf("working turn exposed stale error: %+v", sess)
	}

	// Turn 2: second consecutive no-work 503 (even with gateway model_route-shaped text) marks upstream_unavailable with attempts=2.
	r2 := r1
	r2.Rev = 4
	r2.UpdatedAt = t0.Add(2 * time.Minute)
	r2.TurnError = "API Error: 503 model claude-x not available, service unavailable"
	r2.LastText = r2.TurnError
	ft.sessions("mac", r2)
	sess := listed(t, ft.s, "mac", "cx-1")
	if sess.TurnErrorCode != proto.TurnErrorUpstreamUnavailable {
		t.Fatalf("second consecutive 503: code=%q, want %q", sess.TurnErrorCode, proto.TurnErrorUpstreamUnavailable)
	}
	active := ft.s.describeAgents(ctx, "active", "", "")
	if !strings.Contains(active, " · upstream_unavailable attempts=2") {
		t.Fatalf("agents_list missing attempts=2:\n%s", active)
	}
	needs, _, _, _, _, _, _ := ft.s.snapshotAgents(ctx, []Device{{ID: "mac", Name: "mac", Online: true}}, "")
	if len(needs) != 1 || !strings.Contains(needs[0].Line, "2") {
		t.Fatalf("snapshot Needs missing 2-turn streak line: %+v", needs)
	}

	// Turn 3: a turn that did substantive work before hitting 503 resets the consecutive no-work streak.
	r3 := r2
	r3.Rev = 5
	r3.UpdatedAt = t0.Add(3 * time.Minute)
	r3.TurnError = "API Error: 503 Service Unavailable"
	r3.LastText = r3.TurnError
	r3.TurnErrorAfterWork = true
	ft.sessions("mac", r3)
	if sess := listed(t, ft.s, "mac", "cx-1"); sess.TurnErrorCode != "" {
		t.Fatalf("503 after substantive work stayed marked: %q", sess.TurnErrorCode)
	}

	// Turn 4: normal completion clears all upstream error state, and a single 503 afterwards starts fresh at 1.
	r4 := proto.Session{
		SessionID: "cx-1",
		Title:     "codex-worker",
		Cwd:       "/work/app",
		Agent:     "codex",
		Status:    proto.StatusDone,
		UpdatedAt: t0.Add(4 * time.Minute),
		Rev:       6,
		LastText:  "All checks green.",
	}
	ft.sessions("mac", r4)
	if sess := listed(t, ft.s, "mac", "cx-1"); sess.TurnError != "" || sess.TurnErrorCode != "" {
		t.Fatalf("recovered row kept error: %+v", sess)
	}
	r5 := r1
	r5.Rev = 7
	r5.UpdatedAt = t0.Add(5 * time.Minute)
	ft.sessions("mac", r5)
	if sess := listed(t, ft.s, "mac", "cx-1"); sess.TurnErrorCode != "" {
		t.Fatalf("single 503 after recovery marked %q", sess.TurnErrorCode)
	}
}

func TestUpstreamUnavailableReadinessStaysNonBlockingInAllLocales(t *testing.T) {
	if readinessBlocks(ReadinessStep{State: StepProblem, Code: proto.TurnErrorUpstreamUnavailable}) {
		t.Fatal("upstream_unavailable must not put readiness into setup mode or block sending")
	}
	s := &Service{}
	titles := map[string]bool{}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		st := s.readinessStep(lang, StepModel, StepProblem, proto.TurnErrorUpstreamUnavailable, "mac", "gw", "test_assistant")
		if st.Title == "" || strings.Contains(st.Title, "upstream_unavailable") ||
			st.Detail == "" || strings.Contains(st.Detail, "upstream_unavailable") ||
			st.Action == nil || st.Action.ID != "test_assistant" {
			t.Fatalf("%s: %+v", lang, st)
		}
		titles[st.Title] = true
	}
	if len(titles) != 3 {
		t.Fatalf("expected 3 distinct localized titles, got %v", titles)
	}
}
