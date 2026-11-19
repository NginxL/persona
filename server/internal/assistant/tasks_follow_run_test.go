package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Every receipt a turn's end writes names the run whose turn it was (#10465), so a reader of the to-do's history can
// give it to that run without guessing from the computer's name; an interrupted receipt keeps the run through the
// reads that work out its resolution. With two Agents on two computers it is the run whose turn the receipt reports,
// and the computer's name is that run's too. A named turn's receipt keeps the feed id it had before, so a turn a
// manager before #10465 receipted gets no second receipt.
func TestReceiptNamesItsRun(t *testing.T) {
	prompt := proto.Item{Kind: proto.KindText, Role: "user", Text: "Fix the login crash"}
	reply := "Fixed it and opened https://github.com/o/r/pull/901"
	for _, backend := range []string{"memory", "database"} {
		for _, c := range []struct {
			name      string
			items     []proto.Item
			lastText  string
			turnError string
			state     string
			turnKey   string // the turn's end, when the transcript names it: the receipt's id is receiptID's
			other     bool   // a second Agent, on mac, last updated after the one whose end set this off
			before    bool   // the turn has the receipt an older manager wrote under the same id, naming no run
		}{
			{name: "done", items: turnPage("turn-1", reply, true), lastText: reply, state: "done", turnKey: "turn-1"},
			{name: "cut off by the model service", items: []proto.Item{prompt}, turnError: "API Error: 529 overloaded", state: "interrupted"},
			{name: "no output", items: []proto.Item{prompt, {Kind: proto.KindThinking, Role: "assistant", Text: "…"}}, state: "interrupted"},
			{name: "done by the later of two Agents", items: turnPage("turn-1", reply, true), lastText: reply, state: "done",
				turnKey: "turn-1", other: true},
			{name: "done, receipted before the upgrade", items: turnPage("turn-1", reply, true), lastText: reply, state: "done",
				turnKey: "turn-1", before: true},
		} {
			t.Run(backend+"/"+c.name, func(t *testing.T) {
				var store Store
				if backend == "database" {
					store = taskRecoveryDatabase(t)
				}
				s, f := newQuietServiceOn(t, store)
				f.judge = `{"verdict":"not_done","reason":"not verified yet"}`
				todo := startedTask(t, s, f)
				ctx := context.Background()
				f.setItems(c.items...)
				machine, session := "box", "s1"
				ended := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
					Status: proto.StatusDone, State: proto.StateUnread, TurnError: c.turnError, LastText: c.lastText, UpdatedAt: time.Now()}
				if c.other {
					if err := s.linkTodoAgent(ctx, todo.ID, "mac", "s2", todo.Title, "/code/app", "claude"); err != nil {
						t.Fatal(err)
					}
					later := ended
					later.MachineID, later.SessionID, later.UpdatedAt = "mac", "s2", ended.UpdatedAt.Add(time.Minute)
					ended.LastText = "Still reading the crash log"
					f.setSessions("mac", later)
					machine, session = "mac", "s2"
				}
				var old receiptBody
				if c.before {
					old = receiptBody{Title: todo.Title, State: "done", Summary: "Fixed before the upgrade", Machine: "box"}
					id := receiptID(todo.ID, taskTurn{Machine: "box", Session: "s1", Key: c.turnKey})
					if _, err := s.appendFeedOnce(ctx, id, "conv1", feedReceipt, todo.ID, old); err != nil {
						t.Fatal(err)
					}
				}
				f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
					Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
				s.taskWorking(ctx, "box", "s1")
				f.setSessions("box", ended)
				s.taskTurnEnded(ctx, "box", "s1")

				items, _, err := s.feed(ctx, "conv1", time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				var receipts []FeedItem
				for _, it := range items {
					if it.Kind == feedReceipt && it.Ref == todo.ID {
						receipts = append(receipts, it)
					}
				}
				if len(receipts) != 1 {
					t.Fatalf("%d receipts: %s", len(receipts), feedKinds(s, "conv1"))
				}
				receipt := receipts[0]
				var rb receiptBody
				if err := json.Unmarshal(receipt.Body, &rb); err != nil {
					t.Fatal(err)
				}
				if c.before {
					if rb.Summary != old.Summary || rb.MachineID != "" || rb.SessionID != "" {
						t.Fatalf("the receipt written before was replaced: %s", receipt.Body)
					}
				} else if rb.State != c.state || rb.MachineID != machine || rb.SessionID != session || rb.Machine != machine {
					t.Fatalf("receipt = %s, want the run %s/%s", receipt.Body, machine, session)
				}
				if c.turnKey != "" {
					if want := receiptID(todo.ID, taskTurn{Machine: machine, Session: session, Key: c.turnKey}); receipt.ID != want {
						t.Fatalf("receipt id = %q, want %q", receipt.ID, want)
					}
				}
			})
		}
	}
}

