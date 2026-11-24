package assistant

import (
	"context"
	"strings"
	"sync"
	"time"
)

// What usage is told of a piece of information on its way somewhere (#13246, docs/assistant/health.md "Using
// information by scene"). Two sources, by scene:
//
//   - A text leaving Persona (handed to an agent, a push, sent to other people, written to the knowledge base: scenes
//     3, 4, 5 and 7) carries its own categories, tagged on the spot within infoTagExitTimeout, and its track's default
//     categories (exitUse). Never the conversation's accumulated ones: a long conversation such as the main chat
//     would soon carry every category and have everything confirmed. When the text cannot be tagged in time, a text
//     from the main space is taken for general; in any other track the conversation's categories are taken instead,
//     and a conversation never tagged is taken for sensitive.
//   - A conversation itself (read by an extension, recalled elsewhere: scenes 6 and 2) carries its categories
//     (conversationUse); while a turn of it waits to be tagged they are not known.
//
// With no model set up for info.tag nothing is tagged: a text or a conversation then carries its track's default
// categories, as before tagging (the health space's every switch stays on, the main space's off).

// infoTagExitTimeout bounds tagging a text on its way out.
const infoTagExitTimeout = 3 * time.Second

// infoTagPending are the conversations with a turn ended and not tagged yet (or whose tagging failed), by session id.
// Kept in memory: a restart forgets them, and they read as tagged by what was kept.
type infoTagPending struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (p *infoTagPending) set(sid string, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !on {
		delete(p.ids, sid)
		return
	}
	if p.ids == nil {
		p.ids = map[string]bool{}
	}
	p.ids[sid] = true
}

func (p *infoTagPending) has(sid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ids[sid]
}

// infoTagConfigured: the info.tag use resolves to a model it can call (and tagging is not switched off: noInfoTag).
func (s *Service) infoTagConfigured(ctx context.Context) bool {
	if s.noInfoTag {
		return false
	}
	b, err := s.resolveModel(ctx, purposeInfoTag)
	return err == nil && s.modelUsable(ctx, b)
}

// infoTagging: what an assistant conversation says is tagged here (a store that keeps the categories, a model for
// info.tag).
func (s *Service) infoTagging(ctx context.Context) bool {
	_, durable := s.store().(infoCategoryStore)
	return durable && s.infoTagConfigured(ctx)
}

// infoTagTurnEnded: a turn of conversation sid ended; until it is tagged, the conversation's categories are not known.
func (s *Service) infoTagTurnEnded(ctx context.Context, sid string) {
	if sid != "" && s.infoTagging(ctx) {
		s.infoTagWaiting.set(sid, true)
	}
}

// trackFloor is what everything from track space carries at least: its default categories (none for the main
// space), and whether the track is one the registry knows (one it does not is taken for sensitive).
func (s *Service) trackFloor(ctx context.Context, space string) ([]string, bool) {
	if space == "" {
		return nil, true
	}
	e, ok := s.trackEntryOf(ctx, space)
	return e.DefaultCategories, ok
}

// conversationUse is conversation sid's information as a whole, for an extension reading it or a recall elsewhere
// (scenes 6 and 2): its categories (conversationCategories), not known while a turn of it waits to be tagged or when
// they could not be read.
func (s *Service) conversationUse(ctx context.Context, sid string) infoUse {
	space, _ := s.conversationSpace(ctx, sid)
	cats, _, ok := s.conversationCategoryState(ctx, sid)
	return infoUse{Categories: cats, Known: ok && !s.infoTagWaiting.has(sid), SourceTrack: space, Rules: s.trackRules(ctx, space)}
}

// exitUse is text's information on its way out of conversation sid of track space (scenes 3, 4, 5 and 7): the
// categories tagged of the text within infoTagExitTimeout, and its track's default categories. A track already
// sensitive by default asks no model; with none set up the text carries its track's categories. A text that cannot be
// tagged in time carries its conversation's categories when the conversation was tagged and none of its turns waits,
// else it is taken for sensitive.
func (s *Service) exitUse(ctx context.Context, text, sid, space string) infoUse {
	floor, known := s.trackFloor(ctx, space)
	info := infoUse{Categories: floor, Known: known, SourceTrack: space, Rules: s.trackRules(ctx, space)}
	text = strings.TrimSpace(text)
	if !known || len(floor) > 0 || text == "" || !s.infoTagConfigured(ctx) {
		return info
	}
	cats, err := s.infoTag(ctx, infoTagClip(text), s.infoTagFocus(ctx, space), infoTagExitTimeout)
	if err == nil || modelUnconfigured(err) {
		info.Categories, _ = normalCategories(append(cats, floor...))
		return info
	}
	s.logf("info tag: a text leaving %q: %v", sid, infoTagFailure(err))
	if space == "" && sid != "" {
		// The main space is general by default: a text from one of its conversations that could not be tagged in
		// time is used as general, not as sensitive. Taking it for sensitive asked the phone about nearly every
		// hand-off (the model rarely answers within the timeout, and a busy conversation always has a turn waiting
		// to be tagged), redacted its pushes and refused its knowledge-base writes. A text tagged sensitive in time
		// is still confirmed; tracks other than the main one stay strict.
		return info
	}
	c, tagged, ok := s.conversationCategoryState(ctx, sid)
	info.Categories = c
	info.Known = sid != "" && ok && tagged && !s.infoTagWaiting.has(sid)
	return info
}

// infoTagClip is the part of a text tagged: its first infoTagTurnText runes.
func infoTagClip(text string) string {
	if r := []rune(text); len(r) > infoTagTurnText {
		return string(r[:infoTagTurnText])
	}
	return text
}

// exitPolicy is the policy of text leaving conversation sid of track space (exitUse).
func (s *Service) exitPolicy(ctx context.Context, text, sid, space string) TrackPolicy {
	return policyOf(s.exitUse(ctx, text, sid, space))
}

// conversationPolicy is the policy of conversation sid as a whole (conversationUse). The extension side and the
// main chat's reads still hide by track; they switch to this in the next step (#13246).
func (s *Service) conversationPolicy(ctx context.Context, sid string) TrackPolicy {
	return policyOf(s.conversationUse(ctx, sid))
}

// infoNoneTagged is the category kept for a conversation tagged with none: it says the conversation was tagged, and
// is no category (normalCategories leaves it out).
const infoNoneTagged = ""
