package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

func (u *updater) nodesEnabled() bool {
	if u == nil {
		return false // No updater means the persisted deployment policy has not been read.
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.loaded {
		ctx, cancel := context.WithTimeout(context.Background(), updateRequestLimit)
		defer cancel()
		if err := u.loadLocked(ctx); err != nil {
			return false // An unread persisted choice must never enable deployment.
		}
	}
	return u.nodesEnabledLocked()
}

// nodesEnabledLocked is also used by the view while holding the updater lock.
func (u *updater) nodesEnabledLocked() bool {
	if u.settings.AutoUpdateNodes != nil {
		return *u.settings.AutoUpdateNodes
	}
	return u.h.cfg != nil && u.h.cfg.AutoUpdateNodes
}

// stageNodes never fetches or recreates the node package directory unless fleet
// updates are explicitly enabled. Hub-only releases can contain just one platform.
func (u *updater) stageNodes(ctx context.Context, m *update.Manifest, staged, bin string) (string, error) {
	return u.stageNodesFrom(ctx, m, staged, bin, "", false)
}

func (u *updater) stageNodesFrom(ctx context.Context, m *update.Manifest, staged, bin, localSource string, development bool) (string, error) {
	if !u.nodesEnabled() && !development {
		return "", nil
	}
	nodes := filepath.Join(staged, "nodes")
	own := runtime.GOOS + "-" + runtime.GOARCH
	for _, plat := range nodePlatforms(m) {
		u.set("downloading", plat, "")
		if plat == own {
			// This platform's node binary is the hub's own binary: the same file, so copy the one already here
			// rather than fetching it twice (a machine on the hub's platform added later needs it too).
			err := os.MkdirAll(nodes, 0o755)
			if err == nil {
				err = copyInto(bin, filepath.Join(nodes, "personad-"+plat))
				if err == nil {
					err = copyInto(bin, filepath.Join(nodes, compat.DaemonBinary+"-"+plat))
				}
			}
			if err != nil {
				return "", fmt.Errorf("the node binary for the hub's own platform: %w", err)
			}
			continue
		}
		if _, err := u.download(ctx, m, m.Files[plat], nodes, localSource); err != nil {
			return "", err
		}
	}
	return nodes, nil
}

// nodeBuildName: a personad build for one platform as bin/nodes holds it. Windows keeps .exe: dropping it would
// leave the downloaded program outside both manager lookup and update retention.
var nodeBuildName = lazyre.New(`^(?:personad|` + regexp.QuoteMeta(compat.DaemonBinary) + `)-[a-z0-9]+-[a-z0-9]+(?:\.exe)?$`)

// setAsideOldNodes renames the personad builds in dir that would pass for the hub's own version and no longer are. The
// manager takes a personad there with no version file beside it for a build of the hub's version
// (internal/devices/packages.go), which stops being true once the hub updates itself without its nodes: Redeploy would
// put the old build back on every computer (#1825). Set aside as <name>.prev, the manager fetches the hub's own build
// from the release instead. A build with a version file says what it is and stays; the directory is never created. It
// returns the names it moved.
func setAsideOldNodes(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var moved []string
	var first error
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !nodeBuildName.MatchString(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path + ".version"); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			first = errors.Join(first, err)
			continue
		}
		if err := os.Rename(path, path+".prev"); err != nil {
			first = errors.Join(first, err)
			continue
		}
		moved = append(moved, name)
	}
	return moved, first
}

// labelUnstagedNodes preserves omitted backup platforms without advertising them
// as the restored Hub's version. Existing explicit version identities stay intact.
func labelUnstagedNodes(dir, staged, from string) error {
	stagedEntries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, e := range stagedEntries {
		if e.Type().IsRegular() && nodeBuildName.MatchString(e.Name()) {
			present[e.Name()] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !nodeBuildName.MatchString(e.Name()) || present[e.Name()] {
			continue
		}
		marker := filepath.Join(dir, e.Name()+".version")
		if info, err := os.Lstat(marker); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("retained Node package has an invalid version marker")
			}
			version, err := os.ReadFile(marker)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(version)) == "" {
				return errors.New("retained Node package has an empty version marker")
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := writeRetainedNodeVersion(marker, from); err != nil {
			return err
		}
	}
	return nil
}

func writeRetainedNodeVersion(marker, version string) error {
	f, err := os.CreateTemp(filepath.Dir(marker), ".retained-node-version-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.WriteString(version + "\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Link atomically only when the destination is still absent.
	if err := os.Link(f.Name(), marker); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	d, err := os.Open(filepath.Dir(marker))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
