package devices

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

// nodeBinary is the build a hub of version sends a node of program on goos/arch from this machine's own builds, with
// no release to fetch from (what the deploy tests check): its path, or why none.
func nodeBinary(goos, arch, program, version string) (string, error) {
	d := New(Options{})
	d.cfg = devicewire.Config{HubVersion: version}
	pkg, err := d.lookupPackage(context.Background(), d.packageKey(goos, arch, program))
	return pkg.path, err
}

// The platform the fetched builds in these tests are for: nobody runs the tests there, so no build is run (the
// version -v check is for this machine's own platform only, TestFetchedBuildOfThisPlatformRuns).
const otherOS, otherArch = "linux", "riscv64"

// fakeRelease is a publisher's site: dl/<version>/manifest.json, dl/<version>/<name>.gz and dl/latest.json, with the
// paths asked for.
type fakeRelease struct {
	mu    sync.Mutex
	files map[string][]byte
	asked []string
	down  bool          // answers 503 to everything: the site is away
	delay time.Duration // before each answer
	srv   *httptest.Server
}

func newFakeRelease(t *testing.T) *fakeRelease {
	r := &fakeRelease{files: map[string][]byte{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, req)
			return
		}
		r.mu.Lock()
		r.asked = append(r.asked, req.URL.Path)
		b, ok := r.files[req.URL.Path]
		down, delay := r.down, r.delay
		r.mu.Unlock()
		time.Sleep(delay)
		if down {
			http.Error(w, "away", http.StatusServiceUnavailable)
			return
		}
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRelease) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// publish puts release m up at dl/<dir>/ (and as latest.json when latest) with builds: download name (no .gz) ->
// the binary, each listed in Files or Nodes by its name.
func (r *fakeRelease) publish(t *testing.T, dir string, m update.Manifest, latest bool, builds map[string]string) *update.Manifest {
	t.Helper()
	m.Files = map[string]update.File{}
	for name, bin := range builds {
		raw, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		outer, inner := sha256.Sum256(gz.Bytes()), sha256.Sum256(raw)
		f := update.File{Name: name + ".gz", Size: int64(gz.Len()), SHA256: hex.EncodeToString(outer[:]), BinarySHA256: hex.EncodeToString(inner[:])}
		if plat, ok := strings.CutPrefix(name, "persona-node-"); ok {
			if m.Nodes == nil {
				m.Nodes = map[string]map[string]update.File{"persona-node": {}}
			}
			m.Nodes["persona-node"][plat] = f
		} else {
			m.Files[strings.TrimPrefix(name, "personad-")] = f
		}
		r.mu.Lock()
		r.files["/"+dir+"/"+f.Name] = gz.Bytes()
		r.mu.Unlock()
	}
	b, _ := json.Marshal(m)
	r.mu.Lock()
	r.files["/"+dir+"/manifest.json"] = b
	if latest {
		r.files["/latest.json"] = b
	}
	r.mu.Unlock()
	return &m
}

// packageDevices is a manager whose hub is at version hub, fetching from site ("" none), with its own node binary
// dir, bundled dir and cache in this test's directories.
func packageDevices(t *testing.T, hub, site string) (*Devices, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	isolateNodeBinaries(t)
	bundled := t.TempDir()
	t.Setenv("PERSONA_NODE_BUNDLED", bundled)
	d := newTestDevices(t)
	d.mu.Lock()
	d.cfg.HubVersion, d.cfg.NodePackagesURL = hub, site
	d.mu.Unlock()
	return d, nodeBinaryDir(), bundled
}

// overlapping is a release whose node this manager drives.
func overlapping(version string, build int64) update.Manifest {
	return update.Manifest{Version: version, Build: build, NodeProtocol: nodeproto.Version, NodeProtocolMin: nodeproto.MinManagerVersion,
		NodeCapabilities: nodeproto.Announced}
}

