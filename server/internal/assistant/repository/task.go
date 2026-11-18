package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Task associations describe work. They never authorize or prepare disk resources.
const CatalogKey = "assistant.repositories"
const MaxTaskRepositories = 20
const MaxRepositoryLinks = 50
const MaxRevision int64 = 9007199254740991

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

// CatalogEntry shares the existing catalog wire shape, including its optional transport.
type CatalogEntry struct {
	Identity
	ID       string `json:"id"`
	CloneURL string `json:"clone_url,omitempty"`
	// ProviderName is an extension provider's name when the entry was saved (absent for the built-ins).
	ProviderName string `json:"provider_name,omitempty"`
}
type Catalog struct {
	Revision     int64          `json:"revision"`
	Repositories []CatalogEntry `json:"repositories"`
}
type SourceSnapshot struct {
	Identity        Identity `json:"identity"`
	CloneURL        string   `json:"clone_url"`
	TransportOrigin string   `json:"transport_origin"`
	CatalogRevision int64    `json:"catalog_revision"`
	// ProviderName keeps an extension provider's name, so the row still reads when it is gone (absent for the built-ins).
	ProviderName string `json:"provider_name,omitempty"`
}
type LinkInput struct {
	URL    string `json:"url,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Number string `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	// Resolved is an extension identity's link as the service resolved it before the transaction (never JSON).
	Resolved *ResolvedLink `json:"-"`
}

// ResolvedLink is a link of an extension provider's repository, taken from the stored set or asked of the provider
// (repo.link) before any transaction; Source is the repository it belongs to.
type ResolvedLink struct {
	Source SourceSnapshot
	Link   TaskLink
}
type TaskLink struct {
	Kind         string `json:"kind"`
	Number       string `json:"number"`
	CanonicalURL string `json:"canonical_url"`
	Title        string `json:"title,omitempty"`
}
type TaskInput struct {
	RepoID         string       `json:"repo_id"`
	DirectoryAlias string       `json:"directory_alias,omitempty"`
	RequestedRef   RequestedRef `json:"requested_ref"`
	// Omitted links preserve an existing repository's links. An explicit [] clears them.
	Links []LinkInput `json:"links,omitempty"`
}
type TaskRepository struct {
	RepoID         string         `json:"repo_id"`
	DirectoryAlias string         `json:"directory_alias,omitempty"`
	RequestedRef   RequestedRef   `json:"requested_ref"`
	SourceSnapshot SourceSnapshot `json:"source_snapshot"`
	Links          []TaskLink     `json:"links"`
	CatalogState   string         `json:"catalog_state,omitempty"`
	// ProviderState and LinkKinds describe an extension provider's row when answering reads (available, off or
	// missing; its own link labels). Computed like CatalogState, never stored.
	ProviderState string      `json:"provider_state,omitempty"`
	LinkKinds     []LinkLabel `json:"link_kinds,omitempty"`
}
type TaskSet struct {
	SchemaVersion int              `json:"schema_version"`
	Manager       string           `json:"manager"`
	TodoID        string           `json:"todo_id"`
	Revision      int64            `json:"revision"`
	Repositories  []TaskRepository `json:"repositories"`
}
type CreateBindings struct {
	CatalogRevision *int64      `json:"catalog_revision"`
	Repositories    []TaskInput `json:"repositories"`
}
type SetInput struct {
	ExpectedRevision        *int64      `json:"expected_revision"`
	ExpectedCatalogRevision *int64      `json:"expected_catalog_revision"`
	Repositories            []TaskInput `json:"repositories"`
}
type LinksInput struct {
	ExpectedRevision *int64      `json:"expected_revision"`
	Links            []LinkInput `json:"links"`
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
	base := strings.ToUpper(strings.SplitN(alias, ".", 2)[0])
	if slices.Contains([]string{"CON", "PRN", "AUX", "NUL"}, base) || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return Problem("invalid")
	}
	return nil
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

