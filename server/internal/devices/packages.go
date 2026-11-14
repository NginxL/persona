package devices

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/netproxy"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

// Node packages (docs/operations/node-update.md "Node packages"): the build a computer's node gets, for its OS, architecture
// and the kind of program it runs (personad, or persona-node, the device's own). In order:
//
//  1. the hub's own builds on this machine: the node binary dir (deploy.sh's, a self-updating hub's release, what an
//     air-gapped hub's owner dropped in) and the bundled dir (the Docker image's own platform), of the hub's version;
//  2. the release of the hub's version, dl/<version>/manifest.json at the hub's node_packages.url (devicewire.Config),
//     fetched into the cache;
//  3. when there is none (an edge, dev or pull request image), the newest release, dl/latest.json, if this manager
//     drives the node it has (its node protocol range and capabilities, as compatOf judges a node): the fallback;
//  4. else no build: Redeploy is not offered, and the machine card says what to do.
//
// Nothing is signed (the user's call, docs/operations/hub-update.md "Security: no signing"): a fetched build must match the
// manifest's size and sha256 (archive) and binary_sha256 (unpacked), read as a build for that OS and architecture of
// that kind of program, and, on this machine's own platform, run and say the release's version and build (as hub
// self-update checks its own); the node's node-update-check checks the version once more on arrival. It lands in the
// cache whole: downloaded and checked in a staging directory, then renamed into place.

// Package lookups are remembered: found ones for a while, failures shortly (a manifest that appears, a network that
// comes back).
const (
	packageFoundTTL = 10 * time.Minute
	packageMissTTL  = time.Minute
	// packageLookup bounds one lookup's manifest fetches; packageDownload one download.
	packageLookup   = 20 * time.Second
	packageDownload = 10 * time.Minute
)

// nodePackage is a node build for a platform.
type nodePackage struct {
	// Version is what a node running it reports: the hub's version for its own builds, the release's otherwise.
	Version string
	Program string // nodeproto.ProgramPersonad or ProgramPersonaNode: what it is
	// Fallback: not of the hub's version (the newest release this manager drives).
	Fallback   bool
	goos, arch string
	path       string // a file on this machine; "" until fetched
	// A fetched one: <base>/<dir>/<file.Name>, of release build; dir also names its cache directory.
	base, dir string
	// flat: listed in <base>/latest.json, so also served at <base>/<file.Name>, where hub self-update fetches it. A
	// source without per-version directories has it only there: a GitHub repository's releases/latest/download.
	flat  bool
	file  update.File
	build int64
	// The release's node protocol range and capabilities (a fetched one), kept with it in the cache so a cached
	// fallback is judged again before it is used.
	nodeProtocol, nodeProtocolMin int
	nodeCapabilities              []string
	// stale: the last build found, used again because the release site does not answer now (remembered shortly).
	stale bool
}

type packageKey struct{ goos, arch, program, hub, base string }

type packageResult struct {
	pkg nodePackage
	err error
	at  time.Time
}

// nodePackages remembers lookups, makes one lookup per platform and kind at a time (the others wait for it) and one
// download per file at a time.
type nodePackages struct {
	mu       sync.Mutex
	results  map[packageKey]packageResult
	looking  map[packageKey]chan struct{} // closed when that lookup is done
	fetching map[string]*sync.Mutex       // by the cache file a download goes to
	// httpClient replaces netproxy's client (tests); nil: netproxy.HTTPClient.
	httpClient func(proxy *url.URL, timeout time.Duration) *http.Client
	now        func() time.Time
}

func (p *nodePackages) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// localNodeDirs are where the hub's own node builds are: the node binary dir, then the bundled one.
func localNodeDirs() []string {
	dirs := []string{nodeBinaryDir()}
	if b := identity.NodeBundledDir(); b != "" && b != dirs[0] {
		dirs = append(dirs, b)
	}
	return dirs
}

// safeVersion: a version that can name a directory of the release (dl/<version>/) and of the cache.
var safeVersion = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (d *Devices) packageKey(goos, arch, program string) packageKey {
	cfg := d.config()
	if program != nodeproto.ProgramPersonaNode {
		program = nodeproto.ProgramPersonad // personad, or an older node that does not say
	}
	return packageKey{goos: goos, arch: arch, program: program, hub: cfg.HubVersion, base: cfg.NodePackagesURL}
}

