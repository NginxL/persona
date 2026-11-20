package assistant

import (
	"cmp"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// The formats a pasted configuration is read in (models_import_parse.go): pure functions of the text, none of which
// touches the store or a node, so an import link (models_import_link.go) runs its decoded JSON through parseCindy.

// ---- Claude Code launches: shell lines and settings.json ----

// launchGateway is a Claude Code launch's gateway address (its env's ANTHROPIC_BASE_URL) as a draft stores it, or
// the note code why it makes no draft (gatewayRefused).
func launchGateway(env map[string]string) (base, code string) {
	raw := strings.TrimSpace(env["ANTHROPIC_BASE_URL"])
	if raw == "" {
		return "", noteNoGateway
	}
	if code := gatewayRefused(raw); code != "" {
		return "", code
	}
	return proto.MessagesBaseURL(raw), ""
}

// gatewayRefused is why a pasted gateway address makes no draft, "" when it does: a shell expansion (Persona stores
// text), a user or password in it, or a query or fragment (where some gateways take a key; a saved address has none).
func gatewayRefused(raw string) string {
	switch {
	case shellExpands(raw):
		return noteURLShell
	case urlWithUser(raw):
		return noteURLUser
	case strings.ContainsAny(raw, "?#"):
		return noteURLQuery
	}
	return ""
}

// groupDraft is an alias group (or one launch, newImportGroup) as a draft: the fields an import would save, the key in
// the clear, and of the rest only what holds no secret (the draft goes back to the browser, not into sealed storage).
// On top of the import's rules (importEnv) it leaves out the variables named or valued like secrets (secretEnv), a
// proxy with a query or fragment, arguments that may hold a secret (setArgs) and the headers setHeaders leaves out.
// env is the launch's (its first alias's for a group: the group takes its settings). Name is left to the caller.
func groupDraft(g importGroup, env map[string]string) pasteDraft {
	l := g.ManagedLauncher
	d := pasteDraft{Provider: pastedProvider{
		Kind: "launcher", Agent: "claude", API: "messages", BaseURL: l.BaseURL, Auth: l.Auth, Model: l.Model, Models: l.Models,
		OpusModel: l.OpusModel, SonnetModel: l.SonnetModel, HaikuModel: l.HaikuModel, FableModel: l.FableModel,
		SmallModel: l.SmallModel, SubagentModel: l.SubagentModel, SubagentForce: l.SubagentForce,
		CustomModel: l.CustomModel, CustomModelName: l.CustomModelName, CustomModelDescription: l.CustomModelDescription,
		Effort: l.Effort, CompactWindow: l.CompactWindow, MaxContext: l.MaxContext, TimeoutMS: l.TimeoutMS,
		NoProxy: l.NoProxy,
	}}
	skipped := slices.Clone(g.SkippedEnv)
	var lines []string
	for i, line := range strings.Split(g.env, "\n")[:len(g.EnvNames)] { // importEnv's lines, one a name
		if name := g.EnvNames[i]; secretEnv(name, env[name]) {
			skipped = append(skipped, skippedEnv{Name: name, Reason: envSkipCredential})
		} else {
			lines, d.EnvNames = append(lines, line), append(d.EnvNames, name)
		}
	}
	d.Provider.Env = strings.Join(lines, "\n")
	if strings.ContainsAny(l.Proxy, "?#") { // it may carry a token: the variable it came from is named
		i := slices.IndexFunc(importProxyEnv, func(k string) bool { return strings.TrimSpace(env[k]) != "" })
		skipped = append(skipped, skippedEnv{Name: importProxyEnv[i], Reason: envSkipCredential})
	} else {
		d.Provider.Proxy = l.Proxy
	}
	slices.SortStableFunc(skipped, func(a, b skippedEnv) int { return strings.Compare(a.Name, b.Name) })
	d.SkippedEnv = skipped[:min(len(skipped), pasteSkippedMost)]
	d.setArgs(l.Args)
	d.setHeaders(parseHeaders(g.headers), false)
	d.setKey(g.key)
	return d
}

// envDraft is the draft of a claude command line's env and args, or of env alone (export lines, a .env, settings.json's
// env), or the note code why there is none (launchGateway's).
func envDraft(env map[string]string, args []string) (pasteDraft, string) {
	base, code := launchGateway(env)
	if code != "" {
		return pasteDraft{}, code
	}
	return groupDraft(newImportGroup("", zshrcAlias{Env: env, Args: args}, base), env), ""
}

// pasteSecretWords: a header's or a variable's name holding one of these (any case, separators aside) may name a
// secret. A false positive only names one more thing left out.
var pasteSecretWords = []string{"KEY", "TOKEN", "AUTH", "SECRET", "PASS", "COOKIE", "SESSION", "BEARER", "CREDENTIAL",
	"JWT", "SIGNATURE"}

// holdsSecretWord: s, upper-cased and without - _ . and blanks (XApiKey, X-Access-Token, x.api.key), holds one of
// pasteSecretWords.
func holdsSecretWord(s string) bool {
	s = strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s))
	return slices.ContainsFunc(pasteSecretWords, func(w string) bool { return strings.Contains(s, w) })
}

