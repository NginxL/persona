package tokenuse

import "slices"

// FileAgents are the agents whose usage can be reconstructed from local records.
// Keep runtime event admission and full-window replacement on the same boundary.
func FileAgents() []string { return []string{Claude, Codex, Traex, Pi, Omp, Opencode} }

// FileMetered reports whether an agent uses local records rather than runtime events.
func FileMetered(agent string) bool { return slices.Contains(FileAgents(), agent) }
