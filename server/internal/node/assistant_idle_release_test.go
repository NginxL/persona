package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A policy without release fields is what an older manager sends, and what a newer one sends while neither setting
// is saved: on the wire it stays the shape #8700 defined, so older nodes and stored records see nothing new.
func TestIdleReleasePolicyWireShape(t *testing.T) {
	raw, _ := json.Marshal(idleCompactPolicy)
	if string(raw) != `{"enabled":true,"idle_minutes":30,"context_tokens":300000}` {
		t.Fatalf("policy without release fields: %s", raw)
	}
	withRelease := idleCompactPolicy
	withRelease.ReleaseMinutes, withRelease.ReleaseTokens = 45, 200000
	raw, _ = json.Marshal(withRelease)
	var old struct { // the type an older node decodes into
		Enabled       bool `json:"enabled"`
		IdleMinutes   int  `json:"idle_minutes"`
		ContextTokens int  `json:"context_tokens"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || !old.Enabled || old.IdleMinutes != 30 || old.ContextTokens != 300000 {
		t.Fatalf("an older node reads %s as %+v (%v)", raw, old, err)
	}
}

func TestIdleReleaseOfKeepsOnlyValuesInRange(t *testing.T) {
	for _, c := range []struct {
		minutes, tokens int
		want            assistantRelease
	}{
		{0, 0, assistantRelease{}},
		{4, 9999, assistantRelease{}},
		{56, 10000001, assistantRelease{}},
		{-1, -1, assistantRelease{}},
		{5, 10000, assistantRelease{after: 5 * time.Minute, compactAt: 10000}},
		{55, 10000000, assistantRelease{after: 55 * time.Minute, compactAt: 10000000}},
		{45, 0, assistantRelease{after: 45 * time.Minute}},
		{0, 200000, assistantRelease{compactAt: 200000}},
	} {
		got := releaseOf(assistantwire.IdleCompaction{ReleaseMinutes: c.minutes, ReleaseTokens: c.tokens})
		if got != c.want {
			t.Fatalf("releaseOf(%d, %d) = %+v, want %+v", c.minutes, c.tokens, got, c.want)
		}
	}
}

// reapable and idleCompactable on their own: no release policy keeps 20 minutes and the window's threshold; a policy
// moves the time and replaces the threshold, above or below the window's.
func TestIdleReleaseTimeAndThreshold(t *testing.T) {
	now := time.Now()
	p := &assistantProc{agent: "claude", lastActive: now, compactIdleAt: 750000, context: 250000}
	if p.reapable(now.Add(assistantIdleTimeout-time.Minute)) || !p.reapable(now.Add(assistantIdleTimeout+time.Minute)) {
		t.Fatal("without a release policy an idle process must still go after 20 minutes")
	}
	if p.idleCompactable() {
		t.Fatal("without a release policy the threshold must stay the window's (750000)")
	}
	p.release = assistantRelease{after: 45 * time.Minute, compactAt: 200000}
	if p.reapable(now.Add(44*time.Minute)) || !p.reapable(now.Add(46*time.Minute)) {
		t.Fatal("a 45-minute release policy was not followed")
	}
	if !p.idleCompactable() {
		t.Fatal("a 200000-token release threshold must compact a 250000-token context under a 1M window")
	}
	p.context = 199999
	if p.idleCompactable() {
		t.Fatal("a context below the release threshold was compacted")
	}
	// Above the window's threshold: the absolute number stands, the window does not pull it down.
	p.compactIdleAt, p.context, p.release.compactAt = 300000, 400000, 800000
	if p.idleCompactable() {
		t.Fatal("an 800000-token release threshold compacted a 400000-token context")
	}
	// What never compacts still doesn't: a supervision round, a process compacted once, one compacting now.
	p.release.compactAt = 200000
	for _, c := range []func(*assistantProc){
		func(p *assistantProc) { p.round = true },
		func(p *assistantProc) { p.compacted = true },
		func(p *assistantProc) { p.compacting = true },
		func(p *assistantProc) { p.agent = "codex" },
	} {
		q := &assistantProc{agent: "claude", context: 400000, release: p.release}
		c(q)
		if q.idleCompactable() {
			t.Fatalf("release threshold compacted an excluded process: %+v", q)
		}
	}
	// Background tasks still keep a process past the release time.
	q := &assistantProc{agent: "claude", lastActive: now, tasks: []string{"tail -f"}, release: assistantRelease{after: 5 * time.Minute}}
	if q.reapable(now.Add(time.Hour)) {
		t.Fatal("a release policy let a process with background tasks go")
	}
}

// The policy arrives with the main conversation's messages, is kept on its record, and the reaper reads it from
// there; a side conversation's record never applies one.
func TestIdleReleasePolicyComesFromTheMainRecord(t *testing.T) {
	n, p, _ := idleCompactFixture(t)
	if got := n.assistantReleases([]string{p.id}); len(got) != 0 {
		t.Fatalf("a policy without release fields gave %+v", got)
	}
	policy := idleCompactPolicy
	policy.Enabled = false // no compaction before this message: only the policy is under test
	policy.ReleaseMinutes, policy.ReleaseTokens = 45, 200000
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := idleCompactCall(ctx, n, p, policy, nil); err != nil {
		t.Fatal(err)
	}
	want := assistantRelease{after: 45 * time.Minute, compactAt: 200000}
	if got := n.assistantReleases([]string{p.id, "unknown"}); len(got) != 1 || got[p.id] != want {
		t.Fatalf("release policy from the record: %+v, want %+v", got, want)
	}
	n.state.update(p.id, func(st *sessionState) bool { st.IsMain = false; return true })
	if got := n.assistantReleases([]string{p.id}); len(got) != 0 {
		t.Fatalf("a side conversation's record applied a release policy: %+v", got)
	}
}

// One sweep of the reaper, as reapAssistants runs it once a minute, with the policy on the main conversation's record.
func TestIdleReleaseSweep(t *testing.T) {
	for _, c := range []struct {
		name          string
		main          bool
		minutes       int
		tokens        int
		idle          time.Duration
		window        int
		context       int
		wantCompacted bool
		wantKept      bool
	}{
		{name: "nothing saved: let go after 20 minutes below 75% of the window", main: true, idle: 21 * time.Minute, window: 750000, context: 250000},
		{name: "nothing saved: compacted at 75% of the window", main: true, idle: 21 * time.Minute, window: 300000, context: 300000, wantCompacted: true, wantKept: true},
		{name: "release minutes keep it past 20 minutes", main: true, minutes: 45, idle: 21 * time.Minute, window: 750000, context: 250000, wantKept: true},
		{name: "release minutes let it go once past", main: true, minutes: 45, idle: 46 * time.Minute, window: 750000, context: 250000},
		{name: "release tokens compact below the window's threshold", main: true, tokens: 200000, idle: 21 * time.Minute, window: 750000, context: 250000, wantCompacted: true, wantKept: true},
		{name: "release tokens above the window's threshold", main: true, tokens: 800000, idle: 21 * time.Minute, window: 300000, context: 400000},
		{name: "both, before the time", main: true, minutes: 45, tokens: 200000, idle: 30 * time.Minute, window: 750000, context: 250000, wantKept: true},
		{name: "both, after the time", main: true, minutes: 45, tokens: 200000, idle: 46 * time.Minute, window: 750000, context: 250000, wantCompacted: true, wantKept: true},
		{name: "a side conversation keeps the node's rule", minutes: 45, tokens: 200000, idle: 21 * time.Minute, window: 750000, context: 250000},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, p, sink := idleCompactFixture(t)
			policy := idleCompactPolicy
			policy.ReleaseMinutes, policy.ReleaseTokens = c.minutes, c.tokens
			n.state.update(p.id, func(st *sessionState) bool {
				st.IsMain = c.main
				a := *st.IdleCompaction
				a.Policy = policy
				st.IdleCompaction = &a
				return true
			})
			p.mu.Lock()
			p.lastActive = time.Now().Add(-c.idle)
			p.compactIdleAt, p.context = c.window, c.context
			p.mu.Unlock()
			n.sweepAssistants()
			compacted := strings.Contains(sink.String(), `"/compact"`)
			n.mu.Lock()
			kept := n.procs[p.id] == p
			n.mu.Unlock()
			if compacted != c.wantCompacted || kept != c.wantKept {
				t.Fatalf("compacted %v kept %v, want compacted %v kept %v", compacted, kept, c.wantCompacted, c.wantKept)
			}
		})
	}
}
