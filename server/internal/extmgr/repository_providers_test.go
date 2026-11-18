package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/rpc"
)

// acmeProvider is the fake provider's id: extension acme-code, contribution acme.
const acmeProvider = "ext:acme-code/acme"

// serveRepositoryProviderExtensionForTest is acme-code's program. Its repo namespace answers by the address's last
// segment, a few of them with an answer the manager must refuse, and writes each call's method and params as a line of
// JSON to REPOS_OUT.
func serveRepositoryProviderExtensionForTest() {
	out := os.Getenv("REPOS_OUT")
	var mu sync.Mutex
	note := func(method string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		if f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			b, _ := json.Marshal(map[string]any{"method": method, "params": params})
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	last := func(s string) string { return s[strings.LastIndex(s, "/")+1:] }
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "acme-code", Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{
			"repo": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				note(method, params)
				switch method {
				case "parse":
					var p wire.RepoParseParams
					_ = json.Unmarshal(params, &p)
					p.URL, _, _ = strings.Cut(p.URL, "#")
					name := strings.TrimSuffix(last(p.URL), ".git")
					if _, after, ok := strings.Cut(name, ":"); ok { // git@ssh.acme.test:name
						name = after
					}
					res := wire.RepoParseResult{InstanceURL: "https://code.acme.test", Path: "team/" + name,
						CanonicalURL: "https://code.acme.test/team/" + name, CloneURL: "ssh://git@ssh.acme.test/team/" + name + ".git",
						DisplayName: "Acme\n" + name}
					switch name {
					case "elsewhere": // a host it does not claim
						res.InstanceURL, res.CanonicalURL = "https://code.other.test", "https://code.other.test/team/elsewhere"
					case "plain":
						res.InstanceURL, res.CanonicalURL = "http://code.acme.test", "http://code.acme.test/team/plain"
					case "creds":
						res.InstanceURL, res.CanonicalURL = "https://user:pw@code.acme.test", "https://user:pw@code.acme.test/team/creds"
					case "clonecreds":
						res.CloneURL = "https://user:pw@code.acme.test/team/clonecreds.git"
					case "mismatch":
						res.CanonicalURL = "https://code.acme.test/team/other"
					}
					return res, nil
				case "check":
					var p wire.RepoCheckParams
					_ = json.Unmarshal(params, &p)
					switch last(p.CanonicalURL) {
					case "ok":
						return wire.Empty{}, nil
					case "missing":
						return nil, &rpc.Error{Code: "not_found", Message: "no such repository"}
					case "signed_out":
						return nil, &rpc.Error{Code: "not_signed_in", Message: "token\nrevoked"}
					case "refuse":
						return nil, &rpc.Error{Code: "refused", Message: "policy\nsays no " + strings.Repeat("and more words ", 30)}
					case "noisy":
						return nil, &rpc.Error{Code: "refused", Message: "policy\x1b[31m says\x00 no\x1c\x1dmore"}
					case "gone": // as a version that no longer has the contribution
						return nil, pserve.NoContribution(p.Contribution)
					case "slow": // answers only when the hub gives up on the call (or the program stops)
						<-ctx.Done()
						return nil, ctx.Err()
					case "crash": // the program falls over
						os.Exit(3)
					}
					return nil, errors.New("the source fell over")
				case "link":
					var p wire.RepoLinkParams
					_ = json.Unmarshal(params, &p)
					p.URL, _, _ = strings.Cut(p.URL, "#")
					base := p.Repo.CanonicalURL
					segment := map[string]string{"issue": "issues", "pull_request": "merge_requests"}
					if p.URL == "" {
						number := p.Number
						if number == "13" { // another link than asked
							number = "14"
						}
						return wire.RepoLinkResult{Kind: p.Kind, Number: number, CanonicalURL: base + "/-/" + segment[p.Kind] + "/" + number}, nil
					}
					switch last(p.URL) {
					case "outside":
						return wire.RepoLinkResult{Kind: "issue", Number: "3", CanonicalURL: "https://code.other.test/team/ok/-/issues/3"}, nil
					case "sibling":
						return wire.RepoLinkResult{Kind: "issue", Number: "3", CanonicalURL: base + "-two/-/issues/3"}, nil
					}
					kind := "issue"
					if strings.Contains(p.URL, "/merge_requests/") {
						kind = "pull_request"
					}
					return wire.RepoLinkResult{Kind: kind, Number: last(p.URL), CanonicalURL: base + "/-/" + segment[kind] + "/" + last(p.URL),
						Title: "Fix\nthe build"}, nil
				case "describe":
					var p wire.RepoDescribeParams
					_ = json.Unmarshal(params, &p)
					switch last(p.CanonicalURL) {
					case "old": // as a version without the optional method
						return nil, pserve.UnknownMethod(method)
					case "garbled": // not an answer of the method's shape
						return "garbled", nil
					}
					return wire.RepoDescribeResult{Title: "Fix\tthe build", State: "merged"}, nil
				}
				return nil, pserve.UnknownMethod(method)
			}}})
}