// The hub's own builds come first and need no network; a persona-node device gets the release's persona-node when
// the hub has none of its own; with no build of the hub's version anywhere, the newest release this manager drives
// (the fallback), else a reason.
func TestNodePackageLookupOrder(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, "personad", otherOS, otherArch)
	node := updatetest.Program(t, "persona-node", otherOS, otherArch)
	copyTo := func(t *testing.T, from, to string) {
		b, _ := os.ReadFile(from)
		if err := os.WriteFile(to, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	t.Run("own builds", func(t *testing.T) {
		site := newFakeRelease(t)
		d, dir, bundled := packageDevices(t, "v2", site.srv.URL)
		copyTo(t, full, filepath.Join(bundled, "personad-"+plat)) // the image's own platform
		pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, nodeproto.ProgramPersonad)
		if err != nil || pkg.path != filepath.Join(bundled, "personad-"+plat) || pkg.Version != "v2" || pkg.Fallback {
			t.Fatalf("bundled: %+v %v", pkg, err)
		}
		copyTo(t, full, filepath.Join(dir, "personad-"+plat)) // the node binary dir goes first
		copyTo(t, node, filepath.Join(dir, "persona-node-"+plat))
		_ = os.WriteFile(filepath.Join(dir, "persona-node-"+plat+".version"), []byte("v2\n"), 0o644)
		if pkg, _ := d.nodePackageFor(ctx, otherOS, otherArch, ""); pkg.path != filepath.Join(dir, "personad-"+plat) {
			t.Fatalf("node binary dir: %+v", pkg)
		}
		if pkg, _ := d.nodePackageFor(ctx, otherOS, otherArch, nodeproto.ProgramPersonaNode); pkg.path != filepath.Join(dir, "persona-node-"+plat) || pkg.Program != nodeproto.ProgramPersonaNode {
			t.Fatalf("persona-node of the hub's version: %+v", pkg)
		}
		if got := site.requests(); len(got) != 0 {
			t.Fatalf("asked the release site for builds the hub has: %v", got)
		}
		// A build put there that is not the hub's own says which it is beside it (a release's, on a hub of an image).
		_ = os.WriteFile(filepath.Join(dir, "personad-"+plat+".version"), []byte("v1.2.3-202611060000\n"), 0o644)
		d.packages.results = nil
		if pkg, _ := d.nodePackageFor(ctx, otherOS, otherArch, ""); pkg.Version != "v1.2.3-202611060000" {
			t.Fatalf("a release's build put there: %+v", pkg)
		}
	})

	t.Run("persona-node from the release", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v2", overlapping("v2", 2), false, map[string]string{"personad-" + plat: full, "persona-node-" + plat: node})
		d, dir, _ := packageDevices(t, "v2", site.srv.URL)
		copyTo(t, full, filepath.Join(dir, "personad-"+plat)) // a release hub: personad-* only
		pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, nodeproto.ProgramPersonaNode)
		if err != nil || pkg.path != "" || pkg.Program != nodeproto.ProgramPersonaNode || pkg.Version != "v2" || pkg.Fallback {
			t.Fatalf("%+v %v", pkg, err)
		}
		bin, err := d.fetchPackage(ctx, pkg)
		if err != nil || update.ProgramOf(bin) != nodeproto.ProgramPersonaNode || !strings.HasPrefix(bin, identity.NodePackageCacheDir()) {
			t.Fatalf("fetched %s %v", bin, err)
		}
		// A personad node is sent the hub's own personad: no download for it.
		if pkg, _ := d.nodePackageFor(ctx, otherOS, otherArch, nodeproto.ProgramPersonad); pkg.path != filepath.Join(dir, "personad-"+plat) {
			t.Fatalf("personad: %+v", pkg)
		}
	})

	t.Run("fallback", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: full})
		d, _, _ := packageDevices(t, "1.2.3", site.srv.URL) // an image's version: no release of it
		pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, nodeproto.ProgramPersonaNode)
		if err != nil || !pkg.Fallback || pkg.Version != "v9-1" || pkg.Program != nodeproto.ProgramPersonad {
			t.Fatalf("%+v %v", pkg, err)
		}
		if got := site.requests(); len(got) != 2 || got[0] != "/1.2.3/manifest.json" || got[1] != "/latest.json" {
			t.Fatalf("asked %v", got)
		}
	})

	reason := func(t *testing.T, err error, key string, args i18n.Args) {
		t.Helper()
		if err == nil || err.Error() != i18n.T(i18n.Default, key, args) {
			t.Fatalf("%v, want %s", err, key)
		}
		for _, l := range []i18n.Lang{i18n.En, i18n.Ja} { // the hub says it in the app's language (DeploymentText)
			if got, want := i18n.DeploymentText(l, err.Error()), i18n.T(l, key, args); got != want || got == err.Error() {
				t.Errorf("%s: %q, want %q", l, got, want)
			}
		}
	}
	t.Run("no overlap", func(t *testing.T) {
		site := newFakeRelease(t)
		far := overlapping("v99-1", 99)
		far.NodeProtocol, far.NodeProtocolMin = nodeproto.Version+5, nodeproto.Version+5 // needs a newer manager
		site.publish(t, "v99-1", far, true, map[string]string{"personad-" + plat: full})
		d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
		_, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		reason(t, err, "devices-deploy.no_package", i18n.Args{"os": otherOS, "arch": otherArch, "version": "v99-1", "name": "personad-" + plat})
		unknown := overlapping("v98-1", 98)
		unknown.NodeProtocol = 0 // published before the fields: not taken in another build's place
		site.publish(t, "v98-1", unknown, true, map[string]string{"personad-" + plat: full})
		d.packages.results = nil
		_, err = d.nodePackageFor(ctx, otherOS, otherArch, "")
		reason(t, err, "devices-deploy.no_package", i18n.Args{"os": otherOS, "arch": otherArch, "version": "v98-1", "name": "personad-" + plat})
	})
	t.Run("no release", func(t *testing.T) {
		site := newFakeRelease(t)
		d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
		_, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		reason(t, err, "devices-deploy.no_release", i18n.Args{"os": otherOS, "arch": otherArch, "name": "personad-" + plat})
		d2, _, _ := packageDevices(t, "1.2.3", "") // no node_packages, no update URL
		_, err = d2.nodePackageFor(ctx, otherOS, otherArch, "")
		reason(t, err, "devices-deploy.no_binary", i18n.Args{"os": otherOS, "arch": otherArch, "name": "personad-" + plat})
	})
	t.Run("a name outside the release", func(t *testing.T) { // its name goes into a URL and the cache's paths
		site := newFakeRelease(t)
		m := site.publish(t, "v2", overlapping("v2", 2), true, map[string]string{"personad-" + plat: full})
		f := m.Files[plat]
		f.Name = "../../evil.gz"
		m.Files[plat] = f
		b, _ := json.Marshal(m)
		site.files["/v2/manifest.json"], site.files["/latest.json"] = b, b
		d, _, _ := packageDevices(t, "v2", site.srv.URL)
		_, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		reason(t, err, "devices-deploy.no_package", i18n.Args{"os": otherOS, "arch": otherArch, "version": "v2", "name": "personad-" + plat})
	})
	t.Run("unreachable", func(t *testing.T) {
		site := newFakeRelease(t)
		site.srv.Close()
		d, _, _ := packageDevices(t, "v2", site.srv.URL)
		_, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		if err == nil || !strings.Contains(err.Error(), otherOS+"/"+otherArch) || i18n.DeploymentText(i18n.En, err.Error()) == err.Error() {
			t.Fatalf("%v", err)
		}
	})
}

