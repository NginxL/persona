package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/prompts"
)

// captureMemStore keeps captures as assistant_captures does: one per client id, the loop's results only on a capture
// still new or sorted.
type captureMemStore struct {
	*infoCategoryMemStore
	capMu sync.Mutex
	caps  []db.Capture
}

func (st *captureMemStore) InsertCapture(_ context.Context, c db.Capture) (db.Capture, bool, error) {
	st.capMu.Lock()
	defer st.capMu.Unlock()
	for _, was := range st.caps {
		if was.ClientID == c.ClientID {
			return was, false, nil
		}
	}
	c.Status, c.Sort, c.FiledTo, c.CreatedAt, c.UpdatedAt = assistantwire.CaptureNew, []byte("{}"), []byte("{}"), time.Now().UTC(), time.Now().UTC()
	st.caps = append(st.caps, c)
	return c, true, nil
}

func (st *captureMemStore) Captures(_ context.Context, f db.CaptureFilter) ([]db.Capture, error) {
	st.capMu.Lock()
	defer st.capMu.Unlock()
	var out []db.Capture
	for i := len(st.caps) - 1; i >= 0; i-- {
		c := st.caps[i]
		if (len(f.Statuses) == 0 || slices.Contains(f.Statuses, c.Status)) && !c.UpdatedAt.Before(f.UpdatedSince) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (st *captureMemStore) CapturesToSort(_ context.Context, maxAttempts, limit int) ([]db.Capture, error) {
	st.capMu.Lock()
	defer st.capMu.Unlock()
	// New ones first, then the oldest, as the query orders them.
	var out []db.Capture
	for _, wantNew := range []bool{true, false} {
		for _, c := range st.caps {
			var sorted assistantwire.CaptureSort
			_ = json.Unmarshal(c.Sort, &sorted)
			isNew := c.Status == assistantwire.CaptureNew
			if isNew == wantNew && c.Attempts < maxAttempts && (isNew || c.Status == assistantwire.CaptureSorted && sorted.AutoFile) && len(out) < limit {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (st *captureMemStore) SetCaptureResult(_ context.Context, id, status string, sort, filedTo []byte, errText string, tried bool) (bool, error) {
	st.capMu.Lock()
	defer st.capMu.Unlock()
	for i, c := range st.caps {
		if c.ID != id || (c.Status != assistantwire.CaptureNew && c.Status != assistantwire.CaptureSorted) {
			continue
		}
		c.Status, c.Error, c.UpdatedAt = status, errText, time.Now().UTC()
		if sort != nil {
			c.Sort = sort
		}
		if filedTo != nil {
			c.FiledTo = filedTo
		}
		if tried {
			c.Attempts++
		}
		st.caps[i] = c
		return true, nil
	}
	return false, nil
}

func (st *captureMemStore) capture(t *testing.T, id string) db.Capture {
	t.Helper()
	st.capMu.Lock()
	defer st.capMu.Unlock()
	for _, c := range st.caps {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capture %s", id)
	return db.Capture{}
}

// inboxDevs are the manager's computers, whose nodes keep track directories and write into their inbox/ unless old.
type inboxDevs struct {
	*fleetDevs
	old bool
}

func (d inboxDevs) NodeAnnounced(_, capability string) bool {
	return capability == nodeproto.TrackSpaces || !d.old && capability == nodeproto.TrackInbox
}

// captureService is the health hub's assistant on a registry with two space tracks that take captures (qa-plain,
// qa-finance with finance by default) and one that does not, the node answering inbox writes into files.
func captureService(t *testing.T) (*Service, *fakeHub, *captureMemStore, *[]map[string]any) {
	t.Helper()
	f := healthHub()
	f.devices[0].Online = true
	st := &captureMemStore{infoCategoryMemStore: &infoCategoryMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: "qa-plain", Kind: trackKindSpace, Title: "QA plain", Template: trackTemplateSpace, Revision: 1},
		{Slug: "qa-finance", Kind: trackKindSpace, Title: "QA finance", Template: trackTemplateSpace, DefaultCategories: []string{categoryFinance}, Revision: 1},
		{Slug: "qa-closed", Kind: trackKindSpace, Title: "QA closed", Template: trackTemplateSpace, ConfigOverride: []byte(`{"capture_target":false}`), Revision: 1},
	}}}}
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server" })
	s.devs = inboxDevs{fleetDevs: newFleetDevs()}
	var written []map[string]any
	f.nodes = map[string]nodeAnswer{assistantwire.DeviceTrackInboxWrite: func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		written = append(written, p)
		return map[string]any{"path": "inbox/" + p["name"].(string)}, nil
	}}
	return s, f, st, &written
}

// The sorting prompt (#13502), byte for byte; the note goes as data in the user message, cut at 2,000 characters; an
// answer keeps only known categories, a known track and action, a confidence between 0 and 1.
func TestCaptureSortPrompt(t *testing.T) {
	ctx := context.Background()
	m := &infoTagModel{answer: `{"space":"qa-plain","confidence":1.7,"action":"later","title":"  A placeholder\ntitle ","deep":true,"categories":["Finance","someday"]}`}
	note := captureNote(db.Capture{Text: strings.Repeat("x", captureNoteMax+10), URL: "https://example.com/qa"})
	if len([]rune(note)) != captureNoteMax {
		t.Fatalf("note of %d characters", len([]rune(note)))
	}
	in := captureSortInput{Note: "qa: <a placeholder> & note", Source: assistantwire.CaptureSourceWatch,
		Spaces: []captureSpace{{Slug: "qa-plain", Title: "QA plain"}}}
	a, err := sortCaptureWith(ctx, m, in)
	if err != nil || !slices.Equal(a.Categories, []string{categoryFinance}) {
		t.Fatalf("answer %+v, %v", a, err)
	}
	golden(t, "capture_sort", m.system[0])
	if m.system[0] != prompts.RenderCaptureSort() {
		t.Error("the system prompt is not the template")
	}
	if want := `{"note":"qa: <a placeholder> & note","source":"watch","spaces":[{"slug":"qa-plain","title":"QA plain"}]}`; m.user[0] != want {
		t.Errorf("user message = %s, want %s", m.user[0], want)
	}
	targets := []trackEntry{{Slug: "qa-plain"}, {Slug: "qa-finance"}}
	got := captureSortOf(a, targets, "")
	if got.Track != "qa-plain" || got.Confidence != 1 || got.Action != "keep" || got.Title != "A placeholder title" || !got.Deep || !got.AutoFile {
		t.Errorf("sorted = %+v", got)
	}
	unsure := "qa-plain"
	if got := captureSortOf(captureSortAnswer{Space: &unsure, Confidence: 0.79, Action: "research"}, targets, ""); got.AutoFile || got.Track != "qa-plain" || got.Action != "research" {
		t.Errorf("an unsure answer = %+v: sorted, left for the user", got)
	}
	other := "qa-elsewhere"
	if got := captureSortOf(captureSortAnswer{Space: &other, Confidence: 0.99}, targets, ""); got.Track != "" || got.AutoFile {
		t.Errorf("a track that takes no captures = %+v", got)
	}
	if got := captureSortOf(captureSortAnswer{Confidence: 0.1}, targets, "qa-finance"); got.Track != "qa-finance" || !got.AutoFile {
		t.Errorf("the user's choice = %+v: filed there", got)
	}
}

// Taking a capture in (#13502): one write, answered at once, the same client id delivered once; only a full-access
// client adds or reads; what is not a capture is refused.
func TestCaptureCreate(t *testing.T) {
	s, f, st, _ := captureService(t)
	body := `{"client_id":"qa-watch-0001","source":"watch","kind":"voice","text":"qa: a placeholder note","future_field":1}`
	w := serve(s, http.MethodPost, "/api/v1/assistant/captures", body)
	var first assistantwire.CaptureAccepted
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &first) != nil || !strings.HasPrefix(first.ID, "cap_") ||
		first.Status != assistantwire.CaptureNew || first.Duplicate {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	w = serve(s, http.MethodPost, "/api/v1/assistant/captures", body)
	var again assistantwire.CaptureAccepted
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &again) != nil || again.ID != first.ID || !again.Duplicate {
		t.Fatalf("an offline watch's retry: %d %s", w.Code, w.Body)
	}
	if len(st.caps) != 1 {
		t.Fatalf("%d captures kept, want one", len(st.caps))
	}
	if len(f.pushes) != 0 || len(f.captureSortAsked) != 0 {
		t.Errorf("taking a capture in pushed %d, asked the model %d times", len(f.pushes), len(f.captureSortAsked))
	}
	for _, bad := range []string{
		`{"client_id":"short","source":"watch","kind":"text","text":"x"}`,
		`{"client_id":"qa-watch-0002","source":"fax","kind":"text","text":"x"}`,
		`{"client_id":"qa-watch-0002","source":"watch","kind":"image","text":"x"}`,
		`{"client_id":"qa-watch-0002","source":"watch","kind":"text","text":"  "}`,
		`{"client_id":"qa-watch-0002","source":"web","kind":"url","url":"file:///etc/hosts"}`,
		`{"client_id":"qa-watch-0002","source":"web","kind":"text","text":"x","space_hint":"qa-closed"}`,
		`{"client_id":"qa-watch-0002","source":"web","kind":"text","text":"x","space_hint":"health"}`,
	} {
		if w := serve(s, http.MethodPost, "/api/v1/assistant/captures", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := serveAs(s, roleExtension, http.MethodPost, "/api/v1/assistant/captures", body); w.Code != http.StatusForbidden {
		t.Errorf("an extension adds: %d", w.Code)
	}
	if w := serveAs(s, roleExtension, http.MethodGet, "/api/v1/assistant/captures", ""); w.Code != http.StatusForbidden {
		t.Errorf("an extension reads: %d", w.Code)
	}
	w = serve(s, http.MethodGet, "/api/v1/assistant/captures?status=new", "")
	var list assistantwire.Captures
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Captures) != 1 || list.Captures[0].ID != first.ID ||
		list.Captures[0].Tagged || list.Captures[0].Categories == nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := serve(s, http.MethodGet, "/api/v1/assistant/captures?status=lost", ""); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown status: %d", w.Code)
	}
}

// Sorting (#13502): one model call sorts and tags; a sure one is filed into its track's inbox/ and pushed quietly, a
// sensitive one's push says only "1 capture"; an unsure one stays in the list; the main chat gets no turn.
func TestCaptureSortAndFile(t *testing.T) {
	ctx := context.Background()
	s, f, st, written := captureService(t)
	add := func(client, text, hint string) string {
		t.Helper()
		body, _ := json.Marshal(assistantwire.CaptureCreate{ClientID: client, Source: assistantwire.CaptureSourceShareIOS, Kind: "text", Text: text, SpaceHint: hint})
		w := serve(s, http.MethodPost, "/api/v1/assistant/captures", string(body))
		var a assistantwire.CaptureAccepted
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &a) != nil {
			t.Fatalf("add: %d %s", w.Code, w.Body)
		}
		return a.ID
	}
	sort := func(answer string) {
		t.Helper()
		f.mu.Lock()
		f.captureSort = answer
		f.mu.Unlock()
		s.captureRound(ctx, st)
	}

	plain := add("qa-share-0001", "qa: a placeholder reading list", "")
	sort(`{"space":"qa-plain","confidence":0.92,"action":"keep","title":"Reading list","deep":false,"categories":[]}`)
	c := st.capture(t, plain)
	if c.Status != assistantwire.CaptureFiled || len(*written) != 1 {
		t.Fatalf("a sure capture: %+v, %d written", c, len(*written))
	}
	p := (*written)[0]
	name, _ := p["name"].(string)
	content, _ := p["content"].(string)
	if p["slug"] != "qa-plain" || !strings.HasSuffix(p["dir"].(string), "tracks/qa-plain") || !strings.HasSuffix(name, "-"+plain+".md") ||
		!strings.Contains(content, "\n# Reading list\n\nqa: a placeholder reading list\n") || !strings.Contains(content, "categories: []\n") {
		t.Errorf("written %v", p)
	}
	if len(f.captureSortAsked) != 1 || !strings.Contains(f.captureSortAsked[0].User, `"slug":"qa-finance"`) || strings.Contains(f.captureSortAsked[0].User, "qa-closed") ||
		strings.Contains(f.captureSortAsked[0].User, `"health"`) {
		t.Errorf("asked %+v", f.captureSortAsked)
	}
	l := s.lang(ctx)
	if len(f.pushes) != 1 || f.pushes[0].Level != assistantwire.LevelP2 || f.pushes[0].Body != "Reading list → QA plain" || f.pushes[0].SessionID != "" {
		t.Fatalf("pushes %+v", f.pushes)
	}

	// A finance note, filed into qa-finance: its push and its briefing line say only "1 capture".
	money := add("qa-share-0002", "qa: a placeholder monthly budget", "")
	sort(`{"space":"qa-finance","confidence":0.95,"action":"keep","title":"Monthly budget","categories":["finance"]}`)
	if c := st.capture(t, money); c.Status != assistantwire.CaptureFiled {
		t.Fatalf("a sensitive capture: %+v", c)
	}
	one := i18n.T(l, "assistant-captures.count", i18n.Args{"count": 1})
	if len(f.pushes) != 2 || f.pushes[1].Body != one || strings.Contains(f.pushes[1].Title+f.pushes[1].Body, "budget") ||
		strings.Contains(f.pushes[1].Body, "QA finance") {
		t.Fatalf("a sensitive push: %+v", f.pushes)
	}
	if !strings.Contains((*written)[1]["content"].(string), "categories: [finance]\n") {
		t.Errorf("the sensitive file: %v", (*written)[1])
	}

	// An unsure one stays in the list, sorted; nothing written, nothing pushed.
	unsure := add("qa-share-0003", "qa: a placeholder thought", "")
	sort(`{"space":"qa-plain","confidence":0.4,"action":"discuss","title":"A thought","categories":[]}`)
	if c := st.capture(t, unsure); c.Status != assistantwire.CaptureSorted || len(*written) != 2 || len(f.pushes) != 2 {
		t.Fatalf("an unsure capture: %+v, %d written, %d pushed", c, len(*written), len(f.pushes))
	}

	lines := s.captureBriefLines(ctx, l, time.Now().Add(-time.Hour))
	want := []string{i18n.T(l, "assistant-captures.waiting", i18n.Args{"count": 1}), "Reading list → QA plain", one}
	if !slices.Equal(lines, want) {
		t.Errorf("briefing lines = %q, want %q", lines, want)
	}
	for _, line := range lines {
		if strings.Contains(line, "budget") || strings.Contains(line, "finance") {
			t.Errorf("a briefing line names the sensitive capture: %q", line)
		}
	}
	if n := len(f.asked) + len(f.sends); n != 0 {
		t.Errorf("the main chat was asked or sent to %d times", n)
	}
}

