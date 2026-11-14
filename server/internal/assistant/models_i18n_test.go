package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The model config, its errors and the launchers' checks read in the relayed request's language (Accept-Language),
// else the user's; with neither they are the Chinese they were before i18n.
func TestModelConfigSpeaksTheRequestLanguage(t *testing.T) {
	s, _, _ := modelService(t)
	get := func(path, lang string) assistantwire.HTTPResponse {
		h := map[string][]string{}
		if lang != "" {
			h["Accept-Language"] = []string{lang}
		}
		res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: "GET", Path: path, Header: h}, s.lang)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, c := range []struct{ lang, name, group, use, child string }{
		{"", "主对话", "默认模型", "定时任务", "信息处理"},
		{"en-US", "Main chat", "Default models", "Scheduled tasks", "Processing"},
		{"ja", "メインチャット", "デフォルトのモデル", "定期タスク", "情報処理"},
	} {
		res := get("/api/v1/model-config", c.lang)
		if res.Status != 200 {
			t.Fatalf("%q: %d %s", c.lang, res.Status, res.Body)
		}
		var out struct {
			Purposes []ModelPurpose `json:"purposes"`
		}
		if err := json.Unmarshal(res.Body, &out); err != nil {
			t.Fatal(err)
		}
		p := out.Purposes[0]
		if p.ID != "assistant" || p.Name != c.name || p.Group != c.group || len(p.Uses) != 3 || p.Uses[1].Name != c.use {
			t.Fatalf("%q: %+v", c.lang, p)
		}
		var detail struct {
			Purpose ModelPurpose `json:"purpose"`
		}
		if err := json.Unmarshal(get("/api/v1/model-config/purposes/translate", c.lang).Body, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Purpose.InheritedFrom != c.child {
			t.Fatalf("%q: inherited from %q", c.lang, detail.Purpose.InheritedFrom)
		}
	}
	for lang, want := range map[string]string{"": "没有这个模型用途", "en": "No such model use", "ja": "このモデルの用途はありません"} {
		res := get("/api/v1/model-config/purposes/missing", lang)
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(res.Body, &body)
		if res.Status != http.StatusNotFound || body.Error != want {
			t.Fatalf("%q: %d %q", lang, res.Status, body.Error)
		}
	}

	err := launcherInput{Name: "x", Kind: "nope"}.check()
	if err.Error() != "接入类型不支持" || i18n.Localize(err, i18n.En) != "Unsupported connection type" || i18n.Localize(err, i18n.Ja) != "対応していない接続の種類です" {
		t.Errorf("launcher check: %q / %q", err, i18n.Localize(err, i18n.En))
	}
	err = launcherInput{Name: "ok", BaseURL: "https://example.com", Model: "m", Args: []string{"x"}}.check()
	if err.Error() != `额外参数 "x" 要以 - 开头` || i18n.Localize(err, i18n.En) != `Extra argument "x" must start with -` {
		t.Errorf("launcher args: %q / %q", err, i18n.Localize(err, i18n.En))
	}
}

// A provider check's failure reads in the request's language, with the gateway's own words and no key (#2455).
func TestProviderTestSpeaksTheRequestLanguage(t *testing.T) {
	s, _ := launcherTestService(t)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key ` + r.Header.Get("x-api-key") + `"}}`))
	}))
	defer gw.Close()
	for lang, want := range map[string]string{"": "认证失败（401）", "en": "Authentication failed (401)", "ja": "認証に失敗しました（401）"} {
		header := map[string][]string{}
		if lang != "" {
			header["Accept-Language"] = []string{lang}
		}
		res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: "POST", Path: "/api/v1/launchers/test", Header: header,
			Body: []byte(`{"base_url":"` + gw.URL + `","api_key":"` + fakeKey + `","model":"m"}`)}, s.lang)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(res.Body, &out)
		msg, _ := out["error"].(string)
		if !strings.HasPrefix(msg, want) || !strings.Contains(msg, "invalid key ***") || strings.Contains(msg, fakeKey) {
			t.Fatalf("%q: %q", lang, msg)
		}
	}
}

