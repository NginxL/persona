package assistant

import (
	"cmp"
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// A conversation in a track other than the main space (a domain space, the health space) gets a short context of its
// own with each message instead of the main chat's snapshot (#13499, docs/assistant/health.md "Tracks and kinds"):
// the time, the track and the owner's language. What the snapshot tells (Agents, computers, unread Lark chats, tasks,
// the knowledge base) is the main chat's work, and a track's conversation neither acts on it nor should see it.

// spaceContextMax bounds a track conversation's context, in characters.
const spaceContextMax = 600

// spaceOwnerMax bounds the owner's name in it, in characters (a track's title is bounded by trackTitleMax).
const spaceOwnerMax = 40

// spaceLookupTimeout bounds asking the conversation's computer for its space when the manager never noted it.
const spaceLookupTimeout = 2 * time.Second

// contextTrack is the track conversation p.SessionID works in when that is one other than the main space, else "".
// The main conversation, a leader and the background supervisor are the main space's and are not looked up, so their
// context is what it was. Another is told as the manager noted it (notedSpaces), else as its computer lists it, a
// directory inside a track counting as the track (runSpace: telling a track's conversation for the main space's would
// show it the snapshot). One that cannot be told (never noted, its computer not answering) gets the main space's.
func (s *Service) contextTrack(ctx context.Context, p ContextParams, background bool) string {
	id := p.SessionID
	if id == "" || background || s.isMain(ctx, id) || s.isLeader(ctx, id) {
		return ""
	}
	if space, ok := s.notedSpaces(ctx, []string{id})[id]; ok {
		return space
	}
	machine := cmp.Or(p.MachineID, s.settings(ctx).MachineID)
	if machine == "" {
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, spaceLookupTimeout)
	defer cancel()
	if c, ok := s.session(lctx, machine, id); ok {
		return runSpace(c)
	}
	return ""
}

// spaceContext is the context of a conversation in track slug (prompts/assistant/space_context.md), at most
// spaceContextMax characters; "" for a track whose kind asks for none (context "none"). A track the registry does not
// know (an archived kind's, one from a newer manager) still gets this one, never the main chat's snapshot, named by
// its slug. A kind asking for the engineering context gets this one too for now: the snapshot is the main
// conversation's, and a track of the orchestration kind has no leaders of its own yet.
func (s *Service) spaceContext(ctx context.Context, slug string) string {
	title := slug
	health := false
	if e, ok := s.trackEntryOf(ctx, slug); ok {
		if e.Config.Context == "none" {
			return ""
		}
		title, health = e.Title, e.Title == "" && slug == SpaceHealth
		if title == "" {
			title = slug
		}
	}
	hello := s.hello(ctx)
	c := prompts.SpaceContext{Now: time.Now().In(s.location(ctx)), Health: health, Language: answerLanguage(hello.Locale),
		Title: textfmt.Cut(oneLine(title), trackTitleMax), Owner: textfmt.Cut(oneLine(hello.Owner), spaceOwnerMax)}
	c.Zone = c.Now.Location().String()
	return textfmt.Cut(prompts.RenderSpaceContext(c), spaceContextMax)
}
