package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// One classification of a failed relay send (#11393): every code the issue names, the ones the node and the
// manager's devices layer were found to answer on assistant.send as well, the bus's shapes of the hub's codes, the
// manager's own errors and an unknown code.
func TestRelayFailureClassification(t *testing.T) {
	coded := func(code string) error { return &rpc.Error{Code: code, Message: "refused: " + code} }
	type row struct {
		name string
		err  error
		want relayFailure
	}
	cases := []row{
		{"link dropped while it went", rpc.ErrDropped, relayFailedInDoubt},
		{"link closed", rpc.ErrClosed, relayFailedInDoubt},
		{"time ran out", context.DeadlineExceeded, relayFailedInDoubt},
		{"assistant_in_doubt", coded(assistantwire.CodeAssistantInDoubt), relayFailedInDoubt},
		{"node_closed from the bus", busError(coded(CodeNodeClosed)), relayFailedInDoubt},

		{"no code", &rpc.Error{Message: "the launcher did not start, the message was not delivered"}, relayFailedNotNow},
		{"internal", coded("internal"), relayFailedNotNow},
		{"machine_offline", coded(CodeMachineOffline), relayFailedNotNow},
		{"machine_offline from the bus", busError(coded(CodeMachineOffline)), relayFailedNotNow},
		// The manager's own computers answer an offline machine with the bare error, no code (devices/machine.go).
		{"offline on the manager's own computer", fmt.Errorf("%s: %w", "box", apierr.ErrOffline), relayFailedNotNow},
		{"node_closed", coded(CodeNodeClosed), relayFailedNotNow},

		{"no_device from the bus", busError(coded(assistantwire.CodeNoDevice)), relayFailedSetup},

		{"the conversation is gone", &rpc.Error{Message: "conversation conv1 is gone"}, relayFailedGone},
		{"the manager's pick is gone", errors.New("the picked model is gone"), relayFailedGone},

		{"the manager's own refusal", errors.New("the model pick cannot be read"), relayFailedLocal},
		{"the manager's own localized refusal", i18n.Err("external-adapter.scope_required"), relayFailedLocal},

		{"assistant_supervision_runtime", coded(assistantwire.CodeSupervisionRuntime), relayFailedRefused},
		{"conversation_deleted", coded("conversation_deleted"), relayFailedRefused},
		{"an unknown code", coded("unheard_of"), relayFailedRefused},
	}
	// The codes the review of #11393 found missing are named here, not only reached through the lists.
	for _, code := range []string{"busy", "adapter_busy", "compacting", "runtime_away", "assistant_control_failed", "adapter_timeout",
		"adapter_unavailable", "adapter_revision_mismatch", "handover", "node_restarting", "deadline"} {
		cases = append(cases, row{code, coded(code), relayFailedBusy})
	}
	for _, code := range []string{proto.CodeClaudeMissing, proto.CodeLauncherMissing, proto.CodeNodeOutdated, "no_device",
		"adapter_credentials_missing", "adapter_disabled", "adapter_config_conflict", "launcher_out_of_scope", "launcher_own_service",
		"launcher_other_kind", "assistant_launcher_profile_switch"} {
		cases = append(cases, row{code, coded(code), relayFailedSetup})
	}
	for _, code := range relayBusyCodes {
		cases = append(cases, row{code, coded(code), relayFailedBusy})
	}
	for _, code := range relaySetupCodes {
		cases = append(cases, row{code, coded(code), relayFailedSetup})
	}
	for _, tc := range cases {
		if got := relayFailureOf(tc.err); got != tc.want {
			t.Errorf("%s (%v): %d, want %d", tc.name, tc.err, got, tc.want)
		}
		if got := refusedForGood(tc.err); got != (tc.want == relayFailedRefused) {
			t.Errorf("%s: refused for good %v", tc.name, got)
		}
	}
}

// Every refusal an external launcher's adapter can answer (node/external_assistant.go externalStderrCode, the codes the
// node passes back on assistant.send) waits: not now, or until the owner fixes the connection. None ends a message.
func TestEveryExternalAdapterRefusalWaits(t *testing.T) {
	src, err := os.ReadFile("../node/external_assistant.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func externalStderrCode(")
	if start < 0 {
		t.Fatal("externalStderrCode is gone from node/external_assistant.go: read the adapter's codes where they are now")
	}
	end := strings.Index(body[start:], "\n}\n")
	codes := regexp.MustCompile(`"(adapter_[a-z_]+)"`).FindAllStringSubmatch(body[start:start+end], -1)
	if len(codes) < 10 {
		t.Fatalf("read %d adapter codes, want the node's whole list", len(codes))
	}
	for _, m := range codes {
		if m[1] == "adapter_source_options_ignored" { // a diagnostic the node drops, never a refusal
			continue
		}
		if f := relayFailureOf(&rpc.Error{Code: m[1]}); f != relayFailedBusy && f != relayFailedSetup {
			t.Errorf("%s ends a message (%d): list it in relayBusyCodes or relayAdapterSetupCodes", m[1], f)
		}
	}
}

