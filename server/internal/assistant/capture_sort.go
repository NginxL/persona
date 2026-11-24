package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Sorting quick captures (#13502, docs/assistant/health.md "Quick capture"): a work loop in the manager takes up each
// new capture and, in one call of the capture.sort use, sorts it into one of the tracks that take captures and tags its
// information categories. A capture the user put in a track, or one the model is sure of, is filed into that track's
// inbox/ on the assistant's machine (nodeproto.TrackInbox); the others stay in the list, sorted, for the user. No
// conversation gets a turn. A sensitive capture's push says only "1 capture".

const (
	// purposeCaptureSort is the model use that sorts and tags a capture: Sorting's model until it is given its own.
	purposeCaptureSort = "capture.sort"
	// captureNoteMax bounds how much of a capture the model reads, in characters.
	captureNoteMax = 2000
	// captureSortTimeout bounds one sorting call; captureFileTimeout one write on the node.
	captureSortTimeout = time.Minute
	captureFileTimeout = 30 * time.Second
	// captureAutoFile is the confidence from which a capture is filed without the user.
	captureAutoFile = 0.8
	// captureMaxAttempts is how often a capture is tried (sorted, or filed) before it is left to the user.
	captureMaxAttempts = 5
	// captureWorkers is how many captures are sorted at once; captureBatch how many a round takes up.
	captureWorkers = 2
	captureBatch   = 8
	// capturePoll is how often the loop looks without being woken (a model or a node that came back).
	capturePoll = time.Minute
	// captureTitleMax bounds a capture's title.
	captureTitleMax = 60
)

// The actions sorting may suggest; only filing happens without the user.
var captureActions = []string{"keep", "discuss", "research"}

// captureWork wakes the loop when a capture comes in.
type captureWork struct {
	once sync.Once
	wake chan struct{}
}

func (w *captureWork) wakeup() chan struct{} {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
	return w.wake
}

// kick wakes the loop, without waiting.
func (w *captureWork) kick() {
	select {
	case w.wakeup() <- struct{}{}:
	default:
	}
}

// captureLoop sorts the captures until ctx ends: when one comes in, and each capturePoll.
func (s *Service) captureLoop(ctx context.Context) {
	st, ok := s.store().(captureStore)
	if !ok {
		return
	}
	ctx = ai.Background(ctx) // its model calls give their turn back when queued under a connection's cap (ai/limit.go)
	wake := s.captures.wakeup()
	t := time.NewTicker(capturePoll)
	defer t.Stop()
	for {
		if full := s.captureRound(ctx, st); full {
			s.captures.kick() // more are waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-t.C:
		}
	}
}

// captureSortConfigured: the capture.sort use resolves to a model it can call. Without one, captures stay new, listed
// for the user.
func (s *Service) captureSortConfigured(ctx context.Context) bool {
	b, err := s.resolveModel(ctx, purposeCaptureSort)
	return err == nil && s.modelUsable(ctx, b)
}

// captureRound sorts (or files) up to captureBatch captures, captureWorkers at a time; full says a whole batch was
// taken up and more may be waiting.
func (s *Service) captureRound(ctx context.Context, st captureStore) (full bool) {
	if !s.captureSortConfigured(ctx) {
		return false
	}
	rows, err := st.CapturesToSort(ctx, captureMaxAttempts, captureBatch)
	if err != nil {
		s.logf("captures: read: %v", err)
		return false
	}
	work := make(chan db.Capture)
	var wg sync.WaitGroup
	for range captureWorkers {
		wg.Go(func() {
			for c := range work {
				s.sortCapture(ctx, st, c)
			}
		})
	}
	for _, c := range rows {
		work <- c
	}
	close(work)
	wg.Wait()
	// Another round at once only when this one sorted something: captures waiting for their computer would make it
	// spin.
	return len(rows) == captureBatch && slices.ContainsFunc(rows, func(c db.Capture) bool { return c.Status == assistantwire.CaptureNew })
}

// captureSortInput is the user message of a sorting call: data, never instructions (prompts/assistant/capture_sort.md).
type captureSortInput struct {
	Note   string         `json:"note"`
	Source string         `json:"source"`
	Spaces []captureSpace `json:"spaces"`
	Chosen string         `json:"chosen,omitempty"`
}

type captureSpace struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// captureSortAnswer is a sorting call's answer.
type captureSortAnswer struct {
	Space      *string  `json:"space"`
	Confidence float64  `json:"confidence"`
	Action     string   `json:"action"`
	Title      string   `json:"title"`
	Deep       bool     `json:"deep"`
	Categories []string `json:"categories"`
}

// captureNote is what the model reads of c: its text and its link, cut at captureNoteMax characters.
func captureNote(c db.Capture) string {
	note := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(c.Text), strings.TrimSpace(c.URL)}, "\n"))
	if utf8.RuneCountInString(note) > captureNoteMax {
		note = string([]rune(note)[:captureNoteMax])
	}
	return note
}

