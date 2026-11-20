package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// An explicit review belongs to an existing source Agent. The fake node already
// knows how to provide its real diff, a disposable snapshot and reviewer replies.
const (
	sessionReviewMachine  = "mac"
	sessionReviewSource   = "source"
	sessionReviewSourceWD = "/code/app"
)

const sessionReviewDisplayReport = "1. P0 login/view.swift:11 Landscape still crashes → call layout()\n" +
	"2. P2 login/view.swift:12 The name is unclear → rename it"

const sessionReviewRawReport = sessionReviewDisplayReport + "\n<review-findings count=\"2\" blocking=\"1\"/>"

type sessionReviewFixture struct {
	ft      *fleetTest
	node    *reviewNode
	handler http.Handler
}

func sessionReviewSourceSession(agent string) proto.Session {
	return proto.Session{SessionID: sessionReviewSource, MachineID: sessionReviewMachine, Cwd: sessionReviewSourceWD,
		Agent: agent, Launcher: agent, Status: proto.StatusIdle, Title: "Fix login"}
}

func newSessionReviewFixture(t *testing.T) *sessionReviewFixture {
	t.Helper()
	return newSessionReviewFixtureOn(t, newFleetMem())
}

func newSessionReviewFixtureOn(t *testing.T, st Store) *sessionReviewFixture {
	t.Helper()
	ft := newFleetTestOn(t, st)
	switch mem := st.(type) {
	case *fleetMem:
		ft.st = mem
	case *restoreMem:
		ft.st = mem.fleetMem
	}
	ft.f.mu.Lock()
	ft.f.hello.Locale = "en"
	ft.f.mu.Unlock()
	n := &reviewNode{change: reviewChangeOf()}
	withReviewNode(ft.f, n)
	ft.f.nodes[DeviceSessionSendOnce] = func(_ string, raw json.RawMessage) (any, error) {
		if ft.f.failSend != nil {
			return nil, ft.f.failSend
		}
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		ft.f.agentSent = append(ft.f.agentSent, params)
		return map[string]any{}, nil
	}
	fx := &sessionReviewFixture{ft: ft, node: n, handler: ft.s.fleetHandler(ft.s)}
	fx.setSource(sessionReviewSourceSession("claude"))
	ft.f.setItems(
		proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: "ask-1",
			Text: "Fix the login crash without changing the session API."},
		proto.Item{Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "SOURCE_AGENT_REASONING: I anchored on the layout cache."},
		proto.Item{Seq: 3, Role: "user", Kind: proto.KindText, ClientMsgID: "ask-2",
			Text: "Also add a regression test for landscape."},
		proto.Item{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "SOURCE_AGENT_FINAL: everything is fixed and all tests pass."},
		proto.Item{Seq: 5, Role: "user", Kind: proto.KindText, Text: "INJECTED_NOT_A_USER_ASK",
			Injected: &proto.Injected{Kind: proto.InjectedFollowup}},
		proto.Item{Seq: 6, Role: "user", Kind: proto.KindText, From: "other-agent", Text: "OTHER_AGENT_MESSAGE"},
	)
	return fx
}

func (fx *sessionReviewFixture) setSource(sess proto.Session) {
	fx.ft.d.setSessions(sessionReviewMachine, []proto.Session{sess})
	fx.ft.f.setSessions(sessionReviewMachine, sess)
}

func (fx *sessionReviewFixture) post(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&encoded).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &encoded)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	fx.handler.ServeHTTP(w, req)
	return w
}

func (fx *sessionReviewFixture) requestReview(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return fx.post(t, "/api/v1/sessions/"+sessionReviewMachine+"/"+sessionReviewSource+"/review", nil)
}

