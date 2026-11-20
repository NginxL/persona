package node

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// assistantExecution reads only registered assistant processes or their owning runtime.
// Launcher preferences, a warm spare, and transcripts do not prove execution.
func (n *Node) assistantExecution(ctx context.Context, id string) proto.AssistantExecution {
	out := proto.AssistantExecution{State: proto.ExecutionUnknown}
	if id == "" {
		return out
	}
	n.mu.Lock()
	p := n.procs[id]
	recovery := n.recoveries[id]
	restarting := recovery != nil && recovery.running
	n.mu.Unlock()
	if p != nil && p.alive() {
		out.Source, out.Harness = "native", p.agent
		out.Mode, out.ModeNote = p.executionMode()
		out.State = proto.ExecutionRunning
		if p.ready != nil {
			select {
			case <-p.ready:
			default:
				out.State = proto.ExecutionStarting
			}
		}
		host, _ := os.Hostname()
		out.Location = executionLocation(&proto.ExecutionLocation{
			Host: host, Container: os.Getenv("PERSONA_EXECUTION_CONTAINER"),
		})
		return out
	}
	if restarting {
		out.State = proto.ExecutionStarting
		return out
	}
	if row, h, ok := n.runtimes.rowOf(id); ok && n.isAssistantSession(id) {
		return runtimeAssistantExecution(ctx, id, row, h)
	}
	if n.isAssistantSession(id) {
		if _, _, known := n.runtimes.belongs(id); known || runtimeSessionID(id) {
			out.Source = "runtime"
			if h, _, _ := n.runtimes.belongs(id); h == nil {
				out.State = proto.ExecutionOffline
			}
		} else {
			out.State = proto.ExecutionStopped
		}
	}
	return out
}

func runtimeAssistantExecution(ctx context.Context, id string, row proto.Session, h *runtimeConn) proto.AssistantExecution {
	out := proto.AssistantExecution{
		State: proto.ExecutionUnknown, Source: "runtime", Harness: row.Agent, HarnessLabel: row.AgentLabel,
	}
	switch row.Status {
	case proto.StatusWorking, proto.StatusBlocked:
		out.State = proto.ExecutionRunning
	case proto.StatusOffline:
		out.State = proto.ExecutionOffline
	}
	if row.Starting {
		out.State = proto.ExecutionStarting
	}
	h.mu.Lock()
	capable, peer := h.hello.Caps.Execution, h.peer
	h.mu.Unlock()
	if !capable || peer == nil {
		return out
	}
	// Some runtimes execute elsewhere even when they connect through a local socket.
	// Never attach this node's hostname or container to an external runtime's row.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var report proto.AssistantExecution
	if err := peer.Call(ctx, "session.execution", map[string]string{"session_id": id}, &report); err != nil {
		// The inventory can still tell us which harness owns the conversation.
		out.State = proto.ExecutionUnknown
		return out
	}
	switch report.State {
	case proto.ExecutionStarting, proto.ExecutionRunning, proto.ExecutionStopped, proto.ExecutionOffline:
		out.State = report.State
		out.Location = executionLocation(report.Location)
	default:
		out.State = proto.ExecutionUnknown
	}
	return out
}

func executionLocation(in *proto.ExecutionLocation) *proto.ExecutionLocation {
	if in == nil {
		return nil
	}
	label := func(value string) string {
		r := []rune(strings.Join(strings.Fields(value), " "))
		return string(r[:min(len(r), 160)])
	}
	out := &proto.ExecutionLocation{Host: label(in.Host), Container: label(in.Container)}
	if *out == (proto.ExecutionLocation{}) {
		return nil
	}
	return out
}