// nameWords is a name's words, upper-cased: split at - _ . and blanks and where camelCase starts a word (XApiKey:
// X, API, KEY; bypassPermissions: BYPASS, PERMISSIONS).
func nameWords(s string) []string {
	var words []string
	var cur []rune
	rs := []rune(s)
	for i, r := range rs {
		sep := r == '-' || r == '_' || r == '.' || unicode.IsSpace(r)
		// An upper-case letter after a lower-case one or a digit, or one that starts a word after an acronym (the A of XApi).
		starts := i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) ||
			unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1]))
		if (sep || starts) && len(cur) > 0 {
			words, cur = append(words, strings.ToUpper(string(cur))), nil
		}
		if !sep {
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		words = append(words, strings.ToUpper(string(cur)))
	}
	return words
}

// secretHeaderWords: a header name with one of these as a word (nameWords) names a secret too, beyond
// pasteSecretWords (X-Api, X-Access, X-Sig, X-Otp…).
var secretHeaderWords = []string{"PWD", "HMAC", "SIG", "SIGNATURE", "OTP", "SID", "ACCESS", "API"}

// secretHeader: a header that is a credential, by its name (pasteSecretWords, secretHeaderWords) or its value
// (secretValue).
func secretHeader(name, value string) bool {
	return holdsSecretWord(name) || slices.ContainsFunc(nameWords(name), func(w string) bool { return slices.Contains(secretHeaderWords, w) }) ||
		secretValue(value)
}

// secretValuePrefixes start the keys of common services (OpenAI-style sk-, GitHub, Slack, AWS, Google, GitLab).
var secretValuePrefixes = []string{"sk-", "ghp_", "gho_", "ghs_", "github_pat_", "xox", "AKIA", "AIza", "glpat-"}

// secretValue: v looks like a secret whatever it is named: it holds a Bearer credential (any case) or starts like a
// common service's key.
func secretValue(v string) bool {
	v = strings.TrimSpace(v)
	return strings.Contains(strings.ToLower(v), "bearer ") ||
		slices.ContainsFunc(secretValuePrefixes, func(p string) bool { return strings.HasPrefix(v, p) })
}

// secretEnv: a variable a paste leaves out as a credential on top of importEnv's rules: its name holds one of
// pasteSecretWords, a word TOKENS aside (CLAUDE_CODE_MAX_OUTPUT_TOKENS is a number), has a word PAT or PWD
// (GITHUB_PAT, OPENAIKEY, ACCESSTOKEN, RELAY_AUTH, SESSIONID) or ends in _HEADERS (OTEL_EXPORTER_OTLP_HEADERS); or
// its value looks like a secret (secretValue).
func secretEnv(name, value string) bool {
	words := strings.Split(strings.ToUpper(name), "_")
	if slices.Contains(words, "PAT") || slices.Contains(words, "PWD") || strings.HasSuffix(strings.ToUpper(name), "_HEADERS") ||
		secretValue(value) {
		return true
	}
	return holdsSecretWord(strings.Join(slices.DeleteFunc(words, func(w string) bool { return w == "TOKENS" }), ""))
}

// secretOptionWords: an option named with one of these as a word (nameWords) takes a secret: --api-key, --token.
var secretOptionWords = []string{"KEY", "APIKEY", "TOKEN", "AUTH", "SECRET", "PASSWORD", "PASS", "PASSWD", "PWD", "COOKIE",
	"BEARER", "CREDENTIAL", "CREDENTIALS", "JWT"}

// setArgs keeps the launch's arguments unless one may hold a secret or a setting's text, then none (args_left_out):
// an option named for a secret (secretOptionWords: its value may be the secret), or a value (an argument, or an
// option's after its =) with a { or an =, or that looks like a secret (secretValue). --permission-mode
// bypassPermissions, --fork-session and --allowedTools "Bash(git:*)" stay.
func (d *pasteDraft) setArgs(args []string) {
	risky := func(a string) bool {
		value := a
		if name, ok := strings.CutPrefix(a, "-"); ok {
			name, value, _ = strings.Cut(name, "=")
			if slices.ContainsFunc(nameWords(name), func(w string) bool { return slices.Contains(secretOptionWords, w) }) {
				return true
			}
		}
		return strings.ContainsAny(value, "{=") || secretValue(value)
	}
	if slices.ContainsFunc(args, risky) {
		addNote(&d.Notes, pasteNote{Code: noteArgsLeftOut})
		return
	}
	d.Provider.Args = args
}

