package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Right after a message starts a session, claude has not written its transcript yet: the phone's backlog request
// must get an empty chat, not "transcript not found" (which flashed as an error on the chat page). So does every
// session that may not have written its first line yet (startingSession): one that runs (a live agent, an assistant
// process, an external start under way), waits for its first message, has a start under way or one that failed a
// moment ago, or was started within the start bound. A known session that is none of these is not_found
// (TestReadsOfAGoneTranscriptAreNotFound), and so is an unknown id.
func TestItemsOfStartingSession(t *testing.T) {
	n, home := testNode(t)
	long := time.Now().Add(-time.Hour).UTC()
	sessions := map[string]string{
		"just started":                  "11111111-2222-3333-4444-555555555501",
		"waiting for its first message": "11111111-2222-3333-4444-555555555502",
		"a live agent":                  "11111111-2222-3333-4444-555555555503",
		"a running assistant process":   "11111111-2222-3333-4444-555555555504",
		"a start under way":             "11111111-2222-3333-4444-555555555505",
		"a start that just failed":      "11111111-2222-3333-4444-555555555506",
		"an external start under way":   "11111111-2222-3333-4444-555555555507",
	}
	n.state.put(sessions["just started"], sessionState{Cwd: home + "/.persona/assistant", StartedAt: time.Now().UTC()})
	n.state.put(sessions["waiting for its first message"], sessionState{Cwd: home + "/.persona/assistant", StartedAt: long, AwaitingFirstMessage: true})
	n.agents[sessions["a live agent"]] = host.Agent{PaneID: "w1:p1"}
	n.procs[sessions["a running assistant process"]] = &assistantProc{}
	if err := n.starts.add(sessions["a start under way"], &startRec{done: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	failed := &startRec{done: make(chan struct{}), err: errors.New("no terminal"), ended: time.Now()}
	if err := n.starts.add(sessions["a start that just failed"], failed); err != nil {
		t.Fatal(err)
	}
	_, done := n.beginExternalStartup(context.Background(), sessions["an external start under way"])
	defer done()
	for what, id := range sessions {
		res, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+id+`","turns":2}`))
		if err != nil {
			t.Fatalf("%s should not fail: %v", what, err)
		}
		if b, _ := json.Marshal(res); !strings.Contains(string(b), `"items":[]`) {
			t.Fatalf("%s: want an empty page, got %s", what, b)
		}
		res, err = n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+id+`","user_inputs":true}`))
		if b, _ := json.Marshal(res); err != nil || !strings.Contains(string(b), `"inputs":[]`) {
			t.Fatalf("%s: want an empty index, got %s %v", what, b, err)
		}
	}
	_, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"99999999-2222-3333-4444-555555555555"}`))
	if rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundUnknownSession {
		t.Fatalf("an unknown session should still fail, got %v", err)
	}
}

// A start that fails at once (here: no terminal host) can finish before startAsync answers. The answer is the session
// as it was when the start began, never one the start goroutine is updating (go test -race caught the read of the
// record racing finish).
func TestStartAsyncAnswersBeforeAFastFailure(t *testing.T) {
	n, _ := testNode(t)
	n.modes = map[string]sessionModes{}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("11111111-2222-3333-4444-%012d", i)
		s, err := n.startAsync(id, "", "", "", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if s.SessionID != id || !s.Starting || s.Status != proto.StatusIdle || s.StartError != "" {
			t.Fatalf("answered %+v, want the starting session", s)
		}
		// Wait on the record's channel, not through the log: taking the log's lock here would order this read before
		// finish for the race detector and hide the race this test is for. Only this goroutine writes the map.
		r := n.starts.m[id]
		<-r.done
		if r.err == nil {
			t.Fatalf("start %s should fail without a terminal host", id)
		}
	}
}

func TestStartAsyncAllowsExpiredFailedID(t *testing.T) {
	n, _ := testNode(t)
	n.modes = map[string]sessionModes{}
	const id = "11111111-2222-3333-4444-555555555555"
	if _, err := n.startAsync(id, "", "", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	first := n.starts.get(id)
	if first == nil {
		t.Fatal("the first start has no record")
	}
	<-first.done
	if first.err == nil {
		t.Fatal("the first start should fail without a terminal host")
	}
	if _, err := n.startAsync(id, "", "", "", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "already starting") {
		t.Fatalf("an unexpired failed ID must still refuse a duplicate: %v", err)
	}

	n.starts.mu.Lock()
	first.ended = time.Now().Add(-startFailedFor - time.Second)
	n.starts.mu.Unlock()
	if got := n.starts.listed(nil); len(got) != 0 {
		t.Fatalf("the expired failure should no longer be listed: %+v", got)
	}
	s, err := n.startAsync(id, "", "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("the expired failed ID must be retryable: %v", err)
	}
	if s == nil || !s.Starting {
		t.Fatalf("the retry must answer as a new starting session: %+v", s)
	}
	second := n.starts.get(id)
	if second == nil || second == first {
		t.Fatal("the retry must have a distinct start record")
	}
	<-second.done
	if second.err == nil {
		t.Fatal("the retry's worker should run and fail again without a terminal host")
	}
}

// A fixture may only check that an async start typed its command. Its unfinished starts must still stop before the
// node and fake terminal are torn down, not wake three minutes later in another test.
func TestAsyncStartsStopWithTheirNode(t *testing.T) {
	var pending []*startRec
	var stopped *Node
	t.Run("node", func(t *testing.T) {
		n, f, home := herdrNode(t)
		stopped = n
		startHerdr(f, "w1:p2", proto.StatusUnknown)
		for i := 0; i < 2; i++ {
			s, err := n.startAsync("", home, "claude", "", "w1", "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			r := n.starts.get(s.SessionID)
			waitCalls(t, f, "pane.send_input", i+1)
			select {
			case <-r.done:
				t.Fatal("the fake shell should leave this start pending")
			default:
			}
			pending = append(pending, r)
		}
	})
	for _, r := range pending {
		select {
		case <-r.done:
			if !errors.Is(r.err, context.Canceled) {
				t.Fatalf("start ended with %v, want fixture cancellation", r.err)
			}
		default:
			t.Fatal("async start outlived its node's fixture")
		}
	}
	if _, err := stopped.startAsync("", t.TempDir(), "claude", "", "w1", "", "", ""); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("a closed node accepted another start: %v", err)
	}
}

// More sessions that may not have written their first line yet, which a read answers with an empty page and not
// no_transcript (#11450): a thread a Codex app-server has loaded, which has no rollout before its first message; a
// Codex waiting row whose record still names its pane, before a host refresh lists it again after a reconnect.
func TestItemsOfMoreSessionsThatHaveNotWrittenYet(t *testing.T) {
	n, home := testNode(t)
	long := time.Now().Add(-time.Hour).UTC()
	items := func(id string) (any, error) {
		return n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+id+`","turns":2}`))
	}
	empty := func(what, id string) {
		t.Helper()
		res, err := items(id)
		if b, _ := json.Marshal(res); err != nil || !strings.Contains(string(b), `"items":[]`) {
			t.Errorf("%s: want an empty page, got %s %v", what, b, err)
		}
	}
	gone := func(what, id string) {
		t.Helper()
		if _, err := items(id); rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundNoTranscript {
			t.Errorf("%s: want not_found no_transcript, got %v", what, err)
		}
	}

	// A thread a ready app-server has loaded, with no rollout yet.
	n.codices = newCodexRegistry()
	codexHome := canonicalCodexHome(filepath.Join(home, ".codex"), home)
	n.codices.config.LegacyHome = codexHome
	const thread = "019a0000-0000-7000-8000-000000000001"
	if id := n.registerCodex(codexHome, thread, ""); id != thread {
		t.Fatalf("registered as %q", id)
	}
	gone("a Codex thread no app-server has loaded", thread)
	n.codices.remotes = map[string]*codexRemote{"app": {n: n, spec: proto.CodexServer{ID: "app", Home: codexHome}, ready: true,
		threads: map[string]remoteThread{thread: {id: thread, publicID: thread}}}}
	empty("a Codex thread an app-server has loaded", thread)

	// A Codex waiting row long open, which no host refresh has registered again yet; once it left its pane, gone.
	row := codexWaitID()
	n.state.put(row, sessionState{Pane: "w1:p1", Launcher: "codex", Cwd: home, StartedAt: long})
	empty("a Codex waiting row", row)
	n.state.setPane(row, "")
	gone("a Codex waiting row that left its pane", row)
}

