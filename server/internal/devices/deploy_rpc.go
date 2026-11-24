package devices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Adaptive chunk bounds for node.update.chunk over Persona Link / relayed RPC (#13761): each chunk is sent
// synchronously as JSON base64 (~4/3 raw size), so keeping each chunk well under the Link pong timeout avoids starving
// WebSocket pings on slow or high-RTT connections (both framed and unframed) while ramping quickly on fast links.
const (
	nodeUpdateMinChunk    = 16 << 10
	nodeUpdateInitChunk   = 64 << 10
	nodeUpdateMaxChunk    = proto.MachineFileUploadChunkBytes // 256 KiB raw (~341 KiB JSON base64)
	nodeUpdateChunkStep   = 4 << 10
	nodeUpdateTargetFloor = 250 * time.Millisecond
	nodeUpdateTargetCap   = 1500 * time.Millisecond
)

type nodeUpdateChunkSizer struct {
	min, max, cur int
	target        time.Duration
}

func newNodeUpdateChunkSizer(pingTimeout time.Duration) *nodeUpdateChunkSizer {
	if pingTimeout <= 0 {
		pingTimeout = link.PingTimeout
	}
	target := min(max(pingTimeout/4, nodeUpdateTargetFloor), nodeUpdateTargetCap)
	maxChunk := min(nodeUpdateMaxChunk, proto.UploadChunkBytes)
	init := nodeUpdateInitChunk
	if defaultTarget := link.PingTimeout / 4; target < defaultTarget {
		init = int(int64(nodeUpdateInitChunk) * int64(target) / int64(defaultTarget))
		maxChunk = int(int64(maxChunk) * int64(target) / int64(defaultTarget))
	}
	maxChunk = max((maxChunk/nodeUpdateChunkStep)*nodeUpdateChunkStep, nodeUpdateMinChunk)
	s := &nodeUpdateChunkSizer{
		min:    nodeUpdateMinChunk,
		max:    maxChunk,
		target: target,
	}
	s.cur = s.clamp(init)
	return s
}

func (s *nodeUpdateChunkSizer) next() int { return s.cur }

func (s *nodeUpdateChunkSizer) observe(sent int, elapsed time.Duration, stats rpc.LaneStats) {
	if sent <= 0 {
		return
	}
	var desired int
	if elapsed <= 0 {
		desired = s.cur * 2
	} else {
		desired = int(float64(sent) * s.target.Seconds() / elapsed.Seconds())
	}
	if desired > s.cur {
		// Grow at most 2x per chunk so a single highly-compressible section cannot jump straight to max.
		desired = min(desired, s.cur*2)
	}
	if stats.Framed && stats.RTT > 2*s.target {
		// Frames are already queueing toward the ping deadline: hold steady or shrink, never grow.
		desired = min(desired, s.cur)
		if stats.RTT > 3*s.target {
			desired = min(desired, s.cur/2)
		}
	}
	s.cur = s.clamp(desired)
}

func (s *nodeUpdateChunkSizer) clamp(n int) int {
	n = (n / nodeUpdateChunkStep) * nodeUpdateChunkStep
	return min(max(n, s.min), s.max)
}

// The desired deployment survives a hub restart. Only the node's local recovery
// transaction lives beside its executable; hub state stays in Postgres settings.
type nodeDeployRecord struct {
	Development *devicewire.DevelopmentTarget `json:"development,omitempty"`
	Source      string                        `json:"source,omitempty"`
	Target      string                        `json:"target"`
	State       string                        `json:"state"` // queued | succeeded | failed
	Phase       string                        `json:"phase,omitempty"`
	Error       string                        `json:"error,omitempty"`
	At          time.Time                     `json:"at"`
	// Fallback: the target is a fallback build (another version than the hub's), which only an explicit Redeploy
	// sends; a record queued for the hub's own version never resumes as one.
	Fallback bool `json:"fallback,omitempty"`
}

type nodeDeployPreflight struct {
	id     string
	failed chan error
}

// nodeUpdatePreflightFailed wakes only the deploy waiting on this upload's commit or confirmation.
func (m *Machine) nodeUpdatePreflightFailed(params json.RawMessage) {
	var failure proto.NodeUpdatePreflightFailure
	if json.Unmarshal(params, &failure) != nil || failure.Error == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pending := m.deployPreflight; pending != nil && pending.id == failure.ID {
		select {
		case pending.failed <- errors.New(failure.Error):
		default:
		}
	}
}

func (d *Devices) deployContext() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx != nil {
		return d.ctx
	}
	return context.Background()
}

