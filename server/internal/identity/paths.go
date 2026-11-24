package identity

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/compat"
)

func explicitRoot(home string) string {
	if root := Getenv("PERSONA_HOME"); root != "" {
		if root == "~" {
			return home
		}
		if strings.HasPrefix(root, "~/") {
			return filepath.Join(home, root[2:])
		}
		if !filepath.IsAbs(root) {
			return filepath.Join(home, root)
		}
		return filepath.Clean(root)
	}
	return ""
}

func DataDir(home string) string {
	if root := explicitRoot(home); root != "" {
		return root
	}
	current, legacy := filepath.Join(home, ".persona"), filepath.Join(home, compat.StateDir)
	if _, err := os.Lstat(legacy); err == nil {
		if a, err := os.Stat(current); err == nil {
			if b, err := os.Stat(legacy); err == nil && os.SameFile(a, b) {
				return current
			}
		}
		return legacy
	}
	return current
}

func HomeDir() string {
	home, _ := os.UserHomeDir()
	return DataDir(home)
}

// PrepareHome creates only the configured state directory. Existing directory
// symlinks are accepted, but unavailable targets are never repaired or replaced.
// An installation still kept under the former name gets the current name as an
// alias, so both spellings reach it; one under the current name gets no alias
// under the former name (#1445). An alias already there is left as it is.
func PrepareHome(home string) (string, error) {
	root := DataDir(home)
	currentPath, legacyPath := filepath.Join(home, ".persona"), filepath.Join(home, compat.StateDir)
	if root == currentPath || root == legacyPath {
		a, aerr := os.Stat(currentPath)
		b, berr := os.Stat(legacyPath)
		// An explicit PERSONA_HOME has picked one of two independent stores: it is used, and the other is left as
		// it is (it is never aliased over, below). Without one, neither is guessed.
		if aerr == nil && berr == nil && !os.SameFile(a, b) && explicitRoot(home) == "" {
			return "", fmt.Errorf("independent state directories exist; set PERSONA_HOME to the one to use, without moving either directory")
		}
	}
	if st, err := os.Lstat(root); err == nil && st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Stat(root)
		if err != nil || !target.IsDir() {
			return "", fmt.Errorf("state directory %s has an unavailable directory symlink target", root)
		}
		// Keep the existing target; install only the missing compatibility alias.
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	current, legacy := filepath.Join(home, ".persona"), filepath.Join(home, compat.StateDir)
	if root == legacy {
		alias := current
		if _, err := os.Lstat(alias); os.IsNotExist(err) {
			if err := os.Symlink(filepath.Base(root), alias); err != nil && !os.IsExist(err) {
				return "", err
			}
			// Another process can initialize the alias concurrently. Never accept
			// an alias pointing at another store after losing that race.
			a, aerr := os.Stat(root)
			b, berr := os.Stat(alias)
			if aerr != nil || berr != nil || !os.SameFile(a, b) {
				return "", fmt.Errorf("state directories changed during initialization")
			}
		} else if err != nil {
			return "", err
		}
	}
	return root, nil
}

func SocketDir(home string) string {
	return DataDir(home)
}

func AssistantDir(home string) string {
	return filepath.Join(DataDir(home), "assistant")
}

// HealthDir is the health space's directory (docs/assistant/health.md): the assistant's conversations about health run there,
// next to its own directory, so their memory and instructions are their own.
func HealthDir(home string) string {
	return filepath.Join(DataDir(home), SpaceHealth)
}

// SpaceHealth is the health space's name: its directory's and Session.Space.
const SpaceHealth = "health"

// builtinManager is the id of the manager the hub starts (assistantwire.AssistantID): its directories keep their
// names.
const builtinManager = "assistant"

// AssistantDirName is the name in the state directory of manager id's assistant directory: "assistant" for the
// built-in manager's ("" too), "assistant-<id>" for another's (docs/assistant/managers.md "Directories on a computer").
func AssistantDirName(id string) string { return managerDirName("assistant", id) }

// HealthDirName is AssistantDirName for its health space: "health", or "health-<id>".
func HealthDirName(id string) string { return managerDirName(SpaceHealth, id) }

func managerDirName(base, id string) string {
	if id == "" || id == builtinManager {
		return base
	}
	return base + "-" + id
}

// IsManagerDirName: name is base or base-<id> for a manager id the hub makes ("m-" and hex digits). Other names that
// start the same (assistant-bin, assistant-hooks) are not.
func IsManagerDirName(name, base string) bool {
	if name == base {
		return true
	}
	hex, ok := strings.CutPrefix(name, base+"-m-")
	return ok && hex != "" && strings.Trim(hex, "0123456789abcdef") == ""
}