// start chooses the review explicitly and waits for its fresh reviewer. The
// source session was already present; every session.start here is a reviewer.
func (fx *sessionReviewFixture) start(t *testing.T) (map[string]any, string) {
	t.Helper()
	before := len(startsOf(fx.ft.f))
	w := fx.requestReview(t)
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("start review: status %d: %s", w.Code, w.Body)
	}
	waitFor(t, "the explicit session reviewer", func() bool { return len(startsOf(fx.ft.f)) == before+1 })
	starts := startsOf(fx.ft.f)
	reviewer := fmt.Sprintf("s%d", fx.ft.f.agentCount())
	return starts[len(starts)-1], reviewer
}

func sessionReviewIDs(list []proto.Session) []string {
	out := make([]string, len(list))
	for i, sess := range list {
		out[i] = sess.SessionID
	}
	return out
}

func sessionReviewCards(s *Service) []proto.Interaction {
	var out []proto.Interaction
	for _, ix := range s.pendingInteractions() {
		if ix.MachineID == sessionReviewMachine && ix.SessionID == sessionReviewSource {
			out = append(out, ix)
		}
	}
	return out
}

func sessionReviewJSON(ix proto.Interaction) string {
	b, _ := json.Marshal(ix)
	return string(b)
}

func sessionReviewOneCard(t *testing.T, s *Service) proto.Interaction {
	t.Helper()
	cards := sessionReviewCards(s)
	if len(cards) != 1 {
		t.Fatalf("source review cards = %+v, want exactly one", cards)
	}
	return cards[0]
}

// finish delivers the reviewer turn end through the same manager entry point as
// a node turn end, then waits for this result (not an older pending result).
func (fx *sessionReviewFixture) finish(t *testing.T, reviewer, raw, displayed string) proto.Interaction {
	t.Helper()
	fx.node.answer(fx.ft.f, reviewer, raw)
	fx.ft.s.TurnEnded(context.Background(), assistantwire.SessionRef{MachineID: sessionReviewMachine, SessionID: reviewer})
	waitFor(t, "the finished session-review card", func() bool {
		for _, ix := range sessionReviewCards(fx.ft.s) {
			if strings.Contains(ix.ReviewReport, displayed) {
				return true
			}
		}
		return false
	})
	return sessionReviewOneCard(t, fx.ft.s)
}

