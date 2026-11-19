package node

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A Persona conversation's queue (#4342). A message to a headless claude that is busy waits in Claude Code's own
// queue until its next step takes it in (mid-turn) or its turn ends. Its transcript notes the wait with no words
// (queue-operation enqueue without content, unlike a terminal's), so the node follows its stdout instead: every user
// message goes in with an id of the node's, and Claude Code's command_lifecycle says what became of that id
// (queued, started, cancelled, completed). The ones queued and not started yet are the session's Queued, shown above
// the composer as an Agent's typed-ahead input is (proto.Session.Queued).
//
// The queue strip's actions reach them over stream-json, not keys (an Agent's are keys in its pane, queue.go):
//   - take back, delete, move: cancel_async_message drops a waiting message by its id (cancelled false: it was
//     taken in meanwhile, and stays where it went); moved ones are written again in the new order, with new ids.
//   - send now: an interrupt with send_now, Claude Code's own send-now: the waiting messages reach the model at once,
//     whatever the turn was doing goes to the background or stops ("stopped"), and they start the next request.
//
// Measured on Claude Code 2.1.322. One that sends no command_lifecycle shows no queue, as before; one without these
// control requests answers them with an error, which the strip shows.

// assistantInput is one user message written to claude: its id, what the user wrote and the message's content
// (the live snapshot, any images, the words) to write it again.
type assistantInput struct {
	id      string
	text    string
	images  []proto.Image
	content []map[string]any
	queued  bool      // Claude Code said it queued it, and has not started it yet
	at      time.Time // when it was written
}

// assistantInputKeep is how long a message whose lifecycle never comes (a Claude Code without command_lifecycle,
// or one that started it before saying anything) is remembered; one it queued is kept until it says more.
const assistantInputKeep = time.Minute

// writeInput writes in as a user message with a new id and remembers it until Claude says what became of it. It is
// remembered before it is written: Claude Code says it queued it within a millisecond (measured on 2.1.322), and the
// reader drops a lifecycle line of an id it does not know. A message whose write failed is forgotten again.
func (p *assistantProc) writeInput(in assistantInput) error {
	in.id, in.queued, in.at = newUUID(), false, time.Now()
	line, _ := json.Marshal(map[string]any{"type": "user", "uuid": in.id,
		"message": map[string]any{"role": "user", "content": in.content}})
	p.mu.Lock()
	p.written = slices.DeleteFunc(p.written, func(w assistantInput) bool { return !w.queued && time.Since(w.at) > assistantInputKeep })
	p.written = append(p.written, in)
	p.mu.Unlock()
	if err := p.write(line); err != nil {
		p.mu.Lock()
		p.written = slices.DeleteFunc(p.written, func(w assistantInput) bool { return w.id == in.id })
		p.mu.Unlock()
		return err
	}
	return nil
}

// commandLifecycle applies a command_lifecycle line: queued marks the message as waiting, any later state forgets
// it, and started makes the process busy, also for a message it no longer knows (one written over
// assistantInputKeep ago): a turn starts either way. True when the queue or busy changed.
func (p *assistantProc) commandLifecycle(id, state string) bool {
	if id == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycle = true
	was := false
	if i := slices.IndexFunc(p.written, func(w assistantInput) bool { return w.id == id }); i >= 0 {
		if state == "queued" {
			changed := !p.written[i].queued
			p.written[i].queued = true
			return changed
		}
		was = p.written[i].queued
		p.written = slices.Delete(p.written, i, i+1)
	}
	if state == "started" && !p.busy {
		// A message queued behind a turn starts its own once that turn's result cleared busy (measured on 2.1.322:
		// started 3 ms after the result, the model's first token seconds later). Its turn is under way from here: the
		// next result ends it, and one taken into a turn still running finds busy set already.
		p.busy, p.lastActive = true, time.Now()
		return true
	}
	return was
}

// queuedLocked is what waits, in order (p.mu held).
func (p *assistantProc) queuedLocked() []assistantInput {
	var out []assistantInput
	for _, w := range p.written {
		if w.queued {
			out = append(out, w)
		}
	}
	return out
}

// queuedTexts are the words the queue strip shows: what the user wrote ("" for photos alone, which the apps label).
func queuedTexts(q []assistantInput) []string {
	if len(q) == 0 {
		return nil
	}
	out := make([]string, len(q))
	for i, w := range q {
		out[i] = w.text
	}
	return out
}

// assistantQueueEdit is the answer to an edit: the messages that were queued (the one taken back is the app's to put in
// its composer) and their images.
type assistantQueueEdit = proto.QueueEdit

// assistantQueueProc is the claude running conversation id, or a refusal saying why its queue can't be changed.
func (n *Node) assistantQueueProc(id string) (*assistantProc, error) {
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p == nil || !p.alive() {
		return nil, refusal("nothing_queued", "nothing is queued for the conversation any more", nil)
	}
	if p.agent != "claude" {
		return nil, refusal("queue_unsupported", "this conversation's agent takes messages as they come: nothing waits", nil)
	}
	return p, nil
}