// flatten moves release dir's builds beside latest.json and drops dl/<dir>/: a source with no per-version directories,
// as a GitHub repository's releases/latest/download serves a release.
func (r *fakeRelease) flatten(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for path, b := range r.files {
		if name, ok := strings.CutPrefix(path, "/"+dir+"/"); ok {
			delete(r.files, path)
			if name != "manifest.json" {
				r.files["/"+name] = b
			}
		}
	}
}

// A source that keeps no dl/<version>/ (a GitHub release base) serves latest.json's builds beside it: a release of
// the hub's own version found there is the hub's build (no fallback, so automatic node updates send it), and a newer
// release is the fallback; both download from beside latest.json once dl/<version>/ answers 404. A source that has
// dl/<version>/ is still asked there only.
func TestNodePackagesFromAFlatReleaseSource(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, "personad", otherOS, otherArch)
	ctx := context.Background()
	fetch := func(t *testing.T, d *Devices, pkg nodePackage) {
		t.Helper()
		bin, err := d.fetchPackage(ctx, pkg)
		if err != nil || update.ProgramOf(bin) != nodeproto.ProgramPersonad || !strings.HasPrefix(bin, identity.NodePackageCacheDir()) {
			t.Fatalf("fetched %s %v", bin, err)
		}
	}

	t.Run("the hub's own version", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v2", overlapping("v2", 2), true, map[string]string{"personad-" + plat: full})
		site.flatten("v2")
		d, _, _ := packageDevices(t, "v2", site.srv.URL)
		pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		if err != nil || pkg.Fallback || pkg.Version != "v2" || pkg.path != "" {
			t.Fatalf("%+v %v", pkg, err)
		}
		fetch(t, d, pkg)
		// dl/v2/ first (under each name a release may give the build), then beside latest.json.
		got := site.requests()
		if len(got) < 4 || got[0] != "/v2/manifest.json" || got[1] != "/latest.json" || got[2] != "/v2/personad-"+plat+".gz" ||
			got[len(got)-1] != "/personad-"+plat+".gz" {
			t.Fatalf("asked %v", got)
		}
		// The cache serves it as the release of the hub's version while the site is away.
		site.mu.Lock()
		site.down = true
		site.mu.Unlock()
		d.packages.results = nil
		if pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, ""); err != nil || pkg.Fallback || pkg.Version != "v2" || pkg.path == "" {
			t.Fatalf("cached: %+v %v", pkg, err)
		}
	})

	t.Run("a newer release", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: full})
		site.flatten("v9-1")
		d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
		pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		if err != nil || !pkg.Fallback || pkg.Version != "v9-1" {
			t.Fatalf("%+v %v", pkg, err)
		}
		fetch(t, d, pkg)
	})

	t.Run("a site with dl/<version>/", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: full})
		d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
		pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		if err != nil || !pkg.Fallback {
			t.Fatalf("%+v %v", pkg, err)
		}
		fetch(t, d, pkg)
		for _, path := range site.requests() {
			if path == "/personad-"+plat+".gz" {
				t.Fatalf("asked beside latest.json while dl/v9-1/ has it: %v", site.requests())
			}
		}
	})
}

