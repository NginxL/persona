package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Persona's Claude Code plugin (integrations/claude-code-plugin; docs/integrations/claude-integration.md "Owner input
// through the Persona plugin"). A claude outside Persona's terminals — Claude's desktop app, a terminal no engine runs,
// a background session — has no pane to type into, and its peer inbox tells the model that what comes through it is
// another session's, never the person's approval. The plugin, loaded in such a claude, asks this node for messages
// over the node socket while the session lives, and submits each with $.prompt.submit({asUser: true}): the session
// reads it as the person's own words, as typed input.
//
// Only the owner's own messages go that way (session.send's owner_input, which the devices manager sets for a message
// typed in the owner's app, devices.go ownerInputParams), and only to a session at rest: one that works or waits on a
// dialog gets it through the inbox as before (it queues there, or answers its question), and so does everyone else's
// message. When the plugin does not take the message in time or says it did not submit it, it goes through the inbox
// too, and session.send says which way it went (delivered_as, relay_reason).
//
// The wire is HTTP/1.1 on the node socket (serveCCPluginHTTP), since a plugin's only local transport is $.http.fetch over
// a Unix socket: POST /claude-plugin/v1/poll and /ack, JSON both ways (the plugin's README.md). The node answers only
// the claude process that has the session open: the socket's other end, as the system names it (ccPluginPeerPID), must
// be the pid of that session's sessions/<pid>.json. No other process of the user, and no other session, can take a
// session's messages; and nothing that comes in on the socket ever becomes a message, which only session.send makes.

// ccPluginProtocol is the plugin protocol this node speaks (claude-plugin.status).
const ccPluginProtocol = 1

// ccPluginPrefix leads the plugin's endpoints.
const ccPluginPrefix = "/claude-plugin/v1/"

const (
	// ccPluginPollMost bounds how long a poll is held: under the 30 s after which Claude Code's fetch gives up.
	ccPluginPollMost = 25 * time.Second
	// ccPluginOtherRetry is how long a claude whose session the node does not deliver to this way (one in an engine's
	// pane, the assistant's own, an SDK's) waits before it asks again.
	ccPluginOtherRetry = 5 * time.Minute
	// ccPluginStrangerRetry is how long a caller the node does not take for the session it names waits: a claude that
	// has not written its sessions/<pid>.json yet asks again soon.
	ccPluginStrangerRetry = 15 * time.Second
	// ccPluginBodyMost bounds a request's body, and ccPluginRequestMost the whole request with its headers.
	ccPluginBodyMost    = 64 << 10
	ccPluginRequestMost = 256 << 10
	// ccPluginStale is how long after its last poll a plugin still counts: an idle one polls again at once, a busy
	// session's plugin keeps polling too (the node, not the plugin, tells whether the session rests).
	ccPluginStale = 90 * time.Second
)

var (
	// ccPluginPickupWait is how long a message waits for the session's plugin to take it: an idle plugin's next poll
	// comes within moments of the last one ending.
	ccPluginPickupWait = 3 * time.Second
	// ccPluginSubmitWait is the deadline the plugin gets: it submits nothing later than this after it was handed over.
	ccPluginSubmitWait = 5 * time.Second
	// ccPluginConfirmWait is how long the node looks for the prompt in the transcript, past ccPluginSubmitWait: only a
	// message the plugin can no longer submit goes through the inbox instead.
	ccPluginConfirmWait = 10 * time.Second
)

// What session.send says about how a message for the owner went in (delivered_as), and why it went through the inbox
// (relay_reason). Only messages that carried owner_input to a claude outside Persona's terminals have them.
const (
	deliveredUserInput = "user_input" // the plugin submitted it as the person's words
	deliveredRelayed   = "relayed"    // through the session's peer inbox, as another session's message

	relayNoPlugin    = "no_plugin"          // no plugin polls from this session's claude
	relayBusy        = "busy"               // the session works or waits on a dialog: the inbox queues the message there
	relayUnavailable = "plugin_unavailable" // the plugin did not take it in time
	relayRefused     = "plugin_refused"     // the plugin took it and did not submit it
	relayUnconfirmed = "plugin_unconfirmed" // taken, and nothing showed it went in before its deadline
)

