package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

// catalogEntry is a Codex model entry with every field Codex requires (codexCatalogFields), and a few it reads.
func catalogEntry(slug, visibility string, levels ...string) map[string]any {
	ls := []map[string]any{}
	for _, l := range levels {
		ls = append(ls, map[string]any{"effort": l, "description": l})
	}
	return map[string]any{"slug": slug, "display_name": slug, "description": "From example-gateway", "visibility": visibility,
		"supported_reasoning_levels": ls, "shell_type": "shell_command", "supported_in_api": true, "priority": 1,
		"support_verbosity": false, "truncation_policy": map[string]any{"mode": "tokens", "limit": 10000},
		"experimental_supported_tools": []string{}, "context_window": 1000000}
}

func catalogJSON(entries ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"models": entries})
	return string(b)
}

// codexCatalogBody is a Codex model catalog as a gateway serves it: a listed model with levels, one that takes none,
// and a hidden one.
var codexCatalogBody = catalogJSON(catalogEntry("example-big", "list", "low", "high"), catalogEntry("example-plain", "list"),
	catalogEntry("example-hidden", "hide", "low"))

// openAIModelsBody is OpenAI's model list, which Codex cannot read as a catalog.
const openAIModelsBody = `{"object":"list","data":[{"id":"example-big","object":"model"}]}`

