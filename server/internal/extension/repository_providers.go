package extension

import (
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Repository providers (docs/extensions/repository-providers.md): an extension's repository_providers contribution is
// a code-hosting source on the exact hosts it names, for the repository catalog and a to-do's repositories and links.
// The registry says which installed extension claims a host and whether it is on; calling it (repo.parse, repo.check,
// repo.link, repo.describe) is the manager's (extmgr/repository_providers.go).

// ReservedRepositoryHosts are the hosts the built-in GitHub and GitLab providers read addresses on (the ones the
// repository package's Parse maps to them): no repository_providers contribution may claim one.
var ReservedRepositoryHosts = []string{"github.com", "www.github.com", "api.github.com", "ssh.github.com", "gitlab.com", "altssh.gitlab.com"}

// The bounds of one repository_providers item, the same as manifest.schema.json's RepositoryProviderItem.
const (
	repositoryNameMost  = 80
	repositoryHostsMost = 16
	repositoryLabelMost = 24
	exactHostMost       = 253
)

// RepositoryProvider is one installed repository_providers contribution, on or off.
type RepositoryProvider struct {
	// Extension is the extension's canonical id (after a rename), Contribution the item's id.
	Extension, Contribution string
	// Name is the contribution's name (Acme Code): what the catalog and the to-do's repositories call the source.
	Name string
	// Hosts are the exact lowercase hosts it claims.
	Hosts []string
	// LinkKinds relabel the link kinds it names ("MR" for pull_request); the others keep the built-in words.
	LinkKinds []RepositoryLinkLabel
	// On: the extension is enabled, has no problem and the contribution is switched on. Whether its program runs is
	// the manager's to say.
	On bool
}

// checkRepositoryProvider holds one repository_providers item to the point's rules (Points' Item).
func checkRepositoryProvider(p RepositoryProviderItem) error {
	if !idPattern.MatchString(p.ID) {
		return fmt.Errorf("repository_providers id %q: a-z0-9_-", p.ID)
	}
	if !shortText(p.Name, repositoryNameMost) {
		return fmt.Errorf("repository_providers %s: a name of 1 to %d characters, no control characters", p.ID, repositoryNameMost)
	}
	if len(p.Hosts) == 0 || len(p.Hosts) > repositoryHostsMost {
		return fmt.Errorf("repository_providers %s: 1 to %d hosts", p.ID, repositoryHostsMost)
	}
	for i, h := range p.Hosts {
		if !exactHost(h) {
			return fmt.Errorf("repository_providers %s: host %q: an exact lowercase host name of at most %d characters, not an IP address",
				p.ID, h, exactHostMost)
		}
		if slices.Contains(ReservedRepositoryHosts, h) {
			return fmt.Errorf("repository_providers %s: host %q is the built-in GitHub's or GitLab's (not %s)", p.ID, h,
				strings.Join(ReservedRepositoryHosts, ", "))
		}
		if slices.Contains(p.Hosts[:i], h) {
			return fmt.Errorf("repository_providers %s: host %q twice", p.ID, h)
		}
	}
	if len(p.LinkKinds) > len(wire.RepositoryLinkKinds) {
		return fmt.Errorf("repository_providers %s: at most %d link_kinds", p.ID, len(wire.RepositoryLinkKinds))
	}
	for i, k := range p.LinkKinds {
		if !slices.Contains(wire.RepositoryLinkKinds, k.Kind) {
			return fmt.Errorf("repository_providers %s: link kind %q: one of %s", p.ID, k.Kind, strings.Join(wire.RepositoryLinkKinds, ", "))
		}
		if slices.ContainsFunc(p.LinkKinds[:i], func(o RepositoryLinkLabel) bool { return o.Kind == k.Kind }) {
			return fmt.Errorf("repository_providers %s: link kind %q twice", p.ID, k.Kind)
		}
		if !shortText(k.Label, repositoryLabelMost) {
			return fmt.Errorf("repository_providers %s: link kind %s: a label of 1 to %d characters, no control characters", p.ID, k.Kind,
				repositoryLabelMost)
		}
	}
	return nil
}

// shortText is text for people of 1 to most characters: not only spaces, no control characters. Spaces are Unicode's
// White_Space (what TrimSpace drops, U+3000 included), the same set manifest.schema.json's name and label patterns
// exclude from the one character they require.
func shortText(s string, most int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= most && !strings.ContainsFunc(s, unicode.IsControl)
}

// endsInNumber is a host whose last label is a number, all digits or 0x and hex digits: an IPv4 address (192.0.2.1,
// 0x7f000001), or what a browser's URL parser reads as one and refuses (code.123, code.0x1f; the WHATWG URL
// standard's "ends in a number"), never a domain name. Upper case is refused before this (hostPattern).
var endsInNumber = lazyre.New(`(^|\.)([0-9]+|0x[0-9a-f]*)$`)

// exactHost is a lowercase host name: no scheme, port, path or wildcard, at most 253 characters, not an IP address.
func exactHost(h string) bool {
	return !strings.HasPrefix(h, "*.") && len(h) <= exactHostMost && hostPattern.MatchString(h) && net.ParseIP(h) == nil &&
		!endsInNumber.MatchString(h)
}

// repositoryProvidersOf are p's repository_providers contributions as installed (Full), each On when it takes effect.
// Called with r.mu held; id is p's canonical id.
func repositoryProvidersOf(id string, p *Extension) []RepositoryProvider {
	if p == nil || p.Full == nil {
		return nil
	}
	var on []string
	if p.Record.Enabled && p.Problem == "" && p.Manifest != nil {
		on = p.Manifest.Items("repository_providers")
	}
	var out []RepositoryProvider
	for _, it := range p.Full.RepositoryProviders() {
		out = append(out, RepositoryProvider{Extension: id, Contribution: it.ID, Name: it.Name, Hosts: slices.Clone(it.Hosts),
			LinkKinds: slices.Clone(it.LinkKinds), On: slices.Contains(on, it.ID)})
	}
	return out
}

// RepositoryProviders are every installed repository_providers contribution, on or off (an extension that is off, has
// a problem or has the contribution switched off keeps its hosts), by extension id and then in manifest order.
func (r *Registry) RepositoryProviders() []RepositoryProvider {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RepositoryProvider
	for _, id := range r.sortedIDs() {
		out = append(out, repositoryProvidersOf(id, r.extensions[id])...)
	}
	return out
}

// RepositoryProviderOf is the contribution of extension, on or off. A stored ext:<extension>/<contribution> provider
// keeps the id of the day it was saved, so extension is followed through renames; the answer's Extension is the
// canonical id.
func (r *Registry) RepositoryProviderOf(extension, contribution string) (RepositoryProvider, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.canonicalID(extension)
	for _, p := range repositoryProvidersOf(id, r.extensions[id]) {
		if p.Contribution == contribution {
			return p, true
		}
	}
	return RepositoryProvider{}, false
}

// RepositoryProviderForHost is the installed contribution, on or off, that claims host (exact, lowercase). The
// install refuses a second claim, but rows this hub did not install (an older hub's, which did not know the point)
// may hold one: then the first that is on wins, else the first, by extension id.
func (r *Registry) RepositoryProviderForHost(host string) (RepositoryProvider, bool) {
	host = strings.ToLower(host)
	r.mu.Lock()
	defer r.mu.Unlock()
	var off *RepositoryProvider
	for _, id := range r.sortedIDs() {
		for _, p := range repositoryProvidersOf(id, r.extensions[id]) {
			if !slices.Contains(p.Hosts, host) {
				continue
			}
			if p.On {
				return p, true
			}
			if off == nil {
				off = &p
			}
		}
	}
	if off != nil {
		return *off, true
	}
	return RepositoryProvider{}, false
}

// sortedIDs are the registered extensions' ids, sorted. Called with r.mu held.
func (r *Registry) sortedIDs() []string {
	ids := make([]string, 0, len(r.extensions))
	for id := range r.extensions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
