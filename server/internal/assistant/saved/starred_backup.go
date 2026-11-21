package saved

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	starBackupFormat  = "persona.starred"
	starBackupVersion = 1
	backupMachine     = "persona-starred-backup"
	backupSession     = "backup."
	backupItem        = "line0#0"
)

var errStarBackupInput = i18n.Err("assistant-starred.err_backup_input")

// A backup message carries the saved words and display metadata. It has no
// information that can prove ownership of any item in a live transcript.
type starBackupMessage struct {
	Key          string     `json:"key"`
	Conversation string     `json:"conversation"`
	Role         string     `json:"role"`
	Title        string     `json:"title"`
	Speaker      string     `json:"speaker"`
	Text         string     `json:"text"`
	StarredAt    time.Time  `json:"starred_at"`
	WrittenAt    *time.Time `json:"written_at,omitempty"`
	Omitted      int        `json:"omitted_chars,omitempty"`
}

func validStarBackupKey(key string) bool {
	digits, ok := strings.CutPrefix(key, "sm")
	return ok && len(digits) == 32 && strings.Trim(digits, "0123456789abcdef") == ""
}

// The original star path normalizes label spaces before truncating the label.
// A maximum-length saved label can therefore end in exactly one ASCII space.
func validBackupLabel(saved, normalized string) bool {
	if saved == normalized {
		return true
	}
	prefix, clippedAtSpace := strings.CutSuffix(saved, " ")
	return clippedAtSpace && prefix == normalized && utf8.RuneCountInString(saved) == starLabelMax
}

func parseStarBackupMessage(raw json.RawMessage) (starBackupMessage, error) {
	const optionalWrittenAt, optionalOmitted = "written_at", "omitted_chars"
	required := []string{"key", "conversation", "role", "title", "speaker", "text", "starred_at"}
	fields, err := savedSearchObject(raw, append(required, optionalWrittenAt, optionalOmitted)...)
	if err != nil {
		return starBackupMessage{}, errStarBackupInput
	}
	for _, name := range required {
		if fields[name] == nil || string(fields[name]) == "null" {
			return starBackupMessage{}, errStarBackupInput
		}
	}
	if string(fields[optionalWrittenAt]) == "null" || string(fields[optionalOmitted]) == "null" {
		return starBackupMessage{}, errStarBackupInput
	}
	var message starBackupMessage
	if err := json.Unmarshal(raw, &message); err != nil || !validStarBackupKey(message.Key) ||
		message.StarredAt.IsZero() ||
		len(message.Text) > StarTextMax*4 || len(message.Title) > starLabelMax*4 || len(message.Speaker) > starLabelMax*4 {
		return starBackupMessage{}, errStarBackupInput
	}
	snapshot := message.snapshot()
	// The regular star validator protects the same types and bounds. A
	// portable backup must already fit them, so restoring it does not alter
	// its text, names or previous truncation count.
	err = snapshot.validate()
	// The original star path checked for blank text before clipping. It
	// could save a max-size whitespace prefix followed by omitted content.
	if err == errStarText && message.Omitted > 0 && utf8.RuneCountInString(snapshot.Text) == StarTextMax {
		err = nil
	}
	if err != nil || snapshot.Text != message.Text || snapshot.Omitted != message.Omitted ||
		!validBackupLabel(message.Title, snapshot.Title) || !validBackupLabel(message.Speaker, snapshot.Speaker) {
		return starBackupMessage{}, errStarBackupInput
	}
	return message, nil
}

func (m starBackupMessage) snapshot() StarInput {
	return StarInput{
		StarSource: StarSource{
			Machine: backupMachine, Session: backupSession + m.Key, Item: backupItem, Conversation: m.Conversation,
		},
		Role: m.Role, Title: m.Title, Speaker: m.Speaker, WrittenAt: m.WrittenAt, Text: m.Text, Omitted: m.Omitted,
	}
}

func parseStarBackup(raw []byte, commit bool) ([]starBackupMessage, int64, error) {
	keys := []string{"format", "version", "messages"}
	if commit {
		keys = append(keys, "revision")
	}
	fields, err := savedSearchObject(raw, keys...)
	if err != nil {
		return nil, 0, errStarBackupInput
	}
	var format string
	var version int
	if json.Unmarshal(fields["format"], &format) != nil || format != starBackupFormat ||
		json.Unmarshal(fields["version"], &version) != nil || version != starBackupVersion {
		return nil, 0, errStarBackupInput
	}
	var revision *int64
	if commit && (json.Unmarshal(fields["revision"], &revision) != nil || revision == nil || *revision < 0 || *revision >= math.MaxInt64) {
		return nil, 0, errStarBackupInput
	}
	var records []json.RawMessage
	if json.Unmarshal(fields["messages"], &records) != nil || records == nil || len(records) > StarredMax {
		return nil, 0, errStarBackupInput
	}
	seen := make(map[string]bool, len(records))
	messages := make([]starBackupMessage, 0, len(records))
	for _, raw := range records {
		message, err := parseStarBackupMessage(raw)
		if err != nil || seen[message.Key] {
			return nil, 0, errStarBackupInput
		}
		seen[message.Key] = true
		messages = append(messages, message)
	}
	if !commit {
		return messages, 0, nil
	}
	return messages, *revision, nil
}

