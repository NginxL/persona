package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/rpc"
)

func TestPromptFilesBindExecutionAndFailBeforeDelivery(t *testing.T) {
	ctx := context.WithValue(t.Context(), taskInputKey{}, true)
	f := cliTestBash(t)
	f.n.machineID = "box"
	cwd := t.TempDir()
	file := filepath.Join(cwd, "notes.txt")
	if err := os.WriteFile(file, []byte("current content"), 0600); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf(`<persona-context kind="file" path="notes.txt" machine="%s" cwd="%s" launcher="claude" runtime="%s">Notes</persona-context>`+"\n\nRead it", f.n.machineID, cwd, "")
	if _, err := f.n.selectedFiles(t.Context(), text, cwd, "claude", ""); err == nil {
		t.Fatal("unmarked conversation read a task selection")
	}
	outside := filepath.Join(t.TempDir(), "out.txt")
	writeFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(cwd, "outside-link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, "outside-link"} {
		if _, err := f.n.selectedFiles(ctx, strings.Replace(text, "notes.txt", path, 1), cwd, "claude", ""); err == nil {
			t.Fatal("selection crossed task directory")
		}
	}
	entries, err := nodeCall(t, f.n, "fs.task.list", map[string]string{"cwd": cwd, "path": "."})
	if err != nil || !strings.Contains(string(entries), "notes.txt") || strings.Contains(string(entries), "outside-link") {
		t.Fatalf("task picker: %s %v", entries, err)
	}
	if _, err := nodeCall(t, f.n, "fs.task.list", map[string]string{"cwd": cwd, "path": filepath.Dir(outside)}); err == nil {
		t.Fatal("outside directory listed")
	}
	physical, _ := filepath.EvalSymlinks(file)
	selected, err := f.n.selectedFiles(ctx, text, cwd, "claude", "")
	if err != nil || len(selected) != 1 || selected[0].Path != physical {
		t.Fatalf("resolve: %+v %v", selected, err)
	}
	for _, changed := range []string{strings.Replace(text, `launcher="claude"`, `launcher="other"`, 1), strings.Replace(text, `cwd="`+cwd+`"`, `cwd="/another"`, 1), strings.Replace(text, `machine="`+f.n.machineID+`"`, `machine="other"`, 1)} {
		if _, err := f.n.assistantSend(ctx, "", cwd, "claude", changed, nil, "default", "", modelExecution{}); err == nil {
			t.Fatal("stale selection delivered")
		}
	}
	wire := map[string]any{"text": text, "task_input": true, "cwd": cwd, "launcher": "claude", "permission_mode": "default", "origin": "task", "origin_known": true}
	reply, err := nodeCall(t, f.n, "assistant.send", wire)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal(reply, &started); err != nil || started.ID == "" {
		t.Fatalf("start: %s %v", reply, err)
	}
	id := started.ID
	f.idle(t, id)
	if got := f.file("stdin.jsonl"); !strings.Contains(got, "file-instructions") || !strings.Contains(got, physical) {
		t.Fatalf("file not injected: %s", got)
	}
	// Content may change between runs; the reference stays at the same path, without storing a snapshot.
	if err := os.WriteFile(file, []byte("updated content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.selectedFiles(ctx, text, cwd, "claude", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	before := f.file("stdin.jsonl")
	if _, err := f.n.assistantSend(ctx, id, cwd, "claude", text, nil, "default", "", modelExecution{}); err == nil {
		t.Fatal("deleted file delivered")
	}
	if f.file("stdin.jsonl") != before {
		t.Fatal("sent after file refusal")
	}
}

func TestAssistantContentMatchesFileAudienceRefusals(t *testing.T) {
	f := cliTestBash(t)
	f.n.machineID = "fixture"
	cwd := resolved(t, t.TempDir())
	writeFile(t, filepath.Join(cwd, "notes.txt"), "Synthetic notes")
	selection := fmt.Sprintf(`<persona-context kind="file" path="notes.txt" machine="%s" cwd="%s" launcher="claude">Notes</persona-context>`, f.n.machineID, cwd)
	for _, tc := range []struct {
		name                         string
		guest                        []string
		rawOrigin, storedOrigin      string
		originKnown, public, refused bool
	}{
		{name: "ordinary"},
		{name: "guest", guest: []string{"lark_agent_reply"}, refused: true},
		{name: "public input", public: true, refused: true},
		{name: "persisted Inbox", storedOrigin: originInbox, refused: true},
		{name: "manager-attested Inbox", rawOrigin: originInbox, originKnown: true, refused: true},
		{name: "persisted Inbox beats an unattested origin", rawOrigin: "task", storedOrigin: originInbox, refused: true},
		{name: "manager reattested ordinary", storedOrigin: originInbox, originKnown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := newUUID()
			f.n.state.put(id, sessionState{Cwd: cwd, Launcher: "claude", Guest: tc.guest, Origin: tc.storedOrigin, OriginKnown: true})
			params := map[string]any{"session_id": id, "cwd": cwd, "launcher": "claude", "text": selection,
				"origin": tc.rawOrigin, "origin_known": tc.originKnown, "public": tc.public, "task_execution": true, "task_input": true}
			_, checkErr := nodeCall(t, f.n, "assistant.content", params)
			if tc.refused {
				_, sendErr := nodeCall(t, f.n, "assistant.send", params)
				check, send := assertFileRefusalDelivery(t, checkErr), assertFileRefusalDelivery(t, sendErr)
				if check.Message != send.Message {
					t.Errorf("content error %v does not match send error %v", checkErr, sendErr)
				}
			} else if checkErr != nil {
				t.Errorf("ordinary selection refused: %v", checkErr)
			}
			for _, text := range []string{"", "Review ordinary text"} {
				params["text"] = text
				if _, err := nodeCall(t, f.n, "assistant.content", params); err != nil {
					t.Errorf("content check rejected input without file references: %q: %v", text, err)
				}
			}
		})
	}
}

func TestPromptFileRefusalKeepsWarmSpareAndConversationState(t *testing.T) {
	f := cliTestBash(t)
	n := f.n
	n.machineID = "fixture"
	cwd := resolved(t, t.TempDir())
	warm := map[string]any{"cwd": cwd, "launcher": "claude", "model_fingerprint": "file-refusal-spare"}
	if _, err := nodeCall(t, n, "assistant.warm", warm); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	spare := n.spare
	n.mu.Unlock()
	if spare == nil {
		t.Fatal("missing warm spare")
	}

	selection := fmt.Sprintf(`<persona-context kind="file" path="notes.txt" machine="%s" cwd="%s" launcher="claude">Notes</persona-context>`, n.machineID, cwd)
	send := map[string]any{
		"cwd": cwd, "launcher": "claude", "model_fingerprint": warm["model_fingerprint"], "permission_mode": "default",
		"task_input": true, "origin": "task", "origin_known": true, "text": selection,
	}
	_, err := nodeCall(t, n, "assistant.send", send)
	assertFileRefusalDelivery(t, err)
	n.mu.Lock()
	stillSpare := n.spare == spare
	n.mu.Unlock()
	if !stillSpare || !spare.alive() || f.proc(spare.id) != nil {
		t.Fatal("rejected send moved the warmed process into the conversation registry")
	}
	if _, ok := n.state.get(spare.id); ok {
		t.Fatal("rejected send persisted the spare as a conversation")
	}

	coldID := newUUID()
	send["new_session_id"] = coldID
	_, err = nodeCall(t, n, "assistant.send", send)
	assertFileRefusalDelivery(t, err)
	if _, ok := n.state.get(coldID); ok || f.proc(coldID) != nil {
		t.Fatal("rejected cold start left a conversation")
	}
	delete(send, "new_session_id")

	notes := filepath.Join(cwd, "notes.txt")
	writeFile(t, notes, "synthetic notes")
	reply, err := nodeCall(t, n, "assistant.send", send)
	if err != nil {
		t.Fatalf("valid selection after refusal: %v", err)
	}
	var started struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal(reply, &started); err != nil || started.ID != spare.id || f.proc(spare.id) != spare {
		t.Fatalf("valid selection did not reuse the spare: %s (%v)", reply, err)
	}
	f.idle(t, started.ID)
	sent := f.file("stdin.jsonl")
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	send["session_id"], send["origin"] = started.ID, "channel"
	_, err = nodeCall(t, n, "assistant.send", send)
	assertFileRefusalDelivery(t, err)
	st, ok := n.state.get(started.ID)
	if !ok || st.Origin != "task" || f.proc(started.ID) != spare || f.file("stdin.jsonl") != sent {
		t.Fatal("rejected selection changed the existing conversation or delivered another turn")
	}
}

func assertFileRefusalDelivery(t *testing.T, err error) *rpc.Error {
	t.Helper()
	var refusal *rpc.Error
	if !errors.As(err, &refusal) || refusal.Code != "file_unavailable" {
		t.Fatalf("expected a file refusal, got %v", err)
	}
	var data struct {
		Delivered string `json:"delivered"`
		Retryable *bool  `json:"retryable"`
	}
	if err := json.Unmarshal(refusal.Data, &data); err != nil || data.Delivered != deliveredNone || data.Retryable == nil || *data.Retryable {
		t.Errorf("file refusal must report no delivery and explicit non-retryability: %s (%v)", refusal.Data, err)
	}
	return refusal
}
