package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A warm spare that matches the start is the conversation: its live process needs no sign-in check, even when the
// profile's `auth status` now says signed out.
func TestNewProfilePreflightSkipsAReusedWarmSpare(t *testing.T) {
	f := cliTest(t)
	const cli = `case " $* " in
  *" auth status "*)
    touch "$CLI_FAKE_DIR/auth-ran"
    echo '{"loggedIn": false}'
    exit 1 ;;
esac
echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"control_request"'*)
      rid=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}\n' "$rid" ;;
    *'"type":"user"'*) echo '{"type":"result","subtype":"success"}' ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+cli), 0o700); err != nil {
		t.Fatal(err)
	}
	n := f.n
	n.home = os.Getenv("HOME")
	n.dir = filepath.Join(n.home, ".persona")
	n.claudeV.set("2.1.308")
	cwd := t.TempDir()
	params := map[string]any{"launcher": "claude", "cwd": cwd, "model_fingerprint": "spare-preflight-test"}
	if _, err := nodeCall(t, n, "assistant.warm", params); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	spare := n.spare
	n.mu.Unlock()
	if spare == nil {
		t.Fatal("no warm spare")
	}
	t.Cleanup(spare.stop)
	send := map[string]any{"launcher": "claude", "cwd": cwd, "text": "hello", "permission_mode": "default", "model_fingerprint": "spare-preflight-test"}
	answer, err := nodeCall(t, n, "assistant.send", send)
	if err != nil {
		t.Fatalf("a reused warm spare was refused: %v", err)
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(answer, &res); err != nil {
		t.Fatal(err)
	}
	if res.SessionID != spare.id {
		t.Fatalf("session %q is not the warm spare %q", res.SessionID, spare.id)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CLI_FAKE_DIR"), "auth-ran")); !os.IsNotExist(err) {
		t.Fatal("a reused warm spare was preflighted")
	}
	n.stopAssistant(res.SessionID)
}
