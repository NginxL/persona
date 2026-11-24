package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
)

// The tracks and track kinds API (#13472, docs/assistant/health.md "Tracks and kinds"): reads list everything, the
// built-in ones and archived ones included; only the user (a full-access client) creates or changes a track or a kind,
// and never a built-in one. A change is made against the revision it was read at (409 when another came first).

// trackWriteStore writes the registry.
type trackWriteStore interface {
	InsertTrack(ctx context.Context, t db.Track) error
	UpdateTrack(ctx context.Context, t db.Track, revision int64) (int64, error)
	InsertTrackKind(ctx context.Context, k db.TrackKind) error
	UpdateTrackKind(ctx context.Context, k db.TrackKind, revision int64) (int64, error)
}

var _ trackWriteStore = (*db.DB)(nil)

// Limits of what a track or a kind is called.
const (
	trackTitleMax       = 80
	trackDescriptionMax = 500
	trackOverrideMax    = 16 << 10
	trackBodyMax        = 64 << 10
)

func (s *Service) trackRoutes() {
	s.handle("GET /api/v1/assistant/tracks", s.trackList)
	s.handle("POST /api/v1/assistant/tracks", s.trackCreate)
	s.handle("PUT /api/v1/assistant/tracks/{slug}", s.trackChange)
	s.handle("GET /api/v1/assistant/track-kinds", s.trackKindList)
	s.handle("POST /api/v1/assistant/track-kinds", s.trackKindCreate)
	s.handle("PUT /api/v1/assistant/track-kinds/{slug}", s.trackKindChange)
	s.trackSpaceMDRoutes() // track_space_md.go
}

// wireTrack is e as the apps see it.
func wireTrack(e trackEntry) assistantwire.Track {
	cats := e.DefaultCategories
	if cats == nil {
		cats = []string{}
	}
	return assistantwire.Track{Slug: e.Slug, Kind: e.Kind, Title: e.Title, Icon: e.Icon, Description: e.Description,
		DefaultCategories: cats, Template: e.Template, Config: e.Config, Builtin: e.Builtin, Archived: e.Archived, Revision: e.Revision}
}

// wireKind is k as the apps see it.
func wireKind(k kindEntry) assistantwire.TrackKind {
	out := assistantwire.TrackKind{Slug: k.Slug, Title: k.Title, Config: k.Config, Builtin: k.Builtin, Revision: k.Revision, Disabled: k.Disabled}
	if k.Disabled == "" {
		out.Warnings = kindWarnings(k.Config)
	}
	return out
}

// checkTrackText checks a title (required) and a description and an icon.
func checkTrackText(title, description, icon string) error {
	if strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > trackTitleMax {
		return fmt.Errorf("title: empty or longer than %d characters", trackTitleMax)
	}
	if utf8.RuneCountInString(description) > trackDescriptionMax {
		return fmt.Errorf("description: longer than %d characters", trackDescriptionMax)
	}
	if !validTrackIcon(icon) {
		return errors.New("icon: not an icon token's name")
	}
	return nil
}

// trackWriteError answers a registry write's error.
func trackWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTrackExists), errors.Is(err, db.ErrTrackRevision):
		apierr.Write(w, http.StatusConflict, err)
	case errors.Is(err, db.ErrTrackNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

// trackWriter is the store a write goes to, or answers 503.
func (s *Service) trackWriter(w http.ResponseWriter) (trackWriteStore, bool) {
	st, ok := s.store().(trackWriteStore)
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("track store unavailable"))
	}
	return st, ok
}

// decodeTrackBody reads r's JSON body into v, refusing fields it does not know.
func decodeTrackBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, trackBodyMax))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

// normalOverride is a track's override in its stored form ("{}" for none), or an error when it is not a JSON object
// or too large.
func normalOverride(raw json.RawMessage) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return []byte("{}"), nil
	}
	if len(trimmed) > trackOverrideMax {
		return nil, fmt.Errorf("config_override: larger than %d bytes", trackOverrideMax)
	}
	if !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
		return nil, errors.New("config_override: not a JSON object")
	}
	return []byte(trimmed), nil
}

func (s *Service) trackList(w http.ResponseWriter, r *http.Request) {
	out := assistantwire.Tracks{Tracks: []assistantwire.Track{}}
	for _, e := range s.tracks(r.Context()) {
		out.Tracks = append(out.Tracks, wireTrack(e))
	}
	apierr.WriteJSON(w, out)
}

