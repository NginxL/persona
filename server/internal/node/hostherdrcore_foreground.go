package node

import (
	"runtime"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/herdrcore"
)

// agentForegrounds puts the agent in front where herdr-core names a program above it. herdr-core on Windows reports
// the first program its shell started as the one in front, not what runs in front of that, so an agent a wrapper
// started (a start runs claude from Git Bash: bash.exe, two more bash, then claude.exe) stays unseen, and the start
// waits for a claude that never shows until it fails (#11214). A terminal whose foreground is no agent gets the
// nearest claude or codex below that program instead, with its own pid: the one Claude Code names its session file
// by. The process table is read once per call, and only when some terminal needs it. Elsewhere herdr-core reports the
// program in front itself, and nothing changes.
func agentForegrounds(ts []herdrcore.Terminal) {
	if runtime.GOOS != "windows" {
		return
	}
	var children map[int][]int
	var procs map[int]proc
	for i := range ts {
		fg := ts[i].Foreground
		if fg == nil || fg.Pid <= 0 || hcAgentProgram(fg.Name) != "" || len(fg.Argv) > 0 && hcAgentProgram(fg.Argv[0]) != "" {
			continue
		}
		if procs == nil {
			var err error
			if procs, err = winProcs(); err != nil {
				return
			}
			children = procChildren(procs)
		}
		if pid := agentBelow(procs, children, fg.Pid); pid > 0 {
			name := procs[pid].cmd
			ts[i].Foreground = &herdrcore.Foreground{Pid: pid, Name: name, Argv: []string{name}, Cwd: fg.Cwd}
		}
	}
}

// procChildren is each process's children, in pid order.
func procChildren(procs map[int]proc) map[int][]int {
	children := map[int][]int{}
	for pid, p := range procs {
		if p.ppid > 0 && p.ppid != pid {
			children[p.ppid] = append(children[p.ppid], pid)
		}
	}
	for _, c := range children {
		slices.Sort(c)
	}
	return children
}

// agentBelow is the nearest claude or codex process below pid, breadth first; 0 when there is none.
func agentBelow(procs map[int]proc, children map[int][]int, pid int) int {
	seen := map[int]bool{pid: true}
	queue := slices.Clone(children[pid])
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		if hcAgentProgram(strings.ToLower(procs[p].cmd)) != "" {
			return p
		}
		queue = append(queue, children[p]...)
	}
	return 0
}