// A build from the release is used only once it matches the manifest and is a build of the right platform and kind;
// it lands in the cache whole (nothing half-written or staged is left), and the cache serves it again without asking.
func TestFetchedBuildIsChecked(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, "personad", otherOS, otherArch)
	node := updatetest.Program(t, "persona-node", otherOS, otherArch)
	mac := updatetest.Program(t, "personad", "darwin", "arm64")
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		build string                      // what is served as personad-<plat>
		bend  func(f *update.File)        // what the manifest says of it
		check func(t *testing.T, e error) // nil: it must work
	}{
		{"good", full, nil, nil},
		{"size", full, func(f *update.File) { f.Size++ }, nil},
		{"sha256", full, func(f *update.File) { f.SHA256 = strings.Repeat("0", 64) }, nil},
		{"binary_sha256", full, func(f *update.File) { f.BinarySHA256 = strings.Repeat("0", 64) }, nil},
		{"platform", mac, nil, nil},
		{"kind", node, nil, nil}, // persona-node served as personad: not what the name says
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := newFakeRelease(t)
			m := site.publish(t, "v2", overlapping("v2", 2), false, map[string]string{"personad-" + plat: tc.build})
			if tc.bend != nil {
				f := m.Files[plat]
				tc.bend(&f)
				m.Files[plat] = f
				b, _ := json.Marshal(m)
				site.files["/v2/manifest.json"] = b
			}
			d, _, _ := packageDevices(t, "v2", site.srv.URL)
			pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
			if err != nil {
				t.Fatal(err)
			}
			bin, err := d.fetchPackage(ctx, pkg)
			var left []string
			_ = filepath.Walk(identity.NodePackageCacheDir(), func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					left = append(left, strings.TrimPrefix(p, identity.NodePackageCacheDir()))
				}
				return nil
			})
			if tc.name != "good" {
				if err == nil || !strings.Contains(i18n.DeploymentText(i18n.En, err.Error()), "did not check out") {
					t.Fatalf("taken: %s %v", bin, err)
				}
				if len(left) != 0 {
					t.Fatalf("left in the cache: %v", left)
				}
				return
			}
			if err != nil || update.ProgramOf(bin) != nodeproto.ProgramPersonad {
				t.Fatalf("%s %v", bin, err)
			}
			if len(left) != 2 { // the build and what it is
				t.Fatalf("cache: %v", left)
			}
			asked := len(site.requests())
			if again, err := d.fetchPackage(ctx, pkg); err != nil || again != bin || len(site.requests()) != asked {
				t.Fatalf("not from the cache: %s %v, %d requests more", again, err, len(site.requests())-asked)
			}
			// The site away: the build fetched before still serves the hub's version.
			site.srv.Close()
			d.packages.results = nil
			if pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, ""); err != nil || pkg.path != bin {
				t.Fatalf("cached while the site is away: %+v %v", pkg, err)
			}
		})
	}
}

