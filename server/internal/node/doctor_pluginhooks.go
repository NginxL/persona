package node

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/extension"
)

// A PermissionRequest hook in the user's settings.json can be an extension's: an extension's program on this computer
// (plugins.go) may put its own hook there, and the user chose that by installing the extension (#11973). The
// global-permission-hooks check names such a hook's extension instead of warning about it as another tool's. No
// manifest field declares Claude Code hooks, so the program writes the entry itself; what says the hook is the
// extension's is the node's own install record of the package the hook's program is in. A path under node-plugins/
// alone is not enough, as anything can be put there by hand:
//
//   - the program resolves (symlinks followed) to a file in <persona dir>/node-plugins/<id>/<version>/, so a link out
//     of the package is not the package's;
//   - <id>/ carries the mark only the node writes (pluginOwnMark, pluginHost.own), the one sync relies on too;
//   - <version>/ carries the digest the node writes only after the package's sha256 matched (pluginHost.commit);
//   - the package has its manifest, whose name is what the doctor says.

// extensionHookOwner is the name of the installed extension whose package the hook program is in, "" when it is not
// one this node installed under dataDir (the node's persona dir).
func extensionHookOwner(dataDir, program string) string {
	if dataDir == "" || program == "" || !filepath.IsAbs(program) {
		return "" // a bare name runs from PATH, not from a package
	}
	root, err := filepath.EvalSymlinks(filepath.Join(dataDir, nodePluginsDir))
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(program)
	if err != nil {
		return ""
	}
	if st, err := os.Stat(resolved); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 || parts[0] == ".." {
		return ""
	}
	id, version := parts[0], parts[1]
	if !extension.ValidID(id) || !(&pluginHost{dir: root}).owns(id) {
		return ""
	}
	if _, err := extension.ParseVersion(version); err != nil {
		return ""
	}
	dir := filepath.Join(root, id, version)
	if !hasPackageDigest(dir) {
		return ""
	}
	return packageName(dir)
}

// hasPackageDigest: dir has the .persona-digest file pluginHost.commit writes, holding a sha256.
func hasPackageDigest(dir string) bool {
	at := filepath.Join(dir, ".persona-digest")
	if st, err := os.Lstat(at); err != nil || !st.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(at)
	return err == nil && digestPattern.MatchString(strings.TrimSpace(string(b)))
}

// packageName is the name in the manifest of the package unpacked at dir (its id when it has no name), "" when there
// is no manifest to read.
func packageName(dir string) string {
	f, err := os.Open(extension.ManifestPath(dir))
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, extension.ManifestMost+1))
	if err != nil || len(b) > extension.ManifestMost {
		return ""
	}
	var m struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &m) != nil || !extension.ValidID(m.ID) {
		return ""
	}
	if name := strings.TrimSpace(m.Name); name != "" {
		return shorten(name, 40)
	}
	return m.ID
}

// hookProgram is the program a settings.json hook runs: its command when the hook lists args, else the first word of
// the command line (shellFirstWord). A leading ~/, $HOME/ or ${HOME}/ is the home directory, which is how hooks
// written for any machine name it.
func hookProgram(command string, args []string) string {
	p := command
	if args == nil {
		p = shellFirstWord(command)
	}
	for _, prefix := range []string{"~/", "$HOME/", "${HOME}/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return ""
			}
			return filepath.Join(home, rest)
		}
	}
	return p
}
