package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

const conversationSegmentsKey = "assistant.segments"

type logicalSwitchRequest struct {
	OperationID    string `json:"operation_id"`
	ExpectedEpoch  int64  `json:"expected_epoch"`
	MachineID      string `json:"machine_id"`
	Launcher       string `json:"launcher"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
}
type logicalInput struct {
	Method    string               `json:"method"`
	Params    map[string]any       `json:"params"`
	Transport *devicewire.NodeCall `json:"transport,omitempty"`
}
type segmentTarget struct {
	Registered        bool   `json:"registered"`
	Engine            string `json:"engine"`
	SupervisionPolicy bool   `json:"supervision_policy"`
	Prepare           bool   `json:"prepare"`
}

// The node resolves a registered native/runtime execution path. An empty or
// unrecognized path cannot be made valid by guessing from a launcher name.
func knownSegmentTarget(target segmentTarget) bool {
	return target.Registered && target.Prepare && proto.AssistantEngineKind(target.Engine) != ""
}
func (s *Service) logicalTarget(ctx context.Context, machine, launcher string) (segmentTarget, error) {
	var target segmentTarget
	if err := s.launcherScopeErr(ctx, machine, launcher); err != nil {
		return target, err
	}
	raw, err := s.logicalPhysical(ctx, machine, "assistant.segment.check", map[string]any{"launcher": launcher})
	if err != nil {
		return target, err
	}
	if json.Unmarshal(raw, &target) != nil || !knownSegmentTarget(target) {
		return target, logicalRefusal(ctx, "unknown_engine")
	}
	return target, nil
}
func (s *Service) segmentsEnabled(ctx context.Context) bool {
	if s.store() == nil {
		return false
	}
	var v struct {
		Enabled *bool `json:"enabled"`
	}
	_, err := s.store().Setting(ctx, conversationSegmentsKey, &v)
	return err == nil && (v.Enabled == nil || *v.Enabled)
}
func (s *Service) logicalSwitchRoutes() {
	s.handle("GET /api/v1/assistant/segments", func(w http.ResponseWriter, r *http.Request) {
		apierr.WriteJSON(w, map[string]any{"enabled": s.segmentsEnabled(r.Context()), "feature": "assistant_segments", "routing": true})
	})
	s.handle("PUT /api/v1/assistant/segments", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&p); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if s.store() == nil {
			apierr.Call(w, logicalRefusal(r.Context(), "unavailable"))
			return
		}
		if err := s.store().PutSetting(r.Context(), conversationSegmentsKey, p); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, p)
	})
	s.handle("GET /api/v1/assistant/conversations/{id}/segments", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		ctx := r.Context()
		machine, id := r.URL.Query().Get("machine_id"), r.PathValue("id")
		if machine == "" || !s.keepsPickOn(ctx, id, machine) {
			apierr.Call(w, logicalRefusal(ctx, "unavailable"))
			return
		}
		st, ok := s.store().(logicalSwitchStore)
		if !ok {
			apierr.Call(w, logicalRefusal(ctx, "unavailable"))
			return
		}
		c, err := s.physicalLogicalConversation(ctx, st, machine, id)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		epoch := int64(1)
		var operation any
		if c != nil {
			if c.Deleted || c.MachineID != machine || c.SessionID != id {
				apierr.Call(w, logicalRefusal(ctx, "private_segment"))
				return
			}
			epoch = c.Epoch
			if c.Switch != nil {
				operation = map[string]any{"operation_id": c.Switch.OperationID, "phase": c.Switch.Phase}
			}
		}
		role, err := s.logicalRoleRequired(ctx, machine, id)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"enabled": s.segmentsEnabled(ctx), "epoch": epoch, "switch": operation, "supervision_required": role})
	})
	s.handle("POST /api/v1/assistant/conversations/{id}/switch", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		var p struct {
			logicalSwitchRequest
			SourceMachine string `json:"source_machine_id"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		op, err := s.beginLogicalSwitch(r.Context(), p.SourceMachine, r.PathValue("id"), p.logicalSwitchRequest)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, op)
	})
	s.handle("GET /api/v1/assistant/conversations/{id}/switch/{operation}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		st, ok := s.store().(logicalSwitchStore)
		if !ok {
			apierr.Call(w, logicalRefusal(r.Context(), "unavailable"))
			return
		}
		op, err := st.ConversationSwitch(r.Context(), r.URL.Query().Get("machine_id"), r.PathValue("id"), r.PathValue("operation"))
		if err != nil {
			apierr.Call(w, err)
			return
		}
		if op == nil {
			http.NotFound(w, r)
			return
		}
		apierr.WriteJSON(w, map[string]any{"operation_id": op.OperationID, "phase": op.Phase, "expected_epoch": op.ExpectedEpoch, "error": op.Error})
	})
}

