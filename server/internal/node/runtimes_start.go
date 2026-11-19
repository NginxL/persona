package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// runtimeOf is the runtime that runs launcher name in a terminal host's start: the one offering it, unless the login
// shell or a launcher kept in Persona has that name. Those run instead, as that host's pickers list them under the
// name: a managed launcher made from a Paseo provider of the same name is started with personad launch, not through
// Paseo. nil when no runtime runs it. Where every start goes to an engine runtime that runs only its own launchers
// (Paseo with caps.same_kind) its provider of that name runs instead, whatever else has it: runtimeOfIn. Engine
// runtimes this machine does not enable never offer launchers (runtimeRegistry.hostOff), so they cannot take a name
// from an enabled runtime.
func (n *Node) runtimeOf(name string) *runtimeConn {
	if shellLauncherNames()[name] {
		return nil
	}
	if _, managed := n.managedLauncher(name); managed {
		return nil
	}
	return n.runtimes.launcherRuntime(name)
}

// engineHostOff: runtime is an engine host (Paseo, Cindy) this machine does not enable. Its providers start nothing
// here and are not listed (launchers.list), even while its runtime is connected.
func (n *Node) engineHostOff(runtime string) bool {
	_, engine := engineRuntimes[runtime]
	return engine && !n.hostEnabled(runtime)
}

// startRuntime is the runtime a terminal host's start with launcher name goes to, and the model it starts with when
// the form chose none: runtimeOf, or the runtime's own agy launcher for a managed Antigravity launcher, whatever its
// name. Checks about an agent already in a terminal (a launcher switch) ask here; a new start asks startRuntimeIn.
// nil when the start is not a runtime's.
func (n *Node) startRuntime(name string) (h *runtimeConn, model string) {
	if h = n.runtimeOf(name); h != nil {
		return h, ""
	}
	if m, ok := n.managedLauncher(name); ok && m.AgentType() == "agy" && !shellLauncherNames()[name] {
		return n.runtimes.launcherRuntime("agy"), m.Model
	}
	return nil, ""
}

// runtimeStart starts a session in the runtime a start in ctx with the launcher goes to (startRuntimeIn: on a host
// whose engine runtime runs only its own launchers, that runtime for every name it offers); false when none does.
func (n *Node) runtimeStart(ctx context.Context, p runtimeStartParams) (*proto.Session, *proto.StartInstead, error, bool) {
	h, model := n.startRuntimeIn(ctx, p.Launcher)
	if h == nil {
		return nil, nil, nil, false
	}
	if p.Model == "" {
		p.Model = model
	}
	s, instead, err := n.runtimeStartOn(ctx, h, p)
	return s, instead, err, true
}