// repositoryProviderPackage is acme-code's package as id, started with the hub so that a stopped one stays stopped
// (unless extra, more manifest fields, has its own activation_events).
func repositoryProviderPackage(t *testing.T, id, out string, extra ...string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "code"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=repos exec "+self+" \"$@\"\n"), 0o755)
	activation, fields := `"activation_events": ["on_startup"], `, ""
	for _, f := range extra {
		if strings.HasPrefix(f, `"activation_events"`) {
			activation = ""
		}
		fields += f + ", "
	}
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": %q, "name": "Acme Code", "version": "1.0.0", %s%s
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/code"}, "env": {"REPOS_OUT": %q}},
		"contributes": {"repository_providers": [{"id": "acme", "name": "Acme Code", "hosts": ["code.acme.test", "ssh.acme.test"],
			"link_kinds": [{"kind": "pull_request", "label": "MR"}]}]}}`, id, activation, fields, runtime.GOOS+"-"+runtime.GOARCH, out)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// installRepositoryProvider installs acme-code and waits for its program to answer; the calls it gets go to the
// file it returns.
func installRepositoryProvider(t *testing.T, h *Manager, mux http.Handler) string {
	t.Helper()
	out := installRepositoryProviderPackage(t, h, mux)
	waitRepositoryProviderUp(t, h)
	return out
}

// installRepositoryProviderPackage installs acme-code (with extra manifest fields) without waiting for it.
func installRepositoryProviderPackage(t *testing.T, h *Manager, mux http.Handler, extra ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "calls.jsonl")
	c, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", h.cfg.Token, `{"source": "dir:`+repositoryProviderPackage(t, "acme-code", out, extra...)+`"}`)
	if c != 200 || !strings.Contains(fmt.Sprint(pv["permissions"]), "code.acme.test") {
		t.Fatalf("the install page names the hosts it reads: %d %v", c, pv)
	}
	if c, body := call(t, mux, "POST", "/api/v1/plugins/-/install", h.cfg.Token, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); c != 200 {
		t.Fatalf("install: %d %v", c, body)
	}
	return out
}

func waitRepositoryProviderUp(t *testing.T, h *Manager) {
	t.Helper()
	waitLong(t, "the provider answering", func() bool {
		return h.RepositoryCheck(context.Background(), acmeRepository("ok")) == nil
	})
}

// acmeRepository is team/<name> on code.acme.test, as repo.parse answers it.
func acmeRepository(name string) repository.Identity {
	return repository.Identity{Provider: acmeProvider, InstanceURL: "https://code.acme.test", Path: "team/" + name,
		CanonicalURL: "https://code.acme.test/team/" + name}
}

// providerErr is err's ProviderError, or a failure of the test.
func providerErr(t *testing.T, err error) *repository.ProviderError {
	t.Helper()
	var pe *repository.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("not a ProviderError: %v", err)
	}
	return pe
}

// repoCalls is how many calls the program wrote to path.
func repoCalls(path string) int {
	raw, _ := os.ReadFile(path)
	return strings.Count(string(raw), "\n")
}

// hubLines are the hub's lines in the extension's own log.
func hubLines(h *Manager, id string) string {
	lines, _ := h.extensions.stats.of(id).Lines(-1, "", 0)
	var b strings.Builder
	for _, l := range lines {
		if l.Source == host.LogHub {
			b.WriteString(l.Text + "\n")
		}
	}
	return b.String()
}

// What the provider is asked and what of its answers comes back: every answer re-checked, a refused one logged with
// the host and the rule only, an address on another host never sent.
func TestRepositoryProviderCalls(t *testing.T) {
	h, _ := extensionTestHub(t)
	out := installRepositoryProvider(t, h, h.routes())
	ctx := context.Background()

	parsed, err := h.RepositoryParse(ctx, acmeProvider, "https://code.acme.test/team/ok")
	want := repository.Parsed{Identity: acmeRepository("ok"), CloneURL: "ssh://git@ssh.acme.test/team/ok.git", ProviderName: "Acme Code",
		DisplayName: "Acme ok"}
	if err != nil || parsed != want {
		t.Fatalf("parsed: %+v %v", parsed, err)
	}
	if !strings.Contains(readFile(t, out), `{"method":"parse","params":{"contribution":"acme","url":"https://code.acme.test/team/ok"}}`) {
		t.Fatalf("what repo.parse was told: %s", readFile(t, out))
	}
	if parsed, err := h.RepositoryParse(ctx, acmeProvider, "git@ssh.acme.test:team/ok.git"); err != nil || parsed.Identity != acmeRepository("ok") {
		t.Fatalf("an ssh address on its other host: %+v %v", parsed, err)
	}
	if parsed, err := h.RepositoryParse(ctx, acmeProvider, "https://code.acme.test/team/ok#readme"); err != nil || parsed.Identity != acmeRepository("ok") {
		t.Fatalf("a fragment with no credentials: %+v %v", parsed, err)
	}

	// Answers that break a rule: failed, and the extension's log says which rule on which host, nothing of the answer.
	for _, name := range []string{"elsewhere", "plain", "creds", "clonecreds", "mismatch"} {
		_, err := h.RepositoryParse(ctx, acmeProvider, "https://code.acme.test/team/"+name)
		if pe := providerErr(t, err); pe.Code != "failed" || pe.Provider != "Acme Code" || !strings.HasPrefix(pe.Why, "an answer Persona refused: ") {
			t.Fatalf("%s: %+v", name, pe)
		}
	}
	logged := hubLines(h, "acme-code")
	if n := strings.Count(logged, "repo.parse answer refused (code.acme.test): "); n != 5 {
		t.Fatalf("each refused answer logged once: %d in %q", n, logged)
	}
	if strings.Contains(logged, "://") || strings.Contains(logged, "pw") || strings.Contains(logged, "elsewhere") {
		t.Fatalf("a refused answer is logged as its host and rule only: %q", logged)
	}

	// An address on another host, or with credentials, never reaches it.
	sent := repoCalls(out)
	for _, remote := range []string{"https://code.other.test/team/ok", "https://code.acme.test/team/ok?private_token=x",
		"https://user:pw@code.acme.test/team/ok", "ssh://root@ssh.acme.test/team/ok.git",
		"https://code.acme.test/team/ok#access_token=x", "https://code.acme.test/team/ok?tab=1#private_token=x",
		"ssh://git@ssh.acme.test/team/ok.git?private_token=x", "ssh://git@ssh.acme.test/team/ok.git#x",
		"git@ssh.acme.test:team/ok.git?private_token=x", "git@ssh.acme.test:team/ok.git#x"} {
		if _, err := h.RepositoryParse(ctx, acmeProvider, remote); !errors.Is(err, repository.ErrRemote) {
			t.Fatalf("%s: %v", remote, err)
		}
	}
	for _, link := range []string{"https://code.other.test/team/ok/-/issues/1", "https://code.acme.test/team/ok/-/issues/1?private_token=x",
		"https://code.acme.test/team/ok/-/issues/1#private_token=x", "https://user:pw@code.acme.test/team/ok/-/issues/1"} {
		if _, err := h.RepositoryLink(ctx, acmeRepository("ok"), repository.LinkInput{URL: link}); !errors.Is(err, repository.ErrRemote) {
			t.Fatalf("link %s: %v", link, err)
		}
	}
	for _, id := range []repository.Identity{
		{Provider: acmeProvider, InstanceURL: "https://code.other.test", Path: "team/ok", CanonicalURL: "https://code.other.test/team/ok"},
		{Provider: acmeProvider, InstanceURL: "https://code.acme.test", Path: "team/ok", CanonicalURL: "https://code.other.test/team/ok"},
		{Provider: acmeProvider, InstanceURL: "http://code.acme.test", Path: "team/ok", CanonicalURL: "http://code.acme.test/team/ok"},
		{Provider: acmeProvider, InstanceURL: "https://user:pw@code.acme.test", Path: "team/ok", CanonicalURL: "https://user:pw@code.acme.test/team/ok"},
		{Provider: acmeProvider, InstanceURL: "https://code.acme.test", Path: "", CanonicalURL: "https://code.acme.test/"},
	} {
		if pe := providerErr(t, h.RepositoryCheck(ctx, id)); pe.Code != "failed" {
			t.Fatalf("a repository off its hosts: %+v", pe)
		}
		_, err := h.RepositoryLink(ctx, id, repository.LinkInput{Kind: "issue", Number: "1"})
		if pe := providerErr(t, err); pe.Code != "failed" {
			t.Fatalf("a link of a repository off its hosts: %+v", pe)
		}
	}
	for _, link := range []string{"https://code.other.test/team/ok/-/issues/1", "http://code.acme.test/team/ok/-/issues/1",
		"https://user:pw@code.acme.test/team/ok/-/issues/1", "https://code.acme.test/team/ok/-/issues/1?private_token=x",
		"https://code.acme.test/team/ok/-/issues/1?", "https://code.acme.test/team/ok/-/issues/1#c"} {
		if _, err := h.RepositoryDescribe(ctx, acmeProvider, link); providerErr(t, err).Code != "failed" {
			t.Fatalf("describe %s: %v", link, err)
		}
	}
	if n := repoCalls(out); n != sent {
		t.Fatalf("%d calls reached it", n-sent)
	}

	// repo.check: the codes the protocol names, with the provider's words on one line.
	if err := h.RepositoryCheck(ctx, acmeRepository("ok")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, out), `{"method":"check","params":{"contribution":"acme","canonical_url":"https://code.acme.test/team/ok"}}`) {
		t.Fatalf("what repo.check was told: %s", readFile(t, out))
	}
	for name, code := range map[string]string{"missing": "not_found", "signed_out": "not_signed_in", "refuse": "refused", "boom": "failed",
		"noisy": "refused", "gone": "failed"} {
		pe := providerErr(t, h.RepositoryCheck(ctx, acmeRepository(name)))
		if pe.Code != code || pe.Provider != "Acme Code" || strings.ContainsFunc(pe.Why, unicode.IsControl) || utf8.RuneCountInString(pe.Why) > 200 {
			t.Fatalf("%s: %+v", name, pe)
		}
		if name == "refuse" && (!strings.HasPrefix(pe.Why, "policy says no and more words") || !strings.HasSuffix(pe.Why, "…")) {
			t.Fatalf("the refusal keeps its words, clipped: %q", pe.Why)
		}
		if name == "signed_out" && pe.Why != "token revoked" || name == "noisy" && pe.Why != "policy [31m says no more" ||
			name == "gone" && pe.Why != "it does not provide repositories (an older version?)" {
			t.Fatalf("%s, the words: %q", name, pe.Why)
		}
	}

	// repo.link: by address or by kind and number, its answer under the repository's own address.
	link, err := h.RepositoryLink(ctx, acmeRepository("ok"), repository.LinkInput{URL: "https://code.acme.test/team/ok/-/merge_requests/7"})
	if err != nil || link != (repository.TaskLink{Kind: "pull_request", Number: "7", CanonicalURL: "https://code.acme.test/team/ok/-/merge_requests/7", Title: "Fix the build"}) {
		t.Fatalf("by address: %+v %v", link, err)
	}
	link, err = h.RepositoryLink(ctx, acmeRepository("ok"), repository.LinkInput{URL: "https://code.acme.test/team/ok/-/merge_requests/7#note_1"})
	if err != nil || link.CanonicalURL != "https://code.acme.test/team/ok/-/merge_requests/7" {
		t.Fatalf("a fragment with no credentials: %+v %v", link, err)
	}
	link, err = h.RepositoryLink(ctx, acmeRepository("ok"), repository.LinkInput{Kind: "issue", Number: "5"})
	if err != nil || link != (repository.TaskLink{Kind: "issue", Number: "5", CanonicalURL: "https://code.acme.test/team/ok/-/issues/5"}) {
		t.Fatalf("by number: %+v %v", link, err)
	}
	if !strings.Contains(readFile(t, out), `{"method":"link","params":{"contribution":"acme","repo":{"instance_url":"https://code.acme.test","path":"team/ok","canonical_url":"https://code.acme.test/team/ok"},"kind":"issue","number":"5"}}`) {
		t.Fatalf("what repo.link was told: %s", readFile(t, out))
	}
	for what, in := range map[string]repository.LinkInput{
		"outside the repository": {URL: "https://code.acme.test/team/ok/-/issues/outside"},
		"a sibling repository's": {URL: "https://code.acme.test/team/ok/-/issues/sibling"},
		"another number":         {Kind: "issue", Number: "13"},
	} {
		_, err := h.RepositoryLink(ctx, acmeRepository("ok"), in)
		if pe := providerErr(t, err); pe.Code != "failed" || !strings.HasPrefix(pe.Why, "an answer Persona refused: ") {
			t.Fatalf("%s: %+v", what, pe)
		}
	}
	if n := strings.Count(hubLines(h, "acme-code"), "repo.link answer refused (code.acme.test): "); n != 3 {
		t.Fatalf("each refused link logged: %d", n)
	}

	// repo.describe, which a provider may leave out.
	d, err := h.RepositoryDescribe(ctx, acmeProvider, "https://code.acme.test/team/ok/-/merge_requests/7")
	if err != nil || d != (repository.Description{Title: "Fix the build", State: "merged"}) {
		t.Fatalf("described: %+v %v", d, err)
	}
	_, err = h.RepositoryDescribe(ctx, acmeProvider, "https://code.acme.test/team/ok/-/issues/old")
	if pe := providerErr(t, err); pe.Code != "failed" || !strings.Contains(pe.Why, "an older version?") {
		t.Fatalf("no describe: %+v", pe)
	}
	_, err = h.RepositoryDescribe(ctx, acmeProvider, "https://code.acme.test/team/ok/-/issues/garbled")
	if pe := providerErr(t, err); pe.Code != "failed" || !strings.Contains(pe.Why, "cannot unmarshal") {
		t.Fatalf("an answer of another shape: %+v", pe)
	}

	// Bounded: no answer in repoCallTimeout, or the caller gone, is failed.
	saved := repoCallTimeout
	repoCallTimeout = 200 * time.Millisecond
	err = h.RepositoryCheck(ctx, acmeRepository("slow"))
	repoCallTimeout = saved
	if pe := providerErr(t, err); pe.Code != "failed" || pe.Why != "no answer in 200ms" {
		t.Fatalf("timed out: %+v", pe)
	}
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	if pe := providerErr(t, h.RepositoryCheck(cctx, acmeRepository("slow"))); pe.Code != "failed" || pe.Why != "canceled" {
		t.Fatalf("canceled: %+v", pe)
	}
	dctx, dcancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer dcancel()
	if pe := providerErr(t, h.RepositoryCheck(dctx, acmeRepository("slow"))); pe.Code != "failed" || pe.Why != "no answer in time" {
		t.Fatalf("the caller's time up: %+v", pe)
	}
}

// Listed on or off, with its link words; switched off, its contribution off, its program stopped or uninstalled, a
// call does not reach it; a second package may not claim its host.
func TestRepositoryProviderStates(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	ctx := context.Background()
	out := installRepositoryProvider(t, h, mux)

	available := repository.ProviderInfo{ID: acmeProvider, Name: "Acme Code", Hosts: []string{"code.acme.test", "ssh.acme.test"},
		State: repository.ProviderAvailable, LinkKinds: []repository.LinkLabel{{Kind: "pull_request", Label: "MR"}}}
	if got := h.RepositoryProviders(); !reflect.DeepEqual(got, []repository.ProviderInfo{available}) {
		t.Fatalf("listed: %+v", got)
	}
	if got, ok := h.RepositoryProvider(acmeProvider); !ok || !reflect.DeepEqual(got, available) {
		t.Fatalf("by id: %+v %v", got, ok)
	}
	if got, ok := h.RepositoryProviderForHost("ssh.acme.test"); !ok || got.ID != acmeProvider {
		t.Fatalf("by host: %+v %v", got, ok)
	}
	for _, id := range []string{"ext:acme-code/other", "ext:other/acme", "github", "ext:Acme-Code/acme", ""} {
		if _, ok := h.RepositoryProvider(id); ok {
			t.Fatalf("%q is no provider", id)
		}
	}
	if _, ok := h.RepositoryProviderForHost("code.other.test"); ok {
		t.Fatal("a host nobody claims")
	}

	// A second package may not claim one of its hosts.
	c, body := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+repositoryProviderPackage(t, "acme-code-two", out)+`"}`)
	if c == 200 {
		c, body = call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, body["preview_id"], body["permissions_digest"]))
	}
	if c == 200 || !strings.Contains(fmt.Sprint(body), `"code.acme.test" is already extension acme-code's`) || len(h.RepositoryProviders()) != 1 {
		t.Fatalf("a host another extension claims fails the install: %d %v %+v", c, body, h.RepositoryProviders())
	}

	offCalls := func(what string, want *repository.ProviderError) {
		t.Helper()
		sent := repoCalls(out)
		check := func(method string, err error) {
			t.Helper()
			if pe := providerErr(t, err); *pe != *want {
				t.Fatalf("%s, %s: %+v", what, method, pe)
			}
		}
		_, err := h.RepositoryParse(ctx, acmeProvider, "https://code.acme.test/team/ok")
		check("parse", err)
		check("check", h.RepositoryCheck(ctx, acmeRepository("ok")))
		_, err = h.RepositoryLink(ctx, acmeRepository("ok"), repository.LinkInput{Kind: "issue", Number: "1"})
		check("link", err)
		_, err = h.RepositoryDescribe(ctx, acmeProvider, "https://code.acme.test/team/ok/-/issues/1")
		check("describe", err)
		if n := repoCalls(out); n != sent {
			t.Fatalf("%s: %d calls reached it", what, n-sent)
		}
	}
	state := func() string {
		info, _ := h.RepositoryProvider(acmeProvider)
		return info.State
	}
	off := &repository.ProviderError{Code: "provider_off", Provider: "Acme Code"}

	// Switched off: listed off, with its hosts, and never asked.
	if c, _ := call(t, mux, "PUT", "/api/v1/extensions/acme-code/-/enabled", master, `{"enabled": false}`); c != 200 {
		t.Fatalf("switch off: %d", c)
	}
	waitLong(t, "the provider off", func() bool { return state() == repository.ProviderOff })
	if got, ok := h.RepositoryProviderForHost("code.acme.test"); !ok || got.State != repository.ProviderOff {
		t.Fatalf("its host is still its own: %+v %v", got, ok)
	}
	offCalls("switched off", off)
	if c, _ := call(t, mux, "PUT", "/api/v1/extensions/acme-code/-/enabled", master, `{"enabled": true}`); c != 200 {
		t.Fatalf("switch on: %d", c)
	}
	waitRepositoryProviderUp(t, h)

	// Its contribution switched off.
	if c, body := call(t, mux, "PUT", "/api/v1/extensions/acme-code/-/contributions/repository_providers/acme", master, `{"enabled": false}`); c != 200 {
		t.Fatalf("contribution off: %d %v", c, body)
	}
	waitLong(t, "the contribution off", func() bool { return state() == repository.ProviderOff })
	offCalls("contribution off", off)
	if c, body := call(t, mux, "PUT", "/api/v1/extensions/acme-code/-/contributions/repository_providers/acme", master, `{"enabled": true}`); c != 200 {
		t.Fatalf("contribution on: %d %v", c, body)
	}
	waitRepositoryProviderUp(t, h)

	// Its program stopped (started with the hub, so it stays stopped and a call does not start it): listed off, as
	// its calls answer.
	h.extensions.get("acme-code").proc.Stop()
	if got, ok := h.RepositoryProvider(acmeProvider); !ok || got.State != repository.ProviderOff {
		t.Fatalf("stopped, by id: %+v %v", got, ok)
	}
	if got := h.RepositoryProviders(); len(got) != 1 || got[0].State != repository.ProviderOff {
		t.Fatalf("stopped, listed: %+v", got)
	}
	if got, ok := h.RepositoryProviderForHost("code.acme.test"); !ok || got.State != repository.ProviderOff {
		t.Fatalf("stopped, by host: %+v %v", got, ok)
	}
	offCalls("stopped", off)

	// Uninstalled: no provider, named by its contribution.
	if c, body := call(t, mux, "DELETE", "/api/v1/extensions/acme-code/-/", master, ""); c != 200 {
		t.Fatalf("uninstall: %d %v", c, body)
	}
	if _, ok := h.RepositoryProvider(acmeProvider); ok {
		t.Fatal("uninstalled, still a provider")
	}
	if _, ok := h.RepositoryProviderForHost("code.acme.test"); ok || len(h.RepositoryProviders()) != 0 {
		t.Fatalf("uninstalled, still listed: %+v", h.RepositoryProviders())
	}
	offCalls("uninstalled", &repository.ProviderError{Code: "no_provider"})

	// Installed again, waiting for a required key: switched on but with no program, so off until the key is in.
	out = installRepositoryProviderPackage(t, h, mux, `"secrets": [{"name": "token", "title": "Example key", "required": true}]`)
	waitLong(t, "waiting for its key", func() bool {
		hp := h.extensions.get("acme-code")
		return hp != nil && hp.proc == nil && hp.status == extensionNeedsSetup
	})
	if got, ok := h.RepositoryProvider(acmeProvider); !ok || got.State != repository.ProviderOff {
		t.Fatalf("needs setup: %+v %v", got, ok)
	}
	offCalls("needs setup", off)
	if c, body := call(t, mux, "PUT", "/api/v1/extensions/acme-code/-/secrets/token", master, `{"value": "example-key-value"}`); c != 200 {
		t.Fatalf("its key: %d %v", c, body)
	}
	waitRepositoryProviderUp(t, h)
}