// ccPluginMessage is one message handed to a plugin, as the poll answers it.
type ccPluginMessage struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	DeadlineMS int64  `json:"deadline_ms"`
}

// ccPluginAck is what the plugin said became of a message.
type ccPluginAck struct {
	Submitted bool
	Reason    string
}

// ccPluginSession is a session whose plugin polls this node.
type ccPluginSession struct {
	pid             int
	version, engine string
	seen            time.Time
	// poll is the waiting poll's hand-off (one message), nil while none waits; done ends it early (a newer poll).
	poll chan ccPluginMessage
	done chan struct{}
	acks map[string]chan ccPluginAck // messages handed over and not settled, by id
}

// ccPluginRegistry is every session whose plugin polled this node since it started; its zero value is ready.
type ccPluginRegistry struct {
	mu       sync.Mutex
	sessions map[string]*ccPluginSession
}

// polled records a poll from session id's claude pid and makes it the session's waiting poll; the poll it replaces
// (the same session's, still waiting) is ended. It returns the poll's hand-off and its end.
func (r *ccPluginRegistry) polled(id string, pid int, version, engine string) (chan ccPluginMessage, chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*ccPluginSession{}
	}
	for other, o := range r.sessions {
		if o.poll == nil && len(o.acks) == 0 && time.Since(o.seen) > ccPluginStale {
			delete(r.sessions, other) // its claude exited or moved on; a live one polls again and is back
		}
	}
	s := r.sessions[id]
	if s == nil || s.pid != pid {
		s = &ccPluginSession{pid: pid, acks: map[string]chan ccPluginAck{}}
		r.sessions[id] = s
	}
	if s.done != nil {
		close(s.done)
	}
	s.version, s.engine, s.seen = version, engine, time.Now()
	s.poll, s.done = make(chan ccPluginMessage, 1), make(chan struct{})
	return s.poll, s.done
}

// pollEnded takes poll off session id when it is still the waiting one; ok false when a message was handed to it (or
// a newer poll replaced it), which the caller then reads from it.
func (r *ccPluginRegistry) pollEnded(id string, poll chan ccPluginMessage) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	if s == nil || s.poll != poll {
		return false
	}
	s.poll, s.done = nil, nil
	s.seen = time.Now()
	return true
}

// session is session id's plugin while it polls from claude pid (a poll waits, or the last one ended a moment ago);
// nil for none.
func (r *ccPluginRegistry) session(id string, pid int) *ccPluginSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[id]; s != nil && s.pid == pid && (s.poll != nil || time.Since(s.seen) < ccPluginStale) {
		return s
	}
	return nil
}

// expect makes room for the plugin's word on message mid, before it is handed over.
func (r *ccPluginRegistry) expect(id, mid string) chan ccPluginAck {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan ccPluginAck, 1)
	if s := r.sessions[id]; s != nil {
		s.acks[mid] = ch
	}
	return ch
}

// settle forgets message mid.
func (r *ccPluginRegistry) settle(id, mid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[id]; s != nil {
		delete(s.acks, mid)
	}
}

// acked passes the plugin's word on message mid on, false for a message the node is not waiting on.
func (r *ccPluginRegistry) acked(id string, pid int, mid string, a ccPluginAck) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	if s == nil || s.pid != pid {
		return false
	}
	ch, ok := s.acks[mid]
	if !ok {
		return false
	}
	select {
	case ch <- a:
	default:
	}
	return true
}