// Called with m.mu held: use the node protocol when SSH cannot reach the owner.
func (m *Machine) rpcDeploymentLocked() bool {
	return m.cfg.Kind == kindLink || m.cfg.Kind == kindService || m.derived || m.via == viaHub
}

// canDeployLocked: nil when the machine's node can be updated from here, else why not (an i18n.Message, but for
// the node's own reason, which comes in its words).
func (m *Machine) canDeployLocked() error {
	if m.cfg.Kind == "local" {
		return i18n.Err("hub-machine.deploy_local")
	}
	if m.rpcDeploymentLocked() && !(nodeHas(m.info, nodeproto.NodeUpdate) && m.info.NodeUpdate) {
		if m.info.NodeUpdateReason != "" {
			return errors.New(m.info.NodeUpdateReason)
		}
		return i18n.Err("hub-machine.deploy_upgrade_first")
	}
	if !m.online {
		return i18n.Err("hub-machine.deploy_offline")
	}
	return nil
}

func (m *Machine) deployRecordKey() string { return "node.deploy." + m.cfg.ID }

func (m *Machine) saveDeployRecord(ctx context.Context, record nodeDeployRecord) error {
	if m.d.opts.Store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.d.opts.Store.PutSetting(ctx, m.deployRecordKey(), record)
}

func (m *Machine) deploymentPhase(ctx context.Context, record *nodeDeployRecord, phase string) error {
	record.Phase, record.At = phase, time.Now().UTC()
	if err := m.saveDeployRecord(ctx, *record); err != nil {
		return explain(i18n.Err("devices-deploy.save_progress_failed", i18n.Args{"err": err.Error()}), err)
	}
	m.mu.Lock()
	m.deployPhase = phase
	m.mu.Unlock()
	m.d.changed()
	return nil
}

func (m *Machine) startRPCDeployment(pkg nodePackage, saved *nodeDeployRecord) error {
	_, err := m.startRPCDeploymentRun(pkg, saved)
	return err
}

func (m *Machine) startRPCDeploymentRun(pkg nodePackage, saved *nodeDeployRecord) (*deployRun, error) {
	m.mu.Lock()
	if m.deploying {
		run := m.deployRun
		m.mu.Unlock()
		return run, nil
	}
	if err := m.canDeployLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	run := m.beginDeployLocked("waiting_idle")
	m.mu.Unlock()
	record := nodeDeployRecord{Development: pkg.development, Target: pkg.Version, State: "queued", Phase: "waiting_idle", At: time.Now().UTC(), Fallback: pkg.Fallback}
	if saved != nil {
		record = *saved
	}
	ctx := m.d.deployContext()
	if err := m.saveDeployRecord(ctx, record); err != nil {
		err = explain(i18n.Err("devices-deploy.save_task_failed", i18n.Args{"err": err.Error()}), err)
		m.mu.Lock()
		m.endDeployLocked(err)
		m.mu.Unlock()
		return nil, err
	}
	m.d.changed()
	go m.runRPCDeployment(ctx, pkg, record)
	return run, nil
}