// refuseSendsTo has the fake node refuse every assistant.send into conversation id with err, and take the others.
func refuseSendsTo(f *fakeHub, id string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if prev != nil {
			prev(f, p)
		}
		f.failAssistant = nil
		if p["session_id"] == id {
			f.failAssistant = err
		}
	}
}

// sentTexts are the texts of the sends that went into conversation id.
func (f *fakeHub) sentTexts(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.sends {
		if a["session_id"] == id {
			text, _ := a["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

func (f *fakeHub) pushList() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pushes)
}

// drainSettled drains conversation c and waits until no drain of it runs.
func drainSettled(t *testing.T, s *Service, c proto.Session) {
	t.Helper()
	s.drainRelays(c.MachineID, []proto.Session{c})
	waitFor(t, "the drain to settle", func() bool { return !drainingNow(s, c.SessionID) })
}

// side1 is the side conversation relayRefusalEnv refuses messages in.
var side1 = proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Title: "Research", Status: proto.StatusIdle}

// relayRefusalEnv is a service with the main conversation conv1, side1, which refuses every message with refusal, and
// conv2, which sends; all idle. It returns the once_ids of the tries into side1.
func relayRefusalEnv(t *testing.T, refusal error) (*Service, *fakeHub, *[]string) {
	t.Helper()
	s, f := newQuietService(t)
	f.mu.Lock()
	f.known["side1"], f.known["conv2"] = true, true
	f.mu.Unlock()
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}, side1,
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	refuseSendsTo(f, "side1", refusal)
	return s, f, relayTries(f, "side1")
}

// A conversation that cannot take a message now, by a code (it is at work, compacting, away; its setup missing or too
// old), keeps it queued: it is tried again on a later drain, never sooner than relayRetryGap after it failed, and goes
// in once the conversation takes it.
func TestQueuedNotNowRefusalWaitsForTheGap(t *testing.T) {
	for _, code := range append(slices.Clone(relayBusyCodes), relaySetupCodes...) {
		t.Run(code, func(t *testing.T) {
			s, f := newQuietService(t)
			gap := relayRetryGap
			relayRetryGap = 100 * time.Millisecond
			t.Cleanup(func() { relayRetryGap = gap })
			idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			s.queueRelay("conv1", relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "status?", At: time.Now().UTC()})
			var at []time.Time
			f.mu.Lock()
			f.failAssistant = &rpc.Error{Code: code, Message: "not now"}
			f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
				if p["session_id"] == "conv1" {
					at = append(at, time.Now())
				}
			}
			f.mu.Unlock()
			tries := func() []time.Time {
				f.mu.Lock()
				defer f.mu.Unlock()
				return slices.Clone(at)
			}
			drainSettled(t, s, idle)
			if len(tries()) != 1 || s.queuedFor("conv1") != 1 {
				t.Fatalf("after the first try: %d tries, %d queued", len(tries()), s.queuedFor("conv1"))
			}
			s.drainRelays("server", []proto.Session{idle}) // at once: it waits for the gap, then its timer drains it
			waitFor(t, "the try after the gap", func() bool { return len(tries()) == 2 && !drainingNow(s, "conv1") })
			if got := tries(); got[1].Sub(got[0]) < relayRetryGap {
				t.Fatalf("tried again after %v, sooner than the gap", got[1].Sub(got[0]))
			}
			if s.queuedFor("conv1") != 1 || len(f.pushList()) != 0 || f.sentTo("conv2") != 0 {
				t.Fatal("a refusal that only means not now left the queue or was reported")
			}
			f.mu.Lock()
			f.failAssistant = nil
			f.mu.Unlock()
			s.drainRelays("server", []proto.Session{idle})
			waitFor(t, "the message to go in", func() bool { return s.queuedFor("conv1") == 0 && f.sentTo("conv1") == 1 })
		})
	}
}