func (s *Service) logicalRoleRequired(ctx context.Context, machine, id string) (bool, error) {
	policy, err := s.supervisionInstructions(ctx, machine, id)
	if err != nil {
		return false, err
	}
	cfg := s.settings(ctx)
	return policy != "" || slices.Contains(cfg.Leaders, id) || id == cfg.BackgroundSupervisor || id == cfg.Main && cfg.MainSupervisesOwnTasks != nil && *cfg.MainSupervisesOwnTasks, nil
}

func (s *Service) beginLogicalSwitch(ctx context.Context, machine, id string, p logicalSwitchRequest) (any, error) {
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	if !s.segmentsEnabled(ctx) {
		return nil, logicalRefusal(ctx, "disabled")
	}
	if machine == "" || id == "" || p.MachineID == "" || p.Launcher == "" || p.OperationID == "" || len(p.OperationID) > 128 || p.ExpectedEpoch < 1 {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	request, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	// Replay is checked before current status: accepted operations remain idempotent.
	old, err := st.ConversationSwitch(ctx, machine, id, p.OperationID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if string(old.Request) != string(request) {
			return nil, db.ErrConversationEpoch
		}
		return map[string]any{"operation_id": old.OperationID, "phase": old.Phase}, nil
	}
	c, err := s.physicalLogicalConversation(ctx, st, machine, id)
	if err != nil {
		return nil, err
	}
	row, known := s.session(ctx, machine, id)
	if !known || !row.Assistant || row.PaneID != "" || s.isWorkMachine(p.MachineID) {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	if row.Starting || row.Status == proto.StatusWorking || row.Status == proto.StatusBlocked || row.PendingInteractions > 0 || backgroundRunning(row) || len(row.Queued) > 0 || (row.Compaction != nil && row.Compaction.State == proto.CompactionRunning) {
		return nil, logicalRefusal(ctx, "busy")
	}
	for _, ix := range s.pendingInteractions() {
		if ix.SessionID == id && ix.MachineID == machine {
			return nil, logicalRefusal(ctx, "busy")
		}
	}
	target, err := s.logicalTarget(ctx, p.MachineID, p.Launcher)
	if err != nil {
		return nil, err
	}
	role, err := s.logicalRoleRequired(ctx, machine, id)
	if err != nil {
		return nil, err
	}
	if role && !target.SupervisionPolicy {
		return nil, s.supervisionRuntimeRefusal(ctx, p.MachineID)
	}
	if c == nil {
		if p.ExpectedEpoch != 1 || assistantKind(row.Agent) == "" {
			return nil, logicalRefusal(ctx, "unknown_engine")
		}
		var page historyPage
		if err = s.physicalHistoryCall(ctx, machine, DeviceSessionItems, map[string]any{"session_id": id, "limit": 1, "lazy_images": true}, &page); err != nil {
			return nil, err
		}
		if page.History == "" {
			return nil, logicalRefusal(ctx, "missing_generation")
		}
		sourceEngine := proto.AssistantExecutionIdentity(row.Agent, row.SourceKind, row.SourceInstanceID, row.Launcher)
		if sourceEngine == "" {
			return nil, logicalRefusal(ctx, "unknown_engine")
		}
		if err = st.AdoptConversationAliases(ctx, machine, id, randomHex(16), sourceEngine, page.History, s.physicalAliases(machine)); err != nil {
			return nil, err
		}
		c, err = st.LogicalConversation(ctx, machine, id)
		if err != nil {
			return nil, err
		}
	}
	if c == nil || c.Deleted || c.MachineID != machine || c.SessionID != id {
		return nil, logicalRefusal(ctx, "private_segment")
	}
	a, ok := c.Active()
	if !ok {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	if err = st.BeginConversationSwitch(ctx, machine, id, db.ConversationSwitch{OperationID: p.OperationID, ExpectedEpoch: p.ExpectedEpoch, SourceSegment: a.ID, Request: request}); err != nil {
		if errors.Is(err, db.ErrConversationSwitchBusy) {
			return nil, logicalRefusal(ctx, "busy")
		}
		return nil, err
	}
	s.logicalChanged()
	s.historyWake()
	return map[string]any{"operation_id": p.OperationID, "phase": "preparing", "epoch": c.Epoch}, nil
}

// The loop is independent of the switch-enable setting: disabling new switches
// must not strand existing routing, queued messages or deferred closures.
func (s *Service) logicalSweep(ctx context.Context) {
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return
	}
	rows, err := st.LogicalConversations(ctx)
	if err != nil {
		return
	}
	candidates, err := st.ConversationPreparedCandidates(ctx)
	if err != nil {
		return
	}
	for _, candidate := range candidates {
		c, err := st.LogicalConversation(ctx, candidate.LogicalMachine, candidate.LogicalSession)
		if err != nil {
			continue
		}
		adopted := false
		if c != nil {
			for _, seg := range c.Segments {
				if seg.MachineID == candidate.MachineID && seg.SessionID == candidate.SessionID {
					adopted = true
					break
				}
			}
		}
		if adopted {
			_ = st.ConversationPreparedCandidateDone(ctx, candidate.MachineID, candidate.SessionID)
			continue
		}
		if c != nil && c.Switch != nil && c.Switch.OperationID == candidate.OperationID {
			continue
		}
		closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = s.logicalPhysical(closeCtx, candidate.MachineID, "session.close", map[string]any{"session_id": candidate.SessionID})
		cancel()
		if err == nil {
			_ = st.ConversationPreparedCandidateDone(ctx, candidate.MachineID, candidate.SessionID)
		}
	}
	for _, c := range rows {
		pending, err := st.ConversationInputPending(ctx, c.MachineID, c.SessionID)
		if err != nil {
			continue
		}
		closing := false
		for _, seg := range c.Segments {
			closing = closing || seg.ClosePending
		}
		if c.Switch == nil && !pending && !closing {
			continue
		}
		key := [2]string{c.MachineID, c.SessionID}
		s.logical.mu.Lock()
		if s.logical.running == nil {
			s.logical.running = map[[2]string]bool{}
		}
		busy := s.logical.running[key]
		if !busy {
			s.logical.running[key] = true
		}
		s.logical.mu.Unlock()
		if busy {
			continue
		}
		go func(c db.LogicalConversation) {
			defer func() { s.logical.mu.Lock(); delete(s.logical.running, key); s.logical.mu.Unlock() }()
			run, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			if err := s.advanceLogicalConversation(run, st, &c); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, db.ErrConversationEpoch) {
				s.logf("assistant: segment operation awaits reconciliation: %v", err)
			}
		}(c)
	}
	closures, err := st.ConversationClosures(ctx)
	if err != nil {
		return
	}
	for _, key := range closures {
		closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = s.logicalPhysical(closeCtx, key[0], "session.close", map[string]any{"session_id": key[1]})
		cancel()
		if err == nil {
			_ = st.ConversationClosureDone(ctx, key[0], key[1])
		}
	}
}

func (s *Service) advanceLogicalConversation(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation) error {
	for _, seg := range c.Segments {
		if seg.ClosePending {
			closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err := s.logicalPhysical(closeCtx, seg.MachineID, "session.close", map[string]any{"session_id": seg.SessionID})
			cancel()
			if err == nil {
				_ = st.ConversationSegmentClosed(ctx, seg.ID)
			}
		}
	}
	op := c.Switch
	if op != nil && (op.Phase == "preparing" || op.Phase == "in_doubt" && c.Epoch == op.ExpectedEpoch) {
		var p logicalSwitchRequest
		if err := json.Unmarshal(op.Request, &p); err != nil {
			return err
		}
		if p.PermissionMode == "" {
			source, _ := s.session(ctx, c.MachineID, c.SessionID)
			p.PermissionMode = source.PermissionMode
			if p.PermissionMode == "" {
				p.PermissionMode = "default"
			}
		}
		handoff, claimed, err := st.ConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID)
		if err != nil {
			return err
		}
		if len(handoff) == 0 {
			h, err := s.logicalSwitchHandoff(ctx, st, c)
			if err != nil {
				return err
			}
			handoff, err = json.Marshal(h)
			if err != nil {
				return err
			}
			if err = st.PutConversationHandoff(ctx, c.MachineID, c.SessionID, op.OperationID, handoff); err != nil {
				return err
			}
		}
		method := "assistant.segment.receipt"
		if !claimed {
			var won bool
			won, err = st.ClaimConversationPreparation(ctx, c.MachineID, c.SessionID, op.OperationID)
			if err != nil {
				return err
			}
			if won {
				method = "assistant.segment.prepare"
			}
		}
		params := map[string]any{"operation_id": logicalNodeOperation(c, op.OperationID), "launcher": p.Launcher, "model": p.Model, "effort": p.Effort, "permission_mode": p.PermissionMode, "is_main": c.SessionID == s.settings(ctx).Main}
		if method == "assistant.segment.prepare" {
			row, _ := s.session(ctx, c.MachineID, c.SessionID)
			cwd, err := s.prepareSpace(ctx, p.MachineID, row.Space)
			if err != nil {
				_ = st.ResolveConversationSwitch(ctx, c.MachineID, c.SessionID, op.OperationID, "rejected", "preparation unavailable")
				return err
			}
			params["cwd"] = cwd
		}
		var raw json.RawMessage
		err = st.WithConversationEpoch(ctx, c.MachineID, c.SessionID, c.Epoch, func(current *db.LogicalConversation) error {
			if current.Switch == nil || current.Switch.OperationID != op.OperationID {
				return db.ErrConversationEpoch
			}
			// A manager may have persisted the positive start receipt and crashed
			// before mapping it. That durable receipt also survives loss of the
			// node's in-memory receipt cache; never start a replacement candidate.
			candidates, err := st.ConversationPreparedCandidates(ctx)
			if err != nil {
				return err
			}
			for _, candidate := range candidates {
				if candidate.LogicalMachine == c.MachineID && candidate.LogicalSession == c.SessionID && candidate.OperationID == op.OperationID && candidate.MachineID == p.MachineID {
					raw, err = json.Marshal(map[string]any{"session_id": candidate.SessionID, "prepared": true})
					return err
				}
			}
			raw, err = s.logicalPhysical(ctx, p.MachineID, method, params)
			if err != nil {
				return err
			}
			var receipt struct {
				SessionID string `json:"session_id"`
				Prepared  bool   `json:"prepared"`
			}
			if json.Unmarshal(raw, &receipt) == nil && receipt.Prepared && receipt.SessionID != "" {
				return st.RecordConversationPreparedCandidate(ctx, db.ConversationPreparedCandidate{MachineID: p.MachineID, SessionID: receipt.SessionID, LogicalMachine: c.MachineID, LogicalSession: c.SessionID, OperationID: op.OperationID})
			}
			return nil
		})
		if err != nil {
			if logicalNotTaken(err) {
				return st.ResolveConversationSwitch(ctx, c.MachineID, c.SessionID, op.OperationID, "rejected", "preparation refused")
			}
			_ = st.ResolveConversationSwitch(ctx, c.MachineID, c.SessionID, op.OperationID, "in_doubt", "preparation receipt pending")
			return err
		}
		var reply struct {
			SessionID string `json:"session_id"`
			Prepared  bool   `json:"prepared"`
		}
		if json.Unmarshal(raw, &reply) != nil || !reply.Prepared || reply.SessionID == "" {
			return nil
		}
		source, ok := c.Active()
		if !ok {
			return logicalRefusal(ctx, "unavailable")
		}
		target, err := s.logicalTarget(ctx, p.MachineID, p.Launcher)
		if err != nil {
			return err
		}
		// Pull a proven physical tail after preparation; never infer it from item count.
		var tail historyPage
		if err = s.physicalHistoryCall(ctx, source.MachineID, DeviceSessionItems, map[string]any{"session_id": source.SessionID, "limit": 1, "lazy_images": true}, &tail); err != nil {
			return err
		}
		if tail.ToSeq == nil || tail.MoreAfter {
			return logicalRefusal(ctx, "missing_generation")
		}
		if err = st.PrepareConversationSegment(ctx, c.MachineID, c.SessionID, op.OperationID, conversationlog.Segment{ID: randomHex(16), MachineID: p.MachineID, SessionID: reply.SessionID, Engine: target.Engine, Launcher: p.Launcher, Model: p.Model, Effort: p.Effort, PermissionMode: p.PermissionMode}, *tail.ToSeq); err != nil {
			return err
		}
		c, err = st.LogicalConversation(ctx, c.MachineID, c.SessionID)
		if err != nil {
			return err
		}
		op = c.Switch
	}
	if op != nil && op.Phase == "prepared" {
		// Card creation/approval is fenced by this durable unfinished operation.
		for _, ix := range s.pendingInteractions() {
			if ix.MachineID == c.MachineID && ix.SessionID == c.SessionID {
				return logicalRefusal(ctx, "busy")
			}
		}
		if source, ok := c.Active(); ok {
			_, err := s.logicalPhysical(ctx, source.MachineID, "assistant.segment.retire", map[string]any{"session_id": source.SessionID})
			if err != nil && !errors.Is(err, apierr.ErrOffline) {
				return err
			}
		}
		if err := st.ActivateConversationSegment(ctx, c.MachineID, c.SessionID, op.OperationID, c.Epoch); err != nil {
			return err
		}
		var err error
		c, err = st.LogicalConversation(ctx, c.MachineID, c.SessionID)
		if err != nil {
			return err
		}
		s.logicalRebind(ctx, c)
	}
	if c == nil {
		return nil
	}
	return s.drainLogicalInput(ctx, st, c)
}

