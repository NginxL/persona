package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The tasks a conversation handed out that nothing works on are listed for the main conversation to assign, once a
// day with the card ledger; the owner's own to-dos, and tasks something carries, are not.
func TestUnattendedHandedOutTasksGoToTheMainConversation(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	done := now.Add(-time.Hour)
	later := now.Add(24 * time.Hour)
	put := func(t Todo) {
		s.todos.mu.Lock()
		if s.todos.todos == nil {
			s.todos.todos = map[string]*Todo{}
		}
		s.todos.todos[t.ID] = &t
		s.todos.mu.Unlock()
	}
	put(Todo{ID: "lone", Title: "Fix the flaky test", Origin: "lead1", CreatedAt: now.Add(-2 * time.Hour)})
	put(Todo{ID: "own", Title: "Buy milk", CreatedAt: now})                                                        // the owner's own
	put(Todo{ID: "run", Title: "Ship it", Origin: "lead1", Agents: []TodoAgent{{MachineID: "m", SessionID: "s"}}}) // an Agent is on it
	put(Todo{ID: "parent", Title: "The release", Origin: "lead1"})                                                 // a piece carries it
	put(Todo{ID: "piece", Title: "Notes", Origin: "lead1", PartOf: "parent", Agents: []TodoAgent{{MachineID: "m", SessionID: "s2"}}})
	put(Todo{ID: "closed", Title: "Old", Origin: "lead1", DoneAt: &done})
	put(Todo{ID: "failed", Title: "Did not start", Origin: "lead1", StartError: "no machine"})
	put(Todo{ID: "later", Title: "Someday", Origin: "lead1", SomedayUntil: &later})
	put(Todo{ID: "quiet", Title: "Quiet", Origin: "main", Quiet: true})
	put(Todo{ID: "older", Title: "Rename the module", Origin: "conv2", CreatedAt: now.Add(-3 * time.Hour)})

	list := s.unattendedHandedOut()
	if len(list) != 2 || list[0].ID != "older" || list[1].ID != "lone" {
		t.Fatalf("unattended = %+v, want older then lone", list)
	}
	report := unattendedReport(list)
	for _, want := range []string{"Handed-out tasks nothing works on: 2.", `older "Rename the module" (from conv2)`, `lone "Fix the flaky test" (from lead1)`} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	if unattendedReport(nil) != "" {
		t.Error("no unattended tasks: a report")
	}

	// With the ledger: told to the main conversation the next day, even with nothing pushed, never pushed to the owner.
	pushes := len(f.pushed())
	loc := s.location(ctx)
	today := time.Now().In(loc)
	s.reportCardLedger(ctx, time.Date(today.Year(), today.Month(), today.Day(), 23, 30, 0, 0, loc))
	kept := laterFor(s, "conv1")
	if len(kept) != 1 || !strings.Contains(kept[0].Text, "Handed-out tasks nothing works on: 2.") || strings.Contains(kept[0].Text, "Card ledger for") {
		t.Fatalf("kept for the main conversation: %+v", kept)
	}
	if len(f.pushed()) != pushes {
		t.Error("the list was pushed to the owner")
	}
}

// Past unattendedListed tasks, the rest are counted, not named.
func TestUnattendedReportNamesTheFirstTen(t *testing.T) {
	var list []Todo
	for i := range unattendedListed + 3 {
		list = append(list, Todo{ID: fmt.Sprintf("t%d", i), Title: "task", Origin: "lead1"})
	}
	r := unattendedReport(list)
	if !strings.Contains(r, "\n- and 3 more") || strings.Contains(r, "t10 ") {
		t.Fatalf("report:\n%s", r)
	}
}

// Sources none of the six are named platform defects for the platform's supervisor; a day with none says nothing of it.
func TestCardLedgerReportHandsDefectsToThePlatform(t *testing.T) {
	bug := cardLedgerReport("2026-11-22", []cardCount{{Source: cardTaskFailed, N: 1}})
	if !strings.Contains(bug, "platform defect") || !strings.Contains(bug, "platform's supervisor") {
		t.Fatalf("report:\n%s", bug)
	}
	if ok := cardLedgerReport("2026-11-22", []cardCount{{Source: cardDecision, N: 1}}); strings.Contains(ok, "platform defect") {
		t.Fatalf("a day with no defect names one:\n%s", ok)
	}
}
