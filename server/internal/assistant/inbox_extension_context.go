package assistant

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The Inbox's half of what extensions wrote for the prompts (extension_context.go): the context its triage and drafts
// get, read at most once a minute, and the sentences a draft must not repeat.

// inboxExtensionEvery is how long the Inbox's extension context is kept: triage asks for it for every item.
const inboxExtensionEvery = time.Minute

// inboxExtensionCache is the Inbox's extension context as last read, and when.
type inboxExtensionCache struct {
	mu  sync.Mutex
	at  time.Time
	res assistantwire.ExtensionContextResult
}

// inboxExtensionContext is what the extensions wrote for the Inbox, and the sentences a draft must not repeat.
func (s *Service) inboxExtensionContext(ctx context.Context) ([]prompts.ExtensionContext, []string) {
	c := &s.inboxExt
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || time.Since(c.at) >= inboxExtensionEvery {
		var res assistantwire.ExtensionContextResult
		if err := s.appCall(ctx, AppPlugins, PluginsInboxContext, map[string]any{}, &res); err != nil {
			res = assistantwire.ExtensionContextResult{}
		}
		c.res, c.at = res, time.Now()
	}
	var out []prompts.ExtensionContext
	for _, b := range c.res.Blocks {
		out = append(out, prompts.ExtensionContext{Name: oneLine(b.Name), Text: b.Text})
	}
	return out, c.res.NeverQuote
}

// neverQuoteShortest is the fewest letters and digits a never-quote sentence must have to be checked: a shorter one
// ("OK", "thanks") would stop ordinary drafts.
const neverQuoteShortest = 6

// quoteKey is s as never-quote sentences are compared: compatibility-normalized (full width as half width), lower
// case, letters and digits only, so spacing, punctuation and case do not hide a repeat.
func quoteKey(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return -1
	}, s)
}

// quotesAny is the first of sentences that text repeats (quoteKey contains its key), "" for none.
func quotesAny(text string, sentences []string) string {
	key := quoteKey(text)
	for _, sentence := range sentences {
		k := quoteKey(sentence)
		if len([]rune(k)) >= neverQuoteShortest && strings.Contains(key, k) {
			return sentence
		}
	}
	return ""
}

// errDraftWithheld: the draft repeated a sentence an extension marked never to quote, and so did the rewrite; no draft.
var errDraftWithheld = i18n.Err("assistant-inbox_triage.draft_withheld")
