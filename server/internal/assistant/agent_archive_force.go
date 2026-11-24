package assistant

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Force archive is an explicit cancellation of a confirmation-blocked Agent, not a general busy override.
func confirmationArchiveSafe(row proto.Session) bool {
	if row.Assistant || row.Status != proto.StatusBlocked {
		return false
	}
	row.Status = proto.StatusIdle
	if row.State == proto.StateNeedsYou {
		row.State = proto.StateIdle
	}
	row.PendingInteractions = 0
	return !agentCloseBusy(row)
}

func (s *Service) forceArchiveConfirmation(ctx context.Context, mid, sid string) error {
	row, err := s.agentArchiveTarget(ctx, mid, sid)
	if err != nil {
		return err
	}
	raw, listed := s.nodeSession(row.MachineID, sid)
	if !confirmationArchiveSafe(row) {
		return forceArchiveRefusal(row)
	}
	if !listed {
		return archiveRefusal(http.StatusConflict, "agent_busy", i18n.ErrEnglish("hub-agent_archives.force_unverified"))
	}
	if !confirmationArchiveSafe(raw) {
		// The decorated target is blocked even if the raw row no longer is.
		// Do not suggest plain archive from that less restrictive snapshot.
		if agentArchiveBusy(raw) {
			return archiveBusyRefusal(raw, false)
		}
		return archiveRefusal(http.StatusConflict, "agent_busy", i18n.ErrEnglish("hub-agent_archives.force_unverified"))
	}
	for _, ix := range s.pendingInteractions() {
		if ix.MachineID == row.MachineID && ix.SessionID == sid &&
			ix.Kind != proto.IxPermission && ix.Kind != proto.IxPlan && ix.Kind != proto.IxScreen {
			return archiveRefusal(http.StatusConflict, "agent_busy", i18n.ErrEnglish("hub-agent_archives.force_question"))
		}
	}
	// Persist the archive and close fence together before the node can cancel its runtime.
	provisional, err := s.confirmationArchiveFenceAt(ctx, row, true)
	if err != nil {
		return err
	}
	row.ArchiveChangedAt = archiveChangedAt(provisional)
	closeCtx, cancel := context.WithTimeout(ctx, agentArchiveCloseTimeout)
	defer cancel()
	// Closing kills the terminal or closes the provider runtime. Never press Enter or approve the pending request.
	// The node checks blocked again; a failed close leaves the Agent visible and its cards intact.
	if err := s.nodeCall(closeCtx, row.MachineID, "session.close", map[string]any{"session_id": sid, "force": true}, nil); err != nil {
		// The request may already be cancelled. Roll back with a separate bounded context.
		return errors.Join(err, s.confirmationArchiveFence(context.WithoutCancel(ctx), row, false))
	}
	closedAt := time.Now().UTC()
	no := false
	for _, ix := range s.pendingInteractions() {
		if ix.MachineID != row.MachineID || ix.SessionID != sid {
			continue
		}
		r := proto.Resolution{Dismiss: true}
		if ix.Kind == proto.IxPermission {
			r = proto.Resolution{Allow: &no}
		} else if ix.Kind == proto.IxPlan {
			r = proto.Resolution{Approve: &no}
		}
		// The runtime may already have withdrawn its cards during close. No error can turn this into an allow.
		if err := s.resolve(ix.ID, r); err != nil && !errors.Is(err, errNotPending) {
			s.logf("force archive %s: cancel confirmation %s after close: %v", short(sid), ix.ID, err)
		}
	}
	publishMark, err := s.noteAgentArchiveClosed(ctx, row, closedAt, true)
	if err != nil {
		return err
	}
	if publishMark != 0 {
		s.publishListAtArchiveMark(row.MachineID, publishMark)
	}
	return nil
}

// confirmationArchiveFence never publishes an archived confirmation with an empty ClosedAt.
// A failed close turns only its own provisional record into a new restored generation; a stale rollback leaves a
// newer restore, archive or close generation untouched.
func (s *Service) confirmationArchiveFence(ctx context.Context, row proto.Session, closing bool) error {
	_, err := s.confirmationArchiveFenceAt(ctx, row, closing)
	return err
}

func (s *Service) confirmationArchiveFenceAt(ctx context.Context, row proto.Session, closing bool) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, agentArchiveTimeout)
	defer cancel()
	b := &s.agentArchives
	if err := b.lock(ctx); err != nil {
		return time.Time{}, err
	}
	defer b.unlock()
	if err := s.loadAgentArchivesLocked(ctx); err != nil {
		return time.Time{}, err
	}
	key := runtimeListID(row.MachineID, row.SessionID)
	next := maps.Clone(b.records)
	if closing {
		changedAt := nextAgentArchiveChangedAt(b.records[key].ChangedAt)
		next[key] = agentArchive{Session: archiveSnapshot(row), Archived: true, ChangedAt: changedAt,
			ClosedAt: time.Now().UTC().Add(agentArchiveCloseTimeout + agentArchiveTimeout)}
	} else {
		rec, ok := b.records[key]
		if !ok || row.ArchiveChangedAt == nil || !rec.ChangedAt.Equal(*row.ArchiveChangedAt) {
			return rec.ChangedAt, nil
		}
		rec.Archived = false
		rec.ClosedAt = time.Time{}
		rec.ChangedAt = nextAgentArchiveChangedAt(rec.ChangedAt)
		next[key] = rec
	}
	changedAt := next[key].ChangedAt
	if err := s.saveAgentArchivesLocked(ctx, next); err != nil {
		return time.Time{}, err
	}
	delete(b.pending, key)
	return changedAt, nil
}

// An idle session does not qualify for force cancellation, even if plain archive
// is available. Keep that distinction from the busy-state refusal.
func forceArchiveRefusal(row proto.Session) error {
	if !agentCloseBusy(row) {
		return archiveRefusal(http.StatusConflict, "agent_busy", i18n.ErrEnglish("hub-agent_archives.force_not_blocked"))
	}
	return archiveBusyRefusal(row, true)
}