// cachedPackage is the lookup remembered for k, if it is still fresh.
func (d *Devices) cachedPackage(k packageKey) (packageResult, bool) {
	r, fresh, ok := d.rememberedPackage(k)
	return r, ok && fresh
}

// rememberedPackage is the lookup remembered for k however old (ok), and whether it is still fresh. A result that
// stands in for the site (stale) or says the site is away is looked up again after a minute; what the site said,
// found or not, after ten.
func (d *Devices) rememberedPackage(k packageKey) (r packageResult, fresh, ok bool) {
	p := &d.packages
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok = p.results[k]
	if !ok {
		return r, false, false
	}
	ttl := packageFoundTTL
	if r.pkg.stale || (r.err != nil && siteAway(r.err)) {
		ttl = packageMissTTL
	}
	return r, p.clock().Sub(r.at) <= ttl, true
}

func (d *Devices) remember(k packageKey, pkg nodePackage, err error) {
	p := &d.packages
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.results == nil {
		p.results = map[packageKey]packageResult{}
	}
	p.results[k] = packageResult{pkg: pkg, err: err, at: p.clock()}
}

// standIn marks pkg as standing in for what the site would say (stale): remembered for a minute only.
func standIn(pkg nodePackage) nodePackage {
	pkg.stale = true
	return pkg
}

// lastFound is the build last found for k, however long ago, while the release site does not answer: what the node
// should run stays that (a node on the fallback build does not turn out of date because the site is away).
func (d *Devices) lastFound(k packageKey) (nodePackage, bool) {
	p := &d.packages
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.results[k]
	if !ok || r.err != nil {
		return nodePackage{}, false
	}
	pkg := r.pkg
	pkg.stale = true
	return pkg, true
}

// nodePackageFor looks the build up (manifests over the network when the hub has none of its own), and remembers it.
func (d *Devices) nodePackageFor(ctx context.Context, goos, arch, program string) (nodePackage, error) {
	return d.lookupShared(ctx, d.packageKey(goos, arch, program))
}

// lookupShared looks k up and remembers it, or waits for the lookup of k that is running already and takes its
// result: the machines of one platform make one lookup between them.
func (d *Devices) lookupShared(ctx context.Context, k packageKey) (nodePackage, error) {
	p := &d.packages
	p.mu.Lock()
	if ch, ok := p.looking[k]; ok {
		p.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": ctx.Err().Error()})
		}
		if r, ok := d.cachedPackage(k); ok {
			return r.pkg, r.err
		}
		return d.lookupShared(ctx, k) // forgotten already (a test's clock): look it up again
	}
	ch := make(chan struct{})
	if p.looking == nil {
		p.looking = map[packageKey]chan struct{}{}
	}
	p.looking[k] = ch
	p.mu.Unlock()
	pkg, err := d.lookupPackage(ctx, k)
	d.remember(k, pkg, err)
	p.mu.Lock()
	delete(p.looking, k)
	close(ch)
	p.mu.Unlock()
	return pkg, err
}

// lookupSoon looks k up in the background (once at a time) and says the machines may have changed when done.
func (d *Devices) lookupSoon(k packageKey) {
	p := &d.packages
	p.mu.Lock()
	_, running := p.looking[k]
	p.mu.Unlock()
	if running {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(d.deployContext(), packageLookup)
		defer cancel()
		_, _ = d.lookupShared(ctx, k)
		d.changed()
	}()
}

// nodeTarget is the build the node with info should run: the version of the build its platform resolves to once
// that is known (the hub's own, or the fallback's), the hub's version until then and when there is none. Never
// blocks: an unknown platform is looked up in the background.
func (d *Devices) nodeTarget(info proto.NodeInfo) string {
	hub := d.hubVersion()
	if info.OS == "" || info.Arch == "" {
		return hub
	}
	k := d.packageKey(info.OS, info.Arch, nodeProgram(info))
	r, fresh, ok := d.rememberedPackage(k)
	if !fresh {
		d.lookupSoon(k) // what was remembered stands until the lookup lands, so a node does not flip out of date meanwhile
	}
	if !ok || r.err != nil {
		return hub
	}
	return r.pkg.Version
}

