package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// User-defined tags of assistant conversations (issue #9719): zero or more short labels per conversation, editable by
// both the user and the assistant. The book follows assistant.archived (conversations.go): the assistant owns the data
// in its settings store, keyed by conversation id, with the machine id and who changed it; the hub only forwards the
// routes. No database table or migration.
const conversationTagsKey = "assistant.conversation_tags"

const (
	// conversationTagMaxRunes is the most runes a tag keeps after trimming.
	conversationTagMaxRunes = 32
	// conversationTagsMax is the most tags one conversation can carry.
	conversationTagsMax = 10
)

// conversationTags is one conversation's current tag set and the provenance of its last change.
type conversationTags struct {
	MachineID string    `json:"machine_id"`
	Tags      []string  `json:"tags"`
	At        time.Time `json:"at"`
	By        string    `json:"by"` // user | assistant (titles.go's titleBy*)
}

type conversationTagsBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*conversationTags // by session id
}

// tagsLocked reads the book once; b.mu is held. A failed read leaves it unloaded (nothing is saved over it).
func (s *Service) tagsLocked(ctx context.Context) bool {
	b := &s.tags
	if b.loaded {
		return true
	}
	m := map[string]*conversationTags{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, conversationTagsKey, &m); err != nil {
			s.logf("load conversation tags: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*conversationTags{}
	}
	b.m, b.loaded = m, true
	return true
}

// conversationTagsSet returns the tags by conversation id (a copy of each list); false while the book could not be read.
func (s *Service) conversationTagsSet() (map[string][]string, bool) {
	records, ok := s.conversationTagsRecords()
	if !ok {
		return nil, false
	}
	out := make(map[string][]string, len(records))
	for id, t := range records {
		if len(t.Tags) > 0 {
			out[id] = slices.Clone(t.Tags)
		}
	}
	return out, true
}

// conversationTagsRecords snapshots the map under its lock; writers replace records rather than mutating them.
// A publisher may iterate this snapshot after unlocking, including while a later write adds or drops tags.
func (s *Service) conversationTagsRecords() (map[string]*conversationTags, bool) {
	b := &s.tags
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.tagsLocked(context.Background()) {
		return nil, false
	}
	return maps.Clone(b.m), true
}

var (
	errTagTooLong = i18n.Err("assistant-conversation_tags.tag_too_long", i18n.Args{"max": conversationTagMaxRunes})
	errTagTooMany = i18n.Err("assistant-conversation_tags.too_many", i18n.Args{"max": conversationTagsMax})
)

// normalizeConversationTags applies every write's rules: surrounding whitespace is trimmed, empty entries are
// dropped, duplicates after trimming collapse to the first occurrence, and the length and count limits are enforced.
// The returned slice is never nil when no error occurs.
func normalizeConversationTags(in []string) ([]string, error) {
	return normalizeTagList(in, conversationTagsMax, errTagTooMany)
}

// normalizeTagList is the shared normalization for a conversation's tags and the user's filter entries.
func normalizeTagList(in []string, max int, tooMany error) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > conversationTagMaxRunes {
			return nil, errTagTooLong
		}
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	if len(out) > max {
		return nil, tooMany
	}
	return out, nil
}

// setConversationTags replaces conversation id's tags after normalization; by is who asked (user | assistant). It
// reports whether anything changed. A conversation that cannot be read (deleted, unknown) is refused.
func (s *Service) setConversationTags(ctx context.Context, machine, id string, tags []string, by string) (bool, error) {
	if !validSessionID(id) || machine == "" {
		return false, errors.New("invalid conversation id")
	}
	if err := s.conversationAvailable(ctx, machine, id); err != nil {
		return false, err
	}
	normalized, err := normalizeConversationTags(tags)
	if err != nil {
		return false, err
	}
	st := s.store()
	if st == nil {
		return false, errors.New("no database")
	}
	b := &s.tags
	b.mu.Lock()
	if !s.tagsLocked(ctx) {
		b.mu.Unlock()
		return false, errors.New("conversation tags could not be read")
	}
	if cur := b.m[id]; cur != nil && slices.Equal(cur.Tags, normalized) {
		b.mu.Unlock()
		return false, nil
	}
	next := &conversationTags{MachineID: machine, Tags: normalized, At: time.Now().UTC(), By: by}
	if len(normalized) == 0 {
		delete(b.m, id) // an empty tag set leaves no record, as one never tagged has none
	} else {
		b.m[id] = next
	}
	if err := st.PutSetting(ctx, conversationTagsKey, b.m); err != nil {
		// Keep memory as the database has it: reload on the next use.
		b.loaded = false
		b.mu.Unlock()
		return false, err
	}
	b.mu.Unlock()
	s.publish(ctx) // the apps get the decorated list with the new tags now; never under the book lock (snapshot re-reads it)
	return true, nil
}

