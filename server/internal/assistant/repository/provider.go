package repository

import (
	"context"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Providers (docs/extensions/repository-providers.md): GitHub and GitLab are built in and work offline as before; an
// extension's repository_providers contribution is the source of the hosts it claims, with the provider id
// ext:<extension>/<contribution>. The manager asks the extension (extmgr/repository_providers.go) and re-checks every
// answer here. A transaction never waits on an extension: the service resolves an extension's links first (ResolvedLink).

const (
	GitHub = "github"
	GitLab = "gitlab"
)

// Provider states for clients: available, off (installed but switched off, failing or not running) and missing (the
// stored provider is no longer installed; computed by readers, never listed).
const (
	ProviderAvailable = "available"
	ProviderOff       = "off"
	ProviderMissing   = "missing"
)

// BuiltinHosts are the hosts Parse maps to the built-in providers. No extension may claim one
// (extension.ReservedRepositoryHosts is the same list; a test keeps them equal).
var BuiltinHosts = []string{"github.com", "www.github.com", "api.github.com", "ssh.github.com", "gitlab.com", "altssh.gitlab.com"}

// LinkLabel is a provider's own word for a link kind (MR for pull_request). Kinds stay issue and pull_request.
type LinkLabel struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// ProviderInfo is one source of repositories, as clients list it (GET /api/v1/assistant/repositories/providers).
type ProviderInfo struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Builtin   bool        `json:"builtin"`
	Hosts     []string    `json:"hosts"`
	State     string      `json:"state"`
	LinkKinds []LinkLabel `json:"link_kinds,omitempty"`
}

// Builtins are GitHub and GitLab. Their self-hosted instances are chosen explicitly, as before.
func Builtins() []ProviderInfo {
	return []ProviderInfo{
		{ID: GitHub, Name: "GitHub", Builtin: true, Hosts: []string{"github.com"}, State: ProviderAvailable},
		{ID: GitLab, Name: "GitLab", Builtin: true, Hosts: []string{"gitlab.com"}, State: ProviderAvailable},
	}
}

// Parsed is an address as its provider normalized it. ProviderName is an extension provider's name (the snapshot kept
// beside the identity); DisplayName the provider's own name for the repository. Both are empty for the built-ins.
type Parsed struct {
	Identity     Identity
	CloneURL     string
	ProviderName string
	DisplayName  string
}

// Description is what repo.describe says about a link: its title and, optionally, open, closed or merged.
type Description struct {
	Title, State string
}

// ProviderError is why an extension provider did not answer. Code: no_provider (not installed), provider_off (installed
// but switched off, failing or not running), not_found, not_signed_in, refused or failed; the assistant adds
// providers_starting (none listed yet, right after a restart). Provider is its name when known, Why one line of the
// provider's words or the manager's reason.
type ProviderError struct {
	Code, Provider, Why string
}

func (e *ProviderError) Error() string {
	s := "repository provider " + e.Provider + ": " + e.Code
	if e.Why != "" {
		s += ": " + e.Why
	}
	return s
}

// AnswerError is an extension's answer the manager refused; Why says which rule it broke.
type AnswerError struct{ Why string }

func (e *AnswerError) Error() string { return "repository provider answer: " + e.Why }

// Extensions are the provider extensions (extmgr.Manager). Lists and lookups never block or call a program; every call
// is bounded and its answer re-checked (Check*Answer). Errors are *ProviderError.
type Extensions interface {
	// RepositoryProviders are the installed contributions, on or off.
	RepositoryProviders() []ProviderInfo
	// RepositoryProvider is a provider by id, following extension renames; the answer carries the current id.
	RepositoryProvider(id string) (ProviderInfo, bool)
	// RepositoryProviderForHost is the installed contribution that claims an exact lowercase host, on or off.
	RepositoryProviderForHost(host string) (ProviderInfo, bool)
	RepositoryParse(ctx context.Context, provider, remote string) (Parsed, error)
	RepositoryCheck(ctx context.Context, id Identity) error
	RepositoryLink(ctx context.Context, repo Identity, in LinkInput) (TaskLink, error)
	RepositoryDescribe(ctx context.Context, provider, canonicalURL string) (Description, error)
}

