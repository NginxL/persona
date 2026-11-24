package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const (
	codexTakeoverPrepare = "session.codex.takeover.prepare"
	codexTakeoverCommit  = "session.codex.takeover.commit"
	takeoverPlanTTL      = 2 * time.Minute
	takeoverExitWait     = 20 * time.Second
)

type codexWriter struct {
	PID   int
	Start string
}

type codexTakeoverPlan struct {
	proto.CodexTakeover
	Token              string `json:"token"`
	Cwd                string `json:"cwd"`
	TranscriptPath     string `json:"transcript_path"`
	ContinuationPrompt string `json:"continuation_prompt"`
	id                 string
	source             codexSource
	writers            []codexWriter
	expires            time.Time
}

type codexTakeovers struct {
	mu     sync.Mutex
	plans  map[string]codexTakeoverPlan
	failed map[string]bool
	// Tests replace the OS boundary, never the confirmation and identity checks.
	writers  func(codexSource) ([]codexWriter, error)
	shared   func(context.Context, codexSource) ([]string, error)
	stop     func(context.Context, codexWriter) error
	lockHeld func(codexSource) bool
}

func takeoverRefusal(key string) error {
	return refusal("codex_takeover_"+key, i18n.Err("codex-takeover."+key).Error(), nil)
}

func (n *Node) codexWriterLockHeld(src codexSource) bool {
	if n != nil && n.takeovers.lockHeld != nil {
		return n.takeovers.lockHeld(src)
	}
	return codexWriterLockHeldOS(src)
}

// codexLiveCannotQuit reports whether discoverCodex saw the rollout's current holder as a process that cannot be
// gracefully exited without OS signals (ordinary external terminal, Windows, or app-server).
func (n *Node) codexLiveCannotQuit(id string) bool {
	if n == nil || n.codices == nil {
		return false
	}
	n.codices.mu.Lock()
	canQuit, tracked := n.codices.liveCanQuit[id]
	n.codices.mu.Unlock()
	return tracked && !canQuit
}

// codexDesktopConflict reports whether a Codex App process could still own or write threads in src.home.
// When fresh is false and pass is non-nil, desktop bus availability is reused from the per-home list pass cache.
func (n *Node) codexDesktopConflict(src codexSource, fresh bool, pass *codexListPass) bool {
	if src.home == "" {
		return true
	}
	if !fresh && pass != nil {
		if pass.desktopAvailable(src.home) {
			return true
		}
	} else if codexDesktopAvailable(src.home) {
		return true
	}
	if n == nil || n.codices == nil {
		return false
	}
	if !fresh {
		n.codices.mu.Lock()
		conflict := n.codices.stdioHomes[src.home]
		n.codices.mu.Unlock()
		return conflict
	}
	n.codices.mu.Lock()
	checked, scan := n.codices.checked, n.codices.scan
	n.codices.mu.Unlock()
	var procs []codexProcess
	complete := true
	switch {
	case checked != nil:
		procs, complete = checked()
	case scan != nil:
		procs = scan()
	default:
		procs, complete = scanCodexProcessesChecked()
	}
	if !complete {
		return true
	}
	for _, p := range procs {
		if p.endpoint == "stdio://" && canonicalCodexHome(firstNonEmpty(p.home, n.codexHome()), n.home) == src.home {
			return true
		}
	}
	return false
}

func (n *Node) takeoverSource(id string) (codexSource, bool) {
	src, ok := n.codexSource(id)
	if !ok || src.path == "" || src.thread == "" || n.codexBlocked(id) ||
		n.isAssistantSession(id) || n.ownedAssistantSource(src.thread) {
		return codexSource{}, false
	}
	if _, live := n.agent(id); live {
		return codexSource{}, false
	}
	o := readCodexOrigin(src.path)
	if !o.interactive() {
		return codexSource{}, false
	}
	if _, _, err := n.codexOwner(id); !errors.Is(err, errCodexNoOwner) {
		return codexSource{}, false
	}
	return src, true
}