// setsClaude: env has a variable of Claude Code's (ANTHROPIC_…, CLAUDE_…).
func setsClaude(env map[string]string) bool {
	for k := range env {
		if strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "CLAUDE_") {
			return true
		}
	}
	return false
}

// shellLaunch is one claude command of pasted shell lines: an alias (named by alias) or a command line, defined on
// line, with the variables it sets itself.
type shellLaunch struct {
	alias    string
	line     int
	env      map[string]string
	args     []string
	resetEnv bool
}

// withEnv is env with own over it.
func withEnv(env, own map[string]string) map[string]string {
	out := maps.Clone(env)
	maps.Copy(out, own)
	return out
}

// parseShell reads shell lines (an rc file's, a .env, a command): export and NAME=value lines collect an env E in
// order; each claude alias (with E as it is at the end, as when the alias runs) and each claude command line (with E
// as it is there) is a draft, the variables it sets itself over E's. Aliases group as an import groups them
// (mergeZshrcAliases), but a group whose aliases differ in more than the model is one draft per alias. Without any
// claude command, E alone is the draft. Lines it does not use are noted (in text order), comments and blank lines
// aside.
func parseShell(lines []shellcmd.Line) pasteResult {
	res := pasteResult{Format: "shell"}
	type placed struct {
		at   int
		note pasteNote
	}
	var told []placed
	tell := func(at int, code string, line int, name string) {
		told = append(told, placed{at, pasteNote{Code: code, Line: line, Name: name}})
	}
	env, baseAt := map[string]string{}, 0 // baseAt: the line setting ANTHROPIC_BASE_URL
	var aliases, commands []shellLaunch
	// Past pasteLaunchMost claude launches or pasteEnvMost variables the rest is not read (too_many): each launch's
	// draft reads all of E, so the two bound the work. Aliases on one gateway still merge, exports below them still count.
	recognized, cut := false, false
	launched := func() bool { cut = len(aliases)+len(commands) == pasteLaunchMost; return cut }
read:
	for _, l := range lines {
		if t := strings.TrimSpace(l.Text); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		segs, ok := shellcmd.Split(l.Text)
		if !ok {
			tell(l.Start, noteLineUnused, l.Start, "")
			continue
		}
		for _, seg := range segs {
			words := seg.Words
			switch {
			case len(words) == 0:
			case words[0] == "alias":
				defs := shellcmd.AliasDefs(words)
				if len(defs) == 0 {
					tell(l.Start, noteLineUnused, l.Start, "")
				}
				for _, d := range defs {
					recognized = true
					// Defined again: the last definition counts, where it is (as the shell keeps it), claude or not.
					aliases = slices.DeleteFunc(aliases, func(o shellLaunch) bool { return o.alias == d.Name })
					a, ok := shellcmd.ClaudeCommand(d.Value)
					if !ok {
						tell(l.Start, noteNotClaude, 0, d.Name)
						continue
					}
					if launched() {
						break read
					}
					aliases = append(aliases, shellLaunch{alias: d.Name, line: l.Start, env: a.Env, args: a.Args, resetEnv: a.ResetEnv})
				}
			case words[0] == "export" || !slices.ContainsFunc(words, func(w string) bool { return !shellcmd.AssignRe.MatchString(w) }):
				for _, w := range words {
					if k, v, ok := strings.Cut(w, "="); ok && shellcmd.AssignRe.MatchString(w) {
						if _, set := env[k]; !set && len(env) == pasteEnvMost {
							cut = true
							break read
						}
						env[k], recognized = v, true
						if k == "ANTHROPIC_BASE_URL" {
							baseAt = l.Start
						}
					}
				}
			default:
				own := map[string]string{}
				i, reset := shellcmd.CommandEnvironment(words, own)
				if i < 0 || filepath.Base(words[i]) != "claude" {
					tell(l.Start, noteNotClaude, l.Start, "")
					continue
				}
				recognized = true
				if launched() {
					break read
				}
				if !reset {
					own = withEnv(env, own)
				}
				commands = append(commands, shellLaunch{line: l.Start, env: own, args: slices.Clone(words[i+1:])})
			}
		}
	}
	if !recognized {
		return pasteResult{} // nothing of a configuration: unrecognized, the lines unnamed
	}
	var group []zshrcAlias
	envOf := map[string]map[string]string{}
	for _, a := range aliases {
		e := a.env
		if !a.resetEnv {
			e = withEnv(env, e)
		}
		if _, code := launchGateway(e); code != "" {
			tell(a.line, code, 0, a.alias)
			continue
		}
		group, envOf[a.alias] = append(group, zshrcAlias{Alias: a.alias, Env: e, Args: a.args, Command: "claude"}), e
	}
	for _, g := range mergeZshrcAliases("", group, nil) {
		each := []importGroup{g}
		if len(g.Differs) > 0 {
			each = g.variants
		}
		for _, v := range each {
			d := groupDraft(v, envOf[v.Aliases[0]])
			d.Label, d.Aliases, d.Provider.Name = strings.Join(v.Aliases, ", "), v.Aliases, v.Name
			res.Drafts = append(res.Drafts, d)
		}
	}
	for _, c := range commands {
		if d, code := envDraft(c.env, c.args); code != "" {
			tell(c.line, code, c.line, "")
		} else {
			res.Drafts = append(res.Drafts, d)
		}
	}
	if len(aliases) == 0 && len(commands) == 0 {
		switch d, code := envDraft(env, nil); {
		case code == "":
			res.Drafts = append(res.Drafts, d)
		case code != noteNoGateway:
			tell(baseAt, code, baseAt, "")
		case setsClaude(env): // a .env of something else is no Claude Code at all
			tell(len(lines)+1, noteNoGateway, 0, "")
		}
	}
	slices.SortStableFunc(told, func(a, b placed) int { return a.at - b.at })
	seen := map[pasteNote]bool{} // a line can say the same twice (two commands on it)
	for _, t := range told {
		if !seen[t.note] {
			seen[t.note] = true
			res.Notes = append(res.Notes, t.note)
		}
	}
	if cut {
		res.Notes = append(res.Notes, pasteNote{Code: noteTooMany})
	}
	return res
}

