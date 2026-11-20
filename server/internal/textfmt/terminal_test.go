package textfmt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStripTerminalEscapes(t *testing.T) {
	cases := map[string]string{
		"plain":                 "plain text",
		"colour":                "\x1b[1;32mok\x1b[0m",
		"csi private":           "\x1b[?25lhidden\x1b[?25h",
		"csi intermediate":      "\x1b[1 qcursor",
		"csi with colon":        "\x1b[38:2:255:0:0mred\x1b[m",
		"csi unterminated":      "a\x1b[1;3",
		"c1 csi":                "\u009b1;32mok\u009b0m",
		"osc bel":               "a\x1b]0;title\x07b",
		"osc st":                "a\x1b]8;;https://example.test\x1b\\link\x1b]8;;\x1b\\b",
		"osc c1 st":             "a\x1b]0;title\u009cb",
		"osc 1337 host and cwd": "\x1b]1337;RemoteHost=dev@workstation\x07\x1b]1337;CurrentDir=/Users/dev\x07done",
		"osc 52 clipboard":      "\x1b]52;c;aGVsbG8=\x07x",
		"c1 osc":                "a\u009d0;title\x07b",
		"dcs":                   "a\x1bPq#0;2;0;0;0\x1b\\b",
		"c1 dcs":                "a\u0090data\u009cb",
		"sos":                   "a\x1bXsos\x1b\\b",
		"pm":                    "a\x1b^pm\x1b\\b",
		"apc":                   "a\x1b_apc\x1b\\b",
		"c1 apc":                "a\u009fapc\u009cb",
		"two byte esc":          "a\x1b=b",
		"esc charset":           "a\x1b(Bb",
		"esc save restore":      "a\x1b7b\x1b8c",
		"esc reverse index":     "a\x1bMb",
		"lone trailing esc":     "a\x1b",
		"crlf":                  "a\r\nb",
		"cr":                    "a\rb",
		"controls":              "a\x00\x07\x0b\x0c\x1f\x7fb",
		"c1 controls":           "a\u0080\u0085\u008f b", // U+00A0 is not a C1 control and stays
		"tabs and newlines":     "a\tb\nc",
		"invalid utf8":          "a\xffb",
		"truncated rune":        "进度\xe6\xad",
		"unicode kept":          "进度 ok 😀",
		"noise only":            "\x1b[32m\x1b[39m",
	}
	// The map above holds the inputs; the expectations are separate, so they cannot drift from them.
	want := map[string]string{
		"plain":                 "plain text",
		"colour":                "ok",
		"csi private":           "hidden",
		"csi intermediate":      "cursor",
		"csi with colon":        "red",
		"csi unterminated":      "a",
		"c1 csi":                "ok",
		"osc bel":               "ab",
		"osc st":                "alinkb",
		"osc c1 st":             "ab",
		"osc 1337 host and cwd": "done",
		"osc 52 clipboard":      "x",
		"c1 osc":                "ab",
		"dcs":                   "ab",
		"c1 dcs":                "ab",
		"sos":                   "ab",
		"pm":                    "ab",
		"apc":                   "ab",
		"c1 apc":                "ab",
		"two byte esc":          "ab",
		"esc charset":           "ab",
		"esc save restore":      "abc",
		"esc reverse index":     "ab",
		"lone trailing esc":     "a",
		"crlf":                  "a\nb",
		"cr":                    "a\nb",
		"controls":              "ab",
		"c1 controls":           "a b",
		"tabs and newlines":     "a\tb\nc",
		"invalid utf8":          "a�b",
		"truncated rune":        "进度��",
		"unicode kept":          "进度 ok 😀",
		"noise only":            "",
	}
	for name, in := range cases {
		if got := StripTerminalEscapes(in); got != want[name] {
			t.Errorf("%s: StripTerminalEscapes(%q) = %q, want %q", name, in, got, want[name])
		}
	}
}

