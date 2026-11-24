package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/i18n"
)

func extensionLanguageRequest(t *testing.T, server *httptest.Server, token string, lang i18n.Lang, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if lang != "" {
		req.Header.Set("Accept-Language", string(lang))
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, out
}

func TestExtensionStatusHTTP(t *testing.T) {
	h := newTestManager(t)
	store := newExtensionStore()
	h.store = store
	raw := []byte(`{"manifest_version":1,"id":"example-status","name":"作者名称","description":"作者的说明","version":"1.0.0",
		"engines":{"persona":">=1"},"permissions":{"hub_api":true},"contributes":{"future_status":[{"id":"future"}]},
		"configuration":{"type":"object","properties":{"token_hint":{"type":"string"},"items":{"type":"array","items":{"type":"string"}}}}}`)
	store.rows["example-status"] = extension.Record{ID: "example-status", Source: extension.SourceInstalled, Version: "1.0.0", Enabled: true, Manifest: raw}
	startTestExtensions(t, h)
	server := httptest.NewServer(h.routes())
	defer server.Close()
	p := h.extensions.reg.Get("example-status")
	canonicalProblem, canonicalWarnings := p.Problem, fmt.Sprint(p.Warnings)
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja, i18n.En} {
		t.Run(string(l), func(t *testing.T) {
			request := func(method, path, body string) (int, map[string]any) {
				return extensionLanguageRequest(t, server, h.cfg.Token, l, method, path, body)
			}
			for _, path := range []string{"/api/v1/extensions/missing-extension/-/", "/api/v1/extensions/missing-extension/-/auth"} {
				code, out := request("GET", path, "")
				if code != 404 || out["code"] != "not_found" || out["error"] != i18n.T(l, "extension-status.missing_extension") {
					t.Fatalf("%s: HTTP %d %v", path, code, out)
				}
			}
			code, out := request("GET", "/api/v1/extensions/example-status/-/auth", "")
			if code != 404 || out["code"] != "no_auth" || out["error"] != i18n.T(l, "hub-extension_auth.no_auth") {
				t.Fatalf("no auth: HTTP %d %v", code, out)
			}
			code, out = request("GET", "/api/v1/extensions/example-status/-/", "")
			if code != 200 || out["error"] != i18n.T(l, "extension-status.package_missing", i18n.Args{"path": p.Dir}) || out["name"] != "作者名称" || out["description"] != "作者的说明" {
				t.Fatalf("detail: HTTP %d %v", code, out)
			}
			warnings := fmt.Sprint(out["warnings"])
			for _, key := range []string{"legacy_full_token", "dev_engines", "plaintext_secret"} {
				args := i18n.Args{"name": "token_hint", "required": ">=1"}
				if !strings.Contains(warnings, i18n.T(l, "extension-status."+key, args)) {
					t.Fatalf("warning %s absent: %v", key, out)
				}
			}
			code, out = request("PUT", "/api/v1/extensions/example-status/-/config", `{"config":{"items":["x",0]}}`)
			wantMessage := i18n.T(l, "extension-status.schema_item", i18n.Args{"count": 2, "err": i18n.T(l, "extension-status.schema_string")})
			if code != 400 || out["field"] != "items" || out["message"] != wantMessage || out["error"] != "items: "+wantMessage {
				t.Fatalf("nested validation: HTTP %d %v; want %s", code, out, wantMessage)
			}
			code, out = request("POST", "/api/v1/extensions/missing-extension/-/ui/page", `{}`)
			if code != 404 || out["error"] != i18n.T(l, "extension-status.missing_extension") {
				t.Fatalf("missing UI extension: HTTP %d %v", code, out)
			}
			code, out = request("POST", "/api/v1/extensions/-/install", `{"preview_id":"expired-example","permissions_digest":"example"}`)
			if code != 400 || out["code"] != "install_failed" || out["error"] != i18n.T(l, "extension-status.failed") || out["detail"] == nil {
				t.Fatalf("install diagnostic: HTTP %d %v", code, out)
			}
			code, out = request("POST", "/api/v1/extensions/example-status/-/rollback", `{}`)
			if code != 400 || out["code"] != "rollback_failed" || out["error"] != i18n.T(l, "extension-status.failed") || out["detail"] == nil {
				t.Fatalf("rollback diagnostic: HTTP %d %v", code, out)
			}
		})
	}
	if p.Problem != canonicalProblem || fmt.Sprint(p.Warnings) != canonicalWarnings {
		t.Fatal("request projection changed the canonical registry state")
	}
	code, out := extensionLanguageRequest(t, server, h.cfg.Token, "", "GET", "/api/v1/extensions/missing-extension/-/auth", "")
	if code != 404 || out["error"] != "no such plugin" || errNoExtension.Error() != "no such plugin" {
		t.Fatalf("no-locale legacy error: HTTP %d %v", code, out)
	}
}

