package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// When the background supervisor is replaced (#7315, supervise_round.go backgroundOutgrown).

// drainingRound has the relay book say a queued message is going into conversation id (drainRelays marks it before
// deliverRelay sends, while the session list may still say Idle), or no longer.
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

// A background supervisor that is to be replaced is not while a message is going into it, though listed Idle: the
// round going in would otherwise run in an archived conversation that is no longer the background supervisor, and
// its task_review, task_scope and agent_send would be refused. Once that is over, it is replaced.
func TestBackgroundNotReplacedWhileAMessageGoesIn(t *testing.T) {
	s, f, _, _ := newRoundService(t)
	ctx := context.Background()
	bg := withBackgroundBG1(t, s, f)
	s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "no_anchor"})

	drainingRound(s, bg, true)
	if id, gone := s.ensureBackground(ctx); id != bg || gone != "" {
		t.Fatalf("replaced while a message goes in: %q %q", id, gone)
	}
	if s.isArchived(bg) || len(conversationStarts(f)) != 0 || s.settings(ctx).BackgroundSupervisor != bg {
		t.Fatalf("put away while a message goes in: archived %v, %d starts", s.isArchived(bg), len(conversationStarts(f)))
	}
	if why := outgrownOf(s, bg); why != "no_anchor" {
		t.Fatalf("forgot why it is to be replaced: %q", why)
	}

	drainingRound(s, bg, false)
	if id, gone := s.ensureBackground(ctx); id == bg || id == "" || gone != bg || !s.isArchived(bg) {
		t.Fatalf("not replaced once the message went in: %q %q, archived %v", id, gone, s.isArchived(bg))
	}
}

// The background supervisor is reserved for its replacement in one step with the look that nothing goes into it. A
// conversation writing to it while the replacement starts finds it waiting, so its message queues, and the drain that
// follows (or a session list the node pushes meanwhile) leaves it there: it moves to the replacement with the rest,
// and nothing goes into the old one, which is archived and no longer the background supervisor (a round there would
// have its task_review refused). A replacement that does not start lets the old one take what waits for it.
func TestNothingGoesIntoABackgroundSupervisorBeingReplaced(t *testing.T) {
	for _, starts := range []bool{true, false} {
		name := "the replacement starts"
		if !starts {
			name = "the replacement does not start"
		}
		t.Run(name, func(t *testing.T) {
			s, f, _, _ := newRoundService(t)
			ctx := context.Background()
			bg := withBackgroundBG1(t, s, f)
			s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "no_anchor"})
			const words = "顺便看看 while it is replaced"
			msg := relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: words, At: time.Now().UTC()}
			idle := []proto.Session{{SessionID: bg, MachineID: "server", Assistant: true, Status: proto.StatusIdle}}
			waited, drained := false, false
			f.mu.Lock()
			f.onAssistantSend = func(f *fakeHub, a map[string]any) {
				if id, _ := a["session_id"].(string); id != "" {
					f.failAssistant = nil
					return
				}
				// The replacement starts: a conversation writes to the old one (conversation_send queues what has to
				// wait, then drains), as the node pushes a list with it idle.
				if waited = s.relayWaits(bg, msg); waited {
					s.queueRelay(bg, msg)
				}
				s.drainRelays("server", idle)
				s.relays.mu.Lock()
				drained = s.relays.draining[bg]
				s.relays.mu.Unlock()
				if !starts {
					f.failAssistant = errors.New("the launcher cannot start")
				}
			}
			f.mu.Unlock()

			id, gone := s.ensureBackground(ctx)
			if !waited || drained {
				t.Fatalf("while it was replaced: the message waited %v, a drain went into it %v", waited, drained)
			}
			if starts {
				if id == bg || gone != bg {
					t.Fatalf("not replaced: %q %q", id, gone)
				}
				b := &s.supervise
				b.dedupMu.Lock()
				s.backgroundLost(ctx, gone, id, s.webBase(ctx)) // as the digest does next
				b.dedupMu.Unlock()
				if s.queuedFor(bg) != 0 || s.queuedFor(id) != 1 {
					t.Fatalf("queued: old %d, new %d", s.queuedFor(bg), s.queuedFor(id))
				}
				if got := f.noticesTo(bg); len(got) != 0 {
					t.Fatalf("the old one got %q", got)
				}
			} else {
				if id != bg || gone != "" {
					t.Fatalf("replaced though the replacement did not start: %q %q", id, gone)
				}
				waitFor(t, "the message in the old one", func() bool {
					return slices.ContainsFunc(f.noticesTo(bg), func(n string) bool { return strings.Contains(n, words) })
				})
			}
			s.relays.mu.Lock()
			reserved := s.relays.replacing[bg]
			s.relays.mu.Unlock()
			if reserved {
				t.Fatal("still reserved for a replacement")
			}
		})
	}
}

// The size limit counts only for a round that restarted the process (fresh, or start_failed resumed as before):
// one that keeps its context for good (unsupported: an older Claude Code, another runtime) or was busy is neither
// restarted per round nor spared idle compaction, so a large transcript is no reason to throw its context away.
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
			t.Fatalf("%+v: to be replaced (%s)", res, why)
		}
	}
	if id, gone := s.ensureBackground(ctx); id != bg || gone != "" || len(conversationStarts(f)) != 0 {
		t.Fatalf("replaced: %q %q", id, gone)
	}
	s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "start_failed", TranscriptBytes: big})
	if why := outgrownOf(s, bg); why != "oversized" {
		t.Fatalf("a round started again as before, over the limit: %q", why)
	}
}
