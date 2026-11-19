package node

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Interactive Claude Code for assistant conversations (#10058, docs/assistant/assistant.md "Interactive Claude Code").
//
// With the assistant's claude_interactive setting on, the manager sends claude_interactive with every assistant.send
// (modelExecution.Interactive), and a Claude conversation's process is `personad assistant-tui` instead of `claude -p`.
// It runs the same launcher command with the same flags, less the print-mode ones, as interactive Claude Code in a
// private tmux server, and speaks to the node in the stream-json `claude -p` speaks (assistant_tui_bridge.go), so
// nothing else here changes. It reads its spec as the first line on its stdin (tuiSpec): the print-mode command is in
// it too, and a bridge whose interactive start fails runs that one in its place and says so first (a
// persona_tui_fallback line, tuiFallbackSubtype), so the message the node wrote is delivered either way and the switch
// is visible: in the node's log and in the conversation's execution report (assistantExecution's mode and mode_note).
//
// Permission mode and effort are start flags in an interactive process: a change resumes the conversation in a new
// process, as bypassPermissions always has (needsRestartForLocked, setAssistantModes). Guests, a meeting's room, a
// supervision round (--resume-session-at is print-only), a warm spare and Codex keep what they run on.

// Lines the bridge prints besides what claude -p prints: its interactive Claude Code is ready, or it gave up on it and
// runs claude -p instead.
const (
	tuiReadySubtype    = "persona_tui_ready"
	tuiFallbackSubtype = "persona_tui_fallback"
)

// Execution modes an assistant execution report names for a native Claude process.
const (
	executionModeInteractive = "interactive"
	executionModePrint       = "print"
)

// tuiSpec is what a bridge runs: the conversation, where its transcript may be, and its two commands.
type tuiSpec struct {
	Session    string   `json:"session"`
	Cwd        string   `json:"cwd"`
	ConfigDirs []string `json:"config_dirs"`
	Resume     bool     `json:"resume"`
	// AcceptBypass: the start asked for bypassPermissions, so Claude Code's confirmation of it is that same choice.
	AcceptBypass bool     `json:"accept_bypass"`
	TUI          []string `json:"tui"`
	Print        []string `json:"print"`
}

// printOnlyFlags are the flags of a print-mode start that an interactive one goes without (with their values).
var printOnlyFlags = map[string]int{"-p": 0, "--print": 0, "--verbose": 0, "--include-partial-messages": 0,
	"--input-format": 1, "--output-format": 1}

// withStartModes are a start's flags with the permission mode and effort it starts in, for a process that takes no
// control request to set them: an interactive one, and the print-mode one its bridge may run instead.
func withStartModes(args []string, mode, effort string) []string {
	out := slices.Clone(args)
	if mode != "" && mode != "default" && !slices.Contains(out, "--permission-mode") {
		out = append(out, "--permission-mode", mode)
	}
	if effort != "" && effort != effortAuto && !slices.Contains(out, "--effort") {
		out = append(out, "--effort", effort)
	}
	return out
}

// tuiArgs are a print-mode start's flags as an interactive start's: without the print-only ones.
func tuiArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if n, ok := printOnlyFlags[args[i]]; ok {
			i += n
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// assistantTUICommand is the bridge's command for a conversation whose interactive start is tui and whose print-mode
// start is print (both from assistantCLICommand), and the spec line to write first on its stdin.
func (n *Node) assistantTUICommand(spec tuiSpec, tui, print *exec.Cmd) (*exec.Cmd, []byte) {
	spec.TUI, spec.Print = tui.Args, print.Args
	if spec.Cwd == "" {
		spec.Cwd = print.Dir
	}
	line, _ := json.Marshal(spec)
	cmd := exec.Command(n.bin, "assistant-tui")
	cmd.Dir, cmd.Env = print.Dir, print.Env
	return cmd, append(line, '\n')
}

// tuiCooldown keeps a node from trying an interactive start that just failed on every message: for tuiRetryAfter
// after a fallback, conversations start in print mode, and their execution report says why.
var tuiCooldown struct {
	sync.Mutex
	until  time.Time
	reason string
}

const tuiRetryAfter = 10 * time.Minute

// tuiCoolingDown is the reason the last interactive start fell back, while new starts wait before trying again.
func tuiCoolingDown(now time.Time) (string, bool) {
	tuiCooldown.Lock()
	defer tuiCooldown.Unlock()
	if now.Before(tuiCooldown.until) {
		return tuiCooldown.reason, true
	}
	return "", false
}

func noteTUIFallback(reason string, now time.Time) {
	tuiCooldown.Lock()
	tuiCooldown.until, tuiCooldown.reason = now.Add(tuiRetryAfter), reason
	tuiCooldown.Unlock()
}

// tuiWanted says whether a Claude start for these options runs interactive Claude Code, and when it was asked for but
// does not, why (shown in the execution report).
func tuiWanted(execution modelExecution, guest, spare bool, round spawnRound, mode string) (bool, string) {
	switch {
	case !execution.Interactive || guest || spare || round.anchor != "":
		return false, ""
	case mode == proto.ModePlan:
		return false, ""
	case runtime.GOOS == "windows":
		return false, "interactive Claude Code needs tmux, which this computer (Windows) does not have"
	}
	if reason, cooling := tuiCoolingDown(time.Now()); cooling {
		return false, reason
	}
	return true, ""
}

// tuiStartModes are the permission mode and effort a conversation's interactive process starts in: what
// syncAssistantModes would set by control request on a print-mode one.
func (n *Node) tuiStartModes(id, mode string, execution modelExecution) (string, string) {
	_, effort := n.modesOf(id)
	n.mu.Lock()
	if n.modes[id].Effort == effortAuto {
		effort = effortAuto
	}
	n.mu.Unlock()
	if execution.UseEffort {
		effort = execution.Effort
	}
	return cmpMode(mode), effort
}

// tuiFellBack records that p's bridge runs claude -p because its interactive Claude Code did not start: in the log, in
// p's execution report, and for the next starts (tuiCooldown).
func (n *Node) tuiFellBack(p *assistantProc, reason string) {
	if reason == "" {
		reason = "interactive Claude Code did not start"
	}
	p.mu.Lock()
	p.tuiNote = reason
	p.mu.Unlock()
	noteTUIFallback(reason, time.Now())
	n.logger.Printf("assistant %s: interactive Claude Code did not start (%s): running claude -p instead; interactive starts are tried again after %s",
		short(p.id), reason, tuiRetryAfter)
	n.notifySessionsSoon()
}

// tuiEffortStale: p is interactive Claude Code started at another effort than the conversation's now, so the next
// idle message resumes it in a new process (an interactive one takes no apply_flag_settings).
func (n *Node) tuiEffortStale(p *assistantProc, execution modelExecution) bool {
	p.mu.Lock()
	tui := p.agent == "claude" && p.tui && p.tuiNote == ""
	p.mu.Unlock()
	if !tui {
		return false
	}
	_, effort := n.tuiStartModes(p.id, "", execution)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effortNeedsRestartLocked(effort)
}

// executionMode is how p runs for its execution report: "" when interactive was not asked for (or for Codex),
// "interactive", or "print" with why.
func (p *assistantProc) executionMode() (mode, note string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.agent != "claude" || !p.model.Interactive && !p.tui:
		return "", ""
	case p.tui && p.tuiNote == "":
		return executionModeInteractive, ""
	case p.tuiNote != "":
		return executionModePrint, p.tuiNote
	}
	return executionModePrint, "this conversation runs in print mode (a guest, a supervision round or plan mode)"
}

// tuiFallbackLine says line is the bridge's fallback notice ({"reason":…,"subtype":"persona_tui_fallback",…}).
func tuiFallbackLine(line []byte) bool {
	return bytes.Contains(line, []byte(`"subtype":"`+tuiFallbackSubtype+`"`))
}
