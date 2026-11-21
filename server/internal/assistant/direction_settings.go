package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
)

// directionPromptSettingPrefix exposes the existing direction notes through persona_settings. "prompt" is the
// assistant-facing name; the stored value and its delivery remain direction_notes.go's single source of truth.
const (
	directionPromptSettingPrefix = "direction."
	directionPromptSettingSuffix = ".prompt"
)

func directionPromptSettingKey(id string) string {
	return directionPromptSettingPrefix + id + directionPromptSettingSuffix
}

func directionPromptSettingID(key string) (string, bool) {
	if !strings.HasPrefix(key, directionPromptSettingPrefix) || !strings.HasSuffix(key, directionPromptSettingSuffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, directionPromptSettingPrefix), directionPromptSettingSuffix)
	return id, id != ""
}

// directionOwnSettings adds the current directions to the ordinary settings catalog. Reads stay available to every
// assistant conversation; toolPersonaSettings enforces the main-only write boundary before dispatch.
func (s *Service) directionOwnSettings(ctx context.Context) []ownSetting {
	cfg := s.settings(ctx)
	ids := make([]string, 0, len(cfg.Leaders))
	for _, id := range cfg.Leaders {
		if id != "" && id != cfg.Main && s.directionOf(id) != nil {
			ids = append(ids, id)
		}
	}
	names := s.leaderNames(ctx, ids)
	out := make([]ownSetting, 0, len(ids))
	for _, id := range ids {
		id, name := id, names[id]
		if name == "" {
			name = id
		}
		out = append(out, ownSetting{Setting: Setting{
			Key:   directionPromptSettingKey(id),
			Title: "Direction notes: " + name,
			About: "the owner's prompt for this direction, given to its lead conversation and its new Agents; " +
				"any assistant conversation may read it, but only the main conversation may propose a change",
			Value:   `a string (the whole prompt), or {"old":"one exact unique fragment","new":"replacement"} to change only that fragment; get first`,
			Actions: []string{"get", "set"},
		},
			cardTitle: func(l i18n.Lang) string {
				return i18n.T(l, "assistant-settings_tool.title_direction_prompt", i18n.Args{"name": name})
			},
			get: func(ctx context.Context) (string, error) {
				d := s.directionOf(id)
				if d == nil {
					return "", errNoDirection
				}
				return orNone(d.Notes), nil
			},
			set: func(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
				return s.setDirectionPromptSetting(ctx, id, name, raw)
			},
		})
	}
	return out
}

// directionPromptEdit validates a whole replacement or one exact fragment replacement against the notes read for the
// card. It never guesses an empty or ambiguous fragment, and the existing direction sanitation remains authoritative.
func directionPromptEdit(before string, raw json.RawMessage) (string, error) {
	var whole *string
	if err := json.Unmarshal(raw, &whole); err == nil && whole != nil {
		return cleanDirectionNotes(*whole), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 2 {
		return "", errors.New("value must be a whole-prompt string or an object containing only old and new strings")
	}
	var old, new *string
	oldRaw, oldOK := fields["old"]
	newRaw, newOK := fields["new"]
	if !oldOK || !newOK || json.Unmarshal(oldRaw, &old) != nil || json.Unmarshal(newRaw, &new) != nil || old == nil || new == nil {
		return "", errors.New("value must contain only old and new strings")
	}
	if *old == "" {
		return "", errors.New("old must not be empty")
	}
	first := strings.Index(before, *old)
	if first < 0 {
		return "", errors.New("old fragment was not found in the direction prompt")
	}
	if strings.Index(before[first+1:], *old) >= 0 {
		return "", errors.New("old fragment occurs more than once; include enough context to make it unique")
	}
	return cleanDirectionNotes(strings.Replace(before, *old, *new, 1)), nil
}

// directionPromptChanged returns the changed text with nearby context, so a late edit remains visible after the
// card's display clip instead of showing the same unchanged prefix on both sides. Inputs are valid UTF-8.
func directionPromptChanged(before, after string) (string, string) {
	b, a := []rune(before), []rune(after)
	prefix := 0
	for prefix < len(b) && prefix < len(a) && b[prefix] == a[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(b)-prefix && suffix < len(a)-prefix && b[len(b)-1-suffix] == a[len(a)-1-suffix] {
		suffix++
	}
	const contextRunes = 40
	start := max(0, prefix-contextRunes)
	beforeEnd, afterEnd := min(len(b), len(b)-suffix+contextRunes), min(len(a), len(a)-suffix+contextRunes)
	beforeText, afterText := string(b[start:beforeEnd]), string(a[start:afterEnd])
	if start > 0 {
		beforeText, afterText = "…"+beforeText, "…"+afterText
	}
	if beforeEnd < len(b) {
		beforeText += "…"
	}
	if afterEnd < len(a) {
		afterText += "…"
	}
	return beforeText, afterText
}

// setDirectionPromptSetting prepares a card against the exact notes_at read. Approval applies that same computed edit,
// not a fresh edit against newer text, and setCurrentDirectionNotes serializes Leaders changes and checks the baseline
// under the direction lock.
func (s *Service) setDirectionPromptSetting(ctx context.Context, id, name string, raw json.RawMessage) (*ownChange, error) {
	d := s.directionOf(id)
	if d == nil || !slices.Contains(s.settings(ctx).Leaders, id) {
		return nil, errNoDirection
	}
	before, beforeAt := d.Notes, d.NotesAt
	after, err := directionPromptEdit(before, raw)
	if err != nil {
		return nil, err
	}
	key := directionPromptSettingKey(id)
	if after == before {
		return &ownChange{summary: key + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	shownBefore, shownAfter := directionPromptChanged(before, after)
	return &ownChange{
		applyChecksProposal: true,
		cardText: func(l i18n.Lang) string {
			return i18n.T(l, "assistant-settings_tool.change_direction_prompt", i18n.Args{
				"name": name, "before": clipped(l, shownBefore), "after": clipped(l, shownAfter),
			})
		},
		apply: func(ctx context.Context) (string, error) {
			_, _, err := s.setCurrentDirectionNotes(ctx, id, after, &notesFrom{At: beforeAt})
			var changed *notesChanged
			switch {
			case errors.As(err, &changed), errors.Is(err, errNoDirection):
				return "", fmt.Errorf("%w: %v", approvalops.ErrStale, err)
			case err != nil:
				return "", err
			}
			return key + " updated; the lead takes it on its next idle send and new direction Agents receive it.", nil
		},
	}, nil
}