// sortCaptureWith asks m to sort in: the categories it names that the manager knows, each once, and the rest as it
// answered.
func sortCaptureWith(ctx context.Context, m jsonModel, in captureSortInput) (captureSortAnswer, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(in); err != nil {
		return captureSortAnswer{}, err
	}
	var out captureSortAnswer
	if err := m.JSON(ctx, prompts.RenderCaptureSort(), strings.TrimSuffix(buf.String(), "\n"), &out); err != nil {
		return captureSortAnswer{}, err
	}
	out.Categories, _ = normalCategories(out.Categories)
	return out, nil
}

// captureSortOf is what an answer makes of a capture: the track only when it is one of targets (hint, the user's
// choice, wins and is always filed), the action and title as known, the confidence between 0 and 1, filed on its own
// from captureAutoFile.
func captureSortOf(a captureSortAnswer, targets []trackEntry, hint string) assistantwire.CaptureSort {
	out := assistantwire.CaptureSort{Deep: a.Deep, Title: clip(oneLine(a.Title), captureTitleMax)}
	if !math.IsNaN(a.Confidence) {
		out.Confidence = min(max(a.Confidence, 0), 1)
	}
	if slices.Contains(captureActions, a.Action) {
		out.Action = a.Action
	} else {
		out.Action = "keep"
	}
	if a.Space != nil && slices.ContainsFunc(targets, func(e trackEntry) bool { return e.Slug == *a.Space }) {
		out.Track = *a.Space
	}
	switch {
	case hint != "":
		out.Track, out.AutoFile = hint, true
	case out.Track != "" && out.Confidence >= captureAutoFile:
		out.AutoFile = true
	}
	return out
}

// sortCapture sorts capture c and files it when it should be; a capture sorted before to be filed is only filed.
func (s *Service) sortCapture(ctx context.Context, st captureStore, c db.Capture) {
	var sorted assistantwire.CaptureSort
	if c.Status == assistantwire.CaptureSorted && json.Unmarshal(c.Sort, &sorted) == nil && sorted.AutoFile {
		s.fileCapture(ctx, st, c, sorted, s.captureCategories(ctx, []db.Capture{c})[c.ID])
		return
	}
	targets := s.captureTargets(ctx)
	hint := ""
	if c.SpaceHint != "" && slices.ContainsFunc(targets, func(e trackEntry) bool { return e.Slug == c.SpaceHint }) {
		hint = c.SpaceHint
	}
	in := captureSortInput{Note: captureNote(c), Source: c.Source, Spaces: []captureSpace{}, Chosen: hint}
	for _, e := range targets {
		in.Spaces = append(in.Spaces, captureSpace{Slug: e.Slug, Title: oneLine(e.Title), Description: clip(oneLine(e.Description), 200)})
	}
	b, _ := s.resolveModel(ctx, purposeCaptureSort)
	cctx, cancel := context.WithTimeout(ctx, captureSortTimeout)
	a, err := sortCaptureWith(cctx, s.purposeModel(purposeCaptureSort), in)
	cancel()
	if err != nil {
		if modelUnconfigured(err) || ctx.Err() != nil {
			return // stays new: no try counted
		}
		status := assistantwire.CaptureNew
		if c.Attempts+1 >= captureMaxAttempts {
			status = assistantwire.CaptureFailed
		}
		s.logf("captures: sort %s: %v", c.ID, infoTagFailure(err))
		if _, err := st.SetCaptureResult(ctx, c.ID, status, nil, nil, infoTagFailure(err), true); err != nil {
			s.logf("captures: keep %s: %v", c.ID, err)
		}
		return
	}
	sorted = captureSortOf(a, targets, hint)
	sorted.Model, sorted.At = b.Model, time.Now().UTC()
	// What it carries: what the model tagged, and at least its track's default categories.
	cats := a.Categories
	if e, ok := s.trackEntryOf(ctx, sorted.Track); ok && sorted.Track != "" {
		cats = append(cats, e.DefaultCategories...)
	}
	cats, _ = normalCategories(cats)
	tagged := s.keepCaptureTags(ctx, c.ID, cats, b.Model)
	raw, err := json.Marshal(sorted)
	if err != nil {
		return
	}
	changed, err := st.SetCaptureResult(ctx, c.ID, assistantwire.CaptureSorted, raw, nil, "", false)
	if err != nil || !changed {
		if err != nil {
			s.logf("captures: keep %s: %v", c.ID, err)
		}
		return
	}
	c.Status, c.Sort = assistantwire.CaptureSorted, raw
	if sorted.AutoFile {
		s.fileCapture(ctx, st, c, sorted, captureCats{cats: cats, tagged: tagged})
	}
}

