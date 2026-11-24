package voice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// hubClient calls the Persona hub's API with the credential the hub hands its plugins (PERSONA_HUB_URL / PERSONA_HUB_TOKEN).
// A call takes as long as its context allows (callTimeout when it sets none): a send to a conversation that has to be
// resumed first waits minutes (sendTimeout), not a fixed client timeout.
type hubClient struct {
	url, token string
	http       *http.Client
}

// callTimeout is how long a call whose context has no deadline waits.
var callTimeout = 30 * time.Second

// sendTimeout is how long a send waits: the hub's own wait (90 s, 5 minutes for a conversation that is offline or still
// starting: hub/send.go) and 30 s more, so its answer is what comes back.
func sendTimeout(s *proto.Session) time.Duration {
	if s != nil && (s.Starting || s.Status == proto.StatusOffline) {
		return 5*time.Minute + 30*time.Second
	}
	return 90*time.Second + 30*time.Second
}

func newHubClient(base, token string) *hubClient {
	return &hubClient{url: strings.TrimRight(base, "/"), token: token, http: &http.Client{}}
}

// hubError is a call the hub refused: its status, and what its body says (apierr's error, code, delivered, and a
// refused send's details: node/sendsafe.go).
type hubError struct {
	Status    int
	Message   string // the hub's words ("" when its body had none)
	Code      string
	Delivered string // a send: "unknown" when it may have been typed
	// A send refused with dialog_open: the dialog's first line, and notice when Enter only closes it (a send with
	// dismiss_notice goes through). With input_draft: the words in the input box, and what clear_draft takes to clear
	// them (none when they cannot be cleared).
	Dialog, Draft, Clearable string
	Notice                   bool
	call                     string // method and path, for the log
}

func (e *hubError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s: HTTP %d", e.call, e.Status)
}

// maybeSentError is a send that broke off without the hub's answer (the connection dropped, a redeploy, the client's
// deadline): the message may have been typed all the same.
type maybeSentError struct{ err error }

func (e maybeSentError) Error() string { return e.err.Error() }
func (e maybeSentError) Unwrap() error { return e.err }

// maybeSent marks err, of a call that sends (a message, a new conversation's first one), as one that may have been
// delivered unless the hub answered it.
func maybeSent(err error) error {
	var he *hubError
	if err == nil || errors.As(err, &he) {
		return err
	}
	return maybeSentError{err}
}

// sendFailure is how a failed handover is told (voice/refused.md): unknown when the message may have been typed (do not
// repeat it), unreachable when the hub did not answer before anything was sent, else failed with the hub's words (none
// when it said nothing: never the URL or a Go error).
func sendFailure(err error) (code, reason string) {
	var he *hubError
	var ms maybeSentError
	switch {
	case errors.As(err, &ms):
		return "unknown", ""
	case !errors.As(err, &he):
		return "unreachable", ""
	case he.Delivered == "unknown", he.Message == "" && (he.Status == http.StatusBadGateway || he.Status == http.StatusGatewayTimeout):
		return "unknown", "" // the hub, or a proxy in front of it, gave up waiting: it may have got there
	}
	return "failed", he.Message
}

// hubReason is what the model may be told of a failed call: the hub's words, nothing else.
func hubReason(err error) string {
	var he *hubError
	if errors.As(err, &he) {
		return he.Message
	}
	return ""
}

