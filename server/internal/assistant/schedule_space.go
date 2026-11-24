package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A scheduled task's track (#13501, docs/assistant/health.md "Tracks and kinds"). A task set up from a conversation
// takes that conversation's track (selfSpace); the owner may also put a task into a track from the apps' editor
// (space on POST / PUT /api/v1/assistant/tasks). A task of a track runs in the track's directory, never in a cwd of
// its own (checkTaskExecutionSpace), its run conversation is one of the track's, and what reaches the phone follows
// the track's policy (exitPolicy with Task.Space). The health space is one such track; its behavior is unchanged.

// The topic policies of a recurring task (topic_policy): how its runs share conversations.
const (
	// topicPolicyContinue: every run continues the task's one conversation (the default).
	topicPolicyContinue = "continue"
	// topicPolicyNew: every run starts a conversation of its own (FreshEachRun).
	topicPolicyNew = "new"
)

// errTaskSpaceForbidden: only the owner's full credentials put a task into a track or take it out of one.
var errTaskSpaceForbidden = i18n.Err("assistant-schedule.space_owner_only")

// readTopicPolicy applies topic_policy from a task body to t: the same choice as fresh_each_run, named as a track's
// rhythms name it (assistantwire.TrackRhythm.TopicPolicy). Absent keeps what fresh_each_run left; it wins over
// fresh_each_run when a body has both.
func readTopicPolicy(raw map[string]json.RawMessage, t *Task) error {
	v, ok := raw["topic_policy"]
	if !ok {
		return nil
	}
	var p string
	if err := json.Unmarshal(v, &p); err != nil {
		return i18n.Err("assistant-schedule.bad_topic_policy")
	}
	switch strings.TrimSpace(p) {
	case topicPolicyNew:
		t.FreshEachRun = true
	case topicPolicyContinue:
		t.FreshEachRun = false
	default:
		return i18n.Err("assistant-schedule.bad_topic_policy")
	}
	return nil
}

// taskSpaceAllowed: space is one a task may run in: the assistant's own (""), the health space, or a track with a
// directory of its own here (spaceTrack); an archived track or one without a directory is not.
func (s *Service) taskSpaceAllowed(ctx context.Context, space string) bool {
	if space == "" || space == SpaceHealth {
		return true
	}
	_, ok := s.spaceTrack(ctx, space)
	return ok
}

// applyTaskSpace applies space from a task body to t. Absent keeps the task's track, so an app that does not know
// the field never moves a task, and echoing it unchanged is no change. Moving a task to another track needs full
// credentials (errTaskSpaceForbidden) and a track it may run in; it leaves the conversation it came from and its own
// conversation behind, both of the track it was in.
func (s *Service) applyTaskSpace(r *http.Request, t *Task, raw map[string]json.RawMessage) error {
	v, ok := raw["space"]
	if !ok {
		return nil
	}
	var space string
	if err := json.Unmarshal(v, &space); err != nil {
		return i18n.Err("assistant-schedule.unknown_space")
	}
	space = strings.TrimSpace(space)
	if space == t.Space {
		return nil
	}
	if !fullRole(r) {
		return errTaskSpaceForbidden
	}
	if !s.taskSpaceAllowed(r.Context(), space) {
		return i18n.Err("assistant-schedule.unknown_space")
	}
	t.Space = space
	t.SessionID = ""
	t.RunMachineID, t.RunSessionID, t.RunLauncher, t.RunExecution = "", "", "", nil
	return nil
}

// writeTaskSpaceError answers an applyTaskSpace or readTopicPolicy error.
func writeTaskSpaceError(w http.ResponseWriter, err error) {
	if errors.Is(err, errTaskSpaceForbidden) {
		apierr.Write(w, http.StatusForbidden, err)
		return
	}
	apierr.Write(w, http.StatusBadRequest, err)
}
