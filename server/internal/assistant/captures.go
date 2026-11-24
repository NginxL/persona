package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Quick capture (#13502, docs/assistant/health.md "Quick capture"): what the user jots down from the share sheet, the
// watch, Siri or the web goes into a list of its own. Taking one in is a single write, answered at once: no
// conversation gets a message or a turn. The work loop (capture_sort.go) sorts it into a track and tags it later, in
// one model call, and files the ones it is sure of into that track's inbox/.

// captureStore keeps the captures (assistant_captures).
type captureStore interface {
	InsertCapture(ctx context.Context, c db.Capture) (db.Capture, bool, error)
	Captures(ctx context.Context, f db.CaptureFilter) ([]db.Capture, error)
	CapturesToSort(ctx context.Context, maxAttempts, limit int) ([]db.Capture, error)
	SetCaptureResult(ctx context.Context, id, status string, sort, filedTo []byte, errText string, tried bool) (bool, error)
}

var _ captureStore = (*db.DB)(nil)

// Limits of what one capture holds.
const (
	captureTextMax = 32 << 10
	captureURLMax  = 2048
	captureBodyMax = 64 << 10
	captureListMax = 200
)

// captureClientID is a device's id for one capture: what an offline device sends again with each retry.
var captureClientID = lazyre.New(`^[A-Za-z0-9._:-]{8,128}$`)

var (
	captureSources = []string{assistantwire.CaptureSourceShareIOS, assistantwire.CaptureSourceShareAndroid, assistantwire.CaptureSourceWatch,
		assistantwire.CaptureSourceSiri, assistantwire.CaptureSourceWeb, assistantwire.CaptureSourceFeishu, assistantwire.CaptureSourceShortcut}
	captureKinds    = []string{assistantwire.CaptureKindText, assistantwire.CaptureKindURL, assistantwire.CaptureKindVoice}
	captureStatuses = []string{assistantwire.CaptureNew, assistantwire.CaptureSorted, assistantwire.CaptureFiled, assistantwire.CaptureDismissed,
		assistantwire.CaptureFailed}
)

func (s *Service) captureRoutes() {
	s.handle("GET /api/v1/assistant/captures", s.captureList)
	s.handle("POST /api/v1/assistant/captures", s.captureCreate)
}

// captureTarget is track slug when a capture may be filed into it: a track with a directory of its own here
// (spaceTrack: never the health space, nor an archived one) whose config takes captures.
func (s *Service) captureTarget(ctx context.Context, slug string) (trackEntry, bool) {
	e, ok := s.spaceTrack(ctx, slug)
	if !ok || !e.Config.CaptureTarget {
		return trackEntry{}, false
	}
	return e, true
}

// captureTargets are the tracks a capture may be filed into, by slug.
func (s *Service) captureTargets(ctx context.Context) []trackEntry {
	var out []trackEntry
	for _, slug := range s.spaceTracks(ctx) {
		if e, ok := s.captureTarget(ctx, slug); ok {
			out = append(out, e)
		}
	}
	return out
}

// checkCapture checks what POST /api/v1/assistant/captures was given, trimmed.
func (s *Service) checkCapture(ctx context.Context, in *assistantwire.CaptureCreate) error {
	in.Text, in.URL, in.SpaceHint = strings.TrimSpace(in.Text), strings.TrimSpace(in.URL), strings.TrimSpace(in.SpaceHint)
	switch {
	case !captureClientID.MatchString(in.ClientID):
		return errors.New("client_id: 8-128 letters, digits, dots, dashes, underscores or colons, the same on every retry")
	case !slices.Contains(captureSources, in.Source):
		return fmt.Errorf("source: one of %s", strings.Join(captureSources, ", "))
	case !slices.Contains(captureKinds, in.Kind):
		return fmt.Errorf("kind: one of %s (images and files come later)", strings.Join(captureKinds, ", "))
	case len(in.Text) > captureTextMax:
		return fmt.Errorf("text: larger than %d bytes", captureTextMax)
	case in.Text == "" && in.URL == "":
		return errors.New("text or url: a capture holds something")
	case in.Kind == assistantwire.CaptureKindURL && in.URL == "":
		return errors.New("url: a link capture has one")
	}
	if in.URL != "" {
		u, err := url.Parse(in.URL)
		if err != nil || len(in.URL) > captureURLMax || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("url: an http or https address of at most %d bytes", captureURLMax)
		}
	}
	if in.SpaceHint != "" {
		if _, ok := s.captureTarget(ctx, in.SpaceHint); !ok {
			return fmt.Errorf("space_hint: %q is not a track that takes captures", in.SpaceHint)
		}
	}
	return nil
}

