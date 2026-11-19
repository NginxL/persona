package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A send asking for compaction (#11137) compacts only a nearly full Agent Persona can compact; mid-turn it holds the
// message for after the turn; anything else goes exactly as a plain send.
func TestPlanCompaction(t *testing.T) {
	for _, c := range []struct {
		asked, compactable, busy, full bool
		want                           compactPlan
	}{
		{false, true, false, true, compactNo}, // not asked: as before
		{true, true, false, false, compactNo}, // not nearly full: as before
		{true, true, true, false, compactNo},  // mid-turn, not nearly full: into the runtime's queue as before
		{true, false, false, true, compactNo}, // a runtime Persona cannot compact: as before
		{true, true, false, true, compactThenSend},
		{true, true, true, true, compactHold},
	} {
		if got := planCompaction(c.asked, c.compactable, c.busy, c.full); got != c.want {
			t.Errorf("planCompaction(%v, %v, %v, %v) = %v, want %v", c.asked, c.compactable, c.busy, c.full, got, c.want)
		}
	}
	if !proto.ContextNearlyFull(428111, 475000) || proto.ContextNearlyFull(300000, 475000) || proto.ContextNearlyFull(428111, 0) ||
		proto.ContextNearlyFull(0, 475000) {
		t.Fatal("ContextNearlyFull")
	}
}

// scripted is a probe answering reads in order, the last one for good.
func scripted(reads ...compactProgress) func() compactProgress {
	i := 0
	return func() compactProgress {
		p := reads[min(i, len(reads)-1)]
		i++
		return p
	}
}

// waitCompaction goes by the runtime's own signals: done once the record is there and the Agent rests; failed, with
// why, when it rests again without one, never begins, stops, or runs past the deadline.
func TestWaitCompaction(t *testing.T) {
	const poll, grace, deadline = time.Millisecond, 50 * time.Millisecond, 2 * time.Second
	busy := compactProgress{busy: true, live: true}
	rest := compactProgress{live: true}
	done := compactProgress{compacted: true, live: true}
	for _, c := range []struct {
		name  string
		reads []compactProgress
		want  string // "" for success
	}{
		{"compacted", []compactProgress{rest, busy, busy, {compacted: true, busy: true, live: true}, done}, ""},
		{"record as it rests", []compactProgress{busy, done}, ""},
		{"rests without a record", []compactProgress{busy, rest}, "finished /compact without compacting"},
		{"its error", []compactProgress{busy, {live: true, reason: "Error during compaction: prompt is too long"}},
			"without compacting: Error during compaction: prompt is too long"},
		{"never begins", []compactProgress{rest}, "did not start compacting within"},
		// A Codex whose status lags its rollout: written to, never seen at work, then its record.
		{"codex written to, then its record", []compactProgress{rest, {grew: true, live: true}, {grew: true, live: true}, {compacted: true, grew: true, live: true}}, ""},
		{"refused at once", []compactProgress{{live: true, reason: "Error during compaction: Not enough messages to compact"}},
			"did not compact: Error during compaction: Not enough messages"},
		{"stops", []compactProgress{busy, {}}, "stopped while compacting"},
		{"no record in time", []compactProgress{busy}, "no compaction record within"},
		{"written to, no record in time", []compactProgress{{grew: true, live: true}}, "no compaction record within"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := deadline
			if strings.Contains(c.name, "no record in time") {
				d = 30 * time.Millisecond
			}
			if strings.HasPrefix(c.name, "written to") {
				d = 3 * grace // past the grace: a transcript written to keeps it waiting for its record
			}
			err := waitCompaction(context.Background(), scripted(c.reads...), poll, grace, d)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("failed: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

// compactionSince finds the compaction a /compact wrote after the offset (Claude Code's compact_boundary, a Codex
// rollout's compacted line or context_compacted event), not one from before it, and picks up the error of a failed
// one.
func TestCompactionSince(t *testing.T) {
	dir := t.TempDir()
	write := func(name, before, after string) (string, int64) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(before+after), 0o600); err != nil {
			t.Fatal(err)
		}
		return p, int64(len(before))
	}
	old := `{"type":"system","subtype":"compact_boundary","content":"Conversation compacted"}` + "\n"
	user := `{"type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>"}}` + "\n"
	for _, c := range []struct {
		name, before, after string
		compacted           bool
		reason              string
	}{
		{"claude", old, user + `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":428111}}` + "\n", true, ""},
		{"codex item", "", `{"timestamp":"2026-11-19T10:00:00Z","type":"compacted","payload":{"message":""}}` + "\n", true, ""},
		{"codex event", "", `{"timestamp":"2026-11-19T10:00:00Z","type":"event_msg","payload":{"type":"context_compacted"}}` + "\n", true, ""},
		{"one from before only", old, user, false, ""},
		{"failed", old, user + `{"type":"user","message":{"content":"<local-command-stderr>Error during compaction: Error: prompt is too long</local-command-stderr>"}}` + "\n",
			false, "Error during compaction: Error: prompt is too long</local-command-stderr>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, off := write(strings.ReplaceAll(c.name, " ", "-")+".jsonl", c.before, c.after)
			compacted, grew, reason := compactionSince(p, off)
			if !grew {
				t.Fatal("nothing read as written after the offset")
			}
			if compacted != c.compacted || !strings.HasPrefix(reason, strings.SplitN(c.reason, "<", 2)[0]) || (c.reason == "") != (reason == "") {
				t.Fatalf("compactionSince = %v, %q", compacted, reason)
			}
		})
	}
	if compacted, grew, reason := compactionSince(filepath.Join(dir, "none"), 0); compacted || grew || reason != "" {
		t.Fatal("a missing transcript")
	}
	p, off := write("quiet.jsonl", old, "")
	if compacted, grew, _ := compactionSince(p, off); compacted || grew {
		t.Fatal("nothing written after the offset")
	}
}

// A reply after the /compact that sees far less than before counts as the compaction (Codex writes its token_count on
// the next turn); a small drop does not.
func TestContextShrank(t *testing.T) {
	if !contextShrank(428111, 60000) || contextShrank(428111, 400000) || contextShrank(0, 1000) || contextShrank(428111, 0) {
		t.Fatal("contextShrank")
	}
}