// dropConversationTags deletes a conversation's tag record, used when the conversation itself is deleted
// (conversation_delete.go). A missing record is not an error.
func (s *Service) dropConversationTags(ctx context.Context, id string) error {
	st := s.store()
	if st == nil {
		return errors.New("no database")
	}
	b := &s.tags
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.tagsLocked(ctx) {
		return errors.New("conversation tags could not be read")
	}
	if _, ok := b.m[id]; !ok {
		return nil
	}
	delete(b.m, id)
	if err := st.PutSetting(ctx, conversationTagsKey, b.m); err != nil {
		b.loaded = false
		return err
	}
	return nil
}

// conversationTagRoutes: PUT /api/v1/assistant/conversations/{id}/tags replaces one conversation's tags.
func (s *Service) conversationTagRoutes() {
	s.handle("PUT /api/v1/assistant/conversations/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		id := r.PathValue("id")
		if !validSessionID(id) {
			apierr.Write(w, http.StatusBadRequest, errors.New("invalid conversation id"))
			return
		}
		var in struct {
			Tags []string `json:"tags"`
		}
		if err := decodeBody(w, r, &in, 1<<16); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		machine := s.settings(r.Context()).MachineID
		changed, err := s.setConversationTags(r.Context(), machine, id, in.Tags, titleByUser)
		if err != nil {
			switch {
			case errors.Is(err, errTagTooLong), errors.Is(err, errTagTooMany):
				apierr.Write(w, http.StatusBadRequest, err)
			default:
				apierr.Write(w, http.StatusInternalServerError, err)
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "changed": changed})
	})
}

// rowTagsOf gives a row its optional Tags pointer: nil for an empty list so the wire omits it; otherwise a copy the
// row may keep after the next snapshot replaces the book.
func rowTagsOf(tags []string) *[]string {
	if len(tags) == 0 {
		return nil
	}
	t := slices.Clone(tags)
	return &t
}

// hasTag reports whether row carries tag (exact match).
func hasTag(row proto.Session, tag string) bool {
	if row.Tags == nil {
		return false
	}
	return slices.Contains(*row.Tags, tag)
}

// toolConversationTagsSet is the conversation_tags_set MCP tool. It reaches only conversations of the calling
// conversation's own space (ownSpaceConversation), replaces the whole tag set after normalization, and records the
// change as the assistant's.
func (s *Service) toolConversationTagsSet(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	c, err := s.ownSpaceConversation(ctx, self, strings.TrimSpace(a.ID))
	if err != nil {
		return "", err
	}
	// Validate before writing so an over-long or too-many call changes nothing; setConversationTags normalizes again
	// (the same rules the PUT route and the web app go through).
	normalized, err := normalizeConversationTags(a.Tags)
	if err != nil {
		return "", err
	}
	name := conversationName(c, s.settings(ctx).Main)
	changed, err := s.setConversationTags(ctx, c.MachineID, c.SessionID, a.Tags, titleByAssistant)
	if err != nil {
		return "", err
	}
	switch {
	case len(normalized) == 0:
		if changed {
			// i18n-ignore: MCP tool output for the model; 「」 quotes conversation names like the neighboring conversation tools
			return fmt.Sprintf("Cleared every tag of 「%s」.", name), nil
		}
		// i18n-ignore: MCP tool output for the model; 「」 quotes conversation names like the neighboring conversation tools
		return fmt.Sprintf("「%s」 has no tags already.", name), nil
	case !changed:
		// i18n-ignore: MCP tool output for the model; 「」 quotes conversation names like the neighboring conversation tools
		return fmt.Sprintf("「%s」 already carries exactly these tags: %s.", name, strings.Join(normalized, ", ")), nil
	case c.SessionID == self:
		return fmt.Sprintf("Tagged this conversation with: %s.", strings.Join(normalized, ", ")), nil
	default:
		// i18n-ignore: MCP tool output for the model; 「」 quotes conversation names like the neighboring conversation tools
		return fmt.Sprintf("Set the tags of 「%s」 (%s) to: %s.", name, c.SessionID, strings.Join(normalized, ", ")), nil
	}
}