// IsAssistantDir: path is one of the assistant's directories, its own, the health space's or a track's
// (tracks/<slug>), of any manager.
func IsAssistantDir(path string) bool {
	return isManagerStateDir(path, "assistant") || isManagerStateDir(path, SpaceHealth) || isTrackDir(path)
}

// IsAssistantDirOf: path is one of manager id's assistant directories, its own, its health space's or one of its
// tracks' (another manager's, left on a computer moved away from it, is not).
func IsAssistantDirOf(path, id string) bool {
	return isStateDir(path, AssistantDirName(id)) || isStateDir(path, HealthDirName(id)) || isTrackDirOf(path, id)
}

// isTrackDir: path is a track's directory in a tracks directory of any manager (tracks/<slug>, tracks-<id>/<slug>):
// where every track whose template is space keeps its directory (#13472).
func isTrackDir(path string) bool {
	p := filepath.Clean(NodePath(path))
	return ValidTrackSlug(filepath.Base(p)) && isManagerStateDir(filepath.Dir(p), "tracks")
}

// isTrackDirOf: path is a track's directory in manager id's tracks directory.
func isTrackDirOf(path, id string) bool {
	p := filepath.Clean(NodePath(path))
	return ValidTrackSlug(filepath.Base(p)) && isStateDir(filepath.Dir(p), TracksDirName(id))
}

// TrackDirOf: dir (relative to the state directory, a track's DirName) is a track's directory in manager id's tracks
// directory: tracks/<slug> (tracks-<id>/<slug>), one level, slug valid. A track whose template is space keeps its
// directory there, so every process tells its conversations from the path alone (IsAssistantDir, SpaceOf).
func TrackDirOf(dir, id string) bool {
	slug, ok := strings.CutPrefix(dir, TracksDirName(id)+"/")
	return ok && ValidTrackSlug(slug)
}

// TracksDirName is the name in the state directory of manager id's tracks directory, where a new track's directory
// goes by default (tracks/<slug>, whatever its kind, so a track that changes kind keeps it): "tracks", or
// "tracks-<id>". A track's directory is data (its registry row's), so one made elsewhere (the health space's) stays.
func TracksDirName(id string) string { return managerDirName("tracks", id) }

// reservedTrackSlugs name what a track must not be mistaken for: the main space, the built-in health space (supplied
// by the manager, never created), the assistant's directory, a guest, the spaces and tracks directories.
var reservedTrackSlugs = []string{"main", SpaceHealth, "assistant", "guest", "spaces", "tracks"}