func sessionReviewNestedCount(v any) (int, bool) {
	switch v := v.(type) {
	case map[string]any:
		if n, ok := v["review_count"].(float64); ok {
			return int(n), true
		}
		if n, ok := v["count"].(float64); ok {
			return int(n), true
		}
		for _, child := range v {
			if n, ok := sessionReviewNestedCount(child); ok {
				return n, true
			}
		}
	case []any:
		for _, child := range v {
			if n, ok := sessionReviewNestedCount(child); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func sessionReviewAssertCard(t *testing.T, ix proto.Interaction, displayed string, count int) {
	t.Helper()
	raw := sessionReviewJSON(ix)
	if !strings.Contains(ix.ReviewReport, displayed) {
		t.Fatalf("the card does not carry the displayed report %q: %s", displayed, raw)
	}
	if strings.Contains(raw, "review-findings") {
		t.Fatalf("the machine-readable marker leaked into the card: %s", raw)
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	got, ok := sessionReviewNestedCount(decoded)
	if !ok || got != count {
		t.Fatalf("card findings count = %d, %v; want %d: %s", got, ok, count, raw)
	}
}

func sessionReviewSendResolution(t *testing.T, ix proto.Interaction) proto.Resolution {
	t.Helper()
	if len(ix.Questions) != 1 {
		t.Fatalf("review card questions = %+v, want one Send / Dismiss question", ix.Questions)
	}
	q := ix.Questions[0]
	for _, option := range q.Options {
		action := strings.ToLower(option.Action)
		label := strings.ToLower(option.Label)
		if action == "send" || action == "send_to_agent" || strings.Contains(label, "send to agent") {
			return proto.Resolution{Answers: map[string]string{q.Question: option.Label}}
		}
	}
	t.Fatalf("review card has no Send to agent choice: %+v", q.Options)
	return proto.Resolution{}
}

func (fx *sessionReviewFixture) resolve(t *testing.T, ix proto.Interaction, resolution proto.Resolution) *httptest.ResponseRecorder {
	t.Helper()
	return fx.post(t, "/api/v1/interactions/"+url.PathEscape(ix.ID)+"/resolve", resolution)
}

func sessionReviewSuccessfulSends(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.agentSent)
}

func sessionReviewSendAttempts(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, call := range f.calls {
		if call.Method != DeviceSessionSend && call.Method != DeviceSessionSendOnce {
			continue
		}
		var params map[string]any
		_ = json.Unmarshal(call.Params, &params)
		params["machine"] = call.Device
		params["method"] = call.Method
		out = append(out, params)
	}
	return out
}

func TestSessionReviewStartIsPlanModeInDisposableSnapshot(t *testing.T) {
	fx := newSessionReviewFixture(t)
	if len(startsOf(fx.ft.f)) != 0 || len(fx.node.snapshots) != 0 {
		t.Fatal("a review started before the user explicitly chose it")
	}
	start, reviewer := fx.start(t)
	if reviewer == sessionReviewSource || start["session_id"] == sessionReviewSource || start["resume_only"] == true {
		t.Fatalf("the reviewer is not a fresh session: reviewer %q, start %v", reviewer, start)
	}
	if start["launcher"] != "claude" || start["permission_mode"] != "plan" {
		t.Fatalf("reviewer start does not enforce Claude Code plan mode: %v", start)
	}
	if start["cwd"] != reviewCopy {
		t.Fatalf("reviewer cwd = %v, want the disposable snapshot %q", start["cwd"], reviewCopy)
	}
	if len(fx.node.snapshots) != 1 || fx.node.snapshots[0]["cwd"] != sessionReviewSourceWD {
		t.Fatalf("snapshot calls = %v, want one of the source directory", fx.node.snapshots)
	}
	fx.ft.f.mu.Lock()
	defer fx.ft.f.mu.Unlock()
	snapshotBeforeStart := false
	for _, call := range fx.ft.f.calls {
		if call.Method == "state.put" {
			t.Fatalf("review identity or cleanup relied on unacknowledged state.put: %+v", call)
		}
		if call.Method == "worktree.snapshot" {
			snapshotBeforeStart = true
		}
		if call.Method == DeviceSessionStart && !snapshotBeforeStart {
			t.Fatal("the reviewer started before its disposable snapshot existed")
		}
	}
}

func TestSessionReviewRefusesLaunchersWithoutReadOnlyStart(t *testing.T) {
	for _, launcher := range []string{"codex", "other"} {
		t.Run(launcher, func(t *testing.T) {
			fx := newSessionReviewFixture(t)
			fx.setSource(sessionReviewSourceSession(launcher))
			w := fx.requestReview(t)
			if w.Code != http.StatusBadRequest && w.Code != http.StatusConflict && w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("unsupported launcher refusal status = %d: %s", w.Code, w.Body)
			}
			reason := strings.ToLower(w.Body.String())
			if !strings.Contains(reason, "read-only") && !strings.Contains(reason, "read only") && !strings.Contains(reason, "plan mode") {
				t.Fatalf("refusal does not explain the enforced read-only requirement: %s", w.Body)
			}
			if len(startsOf(fx.ft.f)) != 0 {
				t.Fatalf("a reviewer started for unsupported launcher %q: %v", launcher, startsOf(fx.ft.f))
			}
			if len(fx.node.snapshots) != 0 {
				t.Fatalf("the unsupported launcher got as far as making a snapshot: %v", fx.node.snapshots)
			}
		})
	}
}

func TestSessionReviewSeedContainsOnlyUserAsksAndRealDiff(t *testing.T) {
	fx := newSessionReviewFixture(t)
	start, _ := fx.start(t)
	prompt, _ := start["prompt"].(string)
	for _, want := range []string{
		"Fix the login crash without changing the session API.",
		"Also add a regression test for landscape.",
		"login/view.swift", "+if landscape { layout() }", "0a1b2c3",
		"P0", "path:line", "fix suggestion", `<review-findings count="N" blocking="M"/>`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("reviewer seed lacks %q:\n%s", want, prompt)
		}
	}
	for _, never := range []string{"SOURCE_AGENT_REASONING", "SOURCE_AGENT_FINAL", "INJECTED_NOT_A_USER_ASK", "OTHER_AGENT_MESSAGE"} {
		if strings.Contains(prompt, never) {
			t.Errorf("reviewer seed contains %q:\n%s", never, prompt)
		}
	}
}

func TestSessionReviewFindingsCountParsing(t *testing.T) {
	for _, tc := range []struct {
		name, raw, displayed string
		count                int
	}{
		{"quoted", "Finding\n<review-findings count=\"2\" blocking=\"1\"/>", "Finding", 2},
		{"unquoted and whitespace", "Finding\n<review-findings count=3 blocking = 0 />", "Finding", 3},
		{"last marker wins and all are hidden", "A <review-findings count=\"3\" blocking=\"1\"/>\nB\n<review-findings count=\"1\" blocking=\"0\"/>", "A \nB", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			displayed, count, _, ok := parseReviewFindings(tc.raw)
			if !ok || displayed != tc.displayed || count != tc.count {
				t.Fatalf("parseReviewFindings(%q) = %q, %d, %v; want %q, %d, true", tc.raw, displayed, count, ok, tc.displayed, tc.count)
			}
		})
	}
}