// parseSettings reads a Claude Code settings.json: its env (string values; others are not variables Claude Code
// sets) and its model as ANTHROPIC_MODEL when env has none, as the draft of that env alone. Without a key in env, an
// apiKeyHelper says why there is none: it runs a command for the key.
func parseSettings(obj map[string]any) pasteResult {
	env := map[string]string{}
	if m, ok := obj["env"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				env[k] = s
			}
		}
	}
	if m, ok := obj["model"].(string); ok && strings.TrimSpace(env["ANTHROPIC_MODEL"]) == "" {
		env["ANTHROPIC_MODEL"] = m
	}
	res := pasteResult{Format: "settings"}
	d, code := envDraft(env, nil)
	if code != "" {
		res.note(code, 0, "")
		return res
	}
	if helper, _ := obj["apiKeyHelper"].(string); strings.TrimSpace(helper) != "" {
		if i := slices.Index(d.Notes, pasteNote{Code: noteNoKey}); i >= 0 {
			d.Notes[i] = pasteNote{Code: noteKeyHelper}
		}
	}
	res.Drafts = append(res.Drafts, d)
	return res
}

// ---- headers ----

// protocolHeaders are the request basics a curl command or a cindy endpoint sends that Claude Code and the manager
// set themselves.
var protocolHeaders = []string{"anthropic-version", "anthropic-beta", "content-type", "accept", "accept-encoding", "user-agent"}

// headerToken is a header name a paste names or keeps: a plain token. Anything else (blanks, dots, a $'…' from a
// shell) may be a value run into the name.
var headerToken = lazyre.New(`^[A-Za-z0-9_-]{1,64}$`)

// unsafeHeaderValue: v holds what one "Name: value" line cannot keep as it is (a control character: it would start
// another header) or what a shell or Claude Code would read as something else (a backslash, a $).
func unsafeHeaderValue(v string) bool {
	return strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) || r == '\\' || r == '$' })
}