// handOver gives m to session id's waiting poll from claude pid, waiting up to wait for one; false when none took it.
func (r *ccPluginRegistry) handOver(ctx context.Context, id string, pid int, m ccPluginMessage, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		s := r.sessions[id]
		if s != nil && s.pid == pid && s.poll != nil {
			s.poll <- m // room for one, and only handed over once: the poll is taken off below
			s.poll, s.done = nil, nil
			r.mu.Unlock()
			return true
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ccPluginServes: the node hands the owner's messages to the plugin in claude c, a claude outside Persona's terminals
// (a variable so the live test can serve the stream-json claude it starts, which an SDK's entrypoint names).
var ccPluginServes = func(n *Node, c claudeProc) bool { return n.outsideSource(c) != "" }

// ccPluginStatus is one session in claude-plugin.status.
type ccPluginStatus struct {
	SessionID     string    `json:"session_id"`
	PID           int       `json:"pid"`
	PluginVersion string    `json:"plugin_version"`
	EngineVersion string    `json:"engine_version"`
	Polling       bool      `json:"polling"`
	SeenAt        time.Time `json:"seen_at"`
}

// status lists the sessions whose plugin polled, those whose claude still runs (live: by session, its pid).
func (r *ccPluginRegistry) status(live map[string]int) []ccPluginStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []ccPluginStatus{}
	for id, s := range r.sessions {
		if live[id] != s.pid {
			delete(r.sessions, id) // its claude exited, or went on in another session (a /clear polls under the new id)
			continue
		}
		out = append(out, ccPluginStatus{SessionID: id, PID: s.pid, PluginVersion: s.version, EngineVersion: s.engine,
			Polling: s.poll != nil, SeenAt: s.seen.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// ccPluginInput: session id, open in claude pid, has the plugin (its session row's capabilities.plugin_input).
func (n *Node) ccPluginInput(id string, pid int) bool {
	return n.ccPlugins.session(id, pid) != nil
}

// ccPluginStatusNow is claude-plugin.status's answer.
func (n *Node) ccPluginStatusNow() map[string]any {
	live := map[string]int{}
	for _, c := range n.liveClaudes() {
		live[c.SessionID] = c.pid
	}
	return map[string]any{"protocol": ccPluginProtocol, "sessions": n.ccPlugins.status(live)}
}

// isHTTPRequestLine: the socket's first line is an HTTP request's (the plugin's), not a JSON request.
func isHTTPRequestLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("POST /")) || bytes.HasPrefix(line, []byte("GET /"))
}

// serveCCPluginHTTP answers one HTTP request on the node socket: the plugin's. first is the request's start, already
// read; rest the connection's reader after it.
func (n *Node) serveCCPluginHTTP(ctx context.Context, c net.Conn, first []byte, rest *bufio.Reader) {
	// A request is small: its line, a few headers and a body of at most ccPluginBodyMost. What is past this bound is
	// never read, so no caller makes the node keep more.
	br := bufio.NewReader(io.LimitReader(io.MultiReader(bytes.NewReader(first), rest), ccPluginRequestMost))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, ccPluginBodyMost+1))
	if err != nil || len(body) > ccPluginBodyMost {
		writeCCPluginHTTP(c, http.StatusRequestEntityTooLarge, map[string]any{"error": "too_large"})
		return
	}
	// The plugin going away (its session ended, a reload) ends a poll it left waiting.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = br.ReadByte()
		cancel()
	}()
	status, out := n.ccPluginRequest(ctx, c, req.Method, req.URL.Path, body)
	writeCCPluginHTTP(c, status, out)
}

