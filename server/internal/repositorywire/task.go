// Package repositorywire holds the repository types and checks shared by the manager (internal/assistant/repository)
// and the node (internal/workspacenaming): requested refs, layouts, the preparation snapshot a manager hands to a
// node, and the alias and ref rules both sides apply. It depends on the standard library only (#11013).
package repositorywire

import (
	"encoding/hex"
	"slices"
	"strings"
	"unicode"
)

type Problem string

func (e Problem) Error() string { return "task_repositories_" + string(e) }

type RequestedRef struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type Layout string

const (
	LayoutRoot      Layout = "root"
	LayoutContainer Layout = "container"
)

type SourceSnapshot struct {
	Identity        Identity `json:"identity"`
	CloneURL        string   `json:"clone_url"`
	TransportOrigin string   `json:"transport_origin"`
	CatalogRevision int64    `json:"catalog_revision"`
	// ProviderName keeps an extension provider's name, so the row still reads when it is gone (absent for the built-ins).
	ProviderName string `json:"provider_name,omitempty"`
}

// ValidateAlias is shared with naming and preparation. Empty means no directory override.
func ValidateAlias(alias string) error {
	if alias == "" {
		return nil
	}
	if len(alias) > 64 || strings.HasSuffix(alias, ".") {
		return Problem("invalid")
	}
	for i, c := range alias {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && strings.ContainsRune("._-", c)) {
			return Problem("invalid")
		}
	}
	if IsWindowsReservedName(alias) {
		return Problem("invalid")
	}
	return nil
}

// IsWindowsReservedName checks one directory component, independently of the
// alias character/length rules. Templates use it before and after expansion.
// Extensions and spaces before the extension do not escape a device name;
// Windows also reserves COM/LPT with the superscript digits 1, 2 and 3.
func IsWindowsReservedName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	if slices.Contains([]string{"CON", "PRN", "AUX", "NUL"}, base) {
		return true
	}
	if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
		number := base[3:]
		return len(number) == 1 && number[0] >= '1' && number[0] <= '9' || number == "\u00b9" || number == "\u00b2" || number == "\u00b3"
	}
	return false
}

// RepositoryPath maps only explicitly selected layouts; legacy cwd requests never use it.
func RepositoryPath(layout Layout, alias string, count int) (string, error) {
	if layout == LayoutRoot && count == 1 {
		return ".", nil
	}
	if layout != LayoutContainer || alias == "" {
		return "", Problem("invalid")
	}
	if err := ValidateAlias(alias); err != nil {
		return "", err
	}
	return alias, nil
}

func NormalizeRef(ref RequestedRef) (RequestedRef, error) {
	if ref.Kind == "" && ref.Value == "" {
		ref.Kind = "default"
	}
	if ref.Kind == "default" {
		if ref.Value != "" {
			return ref, Problem("invalid")
		}
		return ref, nil
	}
	if ref.Kind == "commit" {
		if len(ref.Value) != 40 && len(ref.Value) != 64 {
			return ref, Problem("invalid")
		}
		if _, err := hex.DecodeString(ref.Value); err != nil {
			return ref, Problem("invalid")
		}
		ref.Value = strings.ToLower(ref.Value)
		return ref, nil
	}
	if ref.Kind != "branch" && ref.Kind != "tag" && ref.Kind != "ref" {
		return ref, Problem("invalid")
	}
	value := ref.Value
	if value == "" || len(value) > 1024 || value == "@" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " ~^:?*[\\") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".") || strings.ContainsFunc(value, unicode.IsControl) {
		return ref, Problem("invalid")
	}
	if ref.Kind == "ref" && !strings.HasPrefix(value, "refs/") {
		return ref, Problem("invalid")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return ref, Problem("invalid")
		}
	}
	return ref, nil
}

type PreparationRepository struct {
	RepoID         string         `json:"repo_id"`
	DirectoryAlias string         `json:"directory_alias,omitempty"`
	RequestedRef   RequestedRef   `json:"requested_ref"`
	SourceSnapshot SourceSnapshot `json:"source_snapshot"`
}

type PreparationSnapshot struct {
	SchemaVersion int                     `json:"schema_version"`
	Manager       string                  `json:"manager"`
	TodoID        string                  `json:"todo_id"`
	Revision      int64                   `json:"revision"`
	Repositories  []PreparationRepository `json:"repositories"`
	Digest        string                  `json:"digest,omitempty"`
}

// DefaultAlias is a suggestion only; callers must report collisions rather than silently rename.
func DefaultAlias(identity Identity) string {
	parts := strings.Split(identity.Path, "/")
	alias := parts[len(parts)-1]
	if ValidateAlias(alias) != nil {
		return "repo"
	}
	return alias
}
