package node

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTaskStrictBusyExecutionNeverAdoptsOldModel(t *testing.T) {
	for _, background := range []bool{false, true} {
		for _, strict := range []bool{false, true} {
			for _, inherited := range []bool{false, true} {
				for _, change := range []string{"model", "effort", "fingerprint", "launcher", "interactive", "interactive effort", "matching", "round model"} {
					if inherited && change == "launcher" {
						continue // an omitted launcher inherits rather than asking for a different launcher
					}
					if change == "round model" && !background {
						continue // background-only work reaches the new round-tool fallback without a foreground wait
					}
					name := change + "/busy"
					if background {
						name = change + "/background"
					}
					if strict {
						name += "/strict"
					}
					if inherited {
						name += "/inherited"
					}
					t.Run(name, func(t *testing.T) {
						n := codexTestNode(t)
						withFakeHub(t, n)
						old := modelExecution{Strict: strict, Model: "old", Effort: "low", UseEffort: true, Fingerprint: "before", Origin: "task"}
						if change == "interactive effort" {
							old.Interactive = true
						}
						asked := old
						launcher := "claude"
						switch change {
						case "interactive":
							asked.Interactive = true
						case "model", "round model":
							asked.Model = "new"
						case "effort":
							asked.Effort = "high"
						case "fingerprint":
							asked.Fingerprint = "after"
						case "launcher":
							launcher = "other"
						}
						if inherited {
							launcher = ""
						}
						modelProbeCache.Lock()
						modelProbeCache.results[n.home+"\x00claude\x00"+asked.Model] = modelProbeResult{at: time.Now(), agent: "claude"}
						modelProbeCache.Unlock()
						in := &stdinBuf{}
						p := &assistantProc{id: testThread, agent: "claude", launcher: "claude", cwd: t.TempDir(), stdin: in, exited: make(chan struct{}), born: time.Now(),
							mode: "default", effort: old.Effort, model: old, fingerprint: n.executionFingerprint("claude", old), busy: !background}
						if change == "interactive effort" {
							// Its execution fingerprint still matches, but the interactive process started at another effort.
							p.tui, p.effort = true, "high"
						}
						if change == "round model" {
							p.round, p.toolSet = true, roundToolSet
						}
						if background {
							p.tasks = []string{"background work"}
						}
						n.procs[testThread] = p
						n.state.put(testThread, sessionState{Launcher: "claude", Cwd: p.cwd, StartedAt: time.Now().UTC(), Model: old.Model, ModelEffort: old.Effort, ModelFingerprint: old.Fingerprint, Tasks: p.tasks, Origin: "task"})
						_, err := n.assistantSend(context.Background(), testThread, p.cwd, launcher, "scheduled request", nil, "default", "", asked)
						refused := strict && change != "matching"
						if refused {
							if err == nil || err.Error() != "task execution context differs from this conversation; start a new conversation" || in.String() != "" {
								t.Fatalf("strict mismatch delivered or wrong refusal: error=%v stdin=%q", err, in.String())
							}
						} else if err != nil || !strings.Contains(in.String(), "scheduled request") {
							t.Fatalf("matching/legacy send refused: error=%v stdin=%q", err, in.String())
						}
						if !p.alive() || n.procs[testThread] != p || p.model != old || (len(p.tasks) != 0) != background {
							t.Fatal("active process or its background work changed")
						}
						p.mu.Lock()
						retire := p.retire
						p.mu.Unlock()
						if retire != (change == "round model" && !strict) {
							t.Fatalf("round process retirement changed: %v", retire)
						}
						state, _ := n.state.get(testThread)
						if state.Model != old.Model || state.ModelEffort != old.Effort || state.ModelFingerprint != old.Fingerprint || state.Origin != "task" {
							t.Fatalf("running execution was misreported: %+v", state)
						}
					})
				}
			}
		}
	}
}
