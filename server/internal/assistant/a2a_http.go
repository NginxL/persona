package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
)

const a2aProtocolVersion = "1.0"

type a2aPart struct {
	Text      *string         `json:"text,omitempty"`
	Raw       *string         `json:"raw,omitempty"`
	URL       *string         `json:"url,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
}

type a2aMessage struct {
	ID        string    `json:"messageId"`
	ContextID string    `json:"contextId,omitempty"`
	TaskID    string    `json:"taskId,omitempty"`
	Role      string    `json:"role"`
	Parts     []a2aPart `json:"parts"`
}

type a2aStatus struct {
	State     string      `json:"state"`
	Timestamp time.Time   `json:"timestamp"`
	Message   *a2aMessage `json:"message,omitempty"`
}

type a2aArtifact struct {
	ID    string    `json:"artifactId"`
	Name  string    `json:"name"`
	Parts []a2aPart `json:"parts"`
}

type a2aWireTask struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    a2aStatus      `json:"status"`
	History   []a2aMessage   `json:"history,omitempty"`
	Artifacts []a2aArtifact  `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func a2aWireState(state string) string { return "TASK_STATE_" + strings.ToUpper(state) }

func (t *a2aTask) wire(history *int, artifacts bool) a2aWireTask {
	out := a2aWireTask{ID: t.ID, ContextID: t.Context, Metadata: map[string]any{"personaRevision": t.Revision, "personaTargetDisconnected": t.Disconnected}}
	for _, e := range t.Events {
		var m *a2aMessage
		if e.Text != "" {
			role := "ROLE_AGENT"
			if e.Kind == "message" {
				role = "ROLE_USER"
			}
			m = &a2aMessage{ID: e.ID, ContextID: t.Context, TaskID: t.ID, Role: role, Parts: []a2aPart{{Text: &e.Text}}}
			out.History = append(out.History, *m)
		}
		if e.State != "" {
			out.Status = a2aStatus{State: a2aWireState(e.State), Timestamp: e.At, Message: m}
		}
		if e.State == a2aComplete && artifacts && m != nil {
			out.Artifacts = append(out.Artifacts, a2aArtifact{ID: e.ID, Name: "Result", Parts: m.Parts})
		}
	}
	if history != nil && len(out.History) > *history {
		out.History = out.History[len(out.History)-*history:]
	}
	return out
}

func a2aBase(cfg assistantwire.A2AConfig) *url.URL {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil
	}
	return u
}

func a2aHTTPAllowed(r *http.Request) bool {
	role := r.Header.Get(extwire.HeaderRole)
	return role == roleFull || role == "scoped" &&
		slices.Contains(strings.Split(r.Header.Get(extwire.HeaderScopes), ","), assistantwire.ScopeA2A)
}

func (s *Service) a2aCard(w http.ResponseWriter, r *http.Request) {
	cfg := s.a2aConfig(r.Context())
	base := a2aBase(cfg)
	ref := a2aRef{r.PathValue("mid"), r.PathValue("sid")}
	if base == nil || !a2aHTTPAllowed(r) {
		http.NotFound(w, r)
		return
	}
	sess, err := s.a2aSession(r.Context(), cfg, ref)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/api/v1/a2a/" + url.PathEscape(ref.Machine) + "/" + url.PathEscape(ref.Session) + "/rpc"
	skill := map[string]any{"id": "persona-session", "name": "Persona session", "description": "Send work and exchange results with this session.", "tags": []string{"persona", "agent"}}
	a2aJSON(w, map[string]any{
		"name": cmp.Or(strings.TrimSpace(sess.Title), ref.Session), "description": "A Persona session that accepts text tasks.", "version": a2aProtocolVersion,
		"supportedInterfaces":  []any{map[string]string{"url": endpoint, "protocolBinding": "JSONRPC", "protocolVersion": a2aProtocolVersion}},
		"capabilities":         map[string]bool{"streaming": true, "pushNotifications": false},
		"securitySchemes":      map[string]any{"persona": map[string]any{"httpAuthSecurityScheme": map[string]string{"scheme": "Bearer"}}},
		"securityRequirements": []any{map[string]any{"schemes": map[string]any{"persona": map[string]any{"values": []string{}}}}},
		"defaultInputModes":    []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "skills": []any{skill},
	})
}