// resolvedTargetKnown is nodeTarget once the lookup is done (it may fetch manifests), and whether it is known: false
// when the release site did not answer (and nothing found before stands in), so the hub's version is a guess. A lookup
// that found no build at all leaves the hub's version as the target, which is known.
func (d *Devices) resolvedTargetKnown(ctx context.Context, info proto.NodeInfo) (string, bool) {
	pkg, known := d.resolvedPackageKnown(ctx, info)
	return pkg.Version, known
}

// resolvedPackageKnown is the build behind resolvedTargetKnown: its version and whether it is a fallback. A lookup
// that found no build gives the hub's version, not a fallback.
func (d *Devices) resolvedPackageKnown(ctx context.Context, info proto.NodeInfo) (nodePackage, bool) {
	if info.OS == "" || info.Arch == "" {
		return nodePackage{Version: d.hubVersion()}, true
	}
	k := d.packageKey(info.OS, info.Arch, nodeProgram(info))
	r, ok := d.cachedPackage(k)
	if !ok {
		r.pkg, r.err = d.lookupShared(ctx, k)
	}
	if r.err != nil {
		return nodePackage{Version: d.hubVersion()}, !siteAway(r.err)
	}
	return r.pkg, true
}

// siteAway: err is the release site not answering (or the lookup cut short), not an answer that there is no build.
func siteAway(err error) bool {
	var msg *i18n.Message
	return errors.As(err, &msg) && msg.Key == "devices-deploy.package_unreachable"
}

// packageBlocked is why the node with info cannot be sent a build, as the last lookup found (nil: it can, or it is
// not known yet).
func (d *Devices) packageBlocked(info proto.NodeInfo) error {
	if info.OS == "" || info.Arch == "" {
		return nil
	}
	if r, _, ok := d.rememberedPackage(d.packageKey(info.OS, info.Arch, nodeProgram(info))); ok {
		return r.err // the last lookup's, also while a newer one runs (nodeTarget starts it)
	}
	return nil
}

