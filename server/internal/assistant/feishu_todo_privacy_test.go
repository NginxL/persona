package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Exercise the scheduled reminder entry and inspect the whole outgoing payload, including card metadata.
func TestFeishuTodoReminderCardPrivacyAtReminderEntry(t *testing.T) {
	for _, health := range []bool{false, true} {
		name := "ordinary"
		if health {
			name = "health"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, s, posts := todoReminderHub(t)
				now := time.Now().UTC()
				at := now.Add(-time.Minute)
				todo := Todo{ID: "todo_synthetic_" + name, Title: "Synthetic reminder title", Notes: "Synthetic reminder notes", RemindAt: &at}
				if health {
					todo.Origin = "health1"
				}
				s.todos.mu.Lock()
				s.todos.todos = map[string]*Todo{todo.ID: &todo}
				s.todos.loaded = true
				s.todos.mu.Unlock()
				s.remind(context.Background(), todo.ID, at, now)
				synctest.Wait()
				if len(*posts) != 1 {
					t.Fatalf("posts = %d, want one generic or ordinary reminder", len(*posts))
				}
				raw, err := json.Marshal(*posts)
				if err != nil {
					t.Fatal(err)
				}
				payload := string(raw)
				card := (*posts)[0]["card"].(string)
				var decoded map[string]any
				if err := json.Unmarshal([]byte(card), &decoded); err != nil {
					t.Fatal(err)
				}
				target := s.botTargetFor("", "om_1")
				if target.SessionID != "m1" || target.MachineID != "server" {
					t.Fatalf("reply target = %+v", target)
				}
				if len(f.pushed()) != 1 {
					t.Fatalf("phone pushes = %d", len(f.pushed()))
				}
				if health {
					title, body := spaceNotice(s.botLang(context.Background()))
					for _, want := range []string{title, body} {
						if !strings.Contains(card, want) {
							t.Errorf("generic health notice lacks %q", want)
						}
					}
					for _, forbidden := range []string{todo.Title, todo.Notes, todo.ID, "/todos/"} {
						if strings.Contains(payload, forbidden) {
							t.Errorf("health reminder payload contains %q: %s", forbidden, card)
						}
					}
					if target.TodoID != "" {
						t.Errorf("health reply carries to-do ID %q", target.TodoID)
					}
				} else {
					for _, want := range []string{todo.Title, todo.Notes, "https://hub.example.com/todos/" + todo.ID} {
						if !strings.Contains(card, want) {
							t.Errorf("ordinary reminder lacks %q: %s", want, card)
						}
					}
					if target.TodoID != todo.ID {
						t.Errorf("ordinary reply lost to-do context: %+v", target)
					}
				}
			})
		})
	}
}