// setHeaders sorts a paste's headers (name, value) into the draft's ("Name: value" lines) and the ones left out with
// why: request basics (when protocol: a curl command's, a cindy endpoint's), credentials by name or value
// (secretHeader), and values a line would not keep (unsafe). A name that is no plain token is neither kept nor named:
// header_unreadable, once. At most pasteSkippedMost are named.
func (d *pasteDraft) setHeaders(hs [][2]string, protocol bool) {
	var lines []string
	for _, h := range hs {
		why := ""
		switch {
		case !headerToken.MatchString(h[0]):
			addNote(&d.Notes, pasteNote{Code: noteHeaderUnread})
			continue
		case protocol && slices.Contains(protocolHeaders, strings.ToLower(h[0])):
			why = headerSkipProtocol
		case secretHeader(h[0], h[1]):
			why = headerSkipCredential
		case unsafeHeaderValue(h[1]):
			why = headerSkipUnsafe
		}
		if why == "" {
			lines = append(lines, h[0]+": "+h[1])
		} else if len(d.SkippedHeaders) < pasteSkippedMost {
			d.SkippedHeaders = append(d.SkippedHeaders, skippedHeader{Name: h[0], Reason: why})
		}
	}
	d.Provider.Headers = strings.Join(lines, "\n")
}

// ---- curl ----

// curlLine is the first command line of lines (comments and blank lines aside) when it runs curl, after any
// variables set for it: its first line and the words after curl.
func curlLine(lines []shellcmd.Line) (start int, args []string, ok bool) {
	for _, l := range lines {
		if t := strings.TrimSpace(l.Text); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		segs, ok := shellcmd.Split(l.Text)
		if !ok || len(segs) == 0 {
			return 0, nil, false
		}
		words := segs[0].Words
		i := shellcmd.CommandIndex(words, map[string]string{})
		if i < 0 || filepath.Base(words[i]) != "curl" {
			return 0, nil, false
		}
		return l.Start, words[i+1:], true
	}
	return 0, nil, false
}

// curlQuiet are the curl options (short ones also run together, -sSL) that say nothing about the provider: left out
// without a note. -X / --request take a value, also left out.
var curlQuiet = lazyre.New(`^(-[sSivNL]+|--silent|--show-error|--no-buffer|--compressed|--location)$`)

// curlValued are the curl options that take a value (the ones read among them), so a value is not read as the URL.
var curlValued = []string{"-u", "--user", "-A", "--user-agent", "-e", "--referer", "-b", "--cookie", "-c", "--cookie-jar",
	"-o", "--output", "-m", "--max-time", "--connect-timeout", "-w", "--write-out", "-F", "--form", "--form-string",
	"--data-urlencode", "--data-ascii", "-T", "--upload-file", "--cacert", "--capath", "-E", "--cert", "--cert-type",
	"--key", "--key-type", "--pass", "--resolve", "--connect-to", "--retry", "--retry-delay", "--retry-max-time", "-r",
	"--range", "-K", "--config", "--oauth2-bearer", "-U", "--proxy-user", "--proxy-header", "--limit-rate",
	"--max-filesize", "--interface", "--unix-socket", "-Y", "--speed-limit", "-y", "--speed-time", "-z", "--time-cond",
	"--aws-sigv4", "-H", "--header", "-d", "--data", "--data-raw", "--data-binary", "--json", "-x", "--proxy", "--noproxy",
	"-X", "--request", "--url"}

// curlRequest is what a curl command says: its URL, headers (as written), body, proxy and bypass list (with the
// option each was given by), and the options it does not use.
type curlRequest struct {
	url, body, proxy, proxyOpt, noProxy, noProxyOpt string
	headers                                         []string
	unused                                          []string
}

// readCurl reads curl's arguments. The URL is --url's, else the first argument that is no option and has a scheme,
// else the first one that is no option.
func readCurl(args []string) curlRequest {
	var c curlRequest
	var plain []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		opt, value, attached := a, "", false
		if len(a) > 2 && a[0] == '-' && a[1] != '-' && slices.Contains(curlValued, a[:2]) {
			opt, value, attached = a[:2], a[2:], true // -Hname: value
		}
		if !attached && slices.Contains(curlValued, opt) && i+1 < len(args) {
			i++
			value = args[i]
		}
		switch {
		case opt == "--url":
			c.url = value
		case opt == "-H" || opt == "--header":
			c.headers = append(c.headers, value)
		case opt == "-d" || opt == "--data" || opt == "--data-raw" || opt == "--data-binary" || opt == "--json":
			c.body = value
		case opt == "-x" || opt == "--proxy":
			c.proxy, c.proxyOpt = value, opt
		case opt == "--noproxy":
			c.noProxy, c.noProxyOpt = value, opt
		case opt == "-X" || opt == "--request" || curlQuiet.MatchString(a):
		case strings.HasPrefix(a, "--"):
			name, _, _ := strings.Cut(opt, "=") // --opt=value is no curl syntax, and the value may be a secret
			c.unused = append(c.unused, name)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			c.unused = append(c.unused, a[:2])
		default:
			plain = append(plain, a)
		}
	}
	if c.url == "" {
		if i := slices.IndexFunc(plain, func(w string) bool { return strings.Contains(w, "://") }); i >= 0 {
			c.url = plain[i]
		} else if len(plain) > 0 {
			c.url = plain[0]
		}
	}
	return c
}

