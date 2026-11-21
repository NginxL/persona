package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A body's strings are read as JSON reads them, keys and values, in every value of the body: a control character
// other than newline, carriage return and tab in any of them is found; a body that is not JSON, or is cut short, is
// an error.
func TestTerminalControls(t *testing.T) {
	str := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	for _, c := range []struct {
		name, body string
		found      bool
	}{
		{"plain text", `{"text":"carry on with the plan"}`, false},
		{"CJK", `{"text":"继续，按计划来"}`, false},
		{"emoji", `{"text":"done 🎉"}`, false},
		{"an escaped emoji", `{"text":"\ud83d\ude00"}`, false},
		{"newline, carriage return and tab", `{"text":"a\nb\r\nc\td"}`, false},
		{"numbers, booleans and null", `{"n":1.5,"ok":true,"none":null,"list":[1,2]}`, false},
		{"no body", ``, false},
		{"two values", `{"text":"a"} {"text":"b"}`, false},
		{"escape", `{"text":"\u001b"}`, true},
		{"an escape sequence", `{"text":` + str("go on\x1b[0m") + `}`, true},
		{"DEL", `{"text":` + str("a\x7fb") + `}`, true},
		{"DEL as it is", "{\"text\":\"a\x7fb\"}", true},
		{"U+009B", `{"text":"\u009b"}`, true},
		{"U+009B as it is", `{"text":"` + "\u009b" + `"}`, true},
		{"NUL", `{"text":"\u0000"}`, true},
		{"a key spelled in another case", `{"Text":"\u001b"}`, true},
		{"a key", `{"\u001b":"x"}`, true},
		{"deep in the body", `{"attachments":[{"name":"a\u001bb"}]}`, true},
		{"a second value", `{"text":"ok"} {"text":"\u001b"}`, true},
		{"a body that is a string", `"\u001b"`, true},
	} {
		found, err := terminalControls([]byte(c.body))
		if err != nil || found != c.found {
			t.Errorf("%s: %v %v, want %v", c.name, found, err, c.found)
		}
	}
	for _, body := range []string{`{"text":"a`, `{"text":"ok"`, `[`, `{"text":"ok"} x`, "{\"text\":\"\x1b\"}", `{"a" "b"}`} {
		if found, err := terminalControls([]byte(body)); err == nil {
			t.Errorf("%q: read (%v)", body, found)
		}
	}
	for r := rune(0); r <= 0x2000; r++ {
		want := (r < 0x20 || r >= 0x7f && r <= 0x9f) && r != '\n' && r != '\r' && r != '\t'
		if isTerminalControl(r) != want || want != (unicode.IsControl(r) && !strings.ContainsRune("\n\r\t", r)) {
			t.Errorf("U+%04X: %v, want %v", r, isTerminalControl(r), want)
		}
	}
}

