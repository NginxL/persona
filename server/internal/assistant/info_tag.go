package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Tagging information with categories (#13246, docs/assistant/health.md "Information categories"). Collecting is not
// limited: every assistant conversation is tagged, whatever track it runs in, so its information can follow its
// categories wherever it is used. A conversation's categories are its track's default categories (a floor nobody
// takes away) and what was tagged since: by the info.tag use after each turn, by an agent, or by the user, who alone
// takes one away. The categories only grow otherwise.

const (
	// purposeInfoTag is the model use that tags: Sorting's model, always (models.go).
	purposeInfoTag = "info.tag"
	// infoTagTurnText bounds how much of a turn (what the user said, then the reply) is tagged.
	infoTagTurnText = 1000
	// infoTagTurnTimeout bounds tagging a turn after it ended; a text leaving Persona is tagged with a tighter one.
	infoTagTurnTimeout = 30 * time.Second
	infoTagCacheMax    = 4000
	// infoCategoryStoreTimeout bounds reading or writing one object's categories.
	infoCategoryStoreTimeout = 5 * time.Second
)

// infoCategoryStore keeps the categories (assistant_info_categories).
type infoCategoryStore interface {
	AddInfoCategories(ctx context.Context, kind, ref string, cats []string, by, model string) error
	RemoveInfoCategories(ctx context.Context, kind, ref string, cats []string) error
	InfoCategories(ctx context.Context, kind string, refs []string) (map[string][]db.InfoCategory, error)
}

var _ infoCategoryStore = (*db.DB)(nil)

// Who tags a category (assistant_info_categories.by): the user is the only one who takes one away.
const (
	infoTaggedByModel = "model"
	infoTaggedByAgent = "agent"
	infoTaggedByUser  = db.InfoCategoryByUser
)

// normalCategories are cats as the manager keeps them: known ones only, each once, in order. ok is false when one was
// not a known category (it is left out).
func normalCategories(cats []string) (out []string, ok bool) {
	ok = true
	for _, c := range cats {
		c = strings.TrimSpace(strings.ToLower(c))
		if !slices.Contains(infoCategories, c) {
			ok = false
			continue
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out, ok
}

// conversationCategories are the information categories of conversation sid (a session id, on whichever computer it
// runs): its track's default categories and the ones tagged and not taken away by the user. ok is false when they
// could not all be told (its track or the store could not be read): a use outside its track takes it then for
// sensitive.
func (s *Service) conversationCategories(ctx context.Context, sid string) (cats []string, ok bool) {
	cats, _, ok = s.conversationCategoryState(ctx, sid)
	return cats, ok
}

// conversationCategoryState is conversationCategories, and whether the conversation was ever tagged (the store keeps
// a row for it, infoNoneTagged for none found).
func (s *Service) conversationCategoryState(ctx context.Context, sid string) (cats []string, tagged, ok bool) {
	space, known := s.conversationSpace(ctx, sid)
	return s.categoryStateIn(ctx, sid, space, known)
}

// categoryStateIn is conversationCategoryState of conversation sid of track space, known telling whether its track
// is.
func (s *Service) categoryStateIn(ctx context.Context, sid, space string, known bool) (cats []string, tagged, ok bool) {
	floor, registered := s.trackFloor(ctx, space)
	cats = append(cats, floor...)
	ok = known && registered
	if st, durable := s.store().(infoCategoryStore); durable && sid != "" {
		rctx, cancel := context.WithTimeout(ctx, infoCategoryStoreTimeout)
		rows, err := st.InfoCategories(rctx, db.InfoCategoryConversation, []string{sid})
		cancel()
		if err != nil {
			s.logf("info categories: read %s: %v", sid, err)
			ok = false
		}
		for _, r := range rows[sid] {
			tagged = true
			if !r.RemovedByUser {
				cats = append(cats, r.Category)
			}
		}
	}
	cats, _ = normalCategories(cats)
	return cats, tagged, ok
}

// tagConversation adds cats to conversation sid's categories, by a tagger (model names the model, when one did): a
// category the user took away is not added back unless by is the user. Unknown categories are left out.
func (s *Service) tagConversation(ctx context.Context, sid string, cats []string, by, model string) error {
	cats, _ = normalCategories(cats)
	st, durable := s.store().(infoCategoryStore)
	if !durable || sid == "" || len(cats) == 0 {
		return nil
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), infoCategoryStoreTimeout)
	defer cancel()
	return st.AddInfoCategories(wctx, db.InfoCategoryConversation, sid, cats, by, model)
}

// infoTagTurnSettled tags the turn of assistant conversation machine/sid that just ended and stayed still: what the
// user said and the reply, its first infoTagTurnText characters. A turn that cannot be read or tagged leaves the
// conversation's categories as they were; a meeting's batch is no turn here (turnEnded).
func (s *Service) infoTagTurnSettled(ctx context.Context, machine, sid string) {
	waiting := true // until the turn is tagged, or found to be none to tag
	defer func() {
		if !waiting {
			s.infoTagWaiting.set(sid, false)
		}
	}()
	if _, durable := s.store().(infoCategoryStore); !durable {
		waiting = false
		return
	}
	sess, ok := s.decoratedSession(machine, sid)
	if !ok {
		sess, ok = s.session(ctx, machine, sid)
	}
	if !ok || !sess.Assistant || sess.Starting {
		waiting = ok && sess.Assistant // a conversation still starting is tagged at its turn's end
		return
	}
	if sess.Status == proto.StatusWorking {
		return // a newer turn: tagged when it ends
	}
	ask, answer, err := s.lastTurnOf(ctx, machine, sid)
	if err != nil {
		return
	}
	text := infoTagTurn(ask, answer)
	if text == "" {
		waiting = false
		return
	}
	cats, err := s.infoTag(ctx, text, s.infoTagFocus(ctx, spaceOf(sess)), infoTagTurnTimeout)
	if modelUnconfigured(err) {
		waiting = false
		return
	}
	if err != nil {
		s.logf("info tag %s: %v", sid, infoTagFailure(err))
		return
	}
	if err := s.keepTurnTags(ctx, sid, cats); err != nil {
		s.logf("info categories: tag %s: %v", sid, err)
		return
	}
	waiting = false
}

// keepTurnTags keeps what the model tagged of a turn of conversation sid: its categories, or that it was tagged with
// none (infoNoneTagged).
func (s *Service) keepTurnTags(ctx context.Context, sid string, cats []string) error {
	cats, _ = normalCategories(cats)
	if len(cats) > 0 {
		return s.tagConversation(ctx, sid, cats, infoTaggedByModel, "")
	}
	st, durable := s.store().(infoCategoryStore)
	if !durable {
		return nil
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), infoCategoryStoreTimeout)
	defer cancel()
	return st.AddInfoCategories(wctx, db.InfoCategoryConversation, sid, []string{infoNoneTagged}, infoTaggedByModel, "")
}

// infoTagTurn is the text tagged of a turn: what the user said, then the reply, clipped to infoTagTurnText runes.
func infoTagTurn(ask, answer string) string {
	text := strings.TrimSpace(strings.TrimSpace(ask) + "\n\n" + strings.TrimSpace(answer))
	if utf8.RuneCountInString(text) > infoTagTurnText {
		text = string([]rune(text)[:infoTagTurnText])
	}
	return text
}

// infoTagFocus are the categories a track's tagging looks for especially (InfoTag.Focus): its effective tag_focus
// (#13472) and its default categories, which everything from it carries anyway (the health space's focus stays health).
// None for the main space or a track the registry does not know.
func (s *Service) infoTagFocus(ctx context.Context, space string) []string {
	e, ok := s.trackEntryOf(ctx, space)
	if !ok {
		return nil
	}
	focus, _ := normalCategories(append(slices.Clone(e.DefaultCategories), e.Config.TagFocus...))
	return focus
}

// infoTagFailure says why tagging failed, never the text: unconfigured, paused, timeout or the error.
func infoTagFailure(err error) string {
	switch {
	case modelUnconfigured(err):
		return "no model set up"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	if paused, _ := modelPaused(err); paused {
		return "model paused"
	}
	return err.Error()
}

// infoTag tags text with the information categories it carries, by the info.tag use; timeout bounds the model's
// call alone, not reading which model it is. Answers are kept by the text, the focus and the model, so the same text
// is tagged once.
func (s *Service) infoTag(ctx context.Context, text string, focus []string, timeout time.Duration) ([]string, error) {
	focus, _ = normalCategories(focus)
	fp, err := s.modelFingerprint(ctx, purposeInfoTag)
	if err != nil {
		return nil, err
	}
	key := infoTagKey(fp, strings.Join(focus, ","), text)
	if cats, ok := s.infoTags.get(key); ok {
		return cats, nil
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	cats, err := tagInfoWith(cctx, s.purposeModel(purposeInfoTag), text, focus)
	cancel()
	if err != nil {
		return nil, err
	}
	s.infoTags.put(key, cats)
	return cats, nil
}

// tagInfoWith asks m for text's categories. An answer naming a category the manager does not know keeps the known
// ones.
func tagInfoWith(ctx context.Context, m jsonModel, text string, focus []string) ([]string, error) {
	var out struct {
		Categories []string `json:"categories"`
	}
	if err := m.JSON(ctx, prompts.RenderInfoTag(prompts.InfoTag{Focus: focus}), text, &out); err != nil {
		return nil, err
	}
	cats, _ := normalCategories(out.Categories)
	return cats, nil
}

// infoTagCache keeps tagging answers, crudely bounded like translateCache.
type infoTagCache struct {
	mu sync.Mutex
	m  map[string][]string
}

func (c *infoTagCache) get(k string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return slices.Clone(v), ok
}

func (c *infoTagCache) put(k string, v []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= infoTagCacheMax {
		c.m = map[string][]string{}
	}
	c.m[k] = slices.Clone(v)
}

// infoTagKey keys an answer by the categories' version, the model and the input.
func infoTagKey(parts ...string) string {
	sum := sha1.Sum([]byte(strconv.Itoa(infoCategoriesVersion) + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
