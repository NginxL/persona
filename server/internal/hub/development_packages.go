package hub

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

// Both the handover and every download consult the installed identity. A check,
// a staged build, or an unresolved adoption can never authorize distribution.
func (u *updater) confirmedDevelopment() (*devicewire.DevelopmentTarget, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.adoptionUnresolved() {
		return nil, true
	}
	if !strings.HasPrefix(u.h.version, "web-dev-") {
		return nil, false
	}
	installed := devicewire.DevelopmentBuild(u.h.build.Commit, u.h.version, u.h.build.Number)
	if !u.confirmed || !u.loaded || !installed.Valid() || slices.Contains(u.settings.DevelopmentBad, installed.Commit) {
		return nil, true
	}
	if receipt := u.settings.ConfirmedDevelopment; receipt != nil && *receipt == installed {
		return &installed, false
	}
	// An earlier Hub wrote history without a separate receipt. A rollback
	// can also restore the previous binary after its successor wrote history.
	for i := len(u.settings.History) - 1; i >= 0; i-- {
		r := u.settings.History[i]
		if r.Source == "web-dev" && r.Commit == installed.Commit && r.ToVersion == installed.Version && r.ToBuild == installed.Build {
			if r.Result == "updated" && r.Reason == "" {
				return &installed, false
			}
			break
		}
	}
	return nil, true
}

func (u *updater) stageDevelopmentPackages(r *update.Published, staged string) error {
	target := devicewire.DevelopmentBuild(r.Manifest.Commit, r.Manifest.Version, r.Manifest.Build)
	if !target.Valid() { // restoring an ordinary release from a backup
		return nil
	}
	source, err := url.Parse(r.Source)
	if err != nil || source.Scheme != "file" {
		return errors.New("development packages require a local build directory")
	}
	if err := update.Save(filepath.Join(source.Path, "manifest.json"), r.Manifest); err != nil {
		return err
	}
	return os.Rename(source.Path, filepath.Join(staged, "packages"))
}

func (u *updater) packageStore() string { return filepath.Join(u.dir, "development-packages") }

func (u *updater) publishDevelopmentPackages(p update.Pending) error {
	t := devicewire.DevelopmentBuild(p.Development.Commit, p.ToVersion, p.ToBuild)
	if !t.Valid() {
		return nil
	}
	src := filepath.Join(u.stagedDir(p.ToBuild), "packages")
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		// A transaction prepared by an older Hub had no hosted packages. The
		// confirmed identity still permits an exact source build on a manager.
		return nil
	}
	root := u.packageStore()
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	dest := filepath.Join(root, t.Version)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(src, dest); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if d.IsDir() && d.Name() != t.Version && d.Name() != p.FromVersion && strings.HasPrefix(d.Name(), "web-dev-") {
			if err := os.RemoveAll(filepath.Join(root, d.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

var developmentArchive = lazyre.New(`^(?:personad|persona-manager|persona-node)-[a-z0-9]+-[a-z0-9]+(?:\.exe)?\.gz$`)

func (h *Hub) serveDevelopmentPackage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.busCaller(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	t, _ := h.upd.confirmedDevelopment()
	if t == nil || t.Commit != r.PathValue("commit") || t.Version != r.PathValue("version") {
		// Distinct from a missing file of the current target: no source fallback.
		w.WriteHeader(http.StatusGone)
		return
	}
	w.Header().Set(devicewire.DevelopmentCommitHeader, t.Commit)
	name := r.PathValue("file")
	if name != "manifest.json" && !developmentArchive.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.OpenInRoot(filepath.Join(h.upd.packageStore(), t.Version), name)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}
