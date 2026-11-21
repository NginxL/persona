package assistant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMainExecutionSettingsDoNotChangeNewConversationDefaults(t *testing.T) {
	s, f := pickFixture(t)
	shared := ModelBinding{Provider: "launcher.gw", Model: "shared-model", Effort: "low"}
	main := ModelBinding{Provider: "launcher.claude", Model: "opus", Effort: "high"}
	setModel(t, s, "assistant", shared)
	if got, err := s.resolveModel(t.Context(), purposeMain); err != nil || got != shared {
		t.Fatalf("legacy main fallback: %+v %v", got, err)
	}
	setModel(t, s, purposeMain, main)
	if p, err := send(t, s, "main", map[string]any{}); err != nil || p["launcher"] != "claude" || p["model"] != main.Model || p["effort"] != main.Effort {
		t.Fatalf("main next message: %v %v", p, err)
	}
	for _, tc := range []struct {
		body string
		want ModelBinding
	}{
		{`{"prompt":"side"}`, shared},
		{`{"prompt":"main","main":true}`, main},
		{`{"prompt":"another side"}`, shared},
	} {
		code, session := startConv(t, s, tc.body)
		p := f.lastSend(t)
		if code != http.StatusOK || p["launcher"] != strings.TrimPrefix(tc.want.Provider, "launcher.") || p["model"] != tc.want.Model || p["effort"] != tc.want.Effort || session.Effort != tc.want.Effort {
			t.Fatalf("%s: %d %+v %v", tc.body, code, session, p)
		}
	}
	again := newServiceOn(t, f, s.store())
	if got, err := again.resolveModel(t.Context(), purposeMain); err != nil || got != main {
		t.Fatalf("main after manager restart: %+v %v", got, err)
	}
	if got, err := again.resolveModel(t.Context(), "assistant"); err != nil || got != shared {
		t.Fatalf("shared defaults after manager restart: %+v %v", got, err)
	}
}

func TestMainExecutionSettingsUseIndependentRevisionAndReset(t *testing.T) {
	s, _ := pickFixture(t)
	shared := ModelBinding{Provider: "launcher.gw", Model: "shared-model", Effort: "low"}
	setModel(t, s, "assistant", shared)
	p := purposeOf(t, s, purposeMain)
	if p.Parent != "assistant" || p.CategoryID != "assistant" || p.Launch != "headless" || len(p.Fields) == 0 {
		t.Fatalf("main has no independent editor: %+v", p)
	}
	body := fmt.Sprintf(`{"revision":%d,"provider":"launcher.gw","model":"main-model","effort":"high"}`, p.Revision)
	if r := modelRequest(s, "PATCH", purposeMain, body); r.Code != http.StatusOK {
		t.Fatalf("main save: %d %s", r.Code, r.Body.String())
	}
	if r := modelRequest(s, "PATCH", purposeMain, body); r.Code != http.StatusConflict {
		t.Fatalf("stale main revision accepted: %d %s", r.Code, r.Body.String())
	}
	for _, invalid := range []string{`"bad model"`, `"invalid\neffort"`} {
		p = purposeOf(t, s, purposeMain)
		field := "model"
		if invalid == `"invalid\neffort"` {
			field = "effort"
		}
		r := modelRequest(s, "PATCH", purposeMain, fmt.Sprintf(`{"revision":%d,%q:%s}`, p.Revision, field, invalid))
		if r.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s accepted: %d %s", field, r.Code, r.Body.String())
		}
	}
	p = purposeOf(t, s, purposeMain)
	if r := modelRequest(s, "PATCH", purposeMain, fmt.Sprintf(`{"revision":%d,"effort":"low"}`, p.Revision)); r.Code != http.StatusOK {
		t.Fatalf("sparse patch: %d %s", r.Code, r.Body.String())
	}
	if got, _ := s.resolveModel(context.Background(), purposeMain); got.Provider != "launcher.gw" || got.Model != "main-model" || got.Effort != "low" {
		t.Fatalf("sparse patch lost fields: %+v", got)
	}
	if got, _ := s.resolveModel(context.Background(), "assistant"); got != shared {
		t.Fatalf("main edit changed shared defaults: %+v", got)
	}
	p = purposeOf(t, s, purposeMain)
	if r := modelRequest(s, "PATCH", purposeMain, fmt.Sprintf(`{"revision":%d,"provider":null,"model":null,"effort":null}`, p.Revision)); r.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", r.Code, r.Body.String())
	}
	if got, err := s.resolveModel(t.Context(), purposeMain); err != nil || got != shared {
		t.Fatalf("reset fallback: %+v %v", got, err)
	}
}

func TestMainReadinessAndExplicitConversationPickStayIndependent(t *testing.T) {
	s, _ := pickFixture(t)
	setModel(t, s, purposeMain, ModelBinding{Provider: "launcher.claude", Model: "opus", Effort: "high"})
	target, err := s.readinessProbeTarget(t.Context(), "server", Settings{})
	if err != nil || target.launcher != "claude" || target.model != "opus" || target.effort != "high" {
		t.Fatalf("main-start readiness probes another default: %+v %v", target, err)
	}
	if r := putPick(t, s, "main", `{"launcher":"gw","model":"picked-model","effort":"low"}`); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	if p, err := send(t, s, "main", map[string]any{}); err != nil || p["launcher"] != "gw" || p["model"] != "picked-model" || p["effort"] != "low" {
		t.Fatalf("main default replaced an explicit conversation pick: %v %v", p, err)
	}
}

func TestConcurrentMainAndSharedDefaultEditsDoNotOverwriteEachOther(t *testing.T) {
	s, _ := pickFixture(t)
	setModel(t, s, purposeMain, ModelBinding{Provider: "launcher.gw", Model: "main-model", Effort: "high"})
	main, shared := purposeOf(t, s, purposeMain), purposeOf(t, s, "assistant")
	results := make(chan int, 2)
	go func() {
		results <- modelRequest(s, "PATCH", purposeMain, fmt.Sprintf(`{"revision":%d,"effort":"low"}`, main.Revision)).Code
	}()
	go func() {
		results <- modelRequest(s, "PATCH", "assistant", fmt.Sprintf(`{"revision":%d,"model":"new-shared"}`, shared.Revision)).Code
	}()
	for range 2 {
		if code := <-results; code != http.StatusOK {
			t.Fatalf("concurrent edit: %d", code)
		}
	}
	if got, _ := s.resolveModel(t.Context(), purposeMain); got.Model != "main-model" || got.Effort != "low" {
		t.Fatalf("main edit lost: %+v", got)
	}
	if got, _ := s.resolveModel(t.Context(), "assistant"); got.Model != "new-shared" {
		t.Fatalf("shared edit lost: %+v", got)
	}
}