func (s *Service) drainLogicalInput(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation) (retErr error) {
	a, ok := c.Active()
	if !ok {
		return nil
	}
	unresolved, err := st.ConversationUnresolvedInput(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return err
	}
	if unresolved != nil {
		// A positive receipt proves delivery. An absent receipt never proves refusal.
		raw, err := s.logicalPhysical(ctx, a.MachineID, "assistant.segment.input", map[string]any{"session_id": a.SessionID, "client_msg_id": unresolved.ClientMsgID})
		var receipt struct {
			Accepted bool `json:"accepted"`
		}
		if err != nil || json.Unmarshal(raw, &receipt) != nil || !receipt.Accepted {
			return nil
		}
		operation := ""
		if c.Switch != nil {
			operation = c.Switch.OperationID
		}
		if err = st.ResolveConversationDelivery(ctx, c.MachineID, c.SessionID, unresolved.ClientMsgID, c.Epoch, operation, "accepted"); err != nil {
			return err
		}
		if c.Switch != nil {
			next, err := st.LogicalConversation(ctx, c.MachineID, c.SessionID)
			if err != nil {
				return err
			}
			s.logicalRebind(ctx, next)
		}
		return nil
	}
	input, err := st.ClaimConversationInput(ctx, c.MachineID, c.SessionID, c.Epoch)
	if err != nil || input == nil {
		return err
	}
	// Until the input call is attempted, local validation/restore failures prove
	// no message was sent. Do not strand that input as an ambiguous crash claim.
	dispatched := false
	defer func() {
		if retErr == nil || dispatched {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		operation := ""
		if c.Switch != nil {
			operation = c.Switch.OperationID
		}
		if err := st.RequeueConversationInput(cleanup, c.MachineID, c.SessionID, input.ClientMsgID, c.Epoch, operation); err != nil {
			retErr = errors.Join(retErr, err)
			return
		}
		if c.Switch != nil {
			if next, err := st.LogicalConversation(cleanup, c.MachineID, c.SessionID); err == nil {
				s.logicalRebind(cleanup, next)
			}
		}
		s.historyWake()
	}()
	var queued logicalInput
	if err = json.Unmarshal(input.Payload, &queued); err != nil {
		return err
	}
	if queued.Transport != nil {
		ctx = context.WithValue(ctx, logicalTransportKey{}, *queued.Transport)
	}
	if err = s.putSupervisionInstructions(ctx, c.MachineID, c.SessionID, queued.Params); err != nil {
		return err
	}
	if a.Ordinal > 1 {
		// Execution choices were resolved against the source when this input was
		// queued. Only its content/provenance travels; the activated target owns
		// launcher/model/permission and process-specific configuration.
		for _, key := range []string{"launcher", "model", "effort", "permission_mode", "config_dir", "model_fingerprint", "runtime", "external"} {
			delete(queued.Params, key)
		}
		queued.Params["launcher"], queued.Params["model"], queued.Params["effort"], queued.Params["permission_mode"] = a.Launcher, a.Model, a.Effort, a.PermissionMode
	}
	if c.Switch != nil {
		raw, _, err := st.ConversationPreparation(ctx, c.MachineID, c.SessionID, c.Switch.OperationID)
		if err != nil {
			return err
		}
		var h mainHandoff
		if err = json.Unmarshal(raw, &h); err != nil {
			return err
		}
		text, _ := queued.Params["text"].(string)
		queued.Params["text"] = renderLogicalHandoff(string(s.lang(ctx)), h, text)
	}
	var sendErr error
	err = st.WithConversationEpoch(ctx, c.MachineID, c.SessionID, c.Epoch, func(current *db.LogicalConversation) error {
		active, ok := current.Active()
		if !ok || active.ID != a.ID {
			return db.ErrConversationEpoch
		}
		if current.Epoch > 1 {
			if _, err := s.logicalPhysical(ctx, a.MachineID, "assistant.segment.restore", map[string]any{"session_id": a.SessionID}); err != nil {
				return err
			}
		}
		dispatched = true
		_, sendErr = s.logicalPhysical(ctx, a.MachineID, queued.Method, cloneLogicalParams(queued.Params, a))
		return nil
	})
	if err != nil {
		return err
	}
	outcome := "accepted"
	if sendErr != nil {
		outcome = "in_doubt"
		if logicalNotTaken(sendErr) {
			outcome = "rejected"
		}
	}
	operation := ""
	if c.Switch != nil {
		operation = c.Switch.OperationID
	}
	if err = st.ResolveConversationDelivery(ctx, c.MachineID, c.SessionID, input.ClientMsgID, c.Epoch, operation, outcome); err != nil {
		return err
	}
	if c.Switch != nil {
		next, err := st.LogicalConversation(ctx, c.MachineID, c.SessionID)
		if err != nil {
			return err
		}
		s.logicalRebind(ctx, next)
	}
	s.historyWake()
	return sendErr
}

func renderLogicalHandoff(language string, h mainHandoff, text string) string {
	turns := make([]prompts.HandoffTurn, 0, len(h.Recent))
	for _, t := range h.Recent {
		turns = append(turns, prompts.HandoffTurn{Role: t.Role, Text: t.Text})
	}
	return prompts.RenderSegmentHandoff(prompts.SegmentHandoff{Language: language, Summary: h.Summary, Recent: turns, Text: text, Partial: h.Partial})
}

func (s *Service) logicalSwitchHandoff(ctx context.Context, st logicalSwitchStore, c *db.LogicalConversation) (mainHandoff, error) {
	raw, err := s.logicalHistoryCall(ctx, st, c, DeviceAssistantHandoff, json.RawMessage(`{}`))
	if err != nil {
		return mainHandoff{}, err
	}
	var h mainHandoff
	if err = json.Unmarshal(raw, &h); err != nil {
		return h, err
	}
	previousSummary := h.Summary
	h.Summary = ""
	if a, ok := c.Active(); ok {
		if history, ok := s.store().(conversationHistoryStore); ok {
			source, err := history.ConversationHistory(ctx, a.MachineID, a.SessionID)
			if err != nil {
				return h, err
			}
			if source != nil && source.SummaryKnown && strings.TrimSpace(source.Summary) != "" {
				h.Summary = source.Summary
				return h, nil
			}
		}
	}
	// The source engine's own compaction result is always preferred. The logical
	// handoff already reads persisted summaries; ask its active source once too.
	if a, ok := c.Active(); ok {
		var source mainHandoff
		if err = s.physicalHistoryCall(ctx, a.MachineID, DeviceAssistantHandoff, map[string]any{"session_id": a.SessionID}, &source); err == nil && strings.TrimSpace(source.Summary) != "" {
			h.Summary = source.Summary
			return h, nil
		}
	}
	if len(h.Recent) == 0 {
		return h, nil
	}
	page, err := s.logicalHistoryPage(ctx, st, c, historyParams{Limit: 500, LazyImages: true})
	if err != nil {
		return h, err
	}
	summaryTurns := []prompts.HandoffTurn{}
	size := 0
	for i := len(page.Items) - 1; i >= 0; i-- {
		item := page.Items[i]
		if item.Kind != proto.KindText || (item.Role != "user" && item.Role != "assistant") {
			continue
		}
		if item.Stored != nil && !item.Stored.Complete {
			h.Partial = true
			continue
		}
		text := strings.TrimSpace(transcript.StripInjected(item.Text))
		if text == "" {
			continue
		}
		if size+len(text) > 120000 {
			h.Partial = true
			break
		}
		size += len(text)
		summaryTurns = append(summaryTurns, prompts.HandoffTurn{Role: item.Role, Text: text})
	}
	slices.Reverse(summaryTurns)
	h.Partial = h.Partial || page.Partial || (page.HasMore != nil && *page.HasMore)
	input, _ := json.Marshal(map[string]any{"previous_summary": previousSummary, "history": summaryTurns})
	call, err := checkModelCall(assistantwire.ModelCompleteParams{System: prompts.RenderSegmentSummary(answerLanguage(s.hello(ctx).Locale)), Messages: []assistantwire.ModelMessage{{Role: "user", Content: string(input)}}, MaxTokens: 2048})
	if err != nil {
		return h, err
	}
	result, err := waitLogicalSummary(ctx, func() (assistantwire.ModelCompleteResult, error) {
		out, err := s.runModelCall(ctx, "kb", "conversation handoff", "conversation handoff", call)
		if err != nil {
			return assistantwire.ModelCompleteResult{}, err
		}
		value, ok := out.(assistantwire.ModelCompleteResult)
		if !ok {
			return value, errors.New("invalid summary result")
		}
		return value, nil
	})
	if err != nil {
		return h, err
	}
	h.Summary = result.Text
	return h, nil
}

func waitLogicalSummary(ctx context.Context, run func() (assistantwire.ModelCompleteResult, error)) (assistantwire.ModelCompleteResult, error) {
	for {
		out, err := run()
		if err == nil {
			return out, nil
		}
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != assistantwire.CodeModelPaused {
			return out, err
		}
		var pause assistantwire.ModelPause
		_ = json.Unmarshal(re.Data, &pause)
		until, _ := time.Parse(time.RFC3339, pause.Until)
		wait := time.Until(until)
		if wait < time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
}

// Node receipts are scoped by logical identity as well as the public operation ID.
func logicalNodeOperation(c *db.LogicalConversation, operation string) string {
	data, _ := json.Marshal([3]string{c.MachineID, c.SessionID, operation})
	digest := sha256.Sum256(data)
	return "segment-" + hex.EncodeToString(digest[:])
}
