package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

// A managed Codex provider's gateway may serve Codex's own model catalog: each model's context window, compaction
// limit, thinking levels and instructions, in the shape Codex reads from its backend (codex-rs ModelInfo). Without it
// Codex treats the gateway as OpenAI: its picker lists the bundled OpenAI models and a model only the gateway has runs
// on fallback metadata. Codex reads a catalog from a provider's model_catalog_url (0.156 and later; by default since
// 0.161, when api_key_model_discovery became stable), asking `<url>?client_version=<its version>` with the provider's
// key. Once a provider has one, it is the only list Codex uses: one that fails leaves Codex with no list, so the
// URL is passed only where the node found a catalog there, and kept through a probe that could not tell.
//
// The node looks for it by the answer's shape, never the gateway's name (docs/integrations/codex.md, "Gateway model
// catalog"): the probe asks what Codex would, and a catalog is an object whose `models` are Codex model entries.

// codexCatalogPaths are where, below the provider's base URL (the one ending in /v1), a gateway may serve the catalog,
// most likely first: Codex's own path for its backend's catalog (some gateways answer it in Codex's shape when the
// request carries client_version, and with OpenAI's list otherwise), then /codex/models (a gateway that keeps /models
// for OpenAI's list).
var codexCatalogPaths = []string{"/models", "/codex/models"}

// codexCatalogMinVersion is the first Codex that reads a provider's catalog without a feature switch.
var codexCatalogMinVersion = [3]int{0, 161, 0}

// codexCatalogTimeout bounds the whole probe (both paths at once), as Codex bounds its own catalog request.
var codexCatalogTimeout = 5 * time.Second

// codexCatalogTTL: an answer, a catalog or none, is fresh this long per gateway, key, proxy and Codex version.
const codexCatalogTTL = 10 * time.Minute

// codexCatalogStale: an answer older than codexCatalogTTL but younger than this is still given at once, and the gateway
// is asked again in the background for the next start: once a gateway has answered, a start never waits on the probe.
const codexCatalogStale = 24 * time.Hour

// codexCatalogRetry: after a probe that could not tell (no answer in time, a 5xx, 408 or 429), or a codex --version
// that failed, it is tried again this soon; a catalog found before is kept meanwhile.
const codexCatalogRetry = time.Minute

// codexCatalogMost is the most of an answer the probe reads; a larger one is not taken for a catalog.
const codexCatalogMost = 32 << 20

// codexCatalogWait is the most launcher.env waits for the whole lookup (finding codex, its version, the probe): well
// under the 10 seconds `personad launch` waits for the node (nodecmd.LaunchNodeWait), so a lookup never fails a start.
// Past it the answer has no catalog, as before, and the lookup goes on: what it finds is kept for the next start.
var codexCatalogWait = 2 * time.Second

// codexVersionTimeout bounds one `codex --version`.
var codexVersionTimeout = 5 * time.Second

var codexCatalogLookups singleflight.Group

// CodexCatalogWait is codexCatalogWait, for nodecmd's check that `personad launch` waits well longer for the node.
func CodexCatalogWait() time.Duration { return codexCatalogWait }

// codexCatalogAnswer is what the probe last learned of a gateway.
type codexCatalogAnswer struct {
	url   string    // the catalog, "" for none
	at    time.Time // when the gateway said so
	fresh time.Time // until when it is not asked again
}

var codexCatalogCache = struct {
	sync.Mutex
	got    map[string]codexCatalogAnswer
	flight singleflight.Group
}{got: map[string]codexCatalogAnswer{}}

// codexCatalogInput is what a provider's catalog lookup depends on, as its codex will see it (managedEnv: the
// provider's own env included): the gateway, the key and the proxies, with id naming them for one lookup at a time.
type codexCatalogInput struct {
	name, base, key, id string
	env                 map[string]string
}