// ccPluginRequest is one of the plugin's requests, answered with a status and a JSON body.
func (n *Node) ccPluginRequest(ctx context.Context, c net.Conn, method, path string, body []byte) (int, any) {
	what, ok := strings.CutPrefix(path, ccPluginPrefix)
	if !ok || method != http.MethodPost || (what != "poll" && what != "ack") {
		return http.StatusNotFound, map[string]any{"error": "unknown"}
	}
	var in struct {
		SessionID     string `json:"session_id"`
		PluginVersion string `json:"plugin_version"`
		EngineVersion string `json:"engine_version"`
		WaitMS        int64  `json:"wait_ms"`
		ID            string `json:"id"`
		Submitted     bool   `json:"submitted"`
		Reason        string `json:"reason"`
	}
	if json.Unmarshal(body, &in) != nil || !validID(in.SessionID) {
		return http.StatusBadRequest, map[string]any{"error": "invalid"}
	}
	pid, err := ccPluginPeerPID(c)
	if err != nil || pid <= 0 {
		// A system that does not say who is at the other end: nothing is handed to anyone.
		return http.StatusForbidden, map[string]any{"error": "peer_unknown", "retry_ms": ccPluginOtherRetry.Milliseconds()}
	}
	claude, ok := n.liveClaudeOf(pid)
	if !ok || claude.SessionID != in.SessionID {
		return http.StatusForbidden, map[string]any{"error": "not_this_session", "retry_ms": ccPluginStrangerRetry.Milliseconds()}
	}
	if what == "ack" {
		n.ccPlugins.acked(in.SessionID, pid, in.ID, ccPluginAck{Submitted: in.Submitted, Reason: clipReason(in.Reason)})
		return http.StatusOK, map[string]any{"ok": true}
	}
	if !ccPluginServes(n, claude) {
		// A pane's claude takes typing, and the assistant's or an SDK's is driven by its own host: none of them gets
		// messages this way.
		return http.StatusOK, map[string]any{"messages": []ccPluginMessage{}, "retry_ms": ccPluginOtherRetry.Milliseconds()}
	}
	wait := time.Duration(in.WaitMS) * time.Millisecond
	if wait <= 0 || wait > ccPluginPollMost {
		wait = ccPluginPollMost
	}
	poll, done := n.ccPlugins.polled(in.SessionID, pid, clipReason(in.PluginVersion), clipReason(in.EngineVersion))
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case m := <-poll:
		return http.StatusOK, map[string]any{"messages": []ccPluginMessage{m}}
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
	if !n.ccPlugins.pollEnded(in.SessionID, poll) {
		select {
		case m := <-poll: // handed over as the poll ended
			return http.StatusOK, map[string]any{"messages": []ccPluginMessage{m}}
		default:
		}
	}
	return http.StatusOK, map[string]any{"messages": []ccPluginMessage{}}
}

// clipReason keeps a short word from the plugin (a version, a reason) short.
func clipReason(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// writeCCPluginHTTP writes one HTTP response with a JSON body and closes the exchange.
func writeCCPluginHTTP(c net.Conn, status int, body any) {
	b, _ := json.Marshal(body)
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(b), b)
}

// liveClaudeOf is the running claude whose process is pid.
func (n *Node) liveClaudeOf(pid int) (claudeProc, bool) {
	for _, c := range n.liveClaudes() {
		if c.pid == pid {
			return c, true
		}
	}
	return claudeProc{}, false
}

// ownerInputExact: params carry the owner mark exactly as the devices manager writes it, "owner_input": true, and no
// other key that encoding/json would also read as it (OWNER_INPUT, Owner_Input…: its field match ignores case). The
// devices manager strips every such key a caller sent (devices/owner_input.go); this keeps a node from honouring a
// variant that reached it some other way (#9607).
func ownerInputExact(params json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(params))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	owner := false
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		var value json.RawMessage
		if err != nil || !ok || decoder.Decode(&value) != nil {
			return false
		}
		if strings.EqualFold(key, "owner_input") {
			if owner || key != "owner_input" || string(value) != "true" {
				return false
			}
			owner = true
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false
	}
	_, err := decoder.Token()
	return owner && err == io.EOF
}

// ownerDelivery is where the send says how the owner's message went in (a tmux pane, the plugin or the inbox), for
// session.send's answer.
type ownerDelivery struct{ as, reason string }

type ownerDeliveryKey struct{}

// withOwnerDelivery marks a send as the owner's own message (owner_input): an outside Claude's tmux pane takes it as
// terminal input; without a pane, sendOutside may hand it to the plugin.
func withOwnerDelivery(ctx context.Context) (context.Context, *ownerDelivery) {
	d := new(ownerDelivery)
	return context.WithValue(ctx, ownerDeliveryKey{}, d), d
}