// fakeCodex puts a codex on PATH that says it is version (codex --version), and forgets what earlier tests probed.
func fakeCodex(t *testing.T, version string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as codex")
	}
	bin := t.TempDir()
	if version != "" {
		writeFile(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho 'codex-cli "+version+"'\n")
		if err := os.Chmod(filepath.Join(bin, "codex"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir()) // agentExecutable's fallbacks look under the home and in the system's places
	t.Setenv("PERSONA_NODEJS", "")
	hideMachineAgents(t) // and under NVM_DIR: a codex installed with nvm would answer for the fake
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	forgetCodexCatalogs(t)
}

func forgetCodexCatalogs(t *testing.T) {
	t.Helper()
	reset := func() {
		codexCatalogCache.Lock()
		codexCatalogCache.got = map[string]codexCatalogAnswer{}
		codexCatalogCache.Unlock()
		codexVersionCache.Lock()
		codexVersionCache.stamp, codexVersionCache.version, codexVersionCache.read, codexVersionCache.until = "", "", false, time.Time{}
		codexVersionCache.Unlock()
		codexMissingUntil.Lock()
		codexMissingUntil.until = time.Time{}
		codexMissingUntil.Unlock()
		agentExecutableMu.Lock()
		delete(agentExecutableCache, "codex")
		agentExecutableMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// catalogGateway serves handler and records each request's path, query and Authorization.
type catalogGateway struct {
	*httptest.Server
	mu   sync.Mutex
	seen []*http.Request
}

func newCatalogGateway(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *catalogGateway {
	t.Helper()
	g := &catalogGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" && slices.Contains(portProbeAgents, r.Header.Get("User-Agent")) {
			return // a Persona node on the machine probing this server among an agent's ports (ports.go), not the probe tested
		}
		g.mu.Lock()
		g.seen = append(g.seen, r.Clone(context.Background()))
		g.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *catalogGateway) requests() []*http.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*http.Request(nil), g.seen...)
}

func codexProvider(base string) proto.ManagedLauncher {
	return proto.ManagedLauncher{Name: "gw", Agent: "codex", BaseURL: base, APIKey: "sk-test", Model: "example-big"}
}

func quietNode() *Node { return &Node{logger: log.New(io.Discard, "", 0)} }

// catalogOf is the catalog URL the node gives l's codex.
func catalogOf(n *Node, l proto.ManagedLauncher) string {
	u, _ := n.codexCatalog(context.Background(), l)
	return u
}

// ageCodexCatalogs makes every kept answer d older, as if it had been given d earlier.
func ageCodexCatalogs(d time.Duration) {
	codexCatalogCache.Lock()
	defer codexCatalogCache.Unlock()
	for k, a := range codexCatalogCache.got {
		a.at, a.fresh = a.at.Add(-d), a.fresh.Add(-d)
		codexCatalogCache.got[k] = a
	}
}

// waitAsked waits until g has had n requests and the answer they gave is kept (fresh again).
func waitAsked(t *testing.T, g *catalogGateway, n int) {
	t.Helper()
	waitWithin(t, 10*time.Second, "the gateway asked again and its answer kept", func() bool {
		if len(g.requests()) < n {
			return false
		}
		codexCatalogCache.Lock()
		defer codexCatalogCache.Unlock()
		kept := len(codexCatalogCache.got) > 0
		for _, a := range codexCatalogCache.got {
			kept = kept && time.Now().Before(a.fresh)
		}
		return kept
	})
}

// Each place a gateway may serve Codex's catalog is found by what it answers, the first in order winning; OpenAI's
// list, an error, a body that is no catalog or no answer in time is none.
func TestCodexCatalogFoundByShape(t *testing.T) {
	catalogAt := func(paths ...string) func(w http.ResponseWriter, r *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			for _, p := range paths {
				if r.URL.Path == p && r.URL.Query().Get("client_version") != "" {
					io.WriteString(w, codexCatalogBody)
					return
				}
			}
			if r.URL.Path == "/v1/models" {
				io.WriteString(w, openAIModelsBody)
				return
			}
			http.NotFound(w, r)
		}
	}
	body := func(code int, s string) func(w http.ResponseWriter, r *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			io.WriteString(w, s)
		}
	}
	for _, c := range []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		want    string // the path below the server, "" for none
	}{
		{"models with client_version", catalogAt("/v1/models"), "/v1/models"},
		{"codex models", catalogAt("/v1/codex/models"), "/v1/codex/models"},
		{"both", catalogAt("/v1/codex/models", "/v1/models"), "/v1/models"},
		{"none", catalogAt(), ""},
		{"server error", body(http.StatusInternalServerError, codexCatalogBody), ""},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }, ""},
		{"not JSON", body(http.StatusOK, "<html>sign in</html>"), ""},
		{"empty", body(http.StatusOK, `{"models":[]}`), ""},
		{"a catalog Codex cannot read", body(http.StatusOK, catalogJSON(map[string]any{"slug": "a", "display_name": "A",
			"visibility": "list", "supported_reasoning_levels": []any{}})), ""},
		{"cut off", cutOff(codexCatalogBody[:len(codexCatalogBody)/2]), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeCodex(t, "0.167.0")
			// The paths are asked at once: each answer waits until every path was asked (or 2 s), so the first path's
			// catalog cannot end the probe, and cancel the other request, before that request reaches the gateway.
			var asked atomic.Int32
			all := make(chan struct{})
			var allOnce sync.Once
			g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if asked.Add(1) >= int32(len(codexCatalogPaths)) {
					allOnce.Do(func() { close(all) })
				}
				select {
				case <-all:
				case <-time.After(2 * time.Second):
				}
				c.handler(w, r)
			})
			got := catalogOf(quietNode(), codexProvider(g.URL+"/v1"))
			want := ""
			if c.want != "" {
				want = g.URL + c.want
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
			reqs := g.requests()
			if len(reqs) != len(codexCatalogPaths) {
				t.Fatalf("%d requests, want one per path", len(reqs))
			}
			for _, r := range reqs {
				if r.Method != http.MethodGet || r.URL.RawQuery != "client_version=0.167.0" || r.Header.Get("Authorization") != "Bearer sk-test" {
					t.Fatalf("asked %s %s with %q, want what codex asks", r.Method, r.URL, r.Header.Get("Authorization"))
				}
				if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "Persona/") {
					t.Fatalf("User-Agent %q, want Persona's own", ua)
				}
			}
		})
	}
	t.Run("no answer in time", func(t *testing.T) {
		fakeCodex(t, "0.167.0")
		old := codexCatalogTimeout
		codexCatalogTimeout = 200 * time.Millisecond
		t.Cleanup(func() { codexCatalogTimeout = old })
		release := make(chan struct{})
		g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		defer close(release)
		start := time.Now()
		if got := catalogOf(quietNode(), codexProvider(g.URL+"/v1")); got != "" {
			t.Fatalf("got %q", got)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Fatalf("the probe held the start %s", took)
		}
	})
}