// CloneTransport accepts credential-free repository roots, not provider subpages. An extension provider's address was
// checked against its hosts when it was saved; here only its shape is, so it works with the provider gone.
func CloneTransport(raw string, identity Identity) (string, error) {
	if IsExtensionProvider(identity.Provider) {
		if _, ok := cloneHost(raw); !ok {
			return "", Problem("invalid")
		}
		return raw, nil
	}
	parsed, err := Parse(raw, identity.Provider)
	if err != nil || parsed != identity {
		return "", Problem("invalid")
	}
	normalized := raw
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok {
			return "", Problem("invalid")
		}
		normalized = "ssh://git@" + host + "/" + path
	}
	u, err := url.Parse(normalized)
	if err != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", Problem("invalid")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		path = path[:len(path)-4]
	}
	if strings.ToLower(path) != identity.Path {
		return "", Problem("invalid")
	}
	return raw, nil
}

func NormalizeLink(identity Identity, in LinkInput) (TaskLink, error) {
	in.Title = strings.TrimSpace(in.Title)
	if !utf8.ValidString(in.Title) || len(in.Title) > 1024 || strings.ContainsFunc(in.Title, unicode.IsControl) {
		return TaskLink{}, Problem("invalid")
	}
	if IsExtensionProvider(identity.Provider) {
		return extensionLink(identity, in)
	}
	if in.URL != "" {
		if in.Kind != "" || in.Number != "" || len(in.URL) > 2048 {
			return TaskLink{}, Problem("invalid")
		}
		u, err := url.Parse(in.URL)
		base, baseErr := url.Parse(identity.CanonicalURL)
		if err != nil || baseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
			return TaskLink{}, Problem("link_repository_mismatch")
		}
		// Validate the actual URL before comparing its normalized source identity.
		// Public HTTP aliases share HTTPS identity; self-hosted schemes/ports do not.
		actual, err := Parse(in.URL, identity.Provider)
		if err != nil || actual != identity {
			return TaskLink{}, Problem("link_repository_mismatch")
		}
		prefix := base.Path + "/"
		paths := map[string]string{"issues/": "issue", "pull/": "pull_request"}
		if identity.Provider == "gitlab" {
			paths = map[string]string{"-/issues/": "issue", "-/merge_requests/": "pull_request"}
		}
		path := strings.ToLower(u.Path)
		for suffix, kind := range paths {
			if strings.HasPrefix(path, prefix+suffix) {
				in.Kind = kind
				in.Number = strings.TrimPrefix(path, prefix+suffix)
				break
			}
		}
		if in.Kind == "" {
			return TaskLink{}, Problem("link_repository_mismatch")
		}
	}
	if in.Kind != "issue" && in.Kind != "pull_request" || len(in.Number) == 0 || len(in.Number) > 20 || in.Number[0] == '0' {
		return TaskLink{}, Problem("invalid")
	}
	for _, c := range in.Number {
		if c < '0' || c > '9' {
			return TaskLink{}, Problem("invalid")
		}
	}
	segment := "issues"
	if in.Kind == "pull_request" {
		segment = "pull"
	}
	if identity.Provider == "gitlab" {
		segment = "-/issues"
		if in.Kind == "pull_request" {
			segment = "-/merge_requests"
		}
	}
	return TaskLink{Kind: in.Kind, Number: in.Number, CanonicalURL: identity.CanonicalURL + "/" + segment + "/" + in.Number, Title: in.Title}, nil
}