// ownerDeliveryOf is the send's ownerDelivery, nil for a message that is not the owner's own.
func ownerDeliveryOf(ctx context.Context) *ownerDelivery {
	d, _ := ctx.Value(ownerDeliveryKey{}).(*ownerDelivery)
	return d
}

// ccPluginSend hands text, the owner's own message, to the plugin in claude c, which has session id open. true when the
// session took it as the person's input: the transcript shows the prompt (or its queue entry), or the plugin said it
// submitted it. false with why it did not, nothing submitted (the caller sends it through the inbox).
func (n *Node) ccPluginSend(ctx context.Context, c claudeProc, id, text string) (bool, string) {
	if n.ccPlugins.session(id, c.pid) == nil {
		return false, relayNoPlugin
	}
	if claudeBgStatus[c.Status] != proto.StatusIdle {
		return false, relayBusy // at work, or behind a dialog: the inbox queues it there, or answers the question
	}
	path := n.transcriptPath(id)
	var offset int64
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	}
	m := ccPluginMessage{ID: strings.ReplaceAll(newUUID(), "-", ""), Text: text, DeadlineMS: time.Now().Add(ccPluginSubmitWait).UnixMilli()}
	acks := n.ccPlugins.expect(id, m.ID)
	defer n.ccPlugins.settle(id, m.ID)
	if !n.ccPlugins.handOver(ctx, id, c.pid, m, ccPluginPickupWait) {
		return false, relayUnavailable
	}
	needle := ccPluginNeedle(text)
	deadline := time.Now().Add(ccPluginConfirmWait)
	submitted := false
	for {
		if path == "" {
			path = n.transcriptPath(id)
		}
		if path != "" && ccPluginSeen(path, offset, needle) {
			return true, ""
		}
		if time.Now().After(deadline) {
			if submitted {
				return true, "" // its turn started with it (the plugin's submit resolves then); the line is late
			}
			log.Printf("send %s: the plugin took it and nothing showed it went in within %s", id, ccPluginConfirmWait)
			return false, relayUnconfirmed
		}
		select {
		case a := <-acks:
			if !a.Submitted {
				log.Printf("send %s: the plugin did not submit it (%s)", id, a.Reason)
				return false, relayRefused
			}
			submitted = true
		case <-ctx.Done():
			return false, relayUnconfirmed
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// ccPluginNeedle is the first words of text: at most 40 runes, to the end of its first line.
func ccPluginNeedle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	r := []rune(strings.TrimSpace(line))
	if len(r) > 40 {
		r = r[:40]
	}
	return string(r)
}

// ccPluginSeen: a line past offset is the plugin's prompt with needle in it (a user line Persona's plugin submitted as
// the person's words), or the queue entry Claude Code writes when it takes it.
func ccPluginSeen(path string, offset int64, needle string) bool {
	if needle == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		l, err := peerLine(br)
		if ccPluginLineSeen(l, needle) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// ccPluginName is the plugin's name, as Claude Code writes it in a prompt's origin.
const ccPluginName = "persona"

// ccPluginLineSeen: l is the plugin's prompt with needle, or a queue entry whose text has it.
func ccPluginLineSeen(l []byte, needle string) bool {
	if !bytes.Contains(l, []byte(`"queue-operation"`)) && !bytes.Contains(l, []byte(`"plugin"`)) {
		return false
	}
	var e struct {
		Type      string `json:"type"`
		Operation string `json:"operation"`
		Content   string `json:"content"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Origin *struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			AsUser bool   `json:"asUser"`
		} `json:"origin"`
	}
	if json.Unmarshal(l, &e) != nil {
		return false
	}
	switch {
	case e.Type == "queue-operation":
		return e.Operation == "enqueue" && strings.Contains(e.Content, needle)
	case e.Type == "user" && e.Origin != nil && e.Origin.Kind == "plugin" && e.Origin.Name == ccPluginName && e.Origin.AsUser:
		var said string
		_ = json.Unmarshal(e.Message.Content, &said)
		return strings.Contains(said, needle)
	}
	return false
}