// A launcher's field errors read in each language and keep the values they name.
func TestLauncherProviderErrorsInEachLanguage(t *testing.T) {
	neg, effort := -1, "turbo"
	models := []proto.ProviderModel{{ID: "m1", Efforts: []string{"high"}, DefaultEffort: "max"}}
	for _, c := range []struct {
		in       launcherInput
		zh, en   string
		contains string
	}{
		{launcherInput{TimeoutMS: &neg}, "超时不能是负数", "Timeout can’t be negative", ""},
		{launcherInput{Effort: &effort}, "默认的思考档位 \"turbo\" 不支持", "Default thinking effort \"turbo\" isn’t supported", "low"},
		{launcherInput{Models: &models}, "模型 m1 的默认思考档位不在它支持的档位里", "The default thinking effort of model m1 isn’t one it supports", ""},
	} {
		err := c.in.checkProvider()
		if err == nil {
			t.Fatalf("%+v: no error", c.in)
		}
		zh, en := err.Error(), i18n.Localize(err, i18n.En)
		if !strings.HasPrefix(zh, c.zh) || !strings.HasPrefix(en, c.en) || !strings.Contains(en, c.contains) {
			t.Fatalf("zh %q / en %q", zh, en)
		}
	}
	if err := checkHeaders("X-Ok: 1\nbroken"); i18n.Localize(err, i18n.En) != "Header line 2 must be “Name: value”" {
		t.Fatalf("headers: %v", err)
	}
	if err := checkEnv("A=1\nbroken"); i18n.Localize(err, i18n.En) != "Environment line 2 must be NAME=value, NAME made of letters, digits and _" {
		t.Fatalf("env: %v", err)
	}
	if err := checkEnv(strings.Repeat("A=1\n", 51)); i18n.Localize(err, i18n.En) != "At most 50 environment variables" {
		t.Fatalf("env count: %v", err)
	}
}

// Launcher role labels follow each request, even when the hub's language differs and the launcher list is cached.
func TestAntigravityLauncherRolesSpeakTheRequestLanguage(t *testing.T) {
	s, f, _ := modelService(t)
	f.mu.Lock()
	f.hello.Locale = "ja"
	f.launcherList = []proto.Launcher{{Name: "agy", Source: "agy", Agent: "agy", Model: "fallback-model"}}
	f.mu.Unlock()
	for _, discovered := range []bool{false, true} {
		f.mu.Lock()
		f.agentModels = map[string][]proto.AgentModel{}
		if discovered {
			f.agentModels["agy"] = []proto.AgentModel{
				{ID: "main-id", Name: "Main model", Default: true},
				{ID: "other-id", Name: "Other model"},
			}
		}
		f.mu.Unlock()
		for _, tc := range []struct{ locale, main, optional string }{
			{"en-US", "Default model", "Optional model"},
			{"zh-Hans", "默认主模型", "可选模型"},
			{"ja", "既定のメインモデル", "選択可能なモデル"},
			{"", "既定のメインモデル", "選択可能なモデル"},
			{"en", "Default model", "Optional model"},
		} {
			header := map[string][]string{}
			if tc.locale != "" {
				header["Accept-Language"] = []string{tc.locale}
			}
			res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
				Method: "GET", Path: "/api/v1/launchers/agy", Header: header,
			}, s.lang)
			if err != nil || res.Status != http.StatusOK {
				t.Fatalf("discovered=%v locale=%q: %d %s %v", discovered, tc.locale, res.Status, res.Body, err)
			}
			var out struct {
				Launcher LauncherView `json:"launcher"`
			}
			if err := json.Unmarshal(res.Body, &out); err != nil {
				t.Fatal(err)
			}
			models := out.Launcher.Details["server"].Models
			want := []proto.LauncherModel{{Role: tc.main, Env: "agy acp", Model: "fallback-model"}}
			if discovered {
				want = []proto.LauncherModel{
					{Role: tc.main, Env: "main-id", Model: "Main model"},
					{Role: tc.optional, Env: "other-id", Model: "Other model"},
				}
			}
			if !slices.Equal(models, want) {
				t.Fatalf("discovered=%v locale=%q: models=%+v, want %+v", discovered, tc.locale, models, want)
			}
		}
	}
}
