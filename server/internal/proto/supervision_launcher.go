package proto

import (
	"cmp"
	"slices"
)

// LauncherSupervisionPolicy is descriptive inventory, never execution authority.
// Native Claude/Codex use node-owned channels; runtime launchers must explicitly
// advertise their verified channel. Unknown sources and engine kinds stay closed.
func LauncherSupervisionPolicy(l Launcher) bool {
	if l.Name == "" || IsExternalLauncher(l.Name) {
		return false
	}
	switch l.Source {
	case "", "zshrc", "path", "builtin":
		return l.Agent == "" || l.Agent == "claude" || l.Agent == "codex"
	case "persona":
		// Logical-conversation native starts use the app-server adapter for
		// managed Codex providers as well as shell launchers.
		return l.Agent == "" || l.Agent == "claude" || l.Agent == "codex" || l.Assistant && l.SupervisionPolicy
	default:
		return l.Assistant && l.SupervisionPolicy
	}
}

// SupervisionLauncherNames returns stable labels only from the supplied machine's
// inventory. It adds no presumed built-ins, aliases or other machines' launchers.
func SupervisionLauncherNames(list []Launcher) []string {
	names := []string{}
	for _, l := range list {
		if LauncherSupervisionPolicy(l) {
			names = append(names, cmp.Or(l.DisplayName, l.Name))
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