// lookupPackage finds k's build (see the order above).
func (d *Devices) lookupPackage(ctx context.Context, k packageKey) (nodePackage, error) {
	if k.goos == "" || k.arch == "" {
		return nodePackage{}, i18n.Err("devices-deploy.os_unknown")
	}
	plat := k.goos + "-" + k.arch
	local := func(path, program string) nodePackage {
		// The hub's version, or what <name>.version beside it says (a build put there that is not the hub's own: a
		// release's, on a Docker hub without internet access, docs/operations/docker.md "Node builds").
		version := k.hub
		if b, err := os.ReadFile(path + ".version"); err == nil && strings.TrimSpace(string(b)) != "" && program == nodeproto.ProgramPersonad {
			version = strings.TrimSpace(string(b))
		}
		return nodePackage{Version: version, Program: program, goos: k.goos, arch: k.arch, path: path}
	}
	var personad string // the hub's own personad for the platform, if it has one
	for _, dir := range localNodeDirs() {
		if k.program == nodeproto.ProgramPersonaNode {
			// Only deploy.sh puts persona-node there, with the version it is in <name>.version: the hub's own updates
			// replace personad-* alone, so one left from an earlier deploy is another build than the hub's.
			node := filepath.Join(dir, nodeproto.ProgramPersonaNode+"-"+plat)
			if b, err := os.ReadFile(node + ".version"); err == nil && k.hub != "" && strings.TrimSpace(string(b)) == k.hub && fileExists(node) {
				return local(node, nodeproto.ProgramPersonaNode), nil
			}
		}
		if p := filepath.Join(dir, "personad-"+plat); personad == "" && fileExists(p) {
			personad = p
		}
	}
	if personad != "" && k.program != nodeproto.ProgramPersonaNode {
		return local(personad, nodeproto.ProgramPersonad), nil
	}
	if k.base == "" || k.hub == "dev" || !safeVersion.MatchString(k.hub) {
		// Nowhere to fetch from (a hub built without an update URL and no node_packages.url, or "off"; a dev build).
		if personad != "" {
			return local(personad, nodeproto.ProgramPersonad), nil // persona-node's node gets personad, which runs it
		}
		return nodePackage{}, i18n.Err("devices-deploy.no_binary", i18n.Args{"os": k.goos, "arch": k.arch, "name": "personad-" + plat})
	}
	m, err := d.fetchManifest(ctx, k.base+"/"+url.PathEscape(k.hub)+"/manifest.json")
	switch {
	case err == nil:
		if f, ok := m.NodeFile(k.program, plat); ok && fileFor(f, plat) && (personad == "" || fileProgram(f) == k.program) {
			return remotePackage(k, m, k.hub, f, false), nil
		}
	case !errors.Is(err, errNoManifest):
		// The site does not answer: what was fetched or found before, while it did, else the hub's own personad;
		// each stands in for a minute (stale), then the site is asked again.
		if pkg, ok := cachedRelease(k, k.hub); ok {
			return standIn(pkg), nil
		}
		if pkg, ok := d.lastFound(k); ok {
			return pkg, nil
		}
		if personad != "" {
			return standIn(local(personad, nodeproto.ProgramPersonad)), nil
		}
		if pkg, ok := cachedFallback(k); ok {
			return standIn(pkg), nil
		}
		return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": err.Error()})
	}
	if personad != "" {
		return local(personad, nodeproto.ProgramPersonad), nil
	}
	// No build of the hub's version anywhere: the newest release, when this manager drives its node.
	latest, err := d.fetchManifest(ctx, k.base+"/latest.json")
	if err != nil {
		if errors.Is(err, errNoManifest) {
			return nodePackage{}, i18n.Err("devices-deploy.no_release", i18n.Args{"os": k.goos, "arch": k.arch, "name": "personad-" + plat})
		}
		if pkg, ok := d.lastFound(k); ok {
			return pkg, nil
		}
		if pkg, ok := cachedFallback(k); ok {
			return standIn(pkg), nil
		}
		return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": err.Error()})
	}
	f, ok := latest.NodeFile(k.program, plat)
	if ok && fileFor(f, plat) && latest.Version == k.hub {
		// The newest release is the hub's own version, from a source that keeps no dl/<version>/ (a GitHub
		// repository's releases/latest/download): the release of the hub's version, not a fallback.
		pkg := remotePackage(k, latest, k.hub, f, false)
		pkg.flat = true
		return pkg, nil
	}
	why := releaseNodeIncompatible(latest)
	if why == "" && (!ok || !fileFor(f, plat) || !safeVersion.MatchString(latest.Version)) {
		why = "no build for " + plat
	}
	if why != "" {
		d.logf("node package for %s: the newest release %s does not do (%s)", plat, latest.Version, why)
		return nodePackage{}, i18n.Err("devices-deploy.no_package", i18n.Args{"os": k.goos, "arch": k.arch, "version": latest.Version,
			"name": "personad-" + plat})
	}
	pkg := remotePackage(k, latest, latest.Version, f, true)
	pkg.flat = true
	return pkg, nil
}

// releaseNodeIncompatible is why this manager would not drive the node of release m ("" when it would): its node
// protocol range and capabilities, judged as compatOf judges a connected node. A release that does not say
// (published before the fields) is not taken in another build's place.
func releaseNodeIncompatible(m *update.Manifest) string {
	if m.NodeProtocol == 0 {
		return "its node protocol is not known"
	}
	c := compatOf(proto.NodeInfo{Protocol: m.NodeProtocol, ProtocolMin: m.NodeProtocolMin, Capabilities: m.NodeCapabilities})
	switch c.Incompatible {
	case "":
		return ""
	case incompatibleNodeNew:
		return fmt.Sprintf("its node needs a manager of node protocol %d (this one speaks %d)", m.NodeProtocolMin, nodeproto.Version)
	default:
		return fmt.Sprintf("its node speaks node protocol %d (this manager needs %d) or lacks %v", m.NodeProtocol, nodeproto.MinVersion, c.Missing)
	}
}

// fileFor: f is named as a release names a node build for plat (personad-<plat>.gz, persona-node-<plat>.gz): its name
// goes into a URL and the cache's paths, so nothing else is taken.
func fileFor(f update.File, plat string) bool {
	return f.Name == "personad-"+plat+".gz" || f.Name == nodeproto.ProgramPersonaNode+"-"+plat+".gz"
}