// keepCaptureTags keeps capture id's categories (infoNoneTagged for none) and answers whether they were kept.
func (s *Service) keepCaptureTags(ctx context.Context, id string, cats []string, model string) bool {
	st, ok := s.store().(infoCategoryStore)
	if !ok {
		return false
	}
	if len(cats) == 0 {
		cats = []string{infoNoneTagged}
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), infoCategoryStoreTimeout)
	defer cancel()
	if err := st.AddInfoCategories(wctx, db.InfoCategoryCapture, id, cats, infoTaggedByModel, model); err != nil {
		s.logf("captures: tag %s: %v", id, err)
		return false
	}
	return true
}

// trackInboxNode: machine's node writes into a track's inbox/ (nodeproto.TrackInbox).
func (s *Service) trackInboxNode(machine string) bool {
	nodes, ok := s.physicalDevices().(nodeAnnouncer)
	return ok && nodes.NodeAnnounced(machine, nodeproto.TrackInbox)
}

// errNoTrackInbox: the assistant's computer cannot file into a track now (no computer set, a work computer, offline,
// or a node without nodeproto.TrackInbox): the capture waits, sorted, until it can or the user files it.
var errNoTrackInbox = errors.New("the assistant's computer does not file into tracks")

// fileCapture writes capture c into its track's inbox/ on the assistant's machine and pushes that it did. A track
// that no longer takes captures leaves it sorted for the user; a write that fails is tried again later.
func (s *Service) fileCapture(ctx context.Context, st captureStore, c db.Capture, sorted assistantwire.CaptureSort, cats captureCats) {
	e, ok := s.captureTarget(ctx, sorted.Track)
	if !ok {
		sorted.AutoFile = false
		if raw, err := json.Marshal(sorted); err == nil {
			if _, err := st.SetCaptureResult(ctx, c.ID, assistantwire.CaptureSorted, raw, nil, "", false); err != nil {
				s.logf("captures: keep %s: %v", c.ID, err)
			}
		}
		return
	}
	path, err := s.writeCaptureFile(ctx, c, sorted, e, cats)
	if errors.Is(err, errNoTrackInbox) {
		return // not a try: it is filed once the computer can (each capturePoll)
	}
	if err != nil {
		s.logf("captures: file %s into %q: %v", c.ID, e.Slug, err)
		if _, err2 := st.SetCaptureResult(ctx, c.ID, assistantwire.CaptureSorted, nil, nil, err.Error(), true); err2 != nil {
			s.logf("captures: keep %s: %v", c.ID, err2)
		}
		return
	}
	raw, err := json.Marshal(assistantwire.CaptureFiledTo{Kind: "space_file", Track: e.Slug, Ref: path})
	if err != nil {
		return
	}
	changed, err := st.SetCaptureResult(ctx, c.ID, assistantwire.CaptureFiled, nil, raw, "", false)
	if err != nil {
		s.logf("captures: keep %s: %v", c.ID, err)
		return
	}
	if changed {
		s.pushCaptureFiled(ctx, c, sorted, e, cats)
	}
}