func TestSessionReviewRunningIsUniqueAndReviewerHidden(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	if w := fx.requestReview(t); w.Code != http.StatusConflict {
		t.Fatalf("second concurrent review status = %d, want conflict: %s", w.Code, w.Body)
	}
	if len(startsOf(fx.ft.f)) != 1 || len(fx.node.snapshots) != 1 {
		t.Fatalf("a second review started: starts %v, snapshots %v", startsOf(fx.ft.f), fx.node.snapshots)
	}

	source := sessionReviewSourceSession("claude")
	reviewerSession := proto.Session{SessionID: reviewer, MachineID: sessionReviewMachine, Cwd: reviewCopy,
		Agent: "claude", Status: proto.StatusWorking}
	fx.ft.sessions(sessionReviewMachine, source, reviewerSession)
	if got := sessionReviewIDs(fx.ft.s.publishList(sessionReviewMachine)); !slices.Equal(got, []string{sessionReviewSource}) {
		t.Fatalf("Agent list includes the independent reviewer: %v", got)
	}
	if got := sessionReviewIDs(fx.ft.s.decoratedUnarchived(sessionReviewMachine)); !slices.Equal(slices.Sorted(slices.Values(got)), []string{reviewer, sessionReviewSource}) {
		t.Fatalf("the manager lost the reviewer from its own node inventory: %v", got)
	}
}

func TestSessionReviewFinishedCardSurvivesManagerReconstruction(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	finished := fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	sessionReviewAssertCard(t, finished, sessionReviewDisplayReport, 2)
	waitFor(t, "the finished reviewer and snapshot to be cleaned", func() bool {
		stopped := fx.node.stoppedOf(fx.ft.f)
		_, removed := fx.node.cleaned(fx.ft.f)
		return slices.Contains(stopped, reviewer) && slices.ContainsFunc(removed, func(call map[string]any) bool {
			return call["path"] == reviewCopy && call["force"] == true
		})
	})
	fx.ft.cancel() // the first manager goes away; Postgres (the shared fake store) remains

	store := &restoreMem{fleetMem: fx.ft.st}
	again := newSessionReviewFixtureOn(t, store)
	again.ft.s.FleetUp(context.Background())
	restored := sessionReviewOneCard(t, again.ft.s)
	if restored.ID != finished.ID {
		t.Fatalf("reconstruction replaced the finished card: before %q, after %q", finished.ID, restored.ID)
	}
	sessionReviewAssertCard(t, restored, sessionReviewDisplayReport, 2)
	if len(startsOf(again.ft.f)) != 0 {
		t.Fatal("reconstructing the finished result started another reviewer")
	}
	if w := again.resolve(t, restored, proto.Resolution{Dismiss: true}); w.Code < 200 || w.Code >= 300 {
		t.Fatalf("the restored card is not answerable: %d %s", w.Code, w.Body)
	}
	if got := sessionReviewCards(again.ft.s); len(got) != 0 || len(sessionReviewSuccessfulSends(again.ft.f)) != 0 {
		t.Fatalf("dismiss after reconstruction: cards %+v, sends %v", got, sessionReviewSuccessfulSends(again.ft.f))
	}
}

