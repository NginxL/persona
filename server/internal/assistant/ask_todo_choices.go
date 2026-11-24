package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/proto"
)

// askTodoQuestions preserves the form on new records and recovers older ones from their own expired card.
// Missing history leaves a free-text question, never options guessed from localized notes.
func (s *Service) askTodoQuestions(ctx context.Context, a askTodo) []proto.Question {
	if len(a.QuestionDetails) > 0 {
		return a.QuestionDetails
	}
	if st, ok := s.store().(interactionRecorder); ok {
		rec, err := st.InteractionRecord(ctx, a.Interaction)
		if err == nil && rec != nil && rec.Status == "expired" && rec.Interaction.MachineID == a.MachineID &&
			rec.Interaction.SessionID == a.SessionID && len(rec.Interaction.Questions) == len(a.Questions) {
			matches := true
			for i, q := range rec.Interaction.Questions {
				matches = matches && strings.TrimSpace(q.Question) == a.Questions[i]
			}
			if matches {
				return rec.Interaction.Questions
			}
		}
	}
	var questions []proto.Question
	for _, q := range a.Questions {
		questions = append(questions, proto.Question{Question: q})
	}
	return questions
}

func (s *Service) addExpiredQuestionAsks(ctx context.Context, out map[string]todoAsk) {
	b := &s.askTodos
	b.mu.Lock()
	if !s.loadAskTodosLocked(ctx) {
		b.mu.Unlock()
		return
	}
	records := slices.Clone(b.list)
	b.mu.Unlock()
	for _, a := range records {
		t, exists := s.todoNow(a.TodoID)
		// A default reserves the answer even after its sending claim expires; DueAt alone may only mean a reminder.
		if !exists || t.DoneAt != nil || a.ByDefault || !a.AnsweredAt.IsZero() || !a.DroppedAt.IsZero() || a.held(time.Now()) || len(a.Questions) == 0 {
			continue
		}
		if _, has := out[a.TodoID]; has {
			continue
		}
		questions := s.askTodoQuestions(ctx, a)
		ask := todoAsk{Outcome: outcomeDecide, Verdict: strings.Join(a.Questions, " / "),
			Question: a.Questions[0], AskedAt: a.AskedAt, Questions: questions}
		// Legacy rows mark their first option as recommended. Only use that contract for a real recommendation;
		// the questions above always retain the original order and choices without inventing one.
		if len(questions) == 1 && !questions[0].MultiSelect {
			q := questions[0]
			if i := slices.IndexFunc(q.Options, func(o proto.QuestionOption) bool { return o.Label == q.Recommend }); i >= 0 {
				ask.Recommend = q.Recommend
				ask.Options = append(ask.Options, DecisionOption{Label: q.Options[i].Label, LeadsTo: q.Options[i].Description})
				for j, o := range q.Options {
					if j != i {
						ask.Options = append(ask.Options, DecisionOption{Label: o.Label, LeadsTo: o.Description})
					}
				}
			}
		}
		out[a.TodoID] = ask
	}
}

// chooseExpiredQuestion uses the same manual-decision ledger and return-to-asker path, not task acceptance.
func (s *Service) chooseExpiredQuestion(ctx context.Context, id, asked, option string, answers map[string]string) (acceptResult, error) {
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return acceptResult{}, errAcceptStore
	}
	s.askTodos.mu.Lock()
	loaded := s.loadAskTodosLocked(ctx)
	s.askTodos.mu.Unlock()
	if !loaded {
		return acceptResult{}, errAcceptStore
	}
	a, exists := s.askTodoOf(ctx, id)
	t, open := s.todoNow(id)
	at, err := time.Parse(time.RFC3339Nano, asked)
	if !exists || !open || t.DoneAt != nil || err != nil || !at.Equal(a.AskedAt) ||
		a.ByDefault || !a.AnsweredAt.IsZero() || !a.DroppedAt.IsZero() || a.held(time.Now()) {
		return acceptResult{}, nil
	}
	questions := s.askTodoQuestions(ctx, a)
	if len(questions) == 0 || option != "" && answers != nil {
		return acceptResult{}, errDecisionOption
	}
	if answers == nil {
		if len(questions) != 1 || questions[0].MultiSelect {
			return acceptResult{}, errDecisionOption
		}
		if option == "" {
			option = questions[0].Recommend
		}
		if option == "" || !slices.ContainsFunc(questions[0].Options, func(o proto.QuestionOption) bool { return o.Label == option }) {
			return acceptResult{}, errDecisionOption
		}
		answers = map[string]string{questions[0].Question: option}
	}
	if len(answers) != len(questions) {
		return acceptResult{}, errDecisionOption
	}
	var choices []askChoice
	for _, q := range questions {
		answer := strings.TrimSpace(answers[q.Question])
		if answer == "" || utf8.RuneCountInString(answer) > decisionTextMax {
			return acceptResult{}, errDecisionOption
		}
		choices = append(choices, askChoice{Question: q.Question, Choice: answer})
	}
	st := s.todoDB()
	if st == nil {
		return acceptResult{}, errAcceptStore
	}
	// A close may have claimed the drop while the inline form was being validated.
	a, claimed, err := s.claimAskTodoChecked(ctx, id, true, true)
	if err != nil {
		return acceptResult{}, errors.Join(errAcceptStore, err)
	}
	if !claimed {
		return acceptResult{}, nil
	}
	accepted := false
	defer func() {
		if !accepted {
			// A lost transaction reply is not proof of rollback. Reload before releasing the claim, so an
			// answer already committed in the database cannot be overwritten with an unanswered record.
			recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			s.askTodos.mu.Lock()
			s.askTodos.loaded = false
			loaded := s.loadAskTodosLocked(recoverCtx)
			s.askTodos.mu.Unlock()
			if loaded {
				s.settleAskClaim(recoverCtx, id, false)
			}
		}
	}()
	if err := s.commitExpiredQuestionAnswer(ctx, st, a, t.Title, choices); err != nil {
		return acceptResult{}, err
	}
	accepted = true
	return acceptSucceeded(), nil
}