func (m *Machine) runRPCDeployment(ctx context.Context, pkg nodePackage, record nodeDeployRecord) {
	m.mu.Lock()
	already := m.online && m.info.Version == record.Target
	m.mu.Unlock()
	var bin string
	var err error
	if !already { // a release's build is downloaded and checked first (phase transferring)
		if pkg.path == "" {
			err = m.deploymentPhase(ctx, &record, "transferring")
		}
		if err == nil {
			bin, err = m.fetchDeployment(ctx, pkg)
			m.mu.Lock()
			record.Source = m.deploySource
			m.mu.Unlock()
		}
	}
	if err == nil {
		err = m.performRPCDeployment(ctx, bin, &record)
	}
	if ctx.Err() == nil {
		record.State, record.Phase, record.Error, record.At = "succeeded", "", "", time.Now().UTC()
		if err != nil {
			record.State, record.Error = "failed", err.Error()
		}
		if saveErr := m.saveDeployRecord(ctx, record); saveErr != nil {
			m.d.logf("[%s] save node deployment: %v", m.cfg.ID, saveErr)
			if err == nil {
				err = explain(i18n.Err("devices-deploy.save_result_failed", i18n.Args{"err": saveErr.Error()}), saveErr)
			}
		}
	}
	m.mu.Lock()
	m.endDeployLocked(err)
	if err != nil && ctx.Err() == nil {
		m.deployErr = err.Error()
	}
	m.mu.Unlock()
	if err != nil {
		m.d.logf("[%s] node update: %v", m.cfg.ID, err)
	} else {
		m.d.logf("[%s] node update confirmed: %s", m.cfg.ID, record.Target)
	}
	m.d.changed()
}

func (d *Devices) updateHandoffGrace() time.Duration {
	if d.opts.UpdateHandoffGrace == nil {
		return time.Duration(proto.DefaultNodeUpdateHandoffGraceSeconds) * time.Second
	}
	grace := d.opts.UpdateHandoffGrace()
	if grace < 0 {
		return 0
	}
	if max := time.Duration(proto.MaxNodeUpdateHandoffGraceSeconds) * time.Second; grace > max {
		return max
	}
	return grace
}

// endUpdateHandoff reopens admission after a failed or completed deployment. The flag is unknown to older nodes;
// newer ones resume their own headless admission too. The old transport may already be gone, so this is best effort.
func (m *Machine) endUpdateHandoff() {
	m.mu.Lock()
	was := m.updateHandoff
	m.updateHandoff = false
	m.mu.Unlock()
	if !was {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.call(ctx, "sessions.list", map[string]any{"update_handoff": false}, nil)
}

func (m *Machine) performRPCDeployment(ctx context.Context, bin string, record *nodeDeployRecord) error {
	m.mu.Lock()
	already := m.online && m.info.Version == record.Target
	m.mu.Unlock()
	if already {
		return m.confirmNodeDeployment(ctx, record, nil)
	}
	grace := m.d.updateHandoffGrace()
	var handoffDeadline time.Time
	if grace > 0 {
		handoffDeadline = time.Now().Add(grace)
	}
	defer m.endUpdateHandoff()
	for {
		if err := m.deploymentPhase(ctx, record, "waiting_idle"); err != nil {
			return err
		}
		if err := m.waitNodeIdle(ctx, handoffDeadline); err != nil {
			return err
		}
		// Natural idle has no deadline. After the configured grace, new headless turns are refused while current work
		// drains. Transfer/restart has its own timeout, including a busy race at commit, which waits again.
		attempt, cancel := context.WithTimeout(ctx, 15*time.Minute)
		err := m.sendNodeUpdate(attempt, bin, record, int(grace/time.Second))
		cancel()
		var remote *rpc.Error
		if errors.As(err, &remote) && remote.Code == proto.CodeNodeUpdateBusy {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.deployRetry):
			}
			continue
		}
		return err
	}
}