// codexCatalogInputOf is l's lookup input; false when l is no Codex provider with a gateway and a key of Persona's to
// describe (its arguments choose their own provider or profile).
func codexCatalogInputOf(l proto.ManagedLauncher) (codexCatalogInput, bool) {
	if l.AgentType() != "codex" || codexChoosesProvider(l.Args) {
		return codexCatalogInput{}, false
	}
	own := managedEnv(l)
	base, key := strings.TrimRight(envValue(own, "OPENAI_BASE_URL"), "/"), envValue(own, "OPENAI_API_KEY")
	if u, err := url.Parse(base); base == "" || key == "" || err != nil || u.RawQuery != "" || u.Fragment != "" {
		return codexCatalogInput{}, false
	}
	env := map[string]string{}
	for _, kv := range append(os.Environ(), own...) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	sum := sha256.Sum256([]byte(key))
	id := strings.Join([]string{base, hex.EncodeToString(sum[:]), env["http_proxy"], env["HTTP_PROXY"], env["https_proxy"],
		env["HTTPS_PROXY"], env["no_proxy"], env["NO_PROXY"]}, "\x00")
	return codexCatalogInput{name: l.Name, base: base, key: key, id: id, env: env}, true
}

// codexCatalogFor is codexCatalog as launcher.env asks for it: waited for at most codexCatalogWait, after which l's
// codex starts without a catalog while the lookup goes on (one per gateway, key and proxy at a time).
func (n *Node) codexCatalogFor(l proto.ManagedLauncher) (catalog, codex string) {
	in, ok := codexCatalogInputOf(l)
	if !ok {
		return "", ""
	}
	got := codexCatalogLookups.DoChan(in.id, func() (any, error) {
		var v [2]string
		defer n.survive(in.name, "looking for its gateway's Codex model catalog") // a goroutine of singleflight's
		v[0], v[1] = n.codexCatalogOf(context.Background(), in)
		return v, nil
	})
	wait := time.NewTimer(codexCatalogWait)
	defer wait.Stop()
	select {
	case r := <-got:
		v, _ := r.Val.([2]string)
		return v[0], v[1]
	case <-wait.C:
		n.logf("launcher %s: no answer on its gateway's Codex model catalog within %s; starting without it, still asking", l.Name, codexCatalogWait)
		return "", ""
	}
}

// survive, deferred in a goroutine of the lookup's own, logs a panic there instead of ending the node with it: the
// lookup then found nothing.
func (n *Node) survive(launcher, what string) {
	if r := recover(); r != nil {
		n.logf("launcher %s: %s failed: %v", launcher, what, r)
	}
}

// codexCatalog is the model catalog URL for l's codex (no client_version: Codex adds its own) and the codex executable
// whose version allowed it; "" when l is no Codex provider with a gateway and a key, this computer's codex is too old
// to read one, or its gateway serves none. It waits for the lookup; launcher.env uses codexCatalogFor.
func (n *Node) codexCatalog(ctx context.Context, l proto.ManagedLauncher) (catalog, codex string) {
	in, ok := codexCatalogInputOf(l)
	if !ok {
		return "", ""
	}
	return n.codexCatalogOf(ctx, in)
}

