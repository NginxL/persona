package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A legacy background conversation whose rounds can no longer start clean moves its unread work to main (#5732, #7315).
// It is never archived and no replacement conversation is created.

// drainingRound has the relay book say a queued message is going into conversation id, or no longer.
func drainingRound(s *Service, id string, going bool) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	s.relayLocked(context.Background())
	if going {
		b.draining[id] = true
	} else {
		delete(b.draining, id)
	}
}

// An outgrown legacy background is not retired while a message is going into it. Once that drain is over, its unread
// work can move to main without archiving the conversation or starting another one.
func TestBackgroundNotRetiredWhileAMessageGoesIn(t *testing.T) {
	s, f, _, _ := newRoundService(t)
	ctx := context.Background()
	bg := withBackgroundBG1(t, s, f)
	s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "no_anchor"})

	drainingRound(s, bg, true)
	if id, gone := s.ensureBackground(ctx); id != bg || gone != "" {
		t.Fatalf("retired while a message goes in: %q %q", id, gone)
	}
	if s.isArchived(bg) || len(conversationStarts(f)) != 0 || s.settings(ctx).BackgroundSupervisor != bg {
		t.Fatalf("put away while a message goes in: archived %v, %d starts", s.isArchived(bg), len(conversationStarts(f)))
	}
	if why := outgrownOf(s, bg); why != "no_anchor" {
		t.Fatalf("forgot why it cannot continue: %q", why)
	}

	drainingRound(s, bg, false)
	if id, gone := s.ensureBackground(ctx); id != "" || gone != bg {
		t.Fatalf("not retired once the message went in: %q %q", id, gone)
	}
	b := &s.supervise
	b.dedupMu.Lock()
	s.backgroundLost(ctx, bg, "conv1", s.webBase(ctx))
	b.dedupMu.Unlock()
	if s.isArchived(bg) || len(conversationStarts(f)) != 0 {
		t.Fatalf("retirement archived or replaced the legacy conversation: archived %v, %d starts", s.isArchived(bg), len(conversationStarts(f)))
	}
	s.relays.mu.Lock()
	reserved := s.relays.replacing[bg]
	s.relays.mu.Unlock()
	if reserved {
		t.Fatal("the legacy conversation is still reserved after its work moved")
	}
}

// Reserving the legacy conversation and checking that it is idle are one step. A message racing the move queues instead
// of entering the old conversation, then moves to main with the rest of its unread work.
func TestNothingGoesIntoALegacyBackgroundBeingRetired(t *testing.T) {
	s, f, _, _ := newRoundService(t)
	ctx := context.Background()
	bg := withBackgroundBG1(t, s, f)
	s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "no_anchor"})
	const words = "顺便看看 while the legacy work moves"
	msg := relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: words, At: time.Now().UTC()}
	if s.queueRelay(bg, msg) == 0 {
		t.Fatal("could not queue the legacy message")
	}
	if id, gone := s.ensureBackground(ctx); id != "" || gone != bg || !s.relayWaits(bg, msg) {
		t.Fatalf("the retiring conversation still accepts a message: id %q, gone %q", id, gone)
	}
	b := &s.supervise
	b.dedupMu.Lock()
	s.backgroundLost(ctx, bg, "conv1", s.webBase(ctx))
	b.dedupMu.Unlock()
	waitFor(t, "the moved message in main", func() bool {
		return slices.ContainsFunc(f.noticesTo("conv1"), func(n string) bool { return strings.Contains(n, words) })
	})
	if got := f.noticesTo(bg); len(got) != 0 {
		t.Fatalf("the retired conversation got %q", got)
	}
	if s.queuedFor(bg) != 0 || s.isArchived(bg) || len(conversationStarts(f)) != 0 {
		t.Fatalf("after the move: old queue %d, archived %v, starts %d", s.queuedFor(bg), s.isArchived(bg), len(conversationStarts(f)))
	}
}

// A digest containing only new main-supervised work still reconciles an outgrown legacy queue. A healthy legacy
// conversation keeps the same queue: the reconciliation must not reroute work merely because a background exists.
func TestMainOnlyDigestReconcilesLegacyQueue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outgrown bool
	}{
		{"outgrown moves to main", true},
		{"healthy stays queued", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, _ := newRoundService(t)
			ctx := context.Background()
			bg := withBackgroundBG1(t, s, f)
			if tc.outgrown {
				s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "no_anchor"})
			}
			listRound(f, bg, proto.StatusWorking, false)
			wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "legacy-waiting", Minutes: 50})
			if got := s.queuedFor(bg); got != 1 {
				t.Fatalf("the legacy notice did not queue: %d", got)
			}

			listRound(f, bg, proto.StatusIdle, false)
			g := superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "main-only", At: time.Now().Add(-time.Minute)}
			b := &s.supervise
			b.mu.Lock()
			b.init()
			b.pending[g.id()] = g // no explicit legacy target: this is new work routed to main
			b.last = time.Time{}
			b.mu.Unlock()
			s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[g.id()] = g })
			s.sendDigest(ctx)

			waitFor(t, "the main-only signal", func() bool {
				return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), "被模型服务出错打断了")
			})
			got := strings.Join(f.noticesTo("conv1"), "\n")
			moved := strings.Contains(got, "50 分钟没有进展")
			if moved != tc.outgrown {
				t.Fatalf("legacy work moved = %v, want %v; main notices:\n%s", moved, tc.outgrown, got)
			}
			wantQueued := 1
			if tc.outgrown {
				wantQueued = 0
			}
			if queued := s.queuedFor(bg); queued != wantQueued {
				t.Fatalf("legacy queue = %d, want %d", queued, wantQueued)
			}
			if len(conversationStarts(f)) != 0 || s.isArchived(bg) {
				t.Fatalf("reconciliation archived or replaced the old conversation: %d starts, archived %v", len(conversationStarts(f)), s.isArchived(bg))
			}
		})
	}
}

// The size limit counts only for a round that restarted the process (fresh, or start_failed resumed as before): one
// that keeps its context for good or was busy is neither restarted per round nor spared idle compaction.
func TestBackgroundOversizedOnlyWhereRoundsRestart(t *testing.T) {
	s, f, _, _ := newRoundService(t)
	ctx := context.Background()
	bg := withBackgroundBG1(t, s, f)
	big := backgroundTranscriptLimit + 1
	for _, res := range []superviseRoundReply{
		{Context: "kept", Why: "unsupported", TranscriptBytes: big},
		{Context: "kept", Why: "busy", TranscriptBytes: big},
	} {
		s.superviseRoundAnswered(bg, res)
		if why := outgrownOf(s, bg); why != "" {
			t.Fatalf("%+v: cannot continue (%s)", res, why)
		}
	}
	if id, gone := s.ensureBackground(ctx); id != bg || gone != "" || len(conversationStarts(f)) != 0 {
		t.Fatalf("retired: %q %q", id, gone)
	}
	s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "start_failed", TranscriptBytes: big})
	if why := outgrownOf(s, bg); why != "oversized" {
		t.Fatalf("a round started again as before, over the limit: %q", why)
	}
}
