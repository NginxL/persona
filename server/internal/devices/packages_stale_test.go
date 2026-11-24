package devices

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

// A build left in the node binary dir without a version file passes for the hub's own version. After a hub update
// that did not replace it (a Windows build next to a release published without one, a build a deploy had not replaced
// yet), Redeploy sent the old build as the hub's version, and every node refused it at its self-check (#12958). Such a
// build is taken only when it can be the hub's version; otherwise the release of the hub's version is fetched.
func TestStaleLocalNodeBuildIsNotTakenForTheHubs(t *testing.T) {
	const goos, arch = "windows", "amd64"
	const hub, old = "0.9.2-202611221500", "0.9.1-202611190505"
	plat := goos + "-" + arch
	stale := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, old)
	current := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, hub)
	ctx := context.Background()

	t.Run("an older build is passed over for the release", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, hub, overlapping(hub, 202611221500), false, map[string]string{"personad-" + plat + ".exe": current})
		d, dir, _ := packageDevices(t, hub, site.srv.URL)
		copyLocalNodeBuild(t, stale, filepath.Join(dir, "personad-"+plat+".exe"))
		pkg, err := d.nodePackageFor(ctx, goos, arch, nodeproto.ProgramPersonad)
		if err != nil || pkg.path != "" || pkg.Version != hub || pkg.Fallback || pkg.file.Name != "personad-"+plat+".exe.gz" {
			t.Fatalf("the stale local build was taken for %s: %+v %v", hub, pkg, err)
		}
	})

	t.Run("the hub's own build is taken, and seen at once", func(t *testing.T) {
		site := newFakeRelease(t) // no release of the hub's version: only the local build can do
		d, dir, _ := packageDevices(t, hub, site.srv.URL)
		local := filepath.Join(dir, "personad-"+plat+".exe")
		copyLocalNodeBuild(t, stale, local)
		if pkg, err := d.nodePackageFor(ctx, goos, arch, nodeproto.ProgramPersonad); err == nil && pkg.path == local {
			t.Fatalf("the stale build was taken: %+v", pkg)
		}
		// The deploy puts the hub's build in place: the lookup remembered a moment ago does not stand for ten minutes.
		copyLocalNodeBuild(t, current, local)
		pkg, err := d.nodePackageFor(ctx, goos, arch, nodeproto.ProgramPersonad)
		if err != nil || pkg.path != local || pkg.Version != hub {
			t.Fatalf("the hub's build put in place: %+v %v", pkg, err)
		}
	})

	t.Run("a build named without .exe", func(t *testing.T) {
		d, dir, _ := packageDevices(t, hub, "")
		legacy := filepath.Join(dir, "personad-"+plat)
		copyLocalNodeBuild(t, stale, legacy)
		if pkg, err := d.nodePackageFor(ctx, goos, arch, nodeproto.ProgramPersonad); err == nil {
			t.Fatalf("a stale build without .exe was taken: %+v", pkg)
		}
		copyLocalNodeBuild(t, current, legacy)
		pkg, err := d.nodePackageFor(ctx, goos, arch, nodeproto.ProgramPersonad)
		if err != nil || pkg.path != legacy || pkg.Version != hub {
			t.Fatalf("the hub's build without .exe: %+v %v", pkg, err)
		}
		// The current name goes first.
		named := filepath.Join(dir, "personad-"+plat+".exe")
		copyLocalNodeBuild(t, current, named)
		if pkg, err := d.nodePackageFor(ctx, goos, arch, nodeproto.ProgramPersonad); err != nil || pkg.path != named {
			t.Fatalf("with both names: %+v %v", pkg, err)
		}
	})

	t.Run("a version file still says which build it is", func(t *testing.T) {
		d, dir, _ := packageDevices(t, hub, "")
		local := filepath.Join(dir, "personad-"+plat+".exe")
		copyLocalNodeBuild(t, stale, local)
		if err := os.WriteFile(local+".version", []byte(old+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pkg, err := d.nodePackageFor(ctx, goos, arch, nodeproto.ProgramPersonad)
		if err != nil || pkg.path != local || pkg.Version != old {
			t.Fatalf("a labelled build: %+v %v", pkg, err)
		}
	})
}

// A same-sized replacement can keep the same mtime on a coarse filesystem clock. Neither the package-result cache nor
// the negative version scan may make that new current executable invisible (#13298).
func TestReplacedLocalNodeBuildWithTheSameStampIsSeen(t *testing.T) {
	const goos, arch = "windows", "amd64"
	const hub, old = "0.9.2-202611221500", "0.9.1-202611190505"
	plat := goos + "-" + arch
	stale := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, old)
	current := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, hub)
	for _, name := range []string{"personad-" + plat + ".exe", "personad-" + plat} {
		t.Run(name, func(t *testing.T) {
			d, dir, _ := packageDevices(t, hub, "")
			local := filepath.Join(dir, name)
			copyFile(t, stale, local)
			before, err := os.Stat(local)
			if err != nil {
				t.Fatal(err)
			}
			if pkg, err := d.nodePackageFor(context.Background(), goos, arch, nodeproto.ProgramPersonad); err == nil {
				t.Fatalf("the stale build was taken: %+v", pkg)
			}

			copyFile(t, current, local)
			stamp := before.ModTime()
			if err := os.Chtimes(local, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(local)
			if err != nil {
				t.Fatal(err)
			}
			if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("fixture metadata changed: before %+v, after %+v", before, after)
			}
			if r, ok := d.cachedPackage(d.packageKey(goos, arch, nodeproto.ProgramPersonad)); ok {
				t.Fatalf("stale cached result stayed fresh after the same-stamp replacement: %+v %v", r.pkg, r.err)
			}
			pkg, err := d.nodePackageFor(context.Background(), goos, arch, nodeproto.ProgramPersonad)
			if err != nil || pkg.path != local || pkg.Version != hub {
				t.Fatalf("the same-stamp replacement was not seen: %+v %v", pkg, err)
			}
		})
	}
}

