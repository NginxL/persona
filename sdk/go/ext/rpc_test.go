package ext

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// rawHub is the hub's end of the pipes, frame by frame: it sees the program's calls and events as well as its
// answers, and answers the program's calls itself.
type rawHub struct {
	t      *testing.T
	in     io.WriteCloser
	frames chan frame
	done   chan error
}

func startRaw(t *testing.T, prog Program) *rawHub {
	t.Helper()
	progR, hubW := io.Pipe()
	hubR, progW := io.Pipe()
	h := &rawHub{t: t, in: hubW, frames: make(chan frame, 64), done: make(chan error, 1)}
	go func() { err := Serve(context.Background(), progR, progW, prog); _ = progW.Close(); h.done <- err }()
	go func() {
		sc := bufio.NewScanner(hubR)
		for sc.Scan() {
			var f frame
			if json.Unmarshal(sc.Bytes(), &f) == nil {
				h.frames <- f
			}
		}
		close(h.frames)
	}()
	t.Cleanup(func() { hubW.Close() })
	return h
}

// send writes lines as they are, in one write (so the program reads them back to back).
func (h *rawHub) send(lines ...string) {
	h.t.Helper()
	if _, err := h.in.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		h.t.Fatal(err)
	}
}

func (h *rawHub) next() frame {
	h.t.Helper()
	select {
	case f, ok := <-h.frames:
		if !ok {
			h.t.Fatal("the program closed its output")
		}
		return f
	case <-time.After(5 * time.Second):
		h.t.Fatal("nothing from the program")
	}
	return frame{}
}

// answers reads n answers, by id.
func (h *rawHub) answers(n int) map[string]frame {
	h.t.Helper()
	got := map[string]frame{}
	for len(got) < n {
		if f := h.next(); f.ID != "" && f.Method == "" {
			got[f.ID] = f
		}
	}
	return got
}

const initLine = `{"id":"i","method":"initialize","params":{"protocol":1,"hub":{"build":1},"granted":{}}}`

// startWithHub serves a program that keeps the Hub its Init is given, initialized.
func startWithHub(t *testing.T, prog Program) (*rawHub, *Hub) {
	t.Helper()
	hubs := make(chan *Hub, 1)
	prog.Init = func(ctx context.Context, in *Init, hub *Hub) (map[string]any, error) {
		hubs <- hub
		return nil, nil
	}
	h := startRaw(t, prog)
	h.send(initLine)
	if f := h.answers(1)["i"]; f.Error != nil {
		t.Fatalf("initialize: %+v", f.Error)
	}
	return h, <-hubs
}

func TestHubCall(t *testing.T) {
	h, hub := startWithHub(t, Program{ID: "example", Version: "1.0.0"})

	type result struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	var got result
	errs := make(chan error, 1)
	go func() {
		errs <- hub.Call(context.Background(), "quota.http", map[string]string{"url": "https://example.com"}, &got)
	}()
	f := h.next()
	if f.Method != "quota.http" || f.ID == "" || string(f.Params) != `{"url":"https://example.com"}` {
		t.Fatalf("the call on the wire: %+v", f)
	}
	h.send(`{"id":"` + f.ID + `","result":{"status":200,"body":"好"}}`)
	if err := <-errs; err != nil || got != (result{200, "好"}) {
		t.Fatalf("Call: %v %+v", err, got)
	}

	// An error frame is an *Error with its code and data.
	go func() { errs <- hub.Call(context.Background(), "connector.secrets.load", nil, nil) }()
	f = h.next()
	if f.Method != "connector.secrets.load" || string(f.Params) != "null" {
		t.Fatalf("the second call on the wire: %+v", f)
	}
	h.send(`{"id":"` + f.ID + `","error":{"message":"no such secret","code":"not_found","data":{"key":"token"}}}`)
	var e *Error
	if err := <-errs; !errors.As(err, &e) || e.Code != "not_found" || e.Error() != "no such secret" || string(e.Data) != `{"key":"token"}` {
		t.Fatalf("an error answer: %#v", err)
	}

	// A context that has ended does not write anything.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hub.Call(ctx, "quota.http", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ended context: %v", err)
	}

	// A call pending when the hub goes returns ErrClosed, and so does one after.
	go func() { errs <- hub.Call(context.Background(), "quota.http", nil, nil) }()
	if f := h.next(); f.Method != "quota.http" {
		t.Fatalf("the pending call: %+v", f)
	}
	h.in.Close()
	if err := <-errs; !errors.Is(err, ErrClosed) {
		t.Fatalf("pending when the hub went: %v", err)
	}
	if err := <-h.done; err != io.EOF {
		t.Fatalf("Serve after the hub went: %v", err)
	}
	if err := hub.Call(context.Background(), "quota.http", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("after the hub went: %v", err)
	}
	if err := hub.Notify("connector.changed", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("an event after the hub went: %v", err)
	}
}

