package assistant

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Trying triage on a subscription's dry run (#5278, docs/extensions/subscriptions.md "Pages"): the user asks on the
// pages how the triage model would sort what a pull brought; each thing is made an item only to write its prompt, as a
// round's would be, and asked about once. Nothing is kept: no item, no triage state, no log of what it says.

// triagePreviewMost is the most things one try asks about; triagePreviewWait how long each may take.
const (
	triagePreviewMost = 3
	triagePreviewWait = 45 * time.Second
)

// InboxTriagePreview is inbox.triage_preview: one verdict a thing, in order.
func (s *Service) InboxTriagePreview(ctx context.Context, p assistantwire.InboxTriagePreview) []assistantwire.InboxTriageVerdict {
	things := p.Things
	if len(things) > triagePreviewMost {
		things = things[:triagePreviewMost]
	}
	source := p.Source
	if source == "" {
		source = "-preview" // no source has it: its kinds come with the request
	}
	hello := s.hello(ctx)
	language := answerLanguage(hello.Locale)
	b := &s.inbox
	out := make([]assistantwire.InboxTriageVerdict, len(things))
	var wg sync.WaitGroup
	for i, t := range things {
		it := &InboxItem{ID: "preview", Source: source}
		fillConnectorItem(it, t)
		b.mu.Lock()
		item := b.triagePromptItem(it, language)
		b.mu.Unlock()
		if item.Described == "" {
			item.Described = p.Kinds[it.Kind]
		}
		input := prompts.RenderInboxItem(item)
		wg.Go(func() {
			v := s.triageModelIn(ctx, input, it.Rules, triagePreviewWait, hello)
			out[i] = assistantwire.InboxTriageVerdict{Tier: v.tier, Summary: v.summary, Reason: v.reason, Urgent: v.urgent,
				State: v.state, Why: v.why}
		})
	}
	wg.Wait()
	return out
}