// No probe, and no catalog, where codex would not read one or the provider is not Persona's to describe.
func TestCodexCatalogNotProbed(t *testing.T) {
	for _, c := range []struct {
		name    string
		version string
		edit    func(*proto.ManagedLauncher)
	}{
		{"codex before catalogs by default", "0.160.0", nil},
		{"a prerelease of the first one", "0.161.0-alpha.2", nil},
		{"no codex", "", nil},
		{"no key", "0.167.0", func(l *proto.ManagedLauncher) { l.APIKey = "" }},
		{"key emptied by the provider's env", "0.167.0", func(l *proto.ManagedLauncher) { l.Env = "OPENAI_API_KEY=" }},
		{"its own provider", "0.167.0", func(l *proto.ManagedLauncher) { l.Args = []string{"-c", "model_provider=mine"} }},
		{"a profile", "0.167.0", func(l *proto.ManagedLauncher) { l.Args = []string{"--profile", "work"} }},
		{"a Claude provider", "0.167.0", func(l *proto.ManagedLauncher) { l.Agent = "" }},
		{"a base with a query", "0.167.0", func(l *proto.ManagedLauncher) { l.BaseURL += "?team=a" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeCodex(t, c.version)
			g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, codexCatalogBody) })
			l := codexProvider(g.URL + "/v1")
			if c.edit != nil {
				c.edit(&l)
			}
			if got := catalogOf(quietNode(), l); got != "" {
				t.Fatalf("got %q", got)
			}
			if reqs := g.requests(); len(reqs) != 0 {
				t.Fatalf("asked the gateway %s", reqs[0].URL)
			}
		})
	}
	t.Run("no gateway", func(t *testing.T) {
		fakeCodex(t, "0.167.0")
		if got := catalogOf(quietNode(), proto.ManagedLauncher{Name: "plain", Agent: "codex", APIKey: "k"}); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

// A probe's answer is kept for the same gateway, key and codex, and another key asks again. Past its freshness it is
// still given at once while the gateway is asked again in the background; an answer that could not tell keeps the
// catalog found before (and is retried soon), and only a gateway that says it has none drops it.
func TestCodexCatalogKeptAndAskedAgain(t *testing.T) {
	fakeCodex(t, "0.169.0")
	var mu sync.Mutex
	serve := "catalog"
	g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mode := serve
		mu.Unlock()
		switch {
		case mode == "down":
			w.WriteHeader(http.StatusBadGateway)
		case mode == "catalog" && r.URL.Path == "/v1/codex/models":
			io.WriteString(w, codexCatalogBody)
		default:
			io.WriteString(w, openAIModelsBody)
		}
	})
	set := func(mode string) { mu.Lock(); serve = mode; mu.Unlock() }
	n := quietNode()
	l := codexProvider(g.URL + "/v1")
	want := g.URL + "/v1/codex/models"
	for range 3 {
		if got := catalogOf(n, l); got != want {
			t.Fatalf("got %q", got)
		}
	}
	paths := len(codexCatalogPaths)
	if reqs := g.requests(); len(reqs) != paths {
		t.Fatalf("%d requests for three starts, want one probe", len(reqs))
	}
	if r := g.requests()[0]; r.URL.Query().Get("client_version") != "0.169.0" {
		t.Fatalf("client_version %q", r.URL.Query().Get("client_version"))
	}

	// Past its freshness, with the gateway down for a while: the catalog at once, asked again, still kept.
	set("down")
	ageCodexCatalogs(codexCatalogTTL + time.Second)
	if got := catalogOf(n, l); got != want {
		t.Fatalf("a stale answer: got %q", got)
	}
	waitAsked(t, g, 2*paths)
	if got := catalogOf(n, l); got != want {
		t.Fatalf("after a probe that could not tell: got %q", got)
	}
	if reqs := g.requests(); len(reqs) != 2*paths {
		t.Fatalf("asked again at once after a probe that could not tell: %d requests", len(reqs))
	}

	// Retried soon, and a gateway that answers without a catalog drops it.
	set("none")
	ageCodexCatalogs(codexCatalogRetry + time.Second)
	catalogOf(n, l) // the kept one, once more
	waitAsked(t, g, 3*paths)
	if got := catalogOf(n, l); got != "" {
		t.Fatalf("a gateway with no catalog any more: got %q", got)
	}

	// An answer older than codexCatalogStale is not given: the start asks itself.
	set("catalog")
	ageCodexCatalogs(codexCatalogStale)
	if got := catalogOf(n, l); got != want {
		t.Fatalf("a day-old answer: got %q", got)
	}

	before := len(g.requests())
	l.APIKey = "sk-other"
	catalogOf(n, l)
	if reqs := g.requests(); len(reqs) != before+paths {
		t.Fatalf("another key was answered from the first one's probe")
	}
}

