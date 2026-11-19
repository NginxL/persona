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

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The desired deployment survives a hub restart. Only the node's local recovery
// transaction lives beside its executable; hub state stays in Postgres settings.
type nodeDeployRecord struct {
	Target string    `json:"target"`
	State  string    `json:"state"` // queued | succeeded | failed
	Phase  string    `json:"phase,omitempty"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
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
	m.mu.Lock()
	if m.deploying {
		m.mu.Unlock()
		return nil
	}
	if err := m.canDeployLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.deploying, m.deployErr, m.deployPhase = true, "", "waiting_idle"
	m.mu.Unlock()
	record := nodeDeployRecord{Target: pkg.Version, State: "queued", Phase: "waiting_idle", At: time.Now().UTC(), Fallback: pkg.Fallback}
	if saved != nil {
		record = *saved
	}
	ctx := m.d.deployContext()
	if err := m.saveDeployRecord(ctx, record); err != nil {
		m.mu.Lock()
		m.deploying, m.deployPhase = false, ""
		m.mu.Unlock()
		return explain(i18n.Err("devices-deploy.save_task_failed", i18n.Args{"err": err.Error()}), err)
	}
	m.d.changed()
	go m.runRPCDeployment(ctx, pkg, record)
	return nil
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
			bin, err = m.d.fetchPackage(ctx, pkg)
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
	m.deploying, m.deployPhase = false, ""
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

func (m *Machine) performRPCDeployment(ctx context.Context, bin string, record *nodeDeployRecord) error {
	m.mu.Lock()
	already := m.online && m.info.Version == record.Target
	m.mu.Unlock()
	if already {
		return m.confirmNodeDeployment(ctx, record, nil)
	}
	for {
		if err := m.deploymentPhase(ctx, record, "waiting_idle"); err != nil {
			return err
		}
		if err := m.waitNodeIdle(ctx); err != nil {
			return err
		}
		// Waiting for idle has no deadline. Transfer/restart does, including a
		// busy race at commit, which releases the staging transaction and waits again.
		attempt, cancel := context.WithTimeout(ctx, 15*time.Minute)
		err := m.sendNodeUpdate(attempt, bin, record)
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

func (m *Machine) waitNodeIdle(ctx context.Context) error {
	for {
		var list struct {
			Sessions   []proto.Session `json:"sessions"`
			UpdateBusy *bool           `json:"update_busy"` // the node's own commit check (node_update_idle)
		}
		snapshot := new(sessionSnapshot)
		check, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := m.callWithSnapshot(check, "sessions.list", map[string]any{}, &list, snapshot)
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

func (m *Machine) sendNodeUpdate(ctx context.Context, bin string, record *nodeDeployRecord) error {
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
	buf := make([]byte, proto.UploadChunkBytes)
	for offset := begin.Offset; offset < stat.Size(); {
		n, err := io.ReadFull(f, buf[:min(int64(len(buf)), stat.Size()-offset)])
		if err != nil {
			return explain(i18n.Err("devices-deploy.read_package_failed", i18n.Args{"err": err.Error()}), err)
		}
		var chunk struct {
			Offset int64 `json:"offset"`
		}
		if err := peer.Call(ctx, "node.update.chunk", map[string]any{"id": begin.ID, "offset": offset, "data": buf[:n]}, &chunk); err != nil {
			return explain(i18n.Err("devices-deploy.transfer_failed", i18n.Args{"err": err.Error()}), err)
		}
		offset += int64(n)
		if chunk.Offset != offset {
			return i18n.Err("devices-deploy.wrong_offset")
		}
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
	err = peer.Call(commit, "node.update.commit", map[string]any{"id": begin.ID}, nil)
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
	if info.NodeUpdate && (info.Version == m.d.hubVersion() || info.Version == m.d.nodeTarget(info)) {
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
				pkg, err = m.d.nodePackageFor(ctx, info.OS, info.Arch, nodeProgram(info))
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
	record := nodeDeployRecord{Target: pkg.Version, State: "queued", Phase: "waiting_idle", At: time.Now().UTC()}
	return m.saveDeployRecord(ctx, record)
}