// A provider whose program calls start (on_call) is available while it is idle, and a call starts it; once its program
// has failed it is off, as its calls answer, while it waits out its backoff.
func TestRepositoryProviderLazyProgram(t *testing.T) {
	saved := host.RestartFirst
	host.RestartFirst = 50 * time.Millisecond // tried again soon after it falls over (restored after the hub stops)
	t.Cleanup(func() { host.RestartFirst = saved })
	h, _ := extensionTestHub(t)
	mux := h.routes()
	ctx := context.Background()
	out := installRepositoryProviderPackage(t, h, mux, `"activation_events": ["on_call"]`)
	state := func() string {
		info, _ := h.RepositoryProvider(acmeProvider)
		return info.State
	}

	// Idle: nothing has started its program yet, and it is available.
	waitLong(t, "its program handle", func() bool {
		hp := h.extensions.get("acme-code")
		return hp != nil && hp.proc != nil
	})
	proc := h.extensions.get("acme-code").proc
	if st, why := proc.Status(); st != host.StateStopped || !proc.Spec().OnCall || repoCalls(out) != 0 {
		t.Fatalf("not idle: %s %q, %d calls", st, why, repoCalls(out))
	}
	if got := h.RepositoryProviders(); len(got) != 1 || got[0].State != repository.ProviderAvailable {
		t.Fatalf("idle, listed: %+v", got)
	}
	if got := state(); got != repository.ProviderAvailable {
		t.Fatalf("idle, by id: %s", got)
	}

	// A call starts it.
	if err := h.RepositoryCheck(ctx, acmeRepository("ok")); err != nil {
		t.Fatalf("a call to an idle provider: %v", err)
	}
	if st, _ := proc.Status(); st != host.StateRunning || state() != repository.ProviderAvailable {
		t.Fatalf("started by the call: %s, %s", st, state())
	}

	// Its program falls over: still available, since calls start it again, and a later call reaches it.
	if pe := providerErr(t, h.RepositoryCheck(ctx, acmeRepository("crash"))); pe.Code != "failed" {
		t.Fatalf("the call it fell over in: %+v", pe)
	}
	if got, ok := h.RepositoryProviderForHost("ssh.acme.test"); !ok || got.State != repository.ProviderAvailable {
		t.Fatalf("fell over, by host: %+v %v", got, ok)
	}
	sent := repoCalls(out)
	waitLong(t, "a call answered again", func() bool { return h.RepositoryCheck(ctx, acmeRepository("ok")) == nil })
	if repoCalls(out) == sent || state() != repository.ProviderAvailable {
		t.Fatalf("after falling over: %d calls reached it, %s", repoCalls(out)-sent, state())
	}
}