func (s *Service) captureCreate(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	st, ok := s.store().(captureStore)
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("capture store unavailable"))
		return
	}
	var in assistantwire.CaptureCreate
	// Fields this version does not know are ignored, so a newer device's capture still goes in.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, captureBodyMax)).Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	if err := s.checkCapture(ctx, &in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	got, created, err := st.InsertCapture(ctx, db.Capture{ID: "cap_" + randomHex(8), ClientID: in.ClientID, Source: in.Source, Kind: in.Kind,
		Text: in.Text, URL: in.URL, SpaceHint: in.SpaceHint})
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
		s.captures.kick()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(assistantwire.CaptureAccepted{ID: got.ID, Status: got.Status, Duplicate: !created})
}

func (s *Service) captureList(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	st, ok := s.store().(captureStore)
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("capture store unavailable"))
		return
	}
	var f db.CaptureFilter
	if status := r.URL.Query().Get("status"); status != "" {
		if !slices.Contains(captureStatuses, status) {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("status: one of %s", strings.Join(captureStatuses, ", ")))
			return
		}
		f.Statuses = []string{status}
	}
	f.Limit = captureListMax
	ctx := r.Context()
	rows, err := st.Captures(ctx, f)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	cats := s.captureCategories(ctx, rows)
	out := assistantwire.Captures{Captures: []assistantwire.Capture{}}
	for _, c := range rows {
		out.Captures = append(out.Captures, wireCapture(c, cats[c.ID]))
	}
	apierr.WriteJSON(w, out)
}

// captureCats are a capture's information categories and whether it was tagged at all.
type captureCats struct {
	cats   []string
	tagged bool
}

// sensitive: outside the capture list it is used as sensitive information: it carries a category, or it was not
// tagged (yet, or the tagging failed).
func (c captureCats) sensitive() bool { return len(c.cats) > 0 || !c.tagged }

// captureCategories are the categories of captures rows, by id (captureCats{} for one never tagged or unreadable).
func (s *Service) captureCategories(ctx context.Context, rows []db.Capture) map[string]captureCats {
	out := map[string]captureCats{}
	st, ok := s.store().(infoCategoryStore)
	if !ok || len(rows) == 0 {
		return out
	}
	ids := make([]string, 0, len(rows))
	for _, c := range rows {
		ids = append(ids, c.ID)
	}
	rctx, cancel := context.WithTimeout(ctx, infoCategoryStoreTimeout)
	defer cancel()
	tags, err := st.InfoCategories(rctx, db.InfoCategoryCapture, ids)
	if err != nil {
		s.logf("captures: read categories: %v", err)
		return out
	}
	for id, list := range tags {
		var c captureCats
		for _, t := range list {
			c.tagged = true
			if !t.RemovedByUser {
				c.cats = append(c.cats, t.Category)
			}
		}
		c.cats, _ = normalCategories(c.cats)
		out[id] = c
	}
	return out
}

// wireCapture is c as the apps see it.
func wireCapture(c db.Capture, cats captureCats) assistantwire.Capture {
	out := assistantwire.Capture{ID: c.ID, ClientID: c.ClientID, CreatedAt: c.CreatedAt, Source: c.Source, Kind: c.Kind, Text: c.Text,
		URL: c.URL, SpaceHint: c.SpaceHint, Status: c.Status, Categories: cats.cats, Tagged: cats.tagged}
	if out.Categories == nil {
		out.Categories = []string{}
	}
	var sort assistantwire.CaptureSort
	if json.Unmarshal(c.Sort, &sort) == nil && sort != (assistantwire.CaptureSort{}) {
		out.Sort = &sort
	}
	var filed assistantwire.CaptureFiledTo
	if json.Unmarshal(c.FiledTo, &filed) == nil && filed.Ref != "" {
		out.FiledTo = &filed
	}
	return out
}
