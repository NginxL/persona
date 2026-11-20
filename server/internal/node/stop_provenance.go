package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (s *stateFile) stopped(id string, cause proto.StopCause) {
	s.update(id, func(v *sessionState) bool {
		v.StopCause = cause
		v.StopCauseAt = max(time.Now().UnixNano(), v.StopCauseAt+1)
		return true
	})
}

// A new running observation invalidates the previous stopped-session capability.
func (n *Node) observedRunning(id string) {
	release, ok := n.recovery.tryLock(id)
	if !ok {
		return // Never clear a user stop while its close/quit is still in progress.
	}
	defer release()
	n.recordRunning(id)
}

func (n *Node) recordRunning(id string) {
	now := time.Now()
	n.state.update(id, func(v *sessionState) bool {
		changed := false
		if v.StopCause != proto.StopUnknown {
			v.StopCause = proto.StopUnknown
			v.StopCauseAt = max(now.UnixNano(), v.StopCauseAt+1)
			changed = true
		}
		if now.Unix()-v.RanAt >= int64(ranAtEvery/time.Second) {
			v.RanAt = now.Unix()
			changed = true
		}
		return changed
	})
}

// ranAtEvery: how often a running Agent's RanAt is written; restartWindow: how long before this node started an Agent
// may last have been seen running for its end to count as the restart's (markInterrupted). A quick restart or upgrade
// falls inside it; a machine that was off for longer leaves the cause unknown.
const (
	ranAtEvery    = 5 * time.Minute
	restartWindow = 2 * ranAtEvery
)

// state.put is one-way. Verify the cause before closing or consuming a stopped
// capability: a failed save must not revive old recovery permission after a restart.
func (n *Node) saveStopCause(ctx context.Context, id string, cause proto.StopCause) error {
	n.state.stopped(id, cause)
	want, known := n.state.get(id)
	if !known || n.peer == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var saved struct {
		Sessions map[string]sessionState `json:"sessions"`
	}
	err := n.peer.Call(cctx, "state.load", map[string]any{}, &saved)
	got, ok := saved.Sessions[id]
	if err != nil || !ok || got.StopCause != cause || got.StopCauseAt != want.StopCauseAt {
		return refusal("stop_not_saved", "The stop state could not be saved. Nothing was started, stopped or closed; try again.", nil)
	}
	return nil
}

func (n *Node) stopCause(id string) proto.StopCause {
	st, _ := n.state.get(id)
	return st.StopCause
}

// Called again under the session's recovery lock: a cached assistant row must not
// undo a user close that happened after that row was listed.
func (n *Node) supervisorResumeAllowed(id, mode, launcher string) error {
	cause := n.stopCause(id)
	if !validID(id) || !cause.AllowsSupervisorResume() {
		return refusal("resume_not_allowed", fmt.Sprintf("Supervisor resume is refused: stop cause is %s. Use the owner's Resume.", cause), nil)
	}
	if mode != "" || launcher != "" {
		return refusal("resume_not_allowed", "Supervisor resume must keep the session's original launcher and permission mode.", nil)
	}
	if _, live := n.agent(id); live {
		return refusal("resume_not_allowed", "The session is running again; refresh it before sending.", nil)
	}
	return nil
}

// startFailedForSure: a start's error says nothing of it ran (a refusal with delivered none) and no tab was kept for
// it (withKeptTab). Any other failure (a line typed into a shell, a stalled agent, an unanswered call) may still bring
// the Agent up, and a second resume must not start it twice: those stay unknown (#8214).
func startFailedForSure(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || deliveredOf(re) != deliveredNone {
		return false
	}
	if len(re.Data) > 0 {
		var d struct {
			Pane      string `json:"pane"`
			KeptUntil string `json:"kept_until"`
		}
		if json.Unmarshal(re.Data, &d) == nil && (d.Pane != "" || d.KeptUntil != "") {
			return false
		}
	}
	return true
}

// noteResumeFailed records the assistant's resume of session id that ended in err with the Agent not running as
// resume_failed: stopped by Persona's own step, which the assistant may try again, never unknown (#11441). Nothing
// when the resume went (err nil) or the Agent runs after all.
func (n *Node) noteResumeFailed(id string, err error) {
	if err == nil || !startFailedForSure(err) {
		return // an uncertain start may still bring it up: a second resume must not start it twice (unknown, #8214)
	}
	if _, live := n.agent(id); live {
		return
	}
	n.state.stopped(id, proto.StopResumeFailed)
	n.logf("session %s: the assistant's resume failed: %v", short(id), err)
}
