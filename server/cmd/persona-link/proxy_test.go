package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

const proxyWithSecret = "http://corp-user:corp-secret@proxy.example:8080"

func noSecret(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "corp-user") || strings.Contains(text, "corp-secret") {
		t.Fatalf("%s shows the proxy's credentials:\n%s", where, text)
	}
}

func TestProxyCommand(t *testing.T) {
	f := newFakeEnv(t)
	run := func(args ...string) string {
		t.Helper()
		f.out.Reset()
		if err := proxyCmd(f.env, args); err != nil {
			t.Fatalf("proxy %v: %v", args, err)
		}
		noSecret(t, "persona-link proxy", f.out.String())
		return f.out.String()
	}
	if got := run(); got != i18n.T(i18n.Terminal(), "link-cli.proxy_current", i18n.Args{"proxy": i18n.T(i18n.Terminal(), "link-cli.proxy_unset")})+"\n" {
		t.Fatalf("nothing set: %q", got)
	}
	if got := run(proxyWithSecret); got != "代理：http://proxy.example:8080\n" {
		t.Fatalf("set: %q", got)
	}
	if c, err := link.Read(f.configPath()); err != nil || c.Proxy != proxyWithSecret {
		t.Fatalf("link.json keeps the credentials for the dial: %+v %v", c, err)
	}
	if got := run(); got != "代理：http://proxy.example:8080\n" {
		t.Fatalf("show: %q", got)
	}
	if got := run("--off"); !strings.Contains(got, i18n.T(i18n.Terminal(), "link-cli.proxy_off")) {
		t.Fatalf("off: %q", got)
	}
	if c, _ := link.Read(f.configPath()); c.Proxy != link.ProxyOff {
		t.Fatalf("off: %q", c.Proxy)
	}
	if got := run("--env"); !strings.Contains(got, i18n.T(i18n.Terminal(), "link-cli.proxy_unset")) {
		t.Fatalf("env: %q", got)
	}
	if c, _ := link.Read(f.configPath()); c.Proxy != "" {
		t.Fatalf("env: %q", c.Proxy)
	}
	err := proxyCmd(f.env, []string{"socks5://corp-user:corp-secret@proxy.example:1080"})
	if err == nil {
		t.Fatal("a SOCKS proxy was accepted")
	}
	noSecret(t, "the error", err.Error()+link.ErrorText(i18n.Terminal(), err))
	for _, args := range [][]string{{"--on"}, {"http://a:1", "http://b:2"}} {
		if err := proxyCmd(f.env, args); !errors.Is(err, errUsage) {
			t.Fatalf("proxy %v: %v", args, err)
		}
	}
	// A hand-edited setting that cannot be used shows nothing of itself.
	if err := link.Update(f.configPath(), func(c *link.Config) error { c.Proxy = "socks5://corp-user:corp-secret@p"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.Contains(got, i18n.T(i18n.Terminal(), "link-errors.proxy_invalid")) {
		t.Fatalf("invalid setting: %q", got)
	}
}

func TestStatusReportsTheProxyRoute(t *testing.T) {
	f := newFakeEnv(t)
	f.pairHere(t)
	if err := link.SetProxy(f.configPath(), proxyWithSecret); err != nil {
		t.Fatal(err)
	}
	// Not running: link.json's proxy is listed with the candidates, after the direct addresses it comes after.
	f.out.Reset()
	_ = status(f.env, nil)
	txt := f.out.String()
	noSecret(t, "status", txt)
	direct := strings.Index(txt, "hub.example.com:18662")
	proxied := strings.Index(txt, "  · 代理 http://proxy.example:8080\n")
	if direct < 0 || proxied < direct {
		t.Fatalf("status without a link:\n%s", txt)
	}

	release, err := link.LockRun(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	since := f.now()
	f.saveStatus(t, link.Status{State: "connected", Mode: link.ModeActive, Machine: "pi", HubName: "home",
		Address: "https://hub.example.com:18662", Kind: link.KindProxy, Proxy: "http://proxy.example:8080", Since: &since, Node: "online"}, 4242)
	f.out.Reset()
	if err := status(f.env, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	txt = f.out.String()
	noSecret(t, "status", txt)
	for _, want := range []string{"链路：已连接 · 代理 hub.example.com:18662", "  · 代理 http://proxy.example:8080（在用）"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("status output missing %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "hub.example.com:18662（在用") {
		t.Fatalf("the address reached through the proxy is marked as in use directly:\n%s", txt)
	}

	f.out.Reset()
	if err := status(f.env, []string{"--json"}); err != nil {
		t.Fatalf("status --json: %v", err)
	}
	noSecret(t, "status --json", f.out.String())
	var rep report
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Proxy != "http://proxy.example:8080" || rep.Link == nil || rep.Link.Kind != link.KindProxy || rep.Link.Proxy != "http://proxy.example:8080" {
		t.Fatalf("status json = %+v %+v", rep, rep.Link)
	}

	// A hand-edited setting that cannot be used: status says so, without showing it.
	if err := link.Update(f.configPath(), func(c *link.Config) error { c.Proxy = "socks5://corp-user:corp-secret@p"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.saveStatus(t, link.Status{State: "waiting", Mode: link.ModeActive, Machine: "pi"}, 4242)
	f.out.Reset()
	_ = status(f.env, nil)
	noSecret(t, "status", f.out.String())
	if !strings.Contains(f.out.String(), "  · 代理 "+i18n.T(i18n.Terminal(), "link-errors.proxy_invalid")+"\n") {
		t.Fatalf("an unusable proxy setting is not in status:\n%s", f.out.String())
	}
	f.out.Reset()
	_ = status(f.env, []string{"--json"})
	noSecret(t, "status --json", f.out.String())
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil || rep.Proxy != proxyInvalid {
		t.Fatalf("status json proxy = %q (%v)", rep.Proxy, err)
	}
	if err := link.SetProxy(f.configPath(), proxyWithSecret); err != nil {
		t.Fatal(err)
	}
	f.saveStatus(t, link.Status{State: "connected", Mode: link.ModeActive, Machine: "pi", HubName: "home",
		Address: "https://hub.example.com:18662", Kind: link.KindProxy, Proxy: "http://proxy.example:8080", Since: &since, Node: "online"}, 4242)

	// The same in English.
	t.Setenv("PERSONA_LANG", "en")
	f.out.Reset()
	_ = status(f.env, nil)
	for _, want := range []string{"Connected · Proxy hub.example.com:18662", "  · Proxy http://proxy.example:8080 (in use)"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("English status output missing %q:\n%s", want, f.out.String())
		}
	}
}

func TestPairAndInstallTakeTheProxy(t *testing.T) {
	f := newFakeEnv(t)
	if err := pairCmd(f.env, []string{"--listen", "--proxy", proxyWithSecret}); err == nil || !strings.Contains(err.Error(), "--proxy") {
		t.Fatalf("pair --listen --proxy: %v", err)
	}
	if err := install(f.env, []string{"--listen", "--proxy", proxyWithSecret}); err == nil || !strings.Contains(err.Error(), "--proxy") {
		t.Fatalf("install --listen --proxy: %v", err)
	}
	if err := pairCmd(f.env, []string{"--proxy", "socks5://corp-user:corp-secret@p", "persona://link?token=jp_x"}); err == nil {
		t.Fatal("pair took a SOCKS proxy")
	} else {
		noSecret(t, "pair's error", err.Error())
	}
	if _, err := link.Read(f.configPath()); err == nil {
		t.Fatal("an unusable --proxy wrote link.json")
	}
	// The proxy is saved before the pairing, which then goes through it (here: a link that cannot be paired).
	if err := pairCmd(f.env, []string{"--proxy", proxyWithSecret, "persona://link?token=jp_x"}); err == nil {
		t.Fatal("pairing a link without addresses")
	}
	if c, err := link.Read(f.configPath()); err != nil || c.Proxy != proxyWithSecret {
		t.Fatalf("pair --proxy: %+v %v", c, err)
	}
	if err := pairCmd(f.env, []string{"--proxy"}); err == nil {
		t.Fatal("--proxy without a value")
	}
}