// Before the manager has started, lists are empty without waiting, and a call waits for startup as long as its
// context lets it. (Installing needs a started manager, so here there are no extensions: once started, off.)
func TestRepositoryProvidersBeforeStartup(t *testing.T) {
	h := New(Options{Dir: shortTempDir(t), Config: &Config{}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if got := h.RepositoryProviders(); got != nil {
			t.Errorf("listed: %+v", got)
		}
		if _, ok := h.RepositoryProvider(acmeProvider); ok {
			t.Error("by id")
		}
		if _, ok := h.RepositoryProviderForHost("code.acme.test"); ok {
			t.Error("by host")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a list waited for startup")
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := h.RepositoryParse(ctx, acmeProvider, "https://code.acme.test/team/ok")
	if took := time.Since(start); providerErr(t, err).Code != "provider_off" || took < 50*time.Millisecond {
		t.Fatalf("not started: %v after %s", err, took)
	}

	// Started while a call waits: it goes on then, not when its context ends.
	start = time.Now()
	time.AfterFunc(100*time.Millisecond, func() { close(h.started) })
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = h.RepositoryCheck(ctx, acmeRepository("ok"))
	if took := time.Since(start); providerErr(t, err).Code != "provider_off" || took < 100*time.Millisecond || took > 2*time.Second {
		t.Fatalf("started during the call: %v after %s", err, took)
	}
}

// The hosts no extension may claim are the ones the built-in providers read.
func TestRepositoryHostsReservedForBuiltins(t *testing.T) {
	reserved, builtin := slices.Clone(extension.ReservedRepositoryHosts), slices.Clone(repository.BuiltinHosts)
	slices.Sort(reserved)
	slices.Sort(builtin)
	if !slices.Equal(reserved, builtin) {
		t.Fatalf("extension.ReservedRepositoryHosts %v, repository.BuiltinHosts %v", reserved, builtin)
	}
}