// A queued message refused for good with a code nobody knows is tried once and ends as its kind says (#11393): a
// supervision message ends in the owner's push and is not moved (decision 2 is open), a card answer moves to the main
// conversation, Persona's own notices end in one push, a message from another conversation leaves with one line to its
// sender. Three more drains send nothing. An approval result stays queued and is tried again.
func TestQueuedRefusalForGoodEndsByKind(t *testing.T) {
	at := time.Now().UTC()
	notice := "assistant-conversations.notice_delivery_refused"
	for _, tc := range []struct {
		name string
		msg  relayMsg
		body string // the push's text key; "" none
	}{
		{"supervision message", relayMsg{From: superviseTool, FromTitle: "督办", Tool: superviseTool, Text: "📋 a task waits for review", At: at},
			"assistant-conversations.supervision_delivery_refused"},
		{"card answer", relayMsg{From: "todo_1", FromTitle: "待办「x」", Tool: askAnswerTool, Text: "the owner chose A", At: at, Owner: true}, ""},
		{"approval result", relayMsg{From: approvalResultFrom + "ix1", FromTitle: "Approval result", Tool: askAnswerTool, Text: "Approval ix1 applied", At: at}, ""},
		{"done notice", relayMsg{From: "todo_1", FromTitle: "任务「x」", Tool: noticeTool, State: "done", Text: "🔔 done", At: at}, notice},
		{"interrupted notice", relayMsg{From: "todo_1", FromTitle: "任务「x」", Tool: noticeTool, State: "interrupted", Text: "🔔 cut off", At: at}, notice},
		{"task end", relayMsg{From: "todo_1", FromTitle: "任务「x」", Tool: taskEndTool, Text: "🔔 accepted", At: at}, notice},
		{"spawn notice", relayMsg{From: "conv9", FromTitle: "Research", Tool: spawnNoticeTool, Text: "🔔 ended", At: at}, notice},
		{"chat reply", relayMsg{From: "thread1", FromTitle: "群「x」", Tool: chatReplyTool, Text: "new messages", At: at}, notice},
		{"conversation_send", relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "please check the build", At: at}, ""},
		{"conversation_new", relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_new", Text: "please check the build", At: at}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			gap := relayRetryGap
			relayRetryGap = 0 // a message that stays is tried on every drain
			t.Cleanup(func() { relayRetryGap = gap })
			s, f, tries := relayRefusalEnv(t, &rpc.Error{Code: "unheard_of", Message: "this conversation will not take it"})
			fromConversation := tc.msg.Tool == "conversation_send" || tc.msg.Tool == "conversation_new"
			s.queueRelay("side1", tc.msg)

			drainSettled(t, s, side1)
			switch { // what follows a refusal runs once the drain let go of it
			case tc.body != "":
				waitFor(t, "the owner's push", func() bool { return len(f.pushList()) == 1 })
			case tc.name == "card answer":
				waitFor(t, "the answer in the main conversation", func() bool { return f.sentTo("conv1") == 1 })
			case fromConversation:
				waitFor(t, "the line to the sender", func() bool { return f.sentTo("conv2") == 1 })
			}
			for range 3 {
				drainSettled(t, s, side1)
			}
			got := len(triesOf(f, tries))
			if tc.name == "approval result" {
				if got != 4 || s.queuedFor("side1") != 1 || f.sentTo("conv1") != 0 || len(f.pushList()) != 0 {
					t.Fatalf("an approval result: %d tries, %d queued, %d in the main conversation, %d pushes", got, s.queuedFor("side1"),
						f.sentTo("conv1"), len(f.pushList()))
				}
				return
			}
			if got != 1 || s.queuedFor("side1") != 0 {
				t.Fatalf("%d tries, %d still queued: want one try and none", got, s.queuedFor("side1"))
			}
			pushes := f.pushList()
			if tc.body == "" && len(pushes) != 0 {
				t.Fatalf("pushed %+v", pushes)
			}
			if want := i18n.T(s.lang(ctx), tc.body); tc.body != "" && (len(pushes) != 1 || pushes[0].SessionID != "side1" || pushes[0].Body != want) {
				t.Fatalf("pushes %+v, want one about side1 saying %q", pushes, want)
			}
			if tc.name == "card answer" {
				if texts := f.sentTexts("conv1"); !strings.Contains(texts[0], "the owner chose A") {
					t.Fatalf("the main conversation got %q", texts[0])
				}
			} else if f.sentTo("conv1") != 0 {
				t.Fatal("the main conversation got the refused message: a supervision message is not moved, a notice ends")
			}
			if fromConversation {
				line := f.sentTexts("conv2")[0]
				for _, want := range []string{"side1", "Research", "unheard_of", tc.msg.Tool, "please check the build"} {
					if !strings.Contains(line, want) {
						t.Errorf("the sender's line lacks %q:\n%s", want, line)
					}
				}
			} else if f.sentTo("conv2") != 0 {
				t.Fatal("a line went to a conversation that sent nothing")
			}
			main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			sender := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			for range 3 { // the line, the moved answer and the push are told once
				drainSettled(t, s, side1)
				drainSettled(t, s, sender)
				drainSettled(t, s, main)
			}
			if len(triesOf(f, tries)) != 1 || len(f.pushList()) != len(pushes) || f.sentTo("conv1") > 1 || f.sentTo("conv2") > 1 {
				t.Fatal("a later drain sent the refused message again or told it twice")
			}
		})
	}
}