// A turn's receipt id hashes what it hashed before receipts named their run: a manager upgraded between a turn's
// receipt and a second look at that turn finds the receipt under the same id and writes no second one.
func TestReceiptIDKeepsItsInputs(t *testing.T) {
	if got := receiptID("todo1", taskTurn{Machine: "box", Session: "s1", Key: "turn-7"}); got != "feed_turn_6ea0311ff3f9e31dc14e" {
		t.Fatalf("receiptID = %q", got)
	}
}

// Every line of what a task's Agent was told names the run it reached (#10465): the owner's words, so the route's answer
// says where they were delivered; the hub's wording of an answer; and Persona's own lines (agent_send with task, a
// rework). A message that started a new Agent names that one, beside the new_agent fields older apps read.
func TestRoutedItemNamesItsRun(t *testing.T) {
	for _, backend := range []string{"memory", "database"} {
		t.Run(backend, func(t *testing.T) {
			var store Store
			if backend == "database" {
				store = taskRecoveryDatabase(t)
			}
			s, f := newQuietServiceOn(t, store)
			secretary(f)
			ctx := context.Background()
			if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001",
				"text": "Pull the weekly report's numbers", "task": map[string]any{"title": "Weekly report numbers"}}); err != nil {
				t.Fatal(err)
			}
			todo := onlyTodo(s)
			type routed struct {
				Text            string `json:"text"`
				Relay           string `json:"relay"`
				From            string `json:"from"`
				MachineID       string `json:"machine_id"`
				SessionID       string `json:"session_id"`
				NewAgent        string `json:"new_agent"`
				NewAgentMachine string `json:"new_agent_machine"`
			}
			bodyOf := func(it FeedItem) routed {
				t.Helper()
				var b routed
				if err := json.Unmarshal(it.Body, &b); err != nil {
					t.Fatalf("routed body %s: %v", it.Body, err)
				}
				return b
			}

			item, err := s.routeToTask(ctx, "conv1", todo.ID, "Add a week-over-week column too")
			if err != nil {
				t.Fatalf("the owner's message: %v", err)
			}
			if b := bodyOf(item); b.MachineID != "box" || b.SessionID != "sec00001" || b.Relay != "" || b.From != "" {
				t.Fatalf("the owner's message: %s", item.Body)
			}
			item, err = s.relayToTask(ctx, "conv1", todo.ID, "Go with option B", nil)
			if err != nil {
				t.Fatalf("a relayed answer: %v", err)
			}
			if b := bodyOf(item); b.MachineID != "box" || b.SessionID != "sec00001" || b.Relay != routedRelay {
				t.Fatalf("a relayed answer: %s", item.Body)
			}
			if _, err := s.sendToTask(ctx, onlyTodo(s), "The totals row is missing: add it"); err != nil {
				t.Fatalf("a rework: %v", err)
			}

			// Its agent stops (read-only, #4754): the owner's next message starts a new one, and the line names that one.
			f.setSessions("box", proto.Session{SessionID: "sec00001", MachineID: "box", Cwd: "/code/desk", Agent: "claude", Title: "Desk",
				Launcher: "work", LastText: "The sheet is at /code/desk/out.xlsx", Status: proto.StatusOffline, State: proto.StateOffline,
				UpdatedAt: time.Now(), Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, ReadOnlyReason: "stopped"})
			item, err = s.routeToTask(ctx, "conv1", todo.ID, "Then add the week-over-week totals")
			if err != nil {
				t.Fatalf("the message to a stopped task: %v", err)
			}
			if b := bodyOf(item); b.NewAgent == "" || b.NewAgent == "sec00001" || b.SessionID != b.NewAgent || b.MachineID != "box" ||
				b.NewAgentMachine != b.MachineID {
				t.Fatalf("the message that started a new Agent: %s", item.Body)
			}

			// The feed keeps what each line was written with: Persona's two name the agent they told as well.
			items, _, err := s.feed(ctx, "conv1", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			mine, persona := 0, 0
			for _, it := range items {
				if it.Kind != feedRouted {
					continue
				}
				b := bodyOf(it)
				switch {
				case b.MachineID == "" || b.SessionID == "":
					t.Fatalf("a routed line names no run: %s", it.Body)
				case b.From == routedFromAssistant:
					if b.MachineID != "box" || b.SessionID != "sec00001" {
						t.Fatalf("Persona's line: %s", it.Body)
					}
					persona++
				default:
					mine++
				}
			}
			if mine != 3 || persona != 2 {
				t.Fatalf("%d of the owner's lines and %d of Persona's, want 3 and 2: %s", mine, persona, feedKinds(s, "conv1"))
			}
		})
	}
}
