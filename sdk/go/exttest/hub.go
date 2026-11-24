package exttest

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// sentTargetKeys is the most keys of a sent.record target the hub keeps.
const sentTargetKeys = 20

// hub is the hub's side of the protocol, enough to check a program: it calls, and answers the program's calls to
// the hub as the real one would: it refuses what the manifest does not let the program ask (refuses), keeps a
// connector's secrets in memory, a channel's messages and the sends recorded, answers what answers says, refuses a call
// of the connector, channel or knowledge-base namespace it does not know, and answers another extension point's with {}. What the
// program sends that the schemas describe (wire.Calls) is held to them; what does not hold is in problems.
type hub struct {
	run     Run
	wmu     sync.Mutex
	next    atomic.Int64
	mu      sync.Mutex
	pending map[string]chan frame
	closed  chan struct{}

	manifest *wire.Manifest // what refuses goes by; nil refuses nothing
	answers  map[string]func(params json.RawMessage) (any, error)
	secrets  map[string][]byte // by contribution
	events   []Event
	received []wire.ChannelReceiveParams
	sent     []wire.ConnectorSentRecordParams
	problems []string
}

// Event is one the program sent the hub.
type Event struct {
	Name   string
	Params json.RawMessage
}

type frame struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Event  string          `json:"event,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ext.Error      `json:"error,omitempty"`
}

func newHub(run Run) *hub {
	return &hub{run: run, pending: map[string]chan frame{}, closed: make(chan struct{}),
		answers: map[string]func(json.RawMessage) (any, error){}, secrets: map[string][]byte{}}
}

func (h *hub) serve(ctx context.Context) {
	defer close(h.closed)
	sc := bufio.NewScanner(h.run.FromProgram)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var f frame
		if json.Unmarshal(sc.Bytes(), &f) != nil {
			continue
		}
		switch {
		case f.Method != "":
			if f.ID == "" {
				continue
			}
			go h.answer(f)
		case f.Event != "":
			h.mu.Lock()
			h.events = append(h.events, Event{f.Event, append(json.RawMessage(nil), f.Params...)})
			h.mu.Unlock()
			if _, ok := wire.Calls[f.Event]; ok {
				h.holds(f.Event, wire.ValidateParams(f.Event, f.Params))
			}
		case f.ID != "" && (f.Result != nil || f.Error != nil):
			h.mu.Lock()
			ch := h.pending[f.ID]
			delete(h.pending, f.ID)
			h.mu.Unlock()
			if ch != nil {
				ch <- f
			}
		}
	}
}

func wireError(err error) *ext.Error {
	var e *ext.Error
	if errors.As(err, &e) {
		return e
	}
	var c ext.Coded
	if errors.As(err, &c) {
		code, data := c.WireError()
		return &ext.Error{Message: c.Error(), Code: code, Data: data}
	}
	return &ext.Error{Message: err.Error()}
}

// answer answers one of the program's calls.
func (h *hub) answer(f frame) {
	if _, ok := wire.Calls[f.Method]; ok {
		h.holds(f.Method, wire.ValidateParams(f.Method, f.Params))
	}
	res, err := h.result(f.Method, f.Params)
	out := frame{ID: f.ID}
	if err != nil {
		out.Error = wireError(err)
	} else {
		b, merr := json.Marshal(res)
		if merr != nil {
			b = json.RawMessage("{}")
		}
		out.Result = b
	}
	_ = h.write(out)
}

func (h *hub) result(method string, params json.RawMessage) (any, error) {
	h.mu.Lock()
	f := h.answers[method]
	m := h.manifest
	h.mu.Unlock()
	call, described := wire.Calls[method]
	ns, _, _ := strings.Cut(method, ".")
	if !described || call.From != "program" {
		if ns == ext.ConnectorNamespace || ns == ext.ChannelNamespace || ns == "kb" {
			return nil, ext.UnknownMethod(method)
		}
		if f != nil {
			return f(params)
		}
		return struct{}{}, nil // another extension point's: the test answers it with Answer
	}
	// What the real hub refuses before it answers: another extension's contribution, a scope not granted.
	if err := refuses(m, method, params); err != nil {
		return nil, err
	}
	res, err := h.answerOf(f, method, params)
	if err == nil {
		raw, merr := json.Marshal(res)
		if merr == nil {
			h.holds(method+" (the host's answer)", wire.ValidateResult(method, raw))
		}
	}
	return res, err
}

// answerOf is the test's answer (Answer, which replaces the host's own), else the host's own: a connector's secrets in memory, a channel's
// messages kept, and for what needs a hub's data (the knowledge base, a computer's token) the error a hub without
// it answers.
func (h *hub) answerOf(f func(json.RawMessage) (any, error), method string, params json.RawMessage) (any, error) {
	if f != nil {
		return f(params)
	}
	c := ext.Contribution(params)
	switch method {
	case ext.ChannelNamespace + ".receive":
		var p wire.ChannelReceiveParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.received = append(h.received, p)
		h.mu.Unlock()
	case ext.SentNamespace + ".record":
		var p wire.ConnectorSentRecordParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		// As the hub keeps it: 20 keys of the target at most, in key order. (The ledger being down, no_database, is
		// Host.Answer's to simulate.)
		if len(p.Target) > sentTargetKeys {
			keys := make([]string, 0, len(p.Target))
			for k := range p.Target {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys[sentTargetKeys:] {
				delete(p.Target, k)
			}
		}
		h.mu.Lock()
		h.sent = append(h.sent, p)
		h.mu.Unlock()
	case ext.ConnectorNamespace + ".secrets.load":
		h.mu.Lock()
		defer h.mu.Unlock()
		if data, ok := h.secrets[c]; ok {
			return wire.ConnectorSecret{Data: base64.StdEncoding.EncodeToString(data)}, nil
		}
		return wire.ConnectorSecret{}, nil
	case ext.ConnectorNamespace + ".secrets.save":
		var p wire.ConnectorSecretSaveParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.secrets[c] = data
		h.mu.Unlock()
	case ext.ConnectorNamespace + ".secrets.delete":
		h.mu.Lock()
		delete(h.secrets, c)
		h.mu.Unlock()
	}
	switch {
	case strings.HasPrefix(method, "kb."):
		return nil, &ext.Error{Code: wire.KBErrorCodeKbUnavailable, Message: "exttest: no knowledge base here (answer " + method + " with Host.Answer)"}
	case method == ext.ConnectorNamespace+".credentials.gh_token":
		return nil, &ext.Error{Code: wire.ConnectorErrorCodeNoMachine, Message: "exttest: no computer here (answer it with Host.Answer)"}
	case strings.HasPrefix(method, ext.ConnectorNamespace+".posts.") || method == ext.ConnectorNamespace+".archive.chats":
		return nil, &ext.Error{Message: "exttest: no knowledge base here (answer " + method + " with Host.Answer)"}
	}
	return struct{}{}, nil
}

// refuses is the real hub's refusal of a program's call, or nil: a connector or channel the extension does not
// contribute (no_contribution), a knowledge-base call or a gh token without the permission granted
// (permission_denied, data {scope}), a sent.record for a connector that does not declare sends_as_user
// (permission_denied) or with a target that is not an object or no text (invalid_params), the knowledge base's connector calls of a connector without archive
// (no_capability).
func refuses(m *wire.Manifest, method string, params json.RawMessage) error {
	if m == nil {
		return nil
	}
	var perms wire.Permissions
	var connectors []wire.ConnectorItem
	var channels []wire.ChannelItem
	if m.Permissions != nil {
		perms = *m.Permissions
	}
	if m.Contributes != nil {
		connectors, channels = m.Contributes.Connectors, m.Contributes.Channel
	}
	denied := func(scope string) error {
		data, _ := json.Marshal(map[string]string{"scope": scope})
		return &ext.Error{Code: wire.ErrorCodePermissionDenied, Message: method + ": requires " + scope, Data: data}
	}
	c := ext.Contribution(params)
	switch ns, rest, _ := strings.Cut(method, "."); ns {
	case "kb":
		scope := "kb.read"
		if rest == "update" {
			scope = "kb.write"
		}
		// hub_api is the legacy full grant only without scopes (the hub's FullHub).
		if !(perms.HubAPI && len(perms.Hub) == 0) && !slices.Contains(perms.Hub, scope) {
			return denied(scope)
		}
	case ext.SentNamespace:
		// Only a connector of its own that declares sends_as_user (the hub's registry check), with a target that is an
		// object and a text (which may be empty).
		if !slices.ContainsFunc(connectors, func(it wire.ConnectorItem) bool { return it.ID == c && it.SendsAsUser }) {
			return &ext.Error{Code: wire.ErrorCodePermissionDenied, Message: method + ": " + c + " is not a connector that declares sends_as_user"}
		}
		var p struct {
			Target map[string]json.RawMessage `json:"target"`
			Text   *string                    `json:"text"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Target == nil || p.Text == nil {
			return &ext.Error{Code: "invalid_params", Message: method + ": target (an object) and text are required"}
		}
	case ext.ChannelNamespace:
		if c == "" && len(channels) == 1 {
			return nil
		}
		if !slices.ContainsFunc(channels, func(it wire.ChannelItem) bool { return it.ID == c }) {
			return ext.NoContribution(c)
		}
	case ext.ConnectorNamespace:
		i := slices.IndexFunc(connectors, func(it wire.ConnectorItem) bool { return it.ID == c })
		if i < 0 && len(connectors) == 1 {
			i = 0 // the hub routes an extension's call to its only connector, whatever it names
		}
		if i < 0 {
			return ext.NoContribution(c)
		}
		switch {
		case rest == "credentials.gh_token" && !perms.GhLogin:
			return denied("gh_login")
		case (strings.HasPrefix(rest, "posts.") || rest == "archive.chats") &&
			!slices.Contains(connectors[i].Capabilities, wire.ConnectorCapabilityArchive):
			return &ext.Error{Code: wire.ErrorCodeNoCapability, Message: c + " does not declare an archive"}
		}
	}
	return nil
}

// holds notes a message that does not hold to its schema.
func (h *hub) holds(what string, err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.problems = append(h.problems, fmt.Sprintf("%s: does not hold to schema/extension: %v", what, err))
}

func (h *hub) takeProblems() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.problems
	h.problems = nil
	return p
}

func (h *hub) write(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	h.wmu.Lock()
	defer h.wmu.Unlock()
	_, err = h.run.ToProgram.Write(append(b, '\n'))
	return err
}

// notify sends the program an event (event.kb.changed…).
func (h *hub) notify(event string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return h.write(frame{Event: event, Params: raw})
}

func (h *hub) call(ctx context.Context, method string, params, out any) error {
	id := fmt.Sprintf("h%d", h.next.Add(1))
	ch := make(chan frame, 1)
	h.mu.Lock()
	h.pending[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}()
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := h.write(frame{ID: id, Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case f := <-ch:
		if f.Error != nil {
			return f.Error
		}
		if out != nil && len(f.Result) > 0 {
			return json.Unmarshal(f.Result, out)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: no answer: %w", method, ctx.Err())
	case <-h.closed:
		return fmt.Errorf("%s: the program closed its stdout", method)
	}
}