func (n *Node) codexCatalogOf(ctx context.Context, in codexCatalogInput) (catalog, codex string) {
	exe, version := codexVersion(ctx)
	if !codexReadsCatalog(version) {
		return "", ""
	}
	cacheKey := version + "\x00" + in.id
	ask := func() (v any, err error) {
		v = "" // what a panic below leaves
		defer n.survive(in.name, "asking its gateway for Codex's model catalog")
		found, sure := probeCodexCatalog(context.WithoutCancel(ctx), in.base, in.key, version, in.env)
		now := time.Now()
		codexCatalogCache.Lock()
		defer codexCatalogCache.Unlock()
		prev, had := codexCatalogCache.got[cacheKey]
		a := codexCatalogAnswer{url: found, at: now, fresh: now.Add(codexCatalogTTL)}
		if found == "" && !sure {
			a.fresh = now.Add(codexCatalogRetry)
			if had && prev.url != "" { // a gateway that did not answer this time still has the catalog it had
				a.url, a.at = prev.url, prev.at
			}
		}
		if found != "" && prev.url != found { // the path only: a base URL can carry credentials
			n.logf("launcher %s: its gateway serves Codex's model catalog (%s below its base URL)", in.name, strings.TrimPrefix(found, in.base))
		}
		if !had && len(codexCatalogCache.got) >= 64 {
			codexCatalogCache.got = map[string]codexCatalogAnswer{}
		}
		codexCatalogCache.got[cacheKey] = a
		return a.url, nil
	}
	codexCatalogCache.Lock()
	a, ok := codexCatalogCache.got[cacheKey]
	codexCatalogCache.Unlock()
	switch now := time.Now(); {
	case ok && now.Before(a.fresh):
	case ok && now.Sub(a.at) < codexCatalogStale:
		codexCatalogCache.flight.DoChan(cacheKey, ask) // for the next start; one already asking is joined
	default:
		v, _, _ := codexCatalogCache.flight.Do(cacheKey, ask)
		a.url, _ = v.(string)
	}
	if a.url == "" {
		return "", ""
	}
	return a.url, exe
}

// probeCodexCatalog asks each of codexCatalogPaths below base at once, as Codex would ask its model_catalog_url, and
// is the first, in that order, that answers with a catalog. With none, sure says whether every path answered (an
// answer that is no catalog), not an error, no answer in time, a body that did not arrive whole in time or a status
// that may pass (5xx, 408, 429).
func probeCodexCatalog(ctx context.Context, base, key, version string, env map[string]string) (catalog string, sure bool) {
	const (
		none = iota
		found
		unsure
	)
	ctx, cancel := context.WithTimeout(ctx, codexCatalogTimeout)
	defer cancel()
	client := gatewayClient(env) // no redirect: Codex follows none for a catalog either
	got := make([]chan int, len(codexCatalogPaths))
	for i, path := range codexCatalogPaths {
		got[i] = make(chan int, 1)
		go func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+url.Values{"client_version": {codexCatalogClientVersion(version)}}.Encode(), nil)
			if err != nil {
				got[i] <- none
				return
			}
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Accept", "application/json")
			modelhttp.SetDefaultUserAgent(req.Header) // Persona's own request, as its other gateway requests (#8775)
			modelhttp.SetOpenCodeSession(req.Header, req.URL.String(), env[openCodeSessionEnv])
			resp, err := client.Do(req)
			switch {
			case err != nil:
				got[i] <- unsure
				return
			case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
				resp.Body.Close()
				got[i] <- unsure
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				got[i] <- none
				return
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, codexCatalogMost+1))
			switch {
			case err != nil: // cut off, or past the probe's time: an answer that did not arrive says nothing
				got[i] <- unsure
			case len(body) <= codexCatalogMost && isCodexCatalog(body):
				got[i] <- found
			default:
				got[i] <- none
			}
		}()
	}
	sure = true
	for i, path := range codexCatalogPaths {
		switch <-got[i] {
		case found:
			return base + path, true
		case unsure:
			sure = false
		}
	}
	return "", sure
}

// codexCatalogFields are the fields every Codex model entry has, with the JSON kind of each ('"' a string, 'b' a
// boolean, 'i' a whole number, '[' a list, '{' an object): those codex-rs ModelInfo cannot do without in every version that
// reads a catalog by default (0.161 to 0.169 alike), slug aside. One without them fails Codex's whole catalog: it then
// lists nothing and runs every model on fallback metadata, worse than no catalog.
var codexCatalogFields = map[string]byte{"display_name": '"', "supported_reasoning_levels": '[', "shell_type": '"',
	"visibility": '"', "supported_in_api": 'b', "priority": 'i', "support_verbosity": 'b', "truncation_policy": '{',
	"experimental_supported_tools": '['}

