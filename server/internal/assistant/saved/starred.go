package saved

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	StarredKey   = "assistant.starred_messages"
	StarredMax   = 300
	StarTextMax  = 4000
	starLabelMax = 200
	starIDMax    = 512
	starPath     = "/api/v1/assistant/starred"
)

// StarSource identifies the original transcript item. Identical item IDs in
// different conversations or transcripts remain independent.
type StarSource struct {
	Machine           string `json:"machine_id"`
	Session           string `json:"session_id"`
	Item              string `json:"item_id"`
	Conversation      string `json:"conversation"`
	SourceFingerprint string `json:"source_fingerprint,omitempty"`
}

type StarInput struct {
	StarSource
	Role          string     `json:"role"`
	Title         string     `json:"title"`
	Speaker       string     `json:"speaker"`
	SourceManager string     `json:"source_manager,omitempty"`
	WrittenAt     *time.Time `json:"written_at,omitempty"`
	Text          string     `json:"text"`
	// A paged or previously saved message can already be clipped by the client.
	Omitted int `json:"omitted_chars,omitempty"`
}

type Star struct {
	StarInput
	ID        string    `json:"id"`
	StarredAt time.Time `json:"starred_at"`
}

type starRef struct {
	StarSource
	ID string `json:"id"`
}

type starCollection struct {
	Revision int64  `json:"revision"`
	Stars    []Star `json:"stars"`
}

type starBook struct {
	h  Host
	mu sync.Mutex
}

var (
	errStarInput    = i18n.Err("assistant-starred.err_input")
	errStarText     = i18n.Err("assistant-starred.err_text")
	errStarReadOnly = i18n.Err("assistant-starred.err_read_only")
	errStarStore    = i18n.Err("assistant-starred.err_store")
	errStarLimit    = i18n.Err("assistant-starred.err_limit", i18n.Args{"max": StarredMax})
	errStarConflict = i18n.Err("assistant-starred.err_conflict")
)