// The owner's push for a message refused for good says what the refusal means, word for word (#10338's push kept):
// a supervision message to a runtime without supervision policy asks for a Claude Code or Codex supervisor and any
// other refusal of one says the follow-up was refused; a notice or approval result a runtime cannot take says the
// runtime cannot take Persona's separate instructions, and another refusal of a notice says an update was refused. An
// approval result a runtime cannot take ends with that push, as before; any other refusal keeps it queued (above).
func TestRefusedForGoodPushSaysWhy(t *testing.T) {
	runtime := assistantwire.CodeSupervisionRuntime
	texts := map[string]bool{}
	for _, tc := range []struct {
		name, tool, from, code, body string
	}{
		{"supervision, runtime", superviseTool, superviseTool, runtime, "assistant-conversations.supervision_delivery_failed"},
		{"supervision, other", superviseTool, superviseTool, "unheard_of", "assistant-conversations.supervision_delivery_refused"},
		{"task end, runtime", taskEndTool, "todo_1", runtime, "rpc-errors.assistant_supervision_runtime"},
		{"task end, other", taskEndTool, "todo_1", "unheard_of", "assistant-conversations.notice_delivery_refused"},
		{"approval result, runtime", askAnswerTool, approvalResultFrom + "ix1", runtime, "rpc-errors.assistant_supervision_runtime"},
	} {
		for _, path := range []string{"queued", "direct"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				ctx := context.Background()
				refusal := &rpc.Error{Code: tc.code, Message: "no"}
				s, f, tries := relayRefusalEnv(t, refusal)
				f.mu.Lock()
				f.hello.Locale = "en"
				f.mu.Unlock()
				msg := relayMsg{From: tc.from, FromTitle: "x", Tool: tc.tool, Text: "something to tell", At: time.Now().UTC()}
				if path == "queued" {
					s.queueRelay("side1", msg)
					drainSettled(t, s, side1)
				} else if !s.refusedDirect(ctx, "server", "side1", msg, refusal) {
					t.Fatal("the refusal did not end the message")
				}
				waitFor(t, "the owner's push", func() bool { return len(f.pushList()) == 1 })
				want := i18n.T(i18n.En, tc.body)
				if p := f.pushList()[0]; p.SessionID != "side1" || p.Body != want || p.Title != i18n.T(i18n.En, "assistant-conversations.relay_delivery_failed") {
					t.Fatalf("push %q / %q about %s, want %q about side1", p.Title, p.Body, p.SessionID, want)
				}
				texts[want] = true
				if s.queuedFor("side1") != 0 || path == "queued" && len(triesOf(f, tries)) != 1 {
					t.Fatal("the message did not end at its one try")
				}
			})
		}
	}
	if len(texts) != 4 {
		t.Fatalf("%d different texts, want the four kinds of refusal told apart", len(texts))
	}
}

// A card answer the main conversation refuses as well stays queued there; it is not moved again.
func TestCardAnswerRefusedByTheMainConversationStays(t *testing.T) {
	s, f := newQuietService(t)
	gap := relayRetryGap
	relayRetryGap = 0
	t.Cleanup(func() { relayRetryGap = gap })
	main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	refuseSendsTo(f, "conv1", &rpc.Error{Code: "unheard_of", Message: "no"})
	tries := relayTries(f, "conv1")
	s.queueRelay("conv1", relayMsg{From: "todo_1", FromTitle: "待办「x」", Tool: askAnswerTool, Text: "the owner chose A", At: time.Now().UTC()})
	for range 3 {
		drainSettled(t, s, main)
	}
	if got := len(triesOf(f, tries)); got != 3 || s.queuedFor("conv1") != 1 || len(f.pushList()) != 0 {
		t.Fatalf("%d tries, %d queued: want it kept and tried on each drain", got, s.queuedFor("conv1"))
	}
}