// takeoverHost selects an enabled non-Orca terminal host.Launcher (herdr, herdr-core, tern, ...).
func (n *Node) takeoverHost(ctx context.Context) (context.Context, error) {
	if n == nil || n.hosts == nil {
		return ctx, noEngineRefusal()
	}
	var candidates []string
	if start := n.startKind(ctx); start != "" && start != host.Orca && !proto.RuntimeEngine(start) {
		candidates = append(candidates, start)
	}
	for _, kind := range n.engineChoice().Hosts {
		if kind == "" || kind == host.Orca || proto.RuntimeEngine(kind) || slices.Contains(candidates, kind) {
			continue
		}
		candidates = append(candidates, kind)
	}
	var firstErr error
	for _, kind := range candidates {
		if err := n.checkStartHost(kind); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if _, err := hostCapFor[host.Launcher](n, kind); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return withStartHost(ctx, kind), nil
	}
	if firstErr != nil {
		return ctx, firstErr
	}
	return ctx, noEngineRefusal()
}

// canContinueCodex reports whether this node can at least start a fresh session for continuation.
func (n *Node) canContinueCodex(ctx context.Context) bool {
	if n == nil || n.hosts == nil {
		return false
	}
	kind := n.startKind(ctx)
	if kind == "" {
		return false
	}
	if err := n.checkStartHost(kind); err != nil {
		return false
	}
	if proto.RuntimeEngine(kind) {
		return n.runtimes != nil
	}
	_, err := hostCapFor[host.Launcher](n, kind)
	return err == nil
}

// The listing is advisory. Preparation and commit both inspect the actual writer and lock again.
func (n *Node) codexTakeoverRow(id string, src codexSource, m quickMeta, running bool) *proto.CodexTakeover {
	return n.codexTakeoverRowIn(id, src, m, running, nil)
}

func (n *Node) codexTakeoverRowIn(id string, src codexSource, m quickMeta, running bool, pass *codexListPass) *proto.CodexTakeover {
	o := readCodexOrigin(src.path)
	if src.path == "" || src.thread == "" || !o.interactive() {
		return nil
	}
	_, hostErr := n.takeoverHost(context.Background())
	hasShared := n.codexSharedRemote(src.home) != nil
	canResumeTarget := hostErr == nil || hasShared
	if !canResumeTarget && !n.canContinueCodex(context.Background()) {
		return nil
	}
	state := &proto.CodexTakeover{Running: running, CanResume: canResumeTarget}
	n.takeovers.mu.Lock()
	failed := n.takeovers.failed[id]
	n.takeovers.mu.Unlock()
	switch {
	case failed || encryptedCodexFailure(m.turnErr.code+" "+m.turnErr.text):
		state.CanResume, state.Reason = false, "encrypted_context"
	case !canResumeTarget:
		state.CanResume, state.Reason = false, "unavailable"
	case o.desktop && (running || n.codexDesktopConflict(src, false, pass) || n.codexWriterLockHeld(src)):
		state.CanResume, state.Reason = false, "unavailable"
	case !running && n.codexWriterLockHeld(src):
		state.Running = true
		state.CanResume, state.Reason = false, "unavailable"
	case running && (runtime.GOOS == "windows" || n.codexLiveCannotQuit(id)):
		state.CanResume, state.Reason = false, "unavailable"
	}
	return state
}

func encryptedCodexFailure(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "invalid_encrypted_content") ||
		strings.Contains(text, "failed to decrypt") ||
		strings.Contains(text, "could not decrypt") ||
		strings.Contains(text, "could not be decrypted") ||
		strings.Contains(text, "unable to decrypt") ||
		strings.Contains(text, "encryption key is no longer available")
}

