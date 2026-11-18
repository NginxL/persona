package node

import (
	"errors"
	"io"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/transcript"
)

func assistantNode(t *testing.T) *Node {
	t.Helper()
	return &Node{home: t.TempDir(), paths: map[string]string{}, files: map[string]*transcript.File{}, procs: map[string]*assistantProc{},
		agents: map[string]host.Agent{}, state: &stateFile{Sessions: map[string]sessionState{}}, logger: log.New(io.Discard, "", 0),
		metas: map[string]quickMeta{}, bgScans: map[string]*bgScan{}} // a model recovery reads the transcript's meta (#10277)
}

// claude -p reports its running background tasks and monitors (background_tasks_changed); the process waiting on
// them must outlive the idle timeout, and the list goes into the session state for after a restart.
func TestAssistantWithBackgroundTasksIsNotReaped(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	n.state.put(id, sessionState{Launcher: "claude", Cwd: "/home/u/.persona/assistant"})
	p := &assistantProc{id: id}
	stream := func(lines ...string) { n.readAssistant(p, strings.NewReader(strings.Join(lines, "\n")+"\n")) }

	stream(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b1","task_type":"local_bash","description":"Sleep 23 minutes"}]}`,
		`{"type":"result","subtype":"success"}`)
	if st, _ := n.state.get(id); len(st.Tasks) != 1 || st.Tasks[0] != "Sleep 23 minutes" {
		t.Fatalf("remembered tasks %v", st.Tasks)
	}
	later := time.Now().Add(assistantIdleTimeout + time.Minute)
	p.mu.Lock()
	reap := p.reapable(later)
	forgotten := p.reapable(time.Now().Add(assistantTaskMaxIdle + time.Minute))
	p.mu.Unlock()
	if reap {
		t.Fatal("an idle process with a background task running was reaped")
	}
	if !forgotten {
		t.Fatal("a process idle past assistantTaskMaxIdle should go even with tasks")
	}

	stream(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`)
	p.mu.Lock()
	reap = p.reapable(time.Now().Add(assistantIdleTimeout + time.Minute))
	p.mu.Unlock()
	if !reap {
		t.Fatal("with its tasks done the process is reaped after the idle timeout again")
	}
	if st, _ := n.state.get(id); len(st.Tasks) != 0 {
		t.Fatalf("finished tasks still remembered: %v", st.Tasks)
	}
}

// After a restart, a conversation whose tasks died gets told once; herdr sessions and running ones are left alone.
func TestRecoverAssistantTasksAfterRestart(t *testing.T) {
	n := assistantNode(t)
	const lost, alive, herdrOne = "11111111-2222-3333-4444-000000000001", "11111111-2222-3333-4444-000000000002", "11111111-2222-3333-4444-000000000003"
	n.state.put(lost, sessionState{Cwd: "/home/u/.persona/assistant", Tasks: []string{"tail the deploy log", "Sleep 23 minutes"}})
	n.state.put(alive, sessionState{Cwd: "/home/u/.persona/assistant", Tasks: []string{"still mine"}})
	n.state.put(herdrOne, sessionState{Cwd: "/home/u/src", Tasks: []string{"not an assistant"}})
	n.procs[alive] = &assistantProc{id: alive}
	sent := map[string]string{}
	n.recoverAssistantTasks(func(id, text string) error { sent[id] = text; return nil })
	if len(sent) != 1 || !strings.Contains(sent[lost], "tail the deploy log") || !strings.Contains(sent[lost], "Sleep 23 minutes") {
		t.Fatalf("sent %v", sent)
	}
	if st, _ := n.state.get(lost); len(st.Tasks) != 0 {
		t.Fatal("the lost tasks should be forgotten once told")
	}
	n.recoverAssistantTasks(func(id, text string) error { t.Fatalf("told %s twice", id); return nil })
}

// tasksHub stands in for the hub's copy of the session state: what a node writes (state.put) is what the next
// connection's node loads (state.load).
func tasksHub(t *testing.T, n *Node, hub map[string]sessionState) {
	t.Helper()
	n.state.persist = func(id string, v sessionState) { hub[id] = v }
}

// A tell that fails (its launcher not up yet after the restart, the deadline) keeps the record of the lost tasks on
// the hub, so the next connection tells the conversation; dropping it first left the conversation waiting forever
// for a notification that cannot come (#6306).
func TestRecoverAssistantTasksKeptWhenTheTellFails(t *testing.T) {
	const lost = "11111111-2222-3333-4444-000000000001"
	hub := map[string]sessionState{lost: {Cwd: "/home/u/.persona/assistant", Tasks: []string{"watch CI until it finishes"}}}

	n := assistantNode(t)
	tasksHub(t, n, hub)
	n.state.merge(hub)
	tries := 0
	n.recoverAssistantTasks(func(id, text string) error {
		tries++
		return errors.New("launcher not ready: context deadline exceeded")
	})
	if tries != 1 {
		t.Fatalf("tried to tell it %d times, want 1", tries)
	}
	if st, _ := n.state.get(lost); !slices.Equal(st.Tasks, []string{"watch CI until it finishes"}) {
		t.Fatalf("after a failed tell the node remembers %v", st.Tasks)
	}
	if got := hub[lost].Tasks; !slices.Equal(got, []string{"watch CI until it finishes"}) {
		t.Fatalf("after a failed tell the hub keeps %v", got)
	}

	next := assistantNode(t) // the next connection loads what the hub kept
	tasksHub(t, next, hub)
	next.state.merge(hub)
	told := map[string]int{}
	next.recoverAssistantTasks(func(id, text string) error {
		told[id]++
		if !strings.Contains(text, "watch CI until it finishes") {
			t.Errorf("told %q", text)
		}
		return nil
	})
	if told[lost] != 1 || len(told) != 1 {
		t.Fatalf("the next connection told %v, want the conversation once", told)
	}
	if len(hub[lost].Tasks) != 0 {
		t.Fatalf("told, the hub still keeps %v", hub[lost].Tasks)
	}
}

// A tell that goes clears the record once, on the hub too, and only the tasks it named: what the woken conversation
// re-armed while being told is its new list and stays.
func TestRecoverAssistantTasksClearedOnceTold(t *testing.T) {
	const lost, rearmed = "11111111-2222-3333-4444-000000000001", "11111111-2222-3333-4444-000000000002"
	hub := map[string]sessionState{
		lost:    {Cwd: "/home/u/.persona/assistant", Tasks: []string{"tail the deploy log"}},
		rearmed: {Cwd: "/home/u/.persona/assistant", Tasks: []string{"watch CI until it finishes"}},
	}
	n := assistantNode(t)
	tasksHub(t, n, hub)
	n.state.merge(hub)
	told := map[string]int{}
	n.recoverAssistantTasks(func(id, text string) error {
		told[id]++
		if id == rearmed { // the send started its process, which re-armed the watch before the send returned
			n.mu.Lock()
			n.procs[id] = &assistantProc{id: id}
			n.mu.Unlock()
			n.rememberTasks(id, []string{"watch CI again"}) // its background_tasks_changed
		}
		return nil
	})
	if told[lost] != 1 || told[rearmed] != 1 || len(told) != 2 {
		t.Fatalf("told %v, want each conversation once", told)
	}
	if st, _ := n.state.get(lost); len(st.Tasks) != 0 || len(hub[lost].Tasks) != 0 {
		t.Fatalf("told, still remembered: node %v, hub %v", st.Tasks, hub[lost].Tasks)
	}
	if st, _ := n.state.get(rearmed); !slices.Equal(st.Tasks, []string{"watch CI again"}) || !slices.Equal(hub[rearmed].Tasks, st.Tasks) {
		t.Fatalf("the re-armed list was lost: node %v, hub %v", st.Tasks, hub[rearmed].Tasks)
	}
	n.recoverAssistantTasks(func(id, text string) error { t.Fatalf("told %s twice", id); return nil })
}