// An extension's token may not send terminal control characters on any route that puts what it sends into an agent's
// terminal or keeps it for an agent's prompt, not even to a chat its extension opened: refused before the machine or
// the manager hears of it (a todo or an entry refused is not written), and recorded in the extension's audit with the
// route and path, never the text. The app's and a phone's tokens send the same bodies as before, and an extension's
// plain text goes through on every one of those routes.
func TestExtensionTokensMayNotSendTerminalControls(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	scoped := extTokenPrefix + "bx-token"
	scopes := []string{extension.ScopeAgentsControl, extension.ScopeAgentsPermissionMode, extension.ScopeTodosRead,
		extension.ScopeTodosWrite, extension.ScopeKBRead, extension.ScopeKBWrite}
	var mu sync.Mutex
	kept := map[string][]string{} // B's todos, decisions and entries: the path that reads them → the bodies written
	answer := func(method string, params json.RawMessage) (any, error) {
		if method == extwire.MethodAuthorize {
			var a extwire.Authorize
			_ = json.Unmarshal(params, &a)
			if a.Hash != hashToken(scoped) {
				return extwire.Authorized{}, nil
			}
			ok, scope := fakeScopeOpens(scopes, a.Pattern, a.Method, a.Path)
			return extwire.Authorized{OK: ok, Extension: "bx", Name: "BX", Scopes: scopes, Scope: scope}, nil
		}
		if method == assistantwire.MethodHTTP {
			var req assistantwire.HTTPRequest
			_ = json.Unmarshal(params, &req)
			record := req.Path
			if strings.HasPrefix(record, "/api/v1/todos/") && !strings.HasSuffix(record, "/decisions") {
				record = "/api/v1/todos"
			}
			mu.Lock()
			defer mu.Unlock()
			if req.Method != http.MethodGet {
				kept[record] = append(kept[record], string(req.Body))
				return map[string]any{"status": 200, "body": []byte(`{"ok":true}`)}, nil
			}
			items := []map[string]string{}
			for i, body := range kept[record] {
				items = append(items, map[string]string{"id": fmt.Sprintf("b-%d", i), "body": body})
			}
			body, _ := json.Marshal(map[string]any{"todos": items})
			return map[string]any{"status": 200, "body": body}, nil
		}
		return map[string]any{}, nil
	}
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, _ json.RawMessage) (any, error) {
		if method == assistantwire.MethodHTTP {
			return map[string]any{"status": 200, "body": []byte(`{"ok":true}`)}, nil
		}
		return map[string]any{}, nil
	})
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answer)
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "mb", func() bool { return h.localMachine("mb") != nil })
	var res assistantwire.SnapshotResult
	if err := b.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "b", Seq: 1, Sessions: []assistantwire.SessionView{
		{SessionID: "b1", MachineID: "mb", Origin: "extension", OriginRef: "bx", OpenedBy: "bx"},
	}}, &res); err != nil {
		t.Fatal(err)
	}
	if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: proto.Interaction{
		ID: "ask_own", MachineID: "mb", SessionID: "b1", Kind: proto.IxQuestion}}); err != nil {
		t.Fatal(err)
	}
	// Controls need the conversation in its machine's list (extensionControllable).
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "b1", Cwd: "/work/project"}}})
	eventually(t, 5*time.Second, "B's chat and card", func() bool {
		_, v := h.conversation("b1")
		_, pending := h.pendingInteraction("ask_own")
		_, listed := h.localMachine("mb").session("b1")
		return v.OpenedBy == "bx" && pending && listed
	})

	do := func(tok, method, path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	// What reached B: its machine's session calls and the requests relayed to it.
	reached := func() int {
		n := 0
		for _, l := range strings.Split(b.log(), "\n") {
			if strings.Contains(l, `"method":"session.`) || strings.HasPrefix(l, assistantwire.MethodHTTP+" ") {
				n++
			}
		}
		return n
	}
	audits := func() []string {
		var out []string
		for _, l := range strings.Split(b.log(), "\n") {
			if strings.Contains(l, `"method":"audit"`) {
				out = append(out, l)
			}
		}
		return out
	}

	str := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	const refused = "go\x1b[0m"
	post, patch, put := http.MethodPost, http.MethodPatch, http.MethodPut
	routes := []struct{ name, method, path, body string }{ // body: the text goes in place of %s
		{"starting a chat", post, "/api/v1/sessions", `{"machine_id":"mb","prompt":%s}`},
		{"a message", post, "/api/v1/sessions/mb/b1/messages", `{"text":%s}`},
		{"a message's key in another case", post, "/api/v1/sessions/mb/b1/messages", `{"Text":%s}`},
		{"a message in a second value", post, "/api/v1/sessions/mb/b1/messages", `{"text":"go"} {"text":%s}`},
		{"editing the queue", post, "/api/v1/sessions/mb/b1/queue", `{"keep":[0],"note":%s}`},
		{"sending the queue now", post, "/api/v1/sessions/mb/b1/queue/send-now", `{"note":%s}`},
		{"renaming", post, "/api/v1/sessions/mb/b1/rename", `{"title":%s}`},
		{"answering a card", post, "/api/v1/interactions/ask_own/resolve", `{"text":%s}`},
		{"changing an answer", post, "/api/v1/interactions/ask_own/reanswer", `{"answers":{"q":%s}}`},
		{"adding a todo", post, "/api/v1/todos", `{"title":%s}`},
		{"editing a todo", patch, "/api/v1/todos/t1", `{"notes":%s}`},
		{"a todo's decision", post, "/api/v1/todos/t1/decisions", `{"question":"go on?","choice":%s}`},
		{"a knowledge-base entry", put, "/api/v1/kb/projects/p1", `{"summary":%s}`},
	}
	// What B keeps of the todo and knowledge-base routes, read back through the hub.
	keptOf := func(path string) []string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+scoped)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Todos []struct{ Body string } `json:"todos"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %v", path, resp.StatusCode, err)
		}
		var bodies []string
		for _, td := range out.Todos {
			bodies = append(bodies, td.Body)
		}
		return bodies
	}
	records := []string{"/api/v1/todos", "/api/v1/todos/t1/decisions", "/api/v1/kb/projects/p1"}
	for i, c := range routes {
		before := reached()
		code, out := do(scoped, c.method, c.path, fmt.Sprintf(c.body, str(refused)))
		if code != http.StatusForbidden || out["code"] != codeTerminalControls {
			t.Fatalf("%s: %d %v, want 403 %s", c.name, code, out, codeTerminalControls)
		}
		if reached() != before {
			t.Fatalf("%s: B heard of it:\n%s", c.name, b.log())
		}
		all := audits()
		if len(all) != i+1 {
			t.Fatalf("%s: %d audits for %d refusals", c.name, len(all), i+1)
		}
		l := all[i]
		if !strings.Contains(l, `"extension":"bx","action":"terminal_control_refused"`) ||
			!strings.Contains(l, `"path":"`+c.path+`"`) || !strings.Contains(l, `"route":"`) ||
			strings.Contains(l, "[0m") || strings.Contains(l, `\u001b`) {
			t.Fatalf("%s: the audit %s", c.name, l)
		}
	}
	// In the path or the query: a route keeps text from those too.
	inURL := []struct{ name, method, path, body string }{
		{"a knowledge-base entry's id", put, "/api/v1/kb/projects/%1Bp1", `{}`},
		{"a todo's id", patch, "/api/v1/todos/t%C2%9B1", `{"notes":"go"}`},
		{"a query's value", post, "/api/v1/sessions/mb/b1/messages?a=%1B", `{"text":"go"}`},
		{"a query's key", post, "/api/v1/todos?a%7F=1", `{"title":"go"}`},
	}
	for i, c := range inURL {
		code, out := do(scoped, c.method, c.path, c.body)
		if code != http.StatusForbidden || out["code"] != codeTerminalControls {
			t.Fatalf("%s: %d %v, want 403 %s", c.name, code, out, codeTerminalControls)
		}
		all := audits()
		if len(all) != len(routes)+i+1 {
			t.Fatalf("%s: the audits %q", c.name, all)
		}
		if l := all[len(routes)+i]; !strings.Contains(l, `"path":"`+strings.Split(c.path, "?")[0]+`"`) ||
			strings.ContainsFunc(l, isTerminalControl) || strings.Contains(l, `\u001b`) || strings.Contains(l, `\u009b`) ||
			strings.Contains(l, `\u007f`) {
			t.Fatalf("%s: the audit %s", c.name, l)
		}
	}
	// Not JSON: refused here, even where the route would read the first value and leave the rest.
	for _, c := range []struct{ path, body string }{
		{"/api/v1/sessions/mb/b1/messages", `{"text":"go`},
		{"/api/v1/sessions/mb/b1/queue", `{"note":"go"} x`},
	} {
		if code, out := do(scoped, post, c.path, c.body); code != http.StatusBadRequest {
			t.Fatalf("%s %s: %d %v", c.path, c.body, code, out)
		}
	}
	if reached() != 0 || len(audits()) != len(routes)+len(inURL) {
		t.Fatalf("refused, B heard of:\n%s", b.log())
	}
	if got := keptOf("/api/v1/kb/projects/%1Bp1"); len(got) != 0 {
		t.Fatalf("refused, the entry has %q", got)
	}
	for _, path := range records {
		if got := keptOf(path); len(got) != 0 {
			t.Fatalf("refused, %s has %q", path, got)
		}
	}

	_, phone := relayDevice(t, srv, h.cfg.Token, "iPhone", "phone")
	for who, tok := range map[string]string{"the app": h.cfg.Token, "a phone": phone} {
		for _, c := range routes {
			if code, out := do(tok, c.method, c.path, fmt.Sprintf(c.body, str(refused))); code != http.StatusOK {
				t.Errorf("%s, %s: %d %v", who, c.name, code, out)
			}
		}
		for _, c := range inURL {
			if code, out := do(tok, c.method, c.path, c.body); code != http.StatusOK {
				t.Errorf("%s, %s: %d %v", who, c.name, code, out)
			}
		}
	}
	if len(audits()) != len(routes)+len(inURL) {
		t.Fatalf("the app's and the phone's requests were audited:\n%s", b.log())
	}
	for _, c := range routes {
		for _, text := range []string{"carry on", "继续 🎉", "line one\nline two\r\n\tindented"} {
			before := reached()
			if code, out := do(scoped, c.method, c.path, fmt.Sprintf(c.body, str(text))); code != http.StatusOK || reached() == before {
				t.Fatalf("the extension, %s, %q: %d %v", c.name, text, code, out)
			}
		}
	}
	if len(audits()) != len(routes)+len(inURL) {
		t.Fatalf("its plain text was audited:\n%s", b.log())
	}
	for _, path := range records {
		if got := keptOf(path); !slices.ContainsFunc(got, func(body string) bool { return strings.Contains(body, "carry on") }) {
			t.Fatalf("its plain text, %s has %q", path, got)
		}
	}
}

