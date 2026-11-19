package node

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// fakeClaudeIn is a headless claude's stdin: it keeps the user messages written to it and answers the queue's
// control requests as Claude Code 2.1.322 does (assistant_queue.go).
type fakeClaudeIn struct {
	p *assistantProc

	mu       sync.Mutex
	users    []fakeUserLine
	controls []map[string]any
	taken    map[string]bool // ids claude took in already: cancel_async_message answers cancelled false
	sendNow  string          // the interrupt's send_now answer
	refuse   string          // an error for every control request (a Claude Code without them)
	// queueAtOnce: claude says it queued a user message before the write returns, as its reader may get the line
	// before the writer goes on (assistant_round_hold_test.go).
	queueAtOnce bool
	failUsers   error                        // the write of every user message fails with it
	onControl   func(request map[string]any) // runs before a control request is answered, no lock held
}

type fakeUserLine struct {
	id   string
	text string // the last text block: the user's words
}

func (f *fakeClaudeIn) Write(b []byte) (int, error) {
	var m struct {
		Type      string         `json:"type"`
		UUID      string         `json:"uuid"`
		RequestID string         `json:"request_id"`
		Request   map[string]any `json:"request"`
		Message   struct {
			Content []map[string]any `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	var answer *assistantControlResponse
	f.mu.Lock()
	switch m.Type {
	case "user":
		if err := f.failUsers; err != nil {
			f.mu.Unlock()
			return 0, err
		}
		text, _ := m.Message.Content[len(m.Message.Content)-1]["text"].(string)
		f.users = append(f.users, fakeUserLine{id: m.UUID, text: text})
		if f.queueAtOnce {
			defer f.p.commandLifecycle(m.UUID, "queued")
		}
	case "control_request":
		f.controls = append(f.controls, m.Request)
		a := assistantControlResponse{RequestID: m.RequestID, Subtype: "success"}
		switch {
		case f.refuse != "":
			a.Subtype, a.Error = "error", f.refuse
		case m.Request["subtype"] == "cancel_async_message":
			id, _ := m.Request["message_uuid"].(string)
			a.Response, _ = json.Marshal(map[string]bool{"cancelled": !f.taken[id]})
		case m.Request["subtype"] == "interrupt":
			a.Response, _ = json.Marshal(map[string]any{"send_now": f.sendNow})
		}
		answer = &a
	}
	onControl := f.onControl
	f.mu.Unlock()
	if answer != nil {
		if onControl != nil {
			onControl(m.Request)
		}
		f.p.controlResponse(*answer)
	}
	return len(b), nil
}

func (f *fakeClaudeIn) Close() error { return nil }

func (f *fakeClaudeIn) userLines() []fakeUserLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.users)
}

func (f *fakeClaudeIn) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.controls)
}

const queueTestID = "11111111-2222-3333-4444-555555555555"

// queueTestProc is a busy Persona conversation on the CLI with texts written to it while it worked, each one queued
// by claude.
func queueTestProc(t *testing.T, texts ...string) (*Node, *assistantProc, *fakeClaudeIn) {
	t.Helper()
	n := assistantNode(t)
	f := &fakeClaudeIn{taken: map[string]bool{}}
	p := &assistantProc{id: queueTestID, agent: "claude", stdin: f, exited: make(chan struct{}), busy: true}
	f.p = p
	n.procs[queueTestID] = p
	for _, text := range texts {
		writeQueued(t, n, p, f, text)
	}
	return n, p, f
}

// writeQueued writes text as assistantSend does and has claude queue it, through its stdout.
func writeQueued(t *testing.T, n *Node, p *assistantProc, f *fakeClaudeIn, text string) {
	t.Helper()
	in := assistantInput{text: text, content: []map[string]any{{"type": "text", "text": "<snapshot>"}, {"type": "text", "text": text}}}
	if err := p.writeInput(in); err != nil {
		t.Fatal(err)
	}
	lines := f.userLines()
	lifecycle(n, p, lines[len(lines)-1].id, "queued")
}

func lifecycle(n *Node, p *assistantProc, id, state string) {
	n.readAssistant(p, strings.NewReader(`{"type":"command_lifecycle","command_uuid":"`+id+`","state":"`+state+`"}`+"\n"))
}

func queuedNow(p *assistantProc) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return queuedTexts(p.queuedLocked())
}

// Claude Code notes a queued message without its words in the transcript; the node lists what it wrote and claude
// said it queued, until claude starts it (#4342).
func TestAssistantQueueFollowsCommandLifecycle(t *testing.T) {
	n, p, f := queueTestProc(t, "first", "second")
	if err := p.writeInput(assistantInput{text: "never queued", content: []map[string]any{{"type": "text", "text": "never queued"}}}); err != nil {
		t.Fatal(err)
	}
	lines := f.userLines()
	if len(lines) != 3 || lines[0].id == "" || lines[0].id == lines[1].id {
		t.Fatalf("user lines %+v, want each with an id of its own", lines)
	}
	list := n.assistantSessions()
	if len(list) != 1 || !slices.Equal(list[0].Queued, []string{"first", "second"}) {
		t.Fatalf("listed %+v, want first and second queued", list)
	}
	lifecycle(n, p, lines[0].id, "started")
	lifecycle(n, p, lines[2].id, "started")
	if got := queuedNow(p); !slices.Equal(got, []string{"second"}) {
		t.Fatalf("queue %q after first started", got)
	}
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
	if list := n.assistantSessions(); len(list[0].Queued) != 0 {
		t.Fatalf("an idle conversation lists %q queued", list[0].Queued)
	}
}

// Taking one out cancels only that one; moving one cancels from the first moved on and writes them again in the new
// order, with new ids and their content.
func TestAssistantEditQueueTouchesOnlyWhatChanges(t *testing.T) {
	n, p, f := queueTestProc(t, "a", "b", "c")
	ids := []string{}
	for _, l := range f.userLines() {
		ids = append(ids, l.id)
	}
	answer, err := n.assistantEditQueue(context.Background(), queueTestID, []int{0, 2})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(answer.Queue, []string{"a", "b", "c"}) {
		t.Fatalf("answer %q, want the queue it changed", answer.Queue)
	}
	if reqs := f.requests(); len(reqs) != 1 || reqs[0]["subtype"] != "cancel_async_message" || reqs[0]["message_uuid"] != ids[1] {
		t.Fatalf("requests %v, want b's cancel only", reqs)
	}
	if len(f.userLines()) != 3 {
		t.Fatal("taking one out wrote messages again")
	}
	if got := queuedNow(p); !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("queue %q", got)
	}

	if _, err := n.assistantEditQueue(context.Background(), queueTestID, []int{1, 0}); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()[1:]
	if len(reqs) != 2 || reqs[0]["message_uuid"] != ids[0] || reqs[1]["message_uuid"] != ids[2] {
		t.Fatalf("requests %v, want a's and c's cancels", reqs)
	}
	again := f.userLines()[3:]
	if len(again) != 2 || again[0].text != "c" || again[1].text != "a" || again[0].id == ids[2] || again[1].id == ids[0] {
		t.Fatalf("written again %+v, want c then a with new ids", again)
	}
	for _, l := range again {
		lifecycle(n, p, l.id, "queued")
	}
	if got := queuedNow(p); !slices.Equal(got, []string{"c", "a"}) {
		t.Fatalf("queue %q after the move", got)
	}
}

// A message claude took in meanwhile is never written twice; taking it out says it went in already.
func TestAssistantEditQueueLeavesWhatClaudeTook(t *testing.T) {
	n, _, f := queueTestProc(t, "a", "b")
	lines := f.userLines()
	f.taken[lines[0].id] = true
	_, err := n.assistantEditQueue(context.Background(), queueTestID, []int{1})
	if refusalCode(err) != "queue_taken" {
		t.Fatalf("taking out a taken message = %v, want queue_taken", err)
	}
	if _, err := n.assistantEditQueue(context.Background(), queueTestID, []int{1, 0}); err != nil {
		t.Fatalf("moving = %v", err)
	}
	again := f.userLines()[2:]
	if len(again) != 1 || again[0].text != "b" {
		t.Fatalf("written again %+v, want b only (a was taken)", again)
	}
	if _, err := n.assistantEditQueue(context.Background(), queueTestID, []int{5}); refusalCode(err) != "queue_changed" {
		t.Fatalf("an index past the queue = %v, want queue_changed", err)
	}
}

// Pulled back, a message's photos come back with it for the composer.
func TestAssistantEditQueueAnswersPhotos(t *testing.T) {
	n, p, f := queueTestProc(t, "words")
	img := proto.Image{Mime: "image/png", Data: "iVBORw0KGgo="}
	if err := p.writeInput(assistantInput{text: "with a photo", images: []proto.Image{img}, content: []map[string]any{{"type": "text", "text": "with a photo"}}}); err != nil {
		t.Fatal(err)
	}
	lines := f.userLines()
	lifecycle(n, p, lines[len(lines)-1].id, "queued")
	answer, err := n.assistantEditQueue(context.Background(), queueTestID, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	if len(answer.Images) != 2 || len(answer.Images[0]) != 0 || len(answer.Images[1]) != 1 || answer.Images[1][0] != img {
		t.Fatalf("images %+v", answer.Images)
	}
}

// Send now is Claude Code's own: an interrupt with send_now. Nothing waiting is said so, and a Claude Code without
// these requests refuses.
func TestAssistantSendNow(t *testing.T) {
	n, p, f := queueTestProc(t, "a")
	f.sendNow = "stopped"
	if err := n.assistantSendNow(context.Background(), queueTestID); err != nil {
		t.Fatal(err)
	}
	if reqs := f.requests(); len(reqs) != 1 || reqs[0]["subtype"] != "interrupt" || reqs[0]["send_now"] != true {
		t.Fatalf("requests %v", reqs)
	}
	f.sendNow = "nothing_waiting"
	if err := n.assistantSendNow(context.Background(), queueTestID); refusalCode(err) != "nothing_queued" {
		t.Fatalf("nothing waiting = %v", err)
	}
	f.refuse = "unsupported control request: cancel_async_message"
	if _, err := n.assistantEditQueue(context.Background(), queueTestID, nil); refusalCode(err) != "assistant_control_failed" {
		t.Fatalf("an old Claude Code = %v", err)
	}
	lifecycle(n, p, f.userLines()[0].id, "started")
	before := len(f.requests())
	if err := n.assistantSendNow(context.Background(), queueTestID); refusalCode(err) != "nothing_queued" || len(f.requests()) != before {
		t.Fatalf("an empty queue = %v, and asked claude anyway", err)
	}
	p.agent = "codex"
	if _, err := n.assistantEditQueue(context.Background(), queueTestID, nil); refusalCode(err) != "queue_unsupported" {
		t.Fatalf("codex = %v", err)
	}
}
