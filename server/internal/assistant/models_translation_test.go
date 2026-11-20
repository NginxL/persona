package assistant

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func translationProvider(t *testing.T, s *Service, name, model, haiku string) {
	t.Helper()
	key, err := s.sealKey("translation-test-key")
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{
		ManagedLauncher: proto.ManagedLauncher{Name: name, Kind: "api", API: "messages", BaseURL: "https://api.example.com", Model: model, HaikuModel: haiku, Effort: "high"},
		KeySealed:       key,
	})
}

func selectTranslationScreeningProvider(t *testing.T, s *Service, provider, model string) {
	t.Helper()
	p := purposeOf(t, s, "inbox.triage")
	body := fmt.Sprintf(`{"revision":%d,"provider":%q,"model":%q}`, p.Revision, provider, model)
	if r := modelRequest(s, http.MethodPatch, "inbox.triage", body); r.Code != http.StatusOK {
		t.Fatalf("set screening binding: %d %s", r.Code, r.Body.String())
	}
}

func TestTranslateIsAnEditableIndependentCategory(t *testing.T) {
	s, _, _ := modelService(t)
	p := purposeOf(t, s, "translate")
	if p.CategoryID != "translate" || p.Parent != "" || p.Kind != "api" || len(p.Fields) == 0 {
		t.Fatalf("translation category is not independently editable: %+v", p)
	}
	if len(p.Uses) != 1 || p.Uses[0].Name == "" {
		t.Fatalf("translation category does not own its use: %+v", p.Uses)
	}
	ids := modelCategoryIDs
	if ids[len(ids)-1] != "translate" {
		t.Fatalf("translation category is not last among categories: %v", ids)
	}
}

func TestTranslateUsesCheapClaudeDefaultAndManagedHaiku(t *testing.T) {
	for _, tc := range []struct{ name, configured, want string }{
		{name: "built-in Haiku", want: "claude-haiku-5-5"},
		{name: "managed Haiku", configured: "gateway-haiku", want: "gateway-haiku"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := modelService(t)
			translationProvider(t, s, "claude-gateway", "claude-sonnet-5-5", tc.configured)
			selectTranslationScreeningProvider(t, s, "launcher.claude-gateway", "claude-sonnet-5-5")
			b, err := s.resolveModel(context.Background(), "translate")
			if err != nil || b.Provider != "launcher.claude-gateway" || b.Model != tc.want || b.Effort != "off" {
				t.Fatalf("translation default = %+v, %v; want model %q and reasoning off", b, err, tc.want)
			}
			if c, err := s.apiConfig(context.Background(), b); err != nil || c.ReasoningEffort != "off" {
				t.Fatalf("translation inherited an expensive provider effort: %+v, %v", c, err)
			}
		})
	}
}

func TestTranslateKeepsNonClaudeScreeningModel(t *testing.T) {
	s, _, _ := modelService(t)
	translationProvider(t, s, "vendor-gateway", "vendor-model", "")
	selectTranslationScreeningProvider(t, s, "launcher.vendor-gateway", "vendor-model")
	b, err := s.resolveModel(context.Background(), "translate")
	if err != nil || b.Provider != "launcher.vendor-gateway" || b.Model != "vendor-model" {
		t.Fatalf("non-Claude translation default = %+v, %v", b, err)
	}
}

func TestTranslatePatchDoesNotChangeOtherCategories(t *testing.T) {
	s, _, _ := modelService(t)
	title := purposeOf(t, s, purposeMeetingTitle)
	if title.Parent != "kb" || title.CategoryID != "kb" {
		t.Fatalf("meeting titles no longer follow information processing: %+v", title)
	}
	translationProvider(t, s, "translation", "translation-default", "")
	for id, binding := range map[string]ModelBinding{
		"assistant":    {Provider: "launcher.main", Model: "main-model"},
		"kb":           {Provider: "legacy.kb", Model: "knowledge-model"},
		"inbox.triage": {Provider: "legacy.triage", Model: "screening-model"},
	} {
		setModel(t, s, id, binding)
	}
	p := purposeOf(t, s, "translate")
	r := modelRequest(s, http.MethodPatch, "translate", fmt.Sprintf(`{"revision":%d,"provider":"launcher.translation","model":"translation-model"}`, p.Revision))
	if r.Code != http.StatusOK {
		t.Fatalf("patch translation: %d %s", r.Code, r.Body.String())
	}
	got, err := s.resolveModel(context.Background(), "translate")
	if err != nil || got.Provider != "launcher.translation" || got.Model != "translation-model" {
		t.Fatalf("translation binding = %+v, %v", got, err)
	}
	for id, want := range map[string]ModelBinding{
		"assistant":         {Provider: "launcher.main", Model: "main-model"},
		"kb":                {Provider: "legacy.kb", Model: "knowledge-model"},
		"inbox.triage":      {Provider: "legacy.triage", Model: "screening-model"},
		purposeMeetingTitle: {Provider: "legacy.kb", Model: "knowledge-model"},
	} {
		got, err := s.resolveModel(context.Background(), id)
		if err != nil || got.Provider != want.Provider || got.Model != want.Model {
			t.Fatalf("editing translation changed %s: %+v, %v", id, got, err)
		}
	}
}
