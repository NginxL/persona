package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

func preparedPauseState(ft *fleetTest, id string) (paused bool, accepted, answered uint64, runs int) {
	ft.s.fleet.mu.Lock()
	defer ft.s.fleet.mu.Unlock()
	p := ft.s.fleet.interactions[id]
	if p == nil || p.prepared == nil {
		return false, 0, 0, 0
	}
	return p.prepared.paused, p.prepared.acceptedSeq, p.prepared.answeredSeq, len(p.prepared.channelRuns)
}

func TestClarificationTurnsCoverOnlyTheirAcceptedQuestions(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	done := startPreparedTest(t, ft, "ix_answer_turns", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_answer_turns", "First question", "owner-turn-1", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	ref := SessionRef{MachineID: "mac", SessionID: "s1"}
	// The tool turn that produced the card may end after clarification. It is not
	// the answer turn and must not restart the original decision timer.
	ft.s.approvalAnswerTurnEnded(ref)
	if paused, _, _, _ := preparedPauseState(ft, "ix_answer_turns"); !paused {
		t.Fatal("the original tool turn ended the clarification pause")
	}
	ft.s.approvalAnswerTurnStarted(ref)
	if err := ft.s.clarify(context.Background(), "ix_answer_turns", "Question during the answer", "owner-turn-2", nil); err != nil {
		t.Fatal(err)
	}
	ft.s.approvalAnswerTurnEnded(ref)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_answer_turns"); !paused || accepted != 2 || answered != 1 {
		t.Fatalf("first answer turn covered a question accepted during it: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}
	ft.s.approvalAnswerTurnStarted(ref)
	ft.s.approvalAnswerTurnEnded(ref)
	if paused, accepted, answered, _ := preparedPauseState(ft, "ix_answer_turns"); paused || accepted != 2 || answered != 2 {
		t.Fatalf("next answer turn did not cover the queued question: paused=%v accepted=%d answered=%d", paused, accepted, answered)
	}
	deny := false
	if err := ft.s.resolve("ix_answer_turns", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_answer_turns")
}

func TestClarificationQuestionWinsExpiryRaceWithoutLosingCard(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	started := make(chan struct{})
	release := make(chan struct{})
	ft.f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		close(started)
		<-release
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		ft.f.sentOnce = append(ft.f.sentOnce, p) // answerNode runs under the fake's lock
		return map[string]any{}, nil
	})
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_question_expiry_race", "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "applied", nil
	})
	questionDone := make(chan error, 1)
	go func() {
		questionDone <- ft.s.clarify(context.Background(), "ix_question_expiry_race", "Question racing expiry", "owner-expiry-race", nil)
	}()
	<-started
	ft.s.fleet.mu.Lock()
	p := ft.s.fleet.interactions["ix_question_expiry_race"]
	ft.s.fleet.mu.Unlock()
	expired := make(chan struct{})
	go func() {
		ft.s.expirePrepared(p)
		close(expired)
	}()
	select {
	case <-expired:
		t.Fatal("expiry did not wait for the in-flight question")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-questionDone; err != nil {
		t.Fatal(err)
	}
	<-expired
	if got := awaitPreparedTest(t, done); got.err != nil || !got.out.Clarifying {
		t.Fatalf("question did not win expiry race: %+v, %v", got.out, got.err)
	}
	ft.waitPending(1)
	if applies.Load() != 0 {
		t.Fatal("the question/expiry race executed the operation")
	}
	allow := true
	if err := ft.s.resolve("ix_question_expiry_race", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if applies.Load() != 1 {
		t.Fatalf("Allow after race applied %d times", applies.Load())
	}
	waitApprovalResult(t, ft, "ix_question_expiry_race")
}

func TestClarificationLosesToClaimedAllowWithoutSending(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	applyStarted := make(chan struct{})
	releaseApply := make(chan struct{})
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_allow_question_race", "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		close(applyStarted)
		<-releaseApply
		return "applied", nil
	})
	allow := true
	resolved := make(chan error, 1)
	go func() { resolved <- ft.s.resolve("ix_allow_question_race", proto.Resolution{Allow: &allow}) }()
	<-applyStarted
	if err := ft.s.clarify(context.Background(), "ix_allow_question_race", "Too late", "owner-after-allow", nil); !errors.Is(err, errClarifyUnavailable) {
		t.Fatalf("question after final claim = %v", err)
	}
	if got := len(ownerQuestionSends(ft)); got != 0 {
		t.Fatalf("question after final claim produced %d sends", got)
	}
	close(releaseApply)
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	if got := awaitPreparedTest(t, done); got.err != nil || got.out.Clarifying || got.out.Text != "applied" {
		t.Fatalf("original Allow result = %+v, %v", got.out, got.err)
	}
	if applies.Load() != 1 {
		t.Fatalf("racing Allow applied %d times", applies.Load())
	}
}