// ValidTrackSlug: slug can name a new track: a lowercase letter, then 1 to 31 lowercase letters, digits or dashes,
// and none of the reserved names.
func ValidTrackSlug(slug string) bool {
	if slices.Contains(reservedTrackSlugs, slug) || len(slug) < 2 || len(slug) > 32 || slug[0] < 'a' || slug[0] > 'z' {
		return false
	}
	for _, r := range slug[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// trackDirs are the directories of the tracks a manager named to this process (a node's assistant.prepare deny_dirs),
// by who named them, each directory with its track's slug: a track's directory is data, never told from its path.
var trackDirs struct {
	sync.Mutex
	by map[string]map[string]string
}

// SetTrackDirs keeps the track directories source (one prepared directory of a manager) names, each a clean path with
// its slug, in place of what source named before; an empty map forgets them.
func SetTrackDirs(source string, dirs map[string]string) {
	trackDirs.Lock()
	defer trackDirs.Unlock()
	if len(dirs) == 0 {
		delete(trackDirs.by, source)
		return
	}
	if trackDirs.by == nil {
		trackDirs.by = map[string]map[string]string{}
	}
	trackDirs.by[source] = maps.Clone(dirs)
}

// TrackDirs are the track directories named to this process (SetTrackDirs), each with its slug.
func TrackDirs() map[string]string {
	trackDirs.Lock()
	defer trackDirs.Unlock()
	out := map[string]string{}
	for _, dirs := range trackDirs.by {
		maps.Copy(out, dirs)
	}
	return out
}

// namedTrackDir is the slug of the named track whose directory path is exactly ("" for none).
func namedTrackDir(path string) string {
	trackDirs.Lock()
	defer trackDirs.Unlock()
	for _, dirs := range trackDirs.by {
		if slug, ok := dirs[path]; ok {
			return slug
		}
	}
	return ""
}

// spaceDirSlug is the track whose directory path is exactly: SpaceHealth for a health space's (health, health-<id>,
// of any manager), the slug of a track a manager named (SetTrackDirs), else the slug of one in a tracks directory
// where a new track goes by default (tracks/<slug>, tracks-<id>/<slug>: a fallback before the manager named it,
// which only hides more), "" for any other.
func spaceDirSlug(path string) string {
	if isManagerStateDir(path, SpaceHealth) {
		return SpaceHealth
	}
	p := filepath.Clean(NodePath(path))
	if slug := namedTrackDir(p); slug != "" {
		return slug
	}
	if slug := filepath.Base(p); ValidTrackSlug(slug) && isManagerStateDir(filepath.Dir(p), "tracks") {
		return slug
	}
	return ""
}

// SpaceOf is the space of an assistant directory: its slug for a space's (SpaceHealth for a health space's), "" for
// the assistant's own and any other.
func SpaceOf(path string) string { return spaceDirSlug(path) }

// SpaceAt is the space whose directory path is, of any manager, or is inside: "" for none. A conversation's directory
// is where it started; a hook's or a card's is where the model's last `cd` left its shell (#2368), which may be a
// subfolder of it (health/reports). This is for hiding and confirming only, where counting a subfolder as the space
// can only hide or ask more: it never decides what a conversation is (SpaceOf and IsAssistantDirOf stay exact). A path
// part named health elsewhere (assistant/.claude/skills/health, /tmp/health) is not one.
func SpaceAt(path string) string {
	if path == "" {
		return ""
	}
	for p := filepath.Clean(NodePath(path)); ; {
		if slug := spaceDirSlug(p); slug != "" {
			return slug
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

// InSpace: path is a track's directory (the health space's, or one a manager named), or a directory inside one
// (SpaceAt). A process that cannot read the manager's registry (the hub, a node) treats every such track as private.
func InSpace(path string) bool { return SpaceAt(path) != "" }

// InHealthSpace: path is a health space's directory, of any manager, or a directory inside one (SpaceAt). Hiding and
// confirming go by InSpace or the space's policy; this tells the health space's own features (its reports) apart.
func InHealthSpace(path string) bool { return SpaceAt(path) == SpaceHealth }

// CardCwd is the directory a card (an interaction) of a conversation is placed in: the conversation's own directory
// home, where it started, rather than where the hook ran (here, after the model's `cd`s). A hook directory inside a
// space a home outside it does not have wins, so a mix-up can only hide more. "" home: here.
func CardCwd(home, here string) string {
	if home == "" || InSpace(here) && !InSpace(home) {
		return here
	}
	return home
}

// NodePath is a directory a node reported, made readable for filepath here. On a hub that is not Windows, a Windows
// node's path (with a drive letter, C:\…, or on a share, \\server\…) gets forward slashes: filepath there splits only
// at /, so C:\Users\me\.persona\assistant read as one name and the hub missed that computer's assistant and health
// directories (#6419). Any other path, and every path on Windows, where filepath takes both, stays as it is.
func NodePath(path string) string {
	if runtime.GOOS == "windows" || !strings.Contains(path, `\`) {
		return path
	}
	letter := len(path) > 0 && ('a' <= path[0]|0x20 && path[0]|0x20 <= 'z')
	if letter && len(path) >= 2 && path[1] == ':' || strings.HasPrefix(path, `\\`) {
		return strings.ReplaceAll(path, `\`, "/")
	}
	return path
}

// isManagerStateDir: path is the directory base, or base-<id> (IsManagerDirName), in the state directory.
func isManagerStateDir(path, base string) bool {
	name := filepath.Base(filepath.Clean(NodePath(path)))
	return IsManagerDirName(name, base) && isStateDir(path, name)
}

// isStateDir recognizes a directory under .persona or the configured PERSONA_HOME.
func isStateDir(path, name string) bool {
	path = filepath.Clean(NodePath(path))
	if filepath.Base(path) != name {
		return false
	}
	if compat.IsStateDir(filepath.Base(filepath.Dir(path))) {
		return true
	}
	expected := filepath.Join(HomeDir(), name)
	if path == expected {
		return true
	}
	a, err := os.Stat(path)
	if err != nil {
		return false
	}
	b, err := os.Stat(expected)
	return err == nil && os.SameFile(a, b)
}

// NodeBinaryDir is where deploy.sh puts the node binaries next to the hub's own (the hub's updates put the release's
// there too, the manager deploys them from there, and an air-gapped Docker hub's owner drops them there, in the data
// volume); PERSONA_NODE_BINARIES points elsewhere.
func NodeBinaryDir() string {
	if d := Getenv("PERSONA_NODE_BINARIES"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(DataDir(home), "bin", "nodes")
}

// NodeBundledDir is where the program running carries node builds of its own version beside NodeBinaryDir:
// PERSONA_NODE_BUNDLED, the Docker image's /opt/persona/nodes with its own platform's build; "" for none.
func NodeBundledDir() string { return Getenv("PERSONA_NODE_BUNDLED") }

// NodePackageCacheDir is where a manager keeps the node builds it fetched from the release (docs/operations/node-update.md),
// by version: <data dir>/cache/nodes, in a Docker hub's data volume too.
func NodePackageCacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(DataDir(home), "cache", "nodes")
}
