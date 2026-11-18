package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

var idleCompactPolicy = assistantwire.IdleCompaction{Enabled: true, IdleMinutes: 30, ContextTokens: 300000}

type idleCompactSink struct {
	syncBuffer
	compact chan struct{}
}

func (s *idleCompactSink) Close() error { return nil }

func (s *idleCompactSink) Write(line []byte) (int, error) {
	n, err := s.syncBuffer.Write(line)
	var v struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &v) == nil && v.Type == "user" && string(v.Message.Content) == `"/compact"` {
		select {
		case s.compact <- struct{}{}:
		default:
		}
	}
	return n, err
}

func idleCompactFixture(t *testing.T) (*Node, *assistantProc, *idleCompactSink) {
	t.Helper()
	n := codexTestNode(t)
	sink := &idleCompactSink{compact: make(chan struct{}, 10)}
	p := viewErrorsClaude(t, n, sink)
	p.model.IsMain = true
	p.fingerprint = n.executionFingerprint("claude", modelExecution{IsMain: true})
	p.context = idleCompactPolicy.ContextTokens
	p.stdin = &ackControlWriter{WriteCloser: sink, p: p}
	n.state.update(p.id, func(st *sessionState) bool {
		st.IsMain = true
		st.IdleCompaction = &idleCompactionState{
			Policy: idleCompactPolicy, Since: time.Now().Add(-time.Hour), Tokens: p.context,
		}
		return true
	})
	return n, p, sink
}

func idleCompactCall(ctx context.Context, n *Node, p *assistantProc, policy assistantwire.IdleCompaction, source *proto.MessageSource) error {
	params, _ := json.Marshal(map[string]any{
		"session_id": p.id, "text": "background wake", "is_main": true,
		"idle_compaction": policy, "message_source": source,
	})
	_, err := n.handle(ctx, "assistant.send", params)
	return err
}

func idleCompactStream(n *Node, p *assistantProc, lines ...string) {
	n.readAssistant(p, strings.NewReader(strings.Join(lines, "\n")+"\n"))
}

func idleCompactAwait(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unattended delivery did not finish")
	}
}

// This exercises assistant.send all the way to Claude's stdin, not just the
// policy helper: removing the send hook makes it fail waiting for /compact.
func TestIdleCompactionBeforeUnattendedDelivery(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		policy assistantwire.IdleCompaction
	}{
		{proto.SourceSupervision, idleCompactPolicy},
		{proto.SourceFollowup, idleCompactPolicy},
		{proto.SourcePatrol, idleCompactPolicy},
		{proto.SourceConversation, idleCompactPolicy},
		{"custom limits", assistantwire.IdleCompaction{Enabled: true, IdleMinutes: 5, ContextTokens: 25000}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			n, p, sink := idleCompactFixture(t)
			p.context = tc.policy.ContextTokens
			n.state.update(p.id, func(st *sessionState) bool {
				a := *st.IdleCompaction
				a.Policy, a.Tokens = tc.policy, p.context
				st.IdleCompaction = &a
				return true
			})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- idleCompactCall(ctx, n, p, tc.policy, &proto.MessageSource{Kind: tc.kind}) }()
			select {
			case <-sink.compact:
			case <-time.After(3 * time.Second):
				t.Fatal("eligible unattended delivery did not send /compact")
			}
			if strings.Contains(sink.String(), "background wake") {
				t.Fatal("original message was written before compaction")
			}
			idleCompactStream(n, p, `{"type":"system","subtype":"compact_boundary","compact_metadata":{"post_tokens":90000}}`)
			select {
			case err := <-done:
				t.Fatalf("boundary alone released delivery before the command ended: %v", err)
			default:
			}
			idleCompactStream(n, p, `{"type":"result","subtype":"success"}`)
			idleCompactAwait(t, done)
			if got := sink.String(); strings.Count(got, `"/compact"`) != 1 || !strings.Contains(got, "background wake") {
				t.Fatalf("expected one compaction followed by the original message: %s", got)
			}
			st, _ := n.state.get(p.id)
			if st.Compaction == nil || st.Compaction.State != proto.CompactionDone || st.IdleCompaction.Tokens != 90000 {
				t.Fatalf("compaction result not recorded: %+v", st)
			}
		})
	}
}

