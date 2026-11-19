package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
)

// serveLiveExtensionForTest has a connector of its own (examplelive), a channel bound to it and a page. Each start
// writes the connectors its initialize lists as active, one line a start, to LIVE_LOG; its sign-in's first step says
// it came from the program.
func serveLiveExtensionForTest() {
	logPath := os.Getenv("LIVE_LOG")
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-live", Version: "1.0.0",
		Init: func(_ context.Context, p extension.InitializeParams, _ *pserve.Hub) (map[string]any, error) {
			f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			_, err = fmt.Fprintln(f, strings.Join(p.Active["connectors"], ","))
			return nil, err
		},
		Namespaces: map[string]pserve.Handler{
			"connector": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				switch method {
				case "status":
					return map[string]string{"state": "ok"}, nil
				case "login.step":
					return map[string]string{"step": "code", "hint": "from the program"}, nil
				}
				return nil, pserve.UnknownMethod(method)
			},
			"ui": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				if method == "render" {
					return map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"type": "text", "text": "live page"}}}}}, nil
				}
				return nil, pserve.UnknownMethod(method)
			}}})
}

// livePackage is the extension serveLiveExtensionForTest runs, started with the hub, its starts written to log.
func livePackage(t *testing.T, log string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "live"), []byte(fmt.Sprintf("#!/bin/sh\nPERSONA_TEST_PLUGIN=live LIVE_LOG=%q exec %s \"$@\"\n", log, self)), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-live", "name": "Example Live", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/live"}}, "activation_events": ["on_startup"],
		"contributes": {
			"connectors": [{"id": "examplelive", "name": "Example Live", "capabilities": ["status", "login"]}],
			"channel": [{"id": "examplelive", "name": "Example Live", "kind": "im", "connector": "examplelive", "capabilities": {}}],
			"ui": [{"id": "panel", "title": "Panel", "placement": "extension_page"}]}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// Switching an extension's connector restarts the extension, so its initialize lists only the connectors that are on
// (#10467): off from its contribution switch (the channel bound to it, which is the connector's switch), on from
// Settings › Connectors, on again by binding the channel, whose sign-in then reaches the program that runs now. Its page
// works throughout. A switch that changes nothing, and the hub's own connector, restart nothing.
func TestConnectorSwitchRestartsItsExtension(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	ctx := context.Background()
	log := filepath.Join(t.TempDir(), "active.log")
	installForTest(t, h, livePackage(t, log))

	starts := func() []string { // what each start's initialize listed, the last line only once it is whole
		raw, _ := os.ReadFile(log)
		lines := strings.Split(string(raw), "\n")
		return lines[:len(lines)-1]
	}
	up := func(n int, active string) *hubExtension {
		t.Helper()
		waitLong(t, fmt.Sprintf("start %d", n), func() bool {
			hp := h.extensions.get("example-live")
			return len(starts()) >= n && hp != nil && hp.proc != nil && hp.proc.Ready()
		})
		if got := starts(); len(got) != n || got[n-1] != active {
			t.Fatalf("started %q, want %d starts, the last with %q active", got, n, active)
		}
		return h.extensions.get("example-live")
	}
	page := func() {
		t.Helper()
		code, out := call(t, mux, "POST", "/api/v1/extensions/example-live/-/ui/panel", master, `{}`)
		if code != 200 || !strings.Contains(fmt.Sprint(out["view"]), "live page") {
			t.Fatalf("the page: %d %v", code, out)
		}
	}
	hp := up(1, "examplelive")
	page()

	// A switch that changes nothing.
	if err := h.setConnectorEnabled(ctx, "examplelive", true); err != nil {
		t.Fatal(err)
	}
	if h.extensions.get("example-live") != hp {
		t.Fatal("restarted for a switch that changed nothing")
	}

	// Off from its contribution switch: restarted once, without it; the page still works.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/example-live/-/contributions/channel/examplelive", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if h.connectors.Enabled("examplelive") {
		t.Fatal("the connector is off")
	}
	if h.extensions.get("example-live") == hp {
		t.Fatal("not restarted")
	}
	hp = up(2, "")
	page()

	// The hub's own connector, switched off and on: nothing restarts.
	if err := h.addConnector(&exampleConnector{}); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} {
		if err := h.setConnectorEnabled(ctx, "example", on); err != nil {
			t.Fatal(err)
		}
	}
	if h.extensions.get("example-live") != hp {
		t.Fatal("restarted for the hub's own connector")
	}

	// On from Settings › Connectors: restarted with it, and the connector registered again stays on.
	if code, out := call(t, mux, "PUT", "/api/v1/connectors/examplelive/settings", master, `{"enabled": true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	up(3, "examplelive")
	if !h.connectors.Enabled("examplelive") {
		t.Fatal("the connector is on")
	}

	// Off again, and on by binding its channel: the sign-in goes to the program that runs now, not the stopped one.
	if err := h.setConnectorEnabled(ctx, "examplelive", false); err != nil {
		t.Fatal(err)
	}
	up(4, "")
	code, out := call(t, mux, "POST", "/api/v1/channels/examplelive/bind", master, `{}`)
	if code != 200 || out["hint"] != "from the program" || out["error"] != nil {
		t.Fatalf("bind: %d %v", code, out)
	}
	up(5, "examplelive")
	page()
}
