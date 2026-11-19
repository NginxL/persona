package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// repositoryItem is a repository_providers item named Acme Code on hosts, relabelling pull requests "MR".
func repositoryItem(id string, hosts ...string) map[string]any {
	return map[string]any{"id": id, "name": "Acme Code", "hosts": hosts,
		"link_kinds": []any{map[string]any{"kind": "pull_request", "label": "MR"}}}
}

// repositoryContrib makes items the manifest's only contributions.
func repositoryContrib(items ...map[string]any) func(map[string]any) {
	return func(m map[string]any) { m["contributes"] = map[string]any{"repository_providers": items} }
}

// repositoryManifest is the smallest manifest with these repository_providers items, for the hub's Parse and the SDK's
// schema alike.
func repositoryManifest(t *testing.T, items ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"manifest_version": 1, "id": "acme-code", "name": "Acme Code", "version": "1.0.0",
		"engines": map[string]any{"extension_protocol": 1}, "runtime": map[string]any{"command": "bin/acme-code"},
		"contributes": map[string]any{"repository_providers": items}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRepositoryProvidersPoint(t *testing.T) {
	p := Points["repository_providers"]
	if p.Name != "repository_providers" || p.Namespace != "repo" || p.Version != 1 || !p.NeedsRPC || p.NeedsHTTP {
		t.Fatalf("point %+v", p)
	}
	m, err := Parse(with(t, repositoryContrib(repositoryItem("acme", "code.acme.test", "ssh.code.acme.test"),
		map[string]any{"id": "mirror", "name": "Acme Mirror", "hosts": []string{"mirror.acme.test"}})))
	if err != nil {
		t.Fatal(err)
	}
	got := m.RepositoryProviders()
	if len(got) != 2 || got[0].ID != "acme" || got[0].Name != "Acme Code" || len(got[0].LinkKinds) != 1 ||
		got[0].LinkKinds[0] != (RepositoryLinkLabel{Kind: wire.RepositoryLinkKindPullRequest, Label: "MR"}) || got[1].LinkKinds != nil {
		t.Fatalf("%+v", got)
	}
	if hosts := m.RepositoryHosts(); !slices.Equal(hosts, []string{"code.acme.test", "ssh.code.acme.test", "mirror.acme.test"}) {
		t.Fatal(hosts)
	}
	if items := m.Items("repository_providers"); !slices.Equal(items, []string{"acme", "mirror"}) {
		t.Fatal(items)
	}
	if got := (&Manifest{}).RepositoryHosts(); got != nil {
		t.Fatal(got)
	}
}

// What an item may be: the hub's Parse and the SDK's schema agree, but for the rules a schema cannot say.
func TestRepositoryProvidersRefusals(t *testing.T) {
	hosts := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("h%d.acme.test", i)
		}
		return out
	}
	edit := func(change func(it map[string]any)) []map[string]any {
		it := repositoryItem("acme", "code.acme.test")
		change(it)
		return []map[string]any{it}
	}
	label := func(kind, label string) map[string]any { return map[string]any{"kind": kind, "label": label} }
	for _, c := range []struct {
		name   string
		items  []map[string]any
		ok     bool
		goOnly bool // a rule the schema cannot say
	}{
		{"one host", edit(func(map[string]any) {}), true, false},
		{"sixteen hosts", edit(func(it map[string]any) { it["hosts"] = hosts(16) }), true, false},
		{"no link kinds", edit(func(it map[string]any) { delete(it, "link_kinds") }), true, false},
		{"an empty link kinds", edit(func(it map[string]any) { it["link_kinds"] = []any{} }), true, false},
		{"both link kinds", edit(func(it map[string]any) {
			it["link_kinds"] = []any{label("issue", "Ticket"), label("pull_request", "Change request")}
		}), true, false},
		{"a name of 80 characters", edit(func(it map[string]any) { it["name"] = strings.Repeat("代", 80) }), true, false},
		{"a name with a wide space inside", edit(func(it map[string]any) { it["name"] = "Acme\u3000Code" }), true, false},
		{"a host with a hex-looking first label", edit(func(it map[string]any) { it["hosts"] = []string{"0x1f.acme.test", "code.0xg"} }), true, false},
		{"a 253-character host", edit(func(it map[string]any) {
			it["hosts"] = []string{strings.Repeat(strings.Repeat("a", 62)+".", 4) + "t"}
		}), true, false},
		{"two items", []map[string]any{repositoryItem("acme", "code.acme.test"), repositoryItem("mirror", "mirror.acme.test")}, true, false},

		{"no hosts", edit(func(it map[string]any) { it["hosts"] = []string{} }), false, false},
		{"seventeen hosts", edit(func(it map[string]any) { it["hosts"] = hosts(17) }), false, false},
		{"a wildcard", edit(func(it map[string]any) { it["hosts"] = []string{"*.acme.test"} }), false, false},
		{"upper case", edit(func(it map[string]any) { it["hosts"] = []string{"Code.acme.test"} }), false, false},
		{"a port", edit(func(it map[string]any) { it["hosts"] = []string{"code.acme.test:8443"} }), false, false},
		{"a scheme", edit(func(it map[string]any) { it["hosts"] = []string{"https://code.acme.test"} }), false, false},
		{"an IP address", edit(func(it map[string]any) { it["hosts"] = []string{"192.0.2.1"} }), false, false},
		{"a numeric last label", edit(func(it map[string]any) { it["hosts"] = []string{"code.acme.123"} }), false, false},
		// A browser's URL parser reads a last label of 0x and hex digits as a number too (WHATWG "ends in a number").
		{"a hex IP address", edit(func(it map[string]any) { it["hosts"] = []string{"0x7f000001"} }), false, false},
		{"a dotted hex IP address", edit(func(it map[string]any) { it["hosts"] = []string{"0x7f.0x0.0x0.0x1"} }), false, false},
		{"a hex last label", edit(func(it map[string]any) { it["hosts"] = []string{"code.0x1f"} }), false, false},
		{"a last label of 0x", edit(func(it map[string]any) { it["hosts"] = []string{"code.0x"} }), false, false},
		{"a 254-character host", edit(func(it map[string]any) {
			it["hosts"] = []string{strings.Repeat(strings.Repeat("a", 62)+".", 4) + "te"}
		}), false, false},
		{"a host twice", edit(func(it map[string]any) { it["hosts"] = []string{"code.acme.test", "code.acme.test"} }), false, false},
		{"github.com", edit(func(it map[string]any) { it["hosts"] = []string{"code.acme.test", "github.com"} }), false, false},
		{"api.github.com", edit(func(it map[string]any) { it["hosts"] = []string{"api.github.com"} }), false, false},
		{"altssh.gitlab.com", edit(func(it map[string]any) { it["hosts"] = []string{"altssh.gitlab.com"} }), false, false},
		{"an unknown field", edit(func(it map[string]any) { it["schemes"] = []string{"acme"} }), false, false},
		{"a bad id", edit(func(it map[string]any) { it["id"] = "Acme!" }), false, false},
		{"no name", edit(func(it map[string]any) { delete(it, "name") }), false, false},
		{"an empty name", edit(func(it map[string]any) { it["name"] = "" }), false, false},
		{"a blank name", edit(func(it map[string]any) { it["name"] = "  " }), false, false},
		// Blank is Unicode's White_Space, not only ASCII's.
		{"a name of a wide space", edit(func(it map[string]any) { it["name"] = "\u3000" }), false, false},
		{"a name of other spaces", edit(func(it map[string]any) { it["name"] = "\u00a0\u1680\u2003\u2028\u2029\u202f\u205f" }), false, false},
		{"a name of 81 characters", edit(func(it map[string]any) { it["name"] = strings.Repeat("a", 81) }), false, false},
		{"a control character in the name", edit(func(it map[string]any) { it["name"] = "Acme\nCode" }), false, false},
		{"no hosts field", edit(func(it map[string]any) { delete(it, "hosts") }), false, false},
		{"link kind merge_request", edit(func(it map[string]any) { it["link_kinds"] = []any{label("merge_request", "MR")} }), false, false},
		{"three link kinds", edit(func(it map[string]any) {
			it["link_kinds"] = []any{label("issue", "Issue"), label("pull_request", "MR"), label("issue", "Bug")}
		}), false, false},
		{"an empty label", edit(func(it map[string]any) { it["link_kinds"] = []any{label("issue", "")} }), false, false},
		{"a blank label", edit(func(it map[string]any) { it["link_kinds"] = []any{label("issue", " ")} }), false, false},
		{"a label of a wide space", edit(func(it map[string]any) { it["link_kinds"] = []any{label("issue", "\u3000")} }), false, false},
		{"a label of a thin space", edit(func(it map[string]any) { it["link_kinds"] = []any{label("issue", "\u2009\u200a")} }), false, false},
		{"a 25-character label", edit(func(it map[string]any) { it["link_kinds"] = []any{label("issue", strings.Repeat("a", 25))} }), false, false},
		{"an unknown field in a link kind", edit(func(it map[string]any) {
			it["link_kinds"] = []any{map[string]any{"kind": "issue", "label": "Issue", "icon": "bug"}}
		}), false, false},
		{"a kind twice", edit(func(it map[string]any) { it["link_kinds"] = []any{label("issue", "Issue"), label("issue", "Bug")} }), false, false},
		{"pull_request twice", edit(func(it map[string]any) {
			it["link_kinds"] = []any{label("pull_request", "MR"), label("pull_request", "PR")}
		}), false, false},
		{"one host in two items", []map[string]any{repositoryItem("acme", "code.acme.test"), repositoryItem("mirror", "code.acme.test")}, false, true},
		{"an id twice", []map[string]any{repositoryItem("acme", "code.acme.test"), repositoryItem("acme", "mirror.acme.test")}, false, true},
	} {
		raw := repositoryManifest(t, c.items...)
		_, hubErr := Parse(raw)
		schemaErr := wire.ValidateJSON("Manifest", raw)
		if (hubErr == nil) != c.ok {
			t.Errorf("%s: hub: want valid=%v, got %v", c.name, c.ok, hubErr)
		}
		if want := c.ok || c.goOnly; (schemaErr == nil) != want {
			t.Errorf("%s: schema: want valid=%v, got %v", c.name, want, schemaErr)
		}
	}
	if _, err := Parse(with(t, func(m map[string]any) {
		delete(m, "runtime")
		delete(m, "activation_events")
		repositoryContrib(repositoryItem("acme", "code.acme.test"))(m)
	})); err == nil || !strings.Contains(err.Error(), "contributes.repository_providers needs a runtime that speaks the protocol") {
		t.Errorf("a provider without a program: %v", err)
	}
	if !slices.Equal(ReservedRepositoryHosts, []string{"github.com", "www.github.com", "api.github.com", "ssh.github.com", "gitlab.com", "altssh.gitlab.com"}) {
		t.Error(ReservedRepositoryHosts)
	}
}