func (n *Node) prepareCodexTakeover(ctx context.Context, id string) (*codexTakeoverPlan, error) {
	src, ok := n.takeoverSource(id)
	if !ok {
		return nil, takeoverRefusal("unsupported")
	}
	_, hostErr := n.takeoverHost(ctx)
	hasShared := n.codexSharedRemote(src.home) != nil
	canResumeTarget := hostErr == nil || hasShared
	if !canResumeTarget && !n.canContinueCodex(ctx) {
		return nil, hostErr
	}
	st, err := os.Stat(src.path)
	if err != nil {
		return nil, err
	}
	m := n.meta(src.path, st)
	state := n.codexTakeoverRow(id, src, m, false)
	if state == nil || m.cwd == "" {
		return nil, takeoverRefusal("unsupported")
	}
	o := readCodexOrigin(src.path)
	writers, err := n.takeoverWriters(src)
	if err != nil {
		state.CanResume, state.Reason = false, "unavailable"
	}
	state.Running = len(writers) > 0
	if !state.Running && n.codexWriterLockHeld(src) {
		state.Running = true
		state.CanResume, state.Reason = false, "unavailable"
	}
	if o.desktop && (state.Running || n.codexDesktopConflict(src, true, nil)) {
		state.CanResume, state.Reason = false, "unavailable"
	}
	if state.Running && runtime.GOOS == "windows" {
		state.CanResume, state.Reason = false, "unavailable"
	}
	if state.Running && state.CanResume && n.takeovers.stop == nil {
		if _, err := n.prepareCodexWriterQuit(ctx, writers[0]); err != nil {
			state.CanResume, state.Reason = false, "unavailable"
		}
	}
	recent, err := codexContinuation(src.path)
	if err != nil {
		return nil, err
	}
	p := codexTakeoverPlan{CodexTakeover: *state, Token: newUUID(), Cwd: m.cwd, TranscriptPath: src.path,
		ContinuationPrompt: prompts.RenderCodexContinuation(prompts.CodexContinuation{Path: src.path, Messages: recent}),
		id:                 id, source: src, writers: writers, expires: time.Now().Add(takeoverPlanTTL)}
	n.takeovers.mu.Lock()
	defer n.takeovers.mu.Unlock()
	if n.takeovers.plans == nil {
		n.takeovers.plans = map[string]codexTakeoverPlan{}
	}
	for token, old := range n.takeovers.plans {
		if time.Now().After(old.expires) || old.id == id {
			delete(n.takeovers.plans, token)
		}
	}
	n.takeovers.plans[p.Token] = p
	return &p, nil
}

