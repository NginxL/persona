package node

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// fakeCLI is the machine's claude -p in cliTest: it appends its args to $CLI_FAKE_DIR/cli-argv and keeps its
// CLAUDE_CONFIG_DIR in cli-profile, keeps every stdin line in stdin.jsonl, answers control requests (an error from
// control-mode-error.json or control-effort-error.json, held while control-hold exists, after the lines in
// control-extra.jsonl, exiting at control-exit), and plays a fixed turn for each user message (no result while
// $CLI_FAKE_DIR/hold exists).
const fakeCLI = `case " $* " in
  *" auth status "*)
    printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-auth-argv"
    echo '{"loggedIn":true}'
    exit 0 ;;
esac
printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
printf '%s\n' "$CLAUDE_CONFIG_DIR" > "$CLI_FAKE_DIR/cli-profile"
echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CLI_FAKE_DIR/stdin.jsonl"
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      if [ -f "$CLI_FAKE_DIR/control-extra.jsonl" ]; then cat "$CLI_FAKE_DIR/control-extra.jsonl"; fi
      if [ -f "$CLI_FAKE_DIR/control-hold" ]; then
        printf '%s\n' "$id" > "$CLI_FAKE_DIR/control-waiting"
        while [ -f "$CLI_FAKE_DIR/control-hold" ]; do sleep 0.02; done
      fi
      if [ -f "$CLI_FAKE_DIR/control-exit" ]; then exit 0; fi
      case "$line" in
        *'"subtype":"set_permission_mode"'*|*'"subtype":"apply_flag_settings"'*)
          case "$line" in
            *'"subtype":"set_permission_mode"'*) error_file="$CLI_FAKE_DIR/control-mode-error.json" ;;
            *) error_file="$CLI_FAKE_DIR/control-effort-error.json" ;;
          esac
          if [ -f "$error_file" ]; then
            printf '{"type":"control_response","response":{"subtype":"error","request_id":"%s","error":%s}}\n' "$id" "$(cat "$error_file")"
          else
            echo "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\"}}"
          fi ;;
        *) echo "{\"type\":\"control_response\",\"response\":{\"subtype\":\"error\",\"request_id\":\"$id\",\"error\":\"unsupported\"}}" ;;
      esac ;;
    *'"type":"user"'*)
      echo '{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
      echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hel"}},"parent_tool_use_id":null}'
      echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}},"parent_tool_use_id":null}'
      echo '{"type":"stream_event","event":{"type":"message_stop"},"parent_tool_use_id":null}'
      if [ ! -e "$CLI_FAKE_DIR/hold" ]; then echo '{"type":"result","subtype":"success","result":"hello"}'; fi ;;
  esac
done
`

type cliFixture struct {
	n   *Node
	fh  *fakeHub
	dir string // $CLI_FAKE_DIR
	log *syncBuffer
}

// cliTest is a node whose assistant conversations run fakeCLI as the machine's claude, with a fake hub.
func cliTest(t *testing.T) *cliFixture {
	return cliTestHome(t, fakeClaudeHome)
}

// cliTestBash runs a regression through the production bash fallback instead of skipping on a host without zsh.
func cliTestBash(t *testing.T) *cliFixture {
	return cliTestHome(t, fakeClaudeHomeBash)
}

func cliTestHome(t *testing.T, home func(*testing.T, string)) *cliFixture {
	t.Helper()
	home(t, fakeCLI)
	dir := t.TempDir()
	t.Setenv("CLI_FAKE_DIR", dir)
	n := codexTestNode(t)
	n.dir = t.TempDir()
	buf := &syncBuffer{}
	n.logger = log.New(buf, "", 0)
	fh := withFakeHub(t, n)
	// Every process it started, the warm spare too, has exited when the test ends: liveAssistants is the test binary's,
	// and a later test reading it (update_inventory.go) would find one still running.
	t.Cleanup(func() {
		n.mu.Lock()
		ids := make([]string, 0, len(n.procs))
		for id := range n.procs {
			ids = append(ids, id)
		}
		spare := n.spare
		n.spare = nil
		n.mu.Unlock()
		var procs []*assistantProc
		for _, id := range ids {
			n.cancelAssistantRecovery(id)
			if p := n.takeAssistant(id); p != nil {
				procs = append(procs, p)
			}
		}
		if spare != nil {
			procs = append(procs, spare)
		}
		stopAndWait(procs)
	})
	return &cliFixture{n: n, fh: fh, dir: dir, log: buf}
}

