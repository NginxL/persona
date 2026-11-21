package repositorywire

import (
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LinkLabel is a provider's display label for a stable issue or pull_request kind.
type LinkLabel struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// SplitExtensionProvider splits a well-formed ext: provider id.
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

// IsExtensionProvider reports whether a provider id names an extension contribution.
func IsExtensionProvider(provider string) bool {
	_, _, ok := SplitExtensionProvider(provider)
	return ok
}

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

// PlainPath checks bounded URL-unreserved path segments.
func PlainPath(path string, most int) bool {
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

// CloneHost checks a credential-free clone address and returns its lowercase host.
func CloneHost(raw string) (string, bool) {
	if len(raw) > 2048 || raw == "" || strings.ContainsAny(raw, " \t\r\n\\") || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", false
	}
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok || host == "" || host != strings.ToLower(host) || strings.ContainsAny(host, "/@[]") || !PlainPath(strings.TrimPrefix(path, "/"), 64) {
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
	if !PlainPath(strings.TrimPrefix(u.Path, "/"), 64) {
		return "", false
	}
	return host, true
}

// UnderRepository checks that a link is strictly below its repository URL.
func UnderRepository(repo Identity, link string) bool {
	rest, ok := strings.CutPrefix(link, repo.CanonicalURL+"/")
	if !ok || len(link) > 2048 || !strings.HasPrefix(link, "https://") {
		return false
	}
	u, err := url.Parse(link)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	return PlainPath(strings.TrimSuffix(rest, "/"), 20)
}

// LinkNumber checks the shared issue and pull-request number format.
func LinkNumber(n string) bool {
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

type ProviderInfo struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Builtin   bool        `json:"builtin"`
	Hosts     []string    `json:"hosts"`
	State     string      `json:"state"`
	LinkKinds []LinkLabel `json:"link_kinds,omitempty"`
	// Capabilities are an extension provider's optional answers (pr_status: repo.pr_status, #11584).
	Capabilities []string `json:"capabilities,omitempty"`
}

type Parsed struct {
	Identity     Identity
	CloneURL     string
	ProviderName string
	DisplayName  string
}

type AnswerError struct{ Why string }

type ParseAnswer struct {
	InstanceURL, Path, CanonicalURL, CloneURL, DisplayName string
}

func (e *AnswerError) Error() string { return "repository provider answer: " + e.Why }

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
	if !PlainPath(a.Path, 20) {
		return Parsed{}, &AnswerError{"path is not 1 to 20 plain segments of at most 512 bytes"}
	}
	if a.CanonicalURL != a.InstanceURL+"/"+a.Path {
		return Parsed{}, &AnswerError{"canonical_url is not instance_url/path"}
	}
	out := Parsed{Identity: Identity{Provider: info.ID, InstanceURL: a.InstanceURL, Path: a.Path, CanonicalURL: a.CanonicalURL},
		ProviderName: info.Name, DisplayName: OneLine(a.DisplayName, 80)}
	if a.CloneURL != "" && a.CloneURL != a.CanonicalURL {
		if err := CheckProviderClone(info, a.CloneURL); err != nil {
			return Parsed{}, err
		}
		out.CloneURL = a.CloneURL
	}
	return out, nil
}

func CheckProviderClone(info ProviderInfo, raw string) error {
	host, ok := CloneHost(raw)
	if !ok || !slices.Contains(info.Hosts, host) {
		return &AnswerError{"clone_url is not a credential-free address on the provider's hosts"}
	}
	return nil
}

func OneLine(s string, most int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) <= most {
		return s
	}
	return string([]rune(s)[:most-1]) + "…"
}