// A card answer moved to the main conversation while the main conversation's turn waits on its own question lets go
// of that turn, as any message queued for it does (ask_release.go), so the owner's answer does not wait behind it.
func TestMovedCardAnswerReleasesTheMainConversationsQuestion(t *testing.T) {
	releaseAfter(t, 0)
	e := newAskTodoEnv(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.f.mu.Lock()
	e.f.known["side1"] = true
	e.f.mu.Unlock()
	e.sessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusWorking},
		proto.Session{SessionID: "c1x1x1", Title: "Release chat", Assistant: true, Status: proto.StatusIdle}, side1)
	done := make(chan string, 1)
	go func() {
		text, err := e.s.toolAskUser(ctx, "server", "m1", json.RawMessage(strings.Replace(askLaterArgs, `"wait":false,`, "", 1)))
		if err != nil {
			text = "error: " + err.Error()
		}
		done <- text
	}()
	waitFor(t, "the main conversation's question", func() bool { return len(e.s.pendingQuestions()) == 1 })
	refuseSendsTo(e.f, "side1", &rpc.Error{Code: "unheard_of", Message: "no"})
	e.s.queueRelay("side1", relayMsg{From: "todo_1", FromTitle: "待办「x」", Tool: askAnswerTool, Text: "the owner chose A", At: time.Now().UTC(), Owner: true})
	drainSettled(t, e.s, side1)
	if e.s.queuedFor("m1") != 1 {
		t.Fatalf("%d queued for the main conversation, want the moved answer", e.s.queuedFor("m1"))
	}
	select {
	case text := <-done:
		if !strings.Contains(text, "no longer holds this turn") {
			t.Fatalf("the question's call returned %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the main conversation's question still holds its turn with the moved answer waiting")
	}
}

// The direct tries refused for good end the same way: a task's end is not queued and the owner is pushed once, a card
// answer moves to the main conversation, an approval result is queued for its conversation, and a supervision
// message to a side conversation ends in the owner's push, neither queued nor moved.
func TestDirectRefusalForGoodEndsByKind(t *testing.T) {
	ctx := context.Background()
	refusal := &rpc.Error{Code: "unheard_of", Message: "no"}
	t.Run("task end", func(t *testing.T) {
		s, f, tries := relayRefusalEnv(t, refusal)
		s.relayOrQueue(ctx, "side1", relayMsg{From: "todo_1", FromTitle: "任务「x」", Tool: taskEndTool, Text: "🔔 accepted", At: time.Now().UTC()})
		if len(triesOf(f, tries)) != 1 || s.queuedFor("side1") != 0 || len(f.pushList()) != 1 {
			t.Fatal("a task's end refused for good was queued, or not told once")
		}
	})
	t.Run("card answer", func(t *testing.T) {
		s, f, tries := relayRefusalEnv(t, refusal)
		if err := s.relayToAsker(ctx, askTodo{TodoID: "todo_2", MachineID: "server", SessionID: "side1"}, "待办「y」", "the owner chose B", true); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the answer in the main conversation", func() bool { return f.sentTo("conv1") == 1 })
		if len(triesOf(f, tries)) != 1 || s.queuedFor("side1") != 0 || !strings.Contains(f.sentTexts("conv1")[0], "the owner chose B") {
			t.Fatal("the refused card answer did not move to the main conversation")
		}
	})
	t.Run("approval result", func(t *testing.T) {
		s, f, tries := relayRefusalEnv(t, refusal)
		if err := s.deliverApprovalResult(approvalJournalEntry{ID: "ix1", MachineID: "server", SessionID: "side1", Status: "resolved",
			Result: "Approval ix1 applied", UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the queue's try", func() bool { return len(triesOf(f, tries)) == 2 && !drainingNow(s, "side1") })
		if s.queuedFor("side1") != 1 || f.sentTo("conv1") != 0 || len(f.pushList()) != 0 {
			t.Fatal("a refused approval result was not kept for its conversation")
		}
	})
	t.Run("supervision message", func(t *testing.T) {
		s, f, task := newSupervisedService(t)
		f.mu.Lock()
		f.known["side1"] = true
		f.mu.Unlock()
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}, side1)
		refuseSendsTo(f, "side1", refusal)
		tries, main := relayTries(f, "side1"), f.sentTo("conv1")
		if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
			v.State = reviewPending
			return true
		}); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		task = todoNow(s, task.ID)
		if !s.deliverDigest(ctx, s.settings(ctx), "side1", watchSignals(task, nil, now), "", now, time.Time{}) {
			t.Fatal("the refused supervision message was not retired")
		}
		waitFor(t, "the owner's push", func() bool {
			return slices.ContainsFunc(f.pushList(), func(p Notification) bool {
				return p.SessionID == "side1" && p.Body == i18n.T(s.lang(ctx), "assistant-conversations.supervision_delivery_refused")
			})
		})
		if len(triesOf(f, tries)) != 1 || s.queuedFor("side1") != 0 || f.sentTo("conv1") != main {
			t.Fatal("the refused supervision message was queued or moved")
		}
		if got := todoNow(s, task.ID); got.DoneAt != nil || got.Supervision.State != reviewPending {
			t.Fatalf("the task must stay open for review: %+v", got)
		}
	})
}

