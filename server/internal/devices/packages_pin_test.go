package devices

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

// releaseFiles is release m as a source with no dl/<version>/ serves it: personad-<plat>.gz holding build, and
// latest.json.
func releaseFiles(t *testing.T, m update.Manifest, plat string, build []byte) map[string][]byte {
	t.Helper()
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	_, _ = zw.Write(build)
	_ = zw.Close()
	outer, inner := sha256.Sum256(packed.Bytes()), sha256.Sum256(build)
	name := "personad-" + plat + ".gz"
	m.Files = map[string]update.File{plat: {Name: name, Size: int64(packed.Len()), SHA256: hex.EncodeToString(outer[:]), BinarySHA256: hex.EncodeToString(inner[:])}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"latest.json": data, name: packed.Bytes()}
}

// A node build looked up at a source with no dl/<version>/ is downloaded as that release although a newer one is
// published before the download (#9263): from GitHub, the release's own tag; from another such source, the download
// says a newer release came out rather than that the build did not check out, and is not fetched again.
func TestNodePackageDownloadKeepsToTheRelease(t *testing.T) {
	plat := otherOS + "-" + otherArch
	name := "personad-" + plat + ".gz"
	full := updatetest.Program(t, "personad", otherOS, otherArch)
	build, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	// The newer release's build is never used: anything longer than the one looked up.
	newer := append(bytes.Clone(build), bytes.Repeat([]byte("a newer build "), 64)...)
	ctx := context.Background()
	lookUp := func(t *testing.T, base func(*updatetest.Releases) string, files map[string][]byte) (*Devices, nodePackage, *updatetest.Releases) {
		t.Helper()
		src := updatetest.NewReleases(t)
		src.Publish("v0.6.1", files, true)
		d, _, _ := packageDevices(t, "0.6.1", base(src))
		d.packages.httpClient = func(*url.URL, time.Duration) *http.Client { return src.Client() }
		pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
		if err != nil || pkg.Fallback || pkg.Version != "0.6.1" || !pkg.flat {
			t.Fatalf("%+v %v", pkg, err)
		}
		return d, pkg, src
	}
	lookUpThenPublish := func(t *testing.T, base func(*updatetest.Releases) string) (*Devices, nodePackage, *updatetest.Releases) {
		t.Helper()
		d, pkg, src := lookUp(t, base, releaseFiles(t, overlapping("0.6.1", 202611160928), plat, build))
		src.Publish("v0.6.2", releaseFiles(t, overlapping("0.6.2", 202611161034), plat, newer), true)
		return d, pkg, src
	}

	t.Run("GitHub", func(t *testing.T) {
		d, pkg, src := lookUpThenPublish(t, (*updatetest.Releases).GitHub)
		bin, err := d.fetchPackage(ctx, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(bin); !bytes.Equal(b, build) {
			t.Fatalf("fetched another build (%d bytes)", len(b))
		}
		got := src.Fetched(name)
		if len(got) == 0 || got[len(got)-1] != updatetest.TagPath("v0.6.1", name) {
			t.Fatalf("fetched %v", got)
		}
		for _, path := range got {
			if path == updatetest.LatestPath(name) {
				t.Fatalf("fetched from the moving base: %v", got)
			}
		}
	})

	t.Run("a site with no dl/<version>/", func(t *testing.T) {
		d, pkg, src := lookUpThenPublish(t, (*updatetest.Releases).DL)
		_, err := d.fetchPackage(ctx, pkg)
		if err == nil || !strings.Contains(err.Error(), "0.6.2") || strings.Contains(err.Error(), "does not match") {
			t.Fatalf("%v", err)
		}
		if text := i18n.DeploymentText(i18n.En, err.Error()); !strings.Contains(text, "A newer release, 0.6.2, was published") {
			t.Fatalf("says %q", text)
		}
		if got := src.Fetched(name); len(got) != 2 || got[1] != "/dl/"+name { // dl/0.6.1/ (404), then beside latest.json
			t.Fatalf("fetched %v", got)
		}
	})

	// A build that is broken while the release stays the one looked up still did not check out.
	t.Run("a broken build", func(t *testing.T) {
		files := releaseFiles(t, overlapping("0.6.1", 202611160928), plat, build)
		files[name] = append(bytes.Clone(files[name]), "garbage"...)
		d, pkg, src := lookUp(t, (*updatetest.Releases).DL, files)
		_, err := d.fetchPackage(ctx, pkg)
		if err == nil {
			t.Fatal("a broken build was used")
		}
		if text := i18n.DeploymentText(i18n.En, err.Error()); !strings.Contains(text, "did not check out") || !strings.Contains(text, "does not match") {
			t.Fatalf("says %q", text)
		}
		if got := src.Fetched(name); len(got) != 2 {
			t.Fatalf("fetched %v", got)
		}
	})
}

// GitHub's moving release base must not be asked for the legacy <version>/<file> path first: its redirect turns
// that into releases/download/v<version>/<version>/<file>, which is not a release asset.
func TestNodePackageGitHubDownloadUsesReleaseTag(t *testing.T) {
	plat := otherOS + "-" + otherArch
	name := "personad-" + plat + ".gz"
	full := updatetest.Program(t, "personad", otherOS, otherArch)
	build, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	src := updatetest.NewReleases(t)
	src.Publish("v0.9.1", releaseFiles(t, overlapping("0.9.1", 202611190000), plat, build), true)
	d, _, _ := packageDevices(t, "0.9.1", src.GitHub())
	d.packages.httpClient = func(*url.URL, time.Duration) *http.Client { return src.Client() }
	pkg, err := d.nodePackageFor(context.Background(), otherOS, otherArch, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.fetchPackage(context.Background(), pkg); err != nil {
		t.Fatal(err)
	}
	want := updatetest.TagPath("v0.9.1", name)
	if got := src.Fetched(name); len(got) != 1 || got[0] != want {
		t.Fatalf("fetched %v, want only %q", got, want)
	}
}
