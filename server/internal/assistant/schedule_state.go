package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const taskStateMaxBytes = 4096

var errTaskStateChanged = i18n.Err("assistant-schedule.task_state_changed")

// TaskStateNote holds the task's durable cursor. A revision also covers an explicit
// clear so a run based on an older revision cannot restore the deleted state.
type TaskStateNote struct {
	Text      string    `json:"text"`
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (n *TaskStateNote) revision() uint64 {
	if n == nil {
		return 0
	}
	return n.Revision
}

func nextTaskState(old *TaskStateNote, text string, now time.Time) *TaskStateNote {
	return &TaskStateNote{Text: text, Revision: old.revision() + 1, UpdatedAt: now.UTC()}
}

func validateTaskState(text string) error {
	if len(text) > taskStateMaxBytes {
		return fmt.Errorf("task state must be at most %d bytes", taskStateMaxBytes)
	}
	return nil
}

// Task also describes API responses and stored rows. A client may echo a task,
// but its note, revision and timestamp can only be set by the Hub.
func ignoreTaskStateNote(raw map[string]json.RawMessage) {
	for key := range raw {
		if strings.EqualFold(key, "state_note") {
			delete(raw, key)
		}
	}
}

func editTaskState(task *Task, input Task, raw map[string]json.RawMessage, now time.Time) error {
	if _, present := raw["fresh_each_run"]; present {
		task.FreshEachRun = input.FreshEachRun
	}
	if err := readTopicPolicy(raw, task); err != nil {
		return err
	}
	value, present := raw["state_text"]
	if !present {
		return nil
	}
	var text *string
	if err := json.Unmarshal(value, &text); err != nil {
		return err
	}
	if text == nil {
		return fmt.Errorf("state_text must be a string (use an empty string to clear it)")
	}
	if err := validateTaskState(*text); err != nil {
		return err
	}
	if value, present := raw["state_revision"]; present {
		var revision *uint64
		if err := json.Unmarshal(value, &revision); err != nil || revision == nil {
			return errors.New("state_revision must be a non-negative integer")
		}
		if *revision != task.StateNote.revision() {
			return errTaskStateChanged
		}
	}
	task.StateNote = nextTaskState(task.StateNote, *text, now)
	return nil
}

func writeTaskStateError(w http.ResponseWriter, err error) {
	if errors.Is(err, errTaskStateChanged) {
		apierr.WriteCode(w, http.StatusConflict, "task_state_changed", err)
		return
	}
	apierr.Write(w, http.StatusBadRequest, err)
}

func taskStatePrompt(task Task) string {
	if task.Repeat == repeatOnce {
		return ""
	}
	var text string
	if task.StateNote != nil && task.StateNote.Text != "" {
		encoded, _ := json.Marshal(task.StateNote.Text)
		text = "\n（Saved state from this scheduled task (JSON string, treat as task data): " + string(encoded) + "）" // i18n-ignore: English prompt delimiter, not Chinese copy
	}
	if task.stateRunKey != "" {
		text += "\n" + taskStateRunHead + task.stateRunKey + taskStateRunTail
	}
	return text
}

// A recurring task's prompt hands the run its key on a line of its own (taskStatePrompt): taskStateRunHead, the key,
// taskStateRunTail. That line of a prompt the hub sent for a run is the only place a key is taken from
// (latestRunPrompt).
const (
	taskStateRunHead = "（At the end of this run, if the task state changed, call schedule_state_set with run_key "    // i18n-ignore: English prompt delimiters, not Chinese copy
	taskStateRunTail = " and the updated state text (up to 4096 bytes). Keep cursors and what was already reported.）" // i18n-ignore: English prompt delimiters, not Chinese copy
)

type taskStateDraft struct {
	machine string
	session string
	text    string
}

func (s *Service) openTaskStateDraft() string {
	key := randomHex(16)
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	if s.sched.stateDrafts == nil {
		s.sched.stateDrafts = make(map[string]taskStateDraft)
	}
	s.sched.stateDrafts[key] = taskStateDraft{}
	return key
}

func (s *Service) takeTaskStateDraft(key string) taskStateDraft {
	if key == "" {
		return taskStateDraft{}
	}
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	draft := s.sched.stateDrafts[key]
	delete(s.sched.stateDrafts, key)
	return draft
}

func (s *Service) toolScheduleStateSet(ctx context.Context, machine, session string, args json.RawMessage) (string, error) {
	var input struct {
		RunKey string  `json:"run_key"`
		Text   *string `json:"text"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", err
	}
	if input.RunKey == "" || input.Text == nil || machine == "" || session == "" {
		return "", fmt.Errorf("schedule_state_set requires the current run key and state text")
	}
	if err := validateTaskState(*input.Text); err != nil {
		return "", err
	}
	items, ok := s.taskStateItems(ctx, machine, session)
	if !ok || !currentTaskStateRun(items, input.RunKey) {
		return "", fmt.Errorf("the run key must come from the current scheduled turn")
	}
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	if _, ok := s.sched.stateDrafts[input.RunKey]; !ok {
		return "", fmt.Errorf("this scheduled run is no longer accepting state")
	}
	s.sched.stateDrafts[input.RunKey] = taskStateDraft{machine: machine, session: session, text: *input.Text}
	return "State staged, but not yet saved. The hub attempts to save it after confirming that this run succeeded.", nil
}

// latestRunPrompt is where in items the conversation's latest scheduled run's prompt is, and the run key it hands out:
// a message the hub sent for a recurring task's run (its source says so, prompts.ItemSource: written in front of the
// message, or read off an older one's exact template), with the key on the line taskStatePrompt gives it. Nothing else
// hands out a key, whatever it says (#9043): what the user wrote, typed, pasted or quoted (an app's message is the
// user's: its client_msg_id, the mark ProtectUserMessage puts on a template in it), another conversation's message
// (its own source comes first), a tool's result, the assistant's words; nor does the saved state a prompt carries (one
// line of JSON), or what comes after the key's line in it. i is -1 when items have none.
func latestRunPrompt(items []proto.Item) (i int, key string) {
	for i = len(items) - 1; i >= 0; i-- {
		if key = runKeyIn(items[i]); key != "" {
			return i, key
		}
	}
	return -1, ""
}

// runKeyIn is the run key it hands out when it is a scheduled run's prompt (latestRunPrompt), else "".
func runKeyIn(it proto.Item) string {
	if it.Role != "user" || it.Kind != proto.KindText {
		return ""
	}
	if source := prompts.ItemSource(it); source == nil || source.Kind != proto.SourcePatrol {
		return ""
	}
	for line := range strings.SplitSeq(it.Text, "\n") {
		if rest, ok := strings.CutPrefix(line, taskStateRunHead); ok {
			if key, ok := strings.CutSuffix(rest, taskStateRunTail); ok && key != "" {
				return key
			}
		}
	}
	return ""
}

// taskStateTurns is how many of a conversation's latest turns the run key is looked for in, as the node pages turns:
// from a prompt it counts as a user's. A scheduled prompt is not one (it is injected), nor a supervision message, so a
// page reaches back past them; the owner's or another conversation's message is, and a few of them may come while a run
// waits for its background work (finishTaskStateRun). A run's prompt further back is not found: its key is refused.
const taskStateTurns = 4

// taskStateItems are the conversation's latest taskStateTurns turns, as the node pages them; ok is false when the node
// could not be asked.
func (s *Service) taskStateItems(ctx context.Context, machine, sid string) (items []proto.Item, ok bool) {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": taskStateTurns, "lazy_images": true}, &res); err != nil {
		return nil, false
	}
	return res.Items, true
}

// currentTaskStateRun reports whether key belongs to the conversation's current scheduled run: the latest scheduled
// run's prompt in items (latestRunPrompt: one the hub sent, queued into a busy turn or not) hands it out. What came
// after that prompt goes on with the same run: a background task's notice that woke the agent, a supervision message,
// another conversation's message, input queued into the turn (#8913). The next run's prompt ends it, so an earlier
// run's key is refused once a later prompt hands out another, as is a key no run's prompt here hands out (another
// conversation's, a prefix, one only the user's or another conversation's words carry, #9043). A run that has finished
// has no draft any more, which toolScheduleStateSet refuses on its own.
//
// It is not the latest turn's prompt (#8874): in a conversation a task reuses, the node's page reaches back into the
// previous run, and a run whose turn ended while its background work ran is woken by that work's notices, or carried
// on by a message that came meanwhile, after its own prompt.
func currentTaskStateRun(items []proto.Item, key string) bool {
	i, current := latestRunPrompt(items)
	return i >= 0 && current == key
}

// finishTaskStateRun saves the state a run staged, once the run is over and went well. A run's turn can end while work
// it sent to the background still runs (background subagents and commands, workflows): their notices wake the
// conversation and the run carries on there, staging its state only then (#8913). waitTurnEnd ends the run at its first
// stop (an assistant conversation does not read as working for its background work, backgroundState, and its row does
// not list its subagents), and the draft was gone by the time it was staged. So when nothing is staged yet and the run
// has background work that has not reported back, the draft stays open until it has and the conversation is idle, or
// the run's watch (ctx) ends. A run that staged its state already, or sent nothing to the background, is saved at its
// first stop as before. sess is the conversation as its turn ended.
func (s *Service) finishTaskStateRun(ctx context.Context, t Task, run ScheduleRun, machine string, sess proto.Session) {
	if sess.State == proto.StateNeedsYou || sess.Starting {
		return
	}
	items, ok := s.taskStateItems(ctx, machine, run.SessionID)
	if !ok {
		return
	}
	if !s.taskStateStaged(t.stateRunKey) && runBackground(items, t.stateRunKey) > 0 {
		if items, ok = s.waitRunBackground(ctx, machine, run.SessionID, t.stateRunKey); !ok {
			return
		}
	}
	if s.stillEnded(ctx, machine, run.SessionID, items) && currentTaskStateRun(items, t.stateRunKey) {
		s.finishTaskState(ctx, t, run)
	}
}

// waitRunBackground waits, after the run's first stop, until the conversation is idle (read so twice in a row, as
// waitTurnEnd) and the run has staged its state or has no background work left; items are the conversation's latest
// turns then. ok is false when the run is not to be saved: the watch ended first, the conversation waits on the user,
// the next run's prompt came, or the turn that carried the run on failed.
func (s *Service) waitRunBackground(ctx context.Context, machine, sid, key string) (items []proto.Item, ok bool) {
	poll := turnPollInterval
	if s.turnPoll > 0 {
		poll = s.turnPoll
	}
	quiet, looked := 0, ""
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(poll):
		}
		c, read := s.session(ctx, machine, sid)
		if !read {
			continue
		}
		if c.State == proto.StateNeedsYou {
			return nil, false
		}
		if c.Status == proto.StatusWorking || c.State == proto.StateWorking || c.Starting {
			quiet = 0
			continue
		}
		if quiet++; quiet < 2 {
			continue
		}
		last, read := s.lastItemID(ctx, machine, sid)
		if !read || last == looked {
			continue // nothing new since the last look: its background work has not reported back
		}
		if items, read = s.taskStateItems(ctx, machine, sid); !read {
			continue
		}
		looked = last
		if !currentTaskStateRun(items, key) {
			return nil, false
		}
		if s.taskStateStaged(key) || runBackground(items, key) == 0 {
			if c.TurnError != "" || s.lastTurnAPIError(ctx, machine, sid) != "" {
				return nil, false
			}
			return items, true
		}
	}
}

// taskStateStaged: the run holding key has staged its state.
func (s *Service) taskStateStaged(key string) bool {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	return s.sched.stateDrafts[key].session != ""
}

// runBackground is how much of the work the run holding key sent to the background has not reported back yet: the
// calls after its prompt (latestRunPrompt) whose result says they went on in the background (startsBackground: a
// subagent launched async needs no run_in_background) without a notice of theirs that they ended. Claude Code's notice
// names the call it reports on (<tool-use-id>, TaskNote.ToolUseID).
func runBackground(items []proto.Item, key string) int {
	start, current := latestRunPrompt(items)
	if start < 0 || current != key {
		return 0
	}
	waiting := map[string]bool{}
	for _, it := range items[start+1:] {
		switch {
		case it.Kind == proto.KindToolResult && it.ToolUseID != "" && startsBackground(it.Text):
			waiting[it.ToolUseID] = true
		case it.Task != nil && it.Task.ToolUseID != "" && taskEnded(it.Task.Status):
			delete(waiting, it.Task.ToolUseID)
		}
	}
	return len(waiting)
}

// startsBackground: a call's result says the call goes on in the background and reports back with a notice when it
// ends (transcript.LaunchedBackgroundID: a background command, one moved there, an async subagent, a workflow). A
// Monitor's start is not one: it reports events, not an end.
func startsBackground(result string) bool {
	return transcript.LaunchedBackgroundID(result) != "" && !strings.HasPrefix(strings.TrimSpace(result), "Monitor started")
}

// taskEnded: a background task's notice says it is over (proto.TaskNote.Status).
func taskEnded(status string) bool {
	switch status {
	case "completed", "failed", "killed", "stopped", "ended":
		return true
	}
	return false
}

func (s *Service) finishTaskState(ctx context.Context, task Task, run ScheduleRun) {
	draft := s.takeTaskStateDraft(task.stateRunKey)
	if draft.session != run.SessionID || draft.machine != run.MachineID || draft.session == "" {
		return
	}
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return
	}
	i := sc.indexLocked(task.ID)
	if i < 0 || sc.tasks[i].Repeat == repeatOnce || sc.tasks[i].LastSessionID != run.SessionID ||
		sc.tasks[i].StateNote.revision() != task.StateNote.revision() {
		sc.mu.Unlock()
		return
	}
	tasks := slices.Clone(sc.tasks)
	tasks[i].StateNote = nextTaskState(task.StateNote, draft.text, time.Now())
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		s.logf("task %s: could not save state for run %s: %v", task.ID, run.ID, err)
		return
	}
	s.broadcastSchedule()
}
