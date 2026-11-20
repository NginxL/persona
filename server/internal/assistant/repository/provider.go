package repository

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/repositorywire"
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
type LinkLabel = repositorywire.LinkLabel

// ProviderInfo is one source of repositories, as clients list it (GET /api/v1/assistant/repositories/providers).
type ProviderInfo = repositorywire.ProviderInfo

// Builtins are GitHub and GitLab. Their self-hosted instances are chosen explicitly, as before.
func Builtins() []ProviderInfo {
	return []ProviderInfo{
		{ID: GitHub, Name: "GitHub", Builtin: true, Hosts: []string{"github.com"}, State: ProviderAvailable},
		{ID: GitLab, Name: "GitLab", Builtin: true, Hosts: []string{"gitlab.com"}, State: ProviderAvailable},
	}
}

// Parsed is an address as its provider normalized it. ProviderName is an extension provider's name (the snapshot kept
// beside the identity); DisplayName the provider's own name for the repository. Both are empty for the built-ins.
type Parsed = repositorywire.Parsed

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
type AnswerError = repositorywire.AnswerError

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
	return repositorywire.SplitExtensionProvider(provider)
}

// IsExtensionProvider: provider is a well-formed ext: id.
func IsExtensionProvider(provider string) bool {
	return repositorywire.IsExtensionProvider(provider)
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
		return &AnswerError{Why: "the address is not on the provider's hosts"}
	}
	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") {
		return nil
	}
	u, err := url.Parse(remote)
	if err != nil {
		return &AnswerError{Why: "not an address"}
	}
	if u.Scheme == "ssh" {
		if u.User == nil || u.User.String() != "git" {
			return &AnswerError{Why: "an ssh address names the git user only"}
		}
		return nil
	}
	if u.User != nil {
		return &AnswerError{Why: "credentials in the address"}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return &AnswerError{Why: "not an address"}
	}
	for key := range query {
		if slices.Contains(credentialKeys, strings.ToLower(key)) {
			return &AnswerError{Why: "credentials in the address"}
		}
	}
	return nil
}

// ParseAnswer is repo.parse's answer as the program sent it.
type ParseAnswer = repositorywire.ParseAnswer

// CheckParseAnswer re-checks repo.parse's answer: an https instance on one of the provider's hosts with nothing but an
// authority, a path of plain segments, canonical_url = instance_url/path, a credential-free clone address on its hosts
// and a one-line display name. The identity's provider is info.ID.
func CheckParseAnswer(info ProviderInfo, a ParseAnswer) (Parsed, error) {
	return repositorywire.CheckParseAnswer(info, a)
}

// plainPath: 1 to most segments of URL-unreserved characters, none empty, . or .., at most 512 bytes.
func plainPath(path string, most int) bool {
	return repositorywire.PlainPath(path, most)
}

// CheckProviderClone: raw is a credential-free clone address on one of the provider's hosts (https, ssh://git@ or
// git@host:path).
func CheckProviderClone(info ProviderInfo, raw string) error {
	return repositorywire.CheckProviderClone(info, raw)
}

// cloneHost is the host of a clone address that is safe to store and hand to git: https with no user, ssh://git@ or
// git@host:path, with a plain path and no query or fragment.
func cloneHost(raw string) (string, bool) {
	return repositorywire.CloneHost(raw)
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
		return TaskLink{}, &AnswerError{Why: "kind is not issue or pull_request"}
	}
	if !linkNumber(a.Number) {
		return TaskLink{}, &AnswerError{Why: "number is not 1 to 20 digits without a leading zero"}
	}
	if in.Kind != "" && (in.Kind != a.Kind || in.Number != a.Number) {
		return TaskLink{}, &AnswerError{Why: "the link is not the one asked about"}
	}
	if !underRepository(repo, a.CanonicalURL) {
		return TaskLink{}, &AnswerError{Why: "canonical_url is not an https address under the repository's"}
	}
	return TaskLink{Kind: a.Kind, Number: a.Number, CanonicalURL: a.CanonicalURL, Title: clipBytes(oneLine(a.Title, 1024), 1024)}, nil
}

// underRepository: link is an https address strictly below repo's canonical URL, with plain segments and no
// credentials, query or fragment.
func underRepository(repo Identity, link string) bool {
	return repositorywire.UnderRepository(repo, link)
}

func linkNumber(n string) bool {
	return repositorywire.LinkNumber(n)
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
	return repositorywire.OneLine(s, most)
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