// jsonKind is raw's kind as codexCatalogFields writes it; 0 for null, nothing or anything else.
func jsonKind(raw json.RawMessage) byte {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return 0
	case raw[0] == '"', raw[0] == '[', raw[0] == '{':
		return raw[0]
	case raw[0] == 't', raw[0] == 'f':
		return 'b'
	case raw[0] == '-', raw[0] >= '0' && raw[0] <= '9':
		if bytes.ContainsAny(raw, ".eE") { // Codex's numbers here are integers: 1.0 or 1e3 fails its decoding
			return 0
		}
		return 'i'
	}
	return 0
}

// fieldsOfKinds is obj's fields when it is a JSON object with each of fields of its kind; nil otherwise.
func fieldsOfKinds(obj json.RawMessage, fields map[string]byte) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if jsonKind(obj) != '{' || json.Unmarshal(obj, &m) != nil {
		return nil
	}
	for name, kind := range fields {
		if jsonKind(m[name]) != kind {
			return nil
		}
	}
	return m
}

// isCodexCatalog: body is a catalog Codex can read, `{"models":[…]}` with at least one entry, each with a unique,
// non-empty slug and every field of codexCatalogFields (each thinking level an effort and a description, the truncation
// policy a mode and a limit), and one at least that its picker lists. OpenAI's model list ({"data":[…]}) is none. The
// words inside (a level, a shell type, a visibility) are not checked: they change with Codex, whose version the gateway
// was told.
func isCodexCatalog(body []byte) bool {
	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Models) == 0 {
		return false
	}
	seen, listed := map[string]bool{}, false
	for _, raw := range doc.Models {
		m := fieldsOfKinds(raw, codexCatalogFields)
		var slug, visibility string
		var levels []json.RawMessage
		if m == nil || jsonKind(m["slug"]) != '"' || json.Unmarshal(m["slug"], &slug) != nil || strings.TrimSpace(slug) == "" ||
			seen[slug] || json.Unmarshal(m["visibility"], &visibility) != nil || json.Unmarshal(m["supported_reasoning_levels"], &levels) != nil ||
			fieldsOfKinds(m["truncation_policy"], map[string]byte{"mode": '"', "limit": 'i'}) == nil {
			return false
		}
		for _, level := range levels {
			if fieldsOfKinds(level, map[string]byte{"effort": '"', "description": '"'}) == nil {
				return false
			}
		}
		seen[slug] = true
		listed = listed || visibility == "list"
	}
	return listed
}

var codexVersionCache = struct {
	sync.Mutex
	stamp, version string
	read           bool      // version is what codex said, not a failed run's ""
	until          time.Time // fresh until then
}{}

var codexVersionReads singleflight.Group

// codexMissingUntil: no codex was found (agentExecutable may run a login shell for it) until then; not asked again
// before it.
var codexMissingUntil struct {
	sync.Mutex
	until time.Time
}

// codexVersion is this computer's codex (agentExecutable's) and its version (codexVersionAt).
func codexVersion(ctx context.Context) (exe, version string) {
	codexMissingUntil.Lock()
	missing := time.Now().Before(codexMissingUntil.until)
	codexMissingUntil.Unlock()
	if missing {
		return "", ""
	}
	exe, err := agentExecutable("codex")
	if err != nil {
		codexMissingUntil.Lock()
		codexMissingUntil.until = time.Now().Add(codexCatalogRetry)
		codexMissingUntil.Unlock()
		return "", ""
	}
	return exe, codexVersionAt(ctx, exe)
}