func starBackupAdditions(doc starCollection, messages []starBackupMessage) []starBackupMessage {
	saved := make(map[string]bool, len(doc.Stars))
	for _, star := range doc.Stars {
		saved[star.ID] = true
		if star.Machine != backupMachine || star.Item != backupItem || star.SourceFingerprint != "" || star.SourceManager != "" {
			continue
		}
		if key, ok := strings.CutPrefix(star.Session, backupSession); ok && validStarBackupKey(key) {
			saved[key] = true
		}
	}
	additions := make([]starBackupMessage, 0, len(messages))
	for _, message := range messages {
		if !saved[message.Key] {
			additions = append(additions, message)
		}
	}
	return additions
}

type starBackupPreview struct {
	Revision     int64 `json:"revision"`
	Total        int   `json:"total"`
	ToAdd        int   `json:"to_add"`
	AlreadySaved int   `json:"already_saved"`
	Available    int   `json:"available"`
}

type starBackupImport struct {
	Revision     int64 `json:"revision"`
	Added        int   `json:"added"`
	AlreadySaved int   `json:"already_saved"`
}

func (b *starBook) previewBackup(ctx context.Context, messages []starBackupMessage) (starBackupPreview, error) {
	st, ok := b.h.Store().(revisionedSettings)
	if !ok {
		return starBackupPreview{}, errStarStore
	}
	doc, err := loadStars(ctx, st)
	if err != nil {
		return starBackupPreview{}, errStarStore
	}
	additions := len(starBackupAdditions(doc, messages))
	return starBackupPreview{
		Revision: doc.Revision, Total: len(messages), ToAdd: additions,
		AlreadySaved: len(messages) - additions, Available: max(0, StarredMax-len(doc.Stars)),
	}, nil
}

func (b *starBook) importBackup(ctx context.Context, messages []starBackupMessage, revision int64) (starBackupImport, error) {
	var result starBackupImport
	err := b.change(ctx, func(doc *starCollection) (bool, error) {
		if doc.Revision != revision {
			return false, errStarConflict
		}
		additions := starBackupAdditions(*doc, messages)
		if len(additions) > max(0, StarredMax-len(doc.Stars)) {
			return false, errStarLimit
		}
		result = starBackupImport{Revision: revision, Added: len(additions), AlreadySaved: len(messages) - len(additions)}
		if len(additions) == 0 {
			return false, nil
		}
		// Keep existing occurrences, and give every archival occurrence its
		// own local ID. The portable key is retained in its source marker.
		for _, message := range additions {
			doc.Stars = append(doc.Stars, Star{
				ID: "sm" + randomHex(16), StarInput: message.snapshot(), StarredAt: message.StarredAt,
			})
		}
		slices.SortStableFunc(doc.Stars, func(a, b Star) int { return b.StarredAt.Compare(a.StarredAt) })
		result.Revision++
		return true, nil
	})
	return result, err
}

func (b *starBook) backupHandler(commit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, assistantwire.StarredImportBodyMax))
		if err != nil {
			b.h.BodyErr(w, err, errStarBackupInput)
			return
		}
		messages, revision, err := parseStarBackup(body, commit)
		if err != nil {
			writeStarError(w, err)
			return
		}
		if commit {
			result, err := b.importBackup(r.Context(), messages, revision)
			if err != nil {
				writeStarError(w, err)
				return
			}
			apierr.WriteJSON(w, result)
			return
		}
		result, err := b.previewBackup(r.Context(), messages)
		if err != nil {
			writeStarError(w, err)
			return
		}
		apierr.WriteJSON(w, result)
	}
}

func (b *starBook) backupRoutes(write func(http.HandlerFunc) http.HandlerFunc) {
	b.h.Handle("GET "+assistantwire.StarredImportPath, write(func(w http.ResponseWriter, _ *http.Request) {
		apierr.WriteJSON(w, map[string]bool{"can_import": true})
	}))
	b.h.Handle("POST "+assistantwire.StarredImportPreviewPath, write(b.backupHandler(false)))
	b.h.Handle("POST "+assistantwire.StarredImportPath, write(b.backupHandler(true)))
}