// A build for this machine's own platform must also run and say the release's version and build (as hub self-update
// checks its own).
func TestFetchedBuildOfThisPlatformRuns(t *testing.T) {
	plat := runtime.GOOS + "-" + runtime.GOARCH
	says := func(version string) string {
		return updatetest.ProgramSource(t, "personad", "", "", "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n"+
			"\tif len(os.Args) > 2 && os.Args[1] == \"version\" && os.Args[2] == \"-v\" {\n\t\tfmt.Println(\""+version+"\")\n"+
			"\t\tfmt.Println(\"channel release, build 7\")\n\t}\n}\n")
	}
	for _, tc := range []struct {
		says string
		ok   bool
	}{{"v7-1", true}, {"v6-1", false}} {
		site := newFakeRelease(t)
		site.publish(t, "v7-1", overlapping("v7-1", 7), false, map[string]string{"personad-" + plat: says(tc.says)})
		d, _, _ := packageDevices(t, "v7-1", site.srv.URL)
		pkg, err := d.nodePackageFor(context.Background(), runtime.GOOS, runtime.GOARCH, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.fetchPackage(context.Background(), pkg)
		if (err == nil) != tc.ok {
			t.Errorf("a build that says %s: %v", tc.says, err)
		}
	}
}

// Fetches go through node_packages.proxy (internal/netproxy, never the environment's), with a timeout; a proxy
// that does not read stops them.
func TestNodePackagesProxyAndTimeout(t *testing.T) {
	site := newFakeRelease(t)
	d, _, _ := packageDevices(t, "v2", site.srv.URL)
	type use struct {
		proxy   string
		timeout time.Duration
	}
	var uses []use
	d.packages.httpClient = func(proxy *url.URL, timeout time.Duration) *http.Client {
		p := ""
		if proxy != nil {
			p = proxy.Host
		}
		uses = append(uses, use{p, timeout})
		return http.DefaultClient
	}
	d.mu.Lock()
	d.cfg.NodePackagesProxy = "http://[fd00::20]:3128"
	d.mu.Unlock()
	_, _ = d.nodePackageFor(context.Background(), otherOS, otherArch, "")
	if len(uses) == 0 || uses[0].proxy != "[fd00::20]:3128" || uses[0].timeout != packageLookup {
		t.Fatalf("uses %+v", uses)
	}
	d.packages.httpClient = nil
	c, err := d.packageClient(packageDownload)
	if err != nil || c.Timeout != packageDownload {
		t.Fatalf("netproxy client: %+v %v", c, err)
	}
	d.mu.Lock()
	d.cfg.NodePackagesProxy = "socks5://[fd00::20]:1080"
	d.mu.Unlock()
	if _, err := d.packageClient(packageLookup); err == nil || !strings.Contains(err.Error(), "node_packages.proxy") {
		t.Fatalf("a bad proxy: %v", err)
	}
}

// A node on the fallback build is up to date: no mismatch, no redeploy on connect, the card shows the build it runs.
// One on another build is offered the fallback; with no build at all, Redeploy is not offered and the card says why.
func TestNodeOnTheFallbackBuildIsUpToDate(t *testing.T) {
	plat := otherOS + "-" + otherArch
	site := newFakeRelease(t)
	site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", otherOS, otherArch)})
	d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	d.addEntry(m)
	m.mu.Lock()
	m.online = true
	m.info = proto.NodeInfo{OS: otherOS, Arch: otherArch, Version: "v9-1", Protocol: nodeproto.Version,
		ProtocolMin: nodeproto.MinManagerVersion, Capabilities: nodeproto.Announced}
	m.mu.Unlock()
	if got, _ := d.resolvedTargetKnown(context.Background(), m.info); got != "v9-1" {
		t.Fatalf("target %s", got)
	}
	if s := m.snapshot(); s.NodeMismatch || s.NodeVersion != "v9-1" || s.CanDeploy == nil || !*s.CanDeploy {
		t.Fatalf("on the fallback: %+v", s)
	}
	m.updateOnConnect(context.Background())
	if s := m.snapshot(); s.Deploying {
		t.Fatal("redeployed a node on the fallback build")
	}
	m.mu.Lock()
	m.info.Version = "v8-1"
	m.mu.Unlock()
	if s := m.snapshot(); !s.NodeMismatch {
		t.Fatalf("another build: %+v", s)
	}

	// No build for its platform anywhere: not offered, and the card says why.
	m.mu.Lock()
	m.info.Arch = "mips"
	m.mu.Unlock()
	_, _ = d.resolvedTargetKnown(context.Background(), m.info)
	s := m.snapshot()
	want := i18n.T(i18n.Default, "devices-deploy.no_package", i18n.Args{"os": otherOS, "arch": "mips", "version": "v9-1",
		"name": "personad-" + otherOS + "-mips"})
	if s.CanDeploy == nil || *s.CanDeploy || s.DeployReason != want {
		t.Fatalf("no build: %v %q", s.CanDeploy, s.DeployReason)
	}
	if err := m.startDeploy(); err == nil || err.Error() != want {
		t.Fatalf("Redeploy anyway: %v", err)
	}
}

func (r *fakeRelease) setDown(down bool) {
	r.mu.Lock()
	r.down = down
	r.mu.Unlock()
}

// While the release site is away, what was found before stands: a node on the fallback build stays up to date and can
// be sent the cached build; after a restart (nothing remembered) the cache's newest fallback this manager drives is
// used, and one it does not drive is not.
func TestTheSiteAwayKeepsWhatWasFound(t *testing.T) {
	plat := otherOS + "-" + otherArch
	site := newFakeRelease(t)
	site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", otherOS, otherArch)})
	d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
	now := time.Now()
	d.packages.now = func() time.Time { return now }
	ctx := context.Background()
	info := proto.NodeInfo{OS: otherOS, Arch: otherArch, Version: "v9-1"}
	pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
	if err != nil || !pkg.Fallback || pkg.Version != "v9-1" {
		t.Fatalf("%+v %v", pkg, err)
	}
	if _, err := d.fetchPackage(ctx, pkg); err != nil {
		t.Fatal(err)
	}

	site.setDown(true)
	now = now.Add(packageFoundTTL + time.Second) // what was found is old now: looked up again, and the site is away
	if v, known := d.resolvedTargetKnown(ctx, info); v != "v9-1" || !known {
		t.Fatalf("target while the site is away: %s %v", v, known)
	}
	if err := d.packageBlocked(info); err != nil {
		t.Fatalf("blocked while the site is away: %v", err)
	}
	pkg, err = d.nodePackageFor(ctx, otherOS, otherArch, "")
	if err != nil || pkg.Version != "v9-1" {
		t.Fatalf("Redeploy while the site is away: %+v %v", pkg, err)
	}
	if bin, err := d.fetchPackage(ctx, pkg); err != nil || update.ProgramOf(bin) != nodeproto.ProgramPersonad {
		t.Fatalf("the cached build: %s %v", bin, err)
	}
	// The stand-in is looked up again soon, not in ten minutes.
	asked := len(site.requests())
	now = now.Add(packageMissTTL + time.Second)
	_, _ = d.resolvedTargetKnown(ctx, info)
	if len(site.requests()) == asked {
		t.Fatal("the site was not asked again once it may be back")
	}

	// A manager that started since (nothing remembered): the cache's fallback, judged again.
	e := newTestDevices(t)
	e.mu.Lock()
	e.cfg.HubVersion, e.cfg.NodePackagesURL = "1.2.3", site.srv.URL
	e.mu.Unlock()
	pkg, err = e.nodePackageFor(ctx, otherOS, otherArch, "")
	if err != nil || pkg.Version != "v9-1" || !pkg.Fallback || pkg.path == "" {
		t.Fatalf("after a restart: %+v %v", pkg, err)
	}
	_, entry := cachePaths("v9-1", update.File{Name: "personad-" + plat + ".gz"})
	var ce packageCacheEntry
	b, _ := os.ReadFile(entry)
	_ = json.Unmarshal(b, &ce)
	ce.NodeProtocol, ce.NodeProtocolMin = nodeproto.MinVersion-1, nodeproto.MinVersion-1 // a node this manager does not drive
	b, _ = json.Marshal(ce)
	_ = os.WriteFile(entry, b, 0o644)
	f := newTestDevices(t)
	f.mu.Lock()
	f.cfg.HubVersion, f.cfg.NodePackagesURL = "1.2.3", site.srv.URL
	f.mu.Unlock()
	if _, err = f.nodePackageFor(ctx, otherOS, otherArch, ""); !siteAway(err) {
		t.Fatalf("a cached build this manager does not drive: %v", err)
	}
}

// A queued update over the link waits out a release site that is away instead of failing: a node that runs what it
// was sent is confirmed (not left to roll a working update back), and one that does not is left for later.
func TestQueuedUpdateWaitsOutTheSite(t *testing.T) {
	site := newFakeRelease(t)
	site.setDown(true)
	isolateNodeBinaries(t) // nothing of the hub's own for the platform, and the cache in this test's directory
	h, m, n, _ := linkedDeployFixture(t, kindService)
	h.opts.Store = newMemStore()
	h.mu.Lock()
	h.cfg.NodePackagesURL = site.srv.URL // no build of its own for the platform: the site decides, and it is away
	h.mu.Unlock()
	ctx := context.Background()

	queued := nodeDeployRecord{Target: "v9-1", State: "queued", Phase: "waiting_idle"}
	if err := m.saveDeployRecord(ctx, queued); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(ctx, m.peer, m.info)
	var saved nodeDeployRecord
	if _, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved); err != nil || saved.State != "queued" || m.snapshot().DeployError != "" {
		t.Fatalf("failed because the site is away: %+v %q", saved, m.snapshot().DeployError)
	}

	m.mu.Lock()
	m.info.Version = "v9-1"
	m.mu.Unlock()
	n.mu.Lock()
	n.target = "v9-1" // a fallback build, not the hub's
	n.mu.Unlock()
	if err := m.saveDeployRecord(ctx, nodeDeployRecord{Target: "v9-1", State: "queued", Phase: "confirming"}); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(ctx, m.peer, m.info)
	waitMachine(t, m, 5*time.Second, func(s proto.Machine) bool { return !s.Deploying })
	if _, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved); err != nil || saved.State != "succeeded" {
		t.Fatalf("record=%+v err=%v", saved, err)
	}
	n.mu.Lock()
	if n.begin != 0 || n.confirms < 1 {
		n.mu.Unlock()
		t.Fatalf("begin=%d confirms=%d", n.begin, n.confirms)
	}
	n.mu.Unlock()

	// The same after the connection's build check: updateOnConnect does not record the hub's version as failed over a
	// queued update while the site is away.
	m.mu.Lock()
	m.info.Version = "v8-1"
	m.mu.Unlock()
	if err := m.saveDeployRecord(ctx, queued); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(ctx, m.peer, m.info)
	m.updateOnConnect(ctx)
	if _, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved); err != nil || saved.State != "queued" || saved.Target != "v9-1" ||
		m.snapshot().DeployError != "" || m.snapshot().Deploying {
		t.Fatalf("after the build check: %+v %q", saved, m.snapshot().DeployError)
	}

	// Nor does the update after the hub's, for this machine online or as an offline one: no record is made or replaced.
	d := m.d
	d.updateNodes(ctx)
	if err := m.queueOfflineNodeUpdate(ctx); err == nil {
		t.Fatal("queued a guessed target while the site is away")
	}
	if _, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved); err != nil || saved.State != "queued" || saved.Target != "v9-1" || m.snapshot().Deploying {
		t.Fatalf("after the hub's update: %+v", saved)
	}

	// A target that moved on (a newer fallback) while the node installed: what it runs is confirmed all the same. The
	// site answers again, so the target is known now: v10-1.
	plat := "linux-amd64"
	site.publish(t, "v10-1", overlapping("v10-1", 10), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", "linux", "amd64")})
	site.setDown(false)
	h.packages.mu.Lock()
	h.packages.results = nil
	h.packages.mu.Unlock()
	if v, known := h.resolvedTargetKnown(ctx, proto.NodeInfo{OS: "linux", Arch: "amd64"}); v != "v10-1" || !known {
		t.Fatalf("the moved target: %s %v", v, known)
	}
	m.mu.Lock()
	m.info.Version = "v9-1"
	m.mu.Unlock()
	if err := m.saveDeployRecord(ctx, nodeDeployRecord{Target: "v9-1", State: "queued", Phase: "confirming"}); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(ctx, m.peer, m.info)
	waitMachine(t, m, 5*time.Second, func(s proto.Machine) bool { return !s.Deploying })
	if _, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved); err != nil || saved.State != "succeeded" {
		t.Fatalf("a moved target: record=%+v err=%v", saved, err)
	}
}

