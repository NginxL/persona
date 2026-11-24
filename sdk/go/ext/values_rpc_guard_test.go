package ext

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestUIRequestNumberRejectsNonFiniteFloats(t *testing.T) {
	for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "-Infinity", " NaN "} {
		r := &UIRequest{UIParams: wire.UIParams{Values: map[string]string{"x": bad}}}
		if got, err := r.Number("x"); err == nil {
			t.Errorf("UIRequest.Number(%q) = (%v, nil), want error", bad, got)
		}
	}

	r := &UIRequest{UIParams: wire.UIParams{Values: map[string]string{"x": " -42.5 "}}}
	got, err := r.Number("x")
	if err != nil || got != -42.5 {
		t.Fatalf("UIRequest.Number(\" -42.5 \") = (%v, %v), want (-42.5, nil)", got, err)
	}
}

func TestServeIgnoresMethodFramesWithoutID(t *testing.T) {
	var called atomic.Int32
	h, _ := startWithHub(t, Program{
		ID:      "example",
		Version: "1.0.0",
		Namespaces: map[string]Handler{
			"quota": func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
				called.Add(1)
				return map[string]string{"ok": "1"}, nil
			},
		},
	})

	h.send(
		`{"method":"quota.status","params":{}}`,
		`{"id":"p1","method":"ping"}`,
	)
	f := h.next()
	if f.ID != "p1" {
		t.Fatalf("first reply frame after ID-less method = %+v, want id=p1", f)
	}
	if f.Error != nil {
		t.Fatalf("ping error = %+v, want nil", f.Error)
	}
	if n := called.Load(); n != 0 {
		t.Fatalf("handler invoked %d times for ID-less method frame, want 0", n)
	}
}
