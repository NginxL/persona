package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// taskInstructionFile keeps stable framework rules outside shell command lines and user messages.
// It is node state, retained with the session for a later terminal restore.
func (n *Node) taskInstructionFile(text string) (string, error) {
	dir := filepath.Join(n.dir, "task-instructions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(text))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".txt")
	if kept, err := os.ReadFile(path); err == nil && string(kept) == text {
		return path, nil
	}
	f, err := os.CreateTemp(dir, ".instructions-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(text); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// Only positively identified terminal launchers get CLI flags. Other runtimes and wrappers
// keep the same rules in their first message; unknown support must never silently drop them.
func (n *Node) taskClaudeChannel(ctx context.Context, launcher string) bool {
	const marker = "persona-task-instruction-file"
	probe, err := n.launcherProbe(ctx, launcher, "--append-system-prompt-file", marker)
	if err != nil {
		return nativeClaudeFile(launcher, probe.File)
	}
	if probe.Agent != "claude" {
		return false
	}
	forwarded := false
	for i, a := range probe.Args {
		if a == "--append-system-prompt-file" && i+1 < len(probe.Args) && probe.Args[i+1] == marker {
			forwarded = true
		} else if strings.HasPrefix(a, "--append-system-prompt") || strings.HasPrefix(a, "--system-prompt") {
			return false
		}
	}
	return forwarded
}

// Avoid replacing configured developer instructions, custom profiles, wrappers, remote
// daemons or project config. The conservative fallback preserves all their existing policy.
func (n *Node) taskCodexChannel(ctx context.Context, launcher, cwd string, argv []string) bool {
	if launcher != "codex" || len(argv) != 1 || argv[0] != "codex" {
		return false
	}
	probe, err := n.launcherProbe(ctx, launcher)
	if err != nil {
		if probe.File == "" {
			return false
		}
		if _, headErr := scriptHead(probe.File); headErr == nil || headErr.Error() != "not a script" {
			return false
		}
	}
	for _, env := range []map[string]string{probe.Env, probe.Shell} {
		if home := env["CODEX_HOME"]; home != "" && filepath.Clean(home) != filepath.Clean(n.codexHome()) {
			return false
		}
		if env["CODEX_CONFIG_DIR"] != "" {
			return false
		}
	}
	if _, err := os.Stat(filepath.Join(n.codexHome(), "config.toml")); !os.IsNotExist(err) {
		return false
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".codex", "config.toml")); !os.IsNotExist(err) {
			return false
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return true
}

func codexTaskArgs(argv []string, instructions string) []string {
	if instructions == "" {
		return argv
	}
	return insertBeforeDashDash(argv, []string{"-c", codexTaskConfig(instructions)})
}

func taskFallback(prompt, rules string) string {
	if strings.TrimSpace(rules) == "" {
		return prompt
	}
	// Keep an image block at the end so terminal senders still recognize attachments.
	if i := strings.LastIndex(prompt, "\n\n<attached_images>"); i >= 0 && strings.HasSuffix(strings.TrimSpace(prompt), "</attached_images>") {
		return strings.TrimSpace(prompt[:i]) + "\n\n" + rules + prompt[i:]
	}
	return strings.TrimSpace(prompt) + "\n\n" + rules
}

func codexKeptTaskInstructions(st sessionState) string {
	if st.TaskInstructionChannel == "codex" {
		return st.TaskInstructions
	}
	return ""
}
func codexTaskFlags(text string) string {
	if text == "" {
		return ""
	}
	return " -c " + dquote(codexTaskConfig(text))
}

// Herdr refuses an apostrophe even inside quoted argv; TOML's Unicode escape preserves it.
func codexTaskConfig(text string) string {
	value, _ := json.Marshal(text)
	return "developer_instructions=" + strings.ReplaceAll(string(value), "'", `\u0027`)
}

// A resumed fallback already has its rules in history. A formerly dedicated policy
// must enter the explicit continuation once; a bare Resume cannot start a policy-only turn.
func taskResumeFallback(ctx context.Context, prompt, rules string, resume bool, previousChannel string) (string, error) {
	if !resume {
		return taskFallback(prompt, rules), nil
	}
	if previousChannel == "" {
		return prompt, nil
	}
	if strings.TrimSpace(prompt) == "" {
		return "", refusal("task_instructions_resume", i18n.T(i18n.From(ctx), "rpc-errors.task_instructions_resume"), nil)
	}
	return taskFallback(prompt, rules), nil
}