// fileProgram is the kind of program a release download is, by its name.
func fileProgram(f update.File) string {
	if strings.HasPrefix(f.Name, nodeproto.ProgramPersonaNode+"-") {
		return nodeproto.ProgramPersonaNode
	}
	return nodeproto.ProgramPersonad
}

func remotePackage(k packageKey, m *update.Manifest, dir string, f update.File, fallback bool) nodePackage {
	return nodePackage{Version: m.Version, Program: fileProgram(f), Fallback: fallback, goos: k.goos, arch: k.arch,
		base: k.base, dir: dir, file: f, build: m.Build, nodeProtocol: m.NodeProtocol, nodeProtocolMin: m.NodeProtocolMin,
		nodeCapabilities: m.NodeCapabilities}
}

var errNoManifest = errors.New("no such release")

// siteError is a download that could not be made (as opposed to one that did not check out).
type siteError struct{ error }

func (e siteError) Unwrap() error { return e.error }

// redacted is err without the user and password that the release site's address (node_packages.url) may carry: its
// text goes to the machine's card (deploy_reason) and the log.
func redacted(err error, address string) error {
	u, perr := url.Parse(address)
	if err == nil || perr != nil || u.User == nil {
		return err
	}
	text := err.Error()
	for _, cred := range []string{u.User.String() + "@", u.User.Username() + ":***@", u.User.Username() + "@"} {
		text = strings.ReplaceAll(text, cred, "")
	}
	return errors.New(text)
}

// fetchManifest reads a release manifest (dl/<version>/manifest.json, dl/latest.json): errNoManifest when the site
// has none (404).
func (d *Devices) fetchManifest(ctx context.Context, u string) (*update.Manifest, error) {
	client, err := d.packageClient(packageLookup)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, packageLookup)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, redacted(err, u)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, redacted(err, u)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoManifest
	}
	if resp.StatusCode != http.StatusOK {
		return nil, redacted(fmt.Errorf("%s: HTTP %d", u, resp.StatusCode), u)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, redacted(fmt.Errorf("%s: larger than 1 MiB", u), u)
	}
	return update.ParseManifest(b)
}

// packageClient is the HTTP client for the release site: through node_packages.proxy, with timeout.
func (d *Devices) packageClient(timeout time.Duration) (*http.Client, error) {
	proxy, err := netproxy.Parse(d.config().NodePackagesProxy)
	if err != nil {
		return nil, fmt.Errorf("node_packages.proxy: %w", err)
	}
	if d.packages.httpClient != nil {
		return d.packages.httpClient(proxy, timeout), nil
	}
	return netproxy.HTTPClient(proxy, timeout), nil
}