// A codex whose --version fails is not run again on every start, and is again once codexCatalogRetry has passed.
func TestCodexVersionFailureKeptBriefly(t *testing.T) {
	fakeCodex(t, "0.167.0")
	exe, err := agentExecutable("codex")
	if err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(t.TempDir(), "runs")
	writeFile(t, exe, "#!/bin/sh\necho run >> '"+runs+"'\nexit 1\n")
	for range 3 {
		if _, v := codexVersion(context.Background()); v != "" {
			t.Fatalf("version %q", v)
		}
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "run") != 1 {
		t.Fatalf("ran codex --version %d times", strings.Count(string(b), "run"))
	}
	codexVersionCache.Lock()
	codexVersionCache.until = time.Now().Add(-time.Second)
	codexVersionCache.Unlock()
	codexVersion(context.Background())
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "run") != 2 {
		t.Fatal("not asked again after codexCatalogRetry")
	}
}

// launcher.env carries the catalog URL to personad launch, and nothing when there is none.
func TestLauncherEnvCodexCatalog(t *testing.T) {
	fakeCodex(t, "0.167.0")
	with := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("client_version") != "" && r.URL.Path == "/v1/models" {
			io.WriteString(w, codexCatalogBody)
			return
		}
		io.WriteString(w, openAIModelsBody)
	})
	without := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, openAIModelsBody) })
	n := quietNode()
	a, b := codexProvider(with.URL+"/v1"), codexProvider(without.URL+"/v1")
	a.Name, b.Name = "with", "without"
	n.setManaged([]proto.ManagedLauncher{a, b})
	exe, _ := agentExecutable("codex")
	if env := n.launcherEnv("with"); env["codex_catalog_url"] != with.URL+"/v1/models" || env["codex_catalog_codex"] != exe {
		t.Fatalf("with a catalog: %v, checked codex %v", env["codex_catalog_url"], env["codex_catalog_codex"])
	}
	if env := n.launcherEnv("without"); env["codex_catalog_url"] != nil || env["codex_catalog_codex"] != nil {
		t.Fatalf("without a catalog: %v", env)
	}
}

func TestCodexVersionWords(t *testing.T) {
	for _, c := range []struct {
		out, version string
		reads        bool
	}{
		{"codex-cli 0.167.0\n", "0.167.0", true},
		{"codex-cli 0.161.0", "0.161.0", true},
		{"codex-cli 1.0.0", "1.0.0", true},
		{"codex-cli 0.160.9", "0.160.9", false},
		{"codex-cli 0.161.0-alpha.2", "0.161.0-", false},
		{"codex-cli 0.170.0-alpha.1", "0.170.0-", true},
		{"codex-cli 0.167.0+build.5", "0.167.0", true},
		{"codex-cli", "", false},
		{"", "", false},
		{"codex-cli 0.167", "", false},
		{"codex-cli v0.167.0", "", false},
		{"codex-cli 0.167.0\nnote: a newer version is out\n", "0.167.0", true},
	} {
		v := parseCodexVersion(c.out)
		if v != c.version || codexReadsCatalog(v) != c.reads {
			t.Errorf("%q: version %q reads %v, want %q %v", c.out, v, codexReadsCatalog(v), c.version, c.reads)
		}
	}
	if got := codexCatalogClientVersion("0.170.0-"); got != "0.170.0" {
		t.Errorf("client_version %q", got)
	}
	if strings.Contains(codexCatalogClientVersion("0.167.0"), "-") {
		t.Error("a release's client_version")
	}
}

// cutOff answers 200 with a length it does not send: part, then the connection closes.
func cutOff(part string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		io.WriteString(w, part)
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}
}