// Filing waits for a computer that can (#13502): on an older node nothing is written and no try is counted; once the
// node can, the capture is filed without asking the model again. A model that fails counts a try each time and gives
// up as failed, still listed.
func TestCaptureFileWaitsAndFails(t *testing.T) {
	ctx := context.Background()
	s, f, st, written := captureService(t)
	s.devs = inboxDevs{fleetDevs: newFleetDevs(), old: true}
	body := `{"client_id":"qa-share-0010","source":"share_android","kind":"text","text":"qa: a placeholder","space_hint":"qa-plain"}`
	w := serve(s, http.MethodPost, "/api/v1/assistant/captures", body)
	var a assistantwire.CaptureAccepted
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &a) != nil {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	f.mu.Lock()
	f.captureSort = `{"space":null,"confidence":0.1,"action":"keep","title":"Placeholder","categories":[]}`
	f.mu.Unlock()
	s.captureRound(ctx, st)
	c := st.capture(t, a.ID)
	var sorted assistantwire.CaptureSort
	_ = json.Unmarshal(c.Sort, &sorted)
	if c.Status != assistantwire.CaptureSorted || !sorted.AutoFile || sorted.Track != "qa-plain" || c.Attempts != 0 || len(*written) != 0 {
		t.Fatalf("on an older node: %+v %+v, %d written", c, sorted, len(*written))
	}
	s.devs = inboxDevs{fleetDevs: newFleetDevs()}
	s.captureRound(ctx, st)
	f.mu.Lock()
	asked := len(f.captureSortAsked)
	f.mu.Unlock()
	if c := st.capture(t, a.ID); c.Status != assistantwire.CaptureFiled || len(*written) != 1 || asked != 1 {
		t.Fatalf("once the node can: %+v, %d written, asked %d times", c, len(*written), asked)
	}

	failing := `{"client_id":"qa-share-0011","source":"web","kind":"text","text":"qa: another placeholder"}`
	w = serve(s, http.MethodPost, "/api/v1/assistant/captures", failing)
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &a) != nil {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	f.mu.Lock()
	f.captureSort = ""
	f.mu.Unlock()
	for range captureMaxAttempts + 1 {
		s.captureRound(ctx, st)
	}
	if c := st.capture(t, a.ID); c.Status != assistantwire.CaptureFailed || c.Attempts != captureMaxAttempts {
		t.Fatalf("a model that keeps failing: %+v", c)
	}
}