func (s *Service) trackCreate(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	var in assistantwire.TrackCreate
	if !decodeTrackBody(w, r, &in) {
		return
	}
	if !identity.ValidTrackSlug(in.Slug) {
		apierr.Write(w, http.StatusBadRequest, errors.New("slug: 2-32 lowercase letters, digits or dashes, starting with a letter, and not a reserved name"))
		return
	}
	if err := checkTrackText(in.Title, in.Description, in.Icon); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	kind, ok := kindOf(s.trackKinds(ctx), in.Kind)
	if !ok || kind.Disabled != "" {
		apierr.Write(w, http.StatusBadRequest, fmt.Errorf("kind: %q is not an enabled kind", in.Kind))
		return
	}
	if _, taken := s.trackEntryOf(ctx, in.Slug); taken {
		apierr.Write(w, http.StatusConflict, db.ErrTrackExists)
		return
	}
	template, cats := kind.Config.Template, kind.Config.DefaultCategories
	if in.Template != "" {
		template = in.Template
	}
	if in.DefaultCategories != nil {
		cats = in.DefaultCategories
	}
	override, err := normalOverride(in.ConfigOverride)
	if err == nil {
		_, err = effectiveTrackConfig(kind.Config, template, cats, override)
	}
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	cats, _ = normalCategories(cats)
	st, ok := s.trackWriter(w)
	if !ok {
		return
	}
	row := db.Track{Slug: in.Slug, Kind: kind.Slug, Title: strings.TrimSpace(in.Title), Icon: in.Icon, Description: in.Description,
		DefaultCategories: cats, Template: template, ConfigOverride: override, DirName: s.defaultTrackDir(in.Slug)}
	err = st.InsertTrack(ctx, row)
	s.tracksChanged()
	if err != nil {
		trackWriteError(w, err)
		return
	}
	s.prepareTrackSoon(in.Slug)
	if e, ok := s.trackEntryOf(ctx, in.Slug); ok {
		// Its rhythms become its scheduled tasks once, now (track_rhythms.go); the track stands either way.
		if err := s.createTrackRhythms(ctx, e); err != nil {
			s.logf("tracks: %q: rhythms: %v", in.Slug, err)
		}
	}
	s.answerTrack(ctx, w, in.Slug, http.StatusCreated)
}