func (m *Machine) waitNodeIdle(ctx context.Context, handoffDeadline time.Time) error {
	for {
		handoff := !handoffDeadline.IsZero() && !time.Now().Before(handoffDeadline)
		params := map[string]any{}
		m.mu.Lock()
		if handoff {
			m.updateHandoff = true
			params["update_handoff"] = true
		}
		m.mu.Unlock()
		var list struct {
			Sessions   []proto.Session `json:"sessions"`
			UpdateBusy *bool           `json:"update_busy"` // the node's own commit check (node_update_idle)
		}
		snapshot := new(sessionSnapshot)
		check, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := m.callWithSnapshot(check, "sessions.list", params, &list, snapshot)
		cancel()
		if err != nil {
			return explain(i18n.Err("devices-deploy.idle_check_failed", i18n.Args{"err": err.Error()}), err)
		}
		sessions, accepted := m.acceptSessionsList(snapshot, list.Sessions, false)
		m.mu.Lock()
		ownCheck := nodeHas(m.info, nodeproto.NodeUpdateIdle) && list.UpdateBusy != nil
		m.mu.Unlock()
		var busy bool
		if ownCheck {
			// Only the node knows whether a conversation it runs headless is busy: its rows do not say so (#6364).
			// A rejected response has no current update_busy to accept, so ask the node again rather than deploy on it.
			busy = !accepted || *list.UpdateBusy
		} else {
			// A node that commits on the old rule refuses while anything works: wait for that here too, rather than
			// transfer the whole build again on every refusal.
			busy = m.machinePending() || slices.ContainsFunc(sessions, sessionAtWork) // a question of its agents waits on the phone
		}
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.deployRetry):
		}
	}
}

// sessionAtWork: the old commit check (nodes before node_update_idle) refuses while s is at work.
func sessionAtWork(s proto.Session) bool {
	if s.Starting || s.State == proto.StateWorking || s.State == proto.StateNeedsYou || s.Status == proto.StatusWorking || s.PendingInteractions > 0 || len(s.Background) > 0 {
		return true
	}
	return slices.ContainsFunc(s.Subagents, func(sub proto.Subagent) bool { return sub.Running })
}

