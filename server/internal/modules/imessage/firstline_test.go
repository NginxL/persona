package imessage

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A message cut to its first line ends on a whole character: a line in Chinese takes three bytes a character, and a
// cut at 200 bytes fell inside one.
func TestFirstLineCutsOnWholeCharacters(t *testing.T) {
	short := strings.Repeat("无法连接服务", 20) // 120 characters, 360 bytes: under the limit, kept whole
	if got := firstLine("  "+short+"\nsecond line", nil); got != short {
		t.Errorf("a line under the limit came back as %q", got)
	}
	long := strings.Repeat("无法连接服务", 50) // 300 characters
	got := firstLine(long+"\nsecond line", nil)
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("the cut line is not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 200 || !strings.HasPrefix(long, got) {
		t.Errorf("cut line has %d characters, want the first 200 of the line", n)
	}
	ascii := strings.Repeat("x", 300)
	if got := firstLine(ascii, nil); got != ascii[:200] {
		t.Errorf("an ASCII line is %d bytes, want 200", len(got))
	}
}