// fakeLaunch makes the node's personad a `personad launch <name>` that runs the machine's claude (fakeCLI) with the
// args after the launcher's name: a managed launcher's conversation then starts as a shell launcher's does.
func (f *cliFixture) fakeLaunch(t *testing.T) {
	t.Helper()
	bin := filepath.Join(os.Getenv("HOME"), "bin", "personad")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nshift 2\nexec claude \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.n.bin = bin
}

// cliExec is a start's model configuration as the hub resolves it (a fingerprint came).
func cliExec() modelExecution {
	return modelExecution{Fingerprint: "fp"}
}

// cliWait waits for what a conversation's process does, with room for a loaded machine.
func cliWait(t *testing.T, what string, ok func() bool) {
	t.Helper()
	waitWithin(t, 10*time.Second, what, ok)
}

func (f *cliFixture) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

func (f *cliFixture) proc(id string) *assistantProc {
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	return f.n.procs[id]
}

// idle waits for the end of the turn as the node publishes it (assistantSessions): an idle process and no model
// recovery still holding the turn. busy alone is not enough: a recovery that moves to the next model registers the
// replacement process before it marks it busy and sends it the retry, and that gap is still the same turn (#3815).
func (f *cliFixture) idle(t *testing.T, id string) {
	t.Helper()
	cliWait(t, "the turn's result", func() bool { return f.turnOver(id) })
}

// turnOver: conversation id has a registered process that is not busy, and no recovery holds its turn.
func (f *cliFixture) turnOver(id string) bool {
	f.n.mu.Lock()
	p, recovering := f.n.procs[id], f.n.recoveries[id] != nil
	f.n.mu.Unlock()
	if p == nil || recovering {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.busy
}

func (f *cliFixture) session(t *testing.T, id string) proto.Session {
	t.Helper()
	for _, s := range f.n.assistantSessions() {
		if s.SessionID == id {
			return s
		}
	}
	t.Fatalf("no session %s", id)
	return proto.Session{}
}

// Deltas and the result stream through; the modes go as control requests claude answers.
func TestAssistantStreamsAndControlRequests(t *testing.T) {
	f := cliTest(t)
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "acceptEdits", "high", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	cliWait(t, "the delta", func() bool {
		f.fh.mu.Lock()
		defer f.fh.mu.Unlock()
		for _, d := range f.fh.deltas {
			if d == "hello" {
				return true
			}
		}
		return false
	})
	in := f.file("stdin.jsonl")
	if !strings.Contains(in, `"subtype":"set_permission_mode"`) || !strings.Contains(in, `"mode":"acceptEdits"`) ||
		!strings.Contains(in, `"subtype":"apply_flag_settings"`) || !strings.Contains(in, `"effortLevel":"high"`) {
		t.Fatalf("control requests: %s", in)
	}
	if logs := f.log.String(); strings.Contains(logs, "control request failed") {
		t.Fatalf("log: %s", logs)
	}
	if argv := f.file("cli-argv"); !strings.Contains(argv, "--input-format stream-json") || !strings.Contains(argv, "--session-id "+id) {
		t.Fatalf("claude args: %s", argv)
	}
}

// Interrupting stops claude (its process group).
func TestAssistantInterruptStopsTheProcess(t *testing.T) {
	f := cliTest(t)
	if err := os.WriteFile(filepath.Join(f.dir, "hold"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	p := f.proc(id)
	cliWait(t, "the turn", func() bool { return strings.Contains(f.file("stdin.jsonl"), `"type":"user"`) })
	if err := f.n.assistantInterrupt(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("claude is still running")
	}
	if f.proc(id) != nil {
		t.Fatal("the stopped process is still registered")
	}
}