func TestSessionReviewNewerResultReplacesOlderPendingCard(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, firstReviewer := fx.start(t)
	firstDisplayed := "FIRST_RESULT: add the missing landscape test"
	first := fx.finish(t, firstReviewer, firstDisplayed+"\n<review-findings count=\"1\" blocking=\"0\"/>", firstDisplayed)

	_, secondReviewer := fx.start(t) // a finished card does not block a new explicit review
	secondDisplayed := "SECOND_RESULT: landscape still uses the old layout path"
	second := fx.finish(t, secondReviewer, secondDisplayed+"\n<review-findings count=\"1\" blocking=\"1\"/>", secondDisplayed)
	if second.ID == first.ID {
		t.Fatalf("the newer result reused the old pending card %q", first.ID)
	}
	sessionReviewAssertCard(t, second, secondDisplayed, 1)
	if raw := sessionReviewJSON(second); strings.Contains(raw, firstDisplayed) {
		t.Fatalf("the older pending result survived replacement: %s", raw)
	}
	if got := sessionReviewSuccessfulSends(fx.ft.f); len(got) != 0 {
		t.Fatalf("publishing or replacing a result messaged an agent: %v", got)
	}
}

func TestSessionReviewSendForwardsReportToSourceExactlyOnce(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	card := fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	resolution := sessionReviewSendResolution(t, card)
	if w := fx.resolve(t, card, resolution); w.Code < 200 || w.Code >= 300 {
		t.Fatalf("Send to agent: status %d: %s", w.Code, w.Body)
	}
	sent := sessionReviewSuccessfulSends(fx.ft.f)
	if len(sent) != 1 || sent[0]["session_id"] != sessionReviewSource {
		t.Fatalf("Send delivered %+v, want one normal session.send to the source", sent)
	}
	text, _ := sent[0]["text"].(string)
	if !strings.Contains(text, sessionReviewDisplayReport) || strings.Contains(text, "review-findings") {
		t.Fatalf("forwarded text is not the displayed report:\n%s", text)
	}
	if len(sessionReviewCards(fx.ft.s)) != 0 {
		t.Fatal("the successfully sent review card is still pending")
	}
	// A repeated callback cannot deliver the same findings again.
	if w := fx.resolve(t, card, resolution); w.Code >= 500 {
		t.Fatalf("repeated Send callback: status %d: %s", w.Code, w.Body)
	}
	if got := sessionReviewSuccessfulSends(fx.ft.f); len(got) != 1 || len(sessionReviewSendAttempts(fx.ft.f)) != 1 {
		t.Fatalf("Send was not exactly once: successful %v, attempts %v", got, sessionReviewSendAttempts(fx.ft.f))
	}
}

func TestSessionReviewDismissDoesNotMessageEitherAgent(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	card := fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	if w := fx.resolve(t, card, proto.Resolution{Dismiss: true}); w.Code < 200 || w.Code >= 300 {
		t.Fatalf("Dismiss: status %d: %s", w.Code, w.Body)
	}
	if got := sessionReviewCards(fx.ft.s); len(got) != 0 {
		t.Fatalf("the dismissed card is still pending: %+v", got)
	}
	if got := sessionReviewSendAttempts(fx.ft.f); len(got) != 0 {
		t.Fatalf("Dismiss messaged the source or reviewer %q: %v", reviewer, got)
	}
	if status, _, err := fx.ft.st.InteractionOutcome(context.Background(), card.ID); err != nil || status != "dismissed" {
		t.Fatalf("dismissed card history = %q, %v", status, err)
	}
}