// A remembered build stands while it is looked up again: a node on it does not show out of date in between, also
// when the site is slow to answer.
func TestATargetStandsWhileItIsLookedUpAgain(t *testing.T) {
	plat := otherOS + "-" + otherArch
	site := newFakeRelease(t)
	site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", otherOS, otherArch)})
	d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
	now := time.Now()
	d.packages.now = func() time.Time { return now }
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	d.addEntry(m)
	m.mu.Lock()
	m.online = true
	m.info = proto.NodeInfo{OS: otherOS, Arch: otherArch, Version: "v9-1"}
	m.mu.Unlock()
	if got, _ := d.resolvedTargetKnown(context.Background(), m.info); got != "v9-1" {
		t.Fatalf("target %s", got)
	}
	site.mu.Lock()
	site.delay = 2 * time.Second
	site.mu.Unlock()
	now = now.Add(packageFoundTTL + time.Second)
	for i := 0; i < 3; i++ {
		if s := m.snapshot(); s.NodeMismatch || s.CanDeploy == nil || !*s.CanDeploy {
			t.Fatalf("while it is looked up again: %+v", s)
		}
	}
}

// The machines of one platform look its build up once between them, not once each.
func TestLookupsAreShared(t *testing.T) {
	plat := otherOS + "-" + otherArch
	site := newFakeRelease(t)
	site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", otherOS, otherArch)})
	site.mu.Lock()
	site.delay = 100 * time.Millisecond
	site.mu.Unlock()
	d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pkg, err := d.nodePackageFor(context.Background(), otherOS, otherArch, ""); err != nil || pkg.Version != "v9-1" {
				t.Errorf("%+v %v", pkg, err)
			}
		}()
	}
	wg.Wait()
	count := map[string]int{}
	for _, p := range site.requests() {
		count[p]++
	}
	if count["/1.2.3/manifest.json"] != 1 || count["/latest.json"] != 1 {
		t.Fatalf("asked %v", count)
	}
}