// curlEndpoint is an endpoint path a curl URL may end in, and the API that says: the base URL is what is before the
// path (an OpenAI-compatible base keeps its /v1, /v1beta/openai or none at all).
type curlEndpoint struct{ path, api string }

var curlEndpoints = []curlEndpoint{
	{"/messages", "messages"},
	{"/chat/completions", "chat"},
	{"/responses", "responses"},
	{"/v1/models", "messages"},
}

// endpointProvider is the provider calling api at base: Messages a Claude Code gateway (base as MessagesBaseURL makes
// it), Responses or Chat Completions a direct API (base without a trailing /).
func endpointProvider(api, base string) pastedProvider {
	if api == "messages" {
		return pastedProvider{Kind: "launcher", Agent: "claude", API: api, BaseURL: proto.MessagesBaseURL(base)}
	}
	return pastedProvider{Kind: "api", API: api, BaseURL: strings.TrimRight(base, "/")}
}

// parseCurl reads a pasted curl command (args, the words after curl on the command line at line start; the other
// lines are noted): the endpoint its URL calls says the provider (Messages: a Claude Code gateway; Chat Completions or
// Responses: a direct API), x-api-key or Authorization: Bearer the key, the JSON body's model the model.
func parseCurl(lines []shellcmd.Line, start int, args []string) pasteResult {
	res := pasteResult{Format: "curl"}
	switch d, code := curlDraft(readCurl(args)); code {
	case "":
		res.Drafts = append(res.Drafts, d)
	case noteUnrecognized:
		return pasteResult{} // no URL that reads: nothing to draft
	default:
		res.note(code, start, "")
	}
	for _, l := range lines {
		if t := strings.TrimSpace(l.Text); l.Start > start && t != "" && !strings.HasPrefix(t, "#") {
			res.Notes = append(res.Notes, pasteNote{Code: noteLineUnused, Line: l.Start}) // one per line: none twice
		}
	}
	return res
}

// curlDraft is a curl request as a draft, or the note code why there is none (unrecognized: no URL that reads).
func curlDraft(c curlRequest) (pasteDraft, string) {
	raw := c.url
	switch {
	case raw == "":
		return pasteDraft{}, noteUnrecognized
	case shellExpands(raw):
		return pasteDraft{}, noteURLShell
	case !strings.Contains(raw, "://"):
		raw = "http://" + raw // as curl takes it
	}
	u, err := url.Parse(raw)
	switch {
	case urlWithUser(raw):
		return pasteDraft{}, noteURLUser
	case err != nil || u.Host == "":
		return pasteDraft{}, noteUnrecognized
	}
	var d pasteDraft
	root, path := u.Scheme+"://"+u.Host, strings.TrimRight(u.EscapedPath(), "/")
	if i := slices.IndexFunc(curlEndpoints, func(e curlEndpoint) bool { return strings.HasSuffix(path, e.path) }); i >= 0 {
		e := curlEndpoints[i]
		d.Provider = endpointProvider(e.api, root+strings.TrimSuffix(path, e.path))
	} else {
		d.Provider = endpointProvider("messages", root+path)
		addNote(&d.Notes, pasteNote{Code: noteCurlPath, Name: u.Path})
	}
	p := &d.Provider
	api := p.Kind == "api"
	for _, opt := range c.unused {
		addNote(&d.Notes, pasteNote{Code: noteCurlOption, Name: opt})
	}
	switch {
	case c.proxyOpt == "":
	case api || urlWithUser(c.proxy) || strings.ContainsAny(c.proxy, "?#"): // a direct API takes none; a proxy is kept in the clear
		addNote(&d.Notes, pasteNote{Code: noteCurlOption, Name: c.proxyOpt})
	default:
		p.Proxy = c.proxy
	}
	if api && c.noProxyOpt != "" {
		addNote(&d.Notes, pasteNote{Code: noteCurlOption, Name: c.noProxyOpt})
	} else {
		p.NoProxy = c.noProxy
	}
	var body struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(c.body), &body) == nil && strings.TrimSpace(body.Model) != "" {
		p.Model = strings.TrimSpace(body.Model)
		p.Models = []proto.ProviderModel{{ID: p.Model}}
	}
	var hs [][2]string
	key, found := "", false
	for _, line := range c.headers { // one header each, whatever it holds (a quote may span lines)
		name, value, ok := strings.Cut(line, ":")
		h := [2]string{strings.TrimSpace(name), strings.TrimSpace(value)}
		if !ok || h[0] == "" {
			continue
		}
		f := strings.Fields(h[1])
		switch {
		case found: // a second key is a credential header like any
		case strings.EqualFold(h[0], "x-api-key"):
			key, p.Auth, found = h[1], "api_key", true
			continue
		case strings.EqualFold(h[0], "authorization") && len(f) == 2 && strings.EqualFold(f[0], "bearer"):
			key, p.Auth, found = f[1], "bearer", true
			continue
		}
		hs = append(hs, h)
	}
	d.setHeaders(hs, true)
	d.setKey(key)
	return d, ""
}

