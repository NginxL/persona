package subscriptions

import (
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// TriagePreview is what the Inbox would be handed for a dry run's items (#5278): each one as a run's item comes in
// (one thread row each, its context, rules and fallback as delivered), with the source's kinds, for the pages to have
// the triage model try them. Nothing of it is kept.
type TriagePreview struct {
	// Source is the subscription's id, "" for a source nobody subscribed to yet.
	Source string                 `json:"source"`
	Name   string                 `json:"name"`
	Kinds  map[string]string      `json:"kinds"`
	Things []connector.InboxThing `json:"things"`
}

// PreviewThings are a dry run's items as sub would hand them to the Inbox; sub may be a source's that is not
// subscribed yet (its label and recipe filled in).
func (s *Service) PreviewThings(sub Subscription, items []Preview) TriagePreview {
	d := s.deliverer(sub, time.Now())
	out := TriagePreview{Source: sub.ID, Name: sub.Label, Kinds: kindsOf(sub.Label, s.o.Lang()), Things: []connector.InboxThing{}}
	for i, it := range items {
		h := Held{Seq: int64(i), Stored: d.now, Item: Item{UID: it.UID, Thread: it.Thread, Title: it.Title, Sender: it.Sender,
			Body: it.Body, Link: it.Link, Kind: sub.Recipe.Kind, Time: previewTime(it.Time), DueDate: previewTime(it.DueDate)}}
		out.Things = append(out.Things, d.threadThing(cmpOr(it.Thread, it.UID), []Held{h}))
	}
	return out
}

// previewTime is a dry run's time back as a time (it writes RFC 3339); nil when there is none.
func previewTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	return &t
}
