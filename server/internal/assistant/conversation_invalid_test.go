package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const sealedCompaction = "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account and can't go to it now"

func TestSealedCompactionCannotContinue(t *testing.T) {
	for _, text := range []string{sealedCompaction, "API Error: 503 upstream: " + sealedCompaction, strings.ToUpper(sealedCompaction)} {
		c := apiErrorTurn("sealed", text)
		c.TurnErrorKind = proto.TurnErrorTransient // A runtime's generic kind must not override a permanent cause.
		if code := sessionTurnErrorCode(c); code != "compaction_sealed" {
			t.Errorf("code = %q, want compaction_sealed", code)
		}
		if turnErrorTransient(c) || transientError(text) {
			t.Errorf("unrecoverable error was retryable: %q", text)
		}
		w, by, skip := triageRules(c, time.UTC, "en")
		if skip || by != "rule" || w == nil || w.On != proto.WaitUser || len(w.Questions) != 0 ||
			!strings.Contains(w.Line, "new conversation") {
			t.Errorf("invalid conversation triage = %+v, %q, %v", w, by, skip)
		}
	}
	for _, text := range []string{"API Error: 503 Service Unavailable", "API Error: 503 encrypted connection failed"} {
		if !turnErrorTransient(apiErrorTurn("temporary", text)) {
			t.Errorf("ordinary transient failure no longer retryable: %q", text)
		}
	}
}

func TestSealedCompactionPublishesInvalidConversation(t *testing.T) {
	ft := newFleetTestNoStore(t)
	c := apiErrorTurn("sealed", sealedCompaction)
	c.TurnErrorKind = proto.TurnErrorTransient
	ft.sessions("mac", c)
	got := listed(t, ft.s, "mac", "sealed")
	if got.TurnErrorCode != "compaction_sealed" || got.TurnErrorKind != "unrecoverable" {
		t.Fatalf("published conversation error = %q/%q", got.TurnErrorCode, got.TurnErrorKind)
	}
}

func TestSealedCompactionOverridesSilentTurn(t *testing.T) {
	for _, kind := range []string{proto.TurnErrorNoOutput, proto.TurnErrorStopped} {
		t.Run(kind, func(t *testing.T) {
			c := apiErrorTurn("sealed", sealedCompaction)
			c.TurnErrorKind = kind
			w, by, skip := triageRules(c, time.UTC, "en")
			if skip || by != "rule" || w == nil || w.On != proto.WaitUser ||
				len(w.Questions) != 0 || !strings.Contains(w.Line, "new conversation") {
				t.Fatalf("sealed error lost to silent-turn handling: %+v, %q, %v", w, by, skip)
			}
			if turnErrorTransient(c) {
				t.Fatal("sealed error with a silent-turn kind can retry")
			}
		})
	}
}

func TestSealedCompactionCancelsQueuedContinue(t *testing.T) {
	s, f := workService(t)
	c := apiErrorTurn("sealed", sealedCompaction)
	f.sessions["box"] = []proto.Session{c}
	now := time.Now().UTC()
	e := db.SessionWork{MachineID: "box", SessionID: c.SessionID, TurnAt: c.UpdatedAt, WakeAt: &now,
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, WakeAt: &now}}
	s.putWork(context.Background(), e)
	s.workWake(context.Background(), s.newWorkView(context.Background()), [2]string{"box", c.SessionID}, now)
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("sent a queued Continue to an invalid conversation: %v", got)
	}
	if got := entryOf(t, s, c.SessionID); got.WakeAt != nil || got.Work.WakeAt != nil {
		t.Fatalf("invalid conversation still has a queued wake: %+v", got)
	}
}

func TestSealedCompactionRejectsManualTaskContinue(t *testing.T) {
	s, f := workService(t)
	f.sessions["box"] = []proto.Session{apiErrorTurn("sealed", sealedCompaction)}
	task := Todo{ID: "task", Agents: []TodoAgent{{MachineID: "box", SessionID: "sealed"}}}
	if _, err := s.continueTask(context.Background(), task); err == nil {
		t.Fatal("manual Continue did not refuse the invalid conversation")
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("manual Continue reached the invalid conversation: %v", got)
	}
}

func TestReplacementAgentOutranksOldInterruption(t *testing.T) {
	task := Todo{Agents: []TodoAgent{{MachineID: "box", SessionID: "old"}, {MachineID: "box", SessionID: "new"}}}
	for _, state := range []string{proto.StateWorking, proto.StateNeedsYou} {
		look := func(_, sid string) (proto.Session, bool) {
			if sid == "old" {
				return apiErrorTurn(sid, sealedCompaction), true
			}
			return proto.Session{SessionID: sid, State: state, Status: state}, true
		}
		if got := triageStatus(task, look, time.Now()); got != state {
			t.Errorf("replacement state %q: task = %q", state, got)
		}
	}
}
