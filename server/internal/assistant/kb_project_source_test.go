package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

type projectSourceModel struct{}

func (projectSourceModel) JSONMax(_ context.Context, _, _ string, _ int, out any) error {
	return json.Unmarshal([]byte(`{"summary":"Original project summary","one_line":"Original one line"}`), out)
}

func TestKBProjectFreshnessHTTPRPCAndTools(t *testing.T) {
	ctx := context.Background()
	s := newKBService(t, newFakeHub())
	e := kbEng(t, s)
	e.Model, e.ModelFor = projectSourceModel{}, nil
	callTool := func(name, raw string) string {
		t.Helper()
		out, err := runTool(s, "example", name, json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	callTool("kb_update", `{"kind":"projects","id":"garden","log":"Original evidence"}`)
	if n := e.RefreshProjects(ctx, 1); n != 1 {
		t.Fatalf("refresh = %d", n)
	}
	callTool("kb_update", `{"kind":"projects","id":"garden","summary":"Owner summary","log":"Newer evidence"}`)
	for _, name := range []string{"kb_list", "kb_get"} {
		out := callTool(name, `{"kind":"projects","id":"garden"}`)
		for _, want := range []string{`"needs_review":true`, `"review_reason":"summary_edited"`, `"timeline_through":`, `"review_by":"persona"`, `"uncovered_evidence_count":1`} {
			if !strings.Contains(out, want) {
				t.Errorf("%s is missing %s: %s", name, want, out)
			}
		}
	}
	do := func(method, path, payload, role string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(payload))
		r.Header.Set(RoleHeader, role)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	listPath := "/api/v1/kb/projects"
	itemPath := listPath + "/garden"
	var list assistantwire.KBListResult
	var detail assistantwire.KBGetResult
	for _, read := range []struct {
		path string
		into any
	}{{listPath, &list}, {itemPath, &detail}} {
		w := do(http.MethodGet, read.path, "", "")
		if err := json.Unmarshal(w.Body.Bytes(), read.into); w.Code != http.StatusOK || err != nil {
			t.Fatalf("GET %s: %d %s (%v)", read.path, w.Code, w.Body, err)
		}
	}
	if len(list.Items) != 1 || detail.Detail == nil || detail.Detail.Freshness == nil ||
		!reflect.DeepEqual(list.Items[0].Freshness, detail.Detail.Freshness) || detail.Detail.Summary != "Owner summary\n" {
		t.Fatalf("HTTP list/detail disagree: %+v / %+v", list, detail)
	}
	getRPC := func(method, raw string) any {
		t.Helper()
		out, handled, err := s.kbRequest(ctx, method, json.RawMessage(raw))
		if !handled || err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return out
	}
	rpcList := getRPC(assistantwire.MethodKBList, `{"kind":"projects"}`).(assistantwire.KBListResult)
	rpcDetail := getRPC(assistantwire.MethodKBGet, `{"kind":"projects","id":"garden"}`).(assistantwire.KBGetResult)
	for _, view := range []*kbwire.ProjectFreshness{rpcList.Items[0].Freshness, rpcDetail.Detail.Freshness} {
		if !reflect.DeepEqual(view, detail.Detail.Freshness) {
			t.Fatalf("HTTP and RPC disagree: %+v / %+v", view, detail.Detail.Freshness)
		}
	}
	for _, a := range []string{`{"kind":"projects","id":"garden","one_line":"fake"}`, `{"kind":"projects","id":"garden","writer":"fake"}`} {
		if _, err := runTool(s, "example", "kb_update", json.RawMessage(a)); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unsupported update field accepted: %s (%v)", a, err)
		}
	}
	if w := do(http.MethodPut, itemPath, `{"one_line":"fake"}`, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "one_line") {
		t.Fatalf("HTTP let an unsupported field through: %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, itemPath, `{"project_mode":"reference-only"}`, roleExtension); w.Code != http.StatusForbidden {
		t.Fatalf("legacy extension could change the policy: %d %s", w.Code, w.Body)
	}

	if w := do(http.MethodPut, itemPath, `{"project_mode":"reference-only","source_urls":["https://example.org/garden"],"aliases":["Garden notes"]}`, ""); w.Code != http.StatusOK {
		t.Fatalf("could not set reference-only: %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, itemPath, `{"log":"Forbidden detail","summary":"Forbidden summary"}`, ""); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "log, summary") {
		t.Fatalf("HTTP did not refuse both fields: %d %s", w.Code, w.Body)
	}
	if _, err := runTool(s, "example", "kb_update", json.RawMessage(`{"kind":"projects","id":"Garden notes","summary":"Forbidden summary"}`)); err == nil || !strings.Contains(err.Error(), "reference-only") {
		t.Fatalf("tool bypassed the boundary: %v", err)
	}
	for _, attempt := range []struct{ arg, reason string }{
		{`"log":"Forbidden detail"`, "reference-only"},
		{`"project_mode":"full"`, "unknown field"},
	} {
		wrapped, err := json.Marshal(assistantwire.KBExtensionUpdate{Extension: "example", Params: json.RawMessage(`{"kind":"projects","id":"garden",` + attempt.arg + `}`)})
		if err != nil {
			t.Fatal(err)
		}
		_, handled, err := s.kbRequest(ctx, assistantwire.MethodKBExtensionUpdate, wrapped)
		var rpcErr *rpc.Error
		if !handled || !errors.As(err, &rpcErr) || rpcErr.Code != "invalid_params" || !strings.Contains(rpcErr.Message, attempt.reason) {
			t.Fatalf("extension RPC bypassed the boundary: %v", err)
		}
	}
	w := do(http.MethodGet, itemPath, "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"project_mode":"reference-only"`) ||
		!strings.Contains(w.Body.String(), "https://example.org/garden") || !strings.Contains(w.Body.String(), "Owner summary") ||
		strings.Contains(w.Body.String(), "Forbidden") {
		t.Fatalf("policy or historical data lost: %d %s", w.Code, w.Body)
	}
}

func TestKBProjectPolicyAndSourceClosureLeaveCancelledTaskClosed(t *testing.T) {
	ctx := context.Background()
	s := newKBService(t, newFakeHub())
	e := kbEng(t, s)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	chat := &kb.Chat{ID: "example-pr", Kind: "pr", State: "merged", StateAt: now.Add(-7 * 24 * time.Hour),
		SourceURL: "https://github.com/sample/garden/pull/4"}
	if err := e.Store.PutChat(chat); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyUser(kb.KindProject, "garden", kb.UserUpdate{ProjectMode: new("reference-only"), Update: kb.Update{Chats: []string{chat.ID}}}); err != nil {
		t.Fatal(err)
	}
	before, err := s.insertTodo(ctx, db.TodoRow{ID: "example-task", Title: "Cancelled verification", Project: "garden",
		CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if done, err := s.reviewDoneTodo(ctx, before.ID, "No verification requested", false); err != nil || !done {
		t.Fatalf("cancel the fixture: done=%v err=%v", done, err)
	}
	before, _, err = s.todo(ctx, before.ID)
	if err != nil || !before.Cancelled() || before.DoneAt == nil {
		t.Fatalf("cancelled fixture: %+v (%v)", before, err)
	}
	if n := e.RefreshProjects(ctx, 1); n != 0 || len(e.FinishProjects(now, s.kbProjectBusy(ctx, e))) != 0 {
		t.Fatal("the reference-only project started maintenance")
	}
	after, ok, err := s.todo(ctx, before.ID)
	if err != nil || !ok || !after.Cancelled() || after.DoneAt == nil || !after.DoneAt.Equal(*before.DoneAt) || len(e.Store.Log("garden", 10)) != 0 {
		t.Fatalf("cancelled task or project timeline changed: %+v (%v)", after, err)
	}
}