func TestSessionReviewFailedSendKeepsCardAnswerable(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	card := fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	resolution := sessionReviewSendResolution(t, card)
	fx.ft.f.mu.Lock()
	fx.ft.f.failSend = errors.New("source node offline")
	fx.ft.f.mu.Unlock()
	if w := fx.resolve(t, card, resolution); w.Code >= 200 && w.Code < 300 {
		t.Fatalf("failed Send was acknowledged: %d %s", w.Code, w.Body)
	}
	still := sessionReviewOneCard(t, fx.ft.s)
	if still.ID != card.ID || len(sessionReviewSuccessfulSends(fx.ft.f)) != 0 || len(sessionReviewSendAttempts(fx.ft.f)) != 1 {
		t.Fatalf("failed Send changed the card or delivery ledger: card %+v, sends %v, attempts %v",
			still, sessionReviewSuccessfulSends(fx.ft.f), sessionReviewSendAttempts(fx.ft.f))
	}

	fx.ft.f.mu.Lock()
	fx.ft.f.failSend = nil
	fx.ft.f.mu.Unlock()
	if w := fx.resolve(t, still, resolution); w.Code < 200 || w.Code >= 300 {
		t.Fatalf("retry after failed Send: status %d: %s", w.Code, w.Body)
	}
	sent := sessionReviewSuccessfulSends(fx.ft.f)
	if len(sent) != 1 || sent[0]["session_id"] != sessionReviewSource || len(sessionReviewCards(fx.ft.s)) != 0 {
		t.Fatalf("retry did not deliver once and resolve the card: sends %v, cards %+v", sent, sessionReviewCards(fx.ft.s))
	}
	attempts := sessionReviewSendAttempts(fx.ft.f)
	if len(attempts) != 2 {
		t.Fatalf("send attempts = %v, want the failure and its one retry", attempts)
	}
	for _, attempt := range attempts {
		if attempt["session_id"] != sessionReviewSource || attempt["session_id"] == reviewer {
			t.Fatalf("card action messaged the reviewer instead of the source: %v", attempt)
		}
	}
}

func TestSessionReviewLateFinishDoesNotReplacePending(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, firstReviewer := fx.start(t)
	firstDisplayed := "FIRST_VALID_RESULT"
	first := fx.finish(t, firstReviewer, firstDisplayed+"\n<review-findings count=\"1\" blocking=\"0\"/>", firstDisplayed)
	_, secondReviewer := fx.start(t)
	reviews, err := fx.ft.s.sessionReviewRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	running, ok := sessionReviewRunningOn(reviews, sessionReviewMachine, sessionReviewSource)
	if !ok || running.ReviewerID != secondReviewer {
		t.Fatalf("second review is not running: %+v", running)
	}
	fx.ft.s.failSessionReview(context.Background(), running, "timeout")
	fx.ft.s.finishSessionReview(context.Background(), running, "LATE_EXPIRED_RESULT\n<review-findings count=\"1\" blocking=\"0\"/>")
	still := sessionReviewOneCard(t, fx.ft.s)
	if still.ID != first.ID || still.ReviewReport != firstDisplayed {
		t.Fatalf("a late expired result replaced the valid pending card: %+v", still)
	}
}

