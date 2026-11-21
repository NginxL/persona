package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestConversationOrderSetting(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	put := func(body string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := put(`{"conversation_order":[" second ","first","second","archived-but-valid"]}`)
	if code != http.StatusOK || !reflect.DeepEqual(out["conversation_order"], []any{"second", "first", "archived-but-valid"}) {
		t.Fatalf("normalized order: %d %v", code, out)
	}
	if got := s.settings(t.Context()).ConversationOrder; !reflect.DeepEqual(got, []string{"second", "first", "archived-but-valid"}) {
		t.Fatalf("saved order: %v", got)
	}
	// An unrelated write and an omitted field leave the order alone, including an id not currently listed.
	code, out = put(`{"main_background_folded":true}`)
	if code != http.StatusOK || !reflect.DeepEqual(out["conversation_order"], []any{"second", "first", "archived-but-valid"}) {
		t.Fatalf("unrelated write keeps order: %d %v", code, out)
	}
	code, out = put(`{"conversation_order":["first","bad id"]}`)
	if code != http.StatusBadRequest || !reflect.DeepEqual(s.settings(t.Context()).ConversationOrder, []string{"second", "first", "archived-but-valid"}) {
		t.Fatalf("invalid write changes nothing: %d %v", code, out)
	}
	code, out = put(`{"conversation_order":[]}`)
	if code != http.StatusOK || out["conversation_order"] != nil || len(s.settings(t.Context()).ConversationOrder) != 0 {
		t.Fatalf("empty order restores activity time: %d %v", code, out)
	}
}

func TestNormalizeConversationOrderBounds(t *testing.T) {
	if got, err := normalizeConversationOrder([]string{"", " a ", "a", "b"}); err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("normalize: %v %v", got, err)
	}
	if _, err := normalizeConversationOrder(make([]string, conversationOrderMax+1)); err == nil {
		t.Fatal("oversized order accepted")
	}
}
