package assistant

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Provider import links (#5420, #5428, docs/integrations/launchers.md "Import links"): cindy's
// cindy://provider/import?v=1&data=…, Persona's persona://provider/import?v=1&data=… and the web's
// <web address>/settings?tab=models#import=…[&v=1] carry one provider in cindy's v1 JSON, base64url-encoded.
// parsePaste reads a pasted or opened link here. A link comes from
// someone else and may carry a key, so it is checked as strictly as cindy checks it (createProviderImportDraftFromRest,
// decodeBase64Url and parsePayload in cindy's providerImport.ts) before parseCindy reads it as any cindy paste. A link
// failing a check is used for nothing: its one note names at most the path of the field to blame, never a value.

// What cindy takes in a link.
const (
	linkMost           = 32 << 10 // a cindy link after cindy://, a web link's fragment
	linkJSONMost       = 24 << 10 // the JSON its data decodes to
	linkKeyMost        = 4 << 10
	linkBuiltinKeyMost = 1 << 10
	linkTextMost       = 2 << 10 // an address, a header value
	linkEndpointsMost  = 8
	linkModelsMost     = 256
	linkHeadersMost    = 24
)

var (
	linkData        = lazyre.New(`^[A-Za-z0-9_-]+$`)               // base64url without padding
	linkVersionName = lazyre.New(`^[0-9]{1,4}$`)                   // a version a note names
	linkFieldName   = lazyre.New(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`) // an unknown field a note names: a plain word, no value
	linkID          = lazyre.New(`^[a-z0-9_-]{1,40}$`)
)

// The fields v1 defines for each kind of provider, and in all; a model's (cindy's MODEL_METADATA_FIELDS and the rest).
var (
	linkKindFields = map[string][]string{
		"builtin": {"kind", "provider", "apiKey"},
		"preset":  {"kind", "preset", "apiKey"},
		"custom":  {"kind", "id", "name", "auth", "endpoints"},
	}
	linkAnyFields      = []string{"kind", "provider", "preset", "apiKey", "id", "name", "auth", "endpoints"}
	linkEndpointFields = []string{"protocol", "baseUrl", "targets", "models", "modelsUrl", "requestPath", "headers", "apiKey"}
	linkModelFields    = []string{"id", "name", "nativeApi", "mode", "modalities", "officialDocs", "description", "group",
		"contextWindow", "contextWindowMax", "maxOutputTokens", "efforts", "defaultEffort", "supportsFastMode",
		"supportsImageInput", "supportsToolCalls", "reasoningRequired", "defaultEnabled", "reasoning", "reasoningEfforts",
		"reasoningDefaultEffort", "discoveredMetadata"}
	linkOAuthCodeFields = []string{"method", "flow", "authorizeUrl", "tokenUrl", "clientId", "scopes", "modelsDiscoveryUrl",
		"redirectPort", "extraAuthParams"}
	linkOAuthDeviceFields = []string{"method", "flow", "deviceAuthorizationUrl", "tokenUrl", "clientId", "scopes",
		"modelsDiscoveryUrl", "extraDeviceParams"}
	linkAgents  = []string{"claude-code", "codex", "pi"}
	linkEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}
)

// linkPriority is how well an endpoint's protocol serves an agent (cindy's protocolPriority): the best one serves it,
// and two equally good ones leave the choice unsaid.
var linkPriority = map[string]map[string]int{
	"claude-code": {cindyMessages: 100, cindyGoogle: 30, cindyChat: 20, cindyResponses: 10},
	"codex":       {cindyResponses: 30, cindyChat: 20, cindyMessages: 10, cindyGoogle: 10},
	"pi":          {cindyChat: 30, cindyResponses: 20, cindyMessages: 10, cindyGoogle: 10},
}

// parseLink reads t (the paste, trimmed) when it is an import link: one word starting with cindy:// or persona:// (in
// any case), or an http(s) address whose fragment has import. ok is false for anything else, which the other readers take (an
// address with another fragment is no link). Text whose first word is a link but which goes on after a blank is a
// link a chat app broke at a space or a line break: which part of it is the data is unknown, so it is refused whole.
func parseLink(t string) (res pasteResult, ok bool) {
	word := t
	if i := strings.IndexFunc(t, unicode.IsSpace); i >= 0 {
		word = t[:i]
	}
	cindy := hasPrefixFold(word, "cindy://")
	persona := hasPrefixFold(word, "persona://")
	if !cindy && !persona && !hasPrefixFold(word, "http://") && !hasPrefixFold(word, "https://") {
		return res, false
	}
	// a web link: only its fragment is read, what comes before it is the user's own web address
	_, frag, _ := strings.Cut(word, "#")
	params, broken := linkQuery(frag)
	var data string
	var n pasteNote
	switch {
	case !cindy && !persona && params["import"] == nil:
		return res, false
	case word != t:
		n = pasteNote{Code: noteLinkInvalid}
	case cindy:
		data, n = cindyLinkData(word[len("cindy://"):])
	case persona:
		data, n = cindyLinkData(word[len("persona://"):])
	case len(frag) > linkMost:
		n = pasteNote{Code: noteLinkTooLong}
	default:
		data, n = linkParams(params, broken, "import", false)
	}
	var payload []byte
	var obj map[string]any
	if n.Code == "" {
		payload, obj, n = linkPayload(data)
	}
	if n.Code != "" {
		return pasteResult{Format: "link", Notes: []pasteNote{n}}, true
	}
	res = parseCindy(payload)
	res.Format = "link"
	if !linkHasKey(obj) { // the usual link: whoever opens it types their own key, no_key would say so needlessly
		for i := range res.Drafts {
			res.Drafts[i].Notes = slices.DeleteFunc(res.Drafts[i].Notes, func(n pasteNote) bool { return n.Code == noteNoKey })
		}
	}
	return res, true
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// cindyLinkData is the data of a cindy or persona link (rest: what follows the scheme), or why the link is refused:
// anything but provider/import with v and data once each, nothing after them.
func cindyLinkData(rest string) (string, pasteNote) {
	if len(rest) > linkMost {
		return "", pasteNote{Code: noteLinkTooLong}
	}
	path, query, found := strings.Cut(rest, "?")
	if !found || path != "provider/import" || strings.Contains(rest, "#") {
		return "", pasteNote{Code: noteLinkInvalid}
	}
	params, broken := linkQuery(query)
	return linkParams(params, broken, "data", true)
}

// linkParams is a link's data (the parameter named data) when params (linkQuery's, not broken) have it once, v once
// (or, unless needV, at most once: no v reads as 1) and nothing else, and v is 1; else the note why the link is refused
// (link_version names a v that is a number).
func linkParams(params url.Values, broken bool, data string, needV bool) (string, pasteNote) {
	v := params["v"]
	invalid := broken || len(params[data]) != 1 || len(v) > 1 || needV && len(v) == 0
	for k := range params {
		invalid = invalid || k != data && k != "v"
	}
	switch {
	case invalid:
		return "", pasteNote{Code: noteLinkInvalid}
	case len(v) == 1 && v[0] != "1":
		n := pasteNote{Code: noteLinkVersion}
		if linkVersionName.MatchString(v[0]) {
			n.Name = v[0]
		}
		return "", n
	}
	return params[data][0], pasteNote{}
}

// linkQuery reads q as a browser's URLSearchParams does (pairs split at &, a name from its value at the first =, + a
// blank, %-escapes decoded), except that a broken %-escape breaks the link instead of staying as it is.
func linkQuery(q string) (params url.Values, broken bool) {
	params = url.Values{}
	for pair := range strings.SplitSeq(q, "&") {
		if pair == "" {
			continue
		}
		rawName, rawValue, _ := strings.Cut(pair, "=")
		name, errName := url.QueryUnescape(rawName)
		value, errValue := url.QueryUnescape(rawValue)
		if errName != nil {
			name = rawName
		}
		broken = broken || errName != nil || errValue != nil
		params[name] = append(params[name], value)
	}
	return params, broken
}

// linkPayload is a link's data decoded and checked as cindy does (decodeBase64Url): base64url in its one canonical
// form (no padding, no other alphabet, no stray bits) of at most linkJSONMost bytes of UTF-8 holding a JSON object
// that v1 takes (checkLinkPayload), as bytes and as the object; else the note why it is refused.
func linkPayload(data string) ([]byte, map[string]any, pasteNote) {
	invalid := pasteNote{Code: noteLinkInvalid}
	if !linkData.MatchString(data) {
		return nil, nil, invalid
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(data)
	switch {
	case err != nil:
		return nil, nil, invalid
	case len(b) > linkJSONMost:
		return nil, nil, pasteNote{Code: noteLinkTooLong}
	case !utf8.Valid(b):
		return nil, nil, invalid
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil || obj == nil {
		return nil, nil, invalid
	}
	return b, obj, checkLinkPayload(obj)
}

// linkHasKey: a checked payload carries a key, the provider's or an endpoint's.
func linkHasKey(obj map[string]any) bool {
	auth, _ := obj["auth"].(map[string]any)
	eps, _ := obj["endpoints"].([]any)
	_, shared := auth["apiKey"]
	return shared || slices.ContainsFunc(eps, func(e any) bool {
		ep, _ := e.(map[string]any)
		_, has := ep["apiKey"]
		return has
	})
}

// ---- the v1 payload (cindy's parsePayload) ----

// The checks below answer the first failure, the zero note when there is none: a field v1 does not define for it
// (fields in key order: a JSON object's keys come in no order), then cindy's checks in cindy's order. A path is the
// field's: name, auth.apiKey, endpoints[0].models[1].id.

func linkInvalid(path string) pasteNote { return pasteNote{Code: noteLinkInvalid, Name: path} }

func linkPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// checkLinkUnknown is the note on obj's first field (in key order) not in fields, at path. It names the field only
// when its key is a plain word that does not start like a service's key (secretValue): anything else may be a value,
// even a key, typed into the wrong place.
func checkLinkUnknown(obj map[string]any, path string, fields []string) pasteNote {
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if !slices.Contains(fields, k) {
			n := pasteNote{Code: noteLinkField}
			if linkFieldName.MatchString(k) && !secretValue(k) {
				n.Name = linkPath(path, k)
			}
			return n
		}
	}
	return pasteNote{}
}

// checkLinkPayload checks a link's JSON object: the fields its kind has (a payload of no known kind: the fields any
// kind has, so that a field nothing defines is what its note names), then the kind, then the kind's own rules.
func checkLinkPayload(obj map[string]any) pasteNote {
	kind, _ := obj["kind"].(string)
	fields, known := linkKindFields[kind]
	if !known {
		fields = linkAnyFields
	}
	if n := checkLinkUnknown(obj, "", fields); n.Code != "" {
		return n
	}
	switch kind {
	case "builtin":
		if p, _ := obj["provider"].(string); p != "gemini" && p != "openai-images" {
			return linkInvalid("provider")
		}
		if !linkKey(obj["apiKey"], linkBuiltinKeyMost) {
			return linkInvalid("apiKey")
		}
	case "preset":
		if _, ok := linkText(obj["preset"], 128); !ok {
			return linkInvalid("preset")
		}
		if !linkKey(obj["apiKey"], linkKeyMost) {
			return linkInvalid("apiKey")
		}
	case "custom":
		return checkLinkCustom(obj)
	default:
		return linkInvalid("kind") // unlike a pasted provider, where no kind reads as custom
	}
	return pasteNote{}
}

// checkLinkCustom checks a custom provider: its name, id (Persona does not use it), auth and endpoints; keyless sign-in
// only to this computer, credentials on an endpoint only with an API key, and no agent left between two endpoints.
func checkLinkCustom(obj map[string]any) pasteNote {
	if _, ok := linkText(obj["name"], 60); !ok {
		return linkInvalid("name")
	}
	if id, has := obj["id"]; has {
		if s, ok := linkText(id, 40); !ok || !linkID.MatchString(s) {
			return linkInvalid("id")
		}
	}
	method, n := checkLinkAuth(obj)
	if n.Code != "" {
		return n
	}
	list, _ := obj["endpoints"].([]any)
	if len(list) == 0 || len(list) > linkEndpointsMost {
		return linkInvalid("endpoints")
	}
	eps := make([]linkEndpoint, len(list))
	for i, v := range list {
		if eps[i], n = checkLinkEndpoint(v, fmt.Sprintf("endpoints[%d]", i)); n.Code != "" {
			return n
		}
	}
	// with no key, a link could send the prompts to anyone (a modelsUrl is on its baseUrl's host: checkLinkEndpoint)
	if method == "none" && slices.ContainsFunc(eps, func(e linkEndpoint) bool { return !linkLoopback(e.baseURL) }) {
		return linkInvalid("auth")
	}
	for i, e := range eps {
		switch {
		case method != "apiKey" && e.key:
			return linkInvalid(fmt.Sprintf("endpoints[%d].apiKey", i))
		case method != "apiKey" && e.headers:
			return linkInvalid(fmt.Sprintf("endpoints[%d].headers", i))
		}
	}
	for i, e := range eps {
		if method == "oauth" && slices.Contains(e.targets, "pi") {
			return linkInvalid(fmt.Sprintf("endpoints[%d].targets", i)) // pi can't sign in that way
		}
	}
	return checkLinkCompeting(eps, method)
}

// checkLinkAuth checks a custom provider's auth (none: an API key, maybe in the endpoints) and answers its method.
// OAuth is checked no further than its fields: Persona does not sign in that way (parseCindy's cindy_auth).
func checkLinkAuth(obj map[string]any) (string, pasteNote) {
	v, has := obj["auth"]
	if !has {
		return "apiKey", pasteNote{}
	}
	auth, ok := v.(map[string]any)
	if !ok {
		return "", linkInvalid("auth")
	}
	method, _ := auth["method"].(string)
	var fields []string
	switch method {
	case "apiKey":
		fields = []string{"method", "apiKey"}
	case "none":
		fields = []string{"method"}
	case "oauth":
		switch auth["flow"] { // a null flow is an absent one, as cindy's ?? reads it
		case nil, "authorization-code":
			fields = linkOAuthCodeFields
		case "device-code":
			fields = linkOAuthDeviceFields
		default:
			return "", linkInvalid("auth.flow")
		}
	default:
		return "", linkInvalid("auth.method")
	}
	if n := checkLinkUnknown(auth, "auth", fields); n.Code != "" {
		return "", n
	}
	if key, has := auth["apiKey"]; has && !linkKey(key, linkKeyMost) {
		return "", linkInvalid("auth.apiKey")
	}
	return method, pasteNote{}
}

// linkEndpoint is what the provider's checks need of a checked endpoint: its protocol and targets (nil: its
// protocol's agents), address, and whether it carries a key or headers.
type linkEndpoint struct {
	protocol     string
	targets      []string
	baseURL      *url.URL
	key, headers bool
}

// checkLinkEndpoint checks one endpoint (path: endpoints[i]) in cindy's order.
func checkLinkEndpoint(v any, path string) (linkEndpoint, pasteNote) {
	var e linkEndpoint
	obj, ok := v.(map[string]any)
	if !ok {
		return e, linkInvalid(path)
	}
	if n := checkLinkUnknown(obj, path, linkEndpointFields); n.Code != "" {
		return e, n
	}
	if e.protocol, _ = obj["protocol"].(string); !slices.Contains(cindyProtocols, e.protocol) {
		return e, linkInvalid(path + ".protocol")
	}
	if t, has := obj["targets"]; has {
		if e.targets, ok = linkDistinct(t, linkAgents); !ok || len(e.targets) == 0 {
			return e, linkInvalid(path + ".targets")
		}
	}
	if n := checkLinkModels(obj["models"], path+".models"); n.Code != "" {
		return e, n
	}
	if p, has := obj["requestPath"]; has && !linkRequestPath(p) {
		return e, linkInvalid(path + ".requestPath")
	}
	if key, has := obj["apiKey"]; has && !linkKey(key, linkKeyMost) {
		return e, linkInvalid(path + ".apiKey")
	}
	if e.baseURL = linkURL(obj["baseUrl"]); e.baseURL == nil {
		return e, linkInvalid(path + ".baseUrl")
	}
	if m, has := obj["modelsUrl"]; has {
		if u := linkURL(m); u == nil || linkOrigin(u) != linkOrigin(e.baseURL) {
			return e, linkInvalid(path + ".modelsUrl")
		}
	}
	if h, has := obj["headers"]; has && !linkHeaders(h) {
		return e, linkInvalid(path + ".headers")
	}
	_, e.key = obj["apiKey"]
	_, e.headers = obj["headers"]
	return e, pasteNote{}
}

// linkDistinct is v as a list of strings from known, none twice (an endpoint's targets, a model's thinking levels).
func linkDistinct(v any, known []string) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	var out []string
	for _, e := range list {
		s, _ := e.(string)
		if !slices.Contains(known, s) || slices.Contains(out, s) {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// checkLinkModels checks an endpoint's models (absent or null: none): at most linkModelsMost, no id twice.
func checkLinkModels(v any, path string) pasteNote {
	if v == nil {
		return pasteNote{}
	}
	list, ok := v.([]any)
	if !ok || len(list) > linkModelsMost {
		return linkInvalid(path)
	}
	ids := map[string]bool{}
	for i, m := range list {
		id, n := checkLinkModel(m, fmt.Sprintf("%s[%d]", path, i))
		if n.Code != "" {
			return n
		}
		ids[id] = true
	}
	if len(ids) != len(list) {
		return linkInvalid(path)
	}
	return pasteNote{}
}

// checkLinkModel checks one model, its id alone or an object, and answers its id. Of the object's other fields, cindy
// checks these; the rest of its model metadata Persona does not read.
func checkLinkModel(v any, path string) (string, pasteNote) {
	if _, ok := v.(string); ok {
		id, ok := linkText(v, 256)
		if !ok {
			return "", linkInvalid(path)
		}
		return id, pasteNote{}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", linkInvalid(path)
	}
	if n := checkLinkUnknown(m, path, linkModelFields); n.Code != "" {
		return "", n
	}
	id, ok := linkText(m["id"], 256)
	if !ok {
		return "", linkInvalid(path + ".id")
	}
	if name, has := m["name"]; has {
		if _, ok := linkText(name, 256); !ok {
			return "", linkInvalid(path + ".name")
		}
	}
	if w, has := m["contextWindow"]; has {
		if f, ok := w.(float64); !ok || f <= 0 || f != math.Trunc(f) {
			return "", linkInvalid(path + ".contextWindow")
		}
	}
	for _, f := range []string{"defaultEnabled", "supportsImageInput", "reasoning"} {
		if b, has := m[f]; has {
			if _, ok := b.(bool); !ok {
				return "", linkInvalid(path + "." + f)
			}
		}
	}
	efforts, hasEfforts := m["reasoningEfforts"]
	def, hasDefault := m["reasoningDefaultEffort"]
	if m["reasoning"] != true { // thinking levels belong to a model that thinks
		switch {
		case hasEfforts:
			return "", linkInvalid(path + ".reasoningEfforts")
		case hasDefault:
			return "", linkInvalid(path + ".reasoningDefaultEffort")
		}
		return id, pasteNote{}
	}
	var levels []string
	if hasEfforts {
		if levels, ok = linkDistinct(efforts, linkEfforts); !ok {
			return "", linkInvalid(path + ".reasoningEfforts")
		}
	}
	if s, _ := def.(string); hasDefault && !slices.Contains(levels, s) {
		return "", linkInvalid(path + ".reasoningDefaultEffort")
	}
	return id, pasteNote{}
}

// linkRequestPath: an endpoint's own request path, one /, no second one (that would name another host), no query,
// fragment or blank.
func linkRequestPath(v any) bool {
	p, ok := linkText(v, 256)
	return ok && strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") &&
		!strings.ContainsFunc(p, func(r rune) bool { return r == '?' || r == '#' || jsSpace(r) })
}

// linkURL is v as an address of an endpoint (cindy's httpUrl): an absolute http(s) URL with a host and no user,
// query or fragment, at most linkTextMost long; nil when it is not one. A ? or # alone counts too: a saved address has
// none, and a gateway may take a key there.
func linkURL(v any) *url.URL {
	raw, ok := linkText(v, linkTextMost)
	if !ok || strings.ContainsAny(raw, "?#") || urlWithUser(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Hostname() == "" {
		return nil
	}
	if port := u.Port(); port != "" { // Go takes any digits, a browser's URL only a port number
		if p, err := strconv.Atoi(port); err != nil || p > 65535 {
			return nil
		}
	}
	return u
}

// linkOrigin is u's origin as a browser compares it: scheme, host and port, the scheme's default port written or not.
func linkOrigin(u *url.URL) string {
	port := u.Port()
	if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		port = ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// linkLoopback: u is on this computer (cindy's isLoopbackProviderUrl): localhost, ::1 or 127.0.0.0/8.
func linkLoopback(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	ip, err := netip.ParseAddr(host)
	return host == "localhost" || err == nil && (ip == netip.IPv6Loopback() || ip.Is4() && ip.IsLoopback())
}

// linkHeaders: an endpoint's headers, 1 to linkHeadersMost, each named with an HTTP token of at most 128 characters
// and valued with a string of at most linkTextMost that a header line holds as it is.
func linkHeaders(v any) bool {
	hs, ok := v.(map[string]any)
	if !ok || len(hs) == 0 || len(hs) > linkHeadersMost {
		return false
	}
	for name, value := range hs {
		s, ok := value.(string)
		if !ok || jsLen(name) > 128 || !httpguts.ValidHeaderFieldName(name) || jsLen(s) > linkTextMost ||
			!httpguts.ValidHeaderFieldValue(s) {
			return false
		}
	}
	return true
}

// checkLinkCompeting refuses a provider whose two endpoints serve one agent equally well: which one it would use is
// left unsaid, and cindy asks the link to name targets instead. An endpoint serves its targets, else the agents its
// protocol suits (Messages and Google every agent, Chat and Responses codex and pi; with OAuth, pi none).
func checkLinkCompeting(eps []linkEndpoint, method string) pasteNote {
	for _, agent := range linkAgents {
		best, ties := -1, 0
		for _, e := range eps {
			if !slices.Contains(linkServes(e, method), agent) {
				continue
			}
			switch p := linkPriority[agent][e.protocol]; {
			case p > best:
				best, ties = p, 1
			case p == best:
				ties++
			}
		}
		if ties > 1 {
			return linkInvalid("endpoints")
		}
	}
	return pasteNote{}
}

// linkServes is the agents endpoint e serves.
func linkServes(e linkEndpoint, method string) []string {
	if e.targets != nil {
		return e.targets
	}
	agents := []string{"codex", "pi"}
	if e.protocol == cindyMessages || e.protocol == cindyGoogle {
		agents = []string{"claude-code", "codex", "pi"}
	}
	if method == "oauth" {
		agents = agents[:len(agents)-1]
	}
	return agents
}

// ---- cindy's text rules ----

// linkText is v as cindy's boundedString takes it, trimmed: a string not blank, at most most characters long as
// JavaScript counts them (UTF-16 units), before trimming.
func linkText(v any, most int) (string, bool) {
	s, ok := v.(string)
	t := strings.TrimFunc(s, jsSpace)
	return t, ok && t != "" && jsLen(s) <= most
}

// linkKey: v is a key cindy takes (parseApiKey): linkText that a header line holds as it is (no control character but
// a tab).
func linkKey(v any, most int) bool {
	s, ok := linkText(v, most)
	return ok && httpguts.ValidHeaderFieldValue(s)
}

// jsSpace is a blank as JavaScript's trim and \s know it: Go's unicode.IsSpace but U+0085, and U+FEFF.
func jsSpace(r rune) bool { return r == '\uFEFF' || r != '\u0085' && unicode.IsSpace(r) }

// jsLen is s's length as JavaScript counts it, in UTF-16 units.
func jsLen(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