// writeCaptureFile writes c as a file in track e's inbox/ on the assistant's machine and answers its path in the
// track's directory.
func (s *Service) writeCaptureFile(ctx context.Context, c db.Capture, sorted assistantwire.CaptureSort, e trackEntry, cats captureCats) (string, error) {
	machine := s.settings(ctx).MachineID
	if machine == "" || s.isWorkMachine(machine) || !s.trackInboxNode(machine) {
		return "", errNoTrackInbox
	}
	if d, ok := s.device(ctx, machine); !ok || !d.Online {
		return "", errNoTrackInbox
	}
	cctx, cancel := context.WithTimeout(ctx, captureFileTimeout)
	defer cancel()
	var res struct {
		Path string `json:"path"`
	}
	params := map[string]any{"dir": trackRel(e), "slug": e.Slug, "name": captureFileName(c, s.location(ctx)),
		"content": captureFileContent(c, sorted, cats.cats)}
	if err := s.call(cctx, machine, assistantwire.DeviceTrackInboxWrite, params, &res); err != nil {
		return "", err
	}
	return res.Path, nil
}

// captureFileName is c's file in a track's inbox/: the day it was captured, in the owner's zone, and its id.
func captureFileName(c db.Capture, loc *time.Location) string {
	return c.CreatedAt.In(loc).Format(time.DateOnly) + "-" + c.ID + ".md"
}

// captureFileContent is c's file: a small front matter (its id, when, from where, its categories), its title, then
// what was captured as it was.
func captureFileContent(c db.Capture, sorted assistantwire.CaptureSort, cats []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + c.ID + "\n")
	b.WriteString("captured_at: " + c.CreatedAt.UTC().Format(time.RFC3339) + "\n")
	b.WriteString("source: " + c.Source + "\n")
	b.WriteString("kind: " + c.Kind + "\n")
	b.WriteString("categories: [" + strings.Join(cats, ", ") + "]\n")
	b.WriteString("---\n\n")
	if sorted.Title != "" {
		b.WriteString("# " + sorted.Title + "\n\n")
	}
	if c.Text != "" {
		b.WriteString(c.Text + "\n")
	}
	if c.URL != "" {
		if c.Text != "" {
			b.WriteString("\n")
		}
		b.WriteString("<" + c.URL + ">\n")
	}
	return b.String()
}

// pushCaptureFiled tells the owner a capture was filed, quietly (P2): where and its title, or, for a sensitive one,
// only "1 capture".
func (s *Service) pushCaptureFiled(ctx context.Context, c db.Capture, sorted assistantwire.CaptureSort, e trackEntry, cats captureCats) {
	l := s.lang(ctx)
	s.push(withCard(ctx, cardCaptureSorted), Notification{Subtitle: "Persona", Title: i18n.T(l, "assistant-captures.push_title"),
		Body: captureLine(l, c, sorted, e, cats), Level: assistantwire.LevelP2, RelatedKey: "capture:" + c.ID})
}

// captureLine is how a filed capture is told outside the capture list (a push, a briefing): its title and track, or
// only "1 capture" when it is sensitive.
func captureLine(l i18n.Lang, c db.Capture, sorted assistantwire.CaptureSort, e trackEntry, cats captureCats) string {
	if cats.sensitive() {
		return i18n.T(l, "assistant-captures.count", i18n.Args{"count": 1})
	}
	title := sorted.Title
	if title == "" {
		title = clip(oneLine(captureNote(c)), captureTitleMax)
	}
	track := oneLine(e.Title)
	if track == "" {
		track = e.Slug
	}
	return i18n.T(l, "assistant-captures.filed", i18n.Args{"track": track, "title": title})
}