// A synchronous start (a Resume always is) keeps the record's first start time and may have no transcript yet, or
// have just lost it to healClaudeRefusalTranscript (a conversation whose only turn was refused): while it runs, a read
// of the session is an empty page; a start that failed takes that back, the heal's removal included (#11450). The fake
// herdr holds the start at its typed line, so the read runs while it is under way whatever the machine's load.
func TestItemsOfASessionWhileItsResumeRuns(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "no transcript", true: "only a refused turn"}[refused], func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "proj"))
			const id = "33333333-2222-3333-4444-555555555555"
			n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, StartedAt: time.Now().Add(-time.Hour).UTC()})
			if refused {
				writeTranscript(t, home, cwd, id, time.Now().Add(-time.Hour), refusalTurnLines("one")...)
			}
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			n.claudeStart, n.claudeReady = 200*time.Millisecond, 200*time.Millisecond // the fake shell never runs claude
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f.after(func(c herdrCall) {
				if c.Method != "pane.send_input" {
					return
				}
				once.Do(func() {
					close(reached)
					select {
					case <-release:
					case <-time.After(10 * time.Second):
					}
				})
			})
			read := func() (any, error) {
				return n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+id+`","turns":2}`))
			}
			done := make(chan error, 1)
			go func() {
				_, err := request(t, n, "session.start", map[string]any{"resume_session_id": id})
				done <- err
			}()
			select {
			case <-reached:
			case <-time.After(10 * time.Second):
				t.Fatal("the Resume never typed its line")
			}
			if refused && n.transcriptPath(id) != "" {
				t.Error("the refused turn's file is still there")
			}
			if res, err := read(); err != nil {
				t.Errorf("while the Resume runs: want an empty page, got %v %v", res, err)
			}
			close(release)
			if err := <-done; err == nil {
				t.Fatal("the Resume should fail without claude")
			}
			if _, err := read(); notFoundReason(err) != proto.NotFoundNoTranscript {
				t.Errorf("after the Resume failed: want not_found no_transcript, got %v", err)
			}
		})
	}
}
