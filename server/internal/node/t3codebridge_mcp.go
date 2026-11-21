package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A client of T3 Code's MCP endpoint (Streamable HTTP, the transport T3 Code's own `t3 thread` command line uses):
// initialize, then tools/call. An answer comes as JSON or as an event stream; a request the server sends inside a
// stream (ping) gets its answer in a POST of its own. docs/integrations/engines.md "T3 Code".

// t3codeMCPProtocol is the MCP protocol version the bridge asks for.
const t3codeMCPProtocol = "2025-06-18"

// errT3CodeSignedOut: the server no longer takes the bridge's session (revoked in T3 Code, expired or the server's
// sessions were reset). The bridge ends and issues a new one.
var errT3CodeSignedOut = errors.New("T3 Code 不再接受 Persona 的登录")

type t3codeMCP struct {
	endpoint string
	token    string
	client   *http.Client

	mu        sync.Mutex
	sessionID string
	protocol  string
	next      int
}

func newT3CodeMCP(endpoint, token string) *t3codeMCP {
	return &t3codeMCP{endpoint: endpoint, token: token, client: &http.Client{Timeout: 60 * time.Second}}
}

func (m *t3codeMCP) headers(h http.Header) {
	h.Set("Accept", "application/json, text/event-stream")
	h.Set("Authorization", "Bearer "+m.token)
	m.mu.Lock()
	if m.sessionID != "" {
		h.Set("Mcp-Session-Id", m.sessionID)
	}
	if m.protocol != "" {
		h.Set("Mcp-Protocol-Version", m.protocol)
	}
	m.mu.Unlock()
}

type t3codeEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// send POSTs one message and answers the messages its response carries (none for a notification), answering any
// request the server makes on the way.
func (m *t3codeMCP) send(ctx context.Context, msg any) ([]t3codeEnvelope, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	m.headers(req.Header)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		m.mu.Lock()
		m.sessionID = sid
		m.mu.Unlock()
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, errT3CodeSignedOut
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("T3 Code 的 MCP 接口回答了 HTTP %d", resp.StatusCode)
	}
	var raws [][]byte
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raws, err = t3codeStreamPayloads(resp.Body)
	} else {
		var raw []byte
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if len(bytes.TrimSpace(raw)) > 0 {
			raws = [][]byte{raw}
		}
	}
	if err != nil {
		return nil, err
	}
	var out []t3codeEnvelope
	for _, raw := range raws {
		var batch []t3codeEnvelope
		if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
			if json.Unmarshal(t, &batch) != nil {
				continue
			}
		} else {
			var one t3codeEnvelope
			if json.Unmarshal(t, &one) != nil {
				continue
			}
			batch = []t3codeEnvelope{one}
		}
		for _, e := range batch {
			if len(e.ID) > 0 && e.Method != "" { // the server asks something of the client
				if e.Method == "ping" {
					_, _ = m.send(ctx, map[string]any{"jsonrpc": "2.0", "id": e.ID, "result": map[string]any{}})
				} else {
					_, _ = m.send(ctx, map[string]any{"jsonrpc": "2.0", "id": e.ID,
						"error": map[string]any{"code": -32601, "message": "Method not found"}})
				}
				continue
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// t3codeStreamPayloads reads the data of each event of an event stream.
func t3codeStreamPayloads(r io.Reader) ([][]byte, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	var out [][]byte
	var data []string
	flush := func() {
		if len(data) > 0 {
			out = append(out, []byte(strings.Join(data, "\n")))
			data = nil
		}
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	flush()
	return out, sc.Err()
}

// request sends one JSON-RPC request and answers its result.
func (m *t3codeMCP) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	m.mu.Lock()
	m.next++
	id := fmt.Sprintf("persona-%d", m.next)
	m.mu.Unlock()
	idRaw, _ := json.Marshal(id)
	msgs, err := m.send(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	for _, e := range msgs {
		if !bytes.Equal(bytes.TrimSpace(e.ID), idRaw) {
			continue
		}
		if e.Error != nil {
			return nil, fmt.Errorf("T3 Code MCP %s: %s", method, e.Error.Message)
		}
		return e.Result, nil
	}
	return nil, fmt.Errorf("T3 Code MCP %s: no answer", method)
}

// initialize opens the MCP session.
func (m *t3codeMCP) initialize(ctx context.Context) error {
	res, err := m.request(ctx, "initialize", map[string]any{
		"protocolVersion": t3codeMCPProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "persona", "version": "1"},
	})
	if err != nil {
		return err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(res, &init) == nil && init.ProtocolVersion != "" {
		m.mu.Lock()
		m.protocol = init.ProtocolVersion
		m.mu.Unlock()
	}
	_, err = m.send(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return err
}

// t3codeToolError is a tool's own refusal (its result's isError), with T3 Code's code when it gave one.
type t3codeToolError struct {
	Tool    string
	Code    string
	Message string
}

func (e *t3codeToolError) Error() string { return e.Tool + ": " + e.Message }

// call calls a tool and decodes its structured result into out.
func (m *t3codeMCP) call(ctx context.Context, tool string, args map[string]any, out any) error {
	res, err := m.request(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return err
	}
	var r struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	text := ""
	for _, c := range r.Content {
		if c.Text != "" {
			text = c.Text
			break
		}
	}
	if r.IsError {
		te := &t3codeToolError{Tool: tool, Message: firstOf(text, "the tool call failed")}
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(text), &failure) == nil && failure.Message != "" {
			te.Code, te.Message = failure.Code, failure.Message
		}
		return te
	}
	if out == nil {
		return nil
	}
	raw := r.StructuredContent
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = json.RawMessage(text)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: unexpected result: %w", tool, err)
	}
	return nil
}

// close ends the MCP session on the server.
func (m *t3codeMCP) close() {
	m.mu.Lock()
	sid := m.sessionID
	m.mu.Unlock()
	if sid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, m.endpoint, nil)
	if err != nil {
		return
	}
	m.headers(req.Header)
	if resp, err := m.client.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}
