package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// notFoundReason is the reason a read's not_found gives (data.reason), "" for none.
func notFoundReason(err error) string {
	var re *rpc.Error
	if !errors.As(err, &re) || len(re.Data) == 0 {
		return ""
	}
	var data struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(re.Data, &data)
	return data.Reason
}

// A read of a session this machine has no transcript for (an unknown id: a stale link, a client that sent the wrong
// field) answers not_found with the words it always had, which the hub serves as 404, not 502 internal (#9551), and
// says why: the node has no record of it (#11388). The other callers of file keep the plain error: the manager takes a
// not_found send as "the session is gone" and withdraws its card, which a missing file alone must not do.
func TestReadsOfAnUnknownSessionAreNotFound(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, paths: map[string]string{}, files: map[string]*transcript.File{}, procs: map[string]*assistantProc{},
		agents: map[string]host.Agent{}, state: &stateFile{Sessions: map[string]sessionState{}}}
	const unknown = "99999999-2222-3333-4444-555555555555"
	const words = "transcript for session " + unknown + " not found"
	for _, method := range []string{"session.items", "session.item", "session.image", "session.visualization"} {
		_, err := n.handle(context.Background(), method, json.RawMessage(`{"session_id":"`+unknown+`","item_id":"x","turns":2}`))
		if rpcCode(err) != codeNotFound || err.Error() != words || notFoundReason(err) != proto.NotFoundUnknownSession {
			t.Errorf("%s: want not_found %q (unknown_session), got code %q reason %q: %v", method, words, rpcCode(err), notFoundReason(err), err)
		}
	}
	if _, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+unknown+`","user_inputs":true}`)); rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundUnknownSession {
		t.Errorf("user_inputs: want not_found (unknown_session), got code %q: %v", rpcCode(err), err)
	}
	// A subagent of a session with no transcript is not there either, for its session's reason.
	if _, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+unknown+subagentSep+`a1"}`)); rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundUnknownSession {
		t.Errorf("subagent: want not_found (unknown_session), got code %q: %v", rpcCode(err), err)
	}
	if _, err := n.file(unknown); err == nil || rpcCode(err) != "" || err.Error() != words {
		t.Errorf("file: want the plain error %q, got code %q: %v", words, rpcCode(err), err)
	}

	// While the node is still loading its records from the hub, an id missing from them may still be Persona's: no
	// reason is claimed.
	n.state.beginLoad()
	_, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+unknown+`","turns":2}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != codeNotFound || err.Error() != words || len(re.Data) != 0 {
		t.Errorf("loading: want not_found %q without data, got %v", words, err)
	}
	n.state.endLoad()
}

// A session the node knows (here one Persona started an hour ago, and an assistant conversation) whose transcript is
// gone, or that never got a message, is not_found with the reason no_transcript, never an empty page that shows an
// empty chat as if nothing had been said (#11388). The message is the one the node has always given, and the reason
// is all the data: a read delivers nothing, so it says nothing of delivery.
func TestReadsOfAGoneTranscriptAreNotFound(t *testing.T) {
	n, home := testNode(t)
	long := time.Now().Add(-time.Hour).UTC()
	const agent, chat = "11111111-2222-3333-4444-555555555555", "22222222-2222-3333-4444-555555555555"
	n.state.put(agent, sessionState{Launcher: "claude", Cwd: home, StartedAt: long})
	n.state.put(chat, sessionState{Launcher: "claude", Cwd: home + "/.persona/assistant", StartedAt: long})
	for _, id := range []string{agent, chat} {
		words := "transcript for session " + id + " not found"
		for _, call := range []struct{ method, params string }{
			{"session.items", `{"session_id":"` + id + `","turns":2}`},
			{"session.items", `{"session_id":"` + id + `","limit":80}`},
			{"session.items", `{"session_id":"` + id + `","after_seq":40}`},
			{"session.items", `{"session_id":"` + id + `","user_inputs":true}`},
			{"session.item", `{"session_id":"` + id + `","item_id":"x"}`},
			{"session.image", `{"session_id":"` + id + `","item_id":"x"}`},
			{"session.visualization", `{"session_id":"` + id + `","item_id":"x"}`},
			{"session.items", `{"session_id":"` + id + subagentSep + `a1","turns":2}`}, // its subagent: its reason
		} {
			res, err := n.handle(context.Background(), call.method, json.RawMessage(call.params))
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != codeNotFound || re.Message != words || string(re.Data) != `{"reason":"no_transcript"}` {
				t.Errorf("%s %s: want not_found %q {reason: no_transcript}, got %v %v", call.method, call.params, words, res, err)
			}
		}
	}
	// Its watch still waits for the transcript, as for a session that has just started (pending_watch_test.go).
	if err := n.watch(agent, 0); err != nil {
		t.Errorf("watch: %v", err)
	}
	n.unwatch(agent)
}

// A pi-family fallback id is runtime-shaped too, but the node's own record says this is no runtime conversation whose
// history may come back. If its extension never reported a session id and no transcript was written, it is known and gone.
func TestReadsOfANodeOwnedRuntimeShapedIDAreNotFound(t *testing.T) {
	n, home := testNode(t)
	const id = "omp-w1-p2"
	n.state.put(id, sessionState{Launcher: "omp", Cwd: home, StartedAt: time.Now().Add(-time.Hour).UTC()})
	_, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+id+`","turns":2}`))
	if rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundNoTranscript {
		t.Fatalf("a recorded node-owned fallback id: want not_found no_transcript, got code %q reason %q: %v", rpcCode(err), notFoundReason(err), err)
	}
}