type a2aRPCRequest struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type a2aRPCParams struct {
	ID            string     `json:"id"`
	Tenant        string     `json:"tenant"`
	Message       a2aMessage `json:"message"`
	HistoryLength *int       `json:"historyLength"`
	Configuration struct {
		HistoryLength     *int            `json:"historyLength"`
		ReturnImmediately bool            `json:"returnImmediately"`
		Push              json.RawMessage `json:"taskPushNotificationConfig"`
		AcceptedModes     []string        `json:"acceptedOutputModes"`
	} `json:"configuration"`
	ContextID   string     `json:"contextId"`
	Status      string     `json:"status"`
	PageSize    int        `json:"pageSize"`
	PageToken   string     `json:"pageToken"`
	Artifacts   *bool      `json:"includeArtifacts"`
	StatusAfter *time.Time `json:"statusTimestampAfter"`
}

type a2aRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type a2aRPCResponse struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *a2aRPCError    `json:"error,omitempty"`
}

var (
	errA2AUnsupported = errors.New("operation is not supported for this A2A task")
	errA2AContent     = errors.New("only text/plain parts are supported")
	errA2APush        = errors.New("push notification configuration is unsupported")
)

func a2aRPCFailure(err error) *a2aRPCError {
	code := -32603
	switch {
	case errors.Is(err, errA2ANotFound), errors.Is(err, errA2AUnavailable):
		code = -32001
	case errors.Is(err, errA2ACancel):
		code = -32002
	case errors.Is(err, errA2APush):
		code = -32003
	case errors.Is(err, errA2AUnsupported), errors.Is(err, db.ErrA2ATargetBusy):
		code = -32004
	case errors.Is(err, errA2AContent):
		code = -32005
	case errors.Is(err, errA2AInvalid), errors.Is(err, errA2ADuplicate):
		code = -32602
	case errors.Is(err, errA2AConflict):
		return &a2aRPCError{Code: code, Message: err.Error()}
	default:
		return &a2aRPCError{Code: code, Message: "Internal error"}
	}
	return &a2aRPCError{Code: code, Message: err.Error()}
}

