package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// orcaPrompts is a new Orca tab where only Enter keys arrived: a zsh prompt per key, the start line nowhere (#10588).
var orcaPrompts = strings.Repeat("➜  proj git:(main)\n", 30) + "➜  proj git:(main) "

// orcaFirstTab is the terminal of the first tab Orca makes, and its pane.
const orcaFirstTab, orcaFirstPane = "term_fake_1", "tab_1:leaf_1"

// orcaStartNode is an Orca node whose tabs run zsh in a worktree at root, with the processes Orca scans in procs: its
// starts wait start for the tab's shell and ready for claude, at the fixture cadence.
func orcaStartNode(t *testing.T, start, ready time.Duration) (n *Node, o *orcarttest.Fake, procs *fakeProcs, root string) {
	t.Helper()
	withPaneShell(t, "/bin/zsh")
	n, _, o, procs, home := orcaNode(t)
	n.claudeStart, n.claudeReady = start, ready
	root = mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", root, "refs/heads/main"))
	return n, o, procs, root
}

// orcaStartTab is orcaStartNode with a new tab made as a start makes one (orcaFirstTab).
func orcaStartTab(t *testing.T, start, ready time.Duration) (*Node, *orcarttest.Fake, *fakeProcs, *host.Created) {
	t.Helper()
	n, o, procs, root := orcaStartNode(t, start, ready)
	launch, err := hostCap[host.Launcher](n)
	if err != nil {
		t.Fatal(err)
	}
	created, err := launch.CreateSpace(context.Background(), root, "tab1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Pane.ID != orcaFirstPane {
		t.Fatalf("pane %s", created.Pane.ID)
	}
	return n, o, procs, created
}

