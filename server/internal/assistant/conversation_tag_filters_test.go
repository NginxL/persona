package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestConversationTagFilterSetting(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	put := func(role, body string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body))
		req.Header.Set(RoleHeader, role)
		s.ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// No preference is automatic. An omitted write leaves it automatic and null is explicit in the snapshot.
	code, out := put(roleFull, `{"main_background_folded":true}`)
	if code != http.StatusOK || out["conversation_tag_filters"] != nil || s.settings(t.Context()).ConversationTagFilters != nil {
		t.Fatalf("unset filters: %d %v", code, out)
	}
	code, out = put(roleFull, `{"conversation_tag_filters":[" planning ","trip","planning","not-currently-in-use"]}`)
	want := []any{"planning", "trip", "not-currently-in-use"}
	if code != http.StatusOK || !reflect.DeepEqual(out["conversation_tag_filters"], want) || !reflect.DeepEqual(s.settings(t.Context()).ConversationTagFilters, []string{"planning", "trip", "not-currently-in-use"}) {
		t.Fatalf("custom filters: %d %v", code, out)
	}
	// Absent and unrelated writes keep the exact list; read-only cannot replace it.
	code, out = put(roleFull, `{"main_background_folded":false}`)
	if code != http.StatusOK || !reflect.DeepEqual(out["conversation_tag_filters"], want) {
		t.Fatalf("omitted write: %d %v", code, out)
	}
	code, _ = put("answer", `{"conversation_tag_filters":[]}`)
	if code != http.StatusForbidden || !reflect.DeepEqual(s.settings(t.Context()).ConversationTagFilters, []string{"planning", "trip", "not-currently-in-use"}) {
		t.Fatalf("read-only write: %d", code)
	}
	// An empty array is a real customized value, not the automatic default.
	code, out = put(roleFull, `{"conversation_tag_filters":[]}`)
	filters, ok := out["conversation_tag_filters"].([]any)
	if code != http.StatusOK || !ok || len(filters) != 0 || s.settings(t.Context()).ConversationTagFilters == nil || len(s.settings(t.Context()).ConversationTagFilters) != 0 {
		t.Fatalf("custom empty filters: %d %v", code, out)
	}
	// JSON null resets to automatic; it is distinct from an absent field.
	code, out = put(roleFull, `{"conversation_tag_filters":null}`)
	if code != http.StatusOK || out["conversation_tag_filters"] != nil || s.settings(t.Context()).ConversationTagFilters != nil {
		t.Fatalf("null resets filters: %d %v", code, out)
	}
	code, _ = put(roleFull, `{"conversation_tag_filters":["kept","bad id does not matter","xx"]}`)
	if code != http.StatusOK {
		t.Fatalf("filter labels are not conversation ids: %d", code)
	}
	before := s.settings(t.Context()).ConversationTagFilters
	code, _ = put(roleFull, `{"conversation_tag_filters":["replacement",`+strconv.Quote(strings.Repeat("好", conversationTagMaxRunes+1))+`]}`)
	if code != http.StatusBadRequest || !reflect.DeepEqual(s.settings(t.Context()).ConversationTagFilters, before) {
		t.Fatalf("invalid write must be atomic: %d", code)
	}
}

func TestNormalizeConversationTagFilters(t *testing.T) {
	got, err := normalizeConversationTagFilters([]string{" planning ", "", "planning", "trip"})
	if err != nil || !reflect.DeepEqual(got, []string{"planning", "trip"}) {
		t.Fatalf("normalize: %v %v", got, err)
	}
	eleven := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	if got, err = normalizeConversationTagFilters(eleven); err != nil || len(got) != len(eleven) {
		t.Fatalf("filter entries do not use the per-conversation tag cap: %v %v", got, err)
	}
	tooMany := make([]string, conversationTagFiltersMax+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("x", i+1)
	}
	if _, err = normalizeConversationTagFilters(tooMany); err == nil {
		t.Fatal("oversized filter set accepted")
	}
}
