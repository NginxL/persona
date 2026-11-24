package proto

import (
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	for _, c := range []struct{ in, home, want string }{
		{"~", "/home/user", "/home/user"},
		{"~/", "/home/user", filepath.Clean("/home/user")},
		{"~/src/app", "/home/user", filepath.Join("/home/user", "src", "app")},
		{"~/src/../app", "/home/user", filepath.Join("/home/user", "app")},
		{"/srv/app", "/home/user", "/srv/app"},
		{"", "/home/user", ""},
		{"~other/app", "/home/user", "~other/app"},
		{"src/~", "/home/user", "src/~"},
		// An unknown home leaves the path for whoever knows it (the node itself).
		{"~", "", "~"},
		{"~/src", "", "~/src"},
	} {
		if got := ExpandHome(c.in, c.home); got != c.want {
			t.Errorf("ExpandHome(%q, %q) = %q, want %q", c.in, c.home, got, c.want)
		}
	}
}

func TestExpandHomeNativeSeparators(t *testing.T) {
	home := t.TempDir()
	for _, input := range []string{`~\`, `~\src\app`, `~\src\..\app`} {
		want := input // A backslash is part of a POSIX filename, not a home separator.
		if filepath.Separator == '\\' {
			want = filepath.Join(home, input[2:])
		}
		if got := ExpandHome(input, home); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", input, got, want)
		}
		if got := ExpandHome(input, ""); got != input {
			t.Errorf("unknown home changed %q to %q", input, got)
		}
	}
}