func TestIdleCompactionIneligibleDelivery(t *testing.T) {
	for _, name := range []string{"recent user", "small context", "unknown context", "disabled", "user message", "already attempted", "active turn"} {
		t.Run(name, func(t *testing.T) {
			n, p, sink := idleCompactFixture(t)
			policy := idleCompactPolicy
			source := &proto.MessageSource{Kind: proto.SourceSupervision}
			n.state.update(p.id, func(st *sessionState) bool {
				a := *st.IdleCompaction
				switch name {
				case "recent user":
					a.Since = time.Now().Add(-29 * time.Minute)
				case "small context":
					p.context, a.Tokens = 299999, 299999
				case "unknown context":
					p.context, a.Tokens = 0, 0
				case "disabled":
					policy.Enabled = false
					a.Policy = policy
				case "user message":
					source = nil
				case "already attempted":
					a.Attempted = true
				case "active turn":
					p.busy = true
				}
				st.IdleCompaction = &a
				return true
			})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := idleCompactCall(ctx, n, p, policy, source); err != nil {
				t.Fatal(err)
			}
			if got := sink.String(); strings.Contains(got, `"/compact"`) || !strings.Contains(got, "background wake") {
				t.Fatalf("ineligible message must go through without compaction: %s", got)
			}
		})
	}
}

func TestIdleCompactionLatchSurvivesRestartAndRearmsAfterDrop(t *testing.T) {
	n, p, sink := idleCompactFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	source := &proto.MessageSource{Kind: proto.SourceSupervision}
	done := make(chan error, 1)
	go func() { done <- idleCompactCall(ctx, n, p, idleCompactPolicy, source) }()
	select {
	case <-sink.compact:
	case <-time.After(3 * time.Second):
		t.Fatal("first unattended message did not compact")
	}
	// A failed attempt must not spin, even if context is still large and a new
	// node has to recover the session's state from the hub.
	idleCompactStream(n, p, `{"type":"result","subtype":"success","result":"Not enough messages to compact."}`)
	idleCompactAwait(t, done)
	idleCompactStream(n, p, `{"type":"result","subtype":"success"}`)
	st, _ := n.state.get(p.id)
	raw, _ := json.Marshal(st)
	var restored sessionState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	beforeRestart := sink.String()
	n, p, sink = idleCompactFixture(t)
	restored.Cwd = p.cwd
	n.state.update(p.id, func(st *sessionState) bool { *st = restored; return true })
	p.context = 0 // a resumed process has no current usage yet
	if err := idleCompactCall(ctx, n, p, idleCompactPolicy, source); err != nil {
		t.Fatal(err)
	}
	if strings.Count(beforeRestart+sink.String(), `"/compact"`) != 1 {
		t.Fatal("the same idle interval compacted twice after a restart")
	}

	// A smaller observed context resets the idle clock; cumulative modelUsage
	// cannot replace this request's actual 90K context with a large total.
	idleCompactStream(n, p,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":80000,"cache_creation_input_tokens":9000}}}}`,
		`{"type":"result","subtype":"success","modelUsage":{"test-model":{"inputTokens":9000000}}}`)
	st, _ = n.state.get(p.id)
	if st.IdleCompaction.Tokens != 90000 || st.IdleCompaction.Attempted || time.Since(st.IdleCompaction.Since) > time.Minute {
		t.Fatalf("small actual context did not rearm from now: %+v", st.IdleCompaction)
	}
	// Growing over the threshold again is not enough without another idle gap.
	p.context = 300001
	if err := idleCompactCall(ctx, n, p, idleCompactPolicy, source); err != nil {
		t.Fatal(err)
	}
	if strings.Count(beforeRestart+sink.String(), `"/compact"`) != 1 {
		t.Fatal("context regrowth compacted before the new idle interval elapsed")
	}
	idleCompactStream(n, p, `{"type":"result","subtype":"success"}`)
	n.state.update(p.id, func(st *sessionState) bool {
		a := *st.IdleCompaction
		a.Since = time.Now().Add(-31 * time.Minute)
		st.IdleCompaction = &a
		return true
	})
	go func() { done <- idleCompactCall(ctx, n, p, idleCompactPolicy, source) }()
	select {
	case <-sink.compact:
	case <-time.After(3 * time.Second):
		t.Fatal("threshold recrossing and another idle gap did not compact")
	}
	idleCompactStream(n, p,
		`{"type":"system","subtype":"compact_boundary","compact_metadata":{"post_tokens":90000}}`,
		`{"type":"result","subtype":"success"}`)
	idleCompactAwait(t, done)
}

func TestIdleCompactionUserReceiptResetsBeforeSendLock(t *testing.T) {
	n, p, _ := idleCompactFixture(t)
	unlock := n.typing.lock(p.id)
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	before := time.Now()
	go func() { done <- idleCompactCall(ctx, n, p, idleCompactPolicy, nil) }()
	waitFor(t, "user receipt while typing is locked", func() bool {
		st, _ := n.state.get(p.id)
		return !st.IdleCompaction.Since.Before(before)
	})
	unlock()
	unlock = nil
	idleCompactAwait(t, done)
}