// The cache observes size and mtime. Equal-sized fixture binaries can be overwritten within one filesystem clock
// tick; make the replacement's metadata distinct so these cases exercise invalidation without a timing dependency.
func copyLocalNodeBuild(t *testing.T, from, to string) {
	t.Helper()
	previous, err := os.Stat(to)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	copyFile(t, from, to)
	if previous != nil {
		stamp := previous.ModTime().Add(time.Second)
		if err := os.Chtimes(to, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

// An unchanged file outside the recent-replacement window still reuses its scan instead of being read in full again.
func TestUnchangedLocalBuildReusesAStableVersionScan(t *testing.T) {
	const goos, arch = "windows", "amd64"
	const hub = "0.9.2-202611221500"
	current := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, hub)
	path := filepath.Join(t.TempDir(), "personad-windows-amd64.exe")
	copyFile(t, current, path)
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	p := &nodePackages{}
	if carries, err := p.carriesVersion(path, hub); err != nil || !carries {
		t.Fatalf("first scan: %v %v", carries, err)
	}
	// Poison the remembered answer: a second call that really uses the cache returns it; a full rescan finds hub and
	// returns true instead. This distinguishes reuse from merely getting the same answer twice.
	p.mu.Lock()
	scan := p.scans[path]
	scan.carries = false
	p.scans[path] = scan
	p.mu.Unlock()
	if carries, err := p.carriesVersion(path, hub); err != nil || carries {
		t.Fatalf("an unchanged stable file was scanned again: %v %v", carries, err)
	}
}

// A timestamp carried from a machine whose clock is ahead must not disable caching forever (#13326 review).
func TestFutureLocalBuildStillUsesCachedPackage(t *testing.T) {
	const goos, arch = "windows", "amd64"
	const hub = "0.9.2-202611221500"
	current := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, hub)
	d, dir, _ := packageDevices(t, hub, "")
	local := filepath.Join(dir, "personad-windows-amd64.exe")
	copyFile(t, current, local)
	stamp := time.Now().Add(time.Hour)
	if err := os.Chtimes(local, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if pkg, err := d.nodePackageFor(context.Background(), goos, arch, nodeproto.ProgramPersonad); err != nil || pkg.path != local {
		t.Fatalf("first future-stamp lookup: %+v %v", pkg, err)
	}
	r, ok := d.cachedPackage(d.packageKey(goos, arch, nodeproto.ProgramPersonad))
	if !ok || r.err != nil || r.pkg.path != local {
		t.Fatalf("an unchanged future-stamp build missed the result cache: %+v %v, ok=%v", r.pkg, r.err, ok)
	}
}

// Callers attached to one in-flight lookup consume that generation together. The recent-file guard applies to later
// independent decisions; it must not turn every waiter into another sequential lookup and another set of web requests.
func TestRecentLocalBuildLookupsAreShared(t *testing.T) {
	const goos, arch = "windows", "amd64"
	const hub, old = "0.9.2-202611221500", "0.9.1-202611190505"
	plat := goos + "-" + arch
	stale := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, old)
	current := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, goos, arch, hub)
	site := newFakeRelease(t)
	site.publish(t, hub, overlapping(hub, 202611221500), true, map[string]string{"personad-" + plat + ".exe": current})
	d, dir, _ := packageDevices(t, hub, site.srv.URL)
	copyFile(t, stale, filepath.Join(dir, "personad-"+plat+".exe"))
	site.mu.Lock()
	site.delay = 200 * time.Millisecond
	site.mu.Unlock()

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if pkg, err := d.nodePackageFor(context.Background(), goos, arch, nodeproto.ProgramPersonad); err != nil {
				t.Errorf("shared lookup: %+v %v", pkg, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	count := map[string]int{}
	for _, path := range site.requests() {
		count[path]++
	}
	if len(count) != 1 || count["/"+hub+"/manifest.json"] != 1 {
		t.Fatalf("concurrent callers did not share one lookup: %v", count)
	}
}

func TestFileContainsAcrossChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build")
	needle := "0.9.2-202611221500"
	body := make([]byte, 3<<20)
	at := 1<<20 - 5 // straddles the first chunk's end
	copy(body[at:], needle)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := fileContains(path, []byte(needle)); err != nil || !ok {
		t.Fatalf("across a chunk boundary: %v %v", ok, err)
	}
	if ok, err := fileContains(path, []byte("0.9.1-202611190505")); err != nil || ok {
		t.Fatalf("absent: %v %v", ok, err)
	}
}