func (m *Machine) sendNodeUpdate(ctx context.Context, bin string, record *nodeDeployRecord, handoffGraceSeconds int) error {
	f, err := os.Open(bin)
	if err != nil {
		return explain(i18n.Err("devices-deploy.open_package_failed", i18n.Args{"err": err.Error()}), err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	if err := m.deploymentPhase(ctx, record, "transferring"); err != nil {
		return err
	}
	m.mu.Lock()
	peer := m.peer
	m.mu.Unlock()
	if peer == nil {
		return errOffline
	}
	var begin struct {
		ID     string `json:"id"`
		Offset int64  `json:"offset"`
	}
	if err := peer.Call(ctx, "node.update.begin", map[string]any{
		"size": stat.Size(), "sha256": hex.EncodeToString(hash.Sum(nil)), "version": record.Target,
	}, &begin); err != nil {
		return explain(i18n.Err("devices-deploy.prepare_failed", i18n.Args{"err": err.Error()}), err)
	}
	if begin.ID == "" {
		return i18n.Err("devices-deploy.bad_transaction")
	}
	committed := false
	defer func() {
		if !committed {
			abort, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = peer.Call(abort, "node.update.abort", map[string]any{"id": begin.ID}, nil)
		}
	}()
	if begin.Offset < 0 || begin.Offset > stat.Size() {
		return i18n.Err("devices-deploy.bad_offset")
	}
	if _, err := f.Seek(begin.Offset, io.SeekStart); err != nil {
		return err
	}
	pingTimeout := link.PingTimeout
	if m.d != nil {
		_, pingTimeout, _ = m.d.linkPingTimes()
	}
	sizer := newNodeUpdateChunkSizer(pingTimeout)
	buf := make([]byte, sizer.max)
	for offset := begin.Offset; offset < stat.Size(); {
		want := min(int64(sizer.next()), stat.Size()-offset)
		n, err := io.ReadFull(f, buf[:want])
		if err != nil {
			return explain(i18n.Err("devices-deploy.read_package_failed", i18n.Args{"err": err.Error()}), err)
		}
		var chunk struct {
			Offset int64 `json:"offset"`
		}
		began := time.Now()
		if err := peer.Call(ctx, "node.update.chunk", map[string]any{"id": begin.ID, "offset": offset, "data": buf[:n]}, &chunk); err != nil {
			return explain(i18n.Err("devices-deploy.transfer_failed", i18n.Args{"err": err.Error()}), err)
		}
		offset += int64(n)
		if chunk.Offset != offset {
			return i18n.Err("devices-deploy.wrong_offset")
		}
		if offset < stat.Size() {
			sizer.observe(n, time.Since(began), peer.LaneStats())
		}
	}
	if err := m.d.CheckDevelopmentTarget(ctx, record.Development); err != nil {
		return err
	}
	if err := m.deploymentPhase(ctx, record, "restarting"); err != nil {
		return err
	}
	// Preflight can finish as soon as the node writes the commit reply, before this worker resumes.
	preflight := &nodeDeployPreflight{id: begin.ID, failed: make(chan error, 1)}
	m.mu.Lock()
	m.deployPreflight = preflight
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.deployPreflight = nil
		m.mu.Unlock()
	}()
	commit, cancelCommit := context.WithTimeout(ctx, 30*time.Second)
	err = peer.Call(commit, "node.update.commit", map[string]any{"id": begin.ID, "handoff_grace_seconds": handoffGraceSeconds}, nil)
	cancelCommit()
	if err != nil {
		var remote *rpc.Error
		if errors.As(err, &remote) {
			return explain(i18n.Err("devices-deploy.install_update_failed", i18n.Args{"err": err.Error()}), err)
		}
		// A lost commit response is ambiguous: the node may already have
		// replaced itself. Never report success until it reconnects and confirms.
		m.d.logf("[%s] node update commit response lost, checking reconnect: %v", m.cfg.ID, err)
	}
	committed = true
	return m.confirmNodeDeployment(ctx, record, preflight.failed)
}

func (m *Machine) confirmNodeDeployment(ctx context.Context, record *nodeDeployRecord, preflightFailed <-chan error) error {
	select {
	case err := <-preflightFailed:
		return err
	default:
	}
	if err := m.deploymentPhase(ctx, record, "confirming"); err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		m.mu.Lock()
		peer, online, version := m.peer, m.online, m.info.Version
		m.mu.Unlock()
		if online && peer != nil && version == record.Target {
			if err := m.d.CheckDevelopmentTarget(ctx, record.Development); err != nil {
				return err
			}
			confirm, ccancel := context.WithTimeout(deadline, 10*time.Second)
			err := peer.Call(confirm, "node.update.confirm", map[string]any{"version": record.Target}, nil)
			ccancel()
			if err == nil {
				return nil
			}
		}
		select {
		case err := <-preflightFailed:
			return err
		case <-deadline.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return i18n.Err("devices-deploy.not_confirmed")
		case <-time.After(time.Second):
		}
	}
}

