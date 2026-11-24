package claudeplugin

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Runner runs Claude Code's CLI exe with args for the config directory dir and returns what it printed on stdout. A
// command that exits non-zero returns its stdout too (the --json commands print their failure there) with an error.
type Runner func(ctx context.Context, exe, dir string, args ...string) ([]byte, error)

// commandTimeout bounds one CLI command: a plugin command reads a few files; a hung one must not hold the check.
const commandTimeout = 2 * time.Minute

// ExecRunner runs the CLI as a child process: in the data directory (never a project, whose settings would mix in),
// with no input, and with the environment commandEnv makes for dir.
func ExecRunner(workDir string) Runner {
	return func(ctx context.Context, exe, dir string, args ...string) ([]byte, error) {
		parent := ctx
		ctx, cancel := context.WithTimeout(parent, commandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Dir = workDir
		cmd.Env = commandEnv(os.Environ(), userClaudeDir(), dir)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		hideWindow(cmd)
		err := cmd.Run()
		if parent.Err() != nil {
			return stdout.Bytes(), fmt.Errorf("claude %s: %w", strings.Join(args, " "), parent.Err())
		}
		if ctx.Err() != nil {
			return stdout.Bytes(), fmt.Errorf("claude %s: timed out after %s", strings.Join(args, " "), commandTimeout)
		}
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return stdout.Bytes(), fmt.Errorf("claude %s: %s", strings.Join(args, " "), firstLine(msg))
		}
		return stdout.Bytes(), nil
	}
}

// userClaudeDir is the real user's default config directory (~/.claude), from the process's own home, never from a
// home a caller passed in: a test with a temporary home must never reach the user's directory by unsetting
// CLAUDE_CONFIG_DIR.
func userClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// commandEnv is base for a CLI command on dir. The user's default directory runs with CLAUDE_CONFIG_DIR unset, not set
// to ~/.claude: with it set, Claude Code keeps its global config (.claude.json) inside that directory instead of beside
// it in the home, and would start a second one. Any other directory is named. The official marketplace is not added
// on the side (a first run of the CLI in a directory would otherwise clone it): these commands change this plugin
// alone.
func commandEnv(base []string, userDir, dir string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL", "CLAUDECODE":
			continue
		}
		out = append(out, kv)
	}
	if userDir == "" || !SameDir(dir, userDir) {
		out = append(out, "CLAUDE_CONFIG_DIR="+dir)
	}
	return append(out, "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1")
}

// SameDir: a and b name the same directory (their real paths, compared without case where the file system usually
// ignores it).
func SameDir(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func unsupportedJSONFlag(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown option") && strings.Contains(message, "--json")
}

// listed is one plugin in `claude plugin list --json`.
type listed struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Scope   string `json:"scope"`
	Enabled bool   `json:"enabled"`
}

// result is a --json command's line: outcome ok | failed, with failureCode saying why.
type result struct {
	Outcome     string `json:"outcome"`
	Message     string `json:"message"`
	FailureCode string `json:"failureCode"`
}

// parseResult reads the command's result line (the last line of stdout that is a JSON object).
func parseResult(out []byte) (result, bool) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r result
		if json.Unmarshal([]byte(line), &r) == nil && r.Outcome != "" {
			return r, true
		}
	}
	return result{}, false
}

// errNotFound: the CLI says there is nothing to remove (the plugin is not installed, the marketplace not configured).
var errNotFound = errors.New("not found")

// cli runs the plugin commands for one config directory.
type cli struct {
	run Runner
	exe string
	dir string
}

func pluginSubcommand(args []string) string {
	rest := args
	if len(rest) > 0 && rest[0] == "plugin" {
		rest = rest[1:]
	}
	if len(rest) >= 2 && rest[0] == "marketplace" {
		return "marketplace " + rest[1]
	}
	if len(rest) >= 1 {
		return rest[0]
	}
	return "command"
}

// do runs a --json command. A failure the CLI reports as "already in the goal state" is success; one saying the
// plugin or marketplace is not there is errNotFound. An older CLI that does not know --json gets one retry without it.
func (c cli) do(ctx context.Context, args ...string) error {
	out, err := c.run(ctx, c.exe, c.dir, append(slices.Clip(args), "--json")...)
	if unsupportedJSONFlag(err) {
		_, err = c.run(ctx, c.exe, c.dir, args...)
		return err
	}
	sub := pluginSubcommand(args)
	r, ok := parseResult(out)
	switch {
	case ok && r.Outcome == "ok":
		return nil
	case ok && r.FailureCode == "already_in_goal_state":
		return nil
	case ok && (r.FailureCode == "not_installed" || r.FailureCode == "not_configured"):
		return errNotFound
	case ok:
		return fmt.Errorf("claude plugin %s: %s", sub, firstLine(r.Message))
	case err != nil:
		return err
	}
	return fmt.Errorf("claude plugin %s: no result", sub)
}

// list is the user-scope install of id, nil when there is none.
func (c cli) list(ctx context.Context, id string) (*listed, error) {
	out, err := c.run(ctx, c.exe, c.dir, "plugin", "list", "--json")
	if err != nil {
		return nil, err
	}
	var all []listed
	if err := json.Unmarshal(bytes.TrimSpace(out), &all); err != nil {
		return nil, fmt.Errorf("claude plugin list: %w", err)
	}
	for _, p := range all {
		if p.ID == id && (p.Scope == "user" || p.Scope == "") {
			return &p, nil
		}
	}
	return nil, nil
}

var versionRe = lazyre.New(`\d+\.\d+\.\d+[0-9A-Za-z.+-]*`)

// Version asks exe its version ("" when it does not say).
func Version(ctx context.Context, run Runner, exe, dir string) string {
	out, err := run(ctx, exe, dir, "--version")
	if err != nil {
		return ""
	}
	return versionRe.FindString(string(out))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Newer: version a is later than b (dotted numbers compared as numbers; what follows them, such as a pre-release,
// is not weighed). An unreadable version is never newer.
func Newer(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	if pa == nil {
		return false
	}
	if pb == nil {
		return true
	}
	for i := range max(len(pa), len(pb)) {
		x, y := "0", "0"
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if c := compareDecimal(x, y); c != 0 {
			return c > 0
		}
	}
	return false
}

func compareDecimal(x, y string) int {
	if len(x) != len(y) {
		return cmp.Compare(len(x), len(y))
	}
	return cmp.Compare(x, y)
}

func versionParts(v string) []string {
	v = versionRe.FindString(v)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ".") {
		digits := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			digits++
		}
		if digits == 0 {
			break
		}
		d := strings.TrimLeft(part[:digits], "0")
		if d == "" {
			d = "0"
		}
		out = append(out, d)
		if digits < len(part) {
			break
		}
	}
	return out
}
