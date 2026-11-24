package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The real Lark callback must give the same known-option receipt as the saved and refreshed conclusion card.
func TestDecisionButtonReceiptMatchesCard(t *testing.T) {
	needsLarkLayer(t)
	for _, tc := range []struct {
		name, kind string
		check      bool
	}{
		{"check other option", "other", true},
		{"check recommended option", "recommended", true},
		{"ordinary other option", "other", false},
		{"stale card", "stale", true},
		{"wrong task", "wrong task", true},
		{"invalid option", "invalid", true},
		{"empty recommendation", "empty", true},
		{"Agent send failure", "failure", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, _, _ := conclusionServiceOn(t, todoTestDB(t), false)
			ctx := context.Background()
			s.superviseTimes.gather = time.Hour
			if tc.check {
				s.todoAskAfter = time.Nanosecond
				task := todoProgressTask(todo.ID)
				setTasks(s, task)
				assessTodoBaseline(t, s, task.ID)
				assessTodoRun(t, s, task.ID, nil)
				assessTodoRun(t, s, task.ID, nil)
			} else {
				waitsForReview(t, s, f, todo.ID, "The owner needs to choose.")
				reviewTask(t, s, todo.ID, ryokanDecision())
			}
			a := askOf(s, todo.ID)
			agentAtWork(f, todo)
			press := botcard.CardAction{}
			press.Operator.OpenID = "ou_me"
			press.Context.MessageID = a.MessageID
			label := a.Options[1].Label
			press.Action.Value = map[string]any{"a": "accept", "id": todo.ID, "opt": label}
			wantType, key := "success", "assistant-reports.decision_chosen_noted"
			if tc.check {
				key = "assistant-reports.decision_chosen_supervisor"
			}
			switch tc.kind {
			case "recommended":
				label = a.Recommend
				press.Action.Value["opt"] = label
				key = "assistant-reports.verdict_taken_supervisor"
			case "stale":
				press.Context.MessageID = "om_old_card"
				wantType = "info"
			case "wrong task":
				press.Action.Value["id"] = "todo_other"
				wantType = "info"
			case "invalid":
				press.Action.Value["opt"] = "Unknown option"
				wantType = "error"
			case "empty":
				s.accept.mu.Lock()
				s.accept.asks[todo.ID].Recommend = ""
				s.saveAcceptLocked(ctx)
				s.accept.mu.Unlock()
				press.Action.Value["opt"] = ""
				wantType = "info"
			case "failure":
				f.mu.Lock()
				f.failSend = errors.New("temporary network break")
				f.mu.Unlock()
				wantType = "error"
			}
			var mu sync.Mutex
			var updated []string
			f.answerApp(AppFeishu, FeishuUpdateCard, func(raw json.RawMessage) (any, error) {
				var in struct {
					Card string `json:"card"`
				}
				_ = json.Unmarshal(raw, &in)
				mu.Lock()
				updated = append(updated, in.Card)
				mu.Unlock()
				return map[string]any{}, nil
			})
			raw, err := json.Marshal(press)
			if err != nil {
				t.Fatal(err)
			}
			invoke := func() *botcard.CardResponse {
				t.Helper()
				resp, err := s.FeishuAction(ctx, FeishuAction{Event: raw, Action: "oa_option"})
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}
			before := len(sentToAgent(f))
			resp := invoke()
			if resp == nil || resp.Toast == nil || resp.Toast.Type != wantType {
				t.Fatalf("button response: %+v", resp)
			}
			after := askOf(s, todo.ID)
			if wantType != "success" {
				if !after.AcceptedAt.IsZero() || after.OptionTaken || len(sentToAgent(f)) != before {
					t.Fatalf("rejected callback took the choice: %+v", after)
				}
				if wantType == "info" && resp.Toast.Content != i18n.T(s.botLang(ctx), "assistant-feishu_cards.already_handled") {
					t.Fatalf("unexpected info receipt: %+v", resp.Toast)
				}
				s.waitWorkEvents()
				return
			}
			want := i18n.T(s.botLang(ctx), key, i18n.Args{"option": label})
			if resp.Toast.Content != want {
				t.Errorf("button receipt: %q, want %q", resp.Toast.Content, want)
			}
			if note := acceptedNote(s.botLang(ctx), after); note != want {
				t.Errorf("saved card note: %q, want %q", note, want)
			}
			if after.AcceptedAt.IsZero() || tc.kind == "other" && !after.OptionTaken {
				t.Errorf("explicit option fact missing: %+v", after)
			}
			mu.Lock()
			cards := append([]string(nil), updated...)
			mu.Unlock()
			if len(cards) == 0 || !strings.Contains(cards[len(cards)-1], want) {
				t.Errorf("refreshed card lacks %q: %v", want, cards)
			}
			s.accept.mu.Lock()
			s.accept.asks = nil
			s.accept.loaded = false
			s.accept.mu.Unlock()
			restored, ok := s.askCopy(todo.ID)
			if !ok || acceptedNote(s.botLang(ctx), &restored) != want {
				t.Errorf("restored receipt differs: %+v", restored)
			}
			sends := len(sentToAgent(f))
			again := invoke()
			if again == nil || again.Toast == nil || again.Toast.Type != "info" || again.Toast.Content != i18n.T(s.botLang(ctx), "assistant-feishu_cards.already_handled") || len(sentToAgent(f)) != sends {
				t.Fatalf("ended callback was taken again: %+v", again)
			}
			s.waitWorkEvents()
		})
	}
}