func TestIdleCompactionCancelledDeliveryCanRetryWithoutRecompacting(t *testing.T) {
	n, p, sink := idleCompactFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := &proto.MessageSource{Kind: proto.SourceSupervision}
	done := make(chan error, 1)
	go func() { done <- idleCompactCall(ctx, n, p, idleCompactPolicy, source) }()
	select {
	case <-sink.compact:
	case <-time.After(3 * time.Second):
		t.Fatal("eligible delivery did not compact")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled wait: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled wait held the send lock")
	}
	if strings.Contains(sink.String(), "background wake") {
		t.Fatal("a cancelled wait delivered the original message")
	}
	idleCompactStream(n, p,
		`{"type":"system","subtype":"compact_boundary","compact_metadata":{"post_tokens":90000}}`,
		`{"type":"result","subtype":"success"}`)
	if err := idleCompactCall(t.Context(), n, p, idleCompactPolicy, source); err != nil {
		t.Fatal(err)
	}
	if got := sink.String(); strings.Count(got, `"/compact"`) != 1 || strings.Count(got, "background wake") != 1 {
		t.Fatalf("retry did not deliver exactly once without another compaction: %s", got)
	}
}

func TestIdleCompactionLeavesOtherAgentsAndSideChatsAlone(t *testing.T) {
	for _, name := range []string{"codex", "side chat", "older manager"} {
		t.Run(name, func(t *testing.T) {
			n, p, sink := idleCompactFixture(t)
			ctx := context.WithValue(t.Context(), idleCompactionInputKey{}, idleCompactionInput{
				At: time.Now(), Policy: &idleCompactPolicy, Source: &proto.MessageSource{Kind: proto.SourceSupervision},
			})
			switch name {
			case "codex":
				p.agent = "codex"
			case "side chat":
				n.state.update(p.id, func(st *sessionState) bool { st.IsMain = false; return true })
			case "older manager":
				ctx = t.Context()
			}
			if err := n.idleCompactBeforeInput(ctx, p); err != nil || sink.String() != "" {
				t.Fatalf("unaffected path attempted compaction: %v %s", err, sink.String())
			}
		})
	}
}

func TestIdleCompactionTaskNoticeIsNotUserActivity(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "notice", true: "user pasted notice"}[protected], func(t *testing.T) {
			n, p, sink := idleCompactFixture(t)
			text := prompts.RenderTaskNotice(prompts.TaskNotice{Title: "Example task", State: "done", Text: "Ready for review."})
			if protected {
				text = prompts.ProtectUserMessage(text)
			}
			raw, _ := json.Marshal(map[string]any{
				"session_id": p.id, "text": text, "is_main": true, "idle_compaction": idleCompactPolicy,
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := n.handle(ctx, "assistant.send", raw); done <- err }()
			if protected {
				idleCompactAwait(t, done)
				if strings.Contains(sink.String(), `"/compact"`) {
					t.Fatal("the user's pasted notice was treated as background activity")
				}
				return
			}
			select {
			case <-sink.compact:
			case <-time.After(3 * time.Second):
				t.Fatal("a task notice incorrectly reset the user-idle clock")
			}
			idleCompactStream(n, p,
				`{"type":"system","subtype":"compact_boundary","compact_metadata":{"post_tokens":90000}}`,
				`{"type":"result","subtype":"success"}`)
			idleCompactAwait(t, done)
		})
	}
}

func TestIdleCompactionOlderInputCannotEraseNewerUserActivity(t *testing.T) {
	n, p, _ := idleCompactFixture(t)
	old := idleCompactionInput{
		At: time.Now().Add(-time.Hour), Policy: &idleCompactPolicy,
		Source: &proto.MessageSource{Kind: proto.SourceSupervision},
	}
	current := idleCompactPolicy
	current.IdleMinutes = 60
	now := time.Now()
	n.noteIdleCompactionInput(p.id, idleCompactionInput{At: now, Policy: &current})
	n.noteIdleCompactionInput(p.id, old)
	st, _ := n.state.get(p.id)
	if st.IdleCompaction.Policy != current || !st.IdleCompaction.Since.Equal(now) {
		t.Fatalf("older background preparation overwrote the newer user receipt: %+v", st.IdleCompaction)
	}
}

func TestIdleCompactionContextReductionRestartsClock(t *testing.T) {
	n, p, _ := idleCompactFixture(t)
	n.state.update(p.id, func(st *sessionState) bool {
		a := *st.IdleCompaction
		a.Tokens, a.Attempted = 450000, true
		st.IdleCompaction = &a
		return true
	})
	before := time.Now()
	n.recordIdleCompactionContext(p.id, 400000)
	st, _ := n.state.get(p.id)
	if st.IdleCompaction.Since.Before(before) || !st.IdleCompaction.Attempted {
		t.Fatalf("a reduction should restart the clock but not rearm above the threshold: %+v", st.IdleCompaction)
	}
}
