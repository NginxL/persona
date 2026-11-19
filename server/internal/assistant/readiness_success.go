package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A successful completed turn remains evidence after its process is released. Keep the event's
// timestamp, not the time readiness is read, so a newer real failure still wins.
type readinessSuccess struct {
	Machine     string    `json:"machine"`
	Launcher    string    `json:"launcher"`
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
}

func (s *Service) readinessModelScope(ctx context.Context, launcher string) (ModelBinding, string) {
	binding, err := s.resolveModel(ctx, "assistant")
	if err != nil || binding.Provider != launcherProvider(launcher) {
		return ModelBinding{}, ""
	}
	return binding, s.modelBindingFingerprint(ctx, binding)
}

func readinessSucceeded(sess proto.Session, binding ModelBinding, fingerprint string, previous *readinessSuccess) bool {
	if fingerprint == "" || sess.UpdatedAt.IsZero() || sess.Starting || !answeredWell(sess) {
		return false
	}
	if sess.Status != "" && sess.Status != proto.StatusDone && sess.Status != proto.StatusIdle {
		return false
	}
	if err, _ := latestTurnError(sess); err != "" || sessionTurnErrorCode(sess) != "" {
		return false
	}
	if previous != nil && previous.Fingerprint != fingerprint && !sess.UpdatedAt.After(previous.At) {
		return false // Re-reading an old reply cannot validate a newly selected connection.
	}
	launcher := strings.TrimPrefix(binding.Provider, "launcher.")
	return sess.Launcher == "" || sess.Launcher == launcher
}

// readinessReplySucceeded also checks which model answered. A per-conversation pick or a reply from before a
// configuration change is not evidence for the binding resolved now. Launcher aliases and its actual default are one
// execution model; two different models offered by the same launcher are not.
func (s *Service) readinessReplySucceeded(ctx context.Context, machine, launcher string, sess proto.Session,
	binding ModelBinding, fingerprint string, previous *readinessSuccess) bool {
	if !readinessSucceeded(sess, binding, fingerprint, previous) {
		return false
	}
	if sess.Model == "" {
		return true
	}
	aliases, defaultModel := s.launcherModelResolution(withLauncherPurpose(ctx, "assistant"), machine, launcher)
	want := binding.Model
	if want == "" {
		if defaultModel == "" {
			return true // The launcher's default is unknown: keep the existing default-binding behavior.
		}
		want = defaultModel
	}
	if sess.Model == want || proto.SameLauncherModel(sess.Model, want) {
		return true
	}
	// The alias and default relationships, comparing the same model under another spelling: a gateway may echo one
	// routed under a prefix, or with/without the 1M marker (proto.SameLauncherModel).
	for alias, resolved := range aliases {
		if proto.SameLauncherModel(alias, sess.Model) && (resolved == want || proto.SameLauncherModel(resolved, want)) {
			return true
		}
		if proto.SameLauncherModel(resolved, sess.Model) && (alias == want || proto.SameLauncherModel(alias, want)) {
			return true
		}
	}
	return false
}

// Capture completion without requiring an open client to poll readiness before the process exits.
func (s *Service) rememberMainReadinessSuccess(ctx context.Context, machine string, sess proto.Session) {
	v := s.settings(ctx)
	if machine != v.MachineID || sess.SessionID == "" || sess.SessionID != v.Main {
		return
	}
	launcher := s.launcherNow(ctx)
	binding, fingerprint := s.readinessModelScope(ctx, launcher)
	if s.readinessReplySucceeded(ctx, machine, launcher, sess, binding, fingerprint, s.readinessRecordNow(ctx).Success) {
		s.keepReadinessSuccess(ctx, readinessSuccess{Machine: machine, Launcher: launcher,
			Fingerprint: fingerprint, At: sess.UpdatedAt})
	}
}

func (s *Service) keepReadinessSuccess(ctx context.Context, success readinessSuccess) {
	b := &s.ready
	b.mu.Lock()
	defer b.mu.Unlock()

	s.readinessLoadLocked(ctx)
	st := s.store()
	if !b.loaded && st != nil {
		return // Never replace an unread record with a partial one.
	}
	if previous := b.rec.Success; previous != nil && !success.At.After(previous.At) {
		return // Repeated reads and late completion callbacks do not refresh the evidence's age.
	}

	next := b.rec
	next.Success = &success
	// A repaired failure is superseded, not merely hidden while this process is alive.
	if probe := next.Probe; probe != nil && !probe.Check.OK && probe.Machine == success.Machine &&
		probe.Launcher == success.Launcher && probe.At.Before(success.At) {
		next.Probe = nil
	}
	if next.AnsweredAt == nil {
		at := success.At
		next.AnsweredAt = &at
	}
	if st != nil {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), historyTimeout)
		err := st.PutSetting(wctx, readinessKey, next)
		cancel()
		if err != nil {
			s.logf("assistant: readiness success: %v", err)
			return // Leave it retryable on the next observation.
		}
	}
	b.rec = next
}