// Confirm a healthy replacement even if the hub restarted during transfer. An
// unconfirmed target must never remain installed just because a worker vanished.
func (m *Machine) nodeUpdateConnected(ctx context.Context, peer *rpc.Peer, info proto.NodeInfo) {
	cfg := m.d.config()
	if info.NodeUpdate && !cfg.DevelopmentHold && (info.Version == cfg.HubVersion || info.Version == m.d.nodeTarget(info)) &&
		m.d.CheckDevelopmentTarget(ctx, cfg.DevelopmentTarget) == nil {
		confirm, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := peer.Call(confirm, "node.update.confirm", map[string]any{"version": info.Version}, nil)
		cancel()
		if err != nil {
			m.d.logf("[%s] confirm connected node update: %v", m.cfg.ID, err)
		}
	}
	if m.d.opts.Store == nil {
		return
	}
	m.mu.Lock()
	active, linked := m.deploying, m.rpcDeploymentLocked()
	m.mu.Unlock()
	if active || !linked {
		return
	}
	var record nodeDeployRecord
	load, cancel := context.WithTimeout(ctx, 10*time.Second)
	found, err := m.d.opts.Store.Setting(load, m.deployRecordKey(), &record)
	cancel()
	if err != nil || !found {
		if err != nil {
			m.d.logf("[%s] load pending node update: %v", m.cfg.ID, err)
		}
		return
	}
	if record.Source != "" {
		m.mu.Lock()
		m.deploySource, m.deployVersion = record.Source, record.Target
		m.mu.Unlock()
	}
	if record.Development != nil && m.d.CheckDevelopmentTarget(ctx, record.Development) != nil {
		return
	}
	if record.State != "queued" {
		if record.State == "failed" {
			m.mu.Lock()
			m.deployErr = record.Error
			m.mu.Unlock()
			m.d.changed()
		}
		return
	}
	// A Hub-only update must not resume an old queued transfer. Confirmation of
	// already installed candidates remains allowed.
	if !m.d.config().AutoUpdateNodes && info.Version != record.Target {
		return
	}
	installed := record.Phase == "restarting" || record.Phase == "confirming"
	if installed && info.Version == record.Target {
		// Running what it was sent: confirmed, whatever the target is now (a newer one, or one not known while the
		// release site is away, is no reason to let the node roll a working update back; updateOnConnect moves it on).
		err = m.startRPCDeployment(nodePackage{Version: record.Target}, &record)
	} else {
		target, known := m.d.resolvedTargetKnown(ctx, info)
		switch {
		case !known:
			// The release site does not answer, so what the node should run is not known: the record waits for the
			// next connection rather than fail because of the site.
			m.d.logf("[%s] queued node update to %s: which build it should run is not known now; it waits", m.cfg.ID, record.Target)
			return
		case record.Target != target:
			err = i18n.Err("devices-deploy.hub_changed")
		case installed:
			// An accepted install may have rolled back while the hub was down.
			// Do not automatically reinstall a build that just failed to start.
			err = i18n.Err("devices-deploy.rolled_back")
		default:
			pkg := nodePackage{Version: record.Target} // running it already: only confirmed
			if info.Version != record.Target {
				pkg, err = m.d.nodePackageFor(ctx, info.OS, info.Arch, info.Program)
				if err == nil && (pkg.Version != record.Target || pkg.Fallback && !record.Fallback) {
					// The build found now is another one than the record's, or the same version now as a fallback (the
					// hub moved to a version without a release of its own): what was queued for the hub's version
					// does not go out as a fallback by itself.
					err = i18n.Err("devices-deploy.hub_changed")
				}
			}
			if err == nil {
				err = m.startRPCDeployment(pkg, &record)
			}
		}
	}
	if err != nil {
		record.State, record.Phase, record.Error = "failed", "", err.Error()
		_ = m.saveDeployRecord(ctx, record)
		m.mu.Lock()
		m.deployErr = err.Error()
		m.mu.Unlock()
		m.d.changed()
	}
}

func (m *Machine) queueOfflineNodeUpdate(ctx context.Context) error {
	m.mu.Lock()
	info := m.info // as it was last connected
	m.mu.Unlock()
	pkg, known := m.d.resolvedPackageKnown(ctx, info)
	if !known {
		return errors.New("which build it should run is not known now (the release site does not answer): queued on its next connection instead")
	}
	if pkg.Fallback {
		m.d.logf("update: [%s] build %s is a fallback (not the hub's version): not queued, sent only on Redeploy", m.cfg.ID, pkg.Version)
		return nil
	}
	record := nodeDeployRecord{Development: pkg.development, Target: pkg.Version, State: "queued", Phase: "waiting_idle", At: time.Now().UTC()}
	return m.saveDeployRecord(ctx, record)
}
