package exttest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

type tbRecorder struct {
	testing.TB
	errors []string
}

func (r *tbRecorder) Helper() {}

func (r *tbRecorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestCheckSurfacesSchemaProblemsFromProgramEvents(t *testing.T) {
	const manifest = `{
		"manifest_version": 1,
		"id": "example",
		"name": "Example",
		"version": "1.0.0",
		"engines": {"extension_protocol": 1},
		"runtime": {"command": "bin/example"},
		"contributes": {
			"connectors": [{"id": "c1", "title": "C1", "capabilities": ["status"]}]
		}
	}`
	prog := ext.Program{
		ID:      "example",
		Version: "1.0.0",
		Init: func(_ context.Context, _ *ext.Init, hub *ext.Hub) (map[string]any, error) {
			_ = hub.Notify("connector.changed", map[string]any{"unexpected": 1})
			return nil, nil
		},
	}

	rec := &tbRecorder{TB: t}
	Check(rec, []byte(manifest), Serve(t, prog))
	if len(rec.errors) == 0 {
		t.Fatal("Check did not report schema problem for invalid connector.changed event")
	}
}

func TestHubIgnoresIncompleteRepliesAndIDLessMethods(t *testing.T) {
	toProgR, toProgW := io.Pipe()
	fromProgR, fromProgW := io.Pipe()
	t.Cleanup(func() {
		_ = toProgR.Close()
		_ = toProgW.Close()
		_ = fromProgR.Close()
		_ = fromProgW.Close()
	})

	h := newHub(Run{ToProgram: toProgW, FromProgram: fromProgR})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go h.serve(ctx)

	requests := make(chan frame, 4)
	go func() {
		sc := bufio.NewScanner(toProgR)
		for sc.Scan() {
			var f frame
			if json.Unmarshal(sc.Bytes(), &f) == nil {
				requests <- f
			}
		}
	}()

	var got struct {
		Value string `json:"value"`
	}
	done := make(chan error, 1)
	go func() {
		done <- h.call(ctx, "ping", struct{}{}, &got)
	}()

	req := <-requests
	_, _ = io.WriteString(fromProgW, `{"method":"custom.notify","params":{}}`+"\n")
	_, _ = io.WriteString(fromProgW, `{"id":"`+req.ID+`"}`+"\n")
	_, _ = io.WriteString(fromProgW, `{"id":"`+req.ID+`","error":null}`+"\n")

	select {
	case err := <-done:
		t.Fatalf("incomplete reply resolved hub.call early: err=%v, got=%+v", err, got)
	case f := <-requests:
		t.Fatalf("ID-less method frame caused hub to write frame: %+v", f)
	case <-time.After(50 * time.Millisecond):
	}

	_, _ = io.WriteString(fromProgW, `{"id":"`+req.ID+`","result":{"value":"ok"}}`+"\n")
	if err := <-done; err != nil || got.Value != "ok" {
		t.Fatalf("valid reply after incomplete frames: err=%v, got=%+v", err, got)
	}
}

type testCodedError struct{}

func (testCodedError) Error() string { return "quota exceeded" }
func (testCodedError) WireError() (string, json.RawMessage) {
	return "rate_limited", json.RawMessage(`{"retry_after":15}`)
}

func TestHostAnswerPreservesWrappedAndCodedErrors(t *testing.T) {
	const manifest = `{
		"manifest_version": 1,
		"id": "example",
		"name": "Example",
		"version": "1.0.0",
		"engines": {"extension_protocol": 1},
		"runtime": {"command": "bin/example"},
		"contributes": {}
	}`
	hubCh := make(chan *ext.Hub, 1)
	host := Start(t, []byte(manifest), Serve(t, ext.Program{
		ID:      "example",
		Version: "1.0.0",
		Init: func(_ context.Context, _ *ext.Init, hub *ext.Hub) (map[string]any, error) {
			hubCh <- hub
			return nil, nil
		},
	}))
	extHub := <-hubCh

	host.Answer("custom.wrapped", func(json.RawMessage) (any, error) {
		return nil, fmt.Errorf("outer: %w", &ext.Error{
			Code:    "not_found",
			Message: "missing item",
			Data:    json.RawMessage(`{"id":"x"}`),
		})
	})
	host.Answer("custom.coded", func(json.RawMessage) (any, error) {
		return nil, testCodedError{}
	})

	err := extHub.Call(context.Background(), "custom.wrapped", struct{}{}, nil)
	var wireErr *ext.Error
	if !errors.As(err, &wireErr) || wireErr.Code != "not_found" || string(wireErr.Data) != `{"id":"x"}` {
		t.Fatalf("wrapped *ext.Error over wire = %#v, want code=not_found data={\"id\":\"x\"}", err)
	}

	wireErr = nil
	err = extHub.Call(context.Background(), "custom.coded", struct{}{}, nil)
	if !errors.As(err, &wireErr) || wireErr.Code != "rate_limited" || !strings.Contains(string(wireErr.Data), `"retry_after":15`) {
		t.Fatalf("ext.Coded over wire = %#v, want code=rate_limited data={\"retry_after\":15}", err)
	}
}