// codexVersionAt is the version of the codex at exe as Codex sends it with a catalog request (major.minor.patch, from
// `codex --version`), with a "-" suffix for a prerelease; "" when it says none or cannot be run within ctx and
// codexVersionTimeout. A version read is fresh for codexCatalogTTL while the executable is unchanged; after that it
// is still given at once and read again in the background, since a shim (volta, mise, asdf) stays the same file when
// the codex behind it is upgraded, and a slow one would otherwise hold that start. A run that failed or hung is tried
// again after codexCatalogRetry, not on every start.
func codexVersionAt(ctx context.Context, exe string) string {
	st, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	stamp := exe + "\x00" + st.ModTime().String() + "\x00" + strconv.FormatInt(st.Size(), 10)
	codexVersionCache.Lock()
	same, version, read, fresh := codexVersionCache.stamp == stamp, codexVersionCache.version, codexVersionCache.read,
		time.Now().Before(codexVersionCache.until)
	codexVersionCache.Unlock()
	switch {
	case same && fresh:
		return version
	case same && read:
		codexVersionReads.DoChan(stamp, func() (any, error) { return readCodexVersion(context.Background(), exe, stamp), nil })
		return version
	}
	return readCodexVersion(ctx, exe, stamp)
}

// readCodexVersion runs `exe --version` and keeps what it says for stamp: a version for codexCatalogTTL, a failed run
// for codexCatalogRetry (a version read before for stamp stays meanwhile). A run cut short by ctx keeps nothing: the
// caller gave up, codex said nothing.
func readCodexVersion(ctx context.Context, exe, stamp string) string {
	run, cancel := context.WithTimeout(ctx, codexVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(run, exe, "--version")
	cmd.WaitDelay = time.Second // a wrapper's child that keeps stdout open does not hold the answer past the timeout
	out, err := cmd.Output()
	if err != nil && ctx.Err() != nil {
		return ""
	}
	codexVersionCache.Lock()
	defer codexVersionCache.Unlock()
	if err != nil {
		if codexVersionCache.stamp == stamp && codexVersionCache.read {
			codexVersionCache.until = time.Now().Add(codexCatalogRetry)
			return codexVersionCache.version
		}
		codexVersionCache.stamp, codexVersionCache.version, codexVersionCache.read = stamp, "", false
		codexVersionCache.until = time.Now().Add(codexCatalogRetry)
		return ""
	}
	version := parseCodexVersion(string(out))
	codexVersionCache.stamp, codexVersionCache.version, codexVersionCache.read = stamp, version, true
	codexVersionCache.until = time.Now().Add(codexCatalogTTL)
	return version
}

// parseCodexVersion reads `codex --version`'s "codex-cli 0.167.0": its first word that is a version, "0.167.0", or
// "0.168.0-" for a prerelease (0.168.0-alpha.2); "" when there is none.
func parseCodexVersion(out string) string {
	for _, word := range strings.Fields(out) {
		v, _, pre := strings.Cut(strings.SplitN(word, "+", 2)[0], "-")
		parts := strings.Split(v, ".")
		if len(parts) != 3 || slices.ContainsFunc(parts, func(p string) bool { _, err := strconv.Atoi(p); return err != nil }) {
			continue
		}
		if pre {
			return v + "-"
		}
		return v
	}
	return ""
}

// codexReadsCatalog: a codex of version (parseCodexVersion's) reads a provider's catalog by default; a prerelease of
// the first one that does is taken for one that does not yet.
func codexReadsCatalog(version string) bool {
	if version == "" {
		return false
	}
	v, pre := strings.CutSuffix(version, "-")
	var got [3]int
	for i, p := range strings.Split(v, ".") {
		got[i], _ = strconv.Atoi(p)
	}
	for i := range got {
		if got[i] != codexCatalogMinVersion[i] {
			return got[i] > codexCatalogMinVersion[i]
		}
	}
	return !pre
}

// codexCatalogClientVersion is the client_version Codex sends for version: major.minor.patch, no prerelease.
func codexCatalogClientVersion(version string) string { return strings.TrimSuffix(version, "-") }

// sameFile: paths a and b name one file (a link and its target too); false when either is empty or missing.
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}