// extensionLink is an extension identity's link as the service resolved it before the transaction: a stored link or the
// provider's answer, re-checked. Nothing is synthesized from a kind and number: without an answer the link is refused.
func extensionLink(identity Identity, in LinkInput) (TaskLink, error) {
	if in.URL != "" && (in.Kind != "" || in.Number != "" || len(in.URL) > 2048) || in.URL == "" && in.Kind == "" {
		return TaskLink{}, Problem("invalid")
	}
	if in.Resolved == nil || in.Resolved.Source.Identity != identity {
		return TaskLink{}, Problem("invalid")
	}
	link := in.Resolved.Link
	if link.Kind != "issue" && link.Kind != "pull_request" || !linkNumber(link.Number) || !underRepository(identity, link.CanonicalURL) ||
		in.Kind != "" && (in.Kind != link.Kind || in.Number != link.Number) {
		return TaskLink{}, Problem("link_repository_mismatch")
	}
	if in.Title != "" {
		link.Title = in.Title
	} else if len(link.Title) > 1024 || !utf8.ValidString(link.Title) || strings.ContainsFunc(link.Title, unicode.IsControl) {
		link.Title = ""
	}
	return link, nil
}

// PreserveLinkTitles keeps metadata when older clients send only link identities.
// Removing a link still removes its title; a nonempty title replaces the saved title.
func PreserveLinkTitles(current, next []TaskLink) {
	for i := range next {
		if next[i].Title != "" {
			continue
		}
		for _, old := range current {
			if old.Kind == next[i].Kind && old.Number == next[i].Number {
				next[i].Title = old.Title
				break
			}
		}
	}
}

func NormalizeLinks(identity Identity, input []LinkInput) ([]TaskLink, error) {
	if len(input) > MaxRepositoryLinks {
		return nil, Problem("limit")
	}
	out := make([]TaskLink, 0, len(input))
	seen := map[string]bool{}
	for _, in := range input {
		link, err := NormalizeLink(identity, in)
		if err != nil {
			return nil, err
		}
		key := link.Kind + "/" + link.Number
		if seen[key] {
			return nil, Problem("duplicate_link")
		}
		seen[key] = true
		out = append(out, link)
	}
	return out, nil
}

// Bind preserves saved identity even if a catalog ID now points at a different source.
// Removing and explicitly reselecting a repository is the only way to adopt that source.
func Bind(current []TaskRepository, input []TaskInput, catalog Catalog) ([]TaskRepository, error) {
	if len(input) > MaxTaskRepositories {
		return nil, Problem("limit")
	}
	out := make([]TaskRepository, 0, len(input))
	ids := map[string]bool{}
	aliases := map[string]bool{}
	identities := map[Identity]bool{}
	for _, in := range input {
		if in.RepoID == "" || len(in.RepoID) > 80 {
			return nil, Problem("invalid")
		}
		if ids[in.RepoID] {
			return nil, Problem("duplicate_repo")
		}
		ids[in.RepoID] = true
		if err := ValidateAlias(in.DirectoryAlias); err != nil {
			return nil, err
		}
		if in.DirectoryAlias != "" {
			key := strings.ToLower(in.DirectoryAlias)
			if aliases[key] {
				return nil, Problem("duplicate_alias")
			}
			aliases[key] = true
		}
		ref, err := NormalizeRef(in.RequestedRef)
		if err != nil {
			return nil, err
		}
		row := TaskRepository{RepoID: in.RepoID, DirectoryAlias: in.DirectoryAlias, RequestedRef: ref, Links: []TaskLink{}}
		old := slices.IndexFunc(current, func(r TaskRepository) bool { return r.RepoID == in.RepoID })
		if old >= 0 {
			row.SourceSnapshot = current[old].SourceSnapshot
			row.Links = slices.Clone(current[old].Links)
		} else {
			index := slices.IndexFunc(catalog.Repositories, func(r CatalogEntry) bool { return r.ID == in.RepoID })
			if index < 0 {
				return nil, Problem("not_found")
			}
			entry := catalog.Repositories[index]
			transport := entry.CloneURL
			origin := "catalog"
			if transport == "" {
				transport = entry.CanonicalURL
				origin = "canonical_default"
			}
			if _, err := CloneTransport(transport, entry.Identity); err != nil {
				return nil, err
			}
			row.SourceSnapshot = SourceSnapshot{entry.Identity, transport, origin, catalog.Revision, entry.ProviderName}
		}
		if identities[row.SourceSnapshot.Identity] {
			return nil, Problem("duplicate_repo")
		}
		identities[row.SourceSnapshot.Identity] = true
		if in.Links != nil {
			previous := row.Links
			row.Links, err = NormalizeLinks(row.SourceSnapshot.Identity, in.Links)
			if err != nil {
				return nil, err
			}
			PreserveLinkTitles(previous, row.Links)
		}
		if row.Links == nil {
			row.Links = []TaskLink{}
		}
		out = append(out, row)
	}
	return out, nil
}

