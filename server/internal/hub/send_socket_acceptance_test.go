package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type socketSendTimes struct {
	mu      sync.Mutex
	byTrace map[string]map[string]time.Time
	byKind  map[string]map[string]time.Time
}

func (s *socketSendTimes) mark(trace, phase string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byTrace[trace] == nil {
		s.byTrace[trace] = map[string]time.Time{}
	}
	s.byTrace[trace][phase] = at
}

func (s *socketSendTimes) kind(kind, phase string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byKind[kind] == nil {
		s.byKind[kind] = map[string]time.Time{}
	}
	s.byKind[kind][phase] = at
}

type socketSendLog struct {
	times  *socketSendTimes
	source string
}

func (w socketSendLog) Write(raw []byte) (int, error) {
	fields := strings.Fields(string(raw))
	if len(fields) >= 4 && fields[0] == "send_timing" {
		trace := strings.TrimPrefix(fields[1], "trace=")
		phase := strings.TrimPrefix(fields[2], "phase=")
		w.times.mark(trace, w.source+"."+phase, time.Now())
	}
	return len(raw), nil
}

// Delegate every guard to real Postgres, measuring SQL separately from HTTP and
// JSON decoding. This is not a fake Store or an admission bypass.
type socketSendStore struct {
	*db.DB
	times *socketSendTimes
}

func (s socketSendStore) BeginClientOp(ctx context.Context, op db.ClientOp) (*db.ClientOp, bool, error) {
	trace := proto.SendTraceID(op.ID)
	s.times.mark(trace, "sql.admission_start", time.Now())
	out, claimed, err := s.DB.BeginClientOp(ctx, op)
	s.times.mark(trace, "sql.admission_done", time.Now())
	return out, claimed, err
}
func (s socketSendStore) ClaimSend(ctx context.Context, id, machine, session string) (string, error) {
	trace := proto.SendTraceID(id)
	s.times.mark(trace, "sql.send_claim_start", time.Now())
	out, err := s.DB.ClaimSend(ctx, id, machine, session)
	s.times.mark(trace, "sql.send_claim_done", time.Now())
	return out, err
}
func (s socketSendStore) SettleSend(ctx context.Context, id, state string) error {
	trace := proto.SendTraceID(id)
	s.times.mark(trace, "sql.send_settle_start", time.Now())
	err := s.DB.SettleSend(ctx, id, state)
	s.times.mark(trace, "sql.send_settle_done", time.Now())
	return err
}

