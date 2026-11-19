package node

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// launcherRepairUpgrade is the one repair a node does by itself for the hub's launcher health (docs/server/launcher-health.md):
// update the Claude Code CLI a launcher runs. Fixed commands only: the hub cannot have the node run anything else.
const launcherRepairUpgrade = "upgrade_cli"

// claudeUpdateScript updates Claude Code in the login shell (tests replace it).
var claudeUpdateScript = "claude update"

// launcherRepairTimeout bounds one repair: an update downloads the CLI.
var launcherRepairTimeout = 5 * time.Minute

// launcherRepair runs one of the fixed repairs for launcher name. A repair that does not apply to the launcher's agent
// (a Codex launcher, an unknown action) answers Unsupported, not an error.
func (n *Node) launcherRepair(ctx context.Context, name, action string) (proto.LauncherRepair, error) {
	if proto.IsExternalLauncher(name) {
		return proto.LauncherRepair{Unsupported: true}, nil
	}
	if action != launcherRepairUpgrade {
		return proto.LauncherRepair{Unsupported: true, Output: fmt.Sprintf("unknown repair %q", action)}, nil
	}
	agent := "claude"
	if _, managed := n.managedLauncher(name); !managed && name != "claude" {
		if d := n.launcherProbeCached(ctx, name); d.Agent != "" {
			agent = d.Agent
		}
	}
	if agent != "claude" {
		return proto.LauncherRepair{Unsupported: true, Output: agent + " is not updated by the node"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, launcherRepairTimeout)
	defer cancel()
	cmd := loginShell(ctx, claudeUpdateScript+" </dev/null")
	killGroupOnCancel(cmd)
	out, err := cmd.CombinedOutput()
	res := proto.LauncherRepair{Output: clipText(redactSecrets(lastLines(strings.TrimSpace(dropShellStartupNoise(string(out))), 6)), 400)}
	switch {
	case ctx.Err() != nil:
		res.Output = strings.TrimSpace(res.Output + "\n" + fmt.Sprintf("timed out after %s", launcherRepairTimeout))
	case err != nil:
		if res.Output == "" {
			res.Output = err.Error()
		}
	default:
		res.OK = true
	}
	return res, nil
}

// dropShellStartupNoise removes the two warnings an interactive bash prints when it has no terminal (a node run as a
// daemon, CI), Ubuntu's hint on how to use sudo, and the "exit" it echoes last when the script runs the exit builtin:
// they are not the repair's output and would crowd it out of the last lines the user sees.
func dropShellStartupNoise(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\r\n"), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !isShellStartupNoise(strings.TrimRight(line, "\r")) {
			kept = append(kept, line)
		}
	}
	// Only a script ending in the exit builtin makes bash echo this; "claude update" itself never does.
	if last := len(kept) - 1; last >= 0 && strings.TrimRight(kept[last], "\r") == "exit" {
		kept = kept[:last]
	}
	return strings.Join(kept, "\n")
}

func isShellStartupNoise(line string) bool {
	// Ubuntu's /etc/bash.bashrc prints these to a sudo-group user until ~/.sudo_as_admin_successful exists.
	if strings.HasPrefix(line, `To run a command as administrator (user "`) && strings.HasSuffix(line, `"), use "sudo <command>".`) ||
		line == `See "man sudo_root" for details.` {
		return true
	}
	shell, msg, ok := strings.Cut(line, ": ")
	if !ok || shell == "" || strings.ContainsAny(shell, " \t") {
		return false
	}
	return msg == "no job control in this shell" ||
		strings.HasPrefix(msg, "cannot set terminal process group (") && strings.HasSuffix(msg, "): Inappropriate ioctl for device")
}