// A message to a personal Agent goes to the manager the same way: an extension's token with terminal control
// characters is refused before it is relayed and audited; the app's token and a phone's with the same body are
// relayed, and so is an extension's plain text.
func TestExtensionMessagesToPersonalAgentsHaveNoTerminalControls(t *testing.T) {
	h, _, fx, srv := relayTestHub(t)
	tok := fx.issue("bx", "BX", []string{extension.ScopeAgentsControl})
	_, phone := relayDevice(t, srv, h.cfg.Token, "iPhone", "phone")
	var mu sync.Mutex
	var audits []string
	fx.on(extwire.HubAudit, func(params json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		audits = append(audits, string(params))
		return map[string]any{}, nil
	})
	audited := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), audits...)
	}
	const path = "/api/v1/agents/sessions/src%3Ak1/messages"
	bad, _ := json.Marshal(map[string]string{"text": "go\x1b[0m"})
	if code, out := call(t, srv.Config.Handler, "POST", path, tok, string(bad)); code != http.StatusForbidden || out["code"] != codeTerminalControls {
		t.Fatalf("the extension: %d %v", code, out)
	}
	if a := audited(); len(a) != 1 || !strings.Contains(a[0], `"action":"terminal_control_refused"`) ||
		!strings.Contains(a[0], `"path":"`+path+`"`) || strings.Contains(a[0], "[0m") {
		t.Fatalf("audited %q", a)
	}
	for who, by := range map[string]string{"the app": h.cfg.Token, "a phone": phone} {
		if code, out := call(t, srv.Config.Handler, "POST", path, by, string(bad)); code != http.StatusOK {
			t.Fatalf("%s: %d %v", who, code, out)
		}
	}
	if code, out := call(t, srv.Config.Handler, "POST", path, tok, `{"text":"carry on\n\tplease"}`); code != http.StatusOK {
		t.Fatalf("the extension's plain text: %d %v", code, out)
	}
	if a := audited(); len(a) != 1 {
		t.Fatalf("audited %q", a)
	}
}