func CatalogState(row TaskRepository, catalog Catalog) string {
	for _, entry := range catalog.Repositories {
		if entry.ID == row.RepoID {
			if entry.Identity == row.SourceSnapshot.Identity {
				return "current"
			}
			return "identity_changed"
		}
	}
	if strings.HasPrefix(row.RepoID, "linked_") {
		return "unlisted"
	}
	return "removed"
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

// Snapshot copies the explicitly selected repositories. Consumers persist this value
// with their operation; later edits of business associations never invalidate it.
func Snapshot(manager, todo string, revision int64, rows []TaskRepository, selected []string) (PreparationSnapshot, error) {
	out := PreparationSnapshot{SchemaVersion: 1, Manager: manager, TodoID: todo, Revision: revision, Repositories: []PreparationRepository{}}
	seen := map[string]bool{}
	for _, id := range selected {
		if seen[id] {
			return out, Problem("duplicate_repo")
		}
		seen[id] = true
		index := slices.IndexFunc(rows, func(r TaskRepository) bool { return r.RepoID == id })
		if index < 0 {
			return out, Problem("not_found")
		}
		r := rows[index]
		out.Repositories = append(out.Repositories, PreparationRepository{r.RepoID, r.DirectoryAlias, r.RequestedRef, r.SourceSnapshot})
	}
	slices.SortFunc(out.Repositories, func(a, b PreparationRepository) int { return strings.Compare(a.RepoID, b.RepoID) })
	payload := struct {
		SchemaVersion int                     `json:"schema_version"`
		Manager       string                  `json:"manager"`
		TodoID        string                  `json:"todo_id"`
		Repositories  []PreparationRepository `json:"repositories"`
	}{out.SchemaVersion, out.Manager, out.TodoID, out.Repositories}
	raw, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return out, nil
}

func ValidRevision(revision *int64) bool {
	return revision != nil && *revision >= 0 && *revision < MaxRevision
}
func LinkKey(identity Identity, link TaskLink) string {
	return identity.Provider + "|" + identity.InstanceURL + "|" + identity.Path + "|" + link.Kind + "|" + link.Number
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

// ParseTaskLink derives a credential-free source from an exact issue/PR URL, offline.
func ParseTaskLink(raw string) (Identity, TaskLink, error) {
	provider := "github"
	if strings.Contains(raw, "/merge_requests/") || strings.Contains(raw, "/-/issues/") {
		provider = "gitlab"
	}
	if provider == "gitlab" && !strings.Contains(raw, "/-/") {
		raw = strings.Replace(raw, "/merge_requests/", "/-/merge_requests/", 1)
	}
	identity, err := Parse(raw, provider)
	if err != nil {
		return Identity{}, TaskLink{}, Problem("invalid")
	}
	link, err := NormalizeLink(identity, LinkInput{URL: raw})
	return identity, link, err
}

// MergeTaskLinks appends references without overwriting aliases, refs or source snapshots.
// A reported repository need not be preconfigured in the manager catalog.
func MergeTaskLinks(current []TaskRepository, urls []string, catalog Catalog) ([]TaskRepository, error) {
	input := make([]LinkInput, 0, len(urls))
	for _, raw := range urls {
		input = append(input, LinkInput{URL: raw})
	}
	return MergeTaskLinkDetails(current, input, catalog)
}

// ParseTaskLinkInput accepts an exact URL and optional title, without a remote lookup. A link the service resolved
// through its extension provider keeps that provider's repository and link.
func ParseTaskLinkInput(in LinkInput) (Identity, TaskLink, error) {
	if in.Kind != "" || in.Number != "" {
		return Identity{}, TaskLink{}, Problem("invalid")
	}
	if in.Resolved != nil {
		identity := in.Resolved.Source.Identity
		if !IsExtensionProvider(identity.Provider) {
			return Identity{}, TaskLink{}, Problem("invalid")
		}
		link, err := NormalizeLink(identity, in)
		return identity, link, err
	}
	identity, link, err := ParseTaskLink(in.URL)
	if err != nil {
		return identity, link, err
	}
	link, err = NormalizeLink(identity, LinkInput{Kind: link.Kind, Number: link.Number, Title: in.Title})
	return identity, link, err
}

// MergeTaskLinkDetails adds references or refreshes known titles without erasing them.
func MergeTaskLinkDetails(current []TaskRepository, input []LinkInput, catalog Catalog) ([]TaskRepository, error) {
	out := slices.Clone(current)
	for i := range out {
		out[i].Links = slices.Clone(out[i].Links)
	}
	for _, in := range input {
		identity, link, err := ParseTaskLinkInput(in)
		if err != nil {
			return nil, err
		}
		index := slices.IndexFunc(out, func(r TaskRepository) bool { return r.SourceSnapshot.Identity == identity })
		if index < 0 {
			if len(out) >= MaxTaskRepositories {
				return nil, Problem("limit")
			}
			digest := sha256.Sum256([]byte(identity.Provider + "|" + identity.CanonicalURL))
			id := "linked_" + hex.EncodeToString(digest[:12])
			source := SourceSnapshot{Identity: identity, CloneURL: identity.CanonicalURL, TransportOrigin: "canonical_default"}
			if in.Resolved != nil {
				// An extension provider's repository is cloned from where its provider said (repo.parse's clone_url).
				source.ProviderName = in.Resolved.Source.ProviderName
				if clone := in.Resolved.Source.CloneURL; clone != "" && clone != identity.CanonicalURL {
					if _, err := CloneTransport(clone, identity); err == nil {
						source.CloneURL, source.TransportOrigin = clone, "provider"
					}
				}
			}
			for _, entry := range catalog.Repositories {
				if entry.Identity == identity && !slices.ContainsFunc(out, func(r TaskRepository) bool { return r.RepoID == entry.ID }) {
					// The saved repository's own transport, as binding it would give (Bind).
					id = entry.ID
					source.CatalogRevision = catalog.Revision
					source.CloneURL, source.TransportOrigin = identity.CanonicalURL, "canonical_default"
					if entry.CloneURL != "" {
						source.CloneURL = entry.CloneURL
						source.TransportOrigin = "catalog"
					}
					if entry.ProviderName != "" {
						source.ProviderName = entry.ProviderName
					}
					break
				}
			}
			if _, err := CloneTransport(source.CloneURL, identity); err != nil {
				return nil, err
			}
			out = append(out, TaskRepository{RepoID: id, RequestedRef: RequestedRef{Kind: "default"}, SourceSnapshot: source, Links: []TaskLink{}})
			index = len(out) - 1
		}
		existing := slices.IndexFunc(out[index].Links, func(l TaskLink) bool { return l.Kind == link.Kind && l.Number == link.Number })
		if existing >= 0 {
			if link.Title != "" {
				out[index].Links[existing].Title = link.Title
			}
			continue
		}
		if len(out[index].Links) >= MaxRepositoryLinks {
			return nil, Problem("limit")
		}
		out[index].Links = append(out[index].Links, link)
	}
	return out, nil
}
