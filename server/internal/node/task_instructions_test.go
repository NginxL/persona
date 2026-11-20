package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTaskInstructionsCodexStartAndRestore(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
	n, _, cwd, _ := codexStartHerdr(t, 0)
	const rules = "Use one closeout.\nKeep the task's scope."
	res, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "codex", "prompt": "Summarize the failed checks", "task_instructions": rules, "task_attachments": "<attached_images>\n/image.png\n</attached_images>"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ran, codexTaskConfig(rules)) {
		t.Fatalf("argv=%q", ran)
	}
	s := decodeSessions(t, []any{res["session"]})[0]
	st, ok := n.state.get(s.SessionID)
	if !ok || st.TaskInstructions != rules || st.TaskInstructionChannel != "codex" {
		t.Fatalf("state=%+v", st)
	}
	argv, ok := n.agentResumeArgv(agentLaunch{Agent: "codex", Launcher: "codex", TaskInstructions: codexKeptTaskInstructions(st)}, s.SessionID)
	if !ok || !strings.Contains(strings.Join(argv, " "), "developer_instructions=") {
		t.Fatalf("restore=%q %v", argv, ok)
	}
	raw, err := os.ReadFile(n.codexPath(s.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), rules) || !strings.Contains(string(raw), "Summarize the failed checks") || !strings.Contains(string(raw), "attached_images") {
		t.Fatalf("assignment transcript=%s", raw)
	}
}

