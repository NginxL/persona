package assistant

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// "Archive and close" (docs/protocol/protocol.md "Agent session archives"): the session is archived, then its agent is
// closed on its computer the way a finished task's agent is (session.retire, node retire.go, tasks_close.go): its tab
// and process go, its transcript and how it was started stay, and the next message to it resumes it (claude --resume,
// codex resume). Restoring it from Archived reopens nothing.
//
// A busy agent is refused before anything changes (409 agent_busy): working, waiting for an answer, background work,
// running subagents or workflows (agentArchiveBusy), and also a /loop, queued messages or a server it runs (closing
// would stop what the user may be using). The archive comes first and is never undone by the close: when the node
// refuses (it cannot bring the agent back: another tool owns it, its engine continues sessions itself, its launcher is
// unknown…), the computer is offline, or the call fails, the session stays archived and still open, and the answer
// says so with the reason. The user can close it from the computer or restore it.

// Outcomes of the close in an "Archive and close" answer (stable: clients branch on them).
const (
	archiveCloseClosed     = "closed"      // its tab and process are gone
	archiveCloseNotRunning = "not_running" // it was not running: nothing to close
	archiveCloseRefused    = "refused"     // the node would not close it (reason, detail)
	archiveCloseOffline    = "offline"     // its computer is not connected
	archiveCloseFailed     = "failed"      // the call failed (detail)
)

// agentArchiveCloseTimeout bounds the node's close (it interrupts the agent, waits and closes the tab).
var agentArchiveCloseTimeout = 20 * time.Second

// agentArchiveCloseResult is what "Archive and close" did beyond the archive: the outcome, an i18n key saying why it
// is still open (empty when closed or not running), and the node's own words when it gave any.
type agentArchiveCloseResult struct {
	Closed    bool
	Outcome   string
	ReasonKey string
	Detail    string
}

// agentCloseBusy: closing the agent now would cut off work, or something it runs the user may be using.
func agentCloseBusy(s proto.Session) bool {
	return agentArchiveBusy(s) || s.Loop != nil || len(s.Queued) > 0 || len(s.Ports) > 0
}

// archiveAndCloseAgentSession archives machine mid's session sid and closes its agent on its computer. The error is a
// refusal (nothing changed: unknown, assistant, busy) or a storage failure (nothing changed either); with no error the
// session is archived and the result says whether its agent was closed.
func (svc *Service) archiveAndCloseAgentSession(ctx context.Context, mid, sid string) (agentArchiveCloseResult, error) {
	look, cancel := context.WithTimeout(ctx, agentArchiveTimeout)
	row, err := svc.agentArchiveTarget(look, mid, sid)
	cancel()
	if err != nil {
		return agentArchiveCloseResult{}, err
	}
	if agentCloseBusy(row) {
		return agentArchiveCloseResult{}, archiveBusyRefusal(row, true)
	}
	if row, err = svc.setAgentArchive(ctx, row.MachineID, row.SessionID, true); err != nil {
		return agentArchiveCloseResult{}, err
	}
	res := svc.retireArchived(ctx, row)
	if res.Closed {
		// What the agent wrote while it was closed must not bring the session back (agentArchive.activeAfter).
		if _, err := svc.noteAgentArchiveClosed(ctx, row, time.Now().UTC(), true); err != nil {
			svc.logf("agent archive %s/%s: note close: %v", row.MachineID, row.SessionID, err)
			// The terminal is gone even when its secondary metadata could not be saved. Recheck the generation before
			// preserving the immediate retry, so a newer restore cannot be overwritten after a read/lock failure too.
			svc.retryNativeArchiveClosed(ctx, row)
		}
	}
	return res, nil
}

// retireArchived closes the archived session's agent through its node. A runtime with a declared close capability
// uses session.close; a terminal agent uses session.retire, and the result says how that went.
func (svc *Service) retireArchived(ctx context.Context, row proto.Session) agentArchiveCloseResult {
	if row.Status == proto.StatusOffline {
		return agentArchiveCloseResult{Outcome: archiveCloseNotRunning}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentArchiveCloseTimeout)
	defer cancel()
	var out struct {
		Closed bool `json:"closed"`
	}
	method := deviceSessionRetire
	runtimeClose := row.Capabilities != nil && row.Capabilities.Close != nil && *row.Capabilities.Close
	if runtimeClose {
		method = "session.close"
	}
	// close_terminal: the user asked to close it (a node before #4754 closes it anyway).
	params := map[string]any{"session_id": row.SessionID}
	if !runtimeClose {
		params["close_terminal"] = true
	}
	err := svc.call(ctx, row.MachineID, method, params, &out)
	var re *rpc.Error
	switch {
	case err == nil && (runtimeClose || out.Closed):
		svc.logf("agent archive %s/%s: closed its agent", row.MachineID, row.SessionID)
		return agentArchiveCloseResult{Closed: true, Outcome: archiveCloseClosed}
	case err == nil:
		return agentArchiveCloseResult{Outcome: archiveCloseNotRunning}
	case errors.As(err, &re) && re.Code == "not_retirable":
		why := strings.TrimSpace(re.Message)
		return agentArchiveCloseResult{Outcome: archiveCloseRefused, ReasonKey: retireRefusalKey(why), Detail: why}
	case errors.Is(err, ErrNoDevice) || apierr.NodeCode(err) == "node_unreachable":
		return agentArchiveCloseResult{Outcome: archiveCloseOffline, ReasonKey: "hub-agent_archives.close_offline"}
	case errors.As(err, &re) && strings.Contains(re.Message, "session.retire"):
		// a node from before session.retire ("unknown method"): it cannot close it this way
		return agentArchiveCloseResult{Outcome: archiveCloseRefused, ReasonKey: "hub-agent_archives.close_node_old", Detail: re.Message}
	default:
		svc.logf("agent archive %s/%s: close: %v", row.MachineID, row.SessionID, err)
		return agentArchiveCloseResult{Outcome: archiveCloseFailed, ReasonKey: "hub-agent_archives.close_failed", Detail: err.Error()}
	}
}