// Until #11393's decision 1 is answered, conversation_send returns the coded "not now" refusals and the setups the
// owner fixes to the sender and queues nothing, as before; a computer away still queues the message, in each shape
// its refusal comes: the node's code, the bus's, and the bare error of the manager's own computers.
func TestConversationSendNotNowRefusals(t *testing.T) {
	try := func(t *testing.T, refusal error) (string, error, int) {
		s, f := newQuietService(t)
		f.mu.Lock()
		f.failAssistant = refusal
		f.known["conv2"] = true
		f.mu.Unlock()
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		out, err := s.toolConversationSend(context.Background(), "conv2", []byte(`{"id":"conv1","text":"status?"}`))
		return out, err, s.queuedFor("conv1")
	}
	for _, code := range append(slices.Clone(relayBusyCodes), relaySetupCodes...) {
		if out, err, queued := try(t, &rpc.Error{Code: code, Message: "not now"}); err == nil || queued != 0 {
			t.Errorf("%s: reported %q, %d queued", code, out, queued)
		}
	}
	for name, away := range map[string]error{
		"the node's code":            &rpc.Error{Code: CodeMachineOffline, Message: "machine is offline"},
		"the bus's coded error":      busError(&rpc.Error{Code: CodeMachineOffline, Message: "machine is offline"}),
		"the manager's own computer": fmt.Errorf("%s: %w", "server", apierr.ErrOffline),
	} {
		if out, err, queued := try(t, away); err != nil || queued != 1 || !strings.Contains(out, "could not take the message now") {
			t.Errorf("a computer away, as %s: %q, %v, %d queued", name, out, err, queued)
		}
	}
}

