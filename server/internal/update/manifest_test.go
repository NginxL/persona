package update

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The manifest carries no min_node_build: nothing ever read it, publish.sh never wrote it, and the "a node older than
// it updates first" it was meant to express is not how a hub updates (the nodes follow once the new hub confirmed,
// docs/operations/hub-update.md, "Update nodes along with it").
func TestManifestHasNoMinNodeBuild(t *testing.T) {
	typ := reflect.TypeOf(Manifest{})
	for i := 0; i < typ.NumField(); i++ {
		if tag := typ.Field(i).Tag.Get("json"); strings.HasPrefix(tag, "min_node_build") {
			t.Fatalf("Manifest.%s is in latest.json but read by nothing", typ.Field(i).Name)
		}
	}
}

// A latest.json published before it went away still reads: the old key is ignored, not an error.
func TestParseManifestIgnoresALegacyMinNodeBuild(t *testing.T) {
	b := []byte(`{"version":"v1","build":202611021530,"protocol":1,"min_node_build":209901010000,
		"files":{"linux-amd64":{"name":"personad-linux-amd64.gz","size":1,"sha256":"a","binary_sha256":"b"}}}`)
	m, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("a manifest with min_node_build does not read: %v", err)
	}
	if m.Build != 202611021530 || m.Version != "v1" || len(m.Files) != 1 {
		t.Fatalf("%+v", m)
	}
}

// LinkFile is the connector's download a release lists for a platform (a manager sends it to a computer switching to
// Persona Link): what BuildManifest put under links, Windows' under its platform without .exe, and nothing for a
// platform the release has none for or an entry without a name.
func TestManifestLinkFile(t *testing.T) {
	m := &Manifest{Links: map[string]File{
		"linux-amd64":   {Name: "persona-link-linux-amd64.gz", Size: 3},
		"windows-arm64": {Name: "persona-link-windows-arm64.exe.gz", Size: 4},
		"linux-arm64":   {Size: 5},
	}}
	if f, ok := m.LinkFile("linux-amd64"); !ok || f.Name != "persona-link-linux-amd64.gz" || f.Size != 3 {
		t.Fatalf("linux-amd64: %+v %v", f, ok)
	}
	if f, ok := m.LinkFile("windows-arm64"); !ok || f.Name != "persona-link-windows-arm64.exe.gz" {
		t.Fatalf("windows-arm64: %+v %v", f, ok)
	}
	for _, plat := range []string{"linux-arm64", "darwin-arm64", ""} {
		if f, ok := m.LinkFile(plat); ok {
			t.Errorf("%q: %+v", plat, f)
		}
	}
	if f, ok := (&Manifest{}).LinkFile("linux-amd64"); ok { // a release from before links
		t.Errorf("no links: %+v", f)
	}
}

func TestBuildManifestHelperWindowsExePlatform(t *testing.T) {
	dir := t.TempDir()
	writeGz(t, filepath.Join(dir, "personad-linux-amd64.gz"), []byte("personad"))
	writeGz(t, filepath.Join(dir, "persona-iphone-bridge-darwin-arm64.gz"), []byte("macOS helper"))
	writeGz(t, filepath.Join(dir, "persona-iphone-bridge-windows-amd64.exe.gz"), []byte("Windows amd64 helper"))
	writeGz(t, filepath.Join(dir, "persona-iphone-bridge-windows-arm64.exe.gz"), []byte("Windows arm64 helper"))

	m, err := BuildManifest(dir, Manifest{Version: "v1", Build: 202611030000})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		plat, want string
	}{
		{"darwin-arm64", "persona-iphone-bridge-darwin-arm64.gz"},
		{"windows-amd64", "persona-iphone-bridge-windows-amd64.exe.gz"},
		{"windows-arm64", "persona-iphone-bridge-windows-arm64.exe.gz"},
	} {
		f, ok := m.HelperFile("persona-iphone-bridge", tc.plat)
		if !ok || f.Name != tc.want || f.BinarySHA256 == "" {
			t.Errorf("HelperFile(%q) = %+v, %v; want %s", tc.plat, f, ok, tc.want)
		}
	}
	if _, ok := m.Helpers["persona-iphone-bridge"]["windows-amd64.exe"]; ok {
		t.Fatalf("Windows helper indexed under .exe platform key: %+v", m.Helpers)
	}
}