// retireRefusalKey says in the user's words why a node would not close an agent (node retire.go's refusals).
func retireRefusalKey(why string) string {
	switch {
	case strings.HasPrefix(why, "the agent is"), strings.HasPrefix(why, "still starting"):
		return "hub-agent_archives.close_busy"
	case strings.Contains(why, "another tool"), strings.Contains(why, "engine"), strings.HasPrefix(why, "Orca"):
		return "hub-agent_archives.close_engine"
	case strings.Contains(why, "transcript"), strings.Contains(why, "launcher"), strings.HasPrefix(why, "cannot resume"):
		return "hub-agent_archives.close_not_resumable"
	}
	return "hub-agent_archives.close_refused"
}

// noteAgentArchiveClosed marks the generation that started a close closed at `at`. It returns the archive read marker
// for the completion's publish, or zero when a restore/re-archive won and there is nothing to publish. When
// retryNative is set, the native retry is emitted under the archive gate only after this generation's close metadata
// is saved.
func (svc *Service) noteAgentArchiveClosed(ctx context.Context, s proto.Session, at time.Time, retryNative bool) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentArchiveTimeout)
	defer cancel()
	b := &svc.agentArchives
	if err := b.lock(ctx); err != nil {
		return 0, err
	}
	defer b.unlock()
	if err := svc.loadAgentArchivesLocked(ctx); err != nil {
		return 0, err
	}
	key := runtimeListID(s.MachineID, s.SessionID)
	rec, ok := b.records[key]
	if !ok || !rec.Archived || s.ArchiveChangedAt == nil || !rec.ChangedAt.Equal(*s.ArchiveChangedAt) {
		return 0, nil
	}
	rec.ClosedAt = at
	next := maps.Clone(b.records)
	next[key] = rec
	if err := svc.saveAgentArchivesLocked(ctx, next); err != nil {
		return 0, err
	}
	if retryNative {
		svc.nativeArchiveWanted(s, true) // its terminal is gone: the tool's archive goes through now
	}
	return b.writes.Load(), nil
}

// retryNativeArchiveClosed preserves the immediate retry after a close metadata error only if the close's generation
// is still current. In particular, a read failure after a newer restore must not put back the old native archive wish.
func (svc *Service) retryNativeArchiveClosed(ctx context.Context, s proto.Session) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentArchiveTimeout)
	defer cancel()
	b := &svc.agentArchives
	if err := b.lock(ctx); err != nil {
		return
	}
	defer b.unlock()
	if err := svc.loadAgentArchivesLocked(ctx); err != nil {
		return
	}
	rec, ok := b.records[runtimeListID(s.MachineID, s.SessionID)]
	if !ok || !rec.Archived || s.ArchiveChangedAt == nil || !rec.ChangedAt.Equal(*s.ArchiveChangedAt) {
		return
	}
	svc.nativeArchiveWanted(s, true)
}

// archiveAndCloseHTTP is POST /api/v1/sessions/{mid}/{sid}/archive?close=1: 200 once archived, with what the close
// did; refusals and storage failures are answered as a plain archive answers them.
func (svc *Service) archiveAndCloseHTTP(w http.ResponseWriter, r *http.Request, mid string) {
	res, err := svc.archiveAndCloseAgentSession(r.Context(), mid, r.PathValue("sid"))
	if err != nil {
		svc.writeAgentArchiveError(w, err)
		return
	}
	closeBody := map[string]any{"closed": res.Closed, "outcome": res.Outcome}
	if res.ReasonKey != "" {
		closeBody["reason"] = i18n.T(i18n.OfWriter(w), res.ReasonKey)
	}
	if res.Detail != "" {
		closeBody["detail"] = res.Detail
	}
	if m, ok := svc.fleetMachine(mid); ok {
		mid = m.ID
	}
	apierr.WriteJSON(w, map[string]any{"ok": true, "archived": true, "close": closeBody,
		"native": svc.nativeArchiveAwaited(r.Context(), mid, r.PathValue("sid"))})
}