// ExtensionProvider is the provider id of an extension's repository_providers contribution.
func ExtensionProvider(extension, contribution string) string {
	return "ext:" + extension + "/" + contribution
}

// SplitExtensionProvider is the extension and contribution of an ext: provider id; false for the built-ins and for
// anything malformed.
func SplitExtensionProvider(provider string) (extension, contribution string, ok bool) {
	rest, found := strings.CutPrefix(provider, "ext:")
	if !found {
		return "", "", false
	}
	extension, contribution, found = strings.Cut(rest, "/")
	if !found || !validProviderPart(extension) || !validProviderPart(contribution) {
		return "", "", false
	}
	return extension, contribution, true
}

// IsExtensionProvider: provider is a well-formed ext: id.
func IsExtensionProvider(provider string) bool {
	_, _, ok := SplitExtensionProvider(provider)
	return ok
}

// validProviderPart matches the manifest's ID pattern, ^[a-z0-9][a-z0-9_-]{0,31}$.
func validProviderPart(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '-')) {
			return false
		}
	}
	return true
}

// Registry routes an address to its provider: the built-ins here, offline and unchanged, the rest to Extensions
// (nil: no provider extensions).
type Registry struct {
	Extensions Extensions
}

// Providers are the built-ins, then the installed extension providers.
func (r Registry) Providers() []ProviderInfo {
	out := Builtins()
	if r.Extensions != nil {
		out = append(out, r.Extensions.RepositoryProviders()...)
	}
	return out
}

// Provider is the extension provider an address or provider id names. ok is false for the built-ins: an explicit
// github or gitlab, and an address no installed extension claims (the built-in rules decide it, as before).
func (r Registry) Provider(remote, provider string) (ProviderInfo, bool, error) {
	if provider == GitHub || provider == GitLab {
		return ProviderInfo{}, false, nil
	}
	if provider != "" {
		if !IsExtensionProvider(provider) {
			return ProviderInfo{}, false, ErrRemote
		}
		if r.Extensions != nil {
			if info, ok := r.Extensions.RepositoryProvider(provider); ok {
				return info, true, nil
			}
		}
		_, contribution, _ := SplitExtensionProvider(provider)
		return ProviderInfo{}, false, &ProviderError{Code: "no_provider", Provider: contribution}
	}
	host, ok := RemoteHost(remote)
	if !ok || r.Extensions == nil {
		return ProviderInfo{}, false, nil
	}
	info, ok := r.Extensions.RepositoryProviderForHost(host)
	return info, ok, nil
}

// Parse normalizes an address: an explicit github or gitlab, or an address on a host no extension claims, with today's
// offline rules; anything else through its extension (repo.parse).
func (r Registry) Parse(ctx context.Context, remote, provider string) (Parsed, error) {
	info, ok, err := r.Provider(remote, provider)
	if err != nil {
		return Parsed{}, err
	}
	if !ok {
		id, err := Parse(remote, provider)
		return Parsed{Identity: id}, err
	}
	if err := CheckProviderRemote(info, remote); err != nil {
		return Parsed{}, ErrRemote
	}
	if info.State != ProviderAvailable {
		return Parsed{}, &ProviderError{Code: "provider_off", Provider: info.Name}
	}
	return r.Extensions.RepositoryParse(ctx, info.ID, remote)
}

// Link normalizes one link of repo: offline for the built-ins (NormalizeLink), through repo.link for an extension.
func (r Registry) Link(ctx context.Context, repo Identity, in LinkInput) (TaskLink, error) {
	if !IsExtensionProvider(repo.Provider) {
		return NormalizeLink(repo, in)
	}
	if r.Extensions == nil {
		_, contribution, _ := SplitExtensionProvider(repo.Provider)
		return TaskLink{}, &ProviderError{Code: "no_provider", Provider: contribution}
	}
	return r.Extensions.RepositoryLink(ctx, repo, in)
}

// RemoteHost is the lowercase host of an http(s), ssh:// or git@host:path address; false when it has none.
func RemoteHost(remote string) (string, bool) {
	if len(remote) > 2048 || remote == "" || strings.ContainsAny(remote, " \t\r\n\\") {
		return "", false
	}
	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") {
		host, _, ok := strings.Cut(strings.TrimPrefix(remote, "git@"), ":")
		if !ok || host == "" || strings.ContainsAny(host, "/@[]") {
			return "", false
		}
		return strings.ToLower(host), true
	}
	u, err := url.Parse(remote)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
		return "", false
	}
	switch u.Scheme {
	case "http", "https", "ssh":
		return strings.ToLower(u.Hostname()), true
	}
	return "", false
}