// Captures waiting for a computer that can file them count no try, so they never leave the queue on their own: more of
// them than a batch must not keep a new capture from being sorted (#13502).
func TestCaptureWaitingNeverStarvesNew(t *testing.T) {
	ctx := context.Background()
	s, f, st, written := captureService(t)
	s.devs = inboxDevs{fleetDevs: newFleetDevs(), old: true}
	add := func(client, hint string) string {
		t.Helper()
		body, _ := json.Marshal(assistantwire.CaptureCreate{ClientID: client, Source: assistantwire.CaptureSourceShareIOS, Kind: "text", Text: "qa: a placeholder", SpaceHint: hint})
		w := serve(s, http.MethodPost, "/api/v1/assistant/captures", string(body))
		var a assistantwire.CaptureAccepted
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &a) != nil {
			t.Fatalf("add: %d %s", w.Code, w.Body)
		}
		return a.ID
	}
	f.mu.Lock()
	f.captureSort = `{"space":null,"confidence":0.1,"action":"keep","title":"Placeholder","categories":[]}`
	f.mu.Unlock()
	for i := range captureBatch + 1 {
		add("qa-share-wait-"+string(rune('a'+i)), "qa-plain")
	}
	for s.captureRound(ctx, st) {
	}
	waiting, err := st.CapturesToSort(ctx, captureMaxAttempts, captureBatch+10)
	if err != nil || len(waiting) != captureBatch+1 || len(*written) != 0 {
		t.Fatalf("waiting to be filed: %d (%v), %d written", len(waiting), err, len(*written))
	}
	for _, c := range waiting {
		if c.Status != assistantwire.CaptureSorted || c.Attempts != 0 {
			t.Fatalf("a waiting capture: %+v", c)
		}
	}

	fresh := add("qa-share-wait-new", "")
	f.mu.Lock()
	f.captureSort = `{"space":"qa-plain","confidence":0.4,"action":"discuss","title":"A thought","categories":[]}`
	f.mu.Unlock()
	s.captureRound(ctx, st)
	if c := st.capture(t, fresh); c.Status != assistantwire.CaptureSorted {
		t.Fatalf("the new capture behind %d waiting ones: %+v", captureBatch+1, c)
	}
}

// The briefing's quick-capture section, as the model is given it (#13502).
func TestGoldenBriefFactsCaptures(t *testing.T) {
	lines := []string{"2 条待归类", "示例标题 → qa-plain", "1 条随手记"}
	golden(t, "routine_facts.captures", prompts.RenderBriefFacts(prompts.BriefFacts{Hours: 12, Captures: lines}))
	golden(t, "routine_facts.captures_card", prompts.RenderBriefFacts(prompts.BriefFacts{Card: true, Hours: 12, Captures: lines}))
}