// ---- cindy ----

// cindyProvider is one provider of a cindy provider import (v1: a cindy:// link's JSON, or a provider file's; cindy's
// docs/provider-import-links.md), as far as Persona reads it.
type cindyProvider struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Auth struct {
		Method string `json:"method"`
		APIKey string `json:"apiKey"`
	} `json:"auth"`
	Endpoints []cindyEndpoint `json:"endpoints"`
	Models    []cindyModel    `json:"models"`
}

// cindyEndpoint is one of a cindy provider's endpoints: an API (protocol) at an address.
type cindyEndpoint struct {
	Protocol    string         `json:"protocol"`
	BaseURL     string         `json:"baseUrl"`
	RequestPath string         `json:"requestPath"`
	APIKey      string         `json:"apiKey"`
	Headers     map[string]any `json:"headers"`
	Models      []cindyModel   `json:"models"`
}

// cindyModel is a model as cindy lists it: its id alone, or an object.
type cindyModel struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	ContextWindow          int      `json:"contextWindow"`
	MaxOutputTokens        int      `json:"maxOutputTokens"`
	Reasoning              *bool    `json:"reasoning"`
	ReasoningEfforts       []string `json:"reasoningEfforts"`
	ReasoningDefaultEffort string   `json:"reasoningDefaultEffort"`
	SupportsImageInput     *bool    `json:"supportsImageInput"`
	SupportsToolCalls      *bool    `json:"supportsToolCalls"`
}

func (m *cindyModel) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &m.ID) == nil {
		return nil
	}
	type plain cindyModel
	var typed *json.UnmarshalTypeError
	if err := json.Unmarshal(b, (*plain)(m)); err != nil && !errors.As(err, &typed) {
		return err
	}
	return nil // a field of another type is left out, as anywhere else in a provider
}

// The cindy endpoint protocols Persona has an API for.
const (
	cindyMessages  = "anthropic-messages"
	cindyResponses = "openai-responses"
	cindyChat      = "openai-chat"
	cindyGoogle    = "google-generative-ai" // no Persona API: an endpoint of it is noted
)

// What cindy v1 defines of a provider's kind and an endpoint's protocol: a note names only these (cindyNamed),
// anything else may be a value pasted into the wrong field. A note names an auth method only as oauth or none.
var (
	cindyKinds     = []string{"custom", "preset", "builtin"}
	cindyProtocols = []string{cindyMessages, cindyResponses, cindyChat, cindyGoogle}
)

// cindyNamed is v when cindy defines it (one of known), else "" (the note goes without a name).
func cindyNamed(v string, known []string) string {
	if slices.Contains(known, v) {
		return v
	}
	return ""
}

// parseCindy reads a cindy v1 provider import's JSON: one provider, or {providers: […]}. A custom provider is one
// draft: its first Messages endpoint as a Claude Code gateway, else its first Responses or Chat endpoint as a direct
// API; its other endpoints are noted, and so is the picked one's own request path (the draft calls the API's usual
// one). A gateway's draft has no auth: v1 does not say which header it takes, and the wizard's check finds it (#5329,
// launcher_auth_auto); a direct API takes Bearer. Presets and built-in key slots name cindy's own catalog: noted, as
// is OAuth. A field of another type than cindy's is left out; data that is no JSON is invalid_json.
func parseCindy(data []byte) pasteResult {
	var batch struct {
		Providers []json.RawMessage `json:"providers"`
	}
	var typed *json.UnmarshalTypeError
	if err := json.Unmarshal(data, &batch); err != nil && !errors.As(err, &typed) {
		return pasteResult{Notes: []pasteNote{{Code: noteInvalidJSON}}}
	}
	if batch.Providers == nil {
		batch.Providers = []json.RawMessage{data}
	}
	res := pasteResult{Format: "cindy"}
	for _, raw := range batch.Providers {
		var p cindyProvider
		if err := json.Unmarshal(raw, &p); err != nil && !errors.As(err, &typed) {
			return pasteResult{Notes: []pasteNote{{Code: noteInvalidJSON}}}
		}
		if res.cindyProvider(p); len(res.Drafts) > pasteMost {
			break // too_many: the rest is not read
		}
	}
	return res
}