// credentialKeys are query keys that carry a secret; an address with one is never sent anywhere.
var credentialKeys = []string{"token", "access_token", "private_token", "auth", "authorization", "password", "api_key", "apikey", "signature"}

// CheckProviderRemote: remote may be sent to the provider that claims info.Hosts: on one of them, with no credentials.
func CheckProviderRemote(info ProviderInfo, remote string) error {
	host, ok := RemoteHost(remote)
	if !ok || !slices.Contains(info.Hosts, host) || strings.ContainsFunc(remote, unicode.IsControl) {
		return &AnswerError{"the address is not on the provider's hosts"}
	}
	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") {
		return nil
	}
	u, err := url.Parse(remote)
	if err != nil {
		return &AnswerError{"not an address"}
	}
	if u.Scheme == "ssh" {
		if u.User == nil || u.User.String() != "git" {
			return &AnswerError{"an ssh address names the git user only"}
		}
		return nil
	}
	if u.User != nil {
		return &AnswerError{"credentials in the address"}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return &AnswerError{"not an address"}
	}
	for key := range query {
		if slices.Contains(credentialKeys, strings.ToLower(key)) {
			return &AnswerError{"credentials in the address"}
		}
	}
	return nil
}

// ParseAnswer is repo.parse's answer as the program sent it.
type ParseAnswer struct {
	InstanceURL, Path, CanonicalURL, CloneURL, DisplayName string
}

// CheckParseAnswer re-checks repo.parse's answer: an https instance on one of the provider's hosts with nothing but an
// authority, a path of plain segments, canonical_url = instance_url/path, a credential-free clone address on its hosts
// and a one-line display name. The identity's provider is info.ID.
func CheckParseAnswer(info ProviderInfo, a ParseAnswer) (Parsed, error) {
	instance, err := url.Parse(a.InstanceURL)
	if err != nil || len(a.InstanceURL) > 2048 || instance.Scheme != "https" || instance.User != nil || instance.Opaque != "" ||
		instance.RawQuery != "" || instance.ForceQuery || instance.Fragment != "" || instance.Path != "" || instance.RawPath != "" {
		return Parsed{}, &AnswerError{"instance_url is not an https address with only a host"}
	}
	host := instance.Hostname()
	if host != strings.ToLower(host) || !slices.Contains(info.Hosts, host) {
		return Parsed{}, &AnswerError{"instance_url is not on the provider's hosts"}
	}
	if port := instance.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || n == 443 {
			return Parsed{}, &AnswerError{"instance_url has a bad port"}
		}
	} else if strings.HasSuffix(instance.Host, ":") {
		return Parsed{}, &AnswerError{"instance_url has a bad port"}
	}
	if "https://"+instance.Host != a.InstanceURL {
		return Parsed{}, &AnswerError{"instance_url is not in canonical form"}
	}
	if !plainPath(a.Path, 20) {
		return Parsed{}, &AnswerError{"path is not 1 to 20 plain segments of at most 512 bytes"}
	}
	if a.CanonicalURL != a.InstanceURL+"/"+a.Path {
		return Parsed{}, &AnswerError{"canonical_url is not instance_url/path"}
	}
	out := Parsed{Identity: Identity{Provider: info.ID, InstanceURL: a.InstanceURL, Path: a.Path, CanonicalURL: a.CanonicalURL},
		ProviderName: info.Name, DisplayName: oneLine(a.DisplayName, 80)}
	if a.CloneURL != "" && a.CloneURL != a.CanonicalURL {
		if err := CheckProviderClone(info, a.CloneURL); err != nil {
			return Parsed{}, err
		}
		out.CloneURL = a.CloneURL
	}
	return out, nil
}

// plainPath: 1 to most segments of URL-unreserved characters, none empty, . or .., at most 512 bytes.
func plainPath(path string, most int) bool {
	parts := strings.Split(path, "/")
	if path == "" || len(path) > 512 || len(parts) > most {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
				return false
			}
		}
	}
	return true
}

