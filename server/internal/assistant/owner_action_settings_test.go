package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOwnerApprovalSettingsDefaultOffAndReadBack(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	view := s.view(context.Background(), false)
	if view.OwnerReplyApproval || view.PersistentOwnerActionCards || !reflect.DeepEqual(view.OwnerApprovalPhrases, []string{"发", "同意"}) {
		t.Fatalf("default exact-approval settings: %+v", view)
	}
	if ownerActionPrepareOffered(s) {
		t.Fatal("exact approval tool is offered while its opt-in is off")
	}
	for _, body := range []string{
		`{"owner_reply_approval_phrases":[" 发"]}`,
		`{"owner_reply_approval_phrases":["发","发"]}`,
		`{"owner_reply_approval_phrases":["1","2","3","4","5","6","7","8","9"]}`,
		`{"owner_reply_approval_phrases":[""]}`,
	} {
		w, _ := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", "assistant", "full", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid phrases %s: %d %s", body, w.Code, w.Body)
		}
	}
	w, saved := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", "assistant", "full",
		`{"owner_reply_approval":true,"persistent_owner_action_cards":true,"owner_reply_approval_phrases":["发","Agree"]}`)
	if w.Code != http.StatusOK || !saved.OwnerReplyApproval || !saved.PersistentOwnerActionCards || !reflect.DeepEqual(saved.OwnerApprovalPhrases, []string{"发", "Agree"}) {
		t.Fatalf("save exact-approval settings: %d %+v %s", w.Code, saved, w.Body)
	}
	if !ownerActionPrepareOffered(s) {
		t.Fatal("exact approval tool is hidden after its opt-in is enabled")
	}
	read := httptest.NewRecorder()
	s.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	var reloaded assistantView
	if err := json.Unmarshal(read.Body.Bytes(), &reloaded); err != nil || !reloaded.OwnerReplyApproval || !reloaded.PersistentOwnerActionCards ||
		!reflect.DeepEqual(reloaded.OwnerApprovalPhrases, []string{"发", "Agree"}) {
		t.Fatalf("read back exact-approval settings: %v %+v", err, reloaded)
	}
	w, saved = permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", "assistant", "full",
		`{"owner_reply_approval":false,"persistent_owner_action_cards":false}`)
	if w.Code != http.StatusOK || saved.OwnerReplyApproval || saved.PersistentOwnerActionCards || !reflect.DeepEqual(saved.OwnerApprovalPhrases, []string{"发", "Agree"}) {
		t.Fatalf("reverse exact-approval switches: %d %+v", w.Code, saved)
	}
	if ownerActionPrepareOffered(s) {
		t.Fatal("exact approval tool remains offered after its opt-in is disabled")
	}

	// Empty is a deliberate disable, not an omitted setting. It must survive both JSON persistence and a fresh view;
	// otherwise the next read silently restores 发 / 同意 and a reply the owner explicitly disabled becomes valid again.
	w, saved = permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", "assistant", "full",
		`{"owner_reply_approval_phrases":[]}`)
	if w.Code != http.StatusOK || saved.OwnerApprovalPhrases == nil || len(saved.OwnerApprovalPhrases) != 0 {
		t.Fatalf("save an explicitly empty phrase list: %d %+v %s", w.Code, saved, w.Body)
	}
	persisted := s.settings(context.Background())
	if persisted.OwnerApprovalPhrases == nil || len(persisted.OwnerApprovalPhrases) != 0 {
		t.Fatalf("empty phrase list was not preserved in settings: %#v", persisted.OwnerApprovalPhrases)
	}
	read = httptest.NewRecorder()
	s.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	reloaded = assistantView{}
	if err := json.Unmarshal(read.Body.Bytes(), &reloaded); err != nil || reloaded.OwnerApprovalPhrases == nil || len(reloaded.OwnerApprovalPhrases) != 0 {
		t.Fatalf("read back an explicitly empty phrase list: %v %#v %s", err, reloaded.OwnerApprovalPhrases, read.Body)
	}
	enabled := true
	if ownerApprovalPhrase(Settings{OwnerReplyApproval: &enabled, OwnerApprovalPhrases: persisted.OwnerApprovalPhrases}, "发") {
		t.Fatal("an explicitly empty phrase list restored a default affirmative")
	}
}

func ownerActionPrepareOffered(s *Service) bool {
	for _, tool := range s.Tools(context.Background()) {
		if tool.Name == ownerActionPrepareTool {
			return true
		}
	}
	return false
}