// A quiet task's notice the conversation refuses for good sends the push it stood for: the card that notice stands
// for, not every card of the task (needs); the newest waiting card for a needs notice queued before notices named
// their card; the failed start (failed); and one push saying an update was refused for any other notice (done).
func TestQuietNoticeRefusedForGoodSendsItsPush(t *testing.T) {
	for _, tc := range []string{"needs", "needs queued by an older manager", "failed", "done"} {
		t.Run(tc, func(t *testing.T) {
			ctx := context.Background()
			s, f := newQuietService(t)
			f.mu.Lock()
			f.known["lark1"] = true
			f.mu.Unlock()
			main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			lark := proto.Session{SessionID: "lark1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}
			f.setSessions("server", main, lark)
			s.markReporter(ctx, "server", "lark1")
			if _, err := s.runTool(ctx, "server", "lark1", "agent_start",
				json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`)); err != nil {
				t.Fatal(err)
			}
			todo := onlyTodo(s)
			if !todo.Quiet || todo.Supervision != nil || todo.Origin != "lark1" {
				t.Fatalf("not a quiet task told in lark1: %+v", todo)
			}
			refuseSendsTo(f, "lark1", &rpc.Error{Code: "unheard_of", Message: "no"})
			tries := relayTries(f, "lark1")
			// The card the notice stands for was raised first; a newer one of the same agent waits too.
			asked := proto.Interaction{ID: "ix_asked", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, CreatedAt: time.Now().Add(-time.Minute),
				Questions: []proto.Question{{Question: "用哪个分支？"}}}
			newer := proto.Interaction{ID: "ix_newer", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, CreatedAt: time.Now(),
				Questions: []proto.Question{{Question: "要不要合并？"}}}
			if strings.HasPrefix(tc, "needs") {
				agentTurn(s, f, proto.StatusBlocked, "", 2)
				s.fleet.mu.Lock()
				if s.fleet.interactions == nil {
					s.fleet.interactions = map[string]*pending{}
				}
				s.fleet.interactions[asked.ID] = &pending{ix: asked}
				s.fleet.interactions[newer.ID] = &pending{ix: newer}
				s.fleet.mu.Unlock()
			}
			switch tc {
			case "needs":
				s.InteractionRaised(ctx, InteractionRaised{Interaction: asked}) // the notice queues behind lark1's turn
			case "needs queued by an older manager":
				s.queueRelay("lark1", relayMsg{From: todo.ID, FromTitle: "任务「x」", Tool: noticeTool, State: "needs", Text: "🔔 等你", At: time.Now().UTC()})
			default:
				s.queueRelay("lark1", relayMsg{From: todo.ID, FromTitle: "任务「x」", Tool: noticeTool, State: tc, Text: "🔔 " + tc, At: time.Now().UTC()})
			}
			waitFor(t, "the notice to queue", func() bool { return s.queuedFor("lark1") == 1 })
			before := len(f.pushList())
			lark.Status = proto.StatusIdle
			f.setSessions("server", main, lark)
			drainSettled(t, s, lark)
			waitFor(t, "the push", func() bool { return len(f.pushList()) == before+1 })
			p := f.pushList()[before]
			switch tc {
			case "needs":
				if p.Kind != assistantwire.NotifyInput || p.Interaction == nil || p.Interaction.ID != asked.ID {
					t.Fatalf("push %+v, want the card the notice stood for", p)
				}
			case "needs queued by an older manager":
				if p.Kind != assistantwire.NotifyInput || p.Interaction == nil || p.Interaction.ID != newer.ID {
					t.Fatalf("push %+v, want the task's newest card", p)
				}
			case "failed":
				if p.SessionID != "lark1" || !strings.Contains(p.Title, todo.Title) {
					t.Fatalf("push %+v, want the failed start's", p)
				}
			case "done":
				if p.SessionID != "lark1" || p.Body != i18n.T(s.lang(ctx), "assistant-conversations.notice_delivery_refused") {
					t.Fatalf("push %+v, want one saying an update was refused", p)
				}
			}
			drainSettled(t, s, lark)
			if len(triesOf(f, tries)) != 1 || s.queuedFor("lark1") != 0 || len(f.pushList()) != before+1 {
				t.Fatal("the refused notice was tried or told again")
			}
		})
	}
}

// The line that tells a sender its message was refused for good (prompts/assistant/relay_refused.md) is written in
// the owner's language by the code that sends it (tellRelayRefused), pinned for the three languages.
func TestRelayRefusedLineInTheOwnersLanguage(t *testing.T) {
	for _, tc := range []struct{ locale, golden string }{{"", "relay_refused"}, {"en", "relay_refused.en"}, {"ja", "relay_refused.ja"}} {
		t.Run(tc.golden, func(t *testing.T) {
			s, f, _ := relayRefusalEnv(t, &rpc.Error{Code: "unheard_of", Message: "no"})
			f.mu.Lock()
			f.hello.Locale = tc.locale
			f.mu.Unlock()
			s.queueRelay("side1", relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "please check\nthe build", At: time.Now().UTC()})
			drainSettled(t, s, side1)
			waitFor(t, "the line to the sender", func() bool { return f.sentTo("conv2") == 1 })
			golden(t, tc.golden, f.sentTexts("conv2")[0])
		})
	}
}

// A chat whose replies a conversation refuses for good pushes the owner once, not once per batch: the refusal counts
// as the reply having gone for the gap between batches, so the next batch waits the gap and those after it merge into
// it, and a later refusal of that conversation's chat replies pushes nobody again. A chat reply that goes in ends it:
// the next refusal is pushed.
func TestRefusedChatReplyPushesOncePerConversation(t *testing.T) {
	h := newChatReplyHarness(t, replyConv("conv-a"))
	now := time.Now()
	h.wrote(t, "server", "conv-a", replyChat, now.Add(-time.Hour))
	refusing := true
	h.f.mu.Lock()
	h.f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		f.failAssistant = nil
		if p["session_id"] == "conv-a" && refusing {
			f.failAssistant = &rpc.Error{Code: "unheard_of", Message: "this conversation will not take it"}
		}
	}
	h.f.mu.Unlock()
	tries := relayTries(h.f, "conv-a")
	setRefusing := func(on bool) {
		h.f.mu.Lock()
		refusing = on
		h.f.mu.Unlock()
	}
	// dueNow has the chat reply waiting for conv-a go in at once, as when its gap is over.
	dueNow := func() {
		b := &h.s.relays
		b.mu.Lock()
		if q := b.st.Queue["conv-a"]; len(q) == 1 {
			q[0].Due = time.Time{}
		}
		b.mu.Unlock()
		drainSettled(t, h.s, replyConv("conv-a"))
	}

	h.round(migratedConnectorID, replyBatch(replyChat, KindGroup, now.Add(-3*time.Minute), "m1"))
	if got := len(triesOf(h.f, tries)); got != 1 || len(h.f.pushList()) != 1 || len(h.queued("conv-a")) != 0 {
		t.Fatalf("the first batch: %d tries, %d pushes, %d queued; want one try, one push", got, len(h.f.pushList()), len(h.queued("conv-a")))
	}
	h.round(migratedConnectorID, replyBatch(replyChat, KindGroup, now.Add(-2*time.Minute), "m2"))
	h.round(migratedConnectorID, replyBatch(replyChat, KindGroup, now.Add(-time.Minute), "m3"))
	q := h.queued("conv-a")
	if got := len(triesOf(h.f, tries)); got != 1 || len(h.f.pushList()) != 1 || len(q) != 1 || !q[0].Due.After(time.Now()) ||
		!slices.Equal(q[0].Chat.IDs, []string{"m2", "m3"}) {
		t.Fatalf("batches after a refusal: %d tries, %d pushes, queued %+v; want them waiting the gap in one message", got, len(h.f.pushList()), q)
	}
	dueNow() // refused again once the gap is over: it leaves, and the owner was told already
	if got := len(triesOf(h.f, tries)); got != 2 || len(h.f.pushList()) != 1 || len(h.queued("conv-a")) != 0 {
		t.Fatalf("the next refusal: %d tries, %d pushes, %d queued; want no second push", got, len(h.f.pushList()), len(h.queued("conv-a")))
	}

	setRefusing(false) // a chat reply goes in: the conversation takes them again
	h.round(migratedConnectorID, replyBatch(replyChat, KindGroup, now, "m4"))
	dueNow()
	if got := h.told("conv-a"); len(got) != 1 || len(h.queued("conv-a")) != 0 {
		t.Fatalf("the reply that goes in: told %d, queued %d", len(got), len(h.queued("conv-a")))
	}
	setRefusing(true)
	h.round(migratedConnectorID, replyBatch(replyChat, KindGroup, now.Add(time.Second), "m5"))
	dueNow()
	if len(h.f.pushList()) != 2 {
		t.Fatalf("%d pushes: a refusal after a reply went in is pushed again", len(h.f.pushList()))
	}
}

// Every code the manager's devices layer answers a call with before or around the node (devices/machine.go callOnce,
// devices/devices.go callNode and wireError) either waits on assistant.send or never answers it, so none ends a relay
// that the owner's update or setup would let in. A new code there fails this until it is placed.
func TestEveryDevicesLayerRefusalWaits(t *testing.T) {
	known := map[string]string{
		"proto.CodeNodeOutdated":         proto.CodeNodeOutdated,
		"proto.CodeStartGoalUnsupported": proto.CodeStartGoalUnsupported,
		"devicewire.CodeNoDevice":        devicewire.CodeNoDevice,
		"devicewire.CodeDeadline":        devicewire.CodeDeadline,
		"devicewire.CodeMachineOffline":  devicewire.CodeMachineOffline,
		"devicewire.CodeNodeClosed":      devicewire.CodeNodeClosed,
	}
	notOnSend := map[string]string{ // the methods they answer; assistant.send is none of them
		proto.CodeStartGoalUnsupported: "session.start with a goal",
		"forbidden":                    "session.send_unseen",
		"user_confirmation_required":   "session.codex.takeover.*",
		"invalid_params":               "port.dial",
	}
	code := regexp.MustCompile(`Code:\s*([A-Za-z.]+|"[a-z_]+")`)
	found := 0
	for _, fn := range []struct{ file, sig string }{
		{"../devices/machine.go", "func (m *Machine) callOnce("},
		{"../devices/devices.go", "func (d *Devices) callNode("},
		{"../devices/devices.go", "func wireError("},
	} {
		src, err := os.ReadFile(fn.file)
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		start := strings.Index(body, fn.sig)
		if start < 0 {
			t.Fatalf("%s is gone from %s: read the devices layer's codes where they are now", fn.sig, fn.file)
		}
		end := strings.Index(body[start:], "\n}\n")
		for _, m := range code.FindAllStringSubmatch(body[start:start+end], -1) {
			name := strings.Trim(m[1], `"`)
			if !strings.HasPrefix(m[1], `"`) {
				v, ok := known[m[1]]
				if !ok {
					t.Errorf("%s answers %s, which this test does not know: classify it in relay_refusal.go and add it here", fn.sig, m[1])
					continue
				}
				name = v
			}
			found++
			if _, never := notOnSend[name]; never {
				continue
			}
			if f := relayFailureOf(&rpc.Error{Code: name}); f != relayFailedNotNow && f != relayFailedBusy && f != relayFailedSetup {
				t.Errorf("%s (%s) ends a relay refused with it (%d)", name, fn.sig, f)
			}
		}
	}
	if found < 8 {
		t.Fatalf("read %d codes, want the devices layer's whole list", found)
	}
}