// Reasons a read gives no more (#11450): a conversation an Agent runtime runs, whose history is the runtime's and comes
// back with it, while the runtime is not connected (a node that restarted with the runtime's app closed knows nothing of
// it but its record); and unknown_session after a load of the node's records failed and is tried again, with none of
// them. Once they have come, an unknown id is unknown_session.
func TestReadsGiveNoReasonTheyCannotKnow(t *testing.T) {
	n, home := testNode(t)
	const words = "transcript for session %s not found"
	read := func(id string) error {
		_, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+id+`","turns":2}`))
		return err
	}
	const runtime = "session-11111111-2222-3333-4444-555555555555" // a runtime's own id (DSH)
	n.state.put(runtime, sessionState{Launcher: "dsh", Cwd: home + "/.persona/assistant", StartedAt: time.Now().Add(-time.Hour).UTC()})
	if err := read(runtime); rpcCode(err) != codeNotFound || err.Error() != fmt.Sprintf(words, runtime) || notFoundReason(err) != "" {
		t.Errorf("a runtime's conversation while it is away: want not_found without a reason, got %q %v", notFoundReason(err), err)
	}
	if err := read(runtime + subagentSep + "a1"); rpcCode(err) != codeNotFound || notFoundReason(err) != "" {
		t.Errorf("a runtime's subagent: want not_found without a reason, got %q %v", notFoundReason(err), err)
	}

	const unknown = "99999999-2222-3333-4444-555555555555"
	n.stateReady = make(chan struct{}) // the first load failed (loading is over) and is tried again
	if err := read(unknown); rpcCode(err) != codeNotFound || notFoundReason(err) != "" {
		t.Errorf("while the records are tried again: want not_found without a reason, got %q %v", notFoundReason(err), err)
	}
	close(n.stateReady)
	if err := read(unknown); notFoundReason(err) != proto.NotFoundUnknownSession {
		t.Errorf("once the records came: want unknown_session, got %q %v", notFoundReason(err), err)
	}
}

// An older page (before_seq) of a session the node knows (or of its subagent), with no transcript, ends paging with an
// empty page and has_more false, as before #11388: released apps ask an older page again on an error. The latest page
// says why there is none, and an unknown id is not_found either way (#11450).
func TestAnOlderPageOfAGoneTranscriptEndsPaging(t *testing.T) {
	n, home := testNode(t)
	const known, unknown = "11111111-2222-3333-4444-555555555555", "99999999-2222-3333-4444-555555555555"
	n.state.put(known, sessionState{Launcher: "claude", Cwd: home, StartedAt: time.Now().Add(-time.Hour).UTC()})
	page := func(id, params string) (any, error) {
		return n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+id+`",`+params+`}`))
	}
	for _, params := range []string{`"before_seq":40,"turns":2`, `"before_seq":40,"limit":80`} {
		for _, id := range []string{known, known + subagentSep + "a1"} { // the session's chat and its subagent's
			res, err := page(id, params)
			if b, _ := json.Marshal(res); err != nil || string(b) != `{"has_more":false,"items":[]}` {
				t.Errorf("%s of %s: want an empty last page, got %s %v", params, id, b, err)
			}
		}
		if _, err := page(unknown, params); notFoundReason(err) != proto.NotFoundUnknownSession {
			t.Errorf("%s of an unknown id: want unknown_session, got %v", params, err)
		}
	}
	if _, err := page(known, `"turns":2`); notFoundReason(err) != proto.NotFoundNoTranscript {
		t.Errorf("the latest page: want no_transcript, got %v", err)
	}
}
