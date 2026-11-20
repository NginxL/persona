package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/rpc"
)

func TestCallErrRefusedSend(t *testing.T) {
	w := httptest.NewRecorder()
	callErr(w, fmt.Errorf("send: %w", &rpc.Error{Code: "input_draft", Message: "输入框里有字",
		Data: json.RawMessage(`{"delivered":"none","draft":"❯ hi","clearable":"hi"}`)}))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body["code"] != "input_draft" || body["error"] != "输入框里有字" ||
		body["delivered"] != "none" || body["clearable"] != "hi" {
		t.Fatalf("%d %v", w.Code, body)
	}
	// Old apps read only error: it is the message they show.
	w = httptest.NewRecorder()
	callErr(w, &rpc.Error{Message: "socket closed", Data: json.RawMessage(`{"delivered":"unknown"}`)})
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadGateway || body["delivered"] != "unknown" || body["error"] != "socket closed" {
		t.Fatalf("%d %v", w.Code, body)
	}
	w = httptest.NewRecorder()
	callErr(w, errors.New("plain"))
	if w.Code != http.StatusBadGateway {
		t.Fatal(w.Code)
	}
}

func TestCallErrEffortChangePending(t *testing.T) {
	w := httptest.NewRecorder()
	callErr(w, &rpc.Error{Code: "effort_change_pending", Message: "Thinking effort will change after this turn.",
		Data: json.RawMessage(`{"delivered":"none","effort":"high"}`)})
	body := errorResponse(t, w)
	if w.Code != http.StatusConflict || body["code"] != "effort_change_pending" || body["delivered"] != "none" || body["effort"] != "high" {
		t.Fatalf("%d %v", w.Code, body)
	}
}

func TestRequestID(t *testing.T) {
	if requestID("1a2b3c4d") != "1a2b3c4d" {
		t.Fatal("a client id is kept")
	}
	for _, bad := range []string{"", "x", "id with spaces", strings.Repeat("a", 40), "token=secret"} {
		if id := requestID(bad); id == bad || len(id) != 8 {
			t.Fatalf("%q -> %q", bad, id)
		}
	}
}

func TestNodeMismatch(t *testing.T) {
	for _, c := range []struct {
		hub, node string
		want      bool
	}{
		{"a1b2c3d-202610290900", "a1b2c3d-202610290900", false},
		{"a1b2c3d-202610290900", "9f8e7d6-202610281200", true},
		{"dev", "9f8e7d6-202610281200", false},
		{"a1b2c3d-202610290900", "", false}, // offline / unknown
	} {
		if got := nodeMismatch(c.hub, c.node); got != c.want {
			t.Errorf("%q vs %q: %v", c.hub, c.node, got)
		}
	}
}
