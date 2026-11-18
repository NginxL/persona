package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/egress"
)

// The real settings route, RPC process and source Unix broker, with only the guest runtime faked. Restart notifications
// stay off so the running registration remains at saved A while the registry checks and commits (or refuses) candidate B.
func TestSourceSettingsCheckCandidateFlow(t *testing.T) {
	noBwrap(t)
	setVarFor(t, &settingsCheckTimeout, 2*time.Second)
	h, _ := extensionTestHub(t)
	ps := h.extensions
	ps.sandboxes.noDefault = false
	const id = "example-gateway"
	socket := filepath.Join(shortSocketDir(t), "runtime.sock")
	dir := settingsCheckPackage(t)
	manifestPath := filepath.Join(dir, extension.ManifestFile)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["permissions"].(map[string]any)["network"] = []string{"config:gateway_url:http"}
	properties := manifest["configuration"].(map[string]any)["properties"].(map[string]any)
	properties["broker_socket"] = map[string]any{"type": "string", "title": "Broker socket", "default": socket}
	raw, _ = json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	proxyOut := filepath.Join(t.TempDir(), "check-proxy")
	self, _ := os.Executable()
	script := "#!/bin/sh\nCHECK_PROXY_OUT=" + proxyOut + " PERSONA_TEST_PLUGIN=settingscheck exec " + self + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "gw"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	mux, master := h.routes(), h.cfg.Token
	_, preview := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+dir+`"}`)
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, preview["preview_id"], preview["permissions_digest"]))
	waitLong(t, "the extension up", func() bool {
		hp := ps.get(id)
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	saved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("saved A")) }))
	defer saved.Close()
	candidate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("candidate B"))
	}))
	defer candidate.Close()

	reg := ps.reg
	onChange, ordinaryCheck := reg.OnChange, reg.SettingsCheck
	reg.OnChange = nil
	t.Cleanup(func() { reg.OnChange, reg.SettingsCheck = onChange, ordinaryCheck })
	seed, _ := json.Marshal(map[string]string{"gateway_url": saved.URL})
	reg.SettingsCheck = nil
	if err := reg.SetConfig(context.Background(), id, seed, "test"); err != nil {
		t.Fatal(err)
	}
	reg.SettingsCheck = func(ctx context.Context, p *extension.Extension, config json.RawMessage, secrets map[string]string) error {
		manifest := *p.Manifest
		manifest.Runtime = &extension.Runtime{Mode: extension.RuntimeDeno}
		p.Manifest = &manifest
		return ordinaryCheck(ctx, p, config, secrets)
	}
	current := reg.Get(id)
	granted := &extension.Manifest{Permissions: extension.Permissions{Network: current.Record.Granted.Network}}
	px := ps.sandboxes.proxyIfStarted()
	if px == nil {
		t.Fatal("no source egress proxy")
	}
	px.Register(id, egress.Rule{Targets: extension.ConfiguredTargets(granted, reg.Config(current))})
	if err := px.ListenRuntimeUnix(id, socket); err != nil {
		t.Fatal(err)
	}

	putConfig := func(gateway string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"config": map[string]string{"gateway_url": gateway}})
		return call(t, mux, "PUT", "/api/v1/extensions/"+id+"/-/config", master, string(body))
	}
	givenProxy := func() (string, string) {
		t.Helper()
		raw, err := os.ReadFile(proxyOut)
		if err != nil || len(raw) == 0 {
			t.Fatalf("the source guest got no check proxy: %v", err)
		}
		u, err := url.Parse(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		cred, _ := u.User.Password()
		return string(raw), cred
	}
	savedGateway := func() string {
		t.Helper()
		var config map[string]any
		if err := json.Unmarshal(reg.Config(reg.Get(id)), &config); err != nil {
			t.Fatal(err)
		}
		value, _ := config["gateway_url"].(string)
		return value
	}
	key := "ag-good-key-0123456789-7f3a"
	body, _ := json.Marshal(map[string]string{"value": key})
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/"+id+"/-/secrets/key", master, string(body)); code != http.StatusOK {
		t.Fatalf("checking a secret against saved A: %d %v", code, out)
	}

	code, out := putConfig(candidate.URL + "/fail")
	proxy, cred := givenProxy()
	message, _ := out["error"].(string)
	if code != http.StatusBadRequest || out["code"] != "check_failed" || !strings.Contains(message, "candidate returned") || strings.Contains(message, key) || strings.Contains(message, cred) {
		t.Fatalf("refused candidate B was not scrubbed: %d %v", code, out)
	}
	if got := savedGateway(); got != saved.URL {
		t.Fatalf("a refused candidate saved %q, want saved A %q", got, saved.URL)
	}
	if status, err := runtimeCheckFetch(t.Context(), socket, proxy, candidate.URL+"/ok"); err != nil || status != http.StatusForbidden {
		t.Fatalf("a refused check's credential afterwards: %d %v", status, err)
	}
	if status, err := runtimeCheckFetch(t.Context(), socket, "", saved.URL); err != nil || status != http.StatusOK {
		t.Fatalf("ordinary saved A after refusal: %d %v", status, err)
	}
	if status, err := runtimeCheckFetch(t.Context(), socket, "", candidate.URL+"/ok"); err != nil || status != http.StatusForbidden {
		t.Fatalf("candidate B entered the ordinary grant set: %d %v", status, err)
	}

	if code, out := putConfig(candidate.URL + "/ok"); code != http.StatusOK {
		t.Fatalf("candidate B could not be checked before save: %d %v", code, out)
	}
	if got := savedGateway(); got != candidate.URL+"/ok" {
		t.Fatalf("a passed candidate saved %q", got)
	}
	proxy, _ = givenProxy()
	if status, err := runtimeCheckFetch(t.Context(), socket, proxy, candidate.URL+"/ok"); err != nil || status != http.StatusForbidden {
		t.Fatalf("a passed check's credential afterwards: %d %v", status, err)
	}
}