// packageCacheEntry is what the cache keeps beside a build it fetched: what it is (written once it checked out).
type packageCacheEntry struct {
	Version      string `json:"version"`
	Program      string `json:"program"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	BinarySHA256 string `json:"binary_sha256"`
	Build        int64  `json:"build,omitempty"`
	// The release's node protocol range and capabilities, for a cached fallback judged again (releaseNodeIncompatible).
	NodeProtocol     int      `json:"node_protocol,omitempty"`
	NodeProtocolMin  int      `json:"node_protocol_min,omitempty"`
	NodeCapabilities []string `json:"node_capabilities,omitempty"`
}

// cacheNames are the cache's file names for k's builds, the ones of its own kind first: personad runs a persona-node
// device's file too.
func cacheNames(k packageKey) []string {
	plat := k.goos + "-" + k.arch
	names := []string{"personad-" + plat}
	if k.program == nodeproto.ProgramPersonaNode {
		names = append([]string{nodeproto.ProgramPersonaNode + "-" + plat}, names...)
	}
	return names
}

// readCacheEntry is the build the cache keeps as name in its version dir, if it is there whole and for k's platform.
func readCacheEntry(k packageKey, dir, name string) (nodePackage, packageCacheEntry, bool) {
	bin, entryPath := cachePaths(dir, update.File{Name: name + ".gz"})
	var e packageCacheEntry
	b, err := os.ReadFile(entryPath)
	if err != nil || json.Unmarshal(b, &e) != nil || e.OS != k.goos || e.Arch != k.arch || !fileExists(bin) {
		return nodePackage{}, e, false
	}
	return nodePackage{Version: e.Version, Program: e.Program, goos: k.goos, arch: k.arch, path: bin,
		file: update.File{Name: name + ".gz", BinarySHA256: e.BinarySHA256}, build: e.Build, nodeProtocol: e.NodeProtocol,
		nodeProtocolMin: e.NodeProtocolMin, nodeCapabilities: e.NodeCapabilities}, e, true
}

func cachePaths(dir string, f update.File) (bin, entry string) {
	bin = filepath.Join(identity.NodePackageCacheDir(), dir, strings.TrimSuffix(f.Name, ".gz"))
	return bin, bin + ".json"
}

// cachedRelease is a build of the release dir fetched before, for k's platform and kind, when the site does not
// answer now.
func cachedRelease(k packageKey, dir string) (nodePackage, bool) {
	for _, name := range cacheNames(k) {
		if pkg, _, ok := readCacheEntry(k, dir, name); ok {
			return pkg, true
		}
	}
	return nodePackage{}, false
}

// cachedFallback is the newest fallback build the cache keeps for k's platform and kind that this manager drives
// (judged again from what the cache kept of its release), when the site does not answer and nothing of the hub's
// version is there: what an earlier lookup found, also after a restart forgot it.
func cachedFallback(k packageKey) (nodePackage, bool) {
	dirs, err := os.ReadDir(identity.NodePackageCacheDir())
	if err != nil {
		return nodePackage{}, false
	}
	var best nodePackage
	found := false
	for _, name := range cacheNames(k) {
		for _, dir := range dirs {
			if !dir.IsDir() || !safeVersion.MatchString(dir.Name()) || dir.Name() == k.hub {
				continue
			}
			pkg, e, ok := readCacheEntry(k, dir.Name(), name)
			if !ok || e.NodeProtocol == 0 || releaseNodeIncompatible(&update.Manifest{NodeProtocol: e.NodeProtocol,
				NodeProtocolMin: e.NodeProtocolMin, NodeCapabilities: e.NodeCapabilities}) != "" {
				continue
			}
			if !found || pkg.build > best.build {
				best, found = pkg, true
			}
		}
		if found {
			best.Fallback = true
			return best, true
		}
	}
	return nodePackage{}, false
}

// fetchPackage is p as a file on this machine: its own, the cache's, or downloaded into the cache and checked.
func (d *Devices) fetchPackage(ctx context.Context, p nodePackage) (string, error) {
	if p.path != "" {
		if root := identity.NodePackageCacheDir(); strings.HasPrefix(p.path, root+string(filepath.Separator)) {
			now := d.packages.clock() // a cached build used now (a stand-in): pruneCache keeps its dir a while
			_ = os.Chtimes(filepath.Dir(p.path), now, now)
		}
		return p.path, nil
	}
	if p.file.Name == "" {
		return "", i18n.Err("devices-deploy.package_bad", i18n.Args{"os": p.goos, "arch": p.arch, "err": "no build to send"})
	}
	bin, entryPath := cachePaths(p.dir, p.file)
	// One download of a file at a time; other files go on.
	defer d.packages.lockFile(bin)()
	// Used now: pruneCache keeps the dir a while.
	defer func() {
		now := d.packages.clock()
		_ = os.Chtimes(filepath.Dir(bin), now, now)
	}()
	var e packageCacheEntry
	if b, err := os.ReadFile(entryPath); err == nil && json.Unmarshal(b, &e) == nil && e.BinarySHA256 == p.file.BinarySHA256 &&
		e.Version == p.Version && fileExists(bin) {
		return bin, nil
	}
	if err := d.downloadPackage(ctx, p, bin, entryPath); err != nil {
		var away siteError
		if errors.As(err, &away) {
			return "", i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": p.goos, "arch": p.arch, "err": redacted(err, p.base).Error()})
		}
		return "", i18n.Err("devices-deploy.package_bad", i18n.Args{"os": p.goos, "arch": p.arch, "err": redacted(err, p.base).Error()})
	}
	return bin, nil
}

// lockFile takes the download lock of the cache file bin; the function it returns gives it back.
func (p *nodePackages) lockFile(bin string) func() {
	p.mu.Lock()
	if p.fetching == nil {
		p.fetching = map[string]*sync.Mutex{}
	}
	l := p.fetching[bin]
	if l == nil {
		l = &sync.Mutex{}
		p.fetching[bin] = l
	}
	p.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// staleStage is how old a staging directory must be to be someone's interrupted download (a process killed while it
// downloaded, a blue-green switch): longer than any download takes.
const staleStage = 2 * packageDownload

// sweepStages removes the staging directories of interrupted downloads from the cache.
func sweepStages(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".fetch-") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > staleStage {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// cacheKeep is how long a cache version dir that is not the hub's version stays after its last use: a node of
// another platform may still be sent its build (a lookup is remembered ten minutes; a deployment fetches its build,
// then waits for the computer to be idle).
const cacheKeep = 24 * time.Hour

// pruneCache removes the cache's version dirs that no node is sent any more: all but the hub's version, keep (the
// build just fetched) and those used within cacheKeep (fetchPackage marks a dir used).
func pruneCache(root, hub, keep string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == hub || e.Name() == keep || !safeVersion.MatchString(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err != nil || now.Sub(fi.ModTime()) <= cacheKeep {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

func (d *Devices) downloadPackage(ctx context.Context, p nodePackage, bin, entryPath string) error {
	root := identity.NodePackageCacheDir()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	sweepStages(root, d.packages.clock())
	// Staged beside the cache's files (one file system: the rename is whole), never left behind.
	stage, err := os.MkdirTemp(root, ".fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	client, err := d.packageClient(packageDownload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, packageDownload)
	defer cancel()
	got, err := update.Download(ctx, client, p.base+"/"+url.PathEscape(p.dir), p.file, stage) // size, sha256, binary_sha256
	if p.flat && update.IsNotFound(err) {
		// No dl/<version>/ there (a GitHub release base): the file beside latest.json, checked against the same manifest.
		got, err = update.Download(ctx, client, p.base, p.file, stage)
	}
	if err != nil {
		var local *fs.PathError
		if update.IsMismatch(err) || errors.As(err, &local) {
			return err // what was served is not the build, or this machine could not keep it
		}
		return siteError{err} // the download could not be made: the site, the network, the proxy
	}
	if err := checkPackageBuild(ctx, got, p); err != nil {
		return err
	}
	if err := os.Chmod(got, 0o755); err != nil {
		return err
	}
	if err := os.Rename(got, bin); err != nil {
		return err
	}
	b, _ := json.Marshal(packageCacheEntry{Version: p.Version, Program: p.Program, OS: p.goos, Arch: p.arch,
		BinarySHA256: p.file.BinarySHA256, Build: p.build, NodeProtocol: p.nodeProtocol, NodeProtocolMin: p.nodeProtocolMin,
		NodeCapabilities: p.nodeCapabilities})
	tmp := entryPath + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, entryPath); err != nil {
		return err
	}
	pruneCache(root, d.hubVersion(), p.dir, d.packages.clock())
	return nil
}

// checkPackageBuild: the file is a build for p's OS and architecture of p's kind of program, and on this machine's
// own platform it runs and says the release's version and build.
func checkPackageBuild(ctx context.Context, path string, p nodePackage) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("not a Go executable: %w", err)
	}
	var goos, goarch string
	for _, s := range info.Settings {
		switch s.Key {
		case "GOOS":
			goos = s.Value
		case "GOARCH":
			goarch = s.Value
		}
	}
	if goos != p.goos || goarch != p.arch {
		return fmt.Errorf("built for %s/%s, not %s/%s", goos, goarch, p.goos, p.arch)
	}
	if !strings.HasSuffix(info.Path, "/cmd/"+p.Program) { // the main package: server/cmd/personad or cmd/persona-node
		return fmt.Errorf("it is %s, not %s", info.Path, p.Program)
	}
	if p.goos == runtime.GOOS && p.arch == runtime.GOARCH {
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
		return update.CheckBinary(ctx, path, &update.Manifest{Version: p.Version, Build: p.build})
	}
	return nil
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
