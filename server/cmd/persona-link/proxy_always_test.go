package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

// Proxy-only mode on the command line (#12299): persona-link proxy --always | --no-always, --proxy-always and
// --no-proxy-always on pair and install, and what status says about it.

func readConfig(t *testing.T, f *fakeEnv) *link.Config {
	t.Helper()
	c, err := link.Read(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProxyCommandAlways(t *testing.T) {
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
	// No URL saved: refused, whatever the environment has (a service rarely has it).
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	if err := proxyCmd(f.env, []string{"--always"}); !errors.Is(err, link.ErrProxyAlwaysNeedsURL) {
		t.Fatalf("--always without a URL: %v", err)
	}
	if _, err := link.Read(f.configPath()); err == nil {
		t.Fatal("a refused --always wrote link.json")
	}
	const always = "代理：http://proxy.example:8080（总是走代理）\n"
	if got := run(proxyWithSecret, "--always"); got != always {
		t.Fatalf("<url> --always: %q", got)
	}
	if c := readConfig(t, f); c.Proxy != proxyWithSecret || !c.ProxyAlways {
		t.Fatalf("link.json: %+v", c)
	}
	if got := run(); got != always {
		t.Fatalf("show: %q", got)
	}
	// A new URL keeps it.
	if got := run("http://other.example:8080"); got != "代理：http://other.example:8080（总是走代理）\n" {
		t.Fatalf("a new URL: %q", got)
	}
	if got := run("--no-always"); got != "代理：http://other.example:8080\n" {
		t.Fatalf("--no-always: %q", got)
	}
	if c := readConfig(t, f); c.Proxy != "http://other.example:8080" || c.ProxyAlways {
		t.Fatalf("--no-always kept: %+v", c)
	}
	// --always on its own uses the URL saved earlier; --off and --env turn it off.
	for _, off := range []string{"--off", "--env"} {
		if got := run("--always"); !strings.Contains(got, "（总是走代理）") {
			t.Fatalf("--always with a saved URL: %q", got)
		}
		if got := run(off); strings.Contains(got, "总是走代理") || readConfig(t, f).ProxyAlways {
			t.Fatalf("%s kept proxy-only mode: %q", off, got)
		}
		run("http://other.example:8080")
	}
	for _, args := range [][]string{{"--always", "--off"}, {"--env", "--always"}, {"--always", "--no-always"}, {"--always", "--always"}} {
		if err := proxyCmd(f.env, args); !errors.Is(err, errUsage) {
			t.Fatalf("proxy %v: %v", args, err)
		}
	}
	// Under Always use SSH: refused before anything is saved.
	if _, err := link.SetSSH(f.configPath(), &link.SSH{Server: "user@example.com", Target: "127.0.0.1:18663", Always: true}); err != nil {
		t.Fatal(err)
	}
	err := proxyCmd(f.env, []string{proxyWithSecret, "--always"})
	if !errors.Is(err, link.ErrProxyAlwaysSSHAlways) {
		t.Fatalf("--always under Always use SSH: %v", err)
	}
	if c := readConfig(t, f); c.Proxy != "http://other.example:8080" || c.ProxyAlways {
		t.Fatalf("a refused --always changed link.json: %+v", c)
	}
	if txt := link.ErrorText(i18n.Terminal(), err); !strings.Contains(txt, "ssh --no-always") {
		t.Fatalf("the refusal does not say what to do: %q", txt)
	}
	// English.
	if _, err := link.SetSSH(f.configPath(), nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_LANG", "en")
	if got := run(proxyWithSecret, "--always"); got != "Proxy: http://proxy.example:8080 (always uses the proxy)\n" {
		t.Fatalf("English: %q", got)
	}
}

func TestSSHAlwaysRefusedInProxyOnlyMode(t *testing.T) {
	f := newFakeEnv(t)
	f.pairHere(t)
	if err := proxyCmd(f.env, []string{proxyWithSecret, "--always"}); err != nil {
		t.Fatal(err)
	}
	err := sshCmd(f.env, []string{"user@example.com", "--always"})
	if !errors.Is(err, link.ErrSSHAlwaysProxyAlways) {
		t.Fatalf("ssh --always in proxy-only mode: %v", err)
	}
	if txt := link.ErrorText(i18n.Terminal(), err); !strings.Contains(txt, "proxy --no-always") {
		t.Fatalf("the refusal does not say what to do: %q", txt)
	}
	if c := readConfig(t, f); c.SSH != nil || !c.ProxyAlways {
		t.Fatalf("a refused ssh --always changed link.json: %+v", c)
	}
	// A tunnel it need not use can be saved; it waits unused until proxy-only mode is off.
	if err := sshCmd(f.env, []string{"user@example.com", "--no-always"}); err != nil {
		t.Fatalf("ssh --no-always in proxy-only mode: %v", err)
	}
	if err := proxyCmd(f.env, []string{"--no-always"}); err != nil {
		t.Fatal(err)
	}
	if err := sshCmd(f.env, []string{"user@example.com", "--always"}); err != nil {
		t.Fatalf("ssh --always after proxy --no-always: %v", err)
	}
}

func TestPairAndInstallTakeProxyAlways(t *testing.T) {
	f := newFakeEnv(t)
	for _, args := range [][]string{{"--listen", "--proxy-always"}, {"--listen", "--no-proxy-always"}} {
		if err := pairCmd(f.env, args); err == nil || !strings.Contains(err.Error(), "--proxy-always") {
			t.Fatalf("pair %v: %v", args, err)
		}
		if err := install(f.env, args); err == nil || !strings.Contains(err.Error(), "--proxy-always") {
			t.Fatalf("install %v: %v", args, err)
		}
	}
	const noAddresses = "persona://link?token=jp_x"
	// No URL saved or given: refused before pairing.
	if err := pairCmd(f.env, []string{"--proxy-always", noAddresses}); !errors.Is(err, link.ErrProxyAlwaysNeedsURL) {
		t.Fatalf("pair --proxy-always without a URL: %v", err)
	}
	if _, err := link.Read(f.configPath()); err == nil {
		t.Fatal("a refused --proxy-always wrote link.json")
	}
	// Always use SSH and proxy-only mode on one command line: refused before anything is saved.
	both := []string{"--ssh", "user@example.com", "--ssh-always", "--proxy", proxyWithSecret, "--proxy-always", noAddresses}
	if err := pairCmd(f.env, both); !errors.Is(err, link.ErrSSHAlwaysProxyAlways) {
		t.Fatalf("pair with both: %v", err)
	}
	if err := install(f.env, both[:len(both)-1]); !errors.Is(err, link.ErrSSHAlwaysProxyAlways) {
		t.Fatalf("install with both: %v", err)
	}
	if _, err := link.Read(f.configPath()); err == nil {
		t.Fatal("a refused command line wrote link.json")
	}
	// Saved before the pairing, which then goes through the proxy alone (here: a link that cannot be paired).
	if err := pairCmd(f.env, []string{"--proxy", proxyWithSecret, "--proxy-always", noAddresses}); err == nil {
		t.Fatal("pairing a link without addresses")
	}
	if c := readConfig(t, f); c.Proxy != proxyWithSecret || !c.ProxyAlways {
		t.Fatalf("pair --proxy --proxy-always: %+v", c)
	}
	// A saved proxy-only mode refuses Always use SSH from a pairing too.
	sshLink := "persona://link?url=http://hub.example.com:18661&tls=18662&fp=pin&hub=h1&token=jp_x&machine=pi"
	if err := pairCmd(f.env, []string{"--ssh", "user@example.com", "--ssh-always", sshLink}); !errors.Is(err, link.ErrSSHAlwaysProxyAlways) {
		t.Fatalf("pair --ssh-always in proxy-only mode: %v", err)
	}
	if err := pairCmd(f.env, []string{"--no-proxy-always", noAddresses}); err == nil {
		t.Fatal("pairing a link without addresses")
	}
	if c := readConfig(t, f); c.Proxy != proxyWithSecret || c.ProxyAlways {
		t.Fatalf("pair --no-proxy-always: %+v", c)
	}
}

func TestStatusReportsProxyOnly(t *testing.T) {
	f := newFakeEnv(t)
	f.pairHere(t)
	if err := link.SetProxy(f.configPath(), proxyWithSecret); err != nil {
		t.Fatal(err)
	}
	statusText := func() string {
		t.Helper()
		f.out.Reset()
		_ = status(f.env, nil)
		noSecret(t, "status", f.out.String())
		return f.out.String()
	}
	statusJSON := func() (report, string) {
		t.Helper()
		f.out.Reset()
		_ = status(f.env, []string{"--json"})
		noSecret(t, "status --json", f.out.String())
		var rep report
		if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		return rep, f.out.String()
	}
	if rep, raw := statusJSON(); rep.ProxyAlways || strings.Contains(raw, "proxy_always") {
		t.Fatalf("off: %s", raw)
	}
	if strings.Contains(statusText(), "总是走代理") {
		t.Fatal("off: marked proxy-only")
	}

	if err := link.SetProxyAlways(f.configPath(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := link.SetSSH(f.configPath(), &link.SSH{Server: "user@example.com", Target: "127.0.0.1:18663"}); err != nil {
		t.Fatal(err)
	}
	// Not running: the proxy first, marked; direct addresses and the tunnel marked as skipped.
	txt := statusText()
	proxied := strings.Index(txt, "  · 代理 http://proxy.example:8080（总是走代理）\n")
	direct := strings.Index(txt, "  · 内网 https://192.0.2.10:18662（已设总是走代理）\n")
	manual := strings.Index(txt, "hub.example.com:18662（手动加的，已设总是走代理）\n")
	tunnel := strings.Index(txt, "user@example.com → 127.0.0.1:18663（已设总是走代理）\n")
	if proxied < 0 || direct < proxied || manual < direct || tunnel < manual {
		t.Fatalf("status in proxy-only mode:\n%s", txt)
	}
	if rep, raw := statusJSON(); !rep.ProxyAlways || rep.Proxy != "http://proxy.example:8080" {
		t.Fatalf("status --json: %s", raw)
	}

	release, err := link.LockRun(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	since := f.now()
	f.saveStatus(t, link.Status{State: "connected", Mode: link.ModeActive, Machine: "pi", HubName: "home",
		Address: "https://hub.example.com:18662", Kind: link.KindProxy, Proxy: "http://proxy.example:8080", Since: &since, Node: "online"}, 4242)
	txt = statusText()
	for _, want := range []string{"链路：已连接 · 代理 hub.example.com:18662", "  · 代理 http://proxy.example:8080（在用，总是走代理）\n"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("status output missing %q:\n%s", want, txt)
		}
	}
	if rep, raw := statusJSON(); !rep.ProxyAlways || rep.Link == nil || rep.Link.Kind != link.KindProxy {
		t.Fatalf("status --json connected: %s", raw)
	}

	// The same in English.
	t.Setenv("PERSONA_LANG", "en")
	txt = statusText()
	for _, want := range []string{"  · Proxy http://proxy.example:8080 (in use, always uses the proxy)", "proxy-only mode is set"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("English status output missing %q:\n%s", want, txt)
		}
	}
}

// A refused proxy-only mode or Always use SSH on pair or install leaves link.json as it was, as persona-link proxy
// does: every refusal is checked before anything is saved, with this line's own --no-ssh-always taking effect first.
func TestPairAndInstallRefuseBeforeSaving(t *testing.T) {
	const (
		noAddresses  = "persona://link?token=jp_x" // cannot be paired: a pairing that starts fails at once
		loopbackLink = "persona://link?url=http://127.0.0.1:18661&tls=18662&fp=pin&hub=h1&token=jp_x&machine=pi"
		newProxy     = "http://new.example:8080"
		badProxy     = "socks5://127.0.0.1:1080" // not a usable http:// or https:// proxy URL
	)
	_, errBadProxy := link.ParseProxy(badProxy)
	if errBadProxy == nil {
		t.Fatalf("ParseProxy(%q) accepted it", badProxy)
	}
	sshAlways := func(c *link.Config) {
		c.SSH = &link.SSH{Server: "user@example.com", Target: "127.0.0.1:18663", Always: true}
	}
	proxyOnly := func(c *link.Config) { c.ProxyAlways = true }
	loopbackOnly := func(c *link.Config) { c.Endpoints = []link.Endpoint{{URL: "https://127.0.0.1:18662", Kind: "lan"}} }
	isErr := func(target error) func(error) bool { return func(err error) bool { return errors.Is(err, target) } }
	sshDefault := func(err error) bool {
		return err != nil && strings.Contains(link.ErrorText(i18n.Terminal(), err), "--no-ssh-always")
	}
	cases := []struct {
		name  string
		saved func(*link.Config)
		args  []string // pair's; install's are the same with --pair before the link
		link  string   // "" for install without --pair only
		want  func(error) bool
	}{
		{"--proxy off", nil, []string{"--proxy", "off", "--proxy-always"}, noAddresses, isErr(errUsage)},
		{"--proxy empty", nil, []string{"--proxy=", "--proxy-always"}, noAddresses, isErr(errUsage)},
		{"--proxy --env", nil, []string{"--proxy", "--env", "--proxy-always"}, noAddresses, isErr(errUsage)},
		{"saved Always use SSH", sshAlways, []string{"--proxy", newProxy, "--proxy-always"}, noAddresses, isErr(link.ErrProxyAlwaysSSHAlways)},
		{"saved Always use SSH, URL saved", sshAlways, []string{"--proxy-always"}, noAddresses, isErr(link.ErrProxyAlwaysSSHAlways)},
		{"loopback link", nil, []string{"--ssh", "user@example.com", "--proxy", newProxy, "--proxy-always"}, loopbackLink, sshDefault},
		{"loopback addresses saved", loopbackOnly, []string{"--ssh", "user@example.com", "--proxy", newProxy, "--proxy-always"}, "", sshDefault},
		{"saved proxy-only mode", proxyOnly, []string{"--ssh", "user@example.com", "--ssh-always", "--proxy", newProxy}, noAddresses, isErr(link.ErrSSHAlwaysProxyAlways)},
		{"saved proxy-only mode, loopback link", proxyOnly, []string{"--ssh", "user@example.com", "--proxy", newProxy}, loopbackLink, isErr(link.ErrSSHAlwaysProxyAlways)},
		// The proxy URL is checked before this line's --no-ssh-always turns the saved Always use SSH off.
		{"--no-ssh-always, unusable proxy", sshAlways, []string{"--ssh", "user@example.com", "--no-ssh-always", "--proxy", badProxy, "--proxy-always"}, noAddresses, isErr(errBadProxy)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeEnv(t)
			f.pairHere(t)
			if err := link.Update(f.configPath(), func(c *link.Config) error {
				c.Proxy = "http://saved.example:8080"
				if tc.saved != nil {
					tc.saved(c)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.configPath())
			if err != nil {
				t.Fatal(err)
			}
			type run struct {
				name string
				cmd  func(*env, []string) error
				args []string
			}
			installArgs := append([]string{"--no-node"}, tc.args...)
			runs := []run{{"install", install, installArgs}}
			if tc.link != "" {
				runs = []run{
					{"pair", pairCmd, append(append([]string{}, tc.args...), tc.link)},
					{"install", install, append(installArgs, "--pair", tc.link)},
				}
			}
			for _, r := range runs {
				if err := r.cmd(f.env, r.args); !tc.want(err) {
					t.Fatalf("%s %v: %v", r.name, r.args, err)
				}
				if after, _ := os.ReadFile(f.configPath()); !bytes.Equal(after, before) {
					t.Fatalf("a refused %s %v changed link.json:\n%s\nwas:\n%s", r.name, r.args, after, before)
				}
			}
		})
	}

	// This line's --no-ssh-always takes effect first: proxy-only mode goes on, from pair and from install.
	for _, cmd := range []string{"pair", "install"} {
		t.Run("--no-ssh-always on "+cmd, func(t *testing.T) {
			f := newFakeEnv(t)
			f.pairHere(t)
			if err := link.Update(f.configPath(), func(c *link.Config) error { sshAlways(c); return nil }); err != nil {
				t.Fatal(err)
			}
			args := []string{"--ssh", "user@example.com", "--ssh-target", "127.0.0.1:18663", "--no-ssh-always", "--proxy", newProxy, "--proxy-always"}
			if cmd == "pair" {
				// Saved before the pairing, which then fails: the link cannot be paired.
				if err := pairCmd(f.env, append(args, noAddresses)); err == nil || errors.Is(err, link.ErrProxyAlwaysSSHAlways) {
					t.Fatalf("pair %v: %v", args, err)
				}
			} else if err := install(f.env, append([]string{"--no-node"}, args...)); err != nil && codeOf(err) != 1 {
				t.Fatalf("install %v: %v\n%s", args, err, f.out) // exit 1: installed, but no service here reports
			}
			c := readConfig(t, f)
			if c.Proxy != newProxy || !c.ProxyAlways || c.SSH == nil || c.SSH.Always {
				t.Fatalf("%s %v: proxy %q, proxy_always %v, ssh %+v", cmd, args, c.Proxy, c.ProxyAlways, c.SSH)
			}
		})
	}
}
