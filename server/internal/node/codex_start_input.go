package node

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/screen"
)

// A terminal can display a paste as several chips with literal text between or
// after them. Chips only confirm the composer took input, never its full content:
// the startup caller still requires the complete first message in the rollout.
var codexPasteChipRE = lazyre.New(`\[Pasted Content ([0-9]+) chars\](?: #([0-9]+))?`)

func codexFirstPromptShown(draft, prompt string) bool {
	if sameText(draft, prompt) {
		return true
	}
	matches := codexPasteChipRE.FindAllStringSubmatchIndex(draft, -1)
	if len(matches) == 0 {
		return false
	}
	// Codex normalizes terminal line endings before counting a paste's chars.
	expected := []rune(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(prompt))
	// Match every visible rune in order, allowing only whitespace to disappear
	// when the terminal wraps/trims a row. Keep possible whitespace boundaries:
	// the next chip must consume exactly its advertised number of Unicode chars.
	visibleEnds := func(starts map[int]bool, text string) map[int]bool {
		ends := map[int]bool{}
		for start := range starts {
			at, matched := start, true
			for _, r := range text {
				if unicode.IsSpace(r) {
					continue
				}
				for at < len(expected) && unicode.IsSpace(expected[at]) {
					at++
				}
				if at == len(expected) || expected[at] != r {
					matched = false
					break
				}
				at++
			}
			if matched {
				for {
					ends[at] = true
					if at == len(expected) || !unicode.IsSpace(expected[at]) {
						break
					}
					at++
				}
			}
		}
		return ends
	}
	positions := map[int]bool{0: true}
	last := 0
	for _, m := range matches {
		n, err := strconv.Atoi(draft[m[2]:m[3]])
		if err != nil || n <= 0 || n > len(expected) {
			return false
		}
		// Codex disambiguates equal-sized paste elements with #2, #3, etc.
		if m[4] >= 0 {
			suffix, err := strconv.Atoi(draft[m[4]:m[5]])
			if err != nil || suffix < 2 {
				return false
			}
		}
		next := map[int]bool{}
		for at := range visibleEnds(positions, draft[last:m[0]]) {
			if n <= len(expected)-at {
				next[at+n] = true
			}
		}
		if len(next) == 0 {
			return false
		}
		positions, last = next, m[1]
	}
	// A short chip with a tail still in flight must not trigger Enter. Matching
	// the full character budget is readiness only; the rollout proves content.
	return visibleEnds(positions, draft[last:])[len(expected)]
}

// typeCodexFirst handles only startup, including a waiting row's first message.
// No update is installed. Skip at most one recognized offer, and replay a paste
// at most once, only into an empty unqueued composer after that skip. There has
// been no Enter at that point. Never clear or append to a different draft.
func (n *Node) typeCodexFirst(ctx context.Context, pane, prompt string) error {
	confirm, cancel := context.WithTimeout(ctx, 4*time.Second+codexDialogPause)
	defer cancel()
	skipped, replayed := false, false
	skipUpdate := func() (bool, error) {
		if skipped {
			return false, nil
		}
		text, err := n.readPane(confirm, pane, "visible", screen.DialogLines)
		if err != nil {
			return false, err
		}
		keys := codexSkipUpdateKeys(text)
		if keys == nil {
			return false, nil
		}
		if err := n.sendKeys(confirm, pane, keys...); err != nil {
			return false, err
		}
		skipped = true
		if !sleepCtx(confirm, codexDialogPause) {
			return true, confirm.Err()
		}
		return true, nil
	}
	for {
		if err := n.codexReady(confirm, pane, true); err != nil {
			ok, skipErr := skipUpdate()
			if skipErr != nil {
				return skipErr
			}
			if ok {
				continue
			}
			return err
		}
		break
	}
	paste := func() error { return n.sendText(confirm, pane, pasteStart+inertControls(prompt)+pasteEnd) }
	if err := paste(); err != nil {
		return err
	}
	var s screen.CodexScreen
	postPasteSkipped := false
	for {
		if !sleepCtx(confirm, codexStep) {
			return uncertain(errors.New(i18n.T(i18n.Default, "node-codex-start.paste_unconfirmed", i18n.Args{"view": s.View})))
		}
		var err error
		if s, err = n.codexScreen(confirm, pane); err != nil {
			return uncertain(err)
		}
		if s.View == screen.CodexComposer {
			if len(s.Queued) == 0 && codexFirstPromptShown(s.Draft, prompt) {
				return uncertain(n.sendKeys(confirm, pane, "enter"))
			}
			if postPasteSkipped && !replayed && s.Draft == "" && len(s.Queued) == 0 {
				replayed = true
				if err := paste(); err != nil {
					return uncertain(err)
				}
			}
			continue
		}
		if ok, err := skipUpdate(); err != nil {
			return uncertain(err)
		} else if ok {
			postPasteSkipped = true
		}
	}
}
