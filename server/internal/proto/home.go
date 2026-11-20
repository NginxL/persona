package proto

import (
	"path"
	"path/filepath"
	"strings"
)

// ExpandHome makes a leading "~" in p the given machine's home: "~" is home itself, "~/x" is x under home. Anything
// else, and every p when home is unknown (""), comes back unchanged. Only the user's own home is meant: "~user" stays.
func ExpandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// HomeNamed is dir with a home reached through a link written as the node names it: dir at or under real (the home
// with its links followed, NodeInfo.HomeReal) comes back at or under home, so /data/home/me/x is /home/me/x when
// /home/me -> /data/home/me. Anything else, and every dir when either home is unknown or they are the same, comes back
// unchanged.
func HomeNamed(dir, home, real string) string {
	if home == "" || real == "" || !strings.HasPrefix(home, "/") || !strings.HasPrefix(real, "/") {
		return dir
	}
	home, real = path.Clean(home), path.Clean(real)
	if home == real {
		return dir
	}
	if d := path.Clean(dir); d == real {
		return home
	} else if rest, ok := strings.CutPrefix(d, real+"/"); ok {
		return path.Join(home, rest)
	}
	return dir
}