// Being asked about repositories on hosts is agreed to like a permission: the install page says it, an upgrade that
// adds a host says the new ones and asks again, and only the hosts are in the digest.
func TestRepositoryProvidersConsent(t *testing.T) {
	parse := func(items ...map[string]any) *Manifest {
		t.Helper()
		m, err := Parse(with(t, repositoryContrib(items...)))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	plain := mustParse(t)([]byte(exampleManifest))
	one := parse(repositoryItem("acme", "code.acme.test"))
	two := parse(repositoryItem("acme", "code.acme.test", "ssh.code.acme.test"))
	swapped := parse(repositoryItem("acme", "ssh.code.acme.test", "code.acme.test"))
	split := parse(repositoryItem("acme", "code.acme.test"), map[string]any{"id": "ssh", "name": "Acme SSH", "hosts": []string{"ssh.code.acme.test"}})
	installed := func(x *Manifest) *Extension {
		return &Extension{Record: Record{Granted: x.Permissions, GrantedDigest: Digest(x)}, Manifest: x, Full: x}
	}
	line := func(lines []PermissionLine) (PermissionLine, bool) {
		i := slices.IndexFunc(lines, func(l PermissionLine) bool { return l.Key == "repository_provider" })
		if i < 0 {
			return PermissionLine{}, false
		}
		return lines[i], true
	}

	// Extensions without the point digest as they did before it existed (the digests on main before #10330).
	if got := Digest(plain); got != "1ca4a05424c3d32168eed93d358d826c0716defa1c2d45640e0faddb8dc7005b" {
		t.Errorf("an extension without a repository provider changed its digest: %s", got)
	}
	secrets, err := Parse(with(t, func(m map[string]any) {
		m["contributes"] = map[string]any{"secret_providers": []any{map[string]any{"id": "vault", "name": "Example Vault", "schemes": []string{"exv"}}}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := Digest(secrets); got != "22390319f3383d44498ba2a5ff810e703fb527ad6979db1cbdd9a76218be52bd" {
		t.Errorf("a secret provider changed its digest: %s", got)
	}
	if got := sortedRepositoryHosts(plain); len(got) != 0 {
		t.Fatal(got)
	}
	if Digest(plain) == Digest(one) || Digest(one) == Digest(two) {
		t.Error("the hosts are agreed to: the digest changes with them")
	}
	if Digest(two) != Digest(swapped) || Digest(two) != Digest(split) {
		t.Error("the digest depends on the order of the hosts or how the items split them")
	}
	relabeled := parse(map[string]any{"id": "acme", "name": "Acme", "hosts": []string{"code.acme.test"}})
	if Digest(relabeled) != Digest(one) {
		t.Error("a name or a link label is not a permission")
	}

	if l, ok := line(Explain(two)); !ok || l.Title != i18n.T(i18n.Default, "extension-permissions.repository_provider_title") ||
		!strings.Contains(l.Detail, "code.acme.test") || !strings.Contains(l.Detail, "ssh.code.acme.test") {
		t.Errorf("explain: %+v", Explain(two))
	}
	if _, ok := line(Explain(plain)); ok {
		t.Error("an extension without the point has no line")
	}
	if l, ok := line(Explain(split)); !ok || strings.Count(l.Detail, "code.acme.test") != 2 {
		t.Errorf("two items: one line with every host: %+v", Explain(split))
	}
	if l, ok := line(Added(plain, two)); !ok || l.Title != i18n.T(i18n.Default, "extension-permissions.repository_provider_more_title") {
		t.Errorf("added: %+v", Added(plain, two))
	}
	if l, ok := line(Added(one, two)); !ok || !strings.Contains(l.Detail, "ssh.code.acme.test") ||
		strings.Contains(strings.ReplaceAll(l.Detail, "ssh.code.acme.test", ""), "code.acme.test") {
		t.Errorf("only the new host: %+v", Added(one, two))
	}
	for name, pair := range map[string][2]*Manifest{"the same": {two, two}, "reordered": {two, swapped}, "split": {two, split},
		"fewer": {two, one}, "taken away": {one, plain}} {
		if _, ok := line(Added(pair[0], pair[1])); ok {
			t.Errorf("%s: no new host, no line", name)
		}
		if MorePermissions(installed(pair[0]), pair[1]) {
			t.Errorf("%s: no new host, nothing to agree to", name)
		}
	}
	if !MorePermissions(installed(plain), one) || !MorePermissions(installed(one), two) || MorePermissions(installed(plain), plain) {
		t.Error("a new host is agreed to again")
	}

	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		explain, ok := line(ExplainIn(l, two))
		added, ok2 := line(AddedIn(l, one, two))
		if !ok || !ok2 {
			t.Fatalf("%s: no line", l)
		}
		for _, x := range []PermissionLine{explain, added} {
			if x.Title == "" || strings.Contains(x.Title+x.Detail, "extension-permissions.") || strings.Contains(x.Detail, "{hosts}") ||
				!strings.Contains(x.Detail, "ssh.code.acme.test") {
				t.Errorf("%s: %+v", l, x)
			}
		}
		if explain.Title != i18n.T(l, "extension-permissions.repository_provider_title") ||
			added.Title != i18n.T(l, "extension-permissions.repository_provider_more_title") || explain.Title == added.Title {
			t.Errorf("%s: titles %q %q", l, explain.Title, added.Title)
		}
	}
}

// The registry: one installed claimant per host, on or off, refused at preview and again when the plan commits; the
// list and lookups see every installed contribution with whether it takes effect.
func TestRepositoryProvidersRegistry(t *testing.T) {
	ctx := t.Context()
	r, _, _ := newTestRegistry(t)
	preview := func(id, contribution string, hosts ...string) (*Preview, error) {
		dir := t.TempDir()
		writePackage(t, dir, func(m map[string]any) {
			m["id"] = id
			repositoryContrib(repositoryItem(contribution, hosts...))(m)
		})
		return r.Preview(ctx, "dir:"+dir, "")
	}
	install := func(id, contribution string, hosts ...string) {
		t.Helper()
		pv, err := preview(id, contribution, hosts...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.RepositoryProviders(); len(got) != 0 {
		t.Fatal(got)
	}
	install("acme-code", "acme", "code.acme.test", "ssh.code.acme.test")
	want := RepositoryProvider{Extension: "acme-code", Contribution: "acme", Name: "Acme Code", Hosts: []string{"code.acme.test", "ssh.code.acme.test"},
		LinkKinds: []RepositoryLinkLabel{{Kind: "pull_request", Label: "MR"}}, On: true}
	same := func(got RepositoryProvider, on bool) bool {
		w := want
		w.On = on
		return got.Extension == w.Extension && got.Contribution == w.Contribution && got.Name == w.Name && slices.Equal(got.Hosts, w.Hosts) &&
			slices.Equal(got.LinkKinds, w.LinkKinds) && got.On == w.On
	}
	check := func(on bool) {
		t.Helper()
		got := r.RepositoryProviders()
		if i := slices.IndexFunc(got, func(p RepositoryProvider) bool { return p.Extension == "acme-code" }); i < 0 || !same(got[i], on) {
			t.Fatalf("list: %+v, want on=%v", got, on)
		}
		if got, ok := r.RepositoryProviderOf("acme-code", "acme"); !ok || !same(got, on) {
			t.Fatalf("of: %+v %v", got, ok)
		}
		if got, ok := r.RepositoryProviderForHost("ssh.code.acme.test"); !ok || !same(got, on) {
			t.Fatalf("for host: %+v %v", got, ok)
		}
	}
	check(true)
	if got, ok := r.RepositoryProviderForHost("Code.Acme.Test"); !ok || got.Contribution != "acme" {
		t.Error("a host is matched in lower case")
	}
	for _, host := range []string{"acme.test", "other.acme.test", "github.com", ""} {
		if got, ok := r.RepositoryProviderForHost(host); ok {
			t.Errorf("%q: %+v", host, got)
		}
	}
	if _, ok := r.RepositoryProviderOf("acme-code", "other"); ok {
		t.Error("an unknown contribution")
	}
	if _, ok := r.RepositoryProviderOf("example-notes", "acme"); ok {
		t.Error("an extension that is not installed")
	}

	// A second extension claiming one of the hosts is refused at preview, whatever its contribution is called; one on
	// other hosts installs.
	if _, err := preview("acme-mirror", "mirror", "mirror.acme.test", "code.acme.test"); err == nil ||
		!strings.Contains(err.Error(), `the host "code.acme.test" is already extension acme-code's`) {
		t.Fatalf("a claimed host: %v", err)
	}
	install("acme-mirror", "mirror", "mirror.acme.test")
	if got := r.RepositoryProviders(); len(got) != 2 || got[0].Extension != "acme-code" || got[1].Extension != "acme-mirror" || !got[1].On {
		t.Fatalf("%+v", got)
	}

	// Off, it keeps its hosts.
	if err := r.SetEnabled(ctx, "acme-code", false, "test"); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := preview("acme-other", "other", "code.acme.test"); err == nil || !strings.Contains(err.Error(), "code.acme.test") {
		t.Fatalf("a disabled claimant still holds its hosts: %v", err)
	}
	if err := r.SetEnabled(ctx, "acme-code", true, "test"); err != nil {
		t.Fatal(err)
	}
	check(true)
	// With the contribution switched off too.
	if err := r.SetContribution(ctx, "acme-code", "repository_providers", "acme", false, "test"); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := preview("acme-other", "other", "ssh.code.acme.test"); err == nil {
		t.Fatal("a switched-off contribution still holds its hosts")
	}
	if err := r.SetContribution(ctx, "acme-code", "repository_providers", "acme", true, "test"); err != nil {
		t.Fatal(err)
	}
	check(true)

	// A claim made between a preview and its install is refused when the plan commits.
	late, err := preview("acme-late", "late", "late.acme.test")
	if err != nil {
		t.Fatal(err)
	}
	install("acme-early", "early", "late.acme.test")
	if _, err := r.Install(ctx, late.ID, late.Digest, "test"); err == nil || !strings.Contains(err.Error(), `the host "late.acme.test" is already extension acme-early's`) {
		t.Fatalf("the commit did not re-check the host: %v", err)
	}
	if r.Get("acme-late") != nil {
		t.Fatal("installed anyway")
	}

	// Uninstalled, it is gone and its hosts are free.
	if err := r.Uninstall(ctx, "acme-code", false, "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.RepositoryProviderOf("acme-code", "acme"); ok {
		t.Error("an uninstalled provider")
	}
	if _, ok := r.RepositoryProviderForHost("code.acme.test"); ok {
		t.Error("an uninstalled provider's host")
	}
	install("acme-next", "next", "code.acme.test")
	if got, ok := r.RepositoryProviderForHost("code.acme.test"); !ok || got.Extension != "acme-next" {
		t.Fatalf("%+v %v", got, ok)
	}
}

// A stored manifest that no longer parses is loaded loosely, unchecked (an older hub that did not know the point
// installed it): its items that break the point's rules claim nothing, so it never takes a built-in host from GitHub,
// and an off claimant this hub did not refuse never hides the one that is on.
func TestRepositoryProvidersOfAnUnreadableManifest(t *testing.T) {
	ctx := t.Context()
	r, store, _ := newTestRegistry(t)
	dir := t.TempDir()
	writePackage(t, dir, func(m map[string]any) {
		m["id"] = "acme-code"
		repositoryContrib(repositoryItem("acme", "code.acme.test"))(m)
	})
	pv, err := r.Preview(ctx, "dir:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	// Sorted before acme-code, so a lookup by id order meets it first.
	raw := with(t, func(m map[string]any) {
		m["id"] = "acme-aaa"
		repositoryContrib(
			map[string]any{"id": "builtin", "name": "Acme Old", "hosts": []string{"github.com", "Mirror.Acme.Test"}},
			map[string]any{"id": "upper", "name": "Acme Upper", "hosts": []string{"Upper.Acme.Test"}},
			map[string]any{"id": "mirror", "name": "Acme Mirror", "hosts": []string{"mirror.acme.test"}},
			map[string]any{"id": "late", "name": "Acme Late", "hosts": []string{"code.acme.test"}},
		)(m)
	})
	if _, err := Parse(raw); err == nil {
		t.Fatal("the stored manifest parses")
	}
	if err := store.PutExtension(ctx, Record{ID: "acme-aaa", Version: "1.0.0", Source: SourceInstalled, Enabled: true, Manifest: raw}); err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p := r2.Get("acme-aaa"); p == nil || p.Problem == "" {
		t.Fatalf("no problem: %+v", p)
	}
	for _, host := range []string{"github.com", "upper.acme.test"} {
		if got, ok := r2.RepositoryProviderForHost(host); ok {
			t.Errorf("%s: %+v", host, got)
		}
	}
	if got, ok := r2.RepositoryProviderForHost("mirror.acme.test"); !ok || got.Extension != "acme-aaa" || got.Contribution != "mirror" || got.On {
		t.Errorf("mirror: %+v %v", got, ok)
	}
	if got, ok := r2.RepositoryProviderForHost("code.acme.test"); !ok || got.Extension != "acme-code" || !got.On {
		t.Errorf("the off claimant hid the one that is on: %+v %v", got, ok)
	}
	var ids []string
	for _, p := range r2.RepositoryProviders() {
		ids = append(ids, p.Extension+"/"+p.Contribution)
	}
	if !slices.Equal(ids, []string{"acme-aaa/mirror", "acme-aaa/late", "acme-code/acme"}) {
		t.Errorf("list: %v", ids)
	}
	for _, c := range []string{"builtin", "upper"} {
		if got, ok := r2.RepositoryProviderOf("acme-aaa", c); ok {
			t.Errorf("%s: %+v", c, got)
		}
	}
	if hosts := r2.Get("acme-aaa").Full.RepositoryHosts(); !slices.Equal(hosts, []string{"mirror.acme.test", "code.acme.test"}) {
		t.Errorf("hosts: %v", hosts)
	}
}

// A stored ext:<extension>/<contribution> provider names the extension as it was called when it was saved: a market
// rename keeps it reachable under the canonical id, and the renamed extension keeps its own hosts.
func TestRepositoryProviderOfFollowsRenames(t *testing.T) {
	ctx := t.Context()
	store := &identityStore{memStore: newMemStore()}
	r := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	change := func(id, version string) func(map[string]any) {
		return func(m map[string]any) {
			m["id"], m["version"] = id, version
			repositoryContrib(repositoryItem("acme", "code.acme.test"))(m)
		}
	}
	dir := t.TempDir()
	writePackage(t, dir, change("old-acme", "1.0.0"))
	pv, err := r.Preview(ctx, "dir:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := r.Install(ctx, pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	old.Record.Origin = "market:example-market/old-acme"
	if err := store.PutExtension(ctx, old.Record); err != nil {
		t.Fatal(err)
	}

	pkg := t.TempDir()
	writePackage(t, pkg, change("new-acme", "2.0.0"))
	raw, err := os.ReadFile(ManifestPath(pkg))
	if err != nil {
		t.Fatal(err)
	}
	archive := tarGz(t, map[string]string{ManifestFile: string(raw), "bin/notes": "#!/bin/sh\n"})
	sum := sha256.Sum256(archive)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(srv.Close)
	spec, err := ParseMarket(srv.URL + "/index.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	entry := &MarketEntry{Manifest: raw, Parsed: m, Replaces: "old-acme",
		Packages: []MarketPackage{{URL: srv.URL + "/pkg.tar.gz", SHA256: hex.EncodeToString(sum[:])}}}
	next, err := r.PreviewMarket(ctx, &Market{Spec: spec, Client: srv.Client()}, "example-market", entry, "old-acme")
	if err != nil {
		t.Fatal(err)
	}
	if next.RenameFrom != "old-acme" {
		t.Fatalf("not a rename: %+v", next)
	}
	if _, err := r.UpgradePlan(ctx, "old-acme", next.ID, next.Digest, nil, "test"); err != nil {
		t.Fatal("the renamed extension's own hosts are not a clash:", err)
	}
	for _, id := range []string{"old-acme", "new-acme"} {
		if got, ok := r.RepositoryProviderOf(id, "acme"); !ok || got.Extension != "new-acme" || got.Contribution != "acme" {
			t.Errorf("%s: %+v %v", id, got, ok)
		}
	}
	if got, ok := r.RepositoryProviderForHost("code.acme.test"); !ok || got.Extension != "new-acme" {
		t.Errorf("%+v %v", got, ok)
	}
	if got := r.RepositoryProviders(); len(got) != 1 || got[0].Extension != "new-acme" {
		t.Errorf("%+v", got)
	}
	if err := r.Uninstall(ctx, "new-acme", false, "test"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-acme", "new-acme"} {
		if got, ok := r.RepositoryProviderOf(id, "acme"); ok {
			t.Errorf("%s after uninstall: %+v", id, got)
		}
	}
}