// A start in an Orca tab takes Orca naming claude there as claude having come only with evidence of its own: Claude
// Code's screen, or its line run (the start file gone) with nothing on screen against it. Until then it waits for the
// shell and reads the shell's answers; a line that never showed and never ran is not delivered, not a Claude stall
// (#10588). Orca names claude in every case here, as it did in a tab where no claude ran.
func TestOrcaStartNeedsEvidenceBeyondOrcasWord(t *testing.T) {
	const start, ready = 1500 * time.Millisecond, 300 * time.Millisecond
	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	banner := " ▐▛███▛█   Claude Code v2.1.312\n▝▜██████▀  Sonnet 5.5 · API Usage Billing\n ▝▝   ▝▝   /private/tmp/project\n"
	for _, tc := range []struct {
		name string
		// screen is the tab's screen; line is the start line as typed, file its start file.
		screen func(line, file string) string
		ran    bool   // the shell ran the line: its start file is gone
		want   string // notDelivered, notRun, rejected, missing, stalled or ready
	}{
		{"only prompts, no echo", func(_, _ string) string { return orcaPrompts }, false, "notDelivered"},
		{"echoed, unrun after the prompt", func(line, _ string) string { return orcaPrompts + line }, false, "notRun"},
		{"echoed, its head eaten", func(line, file string) string {
			return orcaPrompts + strings.TrimPrefix(line, ". ") + "\nzsh: permission denied: " + file + "\n➜  proj git:(main) "
		}, false, "rejected"},
		{"run, claude not found", func(line, _ string) string {
			return orcaPrompts + line + "\nzsh: command not found: claude\n➜  proj git:(main) "
		}, true, "missing"},
		{"echoed and run, nothing drawn yet", func(line, _ string) string { return orcaPrompts + line + "\n" }, true, "stalled"},
		{"Claude's banner, no echo", func(_, _ string) string { return banner }, false, "stalled"},
		{"Claude's input box", func(_, _ string) string { return empty }, true, "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, o, procs, created := orcaStartTab(t, start, ready)
			procs.set(created.Pane.ID, orcaProc{pid: 4242, agent: "claude"})
			file := filepath.Join(t.TempDir(), "agent-start-1.sh")
			if !tc.ran {
				if err := os.WriteFile(file, []byte("true\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			line := startLine(file, startID, "/bin/zsh")
			o.SetScreen(orcaFirstTab, tc.screen(line, file))
			probe := &startProbe{anchor: startID, program: "claude", launcher: "claude", paneShell: "zsh"}
			probe.typed(&typedStart{line: line, path: file})
			began := time.Now()
			a, err := n.waitReady(context.Background(), created.Pane.String(), probe)
			took := time.Since(began)
			var stalled *claudeStalled
			var rejected *startLineRejected
			switch tc.want {
			case "notDelivered":
				if rpcCode(err) != proto.CodeTerminalNotReady || !strings.Contains(err.Error(), "never showed in the new tab") || took < start {
					t.Fatalf("after %s: %v", took, err)
				}
			case "notRun":
				if err == nil || rpcCode(err) != "" || errors.As(err, &stalled) || !strings.Contains(err.Error(), "had not run it yet") || took < start {
					t.Fatalf("after %s: %v", took, err)
				}
			case "rejected":
				if !errors.As(err, &rejected) || took >= start {
					t.Fatalf("after %s: %v", took, err)
				}
			case "missing":
				if rpcCode(err) != proto.CodeClaudeMissing || took >= start {
					t.Fatalf("after %s: %v", took, err)
				}
			case "stalled":
				if !errors.As(err, &stalled) || took >= start {
					t.Fatalf("after %s, want a stall after the ready wait: %v", took, err)
				}
			case "ready":
				if err != nil || a == nil || a.Agent != "claude" {
					t.Fatalf("not ready: %+v, %v", a, err)
				}
			}
		})
	}
}

// Claude Code's folder-trust dialog in an Orca start's tab still counts as claude come, its line unseen or not, and is
// answered as before: the start waits for claude's input box from then on.
func TestOrcaStartTrustDialogStillCounts(t *testing.T) {
	const start, ready = 5 * time.Second, 300 * time.Millisecond // the answer is followed by a second's pause
	n, o, procs, created := orcaStartTab(t, start, ready)
	procs.set(created.Pane.ID, orcaProc{pid: 4242, agent: "claude"})
	o.SetScreen(orcaFirstTab, screenFixture(t, "claude-2.1.312-trust-162.txt"))
	file := filepath.Join(t.TempDir(), "agent-start-1.sh")
	if err := os.WriteFile(file, []byte("true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := &startProbe{anchor: startID, program: "claude", launcher: "claude", paneShell: "zsh"}
	probe.typed(&typedStart{line: startLine(file, startID, "/bin/zsh"), path: file})
	began := time.Now()
	_, err := n.waitReady(context.Background(), created.Pane.String(), probe)
	var stalled *claudeStalled
	if took := time.Since(began); !errors.As(err, &stalled) || took >= start {
		t.Fatalf("after %s: %v", took, err)
	}
	var keys []string
	for _, w := range o.Writes() {
		if w.Handle == orcaFirstTab && !w.Close {
			keys = append(keys, w.Text)
		}
	}
	if len(keys) == 0 || keys[len(keys)-1] != "\r" {
		t.Errorf("the trust dialog was not answered: %q", keys)
	}
}

// The start of #10588 end to end: Orca names claude in the new tab, which shows only empty prompts, the start line
// never in it. The start fails as terminal_not_ready after the wait for the shell, closes the tab and keeps none, and
// its start file goes with it; not agent_start_stalled after the wait for claude, with the tab kept.
func TestOrcaStartWhoseLineNeverArrivedIsNotDelivered(t *testing.T) {
	const start, ready = 1500 * time.Millisecond, 300 * time.Millisecond
	n, o, procs, root := orcaStartNode(t, start, ready)
	procs.set(orcaFirstPane, orcaProc{pid: 4242, agent: "claude"})
	o.SetScreen(orcaFirstTab, orcaPrompts)
	began := time.Now()
	_, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root})
	if took := time.Since(began); rpcCode(err) != proto.CodeTerminalNotReady || took < start {
		t.Fatalf("after %s: %v", took, err)
	}
	if got := startLineIn(o); got != orcaFirstTab {
		t.Fatalf("the start line went to %q", got)
	}
	closed := false
	for _, w := range o.Writes() {
		closed = closed || w.Close && w.Handle == orcaFirstTab
	}
	if !closed {
		t.Errorf("the tab was not closed: %+v", o.Writes())
	}
	if _, kept := isKept(n, "orca:"+orcaFirstPane); kept {
		t.Error("the tab was kept as a stalled start's")
	}
	if left, _ := filepath.Glob(filepath.Join(n.dir, startScriptPrefix+"*")); len(left) != 0 {
		t.Errorf("start files left: %v", left)
	}
}