func (h *hubClient) call(ctx context.Context, method, path string, body, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.url+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if l := i18n.From(ctx); l != "" {
		req.Header.Set("Accept-Language", string(l)) // what the hub answers in, where it says something for people
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Error     string `json:"error"`
			Code      string `json:"code"`
			Delivered string `json:"delivered"`
			Dialog    string `json:"dialog"`
			Notice    bool   `json:"notice"`
			Draft     string `json:"draft"`
			Clearable string `json:"clearable"`
		}
		_ = json.Unmarshal(raw, &e)
		return &hubError{Status: res.StatusCode, Message: e.Error, Code: e.Code, Delivered: e.Delivered, Dialog: e.Dialog,
			Notice: e.Notice, Draft: e.Draft, Clearable: e.Clearable, call: method + " " + path}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type kbHit struct {
	Path string `json:"path"`
	Line string `json:"line"`
}

// searchKnowledge: lines of the knowledge base matching every word of the query, of one kind when kind is not ""
// (GET /api/v1/kb-search's kind, #3862 phase 6: a hub from before searches every kind, an unknown kind is its 400).
func (h *hubClient) searchKnowledge(ctx context.Context, query, kind string) ([]kbHit, error) {
	var out struct {
		Hits []kbHit `json:"hits"`
	}
	q := url.Values{"q": {query}}
	if kind != "" {
		q.Set("kind", kind)
	}
	err := h.call(ctx, "GET", "/api/v1/kb-search?"+q.Encode(), nil, &out)
	return out.Hits, err
}

// agents: the coding agents on every machine, without the Persona tab's own conversations.
func (h *hubClient) agents(ctx context.Context) ([]proto.Session, error) {
	var out struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if err := h.call(ctx, "GET", "/api/v1/sessions", nil, &out); err != nil {
		return nil, err
	}
	agents := out.Sessions[:0]
	for _, s := range out.Sessions {
		if !s.Assistant {
			agents = append(agents, s)
		}
	}
	return agents, nil
}

// session is one session as the hub lists it; nil when it is not there.
func (h *hubClient) session(ctx context.Context, machine, id string) (*proto.Session, error) {
	var out struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if err := h.call(ctx, "GET", "/api/v1/sessions?machine_id="+url.QueryEscape(machine), nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Sessions {
		if out.Sessions[i].SessionID == id {
			return &out.Sessions[i], nil
		}
	}
	return nil, nil
}

// liveRoom is a recording's room conversation as GET /api/v1/recordings/{id}/live tells it
// (docs/assistant/meeting-minutes.md "Fields on GET …/live and detail.meta"): its state (an open set), and its ids once
// it has started.
type liveRoom struct {
	State     string `json:"state"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// meetingRoom is recording id's room (GET …/live?parts=room, full credentials: the hub's own token takes it); nil when
// the recording has none (not a room meeting, or a manager from before rooms).
func (h *hubClient) meetingRoom(ctx context.Context, id string) (*liveRoom, error) {
	var out struct {
		Room *liveRoom `json:"room"`
	}
	err := h.call(ctx, "GET", "/api/v1/recordings/"+url.PathEscape(id)+"/live?parts=room", nil, &out)
	return out.Room, err
}

// machineOnline: the hub has machine id connected (false when it does not know it).
func (h *hubClient) machineOnline(ctx context.Context, id string) (bool, error) {
	m, err := h.machine(ctx, id)
	return m != nil && m.Online, err
}

// machine is machine id as the hub lists it; nil when it does not know it.
func (h *hubClient) machine(ctx context.Context, id string) (*proto.Machine, error) {
	var out struct {
		Machines []proto.Machine `json:"machines"`
	}
	if err := h.call(ctx, "GET", "/api/v1/machines", nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Machines {
		if out.Machines[i].ID == id {
			return &out.Machines[i], nil
		}
	}
	return nil, nil
}

// interactions are the cards waiting on the user, on every machine.
func (h *hubClient) interactions(ctx context.Context) ([]proto.Interaction, error) {
	var out struct {
		Interactions []proto.Interaction `json:"interactions"`
	}
	err := h.call(ctx, "GET", "/api/v1/interactions", nil, &out)
	return out.Interactions, err
}

// resolve answers card id (POST /api/v1/interactions/{id}/resolve, as the app's card does). already: it had been
// answered before (the hub answers that 200 with already, and 410 or 404 when it is over otherwise).
func (h *hubClient) resolve(ctx context.Context, id string, r proto.Resolution) (already bool, err error) {
	var out struct {
		Already bool `json:"already"`
	}
	err = h.call(ctx, "POST", "/api/v1/interactions/"+url.PathEscape(id)+"/resolve", r, &out)
	return out.Already, err
}

// channels are the hub's channels (docs/extensions/channels.md), their names in the language ctx carries (i18n.With).
func (h *hubClient) channels(ctx context.Context) ([]channel.View, error) {
	var out struct {
		Channels []channel.View `json:"channels"`
	}
	err := h.call(ctx, "GET", "/api/v1/channels", nil, &out)
	return out.Channels, err
}

// triageSwitches are the hub's session triage switches (GET /api/v1/agents/triage, docs/assistant/agent-work.md "Switches"): whether
// it triages an agent's stopped turn at all, and whether it goes on ("Continue") after a transient model error
// (transient_retry, unset: on, also with the triage off) or once a usage limit resets (auto_continue).
type triageSwitches struct {
	Enabled        bool  `json:"enabled"`
	AutoContinue   bool  `json:"auto_continue"`
	TransientRetry *bool `json:"transient_retry,omitempty"`
}

// triageOff: nothing triages, nothing is continued (the switches unknown).
func triageOff() triageSwitches {
	off := false
	return triageSwitches{TransientRetry: &off}
}

// transientOn: the hub continues a transient model error by itself.
func (sw triageSwitches) transientOn() bool { return sw.TransientRetry == nil || *sw.TransientRetry }

func (h *hubClient) triage(ctx context.Context) (triageSwitches, error) {
	var out triageSwitches
	err := h.call(ctx, "GET", "/api/v1/agents/triage", nil, &out)
	return out, err
}

// interrupt stops the conversation's turn (the app's "Stop" button).
func (h *hubClient) interrupt(ctx context.Context, machine, session string) error {
	return h.call(ctx, "POST", sessionPath(machine, session)+"/interrupt", map[string]any{}, nil)
}

func sessionPath(machine, session string) string {
	return "/api/v1/sessions/" + url.PathEscape(machine) + "/" + url.PathEscape(session)
}

// itemsPage is a page of a conversation's transcript as the hub serves it.
type itemsPage struct {
	Items     []proto.Item `json:"items"`
	MoreAfter bool         `json:"more_after"`
	ToSeq     int          `json:"to_seq"`
}

// last is the last seq the page covers.
func (p itemsPage) last() int {
	last := p.ToSeq
	for _, it := range p.Items {
		last = max(last, it.Seq)
	}
	return last
}

// lastSeq is the conversation's last line now (0 for an empty one): a run's answer comes after it.
func (h *hubClient) lastSeq(ctx context.Context, machine, session string) (int, error) {
	page, err := h.lastTurns(ctx, machine, session, 1)
	if err != nil {
		return 0, err
	}
	return page.last(), nil
}

// lastTurns is the conversation's last n exchanges.
func (h *hubClient) lastTurns(ctx context.Context, machine, session string, n int) (itemsPage, error) {
	var page itemsPage
	err := h.call(ctx, "GET", fmt.Sprintf("%s/items?turns=%d&lazy_images=1", sessionPath(machine, session), n), nil, &page)
	return page, err
}

// itemsAfter is a page of the lines after seq after.
func (h *hubClient) itemsAfter(ctx context.Context, machine, session string, after int) (itemsPage, error) {
	var page itemsPage
	err := h.call(ctx, "GET", fmt.Sprintf("%s/items?after_seq=%d&lazy_images=1", sessionPath(machine, session), after), nil, &page)
	return page, err
}

// itemFull is one line whole (a page cuts a long one).
func (h *hubClient) itemFull(ctx context.Context, machine, session, id string) (proto.Item, error) {
	var it proto.Item
	err := h.call(ctx, "GET", sessionPath(machine, session)+"/items/"+url.PathEscape(id)+"/full", nil, &it)
	return it, err
}

// sendOpts are a send's options (docs/protocol/protocol.md "Sending messages"): the draft in the agent's input box to clear first (as
// the refusal gave it), and closing a notice that Enter only closes.
type sendOpts struct {
	ClearDraft    string
	DismissNotice bool
}

// send types text into a conversation as message id (voice-…: the conversation shows it came by voice,
// docs/extensions/channels.md "Source markers"; the hub sends an id once, and forgets one it refused: the same id goes again).
func (h *hubClient) send(ctx context.Context, machine, session, text, id string, opts ...sendOpts) error {
	body := map[string]any{"text": text, "client_msg_id": id}
	for _, o := range opts {
		if o.ClearDraft != "" {
			body["clear_draft"] = o.ClearDraft
		}
		if o.DismissNotice {
			body["dismiss_notice"] = true
		}
	}
	return h.call(ctx, "POST", sessionPath(machine, session)+"/messages", body, nil)
}

// assistant: where the Persona tab's assistant runs and its main conversation (empty until there is one).
func (h *hubClient) assistant(ctx context.Context) (machine, main string, err error) {
	var out struct {
		MachineID string `json:"machine_id"`
		Main      string `json:"main"`
	}
	err = h.call(ctx, "GET", "/api/v1/assistant", nil, &out)
	return out.MachineID, out.Main, err
}

// startMain starts an assistant conversation with prompt as its first message and makes it the main one.
func (h *hubClient) startMain(ctx context.Context, prompt string) (proto.Session, error) {
	var out struct {
		Session proto.Session `json:"session"`
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // the hub waits up to 110 s for the conversation to start
	defer cancel()
	err := h.call(ctx, "POST", "/api/v1/assistant/conversations", map[string]any{"prompt": prompt, "main": true}, &out)
	return out.Session, err
}

// voiceMessageID is a voice message's client_msg_id: random (64 bits), so a restarted extension or a resumed call
// never reuses one the hub already sent (it would answer 200 and drop the message).
func voiceMessageID() string { return "voice-" + randomID() }

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
