package update

import (
	"path/filepath"
	"strings"
)

// Artifact aliases are exact release identifiers. A fallback never changes the
// manifest's size or either digest, and is tried only after a read-only 404.
const (
	legacyDaemon = "jarvisd"
	legacyWechat = "jarvisd-wechat"
)

func artifactNames(name string) []string {
	names := []string{name}
	for _, pair := range [][2]string{{"personad-wechat", legacyWechat}, {"personad", legacyDaemon}} {
		for i := range 2 {
			if rest, ok := strings.CutPrefix(name, pair[i]+"-"); ok {
				return append(names, pair[1-i]+"-"+rest)
			}
		}
	}
	return names
}

func artifactIdentity(name string) (program, platform string) {
	base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(name), ".gz"), ".exe")
	for _, prog := range SplitPrograms {
		if platform, ok := strings.CutPrefix(base, prog+"-"); ok {
			return prog, platform
		}
	}
	for _, pair := range [][2]string{{"personad-wechat", legacyWechat}, {"personad", legacyDaemon}} {
		for _, prefix := range pair {
			if platform, ok := strings.CutPrefix(base, prefix+"-"); ok {
				if pair[0] == "personad" {
					return "", platform
				}
				return pair[0], platform
			}
		}
	}
	return "", ""
}

func compatiblePrograms(m *Manifest) {
	if m.Programs == nil {
		return
	}
	canonical, legacy := m.Programs["personad-wechat"], m.Programs[legacyWechat]
	if canonical == nil && legacy == nil {
		return
	}
	if canonical == nil {
		canonical = map[string]File{}
	}
	if legacy == nil {
		legacy = map[string]File{}
	}
	for platform, f := range legacy {
		if _, ok := canonical[platform]; !ok {
			canonical[platform] = f
		}
	}
	for platform, f := range canonical {
		if _, ok := legacy[platform]; !ok {
			legacy[platform] = f
		}
	}
	m.Programs["personad-wechat"], m.Programs[legacyWechat] = canonical, legacy
}

func manifestArtifacts(dir string) ([]string, error) {
	var files []string
	// Legacy first, canonical second: canonical wins a platform collision.
	for _, name := range append([]string{legacyDaemon, "personad"}, SplitPrograms...) {
		matches, err := filepath.Glob(filepath.Join(dir, name+"-*-*.gz"))
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	return files, nil
}