func validStarID(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= starIDMax && !strings.ContainsFunc(id, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

func starLabel(label string) string {
	label = strings.Join(strings.Fields(strings.ReplaceAll(label, "\x00", "")), " ")
	runes := []rune(label)
	if len(runes) > starLabelMax {
		return string(runes[:starLabelMax])
	}
	return label
}

func (in *StarInput) validate() error {
	if !validStarID(in.Machine) || strings.Contains(in.Machine, "/") ||
		!validStarID(in.Session) || !validStarID(in.Item) ||
		(in.SourceManager != "" && !validStarID(in.SourceManager)) ||
		(in.Conversation != "assistant" && in.Conversation != "agent") ||
		(in.Role != "user" && in.Role != "assistant") || in.Omitted < 0 || in.Omitted > 1_000_000_000 {
		return errStarInput
	}
	if in.SourceFingerprint != "" {
		if _, err := hex.DecodeString(in.SourceFingerprint); len(in.SourceFingerprint) != 32 || err != nil || !proto.PositionalItemID(in.Item) {
			return errStarInput
		}
		in.SourceFingerprint = strings.ToLower(in.SourceFingerprint)
	}
	in.Title, in.Speaker = starLabel(in.Title), starLabel(in.Speaker)
	in.Text = strings.ReplaceAll(in.Text, "\x00", "")
	if strings.TrimSpace(in.Text) == "" {
		return errStarText
	}
	runes := []rune(in.Text)
	if len(runes) > StarTextMax {
		in.Text = string(runes[:StarTextMax])
		in.Omitted = min(in.Omitted+len(runes)-StarTextMax, 1_000_000_000)
	}
	return nil
}

func loadStars(ctx context.Context, st revisionedSettings) (starCollection, error) {
	var doc starCollection
	_, err := st.Setting(ctx, StarredKey, &doc)
	return doc, err
}

// change retries against the newest collection so concurrent changes in another
// manager process cannot erase a star. An absent delete or duplicate add returns
// the current state without a new write, timestamp, or broadcast.
func (b *starBook) change(ctx context.Context, apply func(*starCollection) (bool, error)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.h.Store().(revisionedSettings)
	if !ok {
		return errStarStore
	}
	for range 8 {
		doc, err := loadStars(ctx, st)
		if err != nil {
			return errStarStore
		}
		changed, err := apply(&doc)
		if err != nil || !changed {
			return err
		}
		old := doc.Revision
		doc.Revision++
		ok, err := st.CompareSetting(ctx, StarredKey, old, doc)
		if err != nil {
			return errStarStore
		}
		if ok {
			b.h.Event(ctx, map[string]any{"type": "assistant_starred"})
			return nil
		}
	}
	return errStarConflict
}

func sameStarSource(star Star, in StarInput) bool {
	if star.Machine != in.Machine || star.Session != in.Session || star.Item != in.Item || star.Conversation != in.Conversation {
		return false
	}
	if !proto.PositionalItemID(in.Item) {
		return true
	}
	if star.SourceFingerprint != in.SourceFingerprint {
		return false
	}
	if in.SourceFingerprint != "" {
		return true
	}
	// An older snapshot has no verified original. Its text remains available,
	// and Undo can restore it without claiming the current item at this position.
	return star.Role == in.Role && star.Text == in.Text && star.Omitted == in.Omitted &&
		((star.WrittenAt == nil && in.WrittenAt == nil) ||
			(star.WrittenAt != nil && in.WrittenAt != nil && star.WrittenAt.Equal(*in.WrittenAt)))
}

func (b *starBook) add(ctx context.Context, in StarInput) (Star, error) {
	var out Star
	err := b.change(ctx, func(doc *starCollection) (bool, error) {
		for _, star := range doc.Stars {
			if sameStarSource(star, in) {
				out = star
				return false, nil
			}
		}
		if len(doc.Stars) >= StarredMax {
			return false, errStarLimit
		}
		out = Star{StarInput: in, ID: "sm" + randomHex(16), StarredAt: time.Now().UTC()}
		doc.Stars = append([]Star{out}, doc.Stars...)
		return true, nil
	})
	return out, err
}

func (b *starBook) remove(ctx context.Context, id string) (*Star, error) {
	var out *Star
	err := b.change(ctx, func(doc *starCollection) (bool, error) {
		at := slices.IndexFunc(doc.Stars, func(s Star) bool { return s.ID == id })
		if at < 0 {
			out = nil
			return false, nil
		}
		star := doc.Stars[at]
		out = &star
		doc.Stars = slices.Delete(doc.Stars, at, at+1)
		return true, nil
	})
	return out, err
}

func writeStarError(w http.ResponseWriter, err error) {
	switch err {
	case errStarStore:
		apierr.Write(w, http.StatusServiceUnavailable, err)
	case errStarConflict:
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusBadRequest, err)
	}
}

func (b *starBook) list(w http.ResponseWriter, r *http.Request) {
	st, ok := b.h.Store().(revisionedSettings)
	if !ok {
		writeStarError(w, errStarStore)
		return
	}
	doc, err := loadStars(r.Context(), st)
	if err != nil {
		writeStarError(w, errStarStore)
		return
	}
	q := r.URL.Query()
	machine, session, found := strings.Cut(q.Get("session"), "/")
	if q.Has("session") && (!found || !validStarID(machine) || !validStarID(session)) {
		writeStarError(w, errStarInput)
		return
	}
	stars := make([]Star, 0, len(doc.Stars))
	refs := make([]starRef, 0, len(doc.Stars))
	index := q.Get("index") == "1"
	for _, star := range doc.Stars {
		if found && (star.Machine != machine || star.Session != session) {
			continue
		}
		if index {
			refs = append(refs, starRef{StarSource: star.StarSource, ID: star.ID})
		} else {
			stars = append(stars, star)
		}
	}
	out := map[string]any{"can_write": b.h.Full(r)}
	if index {
		out["refs"] = refs
	} else {
		out["stars"] = stars
	}
	apierr.WriteJSON(w, out)
}

func (b *starBook) routes() {
	b.h.Handle("GET "+starPath, b.list)
	write := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !b.h.Full(r) {
				apierr.Write(w, http.StatusForbidden, errStarReadOnly)
				return
			}
			fn(w, r)
		}
	}
	b.h.Handle("POST "+starPath, write(func(w http.ResponseWriter, r *http.Request) {
		var in StarInput
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			b.h.BodyErr(w, err, errStarInput)
			return
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			writeStarError(w, errStarInput)
			return
		}
		if err := in.validate(); err != nil {
			writeStarError(w, err)
			return
		}
		out, err := b.add(r.Context(), in)
		if err != nil {
			writeStarError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"star": out})
	}))
	b.h.Handle("DELETE "+starPath+"/{id}", write(func(w http.ResponseWriter, r *http.Request) {
		out, err := b.remove(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStarError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"removed": out})
	}))
	b.backupRoutes(write)
}