// A catalog is one Codex can read: every entry with every field Codex requires, of its kind, nested ones too. One that
// lacks any (Codex then fails the whole list, lists nothing and runs models on fallback metadata) is none.
func TestIsCodexCatalogNeedsWhatCodexNeeds(t *testing.T) {
	if !isCodexCatalog([]byte(codexCatalogBody)) {
		t.Fatal("a whole catalog is none")
	}
	without := func(field string) map[string]any {
		e := catalogEntry("a", "list", "low")
		delete(e, field)
		return e
	}
	with := func(field string, v any) map[string]any {
		e := catalogEntry("a", "list", "low")
		e[field] = v
		return e
	}
	cases := map[string]string{
		"empty":         `{"models":[]}`,
		"not JSON":      `{"models":[`,
		"OpenAI's list": openAIModelsBody,
		"the four fields the first check read": catalogJSON(map[string]any{"slug": "a", "display_name": "A", "visibility": "list",
			"supported_reasoning_levels": []any{}}),
		"duplicate slug":         catalogJSON(catalogEntry("a", "list"), catalogEntry("a", "list")),
		"empty slug":             catalogJSON(catalogEntry(" ", "list")),
		"slug a number":          catalogJSON(with("slug", 7)),
		"nothing listed":         catalogJSON(catalogEntry("a", "hide")),
		"priority a string":      catalogJSON(with("priority", "1")),
		"priority a fraction":    catalogJSON(with("priority", 1.5)),
		"limit in exponent form": catalogJSON(with("truncation_policy", map[string]any{"mode": "tokens", "limit": json.RawMessage("1e4")})),
		"levels null":            catalogJSON(with("supported_reasoning_levels", nil)),
		"a level no description": catalogJSON(with("supported_reasoning_levels", []any{map[string]any{"effort": "low"}})),
		"a level a word":         catalogJSON(with("supported_reasoning_levels", []any{"low"})),
		"truncation no limit":    catalogJSON(with("truncation_policy", map[string]any{"mode": "tokens"})),
		"tools null":             catalogJSON(with("experimental_supported_tools", nil)),
		"no slug":                catalogJSON(without("slug")),
	}
	for field := range codexCatalogFields {
		cases["no "+field] = catalogJSON(without(field))
	}
	for name, body := range cases {
		if isCodexCatalog([]byte(body)) {
			t.Errorf("%s: taken for a catalog", name)
		}
	}
	// What Codex reads with a default may be missing.
	e := catalogEntry("a", "list")
	delete(e, "description")
	delete(e, "context_window")
	if !isCodexCatalog([]byte(catalogJSON(e))) {
		t.Error("an entry without the optional fields is none")
	}
}

// launcher.env waits for the lookup at most codexCatalogWait (nodecmd's TestLaunchWaitsLongerThanTheCatalogLookup
// keeps it well under what personad launch waits for the node): a gateway or a codex --version that does not answer
// starts the provider's codex without a catalog and never fails the start, and the lookup goes on, so a later start
// has the catalog.
func TestLauncherEnvWaitsBrieflyForTheCatalog(t *testing.T) {
	old := codexCatalogWait
	codexCatalogWait = 100 * time.Millisecond
	t.Cleanup(func() { codexCatalogWait = old })
	for _, slow := range []string{"gateway", "codex --version"} {
		t.Run(slow, func(t *testing.T) {
			fakeCodex(t, "0.167.0")
			release := make(chan struct{})
			released := sync.OnceFunc(func() { close(release) })
			t.Cleanup(released)
			if slow == "codex --version" {
				exe, _ := agentExecutable("codex")
				gate := filepath.Join(t.TempDir(), "gate")
				writeFile(t, exe, "#!/bin/sh\nwhile [ ! -e '"+gate+"' ]; do /bin/sleep 0.05; done\necho 'codex-cli 0.167.0'\n")
				go func() { <-release; os.WriteFile(gate, nil, 0o600) }()
			}
			g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if slow == "gateway" {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				if r.URL.Path == "/v1/models" {
					io.WriteString(w, codexCatalogBody)
				}
			})
			n := quietNode()
			n.setManaged([]proto.ManagedLauncher{codexProvider(g.URL + "/v1")})
			start := time.Now()
			if env := n.launcherEnv("gw"); env["codex_catalog_url"] != nil {
				t.Fatalf("waited for the catalog: %v", env["codex_catalog_url"])
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Fatalf("launcher.env took %s", took)
			}
			released()
			waitWithin(t, 10*time.Second, "the lookup kept what it found", func() bool {
				return n.launcherEnv("gw")["codex_catalog_url"] == g.URL+"/v1/models"
			})
		})
	}
}

