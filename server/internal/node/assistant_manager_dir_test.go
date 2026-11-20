package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
)

// managerDirCLI is fakeCLI that also notes the directory each process runs in (one line per start). The sign-in
// probe a cold start makes first (claude auth status, claudeauth.go) runs elsewhere and is no start.
const managerDirCLI = `case " $* " in *" auth status "*) ;; *) pwd -P >> "$CLI_FAKE_DIR/cli-pwd" ;; esac
` + fakeCLI

// A conversation of a manager runs in that manager's assistant directory (identity.AssistantDirName) on every message,
// not only its first. A later message names no folder (session.send: the hub's /sessions/{machine}/{sid}/messages, a
// continued /agent thread), and the origin the manager attested at the start is written again before the process
// starts: it was written with that empty folder, so the conversation of another manager moved to the built-in one's
// ~/.persona/assistant, with that directory's instructions, and was listed there, where its manager refuses the
// assistant's tools (assistantCaller: identity.IsAssistantDirOf). The built-in manager's conversation stays where it was.
func TestAssistantLaterMessagesKeepManagerDir(t *testing.T) {
	for _, tc := range []struct{ name, manager, origin string }{
		{"another manager", "m-0a1b2c3d", "lark_agent"},
		{"another manager, no origin", "m-0a1b2c3d", ""},
		{"built-in manager", "", "lark_agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTestBash(t)
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+managerDirCLI), 0o700); err != nil {
				t.Fatal(err)
			}
			n := f.n
			dir := filepath.Join(identity.DataDir(n.home), identity.AssistantDirName(tc.manager))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			real, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			start := map[string]any{"cwd": dir, "launcher": "claude", "text": "first", "model_fingerprint": "fp"}
			if tc.origin != "" {
				start["origin"] = tc.origin
			}
			answer, err := nodeCall(t, n, "assistant.send", start)
			if err != nil {
				t.Fatal(err)
			}
			var res struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(answer, &res); err != nil || res.SessionID == "" {
				t.Fatalf("start: %s %v", answer, err)
			}
			id := res.SessionID
			f.idle(t, id)
			check := func(round string, starts int) {
				t.Helper()
				// The folder is what this test is about. Each message attests its own origin (#11170): a later one
				// that names none records none, wherever it runs.
				st, ok := n.state.get(id)
				if !ok || st.Cwd != dir || st.Origin != tc.origin {
					t.Fatalf("%s: recorded cwd=%q origin=%q, want %q %q", round, st.Cwd, st.Origin, dir, tc.origin)
				}
				if p := f.proc(id); p == nil || p.cwd != dir {
					t.Fatalf("%s: process %+v, want it in %s", round, p, dir)
				}
				// The row the manager reads: its assistant tools are for a row in its own assistant directory only.
				listed := f.session(t, id)
				if listed.Cwd != dir || !identity.IsAssistantDirOf(listed.Cwd, tc.manager) {
					t.Fatalf("%s: listed in %q, not in manager %q's assistant directory %s", round, listed.Cwd, tc.manager, dir)
				}
				if tc.manager != "" && identity.IsAssistantDirOf(listed.Cwd, "") {
					t.Fatalf("%s: listed in the built-in manager's assistant directory %s", round, listed.Cwd)
				}
				pwds := strings.Fields(f.file("cli-pwd"))
				if len(pwds) != starts {
					t.Fatalf("%s: %d starts %v, want %d", round, len(pwds), pwds, starts)
				}
				for _, pwd := range pwds {
					if pwd != real {
						t.Fatalf("%s: a process ran in %s, want %s", round, pwd, real)
					}
				}
			}
			// A later message of the thread carries the origin the extension attests for it, as the first did (#11170).
			later := func(text string) map[string]any {
				p := map[string]any{"session_id": id, "text": text}
				if tc.origin != "" {
					p["origin"] = tc.origin
				}
				return p
			}
			check("first message", 1)
			// Continued: the next message of the thread goes to the running process.
			if _, err := nodeCall(t, n, "session.send", later("second")); err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			check("continued", 1)
			// Resumed: the process stopped (idle, a restart of the node), the next message resumes its transcript in a
			// new one (the fake claude writes none: this is the one Claude Code keeps).
			writeFile(t, filepath.Join(n.home, ".claude", "projects", "-assistant", id+".jsonl"),
				`{"type":"user","cwd":`+jsonString(t, dir)+`,"message":{"role":"user","content":"first"}}`+"\n")
			n.stopAssistant(id)
			if _, err := nodeCall(t, n, "session.send", later("third")); err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			check("resumed", 2)
			if argv := strings.Split(strings.TrimSpace(f.file("cli-argv")), "\n"); len(argv) != 2 || !strings.Contains(argv[1], "--resume "+id) {
				t.Fatalf("the stopped conversation was not resumed: %q", argv)
			}
		})
	}
}