// cindyProvider adds p's draft to res, or the notes why there is none.
func (res *pasteResult) cindyProvider(p cindyProvider) {
	if kind := cmp.Or(p.Kind, "custom"); kind != "custom" {
		res.note(noteCindyKind, 0, cindyNamed(kind, cindyKinds))
		return
	}
	if p.Auth.Method == "oauth" {
		res.note(noteCindyAuth, 0, p.Auth.Method)
		return
	}
	usable := func(protocols ...string) int {
		return slices.IndexFunc(p.Endpoints, func(e cindyEndpoint) bool {
			return slices.Contains(protocols, e.Protocol) && strings.TrimSpace(e.BaseURL) != ""
		})
	}
	pick := usable(cindyMessages)
	if pick < 0 {
		pick = usable(cindyResponses, cindyChat)
	}
	if pick < 0 {
		for _, e := range p.Endpoints {
			res.note(noteCindyEndpoint, 0, cindyNamed(e.Protocol, cindyProtocols))
		}
		return
	}
	e := p.Endpoints[pick]
	raw := strings.TrimSpace(e.BaseURL)
	if code := gatewayRefused(raw); code != "" {
		res.note(code, 0, p.Name)
		return
	}
	api := "messages"
	if e.Protocol != cindyMessages {
		api = strings.TrimPrefix(e.Protocol, "openai-")
	}
	d := pasteDraft{Label: p.Name, Provider: endpointProvider(api, raw)}
	pr := &d.Provider
	pr.DisplayName = p.Name
	if pr.Kind == "api" {
		pr.Auth = "bearer"
	}
	models := e.Models
	if len(models) == 0 {
		models = p.Models
	}
	seen := map[string]bool{}
	for _, m := range models {
		if pm := cindyProviderModel(m); pm.ID != "" && !seen[pm.ID] {
			seen[pm.ID], pr.Models = true, append(pr.Models, pm)
		}
	}
	if len(pr.Models) > 0 {
		pr.Model = pr.Models[0].ID
	}
	var hs [][2]string
	for _, name := range slices.Sorted(maps.Keys(e.Headers)) {
		if v, ok := e.Headers[name].(string); ok {
			hs = append(hs, [2]string{name, v})
		}
	}
	d.setHeaders(hs, true)
	if strings.TrimSpace(e.RequestPath) != "" { // a provider has no request path of its own: say so, never the path
		addNote(&d.Notes, pasteNote{Code: noteCindyPath})
	}
	if p.Auth.Method == "none" {
		pr.Auth = ""
		addNote(&d.Notes, pasteNote{Code: noteCindyAuth, Name: p.Auth.Method})
	} else {
		d.setKey(strings.TrimSpace(cmp.Or(e.APIKey, p.Auth.APIKey)))
	}
	for i, other := range p.Endpoints {
		if i != pick {
			addNote(&d.Notes, pasteNote{Code: noteCindyEndpoint, Name: cindyNamed(other.Protocol, cindyProtocols)})
		}
	}
	res.Drafts = append(res.Drafts, d)
}

// cindyProviderModel is a cindy model as a provider's: only the thinking levels Persona knows, and the default only
// when it is one of them.
func cindyProviderModel(m cindyModel) proto.ProviderModel {
	pm := proto.ProviderModel{ID: strings.TrimSpace(m.ID), Name: strings.TrimSpace(m.Name), ContextWindow: max(m.ContextWindow, 0)}
	for _, e := range m.ReasoningEfforts {
		if slices.Contains(proto.EffortLevels, e) && !slices.Contains(pm.Efforts, e) {
			pm.Efforts = append(pm.Efforts, e)
		}
	}
	if slices.Contains(pm.Efforts, m.ReasoningDefaultEffort) {
		pm.DefaultEffort = m.ReasoningDefaultEffort
	}
	// what else cindy says of it, as discovery reads a gateway (#8778): reasoning false and no level is a model that
	// takes none
	pm.MaxOutputTokens, pm.ImageInput, pm.ToolCalls = max(m.MaxOutputTokens, 0), m.SupportsImageInput, m.SupportsToolCalls
	pm.NoEffort = len(pm.Efforts) == 0 && m.Reasoning != nil && !*m.Reasoning
	return pm
}