// A user and password in node_packages.url never reach the machine's card or the log.
func TestSiteErrorsHideTheCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "broken", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	u.User = url.UserPassword("mirror-user", "mirror-secret")
	d, _, _ := packageDevices(t, "1.2.3", u.String())
	_, err := d.nodePackageFor(context.Background(), otherOS, otherArch, "")
	text := i18n.DeploymentText(i18n.En, err.Error())
	if err == nil || strings.Contains(text, "mirror-secret") || strings.Contains(text, "mirror-user") || !strings.Contains(text, "HTTP 500") {
		t.Fatalf("%v", text)
	}
}

// The cache keeps the hub's version, the build just fetched and what was used within a day (a node of another
// platform may still be sent it); staging directories of interrupted downloads go, a running one stays.
func TestCacheIsPruned(t *testing.T) {
	plat := otherOS + "-" + otherArch
	site := newFakeRelease(t)
	site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", otherOS, otherArch)})
	d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
	root := identity.NodePackageCacheDir()
	for _, dir := range []string{"v7-1", "v8-1", "1.2.3", ".fetch-old", ".fetch-running"} {
		_ = os.MkdirAll(filepath.Join(root, dir), 0o755)
		_ = os.WriteFile(filepath.Join(root, dir, "x"), []byte("x"), 0o644)
	}
	old := time.Now().Add(-3 * packageDownload)
	_ = os.Chtimes(filepath.Join(root, ".fetch-old"), old, old)
	unused := time.Now().Add(-cacheKeep - time.Hour)
	_ = os.Chtimes(filepath.Join(root, "v7-1"), unused, unused)
	_ = os.Chtimes(filepath.Join(root, "1.2.3"), unused, unused)
	pkg, err := d.nodePackageFor(context.Background(), otherOS, otherArch, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.fetchPackage(context.Background(), pkg); err != nil {
		t.Fatal(err)
	}
	var left []string
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, " ") != ".fetch-running 1.2.3 v8-1 v9-1" {
		t.Fatalf("cache: %v", left)
	}
}