// runtimeStartOn starts a session in runtime h (session.start): the directory, the first message, the launcher picked
// and what the form chose with it (model, permission mode, effort), the kind of agent a launcher it does not have
// starts (agent_type, caps.same_kind), the session to continue. Fields are sent only when set: a runtime that knows
// none of them still starts its own way. Its answer is the session and what it was asked for and does not run with
// (instead: a runtime that runs only its own launchers says it), nil when it runs as asked.
func (n *Node) runtimeStartOn(ctx context.Context, h *runtimeConn, p runtimeStartParams) (*proto.Session, *proto.StartInstead, error) {
	if p.PermissionMode == "plan" {
		// Only a runtime that promises read-only starts (caps.readonly) gets one: without that promise its provider
		// may ignore the mode, or fall back to a writable one.
		h.mu.Lock()
		readonly := h.hello.Caps.Readonly
		h.mu.Unlock()
		if !readonly {
			return nil, nil, readonlyStartUnsupported()
		}
	}
	if p.AgentType != "" && !sameKind(h) {
		p.AgentType = "" // only a runtime that runs a launcher it lacks by its kind is told the kind
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	params := map[string]any{"cwd": p.Cwd, "prompt": p.Prompt}
	for k, v := range map[string]string{"launcher": p.Launcher, "model": p.Model, "permission_mode": p.PermissionMode,
		"effort": p.Effort, "workspace_id": p.WorkspaceID, "resume": p.Resume, "session_id": p.SessionID, "agent_type": p.AgentType} {
		if v != "" {
			params[k] = v
		}
	}
	if p.Shell {
		params["shell_launcher"] = true
	}
	if p.OpenElsewhere {
		params["open_elsewhere"] = true
	}
	var res struct {
		Session json.RawMessage     `json:"session"`
		Instead *proto.StartInstead `json:"instead"`
	}
	if err := h.peer.Call(ctx, "session.start", params, &res); err != nil {
		return nil, nil, err
	}
	var s proto.Session
	var row struct {
		Transcript string `json:"transcript"`
	}
	if len(res.Session) > 0 {
		if err := json.Unmarshal(res.Session, &s); err != nil {
			return nil, nil, fmt.Errorf("%s: the started session: %w", h.label(), err)
		}
		_ = json.Unmarshal(res.Session, &row)
	}
	started, err := n.listStarted(h, s, row.Transcript)
	if err != nil {
		return nil, nil, err
	}
	return started, cleanInstead(res.Instead), nil
}

// listStarted lists a session h just started right away, before the runtime's next runtime.sessions, so the chat
// that opens next finds it. Who answers for it is decided as for any list (recomputeOwners): a runtime that starts
// sessions elsewhere (pi's companion starts them in pi-web) reports them offline, and the one running it, if it has
// listed it already, keeps it. transcript: the Claude transcript the started row names as its conversation (a session
// taken over keeps its own, runtimetranscript.go), taken as a list naming it would be: a watch right after the start
// is the transcript's, and the next list naming it moves nothing (transcriptMoved).
func (n *Node) listStarted(h *runtimeConn, started proto.Session, transcript string) (*proto.Session, error) {
	list := normalizeRuntimeSessions(h, []proto.Session{started})
	if len(list) == 0 {
		return nil, fmt.Errorf("%s started a session without a valid id", h.label())
	}
	s := list[0]
	known := !h.isDaemon() && namesTranscript(s.SessionID, transcript) && n.transcriptPath(transcript) != ""
	b := n.runtimes
	b.mu.Lock()
	h.mu.Lock()
	found := false
	for i := range h.sessions {
		if h.sessions[i].SessionID == s.SessionID {
			h.sessions[i], found = s, true
		}
	}
	if !found {
		h.sessions = append(h.sessions, s)
	}
	if known {
		if h.transcripts == nil {
			h.transcripts = map[string]string{}
		}
		h.transcripts[s.SessionID] = transcript
	}
	h.mu.Unlock()
	b.recomputeOwners()
	if startedHere(s) { // not while it is only starting: a takeover that fails leaves the session this machine's (#8130)
		b.remember(h, s.SessionID, time.Now())
	}
	b.noteStarted(h, s.SessionID, time.Now())
	var tell *runtimeConn // runs it already, and is to hear that the hub started it (runtime.driven)
	if o := b.owner[s.SessionID]; o != nil && o != h {
		o.mu.Lock()
		for _, r := range o.sessions {
			if r.SessionID == s.SessionID {
				s = r // the running one's row: its status, title, agent
				if r.Status != proto.StatusOffline && !o.drivenTold[r.SessionID] {
					if o.drivenTold == nil {
						o.drivenTold = map[string]bool{}
					}
					o.drivenTold[r.SessionID] = true
					tell = o
				}
			}
		}
		o.mu.Unlock()
	}
	b.mu.Unlock()
	if tell != nil && tell.peer != nil {
		_ = tell.peer.Notify("runtime.driven", map[string]any{"session_id": s.SessionID})
	}
	n.publish(n.sessionList(context.Background(), false))
	return &s, nil
}

// noteRuntimeWorking marks h's cached row for id working as soon as session.send succeeds, before the runtime's
// debounced runtime.sessions push arrives, unless the session is currently waiting on a user interaction.
func (n *Node) noteRuntimeWorking(h *runtimeConn, id string) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	for i := range h.sessions {
		if h.sessions[i].SessionID == id && h.sessions[i].Status != proto.StatusBlocked {
			h.sessions[i].Status = proto.StatusWorking
			h.sessions[i].UpdatedAt = time.Now().UTC()
		}
	}
	h.mu.Unlock()
}