func TestClarifyDeletedConversationReturnsVisibleErrorWithoutFallback(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	other := proto.Session{MachineID: "mac", SessionID: "other", Assistant: true, Agent: "claude", Status: proto.StatusIdle}
	ft.f.setSessions("mac", other)
	ft.d.setSessions("mac", []proto.Session{{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusIdle}, other})
	row := deletedConversation{Machine: "mac", ID: "s1", At: time.Now().UTC()}
	if err := ft.st.PutSetting(context.Background(), deletedConversationsKey, map[string]deletedConversation{runtimeListID("mac", "s1"): row}); err != nil {
		t.Fatal(err)
	}
	done := startPreparedTest(t, ft, "ix_deleted_origin", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_deleted_origin", "Will this go elsewhere?", "owner-deleted-origin", nil); convCode(err) != "conversation_deleted" {
		t.Fatalf("deleted-origin clarification error = %v", err)
	}
	if got := len(ownerQuestionSends(ft)); got != 0 {
		t.Fatalf("deleted-origin question fell back to another conversation: %+v", ownerQuestionSends(ft))
	}
	select {
	case got := <-done:
		t.Fatalf("deleted-origin failure released the original tool: %+v", got)
	default:
	}
	deny := false
	if err := ft.s.resolve("ix_deleted_origin", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
}

func TestClarifyArchivedConversationExplicitlyReopensSameConversation(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	if _, err := ft.s.setArchived(context.Background(), "mac", "s1", true, titleByUser); err != nil {
		t.Fatal(err)
	}
	if !ft.s.isArchived("s1") {
		t.Fatal("fixture did not archive the originating conversation")
	}
	done := startPreparedTest(t, ft, "ix_archived_origin", "", nil)
	question := "Reopen this exact conversation"
	if err := ft.s.clarify(context.Background(), "ix_archived_origin", question, "owner-archived-origin", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	if ft.s.isArchived("s1") {
		t.Fatal("the archived origin was not explicitly reopened")
	}
	sent := ownerQuestionSends(ft)
	if len(sent) != 1 || sent[0]["session_id"] != "s1" || sent[0]["text"] != question {
		t.Fatalf("archived-origin delivery = %+v", sent)
	}
	deny := false
	if err := ft.s.resolve("ix_archived_origin", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_archived_origin")
}

func TestClarifyEndedRoomReturnsVisibleError(t *testing.T) {
	r := newRoomFixture(t)
	sid := r.roomUp(meetingRec, "participant")
	r.do("POST", livePath(meetingRec), "", `{"from":0,"segments":[{"start":1,"end":3,"text":"Ada: done."}]}`, 200)
	end := time.Now().UTC().Format(time.RFC3339)
	r.upload(meetingRec, 2, map[string]any{"room": map[string]any{"attendance": "participant"}, "ended_at": end, "complete": true})
	r.look(meetingRec)
	r.f.mu.Lock()
	for i := range r.f.sessions["server"] {
		if r.f.sessions["server"][i].SessionID == sid {
			r.f.sessions["server"][i].UpdatedAt = time.Now().Add(time.Second)
		}
	}
	r.f.mu.Unlock()
	r.look(meetingRec)
	done := make(chan preparedTestResult, 1)
	go func() {
		out, err := r.s.waitPreparedCard(context.Background(), proto.Interaction{ID: "ix_ended_room", MachineID: "server", SessionID: sid,
			Kind: proto.IxPermission, ExpiresAt: time.Now().Add(time.Hour)}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
			return "unexpected", nil
		}})
		done <- preparedTestResult{out: out, err: err}
	}()
	waitFor(t, "ended-room approval registration", func() bool {
		_, ok := r.s.pendingIx("ix_ended_room")
		return ok
	})
	if err := r.s.clarify(context.Background(), "ix_ended_room", "Can you answer here?", "owner-ended-room", nil); !isCode(err, CodeMeetingEnded) {
		t.Fatalf("ended-room clarification error = %v", err)
	}
	r.f.mu.Lock()
	var sentQuestion bool
	for _, p := range r.f.sentOnce {
		if p["text"] == "Can you answer here?" {
			sentQuestion = true
		}
	}
	r.f.mu.Unlock()
	if sentQuestion {
		t.Fatal("the ended-room question was sent anyway")
	}
	deny := false
	if err := r.s.resolve("ix_ended_room", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.out.Clarifying {
			t.Fatalf("ended-room Deny = %+v, %v", got.out, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ended-room original tool did not finish on Deny")
	}
}