func TestExtensionMarketAndSandboxStatusLanguage(t *testing.T) {
	h, _ := extensionTestHub(t)
	ms := h.marketsState()
	ms.loaded = true
	canonical := i18n.T(i18n.Default, "extension-market_git.git_missing")
	ms.byID = map[string]*marketState{"example-market": {rec: extension.MarketRecord{ID: "example-market", Spec: "github:example/extensions", Name: "Author market", Error: canonical}}}
	server := httptest.NewServer(h.routes())
	defer server.Close()
	oldProbe := sandboxProbe
	t.Cleanup(func() { sandboxProbe = oldProbe })
	sandboxProbe = func() sandbox.Support {
		return sandbox.Support{OS: "linux", Why: i18n.T(i18n.Default, "extension-status.sandbox_no_bwrap")}
	}
	h.extensions.sandboxes.settings.On = map[string]bool{"example-sandbox": true}
	manifest := &extension.Manifest{ID: "example-sandbox", Runtime: &extension.Runtime{}}
	p := &extension.Extension{Record: extension.Record{ID: manifest.ID, Source: extension.SourceInstalled}, Manifest: manifest, Full: manifest}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		code, out := extensionLanguageRequest(t, server, h.cfg.Token, l, "GET", "/api/v1/extensions/-/markets", "")
		markets, _ := out["markets"].([]any)
		if code != 200 || len(markets) != 1 || markets[0].(map[string]any)["error"] != i18n.T(l, "extension-market_git.git_missing") {
			t.Fatalf("market %s: HTTP %d %v", l, code, out)
		}
		state := h.extensions.sandboxOf(i18n.With(context.Background(), l), p)
		want := i18n.T(l, "hub-extension_sandbox.start_blocked", i18n.Args{"why": i18n.T(l, "extension-status.sandbox_no_bwrap")})
		if state.Why != want {
			t.Fatalf("sandbox %s: %q; want %q", l, state.Why, want)
		}
	}
	if ms.byID["example-market"].rec.Error != canonical {
		t.Fatal("market cache was translated in place")
	}
}

func TestExtensionAuthCacheLanguage(t *testing.T) {
	h, _ := extensionTestHub(t)
	m := &extension.Manifest{ManifestVersion: 1, ID: "example-auth-cache", Name: "Auth", Version: "1.0.0", Auth: &extension.AuthSpec{Title: "作者的登录"}}
	if err := h.extensions.reg.AddBuiltin(m, "", true); err != nil {
		t.Fatal(err)
	}
	p := h.extensions.reg.Get(m.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc := host.Start(ctx, host.Spec{Name: "Example Auth", OnCall: true})
	defer proc.Stop()
	h.extensions.byID[m.ID] = &hubExtension{proc: proc}
	server := httptest.NewServer(h.routes())
	defer server.Close()
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		code, out := extensionLanguageRequest(t, server, h.cfg.Token, l, "GET", "/api/v1/extensions/"+m.ID+"/-/auth", "")
		if code != 200 || out["state"] != authError || out["title"] != m.Auth.Title || out["message"] != i18n.T(l, "extension-status.no_protocol", i18n.Args{"name": "Example Auth"}) {
			t.Fatalf("auth status %s: HTTP %d %v", l, code, out)
		}
	}
	canonical, _, _ := h.extensions.auth.get(m.ID)
	if canonical.Message != i18n.T(i18n.En, "extension-status.no_protocol", i18n.Args{"name": "Example Auth"}) {
		t.Fatal("auth cache was translated in place")
	}
	h.extensions.auth.put(m.ID, authStatus{State: authError, Message: "not answering"})
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		view := h.extensions.authView(p, l)
		if view.Message != "not answering" {
			t.Fatalf("authored message was translated: %+v", view)
		}
	}
}

func TestExtensionUILocaleAndDiagnosticsHTTP(t *testing.T) {
	h, _ := extensionTestHub(t)
	pv, err := h.extensions.reg.Preview(context.Background(), "dir:"+uiPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.extensions.reg.Install(context.Background(), pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.routes())
	defer server.Close()
	waitLong(t, "the UI program", func() bool {
		p := h.extensions.get("example-ui")
		return p != nil && p.proc != nil && p.proc.Ready()
	})
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		code, out := extensionLanguageRequest(t, server, h.cfg.Token, l, "POST", "/api/v1/extensions/example-ui/-/ui/nav", `{"context":{"locale":"fr-FR","ui_version":5}}`)
		if code != 200 || !strings.Contains(fmt.Sprint(out), " "+string(l)+" ") {
			t.Fatalf("request locale %s: HTTP %d %v", l, code, out)
		}
		code, fallback := extensionLanguageRequest(t, server, h.cfg.Token, l, "POST", "/api/v1/extensions/example-ui/-/ui/list", `{"context":{"locale":"fr-FR","ui_version":1}}`)
		if code != 200 {
			t.Fatalf("fallback %s: HTTP %d %v", l, code, fallback)
		}
		items := fallback["view"].(map[string]any)["sections"].([]any)[0].(map[string]any)["items"].([]any)
		seenLoading, seenMore, seenOriginal := false, false, false
		for _, item := range items {
			e := item.(map[string]any)
			seenLoading = seenLoading || e["text"] == i18n.T(l, "extension-ui-fallback.loading")
			seenMore = seenMore || e["title"] == i18n.T(l, "extension-ui-fallback.load_more")
			seenOriginal = seenOriginal || e["title"] == "周会"
		}
		if !seenLoading || !seenMore || !seenOriginal {
			t.Fatalf("header/body/fallback composition %s: %v", l, fallback)
		}
		code, out = extensionLanguageRequest(t, server, h.cfg.Token, l, "POST", "/api/v1/extensions/example-ui/-/ui/broken", `{}`)
		if code != 502 || out["code"] != "plugin_failed" || out["error"] != i18n.T(l, "extension-status.view_invalid") || out["detail"] == nil {
			t.Fatalf("SDK diagnostic: HTTP %d %v", code, out)
		}
	}
	code, legacy := extensionLanguageRequest(t, server, h.cfg.Token, "", "POST", "/api/v1/extensions/example-ui/-/ui/nav", `{"context":{"locale":"en-US","ui_version":5}}`)
	if code != 200 || !strings.Contains(fmt.Sprint(legacy), " en-US ") {
		t.Fatalf("legacy body locale: HTTP %d %v", code, legacy)
	}
	// The authored package, not a translated manifest, stays on disk.
	if _, err := os.Stat(filepath.Join(h.extensions.reg.Get("example-ui").Dir, extension.ManifestFile)); err != nil {
		t.Fatal(err)
	}
}