// A frame without a result or a non-null error is not an answer; its ID stays pending for the real answer.
func TestHubCallIgnoresIncompleteReplies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields string
	}{
		{"id_only", ""},
		{"null_error", `,"error":null`},
		{"params_only", `,"params":{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, hub := startWithHub(t, Program{ID: "example", Version: "1.0.0"})
			var got struct {
				Value string `json:"value"`
			}
			errs := make(chan error, 1)
			go func() { errs <- hub.Call(context.Background(), "fixture.read", nil, &got) }()
			f := h.next()
			h.send(`{"id":"` + f.ID + `"` + tc.fields + `}`)
			select {
			case err := <-errs:
				t.Fatalf("incomplete reply completed Call: err=%v, zero-value result=%+v", err, got)
			case <-time.After(50 * time.Millisecond):
			}

			h.send(`{"id":"` + f.ID + `","result":{"value":"ok"}}`)
			select {
			case err := <-errs:
				if err != nil || got.Value != "ok" {
					t.Fatalf("valid reply after incomplete reply: err=%v, result=%+v", err, got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("valid reply after incomplete reply left Call pending")
			}
		})
	}
}

func TestHubCallAcceptsNullAndErrorReplies(t *testing.T) {
	h, hub := startWithHub(t, Program{ID: "example", Version: "1.0.0"})
	errs := make(chan error, 1)
	var got any = "sentinel"
	go func() { errs <- hub.Call(context.Background(), "fixture.read", nil, &got) }()
	f := h.next()
	h.send(`{"id":"` + f.ID + `","result":null}`)
	select {
	case err := <-errs:
		if err != nil || got != nil {
			t.Fatalf("explicit null result: err=%v, result=%#v", err, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("explicit null result left Call pending")
	}

	go func() { errs <- hub.Call(context.Background(), "fixture.read", nil, nil) }()
	f = h.next()
	h.send(`{"id":"` + f.ID + `","error":{"message":"denied","code":"conflict"}}`)
	var e *Error
	select {
	case err := <-errs:
		if !errors.As(err, &e) || e.Message != "denied" || e.Code != "conflict" {
			t.Fatalf("valid error reply: %#v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("valid error reply left Call pending")
	}
}

func TestHubEvents(t *testing.T) {
	h, hub := startWithHub(t, Program{ID: "example", Version: "1.0.0"})
	for _, c := range []struct {
		send          func() error
		event, params string
	}{
		{func() error { return hub.Notify("connector.changed", map[string]string{"contribution": "mail"}) }, "connector.changed", `{"contribution":"mail"}`},
		{func() error { return hub.Badge("inbox", "3", "accent") }, "ui.badge", `{"contribution":"inbox","text":"3","tone":"accent"}`},
		{func() error { return hub.Badge("inbox", "", "") }, "ui.badge", `{"contribution":"inbox"}`},
		{func() error { return hub.Invalidate("home") }, "ui.invalidate", `{"contribution":"home"}`},
		{func() error { return hub.Invalidate("") }, "ui.invalidate", `{}`},
	} {
		if err := c.send(); err != nil {
			t.Fatal(err)
		}
		f := h.next()
		if f.Event != c.event || string(f.Params) != c.params || f.ID != "" || f.Method != "" {
			t.Fatalf("%s: %+v (params %s)", c.event, f, f.Params)
		}
	}
}

// An event the hub tells goes to its namespace's handler, once the program is initialized.
func TestEvents(t *testing.T) {
	type got struct{ method, contribution, n string }
	events := make(chan got, 8)
	h := startRaw(t, Program{ID: "example", Version: "1.0.0", Namespaces: map[string]Handler{
		"quota": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			var p struct {
				N string `json:"n"`
			}
			_ = json.Unmarshal(params, &p)
			events <- got{method, Contribution(params), p.N}
			return nil, nil
		},
	}})
	h.send(`{"event":"quota.x","params":{"contribution":"q","n":"before"}}`)
	h.send(initLine)
	if f := h.answers(1)["i"]; f.Error != nil {
		t.Fatalf("initialize: %+v", f.Error)
	}
	h.send(
		`{"event":"quota.x","params":{"contribution":"q","n":"after"}}`,
		`{"event":"other.x","params":{}}`,
		`{"event":"quota","params":{}}`,
		`{"id":"p","method":"ping"}`,
	)
	if f := h.answers(1)["p"]; f.Error != nil {
		t.Fatalf("ping: %+v", f.Error)
	}
	select {
	case e := <-events:
		if e != (got{"x", "q", "after"}) {
			t.Fatalf("the event: %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event after initialize did not arrive")
	}
	time.Sleep(50 * time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("more events than the one after initialize: %+v", <-events)
	}
	if c := Contribution(json.RawMessage(`not json`)); c != "" {
		t.Fatalf("Contribution of what is not JSON: %q", c)
	}
}

// A call sent right behind initialize, before its answer, waits for it (as in the TS SDK); one sent before
// initialize, or behind one that fails, is not initialized.
func TestPipelinedInitialize(t *testing.T) {
	slowInit := func(ctx context.Context, in *Init, hub *Hub) (map[string]any, error) {
		time.Sleep(time.Millisecond)
		return nil, nil
	}
	for i := 0; i < 50; i++ {
		h := startRaw(t, Program{ID: "example", Version: "1.0.0", Init: slowInit})
		h.send(initLine, `{"id":"p","method":"ping"}`)
		got := h.answers(2)
		if got["i"].Error != nil || got["p"].Error != nil || string(got["p"].Result) != "{}" {
			t.Fatalf("run %d: initialize %+v, ping %+v", i, got["i"].Error, got["p"].Error)
		}
		h.in.Close()
	}

	h := startRaw(t, Program{ID: "example", Version: "1.0.0", Init: slowInit})
	h.send(`{"id":"p0","method":"ping"}`)
	if f := h.answers(1)["p0"]; f.Error == nil || f.Error.Code != wire.ErrorCodeNotInitialized {
		t.Fatalf("ping before initialize: %+v", f)
	}
	h.send(`{"id":"i","method":"initialize","params":{"protocol":2,"hub":{"build":1},"granted":{}}}`, `{"id":"p","method":"ping"}`)
	got := h.answers(2)
	if f := got["i"]; f.Error == nil || f.Error.Code != wire.ErrorCodeWrongProtocol {
		t.Fatalf("initialize of another protocol: %+v", f)
	}
	if f := got["p"]; f.Error == nil || f.Error.Code != wire.ErrorCodeNotInitialized {
		t.Fatalf("ping behind a failed initialize: %+v", f)
	}
	h.send(initLine, `{"id":"p2","method":"ping"}`)
	if got := h.answers(2); got["i"].Error != nil || got["p2"].Error != nil {
		t.Fatalf("initialize again: %+v %+v", got["i"].Error, got["p2"].Error)
	}
}

// An app before UI version 3 gets a result's push as the pushed page and a pop as the page first drawn, with the
// result's toast; push ids in a view the action answers are the SDK's.
func TestOlderResult(t *testing.T) {
	render := func(ctx context.Context, r *UIRequest) (*wire.View, error) {
		title := r.Contribution + ":" + r.State
		return &wire.View{Title: title, Sections: []wire.Section{}}, nil
	}
	c := start(t, Program{ID: "example", Version: "1.0.0", UI: map[string]Page{
		"detail": {Render: render},
		"home": {Render: render, Action: func(ctx context.Context, r *UIRequest) (*wire.ActionResult, error) {
			switch r.Action {
			case "push":
				return &wire.ActionResult{Push: &wire.Push{State: `"7"`}, Toast: "开了"}, nil
			case "push-other":
				return &wire.ActionResult{Push: &wire.Push{UI: "detail", State: `"8"`}}, nil
			case "pop":
				return &wire.ActionResult{Pop: true, Toast: "存好了"}, nil
			case "view":
				return &wire.ActionResult{View: &wire.View{Title: "新的", Sections: []wire.Section{{Items: []wire.Component{
					{Type: wire.ComponentRow, Title: "第一条", Action: Open("open", "detail", "1", "第一条")},
				}}}}}, nil
			}
			return &wire.ActionResult{OpenURL: "https://example.com"}, nil
		}},
	}})
	c.call("initialize", map[string]any{"protocol": 1, "hub": map[string]any{"build": 1}, "granted": map[string]any{}})
	action := func(id string) wire.ActionResult {
		t.Helper()
		var res wire.ActionResult
		f := c.call("ui.action", wire.UIParams{Contribution: "home", UIVersion: 2, Action: id, State: `"s"`})
		if f.Error != nil || json.Unmarshal(f.Result, &res) != nil {
			t.Fatalf("%s: %s %+v", id, f.Result, f.Error)
		}
		return res
	}
	if res := action("push"); res.Push != nil || res.View == nil || res.View.Title != `home:"7"` || res.Toast != "开了" {
		t.Fatalf("a push to this page: %+v", res)
	}
	if res := action("push-other"); res.Push != nil || res.View == nil || res.View.Title != `detail:"8"` {
		t.Fatalf("a push to another page: %+v", res)
	}
	if res := action("pop"); res.Pop || res.View == nil || res.View.Title != "home:" || res.Toast != "存好了" {
		t.Fatalf("a pop: %+v", res)
	}
	res := action("view")
	id := res.View.Sections[0].Items[0].Action.ID
	if !strings.HasPrefix(id, pushPrefix) {
		t.Fatalf("a push in the view answered: %q", id)
	}
	if res := action(id); res.View == nil || res.View.Title != `detail:"1"` {
		t.Fatalf("tapping it: %+v", res)
	}
	if res := action("url"); res.OpenURL != "https://example.com" || res.View != nil {
		t.Fatalf("a result with neither push nor pop: %+v", res)
	}
}