// A body that does not arrive whole is a probe that cannot tell: the catalog found before is kept.
func TestCodexCatalogKeptThroughACutOffAnswer(t *testing.T) {
	fakeCodex(t, "0.167.0")
	var mu sync.Mutex
	cut := false
	g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := cut
		mu.Unlock()
		switch {
		case r.URL.Path != "/v1/models":
			http.NotFound(w, r)
		case c:
			cutOff(`{"models":[`)(w, r)
		default:
			io.WriteString(w, codexCatalogBody)
		}
	})
	n := quietNode()
	l := codexProvider(g.URL + "/v1")
	if got := catalogOf(n, l); got != g.URL+"/v1/models" {
		t.Fatalf("got %q", got)
	}
	mu.Lock()
	cut = true
	mu.Unlock()
	if found, sure := probeCodexCatalog(context.Background(), g.URL+"/v1", "k", "0.167.0", nil); found != "" || sure {
		t.Fatalf("a cut-off answer: found %q, sure %v", found, sure)
	}
	asked := len(g.requests())
	ageCodexCatalogs(codexCatalogTTL + time.Second)
	catalogOf(n, l)
	waitAsked(t, g, asked+len(codexCatalogPaths))
	if got := catalogOf(n, l); got != g.URL+"/v1/models" {
		t.Fatalf("after a cut-off answer: got %q", got)
	}
}

// A codex behind a shim is the same file before and after an upgrade: its version is read again after
// codexCatalogTTL, in the background, the version read before given meanwhile, so a slow shim holds no start.
func TestCodexVersionReadAgainBehindAShim(t *testing.T) {
	fakeCodex(t, "0.167.0")
	exe, _ := agentExecutable("codex")
	dir := t.TempDir()
	said, slow := filepath.Join(dir, "version"), filepath.Join(dir, "slow")
	writeFile(t, said, "codex-cli 0.160.0\n")
	writeFile(t, exe, "#!/bin/sh\nif [ -e '"+slow+"' ]; then /bin/sleep 1; fi\n/bin/cat '"+said+"'\n")
	if _, v := codexVersion(context.Background()); v != "0.160.0" {
		t.Fatalf("version %q", v)
	}
	writeFile(t, said, "codex-cli 0.167.0\n") // upgraded behind the shim; the shim is unchanged
	writeFile(t, slow, "")
	if _, v := codexVersion(context.Background()); v != "0.160.0" {
		t.Fatalf("read again at once: %q", v)
	}
	codexVersionCache.Lock()
	kept := time.Until(codexVersionCache.until)
	codexVersionCache.until = time.Now().Add(-time.Second) // as if codexCatalogTTL had passed
	codexVersionCache.Unlock()
	if kept <= 0 || kept > codexCatalogTTL {
		t.Fatalf("the version is kept %s, want at most %s", kept, codexCatalogTTL)
	}
	start := time.Now()
	if _, v := codexVersion(context.Background()); v != "0.160.0" || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("past codexCatalogTTL: %q after %s, want the one read before at once", v, time.Since(start))
	}
	waitWithin(t, 10*time.Second, "the version read again", func() bool {
		_, v := codexVersion(context.Background())
		return v == "0.167.0"
	})
}

// A run its caller gave up on says nothing of codex: it is not kept as a failure.
func TestCodexVersionCallerGivingUpKeepsNothing(t *testing.T) {
	fakeCodex(t, "0.167.0")
	exe, _ := agentExecutable("codex")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v := codexVersionAt(ctx, exe); v != "" {
		t.Fatalf("version %q", v)
	}
	if v := codexVersionAt(context.Background(), exe); v != "0.167.0" {
		t.Fatalf("after a caller gave up: %q", v)
	}
}