func (s *Service) trackChange(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	slug := r.PathValue("slug")
	var in assistantwire.TrackChange
	if !decodeTrackBody(w, r, &in) {
		return
	}
	ctx := r.Context()
	e, ok := s.trackEntryOf(ctx, slug)
	if !ok {
		// A row left out (its kind not enabled, or not valid here) is not changed through this API either.
		apierr.Write(w, http.StatusNotFound, db.ErrTrackNotFound)
		return
	}
	if e.Builtin {
		apierr.Write(w, http.StatusForbidden, errors.New("a built-in track is not changed"))
		return
	}
	if in.Revision != e.Revision {
		apierr.Write(w, http.StatusConflict, db.ErrTrackRevision)
		return
	}
	row := db.Track{Slug: e.Slug, Title: e.Title, Icon: e.Icon, Description: e.Description, DefaultCategories: e.DefaultCategories,
		Template: e.Template, ConfigOverride: e.Override, ArchivedAt: e.ArchivedAt}
	if in.Title != nil {
		row.Title = strings.TrimSpace(*in.Title)
	}
	if in.Icon != nil {
		row.Icon = *in.Icon
	}
	if in.Description != nil {
		row.Description = *in.Description
	}
	if in.DefaultCategories != nil {
		row.DefaultCategories = *in.DefaultCategories
	}
	if in.Template != nil {
		row.Template = *in.Template
	}
	if in.Archived != nil {
		switch {
		case *in.Archived && row.ArchivedAt == nil:
			now := time.Now().UTC()
			row.ArchivedAt = &now
		case !*in.Archived:
			row.ArchivedAt = nil
		}
	}
	if err := checkTrackText(row.Title, row.Description, row.Icon); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	var err error
	if in.ConfigOverride != nil {
		if row.ConfigOverride, err = normalOverride(in.ConfigOverride); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err = rhythmsUnchanged(e.Override, row.ConfigOverride); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	kind, ok := kindOf(s.trackKinds(ctx), e.Kind)
	if !ok || kind.Disabled != "" {
		apierr.Write(w, http.StatusConflict, fmt.Errorf("kind: %q is not enabled", e.Kind))
		return
	}
	if _, err = effectiveTrackConfig(kind.Config, row.Template, row.DefaultCategories, row.ConfigOverride); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	row.DefaultCategories, _ = normalCategories(row.DefaultCategories)
	st, ok := s.trackWriter(w)
	if !ok {
		return
	}
	_, err = st.UpdateTrack(ctx, row, in.Revision)
	s.tracksChanged()
	if err != nil {
		trackWriteError(w, err)
		return
	}
	s.answerTrack(ctx, w, slug, http.StatusOK)
}

// rhythmsUnchanged: a track sets its rhythms only when it is created (later, its scheduled tasks are changed as
// scheduled tasks).
func rhythmsUnchanged(before, after []byte) error {
	pick := func(raw []byte) string {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		var v any
		if r, ok := fields["rhythms"]; ok && json.Unmarshal(r, &v) == nil {
			b, _ := json.Marshal(v)
			return string(b)
		}
		return ""
	}
	if pick(before) != pick(after) {
		return errors.New("config_override.rhythms: set only when the track is created; change its scheduled tasks instead")
	}
	return nil
}

// answerTrack answers with track slug as the registry has it now.
func (s *Service) answerTrack(ctx context.Context, w http.ResponseWriter, slug string, status int) {
	e, ok := s.trackEntryOf(ctx, slug)
	if !ok {
		apierr.Write(w, http.StatusInternalServerError, errors.New("the track was written but cannot be read back"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wireTrack(e))
}

func (s *Service) trackKindList(w http.ResponseWriter, r *http.Request) {
	out := assistantwire.TrackKinds{Kinds: []assistantwire.TrackKind{}}
	for _, k := range s.trackKinds(r.Context()) {
		out.Kinds = append(out.Kinds, wireKind(k))
	}
	apierr.WriteJSON(w, out)
}

func (s *Service) trackKindCreate(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	var in assistantwire.TrackKindCreate
	if !decodeTrackBody(w, r, &in) {
		return
	}
	if !validKindSlug(in.Slug) {
		apierr.Write(w, http.StatusBadRequest, errors.New("slug: 2-32 lowercase letters, digits or dashes, starting with a letter, and not a built-in kind or a reserved name"))
		return
	}
	if err := checkTrackText(in.Title, "", ""); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := normalKindConfig(in.Config)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	st, ok := s.trackWriter(w)
	if !ok {
		return
	}
	ctx := r.Context()
	err = st.InsertTrackKind(ctx, db.TrackKind{Slug: in.Slug, Title: strings.TrimSpace(in.Title), Config: raw})
	s.tracksChanged()
	if err != nil {
		trackWriteError(w, err)
		return
	}
	s.answerKind(ctx, w, in.Slug, http.StatusCreated)
}

func (s *Service) trackKindChange(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	slug := r.PathValue("slug")
	var in assistantwire.TrackKindChange
	if !decodeTrackBody(w, r, &in) {
		return
	}
	ctx := r.Context()
	k, ok := kindOf(s.trackKinds(ctx), slug)
	if !ok {
		apierr.Write(w, http.StatusNotFound, db.ErrTrackNotFound)
		return
	}
	if k.Builtin {
		apierr.Write(w, http.StatusForbidden, errors.New("a built-in kind is not changed"))
		return
	}
	if in.Revision != k.Revision {
		apierr.Write(w, http.StatusConflict, db.ErrTrackRevision)
		return
	}
	title, cfg := k.Title, k.Config
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
	}
	if err := checkTrackText(title, "", ""); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	if in.Config != nil {
		cfg = *in.Config
	} else if k.Disabled != "" {
		apierr.Write(w, http.StatusConflict, errors.New("the kind is not enabled here: give it a config this assistant knows"))
		return
	}
	cfg, err := normalKindConfig(cfg)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	if err := s.kindFitsTracks(ctx, slug, cfg); err != nil {
		apierr.Write(w, http.StatusConflict, err)
		return
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	st, ok := s.trackWriter(w)
	if !ok {
		return
	}
	_, err = st.UpdateTrackKind(ctx, db.TrackKind{Slug: slug, Title: title, Config: raw}, in.Revision)
	s.tracksChanged()
	if err != nil {
		trackWriteError(w, err)
		return
	}
	s.answerKind(ctx, w, slug, http.StatusOK)
}

// kindFitsTracks: config cfg for kind slug leaves every one of its tracks valid (a track's override over it), so a
// kind change never drops a track.
func (s *Service) kindFitsTracks(ctx context.Context, slug string, cfg assistantwire.TrackKindConfig) error {
	for _, e := range s.tracks(ctx) {
		if e.Kind != slug {
			continue
		}
		if _, err := effectiveTrackConfig(cfg, e.Template, e.DefaultCategories, e.Override); err != nil {
			return fmt.Errorf("track %q would not be valid with this config: %w", e.Slug, err)
		}
	}
	return nil
}

// answerKind answers with kind slug as the registry has it now.
func (s *Service) answerKind(ctx context.Context, w http.ResponseWriter, slug string, status int) {
	k, ok := kindOf(s.trackKinds(ctx), slug)
	if !ok {
		apierr.Write(w, http.StatusInternalServerError, errors.New("the kind was written but cannot be read back"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wireKind(k))
}
