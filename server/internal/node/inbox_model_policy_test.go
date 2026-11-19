package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A native child Agent's MCP server is a descendant of the same assistant process. Even if it names another session,
// the node resolves it to the parent and attests the parent's Inbox origin to the manager.
func TestInboxNativeChildMCPInheritsParentOrigin(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	self := &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	n.mu.Lock()
	n.procs["parent-session"] = &assistantProc{id: "parent-session", origin: originInbox, cmd: self}
	n.mu.Unlock()
	call := mcpCall(true, "child-supplied-session")
	got := ask(call)
	if got["assistant"] != true || got["session_id"] != "parent-session" || got["origin"] != originInbox {
		t.Fatalf("native child MCP call relayed as %v, want the parent's Inbox identity", got)
	}
}

// Codex's temporary process key and its eventual thread id must resolve to the same origin during startup, before the
// manager's origin book can contain the new thread.
func TestInboxCodexStartupKeyKeepsOrigin(t *testing.T) {
	empty := &Node{}
	if got := empty.assistantOrigin("not-started"); got != "" {
		t.Fatalf("a node without loaded session state returned origin %q", got)
	}
	n, _ := testNode(t)
	p := &assistantProc{id: "thread-session", origin: originInbox, cx: &codexConn{key: "proc-inbox"}}
	n.mu.Lock()
	n.akeys = map[string]*assistantProc{"proc-inbox": p}
	n.mu.Unlock()
	for _, id := range []string{"proc-inbox", "thread-session"} {
		if got := n.assistantOrigin(id); got != originInbox {
			t.Fatalf("assistantOrigin(%q)=%q, want inbox", id, got)
		}
	}
}

// The built-in Claude CLI receives non-bypass default through its permission-mode control, even when its start asks
// for bypass. Unlike Plan, default allows the explicit Persona MCP rules to run investigation and inbox_suggest
// without a permission prompt. Origin is recorded before the first turn and re-attested by the manager on later sends,
// so a node restart cannot silently return the conversation to bypass or lose its policy identity. (Only bypass is a
// startup flag; other modes use the control request.)
func TestInboxClaudeUsesDefaultAndPersistsOrigin(t *testing.T) {
	f := cliTest(t)
	// No model fingerprint: filling the launcher's default execution must not discard the start's attested origin.
	execution := modelExecution{Origin: originInbox}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "Investigate", nil, proto.ModeBypass, "", execution)
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	in := f.file("stdin.jsonl")
	if !strings.Contains(in, `"subtype":"set_permission_mode"`) || !strings.Contains(in, `"mode":"default"`) {
		t.Fatalf("Claude control requests did not select non-bypass default: %s", in)
	}
	argv := f.file("cli-argv")
	if !strings.Contains(argv, "--tools "+assistantBuiltinTools) {
		t.Fatalf("Inbox investigation lost built-in file, shell, web or tool-search capabilities: %s", argv)
	}
	if !strings.Contains(argv, assistantAllowedTools) {
		t.Fatalf("Persona's own MCP tools lost their permission allowlist: %s", argv)
	}
	st, ok := f.n.state.get(id)
	if !ok || st.Origin != originInbox || st.AppMode != "default" {
		t.Fatalf("persisted session state=%+v present=%v", st, ok)
	}
	p := f.proc(id)
	p.mu.Lock()
	processOrigin, executionOrigin := p.origin, p.model.Origin
	p.mu.Unlock()
	if processOrigin != originInbox || executionOrigin != originInbox {
		t.Fatalf("spawned process origin=%q execution origin=%q, want inbox", processOrigin, executionOrigin)
	}
	if got := f.n.assistantOrigin(id); got != originInbox {
		t.Fatalf("live process origin=%q, want inbox", got)
	}
}

