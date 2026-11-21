package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// sourcePermission captures mode while the node handles the originating call.
// A claimed session id alone is insufficient: the socket must descend from the
// live process that owns that session. Unsupported/ambiguous runtimes state none.
func (n *Node) sourcePermission(c net.Conn, session, name string, args json.RawMessage) proto.SourcePermission {
	pid, err := peerPID(c)
	if err != nil {
		return proto.SourcePermission{}
	}
	return n.sourcePermissionIn(lineage(pid, parentPID), session, name, args)
}

func (n *Node) sourcePermissionIn(pids []int, session, name string, args json.RawMessage) proto.SourcePermission {
	switch name {
	case "agent_send", "report_to_supervisor", "task_review", "task_plan", "task_retry", "conversation_send":
	default:
		return proto.SourcePermission{}
	}
	if session == "" {
		return proto.SourcePermission{}
	}
	live := n.liveClaudes()
	// The nearest known Agent owns the socket's words. A nested Agent must not
	// name its more privileged parent and inherit that parent's class.
	roots := map[int]string{}
	n.mu.Lock()
	for id, p := range n.procs {
		if p != nil && p.cmd != nil && p.cmd.Process != nil {
			roots[p.cmd.Process.Pid] = id
		}
	}
	n.mu.Unlock()
	for _, c := range live {
		if id, ok := roots[c.pid]; ok && id != c.SessionID {
			return proto.SourcePermission{}
		}
		roots[c.pid] = c.SessionID
	}
	nearest := ""
	for _, pid := range pids {
		if id, ok := roots[pid]; ok {
			nearest = id
			break
		}
	}
	if nearest != session {
		return proto.SourcePermission{}
	}
	evidence := proto.SourcePermission{At: time.Now().UTC(), Session: session, Digest: proto.SourceCallDigest(name, args)}
	n.mu.Lock()
	p := n.procs[session]
	managed := p != nil && p.agent == "claude" && p.cmd != nil && p.cmd.Process != nil && slices.Contains(pids, p.cmd.Process.Pid) && !p.born.IsZero()
	n.mu.Unlock()
	if managed && p.settingsMu.TryLock() {
		select {
		case <-p.exited:
		default:
			p.mu.Lock()
			evidence.Class = permissionClass(p.mode)
			p.mu.Unlock()
			evidence.Generation = fmt.Sprintf("%d/%d", p.cmd.Process.Pid, p.born.UnixNano())
		}
		p.settingsMu.Unlock()
	}
	if managed {
		if evidence.Valid() {
			return evidence
		}
		return proto.SourcePermission{}
	}
	for _, c := range live {
		if c.SessionID != session || !slices.Contains(pids, c.pid) {
			continue
		}
		mark := sourceGeneration(c.pid)
		if !sourceRegistryMatches(c, mark) {
			continue
		}
		evidence.Class = permissionClass(peerSessionModeAt(context.Background(), n, c, session))
		evidence.Generation = fmt.Sprintf("%d/%s", c.pid, mark)
		// Recheck after reading the transcript: never attest a reused process id.
		if evidence.Valid() && sourceGeneration(c.pid) == mark {
			return evidence
		}
	}
	return proto.SourcePermission{}
}

func permissionClass(mode string) string {
	switch mode {
	case "bypassPermissions":
		return peerModeBypass
	case "default", "acceptEdits", "auto", "dontAsk":
		return peerModePrompting
	}
	return ""
}

func (n *Node) putSourcePermission(c net.Conn, params map[string]any, session, name string, args json.RawMessage) {
	if p := n.sourcePermission(c, session, name, args); p.Valid() {
		params["source_permission"] = p
	}
}

type sourceDeliveryKey struct{}

// Only session.source_send sets this context; ordinary session.send ignores any
// claimed class, including through an older devices manager.
func withSourceDelivery(ctx context.Context, params json.RawMessage) context.Context {
	var p struct {
		Source proto.SourcePermission `json:"source_permission"`
	}
	if json.Unmarshal(params, &p) == nil && p.Source.Valid() {
		return context.WithValue(ctx, sourceDeliveryKey{}, p.Source.Class)
	}
	return ctx
}

// Registry data must describe this process generation, not a file left by a
// previous holder of the PID. macOS Claude registries do not expose a comparable
// procStart, so require a file written after the kernel creation timestamp.
func sourceRegistryMatches(c claudeProc, mark string) bool {
	if mark == "" {
		return false
	}
	if strings.HasPrefix(mark, "darwin:") {
		var sec, usec int64
		if _, err := fmt.Sscanf(mark, "darwin:%d:%d", &sec, &usec); err != nil {
			return false
		}
		st, err := os.Stat(filepath.Join(c.dir, strconv.Itoa(c.pid)+".json"))
		return err == nil && !st.ModTime().Before(time.Unix(sec, usec*1000))
	}
	return c.ProcStart != "" && claudeFileStart(c.ProcStart) == mark
}