func a2aJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Service) a2aRPC(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg := s.a2aConfig(ctx)
	to := a2aRef{r.PathValue("mid"), r.PathValue("sid")}
	device := r.Header.Get(extwire.HeaderDevice)
	if !a2aHTTPAllowed(r) || device == "" {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if a2aBase(cfg) == nil || !s.a2aMachineAllowed(ctx, cfg, to.Machine) {
		http.NotFound(w, r)
		return
	}
	resp := a2aRPCResponse{Version: "2.0"}
	fail := func(code int, message string) {
		resp.Error = &a2aRPCError{Code: code, Message: message}
		a2aJSON(w, resp)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil || !json.Valid(raw) {
		fail(-32700, "Invalid JSON payload")
		return
	}
	var req a2aRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Version != "2.0" || req.Method == "" || len(req.ID) == 0 ||
		(req.ID[0] != '"' && req.ID[0] != '-' && (req.ID[0] < '0' || req.ID[0] > '9')) {
		fail(-32600, "Invalid JSON-RPC request")
		return
	}
	resp.ID = req.ID
	if req.Method != "GetTask" && req.Method != "SubscribeToTask" && !s.a2aAllowed(ctx, cfg, to) {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("A2A-Version") != a2aProtocolVersion {
		fail(-32009, "A2A-Version 1.0 is required")
		return
	}
	var p a2aRPCParams
	if len(req.Params) > 0 && (req.Params[0] != '{' || json.Unmarshal(req.Params, &p) != nil) ||
		p.Tenant != "" || p.HistoryLength != nil && *p.HistoryLength < 0 || p.Configuration.HistoryLength != nil && *p.Configuration.HistoryLength < 0 {
		fail(-32602, "Invalid parameters")
		return
	}
	st, err := s.a2aStore()
	if err != nil {
		fail(-32603, "Task storage is unavailable")
		return
	}
	c := a2aCaller{Owner: "device:" + device}
	switch req.Method {
	case "SendMessage", "SendStreamingMessage":
		t, err := s.a2aSendRPC(ctx, st, cfg, c, to, p)
		if err == nil {
			s.a2aSignal()
			if req.Method == "SendStreamingMessage" {
				s.a2aStream(w, r, st, cfg, c, t, resp, p.Configuration.HistoryLength)
				return
			}
			if !p.Configuration.ReturnImmediately {
				t, err = s.a2aWait(ctx, st, cfg, c, t)
			}
			if err == nil {
				resp.Result = map[string]any{"task": t.wire(p.Configuration.HistoryLength, true)}
			}
		}
		if err != nil {
			resp.Error = a2aRPCFailure(err)
		}
	case "GetTask", "CancelTask", "SubscribeToTask":
		t, err := s.a2aHTTPTask(ctx, st, cfg, c, to, p.ID)
		switch {
		case err != nil:
		case req.Method == "CancelTask":
			t, err = s.a2aMutate(ctx, st, cfg, c, t.ID, a2aCancel)
		case req.Method == "SubscribeToTask":
			if a2aTerminal(t.State) {
				err = errA2AUnsupported
				break
			}
			s.a2aStream(w, r, st, cfg, c, t, resp, p.HistoryLength)
			return
		}
		if err != nil {
			resp.Error = a2aRPCFailure(err)
		} else {
			resp.Result = t.wire(p.HistoryLength, true)
		}
	case "ListTasks":
		resp.Result, err = s.a2aListRPC(ctx, st, c, to, p)
		if err != nil {
			resp.Error = a2aRPCFailure(err)
		}
	case "CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig", "ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig":
		resp.Error = a2aRPCFailure(errA2APush)
	case "GetExtendedAgentCard":
		resp.Error = &a2aRPCError{Code: -32007, Message: "Extended Agent Card is not configured"}
	default:
		fail(-32601, "Method not found")
		return
	}
	a2aJSON(w, resp)
}

func (s *Service) a2aHTTPTask(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, to a2aRef, id string) (*a2aTask, error) {
	if !a2aValidID(id) {
		return nil, errA2AInvalid
	}
	t, err := s.a2aGet(ctx, st, cfg, c, id)
	if err == nil && t.target() != to {
		return nil, errA2ANotFound
	}
	return t, err
}

func (s *Service) a2aSendRPC(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, to a2aRef, p a2aRPCParams) (*a2aTask, error) {
	if len(p.Configuration.Push) > 0 && string(p.Configuration.Push) != "null" {
		return nil, errA2APush
	}
	if len(p.Configuration.AcceptedModes) > 0 && !slices.Contains(p.Configuration.AcceptedModes, "text/plain") {
		return nil, errA2AContent
	}
	m := p.Message
	if m.Role != "ROLE_USER" || !a2aValidID(m.ID) || len(m.Parts) == 0 || len(m.Parts) > 32 {
		return nil, errA2AInvalid
	}
	var parts []string
	for _, part := range m.Parts {
		if part.Text == nil || part.Raw != nil || part.URL != nil || len(part.Data) != 0 || part.MediaType != "" && part.MediaType != "text/plain" {
			return nil, errA2AContent
		}
		parts = append(parts, *part.Text)
	}
	text := strings.Join(parts, "\n\n")
	if m.TaskID == "" {
		return s.a2aStart(ctx, st, cfg, c, to, m.ID, m.ContextID, text, false, nil)
	}
	t, err := s.a2aHTTPTask(ctx, st, cfg, c, to, m.TaskID)
	if err != nil {
		return nil, err
	}
	if m.ContextID != "" && m.ContextID != t.Context {
		return nil, errA2AInvalid
	}
	return s.a2aMutate(ctx, st, cfg, c, t.ID, func(t *a2aTask) (bool, error) { return a2aContinue(t, m.ID, text) })
}

func (s *Service) a2aListRPC(ctx context.Context, st a2aStore, c a2aCaller, to a2aRef, p a2aRPCParams) (any, error) {
	limit := p.PageSize
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || p.PageToken != "" && !a2aValidID(p.PageToken) {
		return nil, errA2AInvalid
	}
	state := strings.ToLower(strings.TrimPrefix(p.Status, "TASK_STATE_"))
	switch state {
	case "", a2aComplete, a2aFailed, a2aCanceled, a2aAuth, a2aInput, a2aWorking, a2aSubmitted, "rejected":
	default:
		return nil, errA2AInvalid
	}
	rows, err := st.A2AList(ctx, c.Owner, to.Machine, to.Session, p.ContextID, state, p.PageToken, false, p.StatusAfter, limit+1)
	if err != nil {
		return nil, err
	}
	out := []a2aWireTask{}
	next := ""
	for i, row := range rows {
		if i == limit {
			next = rows[i-1].ID
			break
		}
		t, err := decodeA2A(&row, nil)
		if err != nil {
			return nil, err
		}
		task := t.wire(p.HistoryLength, p.Artifacts != nil && *p.Artifacts)
		out = append(out, task)
	}
	n, err := st.A2ACount(ctx, c.Owner, to.Machine, to.Session, p.ContextID, state, p.StatusAfter)
	return map[string]any{"tasks": out, "nextPageToken": next, "pageSize": limit, "totalSize": n}, err
}

func a2aSettled(state string) bool {
	return a2aTerminal(state) || state == a2aInput || state == a2aAuth
}

func (s *Service) a2aWait(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, t *a2aTask) (*a2aTask, error) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for !a2aSettled(t.State) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
			var err error
			t, err = s.a2aGet(ctx, st, cfg, c, t.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	return t, nil
}

func (s *Service) a2aStream(w http.ResponseWriter, r *http.Request, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, t *a2aTask, resp a2aRPCResponse, history *int) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	write := func(value any) bool {
		resp.Result = value
		raw, err := json.Marshal(resp)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "data: %s\n\n", raw)
		return err == nil && control.Flush() == nil
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	if !write(map[string]any{"task": t.wire(history, true)}) {
		return
	}
	seq := len(t.Events)
	heartbeat := time.Now()
	for !a2aTerminal(t.State) {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			var err error
			t, err = s.a2aGet(ctx, st, cfg, c, t.ID)
			if err != nil {
				return
			}
		}
		for i := seq; i < len(t.Events); i++ {
			event := t.Events[i]
			statusMessage := event.Kind == "progress" || event.Kind == "connection"
			if event.State == "" && !statusMessage {
				continue
			}
			prefix := *t
			prefix.Events = t.Events[:i+1]
			wire := prefix.wire(history, true)
			if event.State == a2aComplete && len(wire.Artifacts) != 0 &&
				!write(map[string]any{"artifactUpdate": map[string]any{"taskId": t.ID, "contextId": t.Context, "artifact": wire.Artifacts[0], "lastChunk": true}}) {
				return
			}
			if statusMessage {
				wire.Status.Timestamp = event.At
				wire.Status.Message = &a2aMessage{ID: event.ID, ContextID: t.Context, TaskID: t.ID, Role: "ROLE_AGENT", Parts: []a2aPart{{Text: &event.Text}}}
			}
			if !write(map[string]any{"statusUpdate": map[string]any{"taskId": t.ID, "contextId": t.Context, "status": wire.Status}}) {
				return
			}
		}
		seq = len(t.Events)
		if time.Since(heartbeat) >= 15*time.Second {
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || control.Flush() != nil {
				return
			}
			heartbeat = time.Now()
		}
	}
}
