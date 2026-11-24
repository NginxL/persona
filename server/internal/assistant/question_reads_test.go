package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// questionReadMem is the fleet's store with the read marks of chats' answers (db.DB's question_reads).
type questionReadMem struct {
	*fleetMem
	since time.Time
	ids   map[[2]string][]string
}

func (m *questionReadMem) QuestionReads(_ context.Context, machine, session string) (time.Time, []string, error) {
	return m.since, slices.Clone(m.ids[[2]string{machine, session}]), nil
}

func (m *questionReadMem) MarkQuestionsRead(_ context.Context, machine, session string, ids []string, _ time.Time) error {
	k := [2]string{machine, session}
	for _, id := range ids {
		if !slices.Contains(m.ids[k], id) {
			m.ids[k] = append(m.ids[k], id)
		}
	}
	return nil
}

func questionReadsCall(t *testing.T, s *Service, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The question rail's read marks (#13021): GET answers when marks began and the marked questions, POST adds marks once
// each, bad bodies and unknown computers are refused, and a store without marks answers 404 so the web keeps its own.
func TestQuestionReadsRoutes(t *testing.T) {
	st := &questionReadMem{fleetMem: newFleetMem(), since: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), ids: map[[2]string][]string{}}
	ft := newFleetTestOn(t, st)
	const path = "/api/v1/sessions/mac/main/question-reads"
	code, out := questionReadsCall(t, ft.s, http.MethodGet, path, "")
	if ids, ok := out["ids"].([]any); code != http.StatusOK || out["since"] != "2026-11-01T09:00:00Z" || !ok || len(ids) != 0 {
		t.Fatalf("first read = %d %v", code, out)
	}
	if code, out := questionReadsCall(t, ft.s, http.MethodPost, path, `{"ids":["u1","u2","u1"]}`); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("mark = %d %v", code, out)
	}
	if got := st.ids[[2]string{"mac", "main"}]; !slices.Equal(got, []string{"u1", "u2"}) {
		t.Fatalf("kept = %v", got)
	}
	_, out = questionReadsCall(t, ft.s, http.MethodGet, path, "")
	if ids, _ := out["ids"].([]any); len(ids) != 2 || ids[0] != "u1" {
		t.Fatalf("read back = %v", out)
	}
	tooMany := `{"ids":[` + strings.TrimSuffix(strings.Repeat(`"x",`, questionReadsPerPost+1), ",") + `]}`
	for _, body := range []string{`{"ids":[""]}`, `{"ids":["` + strings.Repeat("a", questionReadMaxLen+1) + `"]}`, tooMany, `not json`} {
		if code, _ := questionReadsCall(t, ft.s, http.MethodPost, path, body); code != http.StatusBadRequest {
			t.Fatalf("POST %.40s… = %d, want 400", body, code)
		}
	}
	if code, _ := questionReadsCall(t, ft.s, http.MethodGet, "/api/v1/sessions/nowhere/main/question-reads", ""); code != http.StatusNotFound {
		t.Fatalf("unknown computer = %d", code)
	}

	plain := newFleetTestOn(t, newFleetMem())
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if code, _ := questionReadsCall(t, plain.s, method, path, `{"ids":["u1"]}`); code != http.StatusNotFound {
			t.Fatalf("%s without marks = %d, want 404", method, code)
		}
	}
}