// CheckProviderClone: raw is a credential-free clone address on one of the provider's hosts (https, ssh://git@ or
// git@host:path).
func CheckProviderClone(info ProviderInfo, raw string) error {
	host, ok := cloneHost(raw)
	if !ok || !slices.Contains(info.Hosts, host) {
		return &AnswerError{"clone_url is not a credential-free address on the provider's hosts"}
	}
	return nil
}

// cloneHost is the host of a clone address that is safe to store and hand to git: https with no user, ssh://git@ or
// git@host:path, with a plain path and no query or fragment.
func cloneHost(raw string) (string, bool) {
	if len(raw) > 2048 || raw == "" || strings.ContainsAny(raw, " \t\r\n\\") || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", false
	}
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok || host == "" || host != strings.ToLower(host) || strings.ContainsAny(host, "/@[]") || !plainPath(strings.TrimPrefix(path, "/"), 64) {
			return "", false
		}
		return host, true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", false
	}
	switch u.Scheme {
	case "https":
		if u.User != nil {
			return "", false
		}
	case "ssh":
		if u.User == nil || u.User.String() != "git" {
			return "", false
		}
	default:
		return "", false
	}
	host := u.Hostname()
	if host == "" || host != strings.ToLower(host) || strings.HasSuffix(u.Host, ":") || net.ParseIP(host) != nil && strings.Contains(host, ":") {
		return "", false
	}
	if !plainPath(strings.TrimPrefix(u.Path, "/"), 64) {
		return "", false
	}
	return host, true
}

// LinkAnswer is repo.link's answer as the program sent it.
type LinkAnswer struct {
	Kind, Number, CanonicalURL, Title string
}

// CheckLinkAnswer re-checks repo.link's answer for in on repo: kind issue or pull_request (the one asked about, when
// asked by kind and number), a number like the built-ins' (1 to 20 digits, no leading zero), and an https canonical_url
// under the repository's own, with no query or fragment. The title is kept on one line, at most 1024 bytes.
func CheckLinkAnswer(repo Identity, in LinkInput, a LinkAnswer) (TaskLink, error) {
	if a.Kind != "issue" && a.Kind != "pull_request" {
		return TaskLink{}, &AnswerError{"kind is not issue or pull_request"}
	}
	if !linkNumber(a.Number) {
		return TaskLink{}, &AnswerError{"number is not 1 to 20 digits without a leading zero"}
	}
	if in.Kind != "" && (in.Kind != a.Kind || in.Number != a.Number) {
		return TaskLink{}, &AnswerError{"the link is not the one asked about"}
	}
	if !underRepository(repo, a.CanonicalURL) {
		return TaskLink{}, &AnswerError{"canonical_url is not an https address under the repository's"}
	}
	return TaskLink{Kind: a.Kind, Number: a.Number, CanonicalURL: a.CanonicalURL, Title: clipBytes(oneLine(a.Title, 1024), 1024)}, nil
}

// underRepository: link is an https address strictly below repo's canonical URL, with plain segments and no
// credentials, query or fragment.
func underRepository(repo Identity, link string) bool {
	rest, ok := strings.CutPrefix(link, repo.CanonicalURL+"/")
	if !ok || len(link) > 2048 || !strings.HasPrefix(link, "https://") {
		return false
	}
	u, err := url.Parse(link)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	return plainPath(strings.TrimSuffix(rest, "/"), 20)
}

func linkNumber(n string) bool {
	if n == "" || len(n) > 20 || n[0] == '0' {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// CheckDescribeAnswer keeps repo.describe's title on one line (at most 1024 bytes) and its state only when it is
// open, closed or merged.
func CheckDescribeAnswer(title, state string) Description {
	if state != "open" && state != "closed" && state != "merged" {
		state = ""
	}
	return Description{Title: clipBytes(oneLine(title, 1024), 1024), State: state}
}

// oneLine is s with its words joined by single spaces and control characters dropped, cut to most runes with "…".
func oneLine(s string, most int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) <= most {
		return s
	}
	return string([]rune(s)[:most-1]) + "…"
}

// clipBytes cuts s to at most most bytes on a rune boundary.
func clipBytes(s string, most int) string {
	if len(s) <= most {
		return s
	}
	cut := most
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