func (n *Node) commitCodexTakeover(ctx context.Context, id, token string, confirm bool) (session *proto.Session, err error) {
	defer n.typing.lock(id)()
	defer func() {
		if err != nil && encryptedCodexFailure(err.Error()) {
			n.takeovers.mu.Lock()
			if n.takeovers.failed == nil {
				n.takeovers.failed = map[string]bool{}
			}
			n.takeovers.failed[id] = true
			n.takeovers.mu.Unlock()
			err = takeoverRefusal("continue_only")
		}
	}()
	n.takeovers.mu.Lock()
	p, ok := n.takeovers.plans[token]
	if ok {
		delete(n.takeovers.plans, token) // A confirmation authorizes at most one attempt.
	}
	n.takeovers.mu.Unlock()
	if !ok || p.id != id || time.Now().After(p.expires) {
		return nil, takeoverRefusal("expired")
	}
	if !p.CanResume {
		return nil, takeoverRefusal("continue_only")
	}
	if p.Running && !confirm {
		return nil, takeoverRefusal("confirmation_required")
	}
	src, ok := n.takeoverSource(id)
	if !ok || src != p.source {
		return nil, takeoverRefusal("changed")
	}
	o := readCodexOrigin(src.path)
	if o.desktop && n.codexDesktopConflict(src, true, nil) {
		return nil, takeoverRefusal("writer_busy")
	}
	hostCtx, hostErr := n.takeoverHost(ctx)
	var sharedRemote *codexRemote
	if hostErr != nil {
		sharedRemote = n.codexSharedRemote(src.home)
		if sharedRemote == nil {
			return nil, hostErr
		}
	}
	current, err := n.takeoverWriters(src)
	if err != nil || !slices.Equal(current, p.writers) {
		return nil, takeoverRefusal("changed")
	}
	var argv []string
	if hostErr == nil {
		// Prove that a shared entry can be used before interrupting the old terminal.
		shared := n.takeovers.shared
		if shared == nil {
			shared = n.sharedTakeoverCodex
		}
		argv, err = shared(hostCtx, src)
		if err != nil {
			return nil, err
		}
		if src.thread != id && src.home != "" && !hasLeadingEnvArg(argv, "CODEX_HOME") {
			argv = append([]string{"CODEX_HOME=" + src.home}, argv...)
		}
		// Daemon startup may take a while. Never signal a replacement writer with an old confirmation.
		if current, err = n.takeoverWriters(src); err != nil || !slices.Equal(current, p.writers) {
			return nil, takeoverRefusal("changed")
		}
	}
	stop := n.takeovers.stop
	if stop == nil {
		stop = n.stopCodexWriter
	}
	for _, writer := range current {
		if !confirm {
			return nil, takeoverRefusal("confirmation_required")
		}
		if err := stop(ctx, writer); err != nil {
			return nil, err
		}
	}
	if current, err = n.takeoverWriters(src); err != nil || len(current) != 0 {
		return nil, takeoverRefusal("writer_busy")
	}
	if n.codexWriterLockHeld(src) || (o.desktop && n.codexDesktopConflict(src, true, nil)) {
		return nil, takeoverRefusal("writer_busy")
	}
	if err := settledCodexRollout(ctx, src.path); err != nil {
		return nil, err
	}
	if current, err = n.takeoverWriters(src); err != nil || len(current) != 0 || n.codexWriterLockHeld(src) || (o.desktop && n.codexDesktopConflict(src, true, nil)) {
		return nil, takeoverRefusal("writer_busy")
	}
	if hostErr != nil {
		// No terminal engine launcher is available: cold-activate directly on the shared app-server.
		if err := sharedRemote.coldResume(ctx, id, src, p.Cwd); err != nil {
			return nil, err
		}
		for _, s := range n.codexSessions(map[string]bool{}) {
			if s.SessionID == id {
				out := s
				return &out, nil
			}
		}
		return &proto.Session{
			SessionID:        id,
			Agent:            "codex",
			SourceKind:       "codex_app_server",
			SourceInstanceID: sharedRemote.spec.ID,
			SourceThreadID:   src.thread,
			Cwd:              p.Cwd,
			Status:           proto.StatusDone,
			Attached:         true,
			Capabilities:     &proto.SessionCapabilities{Read: true, Send: true, Attach: true, Approve: true},
		}, nil
	}
	launch, err := n.launcherFor(n.startKind(hostCtx))
	if err != nil {
		return nil, err
	}
	ws, err := n.workspaceFor(hostCtx, launch, p.Cwd)
	if err != nil {
		return nil, err
	}
	// A new tab is intentional: do not adopt or type into the source terminal.
	st := sessionState{Launcher: "codex", Cwd: p.Cwd}
	err = n.resumeCodex(hostCtx, launch, id, st, argv, "", ws, n.resumeTabName(id))
	if err != nil {
		return nil, err
	}
	a, ok := n.agent(id)
	if !ok {
		return nil, takeoverRefusal("writer_busy")
	}
	// A running TUI is not yet a usable resumed thread: the requested folder may
	// still need its trust dialog answered before the remote app-server loads it.
	if err := n.waitCodexTakeover(hostCtx, id, a.PaneID); err != nil {
		return nil, err
	}
	st.Pane = a.PaneID
	n.state.put(id, st)
	return n.sessionFor(id, a), nil
}