// inboxPermissionCLI models the narrow Claude Code permission contract from the regression: Plan rejects Persona MCP
// before allowedTools, while default runs calls covered by mcp__persona without asking. It records completion instead
// of contacting a hub. TestInboxModelReadOnlyInvestigationAndSuggestion exercises the manager's real tool path.
const inboxPermissionCLI = `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
mode=default
allowed=
previous=
for argument in "$@"; do
  if [ "$previous" = "--allowedTools" ]; then allowed=$argument; fi
  previous=$argument
done
echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CLI_FAKE_DIR/stdin.jsonl"
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      case "$line" in
        *'"subtype":"set_permission_mode"'*) mode=$(printf '%s' "$line" | sed -n 's/.*"mode":"\([^"]*\)".*/\1/p') ;;
      esac
      echo "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\"}}" ;;
    *'"type":"user"'*)
      if [ "$mode" = "plan" ]; then
        printf '%s\n' 'Cannot call mcp__persona__lark_messages while in plan mode.' >> "$CLI_FAKE_DIR/permission-requests"
      else
        case ",$allowed," in
          *",mcp__persona,"*)
            printf '%s\n' 'mcp__persona__lark_messages' 'mcp__persona__inbox_suggest' >> "$CLI_FAKE_DIR/completed-tools" ;;
          *) printf '%s\n' 'Persona MCP was not allowed without asking' >> "$CLI_FAKE_DIR/permission-requests" ;;
        esac
      fi
      echo '{"type":"result","subtype":"success","result":"done"}' ;;
  esac
done
`

// A read-only MCP investigation and inbox_suggest both complete without a permission request. Reverting the Inbox
// clamp to Plan makes the CLI record the observed mode:plan rejection; removing mcp__persona records a prompt instead.
func TestInboxClaudeReadOnlyMCPAndSuggestionSkipPermission(t *testing.T) {
	f := cliTest(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "Investigate", nil, proto.ModeBypass, "", modelExecution{Origin: originInbox})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := f.file("permission-requests"); got != "" {
		t.Fatalf("Persona MCP stopped at a permission request: %s", got)
	}
	const completed = "mcp__persona__lark_messages\nmcp__persona__inbox_suggest\n"
	if got := f.file("completed-tools"); got != completed {
		t.Fatalf("completed Persona MCP calls = %q, want %q", got, completed)
	}
	if got := len(f.fh.interactions()); got != 0 {
		t.Fatalf("handling opened %d permission cards, want none", got)
	}
}

// Inbox handling never inherits the ordinary bypass-safe bot send/reply exemption. Reads remain available, and the
// same bot sends and replies still work for an ordinary bypass conversation.
func TestInboxLarkOutboundDeniedOrdinaryBypassPreserved(t *testing.T) {
	for _, tc := range []struct {
		name, origin, command, want string
	}{
		{"Inbox bot send", originInbox, "lark-cli im +messages-send --as bot --text hello", "deny"},
		{"Inbox bot reply", originInbox, "lark-cli im +messages-reply --as bot --text hello", "deny"},
		{"Inbox raw API send", originInbox, "lark-cli api POST /open-apis/im/v1/messages --as bot --data '{}'", "deny"},
		{"Inbox ordinary write", originInbox, "lark-cli docs +create --title example", "deny"},
		{"Inbox unknown write", originInbox, "lark-cli custom $action", "deny"},
		{"Inbox read", originInbox, "lark-cli docs +fetch --doc-id example", "allow"},
		{"ordinary bot send", "", "lark-cli im +messages-send --as bot --text hello", "allow"},
		{"ordinary bot reply", "", "lark-cli im +messages-reply --as bot --text hello", "allow"},
		{"ordinary read", "", "lark-cli docs +fetch --doc-id example", "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			const sid = "11111111-1111-4111-8111-111111111111"
			cfg := olCfg()
			cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
			n.larkRisks = map[string]string{"\x00docs +fetch": "read", "\x00docs +create": "write"}
			n.mu.Lock()
			n.larkCfg, n.larkCfgAt = cfg, time.Now().Add(time.Hour)
			n.procs[sid] = &assistantProc{id: sid, agent: "claude", origin: tc.origin, exited: make(chan struct{})}
			n.mu.Unlock()
			n.persistModes(sid, proto.ModeBypass, "")
			decision, reason, err := n.larkCheck(context.Background(), sid, t.TempDir(), tc.command,
				proto.Interaction{SessionID: sid, Cwd: t.TempDir(), ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil || decision != tc.want {
				t.Fatalf("decision=%q reason=%q err=%v, want %q", decision, reason, err, tc.want)
			}
			if tc.origin == originInbox && tc.want == "deny" && !strings.Contains(reason, "inbox_suggest") {
				t.Fatalf("Inbox refusal is not actionable: %q", reason)
			}
			if len(fh.interactions()) != 0 {
				t.Fatalf("policy test unexpectedly opened a confirmation card: %v", fh.interactions())
			}
		})
	}
}