// Inline acceptance is a durable outbox commit, not a send followed by best-effort bookkeeping. The complete answer,
// AnsweredAt, closed to-do and original relay (including its stable once_id inputs) commit before delivery may start.
func (s *Service) commitExpiredQuestionAnswer(ctx context.Context, st todoStore, a askTodo, title string, choices []askChoice) error {
	machine, to, err := s.askTarget(ctx, a)
	if err != nil {
		return err
	}
	now := decisionNow()
	var decisions []db.TodoDecisionRow
	for i, c := range choices {
		// The database dedupes on nonempty source_message, not the primary key. Include the choice so an identical
		// retry is idempotent, including retries of records written by older versions.
		key := sha256.Sum256([]byte(a.TodoID + "\x00" + c.Question + "\x00" + c.Choice))
		source := hex.EncodeToString(key[:16])
		decisions = append(decisions, db.TodoDecisionRow{ID: "dec_ask_" + source, TodoID: a.TodoID,
			SourceMessage: "inline-answer:" + source, Question: decisionText(c.Question), Choice: c.Choice,
			Source: db.DecisionManual, CreatedAt: now.Add(time.Duration(i) * time.Microsecond)})
	}
	msg := relayMsg{From: a.TodoID, FromTitle: "待办「" + title + "」", Tool: askAnswerTool, // i18n-ignore: existing relay label
		Text: renderAskAnswer(a, s.location(ctx), choices, false, false), At: now, QueuedAt: now}
	var inserted []db.TodoDecisionRow
	var doneKeys [][2]string
	err = func() error {
		// No published state or delivery may observe a part of the transaction. Match the to-do writer's
		// lock order, then guard both settings against concurrent saves until memory follows the commit.
		s.todos.writing.Lock()
		defer s.todos.writing.Unlock()
		s.askTodos.mu.Lock()
		defer s.askTodos.mu.Unlock()
		s.relays.mu.Lock()
		defer s.relays.mu.Unlock()
		if !s.relayLocked(ctx) {
			return errAcceptStore
		}
		records := slices.Clone(s.askTodos.list)
		i := slices.IndexFunc(records, func(r askTodo) bool { return r.TodoID == a.TodoID })
		if i < 0 {
			return errAcceptStore
		}
		records[i].AnsweredAt, records[i].ClaimedAt = now, time.Time{}
		relay := s.relays.st
		relay.Queue = maps.Clone(relay.Queue)
		msg.Depth = len(relay.Queue[to]) + 1
		relay.Queue[to] = append(slices.Clone(relay.Queue[to]), msg)
		row, added, err := st.CommitTodoAnswer(ctx, a.TodoID, decisions,
			map[string]any{askTodosKey: records, relayKey: relay}, now)
		if err != nil {
			// A commit whose reply was lost may already be durable. Force subsequent readers to reload;
			// the deferred claim release also rereads before writing anything.
			s.relays.loaded = false
			s.todos.mu.Lock()
			s.todos.loaded = false
			s.todos.mu.Unlock()
			return errors.Join(errAcceptStore, err)
		}
		s.askTodos.list, s.relays.st = records, relay
		inserted = added
		if row != nil {
			_, doneKeys, _ = s.setTodoRow(a.TodoID, *row, nil)
		}
		return nil
	}()
	if err != nil {
		return err
	}
	// Side effects happen only after the whole answer commits. Do not call askTodoDecided per question: this
	// submission is already terminal and owns exactly one complete relay, including when its to-do was deleted.
	for _, row := range inserted {
		s.todoDecisionEvent(row)
		s.parkDecided(context.WithoutCancel(ctx), row)
		if postable(row) {
			s.chat().ThreadPost(row.TodoID, larkhost.ThreadEvent{Kind: larkhost.ThreadDecision, Text: decisionLine(row)})
		}
	}
	s.todoRowSet(ctx, doneKeys)
	s.recordLateAnswer(a, choices, "", "")
	go s.releaseHeldAsks(to)
	if c, ok := s.session(ctx, machine, to); ok {
		s.drainRelays(machine, []proto.Session{c})
	}
	return nil
}