func hasLeadingEnvArg(argv []string, name string) bool {
	prefix := name + "="
	for _, w := range argv {
		if !strings.Contains(w, "=") || strings.HasPrefix(w, "-") {
			return false
		}
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// Takeover has its own budget for joining an existing thread on the shared server.
const codexTakeoverWait = 60 * time.Second

func (n *Node) waitCodexTakeover(ctx context.Context, id, pane string) error {
	wait, cancel := context.WithTimeout(ctx, codexTakeoverWait)
	defer cancel()
	for {
		if err := n.waitCodexBox(wait, pane, codexTakeoverWait); err != nil {
			if wait.Err() != nil {
				return takeoverRefusal("not_ready")
			}
			return err
		}
		// The TUI can briefly draw an empty composer before its trust dialog.
		// Only the original thread loaded and joined on the shared server proves readiness.
		if c, thread, err := n.codexOwner(id); err == nil {
			if !thread.joined {
				if err := c.join(wait, id, false); err != nil {
					return err
				}
			}
			if route, _ := n.codexRoute(id); route == codexRouteAppServer {
				return nil
			}
		} else if !errors.Is(err, errCodexNoOwner) {
			return err
		}
		if !sleepCtx(wait, codexStartPoll) {
			return takeoverRefusal("not_ready")
		}
	}
}

func (n *Node) takeoverWriters(src codexSource) ([]codexWriter, error) {
	if n.takeovers.writers != nil {
		return n.takeovers.writers(src)
	}
	// Discovery's shared process snapshot lasts ten seconds, which is unsafe at a writer handoff.
	procSnapshot.Lock()
	procSnapshot.at = time.Time{}
	procSnapshot.Unlock()
	n.codices.mu.Lock()
	scan := n.codices.checked
	n.codices.mu.Unlock()
	if scan == nil {
		scan = scanCodexProcessesChecked
	}
	procs, complete := scan()
	files, read := scanOpenRollouts(procs)
	if !complete || !read {
		return nil, takeoverRefusal("unavailable")
	}
	var writers []codexWriter
	for _, p := range procs {
		for _, path := range files[p.pid] {
			if !pathWithin(path, src.home) || rolloutThreadID(src.home, path) != src.thread {
				continue
			}
			// A shared/stdio app-server may own many other threads. Never stop it.
			if p.endpoint != "" {
				return nil, takeoverRefusal("unsupported")
			}
			mark := procStartMark(p.pid)
			if mark == "" {
				return nil, takeoverRefusal("unavailable")
			}
			writers = append(writers, codexWriter{p.pid, mark})
			break
		}
	}
	if len(writers) > 1 {
		return nil, takeoverRefusal("writer_busy")
	}
	return writers, nil
}

func (n *Node) sharedTakeoverCodex(ctx context.Context, src codexSource) ([]string, error) {
	socket := filepath.Join(src.home, "app-server-control", "app-server-control.sock")
	if _, err := codexSocketPath(socket); err != nil {
		start, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(start, "codex", "app-server", "daemon", "start")
		cmd.Env = append(withoutEnv(os.Environ(), []string{"CODEX_HOME"}), "CODEX_HOME="+src.home)
		// Output may contain local account/configuration details; it must not reach the API.
		if err := cmd.Run(); err != nil {
			return nil, takeoverRefusal("shared_unavailable")
		}
	}
	if _, _, err := validateCodexEndpoint("unix://" + socket); err != nil {
		return nil, takeoverRefusal("shared_unavailable")
	}
	n.discoverCodex()
	argv := []string{"codex", "--remote", "unix://" + socket}
	if src.home != "" && src.home != canonicalCodexHome(n.codexHome(), n.home) {
		argv = append([]string{"CODEX_HOME=" + src.home}, argv...)
	}
	return argv, nil
}

func settledCodexRollout(ctx context.Context, path string) error {
	first, err := os.Stat(path)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	second, err := os.Stat(path)
	if err != nil {
		return err
	}
	if first.Size() != second.Size() || !first.ModTime().Equal(second.ModTime()) {
		return takeoverRefusal("writer_busy")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if second.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], second.Size()-1); err != nil || last[0] != '\n' {
			return takeoverRefusal("writer_busy")
		}
	}
	return nil
}

// Only bounded plain conversation excerpts cross into the new Agent. Tool arguments,
// system instructions, and encrypted compacted input are not copied into its prompt.
func codexContinuation(path string) ([]prompts.CodexContinuationMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const tailBytes = 2 << 20
	offset := max(int64(0), st.Size()-tailBytes)
	r := bufio.NewReader(io.NewSectionReader(f, offset, tailBytes))
	if offset > 0 {
		_, _ = r.ReadBytes('\n')
	}
	var recent []prompts.CodexContinuationMessage
	var meta transcript.Meta
	for seq := 0; ; seq++ {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			for _, item := range transcript.ParseLine(seq, line, &meta) {
				if item.Kind != "text" || (item.Role != "user" && item.Role != "assistant") || strings.TrimSpace(item.Text) == "" {
					continue
				}
				text := []rune(item.Text)
				if len(text) > 2000 {
					text = append(text[:2000], []rune(" [excerpt truncated]")...)
				}
				recent = append(recent, prompts.CodexContinuationMessage{Role: item.Role, Text: string(text)})
				if len(recent) > 6 {
					recent = recent[1:]
				}
			}
		}
		if readErr == io.EOF {
			return recent, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

func (n *Node) codexTakeoverCall(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p struct {
		SessionID   string `json:"session_id"`
		Token       string `json:"token"`
		ConfirmStop bool   `json:"confirm_stop"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if method == codexTakeoverPrepare {
		return n.prepareCodexTakeover(ctx, p.SessionID)
	}
	s, err := n.commitCodexTakeover(ctx, p.SessionID, p.Token, p.ConfirmStop)
	return map[string]any{"session": s}, err
}
