package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestReplyTranslationUsesTranslatePurposeAndCache(t *testing.T) {
	h := newInboxHub(t)
	translated := "    npm test\n\nA translated **reply**\n"
	answer, _ := json.Marshal(map[string][]string{"texts": {translated}})
	model, calls := captureModel(t, string(answer))
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = model
	h.in.models[purposeTriage] = fakeModel(t, `{"texts":["Wrong purpose"]}`)
	h.in.mu.Unlock()
	input := "    npm test\n\n**Original** " + strings.Repeat("A long reply with Markdown.\n", 100)
	body, _ := json.Marshal(map[string]string{"text": input, "target": "zh-Hans"})
	for i := range 2 {
		code, result := inboxCall(t, h.routes(), "POST", "/api/v1/translate/message", string(body))
		if code != http.StatusOK || result["text"] != translated {
			t.Fatalf("click %d: %d %v", i, code, result)
		}
	}
	if len(calls()) != 1 {
		t.Fatalf("repeat click called the model %d times", len(calls()))
	}
	var request struct {
		Texts []string `json:"texts"`
	}
	if err := json.Unmarshal([]byte(calls()[0].User), &request); err != nil || len(request.Texts) != 1 || request.Texts[0] != input {
		t.Fatalf("long reply was clipped or context was sent: %d texts (%v)", len(request.Texts), err)
	}
	if !strings.Contains(calls()[0].System, "简体中文") { // i18n-ignore: the prompt's target language
		t.Fatal("did not ask for the interface language")
	}
}

func TestReplyTranslationDoesNotReuseClippedShortText(t *testing.T) {
	h := newInboxHub(t)
	model, calls := captureModel(t, `{"texts":["Translation"]}`)
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = model
	h.in.mu.Unlock()
	text := strings.Repeat("x", translateText+20)
	if _, err := h.translateTexts(context.Background(), []string{text}, "en"); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"text": text, "target": "en"})
	code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/translate/message", string(body))
	if code != http.StatusOK || len(calls()) != 2 || !strings.Contains(calls()[1].User, text) {
		t.Fatalf("used a clipped cache result: HTTP %d, %d model calls", code, len(calls()))
	}
}

func TestReplyTranslationRejectsInvalidInputWithoutCallingModel(t *testing.T) {
	h := newInboxHub(t)
	model, calls := captureModel(t, `{"texts":["Translation"]}`)
	h.in.mu.Lock()
	h.in.models[purposeTranslate] = model
	h.in.mu.Unlock()
	for _, text := range []string{"", "   ", strings.Repeat("x", 32001)} {
		body, _ := json.Marshal(map[string]string{"text": text, "target": "en"})
		if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/translate/message", string(body)); code != http.StatusBadRequest {
			t.Fatalf("invalid input: HTTP %d", code)
		}
	}
	if len(calls()) != 0 {
		t.Fatal("invalid input spent model tokens")
	}
}

func TestReplyTranslationRejectsEmptyModelOutput(t *testing.T) {
	for _, text := range []string{"", " \t "} {
		h := newInboxHub(t)
		answer, _ := json.Marshal(map[string][]string{"texts": {text}})
		h.in.mu.Lock()
		h.in.models[purposeTranslate] = fakeModel(t, string(answer))
		h.in.mu.Unlock()
		if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/translate/message", `{"text":"Original","target":"en"}`); code != http.StatusBadGateway {
			t.Fatalf("empty translation returned HTTP %d", code)
		}
	}
}

func TestReplyTranslationHonorsIndependentModelChanges(t *testing.T) {
	h := newInboxHub(t)
	var mu sync.Mutex
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		mu.Lock()
		models = append(models, in.Model)
		mu.Unlock()
		text, _ := json.Marshal(map[string][]string{"texts": {"Translated by " + in.Model}})
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]string{"type": "text", "text": string(text)}}})
	}))
	t.Cleanup(upstream.Close)
	key, err := h.sealKey("translation-test-key")
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, h.Service, storedLauncher{
		ManagedLauncher: proto.ManagedLauncher{Name: "translator", Kind: "api", API: "messages", BaseURL: upstream.URL, Model: "provider-default"},
		KeySealed:       key,
	})
	original := purposeOf(t, h.Service, "assistant").Override
	for _, model := range []string{"translation-one", "translation-two"} {
		p := purposeOf(t, h.Service, purposeTranslate)
		patch, _ := json.Marshal(map[string]any{"revision": p.Revision, "provider": "launcher.translator", "model": model})
		if result := modelRequest(h.Service, "PATCH", purposeTranslate, string(patch)); result.Code != http.StatusOK {
			t.Fatalf("independent setting: %d %s", result.Code, result.Body.String())
		}
		for range 2 {
			code, result := inboxCall(t, h.routes(), "POST", "/api/v1/translate/message", `{"text":"Original reply","target":"en"}`)
			if code != http.StatusOK || result["text"] != "Translated by "+model {
				t.Fatalf("model %s: %d %v", model, code, result)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(models, ",") != "translation-one,translation-two" {
		t.Fatalf("model changes or cache failed: %v", models)
	}
	if current := purposeOf(t, h.Service, "assistant").Override; current != original {
		t.Fatalf("translation changed the main chat: %+v", current)
	}
}