// Only a build of the hub's own version (the hub's, or its release's) goes out by itself, on connect and after the
// hub's update; a fallback build (another version) is offered and sent only on an explicit Redeploy, and an offline
// linked machine gets no queued fallback.
func TestOnlyTheHubsVersionDeploysByItself(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, "personad", otherOS, otherArch)
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		hubBuild bool // the release of the hub's version has the platform's build
	}{{"fallback", false}, {"the hub's version", true}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeSSH(t)
			site := newFakeRelease(t)
			site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: full})
			if tc.hubBuild {
				site.publish(t, "1.2.3", overlapping("1.2.3", 8), false, map[string]string{"personad-" + plat: full})
			}
			d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
			d.opts.Store = newMemStore()
			d.cfg.AutoUpdateNodes = true
			m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh", SSHHost: "box.invalid"})
			d.addEntry(m)
			m.mu.Lock()
			m.online = true
			m.info = proto.NodeInfo{OS: otherOS, Arch: otherArch, Version: "v8-1", Protocol: nodeproto.Version,
				ProtocolMin: nodeproto.MinManagerVersion, Capabilities: nodeproto.Announced}
			m.mu.Unlock()
			_, _ = d.resolvedTargetKnown(ctx, m.info)
			if s := m.snapshot(); !s.NodeMismatch || s.CanDeploy == nil || !*s.CanDeploy {
				t.Fatalf("another build is not offered an update: %+v", s)
			}
			m.updateOnConnect(ctx)
			d.updateNodes(ctx)
			var record nodeDeployRecord
			if !tc.hubBuild {
				deploying := m.snapshot().Deploying
				time.Sleep(500 * time.Millisecond) // a deploy started would have tried ssh and kept its outcome by now
				found, _ := d.opts.Store.Setting(ctx, m.deployRecordKey(), &record)
				if deploying || found || len(sshCalls(t, calls)) != 0 || m.snapshot().DeployError != "" {
					t.Fatalf("a fallback went out by itself: record %v %+v, ssh %v, %q", found, record, sshCalls(t, calls), m.snapshot().DeployError)
				}
				if err := m.startAutoDeploy(); !errors.Is(err, errFallbackOnRedeploy) {
					t.Fatalf("automatic deploy of a fallback: %v", err)
				}
				// Offline over a link after the hub's update: no fallback queued for its next connection.
				l := newMachine(d, MachineConfig{ID: "far", Name: "far", Kind: kindLink})
				l.info = m.info
				d.addEntry(l)
				if err := l.queueOfflineNodeUpdate(ctx); err != nil {
					t.Fatal(err)
				}
				if found, _ := d.opts.Store.Setting(ctx, l.deployRecordKey(), &record); found {
					t.Fatalf("queued a fallback: %+v", record)
				}
				// Redeploy is the explicit choice: it goes.
				if err := m.startDeploy(); err != nil {
					t.Fatalf("Redeploy of a fallback: %v", err)
				}
				eventually(t, 10*time.Second, "the fallback sent on Redeploy", func() bool { return len(sshCalls(t, calls)) > 0 })
				return
			}
			eventually(t, 10*time.Second, "the hub's version sent by itself", func() bool { return len(sshCalls(t, calls)) > 0 })
			eventually(t, 10*time.Second, "the attempt kept", func() bool {
				found, _ := d.opts.Store.Setting(ctx, m.deployRecordKey(), &record)
				return found && record.Target == "1.2.3"
			})
		})
	}
}

// A record queued for the hub's own version does not resume as a fallback when the hub moved to a version without a
// release of its own and the old target is now the newest release: it fails, as the hub's version changed. One made
// by an explicit Redeploy of that fallback resumes.
func TestQueuedRecordNeverResumesAsAFallback(t *testing.T) {
	plat := otherOS + "-" + otherArch
	for _, manual := range []bool{false, true} {
		isolateNodeBinaries(t)
		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", otherOS, otherArch)})
		h, m, n, _ := linkedDeployFixture(t, kindLink)
		h.opts.Store = newMemStore()
		h.mu.Lock()
		h.cfg.HubVersion, h.cfg.NodePackagesURL = "edge-2", site.srv.URL // was v9-1 when the update was queued
		h.mu.Unlock()
		m.mu.Lock()
		m.info.OS, m.info.Arch = otherOS, otherArch
		m.mu.Unlock()
		ctx := context.Background()
		if err := m.saveDeployRecord(ctx, nodeDeployRecord{Target: "v9-1", State: "queued", Phase: "waiting_idle", Fallback: manual}); err != nil {
			t.Fatal(err)
		}
		m.nodeUpdateConnected(ctx, m.peer, m.info)
		if !manual {
			var saved nodeDeployRecord
			_, _ = h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved)
			n.mu.Lock()
			begun := n.begin
			n.mu.Unlock()
			if saved.State != "failed" || begun != 0 || !strings.Contains(m.snapshot().DeployError, "hub 版本已变化") {
				t.Fatalf("a queued update resumed as a fallback: %+v begin=%d %q", saved, begun, m.snapshot().DeployError)
			}
			continue
		}
		// It goes on to the node: the fixture's node refuses this build's metadata (it expects its own bytes), which
		// is as far as the decision matters.
		eventually(t, 10*time.Second, "the Redeploy of the fallback resumed", func() bool {
			return strings.Contains(m.snapshot().DeployError, "wrong update metadata")
		})
	}
}

// A download that cannot be made (the site away) says so, not that the build did not check out.
func TestADownloadThatCannotBeMadeSaysTheSiteIsAway(t *testing.T) {
	plat := otherOS + "-" + otherArch
	site := newFakeRelease(t)
	site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: updatetest.Program(t, "personad", otherOS, otherArch)})
	d, _, _ := packageDevices(t, "1.2.3", site.srv.URL)
	pkg, err := d.nodePackageFor(context.Background(), otherOS, otherArch, "")
	if err != nil {
		t.Fatal(err)
	}
	site.setDown(true)
	if _, err := d.fetchPackage(context.Background(), pkg); !siteAway(err) {
		t.Fatalf("%v", err)
	}
}