// A provider whose gateway and key come only from its own env is looked up like any other.
func TestCodexCatalogForAGatewayInTheProvidersEnv(t *testing.T) {
	fakeCodex(t, "0.167.0")
	g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" && r.Header.Get("Authorization") == "Bearer sk-env" {
			io.WriteString(w, codexCatalogBody)
		}
	})
	n := quietNode()
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", Agent: "codex", Env: "OPENAI_BASE_URL=" + g.URL + "/v1\nOPENAI_API_KEY=sk-env"}})
	if got := n.launcherEnv("gw")["codex_catalog_url"]; got != g.URL+"/v1/models" {
		t.Fatalf("got %v", got)
	}
}

// A panic in a lookup's own goroutine is logged, not the end of the node.
func TestCodexCatalogLookupSurvivesAPanic(t *testing.T) {
	var logged strings.Builder
	n := &Node{logger: log.New(&logged, "", 0)}
	func() {
		defer n.survive("gw", "looking")
		panic("boom")
	}()
	if !strings.Contains(logged.String(), "launcher gw: looking failed: boom") {
		t.Fatalf("logged %q", logged.String())
	}
}

// A codex --version whose child keeps stdout open does not hold the answer past the timeout.
func TestCodexVersionDoesNotWaitOnAChildsOutput(t *testing.T) {
	fakeCodex(t, "0.167.0")
	exe, _ := agentExecutable("codex")
	writeFile(t, exe, "#!/bin/sh\n/bin/sleep 8 &\n/bin/sleep 8\n")
	old := codexVersionTimeout
	codexVersionTimeout = 200 * time.Millisecond
	t.Cleanup(func() { codexVersionTimeout = old })
	start := time.Now()
	if v := codexVersionAt(context.Background(), exe); v != "" {
		t.Fatalf("version %q", v)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("waited %s", took)
	}
}

// No codex found is kept for codexCatalogRetry: agentExecutable may run a login shell for it.
func TestCodexMissingKeptBriefly(t *testing.T) {
	fakeCodex(t, "")
	if exe, v := codexVersion(context.Background()); exe != "" || v != "" {
		t.Fatalf("no codex: %q %q", exe, v)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "codex"), "#!/bin/sh\necho 'codex-cli 0.167.0'\n")
	if err := os.Chmod(filepath.Join(dir, "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, v := codexVersion(context.Background()); v != "" {
		t.Fatalf("looked again at once: %q", v)
	}
	codexMissingUntil.Lock()
	codexMissingUntil.until = time.Now().Add(-time.Second)
	codexMissingUntil.Unlock()
	if _, v := codexVersion(context.Background()); v != "0.167.0" {
		t.Fatalf("after codexCatalogRetry: %q", v)
	}
}

// The node's log names where below the base URL the catalog is, never the base URL: it can carry credentials.
func TestCodexCatalogLogNamesOnlyThePath(t *testing.T) {
	fakeCodex(t, "0.167.0")
	g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/t0ps3cret/v1/codex/models" {
			io.WriteString(w, codexCatalogBody)
		}
	})
	var logged strings.Builder
	n := &Node{logger: log.New(&logged, "", 0)}
	if got := catalogOf(n, codexProvider(g.URL+"/t0ps3cret/v1")); got != g.URL+"/t0ps3cret/v1/codex/models" {
		t.Fatalf("got %q", got)
	}
	host := strings.TrimPrefix(g.URL, "http://")
	if out := logged.String(); !strings.Contains(out, "/codex/models below its base URL") || strings.Contains(out, "t0ps3cret") || strings.Contains(out, host) {
		t.Fatalf("logged %q", out)
	}
}

// launcher.env gives personad launch the node's source allowlist for a Codex provider, which a pane need not have.
func TestLauncherEnvCarriesSourceHostsForCodex(t *testing.T) {
	fakeCodex(t, "0.167.0")
	g := newCatalogGateway(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, openAIModelsBody) })
	n := quietNode()
	l := codexProvider(g.URL + "/v1")
	l.Name = "gw"
	n.setManaged([]proto.ManagedLauncher{l})
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	if got := n.launcherEnv("gw")["model_source_hosts"]; got != "*.example.com" {
		t.Fatalf("with an allowlist: %v", got)
	}
	t.Setenv(modelhttp.SourceHostsEnv, "")
	if got, ok := n.launcherEnv("gw")["model_source_hosts"]; ok {
		t.Fatalf("without one: %v", got)
	}
}
