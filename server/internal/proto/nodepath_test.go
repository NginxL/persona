package proto

import "testing"

// A Windows node's paths read as that node wrote them on a hub of any system.
func TestNodePaths(t *testing.T) {
	for _, c := range []struct {
		in    string
		abs   bool
		clean string
	}{
		{`C:\Users\me\code\app`, true, `C:\Users\me\code\app`},
		{`C:\Users\me\code\app\`, true, `C:\Users\me\code\app`},
		{`c:/Users/me/code/app/`, true, `C:\Users\me\code\app`},
		{`C:\code\.\app\..\lib`, true, `C:\code\lib`},
		{`C:\..\..\code`, true, `C:\code`},
		{`C:\`, true, `C:\`},
		{`C:/`, true, `C:\`},
		{`\\server\share\me\code\`, true, `\\server\share\me\code`},
		{`\\server\share`, true, `\\server\share`},
		{`\\server\share\`, true, `\\server\share`},
		{`\\server/share/me`, true, `\\server\share\me`},
		{`\\server\share\..`, true, `\\server\share`}, // .. stops at the share
		{`\\server\share\..\other`, true, `\\server\share\other`},
		{`\\server\share\me\..\..\..\x`, true, `\\server\share\x`},
		{`\\`, false, `\\`}, // an incomplete share is no root
		{`\\server`, false, `\\server`},
		{`\\server\`, false, `\\server\`},
		{`\\\share\x`, false, `\\\share\x`},
		{`/home/me/code/`, true, "/home/me/code"},
		{`/home/me/a\b`, true, `/home/me/a\b`}, // a backslash in a Unix name
		{`C:code`, false, "C:code"},            // relative to the drive's current directory
		{`code\app`, false, `code\app`},
		{"~/code", false, "~/code"},
		{"ws-one", false, "ws-one"},
	} {
		if got := NodeAbs(c.in); got != c.abs {
			t.Errorf("NodeAbs(%q) = %v, want %v", c.in, got, c.abs)
		}
		if got := NodeClean(c.in); got != c.clean {
			t.Errorf("NodeClean(%q) = %q, want %q", c.in, got, c.clean)
		}
	}
	for _, c := range [][3]string{
		{`C:\wt\app`, "fix-login", `C:\wt\app\fix-login`},
		{`C:\wt\app\`, "fix-login", `C:\wt\app\fix-login`},
		{`\\server\share\wt`, "fix-login", `\\server\share\wt\fix-login`},
		{`\\server\share`, "fix-login", `\\server\share\fix-login`},
		{`C:\`, "fix-login", `C:\fix-login`},
		{"/wt/app", "fix-login", "/wt/app/fix-login"},
	} {
		if got := NodeJoin(c[0], c[1]); got != c[2] {
			t.Errorf("NodeJoin(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// A Windows directory is one whatever its case; a Unix one is not.
func TestNodeKey(t *testing.T) {
	for _, same := range [][2]string{
		{`C:\Code\App`, `c:\code\app\`},
		{`C:/Users/Me/code`, `c:\users\me\CODE`},
		{`\\Server\Share\App`, `\\server\share\app`},
	} {
		if NodeKey(same[0]) != NodeKey(same[1]) {
			t.Errorf("NodeKey(%q) = %q, NodeKey(%q) = %q, want one key", same[0], NodeKey(same[0]), same[1], NodeKey(same[1]))
		}
	}
	for _, other := range [][2]string{
		{"/home/me/Code", "/home/me/code"},
		{`C:\code\app`, `C:\code\app2`},
		{`\\server\share\app`, `\\server\other\app`},
	} {
		if NodeKey(other[0]) == NodeKey(other[1]) {
			t.Errorf("NodeKey(%q) and NodeKey(%q) are both %q, want two keys", other[0], other[1], NodeKey(other[0]))
		}
	}
}