// assistantEditQueue leaves the messages listed in keep queued, in that order, and takes the others out (#4342).
// keep indexes the queue as listed (Session.Queued). Only what has to move is touched: when the kept ones stay in
// their order the others are cancelled; otherwise everything from the first moved one on is cancelled and the kept
// ones are written again in the new order. A message taken in meanwhile can't be cancelled: it is not written again,
// and when it was one to take out the answer is a refusal (it went to Persona).
func (n *Node) assistantEditQueue(ctx context.Context, id string, keep []int) (assistantQueueEdit, error) {
	defer n.typing.lock(id)() // not while a message is on its way in
	// Nor while the process is taken or let go (assistantLife: a round's process retiring, assistant_round.go
	// roundSettled): between the last cancel and the first write again nothing is queued and it may look settled, and
	// the messages cancelled would be written into a process let go.
	defer n.assistantLife.lock(id)()
	p, err := n.assistantQueueProc(id)
	if err != nil {
		return assistantQueueEdit{}, err
	}
	p.mu.Lock()
	queue := p.queuedLocked()
	p.mu.Unlock()
	if len(queue) == 0 {
		return assistantQueueEdit{}, refusal("nothing_queued", "nothing is queued for the conversation any more", nil)
	}
	answer := assistantQueueEdit{Queue: queuedTexts(queue), Images: make([][]proto.Image, len(queue))}
	for i, w := range queue {
		answer.Images[i] = w.images
	}
	if !slices.ContainsFunc(answer.Images, func(im []proto.Image) bool { return len(im) > 0 }) {
		answer.Images = nil
	}
	seen := map[int]bool{}
	for _, k := range keep {
		if k < 0 || k >= len(queue) || seen[k] {
			return answer, refusal("queue_changed", "the queue changed meanwhile: look again", nil)
		}
		seen[k] = true
	}
	same := 0 // kept where they are
	for same < len(keep) && keep[same] == same {
		same++
	}
	var drop, again []int
	if slices.IsSorted(keep[same:]) { // nothing moves: only the ones left out go
		for i := same; i < len(queue); i++ {
			if !seen[i] {
				drop = append(drop, i)
			}
		}
	} else {
		for i := same; i < len(queue); i++ {
			drop = append(drop, i)
		}
		again = keep[same:]
	}
	cancelled := map[int]bool{}
	taken := false
	for _, i := range drop {
		ok, err := p.cancelQueued(ctx, queue[i].id)
		if err != nil {
			return answer, err
		}
		cancelled[i] = ok
		if !ok && !seen[i] {
			taken = true
		}
	}
	for _, i := range again {
		if !cancelled[i] {
			continue // it went in already: never twice
		}
		if err := n.writeQueuedAgain(p, queue[i]); err != nil {
			return answer, err
		}
	}
	n.notifySessions()
	if taken {
		return answer, refusal("queue_taken", "Persona has taken that message in already", nil)
	}
	return answer, nil
}

// cancelQueued drops a waiting message from claude's queue: false when it was not waiting any more.
func (p *assistantProc) cancelQueued(ctx context.Context, id string) (bool, error) {
	raw, err := p.controlAnswer(ctx, map[string]any{"subtype": "cancel_async_message", "message_uuid": id})
	if err != nil {
		return false, err
	}
	var a struct {
		Cancelled bool `json:"cancelled"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.Cancelled {
		p.commandLifecycle(id, "cancelled") // its own line follows; the list should not show it meanwhile
	}
	return a.Cancelled, nil
}

// writeQueuedAgain writes a cancelled message again, at the end of the queue. The turn may have ended meanwhile: it
// then starts the next one, as any message to an idle conversation does.
func (n *Node) writeQueuedAgain(p *assistantProc, w assistantInput) error {
	p.mu.Lock()
	p.busy = true
	p.lastActive = time.Now()
	p.mu.Unlock()
	return p.writeInput(w)
}

// assistantSendNow gives claude what waits in its queue now (#4342), with Claude Code's own send-now: an interrupt with
// send_now, which ends nothing outside the turn and cancels nothing queued.
func (n *Node) assistantSendNow(ctx context.Context, id string) error {
	defer n.typing.lock(id)()
	p, err := n.assistantQueueProc(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	waiting := len(p.queuedLocked())
	p.mu.Unlock()
	if waiting == 0 {
		return refusal("nothing_queued", "nothing is queued for the conversation any more", nil)
	}
	raw, err := p.controlAnswer(ctx, map[string]any{"subtype": "interrupt", "send_now": true})
	if err != nil {
		return err
	}
	var a struct {
		SendNow string `json:"send_now"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.SendNow == "nothing_waiting" {
		return refusal("nothing_queued", "nothing is queued for the conversation any more", nil)
	}
	return nil
}
