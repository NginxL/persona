package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/nwverse/persona/server/internal/agentsend"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exercise the terminal client against a real message-operation claim and receipt,
// with only the final Agent delivery replaced by a synthetic handler.
func TestTerminalAgentSendWithPersistedHubReceipt(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	seed := time.Now().UnixNano() & 0xffffffffffff
	id := fmt.Sprintf("12345678-90ab-4cde-8fab-%012x", seed)
	refusalID := fmt.Sprintf("12345678-90ab-4cde-8fab-%012x", seed+1)
	lostRefusalID := fmt.Sprintf("12345678-90ab-4cde-8fab-%012x", seed+2)
	const message = "Synthetic task message"
	const ownToken = "fixture-own-token"
	const otherToken = "fixture-other-token"
	var store Store = &clientOpsStore{ops: map[string]db.ClientOp{}}
	if dsn := os.Getenv("PERSONA_TEST_DATABASE_URL"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		persisted, err := db.Open(ctx, dsn, db.HubSet)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(persisted.Close)
		store = persisted
	}
	h := &Hub{store: store, byID: map[string]*Machine{}}
	node := newMachine(h, MachineConfig{ID: "computer"})
	node.sessions = []proto.Session{
		{MachineID: "computer", SessionID: "conversation", Status: proto.StatusOffline},
		{MachineID: "computer", SessionID: "persona", Assistant: true},
	}
	h.byID["computer"] = node
	var deliveries atomic.Int32
	write := h.withClientOp("message", func(w http.ResponseWriter, r *http.Request) {
		deliveries.Add(1)
		var params struct {
			Text string `json:"text"`
			ID   string `json:"client_msg_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil || params.Text != message {
			t.Error("incorrect dispatch payload", err)
		}
		if params.ID == refusalID || params.ID == lostRefusalID {
			apierr.CallStatus(w, http.StatusServiceUnavailable, &rpc.Error{
				Code: "machine_offline", Message: "computer is away", Data: json.RawMessage(`{"delivered":"none"}`),
			})
			return
		}
		if params.ID != id {
			t.Error("incorrect dispatch ID")
		}
		writeJSON(w, map[string]any{"ok": true, "waits_on_computer": true})
	})
	var posts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		if err := wsjson.Write(r.Context(), ws, map[string]any{"type": "hello", "features": []string{"client_ops", proto.FeatureAgentTerminalSend, featureAgentMessageHold}}); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /api/v1/ops/{id}", h.getClientOp)
	mux.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/agent-messages", h.withTerminalAgentTarget(h.terminalAgentCheck))
	mux.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/agent-messages", h.withTerminalAgentTarget(func(w http.ResponseWriter, r *http.Request) {
		first := posts.Add(1) == 1
		lostRefusal := r.Header.Get("X-Persona-Op-Id") == lostRefusalID
		if !first && !lostRefusal {
			write(w, r)
			return
		}
		// The first receipt is successfully written, but the transport cannot
		// deliver the answer. The terminal command must recover it with one GET.
		rec := httptest.NewRecorder()
		write(rec, r)
		if first && rec.Code != http.StatusOK || lostRefusal && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("synthetic send returned %d", rec.Code)
		}
		if conn, _, err := w.(http.Hijacker).Hijack(); err != nil {
			t.Error(err)
		} else {
			conn.Close()
		}
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var device string
		switch r.Header.Get("Authorization") {
		case "Bearer " + ownToken:
			device = "fixture-own"
		case "Bearer " + otherToken:
			device = "fixture-other"
		default:
			writeErr(w, http.StatusUnauthorized, context.Canceled)
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: device, Role: roleFull})))
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	run := func(verb, token, text string, args ...string) (string, error) {
		t.Helper()
		path := filepath.Join(dir, "token")
		if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := []string{verb, "--hub", server.URL, "--token-file", path, "--json"}
		cmd = append(cmd, args...)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var output bytes.Buffer
		err := agentsend.Run(ctx, cmd, strings.NewReader(text), &output, io.Discard)
		return output.String(), err
	}
	sendArgs := []string{"--request-id", id, "--message-file", "-", "computer", "conversation"}
	out, err := run("send", ownToken, message, sendArgs...)
	if err != nil || !strings.Contains(out, `"state":"queued"`) || posts.Load() != 1 || deliveries.Load() != 1 {
		t.Fatalf("lost reply recovery: out=%s err=%v posts=%d deliveries=%d", out, err, posts.Load(), deliveries.Load())
	}
	out, err = run("send", ownToken, "Changed synthetic task message", sendArgs...)
	if err != agentsend.ErrUnconfirmed || !strings.Contains(out, `"state":"conflict"`) || posts.Load() != 2 || deliveries.Load() != 1 {
		t.Fatalf("same ID with changed input: out=%s err=%v posts=%d deliveries=%d", out, err, posts.Load(), deliveries.Load())
	}
	holdArgs := append([]string{"--hold"}, sendArgs...)
	out, err = run("send", ownToken, message, holdArgs...)
	if err != agentsend.ErrUnconfirmed || !strings.Contains(out, `"state":"conflict"`) || posts.Load() != 3 || deliveries.Load() != 1 {
		t.Fatalf("same ID with changed hold choice: out=%s err=%v posts=%d deliveries=%d", out, err, posts.Load(), deliveries.Load())
	}
	out, err = run("receipt", otherToken, "", id)
	if err != agentsend.ErrUnconfirmed || !strings.Contains(out, `"state":"unavailable"`) || posts.Load() != 3 || deliveries.Load() != 1 {
		t.Fatalf("another device's receipt: out=%s err=%v posts=%d deliveries=%d", out, err, posts.Load(), deliveries.Load())
	}
	for _, request := range []string{refusalID, lostRefusalID} {
		args := []string{"--request-id", request, "--message-file", "-", "computer", "conversation"}
		direct, err := run("send", ownToken, message, args...)
		if err != agentsend.ErrUnconfirmed || !strings.Contains(direct, `"state":"refused"`) || !strings.Contains(direct, `"http_status":503`) {
			t.Fatalf("direct or recovered refusal lost delivery proof: %q %v", direct, err)
		}
		checked, err := run("receipt", ownToken, "", request)
		if err != agentsend.ErrUnconfirmed || direct != checked {
			t.Fatalf("receipt differs from the refusal: %q vs %q (%v)", checked, direct, err)
		}
		ledger, err := store.ClientOp(context.Background(), request)
		if err != nil || ledger == nil || ledger.State != "unknown" {
			t.Fatalf("fixture did not exercise the unknown ledger envelope: %+v %v", ledger, err)
		}
	}
	if posts.Load() != 5 || deliveries.Load() != 3 {
		t.Fatalf("receipt recovered by re-posting: posts=%d deliveries=%d", posts.Load(), deliveries.Load())
	}
	// Persona rows are refused by the read-only Agent check before any
	// request or durable operation uses the ID, with or without offline hold.
	for _, hold := range []bool{false, true} {
		args := []string{"--request-id", id, "--message-file", "-"}
		if hold {
			args = append(args, "--hold")
		}
		args = append(args, "computer", "persona")
		out, err := run("send", ownToken, message, args...)
		if err == nil || out != "" || posts.Load() != 5 || deliveries.Load() != 3 {
			t.Fatalf("Persona target left preflight: out=%s err=%v posts=%d", out, err, posts.Load())
		}
	}
}

func TestTerminalAgentSendRejectsOtherActorsOrTargetsBeforeClaim(t *testing.T) {
	store := &clientOpsStore{ops: map[string]db.ClientOp{}}
	h := &Hub{store: store, byID: map[string]*Machine{}}
	node := newMachine(h, MachineConfig{ID: "computer"})
	node.sessions = []proto.Session{
		{MachineID: "computer", SessionID: "agent", Status: proto.StatusOffline},
		{MachineID: "computer", SessionID: "persona", Assistant: true},
		{MachineID: "computer", SessionID: "archived", Archived: true},
	}
	h.byID["computer"] = node
	handler := h.withTerminalAgentTarget(h.withClientOp("message", func(w http.ResponseWriter, r *http.Request) {
		t.Error("refused operation reached dispatch")
	}))
	for _, tc := range []struct {
		name    string
		actor   principal
		session string
		want    int
	}{
		{"hub token", principal{Role: roleFull}, "agent", 403},
		{"answer credential", principal{Role: roleAnswer, ID: "watch"}, "agent", 403},
		{"extension", principal{Role: roleExtension, ID: "extension"}, "agent", 403},
		{"unknown target", principal{Role: roleFull, ID: "paired"}, "unknown", 404},
		{"Persona target", principal{Role: roleFull, ID: "paired"}, "persona", 404},
		{"archived target", principal{Role: roleFull, ID: "paired"}, "archived", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), principalKey{}, tc.actor)
			for _, hold := range []bool{false, true} {
				path := "/api/v1/sessions/computer/" + tc.session + "/agent-messages"
				body := fmt.Sprintf(`{"text":"sample","hold_when_away":%t}`, hold)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
				req.SetPathValue("mid", "computer")
				req.SetPathValue("sid", tc.session)
				req.Header.Set("X-Persona-Op-Id", "f11a0b01-0000-4000-8000-000000000001")
				if hold {
					req.Header.Set("X-Persona-Features", featureAgentMessageHold)
				}
				rr := httptest.NewRecorder()
				handler(rr, req)
				if rr.Code != tc.want || len(store.ops) != 0 {
					t.Fatalf("code=%d receipts=%d", rr.Code, len(store.ops))
				}
			}
		})
	}
}

type terminalSendLog func(string)

func (f terminalSendLog) Write(p []byte) (int, error) {
	f(string(p))
	return len(p), nil
}

func TestTerminalAgentSendRejectsTargetChangedAfterAdmission(t *testing.T) {
	for _, scenario := range []string{"becomes Persona", "row removed"} {
		t.Run(scenario, func(t *testing.T) {
			store := &clientOpsStore{ops: map[string]db.ClientOp{}}
			h := &Hub{store: store, byID: map[string]*Machine{}}
			m := newMachine(h, MachineConfig{ID: "computer"})
			m.sessions = []proto.Session{{MachineID: "computer", SessionID: "agent", Agent: "claude"}}
			h.byID["computer"] = m
			var changed bool
			h.logger = log.New(terminalSendLog(func(line string) {
				if strings.Contains(line, "phase=hub_received ") && !changed {
					// The outer Agent guard, durable claim and request body
					// checks ran. Execution has not chosen its routing row.
					m.mu.Lock()
					if scenario == "row removed" {
						m.sessions = nil
					} else {
						m.sessions[0].Assistant = true
					}
					m.mu.Unlock()
					changed = true
				}
				if strings.Contains(line, "phase=dispatch_started ") {
					t.Error("message reached delivery after its Agent identity changed")
				}
			}), "", 0)
			const id = "f11a0b01-0000-4000-8000-000000000002"
			ctx := context.WithValue(context.Background(), principalKey{}, principal{ID: "paired", Role: roleFull})
			req := httptest.NewRequest("POST", "/api/v1/sessions/computer/agent/agent-messages",
				strings.NewReader(`{"text":"synthetic text","hold_when_away":true}`)).WithContext(ctx)
			req.SetPathValue("mid", "computer")
			req.SetPathValue("sid", "agent")
			req.Header.Set("X-Persona-Op-Id", id)
			req.Header.Set("X-Persona-Features", featureAgentMessageHold)
			rr := httptest.NewRecorder()
			h.withTerminalAgentTarget(h.withClientOp("message", h.withMachine(h.sendMessage)))(rr, req)
			op, err := store.ClientOp(ctx, id)
			if !changed || rr.Code != 404 || !strings.Contains(rr.Body.String(), `"delivered":"none"`) ||
				!strings.Contains(rr.Body.String(), "agent_target_required") || err != nil || op == nil {
				t.Fatalf("wanted a refusal after admission and before routing: changed=%t status=%d body=%s op=%v (%v)",
					changed, rr.Code, rr.Body, op, err)
			}
		})
	}
}