func TestSessionReviewPersistenceFailureCleansReviewerAndSnapshot(t *testing.T) {
	for _, stage := range []string{"worktree", "reviewer"} {
		t.Run(stage, func(t *testing.T) {
			fx := newSessionReviewFixture(t)
			if stage == "worktree" {
				fx.node.onSnapshot = func() { fx.ft.st.memStore.failPut = errors.New("snapshot state write failed") }
			} else {
				fx.ft.f.onStart = func(_ *fakeHub, _ map[string]any) {
					fx.ft.st.memStore.failPut = errors.New("reviewer state write failed")
				}
			}
			if w := fx.requestReview(t); w.Code != http.StatusServiceUnavailable {
				t.Fatalf("persistence failure status = %d: %s", w.Code, w.Body)
			}
			fx.ft.st.memStore.failPut = nil
			fx.ft.s.sessionReviewTick(context.Background(), time.Now())
			_, removed := fx.node.cleaned(fx.ft.f)
			if !slices.ContainsFunc(removed, func(call map[string]any) bool { return call["path"] == reviewCopy && call["force"] == true }) {
				t.Logf("fake calls: %+v", fx.ft.f.calls)
				t.Fatalf("snapshot leaked after %s persistence failure: %v", stage, removed)
			}
		})
	}
}

func TestSessionReviewStopFailureIsRetried(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	fx.node.stopErr = errors.New("transient stop failure")
	_ = fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	if got := fx.node.stoppedOf(fx.ft.f); len(got) != 1 {
		t.Fatalf("stop attempts = %v, want the first one", got)
	}
	fx.node.stopErr = nil
	fx.ft.s.sessionReviewTick(context.Background(), time.Now())
	if got := fx.node.stoppedOf(fx.ft.f); len(got) != 2 {
		t.Fatalf("stop was not retried after the transient failure: %v", got)
	}
}

func TestSessionReviewSendUsesDurableOnceGuard(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	card := fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	if w := fx.resolve(t, card, sessionReviewSendResolution(t, card)); w.Code < 200 || w.Code >= 300 {
		t.Fatalf("Send to agent: %d: %s", w.Code, w.Body)
	}
	attempts := sessionReviewSendAttempts(fx.ft.f)
	if len(attempts) != 1 || attempts[0]["method"] != DeviceSessionSendOnce {
		t.Fatalf("ordinary send does not use the durable once-only guard: %v", attempts)
	}
}

func TestSessionReviewResumeStartsWithoutPromptThenSendsOnce(t *testing.T) {
	fx := newSessionReviewFixture(t)
	source := sessionReviewSourceSession("claude")
	source.Status = proto.StatusOffline
	source.Capabilities = &proto.SessionCapabilities{Send: false}
	fx.setSource(source)
	_, reviewer := fx.start(t)
	card := fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	fx.ft.f.onStart = func(_ *fakeHub, params map[string]any) {
		if params["cwd"] == nil {
			params["cwd"] = sessionReviewSourceWD
		}
	}
	if w := fx.resolve(t, card, sessionReviewSendResolution(t, card)); w.Code < 200 || w.Code >= 300 {
		t.Fatalf("Send to stopped source: %d: %s", w.Code, w.Body)
	}
	starts := startsOf(fx.ft.f)
	resume := starts[len(starts)-1]
	if resume["resume_session_id"] != sessionReviewSource || resume["prompt"] != nil && resume["prompt"] != "" {
		t.Fatalf("resume carries the findings prompt, which an already-running source would drop: %v", resume)
	}
	attempts := sessionReviewSendAttempts(fx.ft.f)
	if len(attempts) != 1 || attempts[0]["method"] != DeviceSessionSendOnce || !strings.Contains(attempts[0]["text"].(string), sessionReviewDisplayReport) {
		t.Fatalf("resume did not finish with one guarded findings message: %v", attempts)
	}
}

func TestSessionReviewReadFailureKeepsPendingCard(t *testing.T) {
	fx := newSessionReviewFixture(t)
	_, reviewer := fx.start(t)
	finished := fx.finish(t, reviewer, sessionReviewRawReport, sessionReviewDisplayReport)
	fx.ft.s.sessionReviews.loaded = false
	fx.ft.s.store = func() Store {
		return &settingFailing{fleetMem: fx.ft.st, fail: map[string]bool{sessionReviewSetting: true}}
	}
	still := sessionReviewOneCard(t, fx.ft.s)
	if still.ID != finished.ID || still.ReviewReport != sessionReviewDisplayReport {
		t.Fatalf("a store read failure withdrew the pending card: %+v", still)
	}
}
