package proto

import (
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// HerdrTabName returns the live tab label for an Agent in herdr or Herdr App.
func (s Session) HerdrTabName() string {
	if s.Assistant || s.SlotID == "" && s.TabID == "" {
		return ""
	}
	kind := s.SpaceKind
	if kind == "" {
		kind = s.Host
	}
	if kind != "" && kind != "herdr" && kind != "herdr-core" {
		return ""
	}
	return strings.TrimSpace(s.SlotName)
}

// Keep this recognizer in sync with web/src/lib/format.ts. Whitespace after the
// colon is optional for absolute or home-relative paths, as in default bash titles.
var shellPromptTitle = lazyre.New(`^[A-Za-z0-9_][A-Za-z0-9_.-]*@[A-Za-z0-9_][A-Za-z0-9_.-]*:(?:[ \t]+[^\r\n]+|[~/][^\r\n]*)$`)

// DisplayTabName returns the primary tab name when preferred or the CLI title is empty or a shell prompt.
func (s Session) DisplayTabName(preferTab bool) string {
	title := strings.TrimSpace(s.Title)
	if preferTab || title == "" || shellPromptTitle.MatchString(title) {
		return s.HerdrTabName()
	}
	return ""
}