// A truncated OSC stops at the newline, so the error line after it survives.
func TestStripTerminalEscapesUnterminatedOSCStopsAtNewline(t *testing.T) {
	in := "\x1b]1337;CurrentDir=/Users/dev\nError: no such file"
	if got, want := StripTerminalEscapes(in), "\nError: no such file"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := LauncherErrorSummary(in, 400), "Error: no such file"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

// Text with nothing to strip comes back as it is and costs nothing; the fast path is the common case (a check's answer).
func TestStripTerminalEscapesFastPathAllocations(t *testing.T) {
	plain := "API Error: 401 invalid x-api-key\n" + strings.Repeat("进度 ok 😀\n", 20)
	if got := StripTerminalEscapes(plain); got != plain {
		t.Fatal("fast path changed clean text")
	}
	if a := testing.AllocsPerRun(100, func() { stripSink = StripTerminalEscapes(plain) }); a != 0 {
		t.Fatalf("clean text allocates %v times", a)
	}
	coloured := "\x1b[32mok\x1b[0m"
	if got, want := StripTerminalEscapes(coloured), "ok"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

var stripSink string

func TestTailClip(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abc", 5, "abc"},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "…bcdef"},
		{"progress done", 5, "… done"},
		{"进度同步一下", 3, "…步一下"},
		{"a\xffbcd", 3, "…bcd"},
		{"ab\xff", 2, "…b\xff"},
		{"abc", 0, "…"},
		{"abc", -1, "…"},
	}
	for _, c := range cases {
		if got := TailClip(c.in, c.n); got != c.want {
			t.Errorf("TailClip(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if c.n >= 0 && utf8.RuneCountInString(TailClip(c.in, c.n)) > c.n+1 {
			t.Errorf("TailClip(%q, %d) is longer than n+1 runes", c.in, c.n)
		}
	}
}

func TestLauncherErrorSummary(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"startup noise lines",
			"Restart Zsh to retry gitstatus initialization: \x1b[32mexec zsh\x1b[39m\n" +
				"\x1b]1337;RemoteHost=dev@workstation\x07\x1b]1337;CurrentDir=/Users/dev\x07\n" +
				"Error: No Cursor IDE installation found.",
			"Error: No Cursor IDE installation found.",
		},
		{
			"flattened startup noise and an error on one line",
			"Restart Zsh to retry gitstatus initialization: \x1b[32mexec zsh\x1b[39m " +
				"\x1b]1337;RemoteHost=dev@workstation\x07\x1b]1337;CurrentDir=/Users/dev\x07... " +
				"Error: No Cursor IDE installation found.",
			"Error: No Cursor IDE installation found.",
		},
		{"plain api error", "API Error: 401 invalid x-api-key", "API Error: 401 invalid x-api-key"},
		{"coloured command not found", "\x1b[1mclaude\x1b[0m: command not found", "claude: command not found"},
		{"chinese timeout", "90 秒内没有回答", "90 秒内没有回答"},
		{"percent and blank lines", "%\n\nzsh: no such file\n%", "zsh: no such file"},
		{"warning prefix", "[WARNING]: Console output during zsh initialization detected.\nAPI Error: 403 forbidden", "API Error: 403 forbidden"},
		{"oh my zsh update question", "[oh-my-zsh] Would you like to update?\nAPI Error: 401 invalid key", "API Error: 401 invalid key"},
		{"the last error wins", "Error: first\nwarn: something\nfatal: second", "fatal: second"},
		{"traceback keeps its header", "Traceback (most recent call last):\n  File x.py\nValueError: bad input", "Traceback (most recent call last): ValueError: bad input"},
		{"short trailing detail", "stderr: could not start\nError: exit status 1", "Error: exit status 1"},
		{"no error word", "hello there\nsecond line", "hello there second line"},
		{"only noise falls back to it", "Last login: yesterday", "Last login: yesterday"},
		{"whitespace only", " \n\t\n", ""},
		{"empty", "", ""},
		{"single percent", "%", "%"},
	}
	for _, c := range cases {
		if got := LauncherErrorSummary(c.in, 400); got != c.want {
			t.Errorf("%s: LauncherErrorSummary(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// A long summary keeps its end: the error's own words are never the part cut off.
func TestLauncherErrorSummaryClipsFromTheFront(t *testing.T) {
	tail := "Error: the real cause is " + strings.Repeat("very long detail ", 60) + "END"
	in := strings.Repeat("noise line without an error\n", 60) + tail
	got := LauncherErrorSummary(in, 400)
	if !strings.HasSuffix(got, "detail END") {
		t.Fatalf("summary does not end with the error: %q", got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("summary was not tail-clipped: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > 400 {
		t.Fatalf("summary is %d runes, want at most 400", n)
	}
}

func TestLauncherErrorSummaryKeepsPlainUnicode(t *testing.T) {
	in := "构建失败\nError: 权限不足"
	if got := LauncherErrorSummary(in, 400); got != "Error: 权限不足" {
		t.Fatalf("got %q", got)
	}
}