// Opt-in acceptance probe: real HTTP and WS sockets, a real manager Service on
// its WS bus and real Postgres guards, plus a separate production Node-handler
// process connected by stdio. That process uses only its fake CLI/fake herdr
// socket. Click is the client HTTP invocation; this does not measure UI paint.
func TestSendReceiptOverHTTPAndWebSocket(t *testing.T) {
	binary := os.Getenv("PERSONA_SEND_SOCKET_NODE")
	before := os.Getenv("PERSONA_SEND_SOCKET_BEFORE") != ""
	if binary == "" {
		t.Skip("build internal/node with go test -c; set PERSONA_SEND_SOCKET_NODE and isolated PERSONA_TEST_DATABASE_URL")
	}
	// dbtest normally favors async commits for test speed. This latency probe
	// restores synchronous commits only in its disposable database before either
	// real pool opens, so admission measurements include the WAL commit wait.
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PERSONA_TEST_DATABASE_URL is required")
	}
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := admin.Config().Database
	if !strings.HasPrefix(name, "persona_test_") {
		_ = admin.Close(context.Background())
		t.Fatal("socket probe requires dbtest's own disposable database")
	}
	_, err = admin.Exec(context.Background(), "ALTER DATABASE "+pgx.Identifier{name}.Sanitize()+" SET synchronous_commit=on")
	_ = admin.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h, original, store := newBusHub(t)
	original.Close() // replace only the HTTP listener wrapper, retaining the same real routes
	times := &socketSendTimes{byTrace: map[string]map[string]time.Time{}, byKind: map[string]map[string]time.Time{}}
	h.logger = log.New(socketSendLog{times: times, source: "hub"}, "", 0)
	h.store = socketSendStore{DB: store, times: times}
	routes := h.routes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages") {
			times.mark(proto.SendTraceID(r.Header.Get("X-Persona-Op-Id")), "http.received", time.Now())
		}
		routes.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestSendSocketNodeProcess$", "-test.timeout=5m")
	cmd.Env = append(os.Environ(), "PERSONA_SEND_SOCKET_DIR="+dir)
	// The node's fake screen fixtures, next to this package. go test runs in the package directory; runtime.Caller's
	// file is module-relative under -trimpath (#10614).
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = filepath.Join(wd, "..", "node")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = in.Close()
		select {
		case <-wait:
		case <-time.After(5 * time.Second):
			cancel()
			<-wait
		}
	})
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			_, _ = (socketSendLog{times: times, source: "node"}).Write(sc.Bytes())
		}
	}()
	m := newMachine(h, MachineConfig{ID: "box", Name: "Test computer", Kind: "local"})
	peer := rpc.New("socket-probe", in)
	peer.OnEvent = func(event string, raw json.RawMessage) {
		if event == "probe.stage" {
			var p struct {
				Kind, Phase string
				At          time.Time
			}
			if json.Unmarshal(raw, &p) == nil {
				times.kind(p.Kind, p.Phase, p.At)
			}
			return
		}
		if event == "session.delta" || event == "session.items" || event == "session.live" {
			m.onEvent(event, raw)
		}
	}
	peer.OnRequest = func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		if method == "assistant.context" && !strings.Contains(string(raw), `"ack"`) {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nodeAsks(ctx, m, method, raw) // real manager context, not a stub response
	}
	go func() { _ = peer.Serve(ctx, out) }()
	var info struct {
		Main, Agent, Home string
		MainCwd           string `json:"main_cwd"`
		AgentCwd          string `json:"agent_cwd"`
	}
	if err := peer.Call(ctx, "probe.info", map[string]any{}, &info); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.peer, m.online, m.info.Home = peer, true, info.Home
	m.mu.Unlock()
	h.machines = append(h.machines, m)
	h.byID["box"] = m
	h.cfg.Machines = []MachineConfig{{ID: "box", Name: "Test computer", Kind: "local"}}
	editSessions(m, func([]proto.Session) []proto.Session {
		return []proto.Session{
			{SessionID: info.Main, Cwd: info.MainCwd, Agent: "claude", Launcher: "claude", Assistant: true, Status: proto.StatusIdle},
			{SessionID: info.Agent, Cwd: info.AgentCwd, Agent: "claude", Launcher: "claude", PaneID: "w1:p1", Status: proto.StatusIdle},
		}
	})
	managerStore := managerDB(t, h)
	if err := managerStore.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "box", Main: info.Main, Launcher: "claude"}); err != nil {
		t.Fatal(err)
	}
	dialAssistant(t, h, srv.URL, false)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + h.cfg.Token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	_, greeting, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hello struct {
		Type           string          `json:"type"`
		Features       []string        `json:"features"`
		ReceiptVersion json.RawMessage `json:"message_send_state"`
	}
	if err := json.Unmarshal(greeting, &hello); err != nil || hello.Type != "hello" {
		t.Fatalf("socket hello: %v", err)
	}
	if before {
		if len(hello.ReceiptVersion) != 0 {
			t.Fatal("baseline hello unexpectedly acquired a receipt authority marker")
		}
	} else if !bytes.Equal(hello.ReceiptVersion, []byte("1")) || !slices.Contains(hello.Features, proto.FeatureMessageSendState) {
		t.Fatal("socket hello lacks the hub-authored receipt version")
	}
	seen := make(chan proto.MessageSendState, 16)
	go func() {
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			at := time.Now() // recipient socket acceptance, not the hub's queue time
			var state proto.MessageSendState
			if json.Unmarshal(raw, &state) == nil && state.Type == "message_send_state" && state.State == proto.MessageSendProcessing {
				times.mark(proto.SendTraceID(state.ClientMsgID), "ws.processing_received", at)
				seen <- state
			}
		}
	}()
	for _, sid := range []string{info.Main, info.Agent} {
		raw, _ := json.Marshal(map[string]any{"type": "subscribe", "machine_id": "box", "session_id": sid})
		if err := ws.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "real WS subscriptions", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for c := range h.clients {
			c.mu.Lock()
			yes := c.subs[[2]string{"box", info.Main}] && c.subs[[2]string{"box", info.Agent}]
			c.mu.Unlock()
			if yes {
				return true
			}
		}
		return false
	})
	// Optional hold for the browser's own POST/WS acceptance and paint evidence.
	// It is always bounded and all helpers terminate through test cleanup.
	if metadata := os.Getenv("PERSONA_SEND_SOCKET_METADATA"); metadata != "" {
		raw, _ := json.Marshal(map[string]any{"url": srv.URL, "token": h.cfg.Token, "machine_id": "box", "main": info.Main, "agent": info.Agent, "expires_at": time.Now().Add(180 * time.Second).UTC()})
		if err := os.WriteFile(metadata, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Log("socket fixture ready; browser hold expires in 180 seconds")
		select {
		case <-time.After(180 * time.Second):
		case <-ctx.Done():
		}
	}
	for _, target := range []struct{ kind, sid string }{{"main", info.Main}, {"agent", info.Agent}} {
		id := fmt.Sprintf("52580000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
		trace := proto.SendTraceID(id)
		raw, _ := json.Marshal(map[string]any{"text": "Isolated acceptance prompt", "client_msg_id": id, "launcher": "claude"})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/v1/sessions/box/"+target.sid+"/messages", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Persona-Op-Id", id)
		start := time.Now()
		times.mark(trace, "client.click", start)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		times.mark(trace, "http.final", time.Now())
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s HTTP %d: %s", target.kind, res.StatusCode, body)
		}
		if !before {
			select {
			case <-seen:
			case <-time.After(time.Second):
				t.Fatal("no Processing reached the real WS reader")
			}
		}
		if target.kind == "main" {
			st, err := os.Stat(filepath.Join(dir, "runtime-input.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			times.kind("main", "runtime_stdin_consumed", st.ModTime())
		}
		times.mu.Lock()
		row := map[string]any{"kind": target.kind, "before": before, "click_to_phase_ms": map[string]float64{}}
		phases := row["click_to_phase_ms"].(map[string]float64)
		for phase, at := range times.byTrace[trace] {
			phases[phase] = float64(at.Sub(start).Microseconds()) / 1000
		}
		for phase, at := range times.byKind[target.kind] {
			phases[phase] = float64(at.Sub(start).Microseconds()) / 1000
		}
		admission := times.byTrace[trace]["sql.admission_done"].Sub(times.byTrace[trace]["sql.admission_start"])
		row["admission_sql_ms"] = float64(admission.Microseconds()) / 1000
		processing, hasProcessing := phases["ws.processing_received"]
		row["processing_observed"] = hasProcessing
		times.mu.Unlock()
		if admission < 0 || admission == 0 {
			t.Fatal("real admission SQL was not measured")
		}
		if !before && (!hasProcessing || processing >= 1000) {
			t.Fatalf("click to real WS Processing = %.3f ms", processing)
		}
		if before && hasProcessing {
			t.Fatal("baseline unexpectedly emitted Processing")
		}
		if _, ok := phases["node_received"]; !ok {
			t.Fatal("message never reached the separate Node process")
		}
		encoded, _ := json.Marshal(row)
		t.Logf("SOCKET_SEND_ACCEPTANCE %s", encoded)
	}
}
