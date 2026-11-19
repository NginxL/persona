package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A planned task whose Agent did not start because its new tab's shell stopped on a startup question (shell_waiting)
// keeps that tab on its computer for a while, and its plan task lists the question and the answers it may be given
// (error_args: line, choices, kind…, as a session's start_error_args, node/shellsettle.go). The owner answers it from
// the app (#10365, POST /api/v1/todos/{id}/start/answer, hello feature todo_start_answer): the node types the answer
// into that tab by its pane with start.continue's guards (start.answer: only an offered answer, only while the same
// question is still the tab's last line, Ctrl-C alone into a password prompt), and the task starts again in that tab
// as its Retry does there (retryTaskWith, in_pane). Refused, nothing is typed and nothing changes.

// startAnswerTimeout bounds the node's answer: it reads the tab, types the answer and watches its echo for a moment.
const startAnswerTimeout = 20 * time.Second

// answerChoice is what a choice can look like (the plan task's choices and then the node check it).
var answerChoice = lazyre.New(`^[a-z0-9]{1,8}$`)

// The refusals of a task's answer before the node is asked (409, the node's own codes and words: rpc-errors).
var (
	// errAnswerNotWaiting: the task's last start did not stop on a startup question (it started, failed otherwise, is
	// starting again, or is remedied by the assistant).
	errAnswerNotWaiting = &rpc.Error{Code: "start_not_waiting", Message: "this task's start is not waiting on a question; nothing was typed"}
	// errAnswerTabGone: its computer no longer keeps the tab (kept_until passed): a Retry opens a new one.
	errAnswerTabGone = &rpc.Error{Code: "start_tab_gone", Message: "the task's tab is not kept any more; nothing was typed: retry it"}
	// errAnswerNotOffered: the answer is not one the question was offered.
	errAnswerNotOffered = &rpc.Error{Code: "choice_not_offered", Message: "that answer is not offered for this question; nothing was typed"}
)

// answerRefused: a task's answer refused by the hub with code, in the request's language (403, 409).
type answerRefused struct {
	status int
	code   string
	error
}

func (e answerRefused) Unwrap() error { return e.error }

// answerTaskStart answers the startup question task todoID's last start stopped on with choice, and starts the task
// again in that tab (retryTaskWith). It returns the to-do as the apps get it, reopened.
func (s *Service) answerTaskStart(ctx context.Context, todoID, choice string) (json.RawMessage, error) {
	return s.retryTaskWith(ctx, todoID, retryWith{by: "user", answer: choice})
}

// answerKeptTab types choice, the owner's answer to the question task t's last start stopped on, into its kept tab on
// pane (keptTabOf: "" when its computer no longer keeps it) on computer m, through the node's start.answer. Only a
// shell_waiting refusal that listed the question and offered choice is answered; a node from before start.answer is
// refused node_outdated before anything is asked. The node's own refusals (start_tab_gone, choice_not_offered) come
// back as it gave them; a question no longer on the tab's last line (answered at the computer) gets nothing typed
// (typed false), and the task starts in the tab all the same. A node's reply that does not say typed is taken as
// typed, as before the node said.
func (s *Service) answerKeptTab(ctx context.Context, m Device, t PlanTask, pane, choice string) (typed bool, _ error) {
	line := t.ErrorArgs["line"]
	if t.ErrorCode != proto.CodeShellWaiting || line == "" {
		return false, errAnswerNotWaiting
	}
	if pane == "" {
		return false, errAnswerTabGone
	}
	if !slices.Contains(strings.Split(t.ErrorArgs[proto.ShellAskChoices], ","), choice) {
		return false, errAnswerNotOffered
	}
	outdated := answerRefused{http.StatusConflict, proto.CodeNodeOutdated, i18n.Err("assistant-tasks.start_answer_node_outdated", i18n.Args{"machine": m.Name})}
	if nodes, ok := s.devs.(nodeAnnouncer); ok && s.devs.Has(m.ID) && !nodes.NodeAnnounced(m.ID, nodeproto.StartAnswer) {
		return false, outdated
	}
	cctx, cancel := context.WithTimeout(ctx, startAnswerTimeout)
	defer cancel()
	var out proto.StartAnswerResult
	err := s.call(cctx, m.ID, proto.MethodStartAnswer, proto.StartAnswerParams{Pane: pane, Line: line, Choice: choice}, &out)
	if unknownMethod(err) { // a node from before start.answer, through a hub that relays it
		return false, outdated
	}
	if err != nil {
		return false, err
	}
	return out.Typed == nil || *out.Typed, nil
}

// postTaskStartAnswer: POST /api/v1/todos/{id}/start/answer {"choice":"n"} → {"todo": Todo}.
func (s *Service) postTaskStartAnswer(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) { // typing into a computer's shell is for a device with full credentials
		apierr.WriteCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("assistant-tasks.start_answer_read_only"))
		return
	}
	var in struct {
		Choice string `json:"choice"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || !answerChoice.MatchString(in.Choice) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-tasks.start_answer_need_choice"))
		return
	}
	t, err := s.answerTaskStart(r.Context(), r.PathValue("id"), in.Choice)
	var refused answerRefused
	var re *rpc.Error
	switch {
	case err == nil:
		apierr.WriteJSON(w, map[string]any{"todo": t})
	case errors.Is(err, db.ErrTodoNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.As(err, &refused):
		apierr.WriteCode(w, refused.status, refused.code, refused.error)
	case errors.Is(err, errTaskNotRetryable), errors.Is(err, errTaskNotInPlan), errors.Is(err, errMachineOffline):
		apierr.Write(w, http.StatusConflict, err)
	case errors.As(err, &re) && re.Code != "":
		apierr.Call(w, err) // the node's refusal, nothing typed: 409 with its code, in the request's language
	default:
		planErr(w, err)
	}
}
