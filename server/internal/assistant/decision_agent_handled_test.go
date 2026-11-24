package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPriorAgentReplyDoesNotPublishDecision(t *testing.T) {
	for _, tc := range []struct {
		name, reply, choice string
		relay, before       bool
	}{
		{name: "free text", reply: "A would be fine later, but stop here for now."},
		{name: "exact label", reply: "直接全量", choice: "直接全量"},
		{name: "letter without published option order", reply: "A"},
		{name: "automated relay", reply: "直接全量", relay: true},
		{name: "earlier than the question", reply: "直接全量", before: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := todoTestDB(t)
			s, f, todo, posts, _ := conclusionServiceOn(t, database, true)
			waitsForReview(t, s, f, todo.ID, "A rollout decision is needed.")
			args := rolloutDecision()
			args["question_quote"] = "Should we begin with a limited rollout?"
			first := time.Now().UTC()
			question := proto.Item{ID: "question", Role: "assistant", Kind: proto.KindText, TS: first, Text: args["question_quote"].(string)}
			reply := proto.Item{ID: "owner-reply", Role: "user", Kind: proto.KindText, TS: first.Add(time.Nanosecond), Text: tc.reply}
			if tc.relay {
				reply.Source = &proto.MessageSource{Kind: proto.SourceConversation}
			}
			if tc.before {
				reply.TS = first.Add(-time.Nanosecond)
				f.setItems(reply, question)
			} else {
				f.setItems(question, reply)
			}
			sent, pushed := len(sentToAgent(f)), len(f.pushed())
			out := reviewTask(t, s, todo.ID, args)
			a := askOf(s, todo.ID)
			cur := todoNow(s, todo.ID)
			_, listed := s.todoAsks(t.Context())[todo.ID]
			if tc.relay || tc.before {
				if !listed || a == nil || !a.AcceptedAt.IsZero() || cur.Supervision.State != reviewNeedsUser {
					t.Fatalf("unrelated input suppressed a valid ask: ask %+v state %s result %s", a, cur.Supervision.State, out)
				}
				return
			}
			if listed || a == nil || a.AcceptedAt.IsZero() || !a.NoCard || a.MessageID != "" ||
				a.AgentReply == nil || a.AgentReply.Text != tc.reply || cur.DoneAt != nil || cur.Supervision.State != reviewPending {
				t.Fatalf("owner already replied but the ask was visible or the task was closed: ask %+v state %s result %s", a, cur.Supervision.State, out)
			}
			if len(cardsWith(posts(), args["reason"].(string))) != 0 || len(f.pushed()) != pushed {
				t.Fatal("the owner was sent a decision card or push")
			}
			rows, err := database.ListTodoDecisions(t.Context(), todo.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.choice {
			case "":
				if len(rows) != 0 {
					t.Fatalf("guessed an option from prose: %+v", rows)
				}
			default:
				if len(rows) != 1 || rows[0].Choice != tc.choice || rows[0].Reason != tc.reply || rows[0].SourceSession != "s1" {
					t.Fatalf("missing the exact choice with provenance: %+v", rows)
				}
			}
			s.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
			if len(sentToAgent(f)) != sent {
				t.Fatalf("sent a choice to the Agent: %q", sentToAgent(f)[sent:])
			}
		})
	}
}

func TestAgentReplyDoneWithdrawsDecisionAndLeavesReview(t *testing.T) {
	needsLarkLayer(t)
	for _, tc := range []struct {
		name, status string
		relay, quote bool
	}{
		{name: "free text with done", status: "done"},
		{name: "free text with blocked", status: "blocked"},
		{name: "relayed prompt with done", status: "done", relay: true},
		{name: "quoted done example", status: "done", quote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			presenceTimes(t, 0, 0)
			ownerTurnTimes(t, 0, 0)
			database := todoTestDB(t)
			s, f, todo, _, _ := conclusionServiceOn(t, database, true)
			waitsForReview(t, s, f, todo.ID, "A rollout decision is needed.")
			reviewTask(t, s, todo.ID, rolloutDecision())
			a := askOf(s, todo.ID)
			if _, listed := s.todoAsks(t.Context())[todo.ID]; !listed || a.MessageID == "" {
				t.Fatalf("the test needs a live owner card: %+v", a)
			}
			updated := make(chan string, 4)
			f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
				select {
				case updated <- string(p):
				default:
				}
				return map[string]any{}, nil
			})
			agentTurn(s, f, proto.StatusWorking, "", 0)
			words := "A could work someday, but leave the change here."
			if !tc.relay {
				agentChoiceMessage(t, s, SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}, words)
			}
			sent := len(sentToAgent(f))
			at := time.Now().UTC()
			answer := "<closeout status=\"" + tc.status + "\">\nverified: Saved the draft.\n</closeout>"
			if tc.quote {
				answer = "Here is an example:\n```\n" + answer + "\n```"
			}
			reply := proto.Item{ID: "owner-new", Role: "user", Kind: proto.KindText, Text: words, TS: at}
			if tc.relay {
				reply.Source = &proto.MessageSource{Kind: proto.SourceConversation}
			}
			f.mu.Lock()
			f.judge = `{"verdict":"needs_user","reason":"The Agent is still waiting for a decision."}`
			f.mu.Unlock()
			f.setItems(reply, proto.Item{ID: "agent-answer", Role: "assistant", Kind: proto.KindText, Text: answer, TS: at.Add(time.Nanosecond)},
				proto.Item{ID: "turn-end", Kind: "turn_duration", TS: at.Add(2 * time.Nanosecond)})
			agentTurn(s, f, proto.StatusDone, answer, 0)
			got := askOf(s, todo.ID)
			_, listed := s.todoAsks(t.Context())[todo.ID]
			if tc.status != "done" || tc.relay || tc.quote {
				if !listed || !got.AcceptedAt.IsZero() {
					t.Fatalf("a non-done or automated turn withdrew the ask: %+v", got)
				}
				return
			}
			cur := todoNow(s, todo.ID)
			if listed || got.AcceptedAt.IsZero() || got.AgentReply == nil || !got.AgentReply.Handled || got.AgentReply.Text != words ||
				got.AgentReply.Closeout != answer || got.ByDefault || got.Chosen != "" || cur.DoneAt != nil || cur.Supervision.State != reviewPending {
				t.Fatalf("done owner reply did not leave an open task for review without an owner ask: %+v state %s", got, cur.Supervision.State)
			}
			waitFor(t, "the marked card", func() bool {
				select {
				case p := <-updated:
					return strings.Contains(p, "已在原会话处理")
				default:
					return false
				}
			})
			waitFor(t, "the supervisor's original evidence", func() bool {
				text := strings.Join(f.notices(), "\n")
				return strings.Contains(text, words) && strings.Contains(text, "Saved the draft.") && strings.Contains(text, "Verify the task")
			})
			s.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
			rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
			if err != nil || len(rows) != 0 || len(sentToAgent(f)) != sent {
				t.Fatalf("withdrawing the ask chose or sent an option: decisions %+v, err %v, sends %q", rows, err, sentToAgent(f)[sent:])
			}
		})
	}
}
