package extmgr

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
)

// A settings check's own way out through the egress proxy (#8459): the address the proposed settings name is reached
// with the check's credential only, while the check runs.

// connectThrough asks the proxy at addr for a CONNECT to target with password pass, and says the status.
func connectThrough(t *testing.T, addr, target, pass string) int {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auth := base64.StdEncoding.EncodeToString([]byte(egress.User + ":" + pass))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// loopbackSink is a listener on this machine that takes connections and drops them: an address settings can name.
func loopbackSink(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestSettingsCheckProxy(t *testing.T) {
	h, _ := extensionTestHub(t)
	ps := h.extensions
	mk := func(id string, network ...string) *extension.Extension {
		p := &extension.Extension{Record: extension.Record{ID: id, Source: extension.SourceInstalled, Origin: "dir:/x",
			Granted: extension.Permissions{Network: network}},
			Manifest: &extension.Manifest{ID: id, Runtime: &extension.Runtime{}}}
		p.Full = p.Manifest
		return p
	}
	p := mk("notes", "api.example.com", "config:gateway_url")
	// Register the proxy fixture directly: proxy authorization is independent of sandbox availability.
	// Actual confinement and startup refusal are covered by TestExtensionSandbox and TestExtensionSandboxMissing.
	px, err := ps.egress()
	if err != nil {
		t.Fatal(err)
	}
	ownURL, _ := url.Parse(px.Register(p.Record.ID, egress.Rule{Hosts: p.Record.Granted.Network}))
	own, _ := ownURL.User.Password()
	sink := loopbackSink(t)
	config := json.RawMessage(`{"gateway_url": "https://` + sink + `"}`)

	proxy, cred, revoke := ps.checkProxy(p, config)
	if revoke == nil {
		t.Fatal("no check proxy for a program behind the proxy with a config: entry")
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host != px.Addr() || u.User.Username() != egress.User {
		t.Fatalf("check proxy %q, want the proxy's listener %s", proxy, px.Addr())
	}
	if pw, _ := u.User.Password(); pw != cred || cred == own {
		t.Fatalf("credential %q in %q (own %q)", cred, proxy, own)
	}
	if got := connectThrough(t, px.Addr(), sink, cred); got != http.StatusOK {
		t.Fatalf("the proposed address with the check's credential: %d", got)
	}
	if got := connectThrough(t, px.Addr(), sink, own); got != http.StatusForbidden {
		t.Fatalf("the proposed address with the program's own credential: %d", got)
	}
	revoke()
	if got := connectThrough(t, px.Addr(), sink, cred); got != http.StatusProxyAuthRequired {
		t.Fatalf("after the check: %d", got)
	}

	// On Linux's sandbox the program reaches the proxy on its own loopback, which leads to its socket.
	if err := px.ListenUnix("notes", filepath.Join(shortSocketDir(t), "proxy.sock")); err != nil {
		t.Fatal(err)
	}
	proxy, _, revoke = ps.checkProxy(p, config)
	if u, _ := url.Parse(proxy); revoke == nil || u.Host != "127.0.0.1:"+strconv.Itoa(sandbox.ProxyPort) {
		t.Fatalf("a sandboxed program's check proxy: %q", proxy)
	}
	revoke()

	source := mk("notes", "config:gateway_url")
	source.Manifest.Runtime = &extension.Runtime{Mode: extension.RuntimeDeno}
	proxy, sourceCred, sourceRevoke := ps.checkProxy(source, config)
	if sourceRevoke == nil {
		t.Fatal("a source runtime's settings check has no candidate credential")
	}
	u, err = url.Parse(proxy)
	if sourcePassword, _ := u.User.Password(); err != nil || sourcePassword != sourceCred || sourceCred == "" {
		t.Fatalf("a source runtime's check proxy %q does not carry its candidate credential", proxy)
	}
	if got := connectThrough(t, px.Addr(), sink, sourceCred); got != http.StatusOK {
		t.Fatalf("a source runtime's candidate address with its check credential: %d", got)
	}
	if got := connectThrough(t, px.Addr(), sink, own); got != http.StatusForbidden {
		t.Fatalf("the ordinary credential acquired the source check's candidate: %d", got)
	}
	sourceRevoke()
	if got := connectThrough(t, px.Addr(), sink, sourceCred); got != http.StatusProxyAuthRequired {
		t.Fatalf("a source runtime's credential after its check: %d", got)
	}
	for name, q := range map[string]*extension.Extension{
		"no config: entry":     mk("notes", "api.example.com"),
		"not behind the proxy": mk("elsewhere", "config:gateway_url"),
	} {
		if proxy, _, revoke := ps.checkProxy(q, config); revoke != nil || proxy != "" {
			t.Errorf("%s: check proxy %q", name, proxy)
		}
	}
}

// shortSocketDir is a directory for a unix socket whose path fits sun_path (104 bytes on macOS).
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "chk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// Through the routes: the program gets the check's proxy, a refusal that quotes it gives back no credential, and the
// credential stops working once the check answers, refused or not.
func TestSettingsCheckProxyRoutes(t *testing.T) {
	setVarFor(t, &settingsCheckTimeout, 2*time.Second)
	h, _ := extensionTestHub(t)
	ps := h.extensions
	mux := h.routes()
	master := h.cfg.Token
	dir := settingsCheckPackage(t)
	out := filepath.Join(t.TempDir(), "check-proxy")
	self, _ := os.Executable()
	script := "#!/bin/sh\nCHECK_PROXY_OUT=" + out + " PERSONA_TEST_PLUGIN=settingscheck exec " + self + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "gw"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+dir+`"}`)
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "the extension up", func() bool {
		hp := ps.get("example-gateway")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})
	// The protocol peer lives outside its package, so the normal test harness leaves its sandbox off.
	// Seed only its egress registration; no unavailable-sandbox fallback is involved.
	px, err := ps.egress()
	if err != nil {
		t.Fatal(err)
	}
	p := ps.reg.Get("example-gateway")
	px.Register(p.Record.ID, egress.Rule{Hosts: p.Record.Granted.Network})
	put := func(gateway string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"config": map[string]string{"gateway_url": gateway}})
		return call(t, mux, "PUT", "/api/v1/extensions/example-gateway/-/config", master, string(b))
	}
	given := func() string {
		t.Helper()
		b, err := os.ReadFile(out)
		if err != nil || len(b) == 0 {
			t.Fatalf("the program got no check proxy: %v", err)
		}
		u, err := url.Parse(string(b))
		if err != nil || u.Host != px.Addr() {
			t.Fatalf("check proxy %q", b)
		}
		pw, _ := u.User.Password()
		return pw
	}

	code, res := put("https://refuse.example.com")
	msg, _ := res["error"].(string)
	cred := given()
	if code != 400 || res["code"] != "check_failed" || !strings.Contains(msg, "unreachable through") || strings.Contains(msg, cred) {
		t.Fatalf("refused: %d %v (credential %q)", code, res, cred)
	}
	if got := connectThrough(t, px.Addr(), "refuse.example.com:443", cred); got != http.StatusProxyAuthRequired {
		t.Fatalf("a refused check's credential afterwards: %d", got)
	}

	_ = os.Remove(out)
	sink := loopbackSink(t)
	if code, res := put("https://" + sink); code != 200 {
		t.Fatalf("a check that works: %d %v", code, res)
	}
	cred2 := given()
	if cred2 == cred {
		t.Fatal("the same credential for two checks")
	}
	if got := connectThrough(t, px.Addr(), sink, cred2); got != http.StatusProxyAuthRequired {
		t.Fatalf("a passed check's credential afterwards: %d", got)
	}
}
