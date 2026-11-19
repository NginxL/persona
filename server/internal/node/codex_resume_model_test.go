package node

import (
	"path/filepath"
	"strings"
	"testing"
)

// A Codex Persona started with a model and an effort keeps them in its record, the effort even when the start left
// out a -c Codex's config already runs at, and comes back with them when it is resumed after its tab closed (#10434).

func TestCodexStartRecordsModelAndEffort(t *testing.T) {
	for _, tc := range []struct {
		effort string
		want   []string
	}{
		{"max", []string{"codex", "--model", "gpt-6", "-c", "model_reasoning_effort=max"}},
		{"high", []string{"codex", "--model", "gpt-6"}}, // the config's effort: no -c, still recorded
	} {
		t.Run(tc.effort, func(t *testing.T) {
			withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
			n, _, cwd, _ := codexStartHerdr(t, 0)
			t.Setenv("HOME", n.home)
			t.Setenv("ZDOTDIR", n.home)
			codexOnPath(t, n.home) // the Codex CLI itself, so the config's effort counts as inherited (#10432)
			writeFile(t, filepath.Join(cwd, ".git", "HEAD"), "ref: refs/heads/main\n")
			writeFile(t, filepath.Join(n.codexHome(), "config.toml"), "model_reasoning_effort = \"high\"\n")
			_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "hello", "effort": tc.effort, "model": "gpt-6"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(ran, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("runs %q, want %q", ran, tc.want)
			}
			if st, _ := n.state.get(startThread); st.Model != "gpt-6" || st.ModelEffort != tc.effort {
				t.Errorf("state = %+v", st)
			}
		})
	}
}

func TestCodexWaitingRowRecordsModelAndEffort(t *testing.T) {
	n, _, _, _ := codexWaitHerdr(t)
	res, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "effort": "max", "model": "gpt-6"})
	if err != nil {
		t.Fatal(err)
	}
	s := decodeSessions(t, []any{res["session"]})[0]
	if st, _ := n.state.get(s.SessionID); st.Model != "gpt-6" || st.ModelEffort != "max" {
		t.Fatalf("row's state = %+v", st)
	}
	w := n.codexWaitByID(s.SessionID)
	if w == nil || !n.attachCodexWait(t.Context(), w, startThread, "test") {
		t.Fatal("the row did not attach")
	}
	if st, _ := n.state.get(startThread); st.Model != "gpt-6" || st.ModelEffort != "max" {
		t.Errorf("thread's state = %+v", st)
	}
}

func TestCodexResumeKeepsModelAndEffort(t *testing.T) {
	for _, tc := range []struct {
		effort string
		want   string
	}{
		{"max", "codex --model gpt-6 -c model_reasoning_effort=max resume " + startThread},
		{"high", "codex --model gpt-6 resume " + startThread}, // the config's effort: stays on the shared app-server
	} {
		t.Run(tc.effort, func(t *testing.T) {
			withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
			n, f, cwd := codexHistoryNode(t)
			t.Setenv("HOME", n.home)
			t.Setenv("ZDOTDIR", n.home)
			codexOnPath(t, n.home) // the Codex CLI itself, so the config's effort counts as inherited (#10432)
			writeFile(t, filepath.Join(cwd, ".git", "HEAD"), "ref: refs/heads/main\n")
			writeFile(t, filepath.Join(n.codexHome(), "config.toml"), "model_reasoning_effort = \"high\"\n")
			n.state.put(startThread, sessionState{Launcher: "codex", Cwd: cwd, Model: "gpt-6", ModelEffort: tc.effort})
			listSessions(t, n)
			f.reset()
			got := resumeFile(f, cwd)
			if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); err != nil {
				t.Fatal(err)
			}
			if file := got(); strings.Join(file.Argv, " ") != tc.want {
				t.Errorf("ran %q, want %q", file.Argv, tc.want)
			}
			if st, _ := n.state.get(startThread); st.Model != "gpt-6" || st.ModelEffort != tc.effort {
				t.Errorf("state = %+v", st)
			}
		})
	}
}