func TestTaskInstructionsPreserveCodexConfiguration(t *testing.T) {
	n, _, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	if !n.taskCodexChannel(t.Context(), "codex", cwd, []string{"codex"}) {
		t.Fatal("clean native start lost its channel")
	}
	for _, argv := range [][]string{{"codex", "--remote", "unix:///example.sock"}, {"codex", "--profile", "work"}, {"wrapper"}} {
		if n.taskCodexChannel(t.Context(), "codex", cwd, argv) {
			t.Fatalf("unsafe argv accepted: %q", argv)
		}
	}
	dir := mkdir(t, filepath.Join(cwd, ".codex"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("developer_instructions = 'Keep my policy'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if n.taskCodexChannel(t.Context(), "codex", cwd, []string{"codex"}) {
		t.Fatal("project policy would be replaced")
	}
}

func TestTaskInstructionsClaudeFileRestoreAndHook(t *testing.T) {
	n, asked := closeoutStartNode(t, func() (string, error) { return "duplicate", nil })
	n.dir = t.TempDir()
	rules := "Keep scope.\n<closeout status=\"done\">\n</closeout>"
	file, err := n.taskInstructionFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	again, err := n.taskInstructionFile(rules)
	if err != nil || again != file {
		t.Fatalf("file=%q, %v", again, err)
	}
	raw, err := os.ReadFile(file)
	if err != nil || string(raw) != rules {
		t.Fatalf("policy=%q %v", raw, err)
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	n.state.put(startID, sessionState{TaskInstructions: rules, TaskInstructionFile: file, TaskInstructionChannel: "claude"})
	argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", TaskInstructionFile: file}, startID)
	if !ok || !strings.Contains(strings.Join(argv, " "), "--append-system-prompt-file") || strings.Contains(strings.Join(argv, " "), rules) {
		t.Fatalf("restore=%q %v", argv, ok)
	}
	if out := n.closeoutStart(t.Context(), sockRequest{Kind: "hook", Pane: "w1:p1"}, startHook(startID)); out != nil || asked.Load() != 0 {
		t.Fatalf("duplicate guidance=%v asks=%d", out, asked.Load())
	}
}

func TestTaskInstructionsFallbackKeepsAssignmentAndImages(t *testing.T) {
	prompt := "Summarize the checks\n\n<attached_images>\n/image.png\n</attached_images>"
	got := taskFallback(prompt, "Keep scope")
	if !strings.HasPrefix(got, "Summarize the checks") || !strings.HasSuffix(got, "</attached_images>") || strings.Count(got, "Keep scope") != 1 {
		t.Fatal(got)
	}
}

func TestTaskInstructionsClaudeWrapperChannel(t *testing.T) {
	home := fakeCLIHome(t)
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(`forward() { claude "$@"; }
strip() { claude; }
custom() { claude --append-system-prompt "Existing policy" "$@"; }
`), 0600); err != nil {
		t.Fatal(err)
	}
	n, _, _ := herdrNode(t)
	for _, tc := range []struct {
		name string
		want bool
	}{{"forward", true}, {"strip", false}, {"custom", false}} {
		if got := n.taskClaudeChannel(t.Context(), tc.name); got != tc.want {
			t.Errorf("%s=%v", tc.name, got)
		}
	}
}

func TestTaskInstructionsClaudeStartKeepsFirstMessage(t *testing.T) {
	shellHome := fakeCLIHome(t)
	if err := os.WriteFile(filepath.Join(shellHome, ".zshrc"), []byte("forward() { claude \"$@\"; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	startHerdr(f, "w1:p2", "idle")
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.screenSteps("w1:p2", isKeys(), screenFixture(t, "claude-2.1.312-empty-162.txt"))
	promptLands(t, f, home, cwd)
	const rules = "Keep this assignment's scope.\nUse one closeout."
	_, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": cwd, "launcher": "forward", "prompt": "fix the build", "task_instructions": rules})
	if err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	raw, readErr := os.ReadFile(n.transcriptPath(startID))
	if readErr != nil || !strings.Contains(string(raw), "fix the build") || strings.Contains(string(raw), rules) {
		t.Fatalf("first message=%s %v", raw, readErr)
	}
	st, _ := n.state.get(startID)
	if st.TaskInstructions != rules || st.TaskInstructionFile == "" || st.TaskInstructionChannel != "claude" {
		t.Fatalf("state=%+v", st)
	}
	trace := strings.Join(f.trace(), "\n")
	if !strings.Contains(trace, "--append-system-prompt-file") || strings.Contains(trace, rules) {
		t.Fatalf("trace=%s", trace)
	}
}

func TestTaskInstructionsFollowAnEngineSwitch(t *testing.T) {
	n, f, home, cwd, _ := switchNode(t, "idle", longTurn)
	const rules = "Keep the accepted task's scope. Use one closeout."
	n.state.update(sidA, func(st *sessionState) bool {
		st.TaskInstructions = rules
		st.TaskInstructionChannel = "claude"
		return true
	})
	if err := pick(t, n, map[string]any{"launcher": "cx", "model": "gpt-6"}); err != nil {
		t.Fatal(err)
	}
	f.reset()
	quitsToShell(f, cwd)
	files := codexComesUp(t, f, home, "w1:p1", true)
	if err := tell(t, n, "Continue the assignment", true); err != nil {
		t.Fatal(err)
	}
	got := files()
	if len(got) != 1 || strings.Count(got[0].Prompt, rules) != 1 {
		t.Fatalf("switched first message=%+v", got)
	}
	st, _ := n.state.get(switchedThread)
	if st.TaskInstructions != rules {
		t.Fatalf("switched state=%+v", st)
	}
}

func TestTaskInstructionsResumeFallbackDoesNotDropPolicy(t *testing.T) {
	const rules = "Keep the task's rules."
	got, err := taskResumeFallback(t.Context(), "Continue the assignment", rules, true, "claude")
	if err != nil || got != "Continue the assignment\n\n"+rules {
		t.Fatalf("continuation=%q %v", got, err)
	}
	if _, err = taskResumeFallback(t.Context(), "", rules, true, "claude"); rpcCode(err) != "task_instructions_resume" {
		t.Fatalf("bare Resume=%v", err)
	}
	got, err = taskResumeFallback(t.Context(), "Continue", rules, true, "")
	if err != nil || got != "Continue" {
		t.Fatalf("history policy repeated: %q %v", got, err)
	}
}

func TestTaskInstructionsBareResumeRefusesBeforeCreatingATerminal(t *testing.T) {
	shellHome := fakeCLIHome(t)
	if err := os.WriteFile(filepath.Join(shellHome, ".zshrc"), []byte("fallback() { claude; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	writeUserLine(t, home, cwd, startID, "Original assignment")
	n.state.put(startID, sessionState{Launcher: "fallback", Cwd: cwd, TaskInstructions: "Keep the scope", TaskInstructionChannel: "claude"})
	_, err := request(t, n, "session.start", map[string]any{"resume_session_id": startID, "launcher": "fallback"})
	if rpcCode(err) != "task_instructions_resume" {
		t.Fatalf("resume=%v", err)
	}
	if calls := f.trace("tab.create", "workspace.create", "pane.send_input"); len(calls) != 0 {
		t.Fatalf("resume created a terminal: %q", calls)
	}
}
