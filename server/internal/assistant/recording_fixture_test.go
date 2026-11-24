package assistant

// The recording module's fixtures the core's integration tests use (the module's own tests have theirs, in
// internal/modules/recording): a knowledge base, the main chat, the meetings' routes over HTTP. Placeholders only.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	meetingRec   = "33333333-3333-4333-8333-333333333333"
	meetingOther = "44444444-4444-4444-8444-444444444444"
)

const (
	dayRec   = "55555555-5555-4555-8555-555555555555"
	dayPart2 = "66666666-6666-4666-8666-666666666666"
)

const (
	dayNotesReply = `{"summary":"A standup and the export review.","entries":[` +
		`{"from_line":1,"to_line":1,"kind":"outside","title":"Standup","points":[{"text":"Standup on the export","line":1}]},` +
		`{"from_line":2,"to_line":3,"kind":"meeting","title":"Export review","points":[{"text":"The export drops big entries","line":2,"who":"Ada"},` +
		`{"text":"Bo takes the flaky test","line":3}]}],"actions":[{"who":"Bo","what":"Fix the flaky upload test","line":3}]}`
	dayMergeReply = `{"summary":"A standup, then the export review.","timeline":[` +
		`{"from_line":1,"to_line":1,"kind":"outside","title":"Standup","meeting":0,"points":[{"text":"Standup on the export","line":1}]},` +
		`{"from_line":2,"to_line":3,"kind":"meeting","title":"Export review","meeting":0,"points":[{"text":"The export drops big entries","line":2},` +
		`{"text":"Bo takes the flaky test","line":3}]}],"actions":[{"who":"Bo","what":"Fix the flaky upload test","by":"tomorrow","line":3}]}`
)

const (
	photoOne   = "b0000000-0000-4000-8000-000000000001"
	photoTwo   = "b0000000-0000-4000-8000-000000000002"
	photoThree = "b0000000-0000-4000-8000-000000000003"
	photoFour  = "b0000000-0000-4000-8000-000000000004"
)

// meetingFixture is an assistant with a knowledge base and the main chat c-main on server, served over HTTP.
type meetingFixture struct {
	t     *testing.T
	s     *Service
	f     *fakeHub
	srv   *httptest.Server
	model *meetingModelFake
}

func newMeetingFixture(t *testing.T) *meetingFixture {
	t.Helper()
	needsRecordingModule(t)
	f := newFakeHub().withMachine()
	// Background answers may log after a test returns: they log nowhere.
	s := kbServiceOn(t, f, newKBStore(), t.TempDir(), func(string, ...any) {})
	applyShortRec(t, s)
	setSettings(t, s, func(v *Settings) { v.Main = "c-main" })
	model := &meetingModelFake{reply: `{"question":"","answer":"We kept the cap.","sources":[]}`}
	s.meetingModel = model
	feed := trackAttendFeed(s)
	srv := httptest.NewServer(s)
	m := &meetingFixture{t: t, s: s, f: f, srv: srv, model: model}
	t.Cleanup(func() {
		m.idle()
		srv.Close()
		feed.stop()
	})
	return m
}

// attendFeedTracker runs the attending feed's looks on real timers and, at the end of a test, waits for the ones
// already running: a look writes the recording's live log, which must not land in the test's directory while it is
// being removed (#5510). One that comes due after stop does nothing.
type attendFeedTracker struct {
	mu      sync.Mutex
	stopped bool
	running sync.WaitGroup
}

func trackAttendFeed(s *Service) *attendFeedTracker {
	tr := &attendFeedTracker{}
	recAfter(s, func(d time.Duration, fn func()) func() bool {
		return time.AfterFunc(d, func() {
			tr.mu.Lock()
			if tr.stopped {
				tr.mu.Unlock()
				return
			}
			tr.running.Add(1)
			tr.mu.Unlock()
			defer tr.running.Done()
			fn()
		}).Stop
	})
	return tr
}

func (tr *attendFeedTracker) stop() {
	tr.mu.Lock()
	tr.stopped = true
	tr.mu.Unlock()
	tr.running.Wait()
}

// idle waits until no question is being answered and no hub run goes on (they write into the test's directory).
func (m *meetingFixture) idle() {
	m.t.Helper()
	recWait(m.t, m.s)
}

func (m *meetingFixture) engine() *kb.Engine { return kbEng(m.t, m.s) }

// do sends body (a string as it is, anything else as JSON) with role (none: full) and wants status; the decoded body.
func (m *meetingFixture) do(method, path, role string, body any, want int) map[string]any {
	m.t.Helper()
	var data []byte
	switch b := body.(type) {
	case nil:
	case string:
		data = []byte(b)
	default:
		data, _ = json.Marshal(b)
	}
	req, _ := http.NewRequest(method, m.srv.URL+path, bytes.NewReader(data))
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	req.Header.Set("Accept-Language", "ja")
	resp, err := m.srv.Client().Do(req)
	if err != nil {
		m.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		m.t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, raw, want)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		m.t.Fatalf("%s %s: %s", method, path, raw)
	}
	return out
}

// upload PUTs recording id: a phone meeting started an hour ago, with fields over the defaults.
func (m *meetingFixture) upload(id string, revision int, fields map[string]any) map[string]any {
	m.t.Helper()
	body := map[string]any{"revision": revision, "started_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "device": "phone",
		"kind": "meeting", "language": "en-US", "segments": []any{}, "complete": false}
	for k, v := range fields {
		body[k] = v
	}
	return m.do("PUT", "/api/v1/recordings/"+id, "", body, http.StatusOK)
}

func livePath(id string) string { return "/api/v1/recordings/" + id + "/live" }

func wantCode(t *testing.T, body map[string]any, code string) {
	t.Helper()
	if body["code"] != code {
		t.Fatalf("code %v, want %s: %v", body["code"], code, body)
	}
}

// meetingModelFake answers every meeting.answer call with reply (or err); with a gate each call first waits for one
// value on it (or its context's end).
type meetingModelFake struct {
	mu    sync.Mutex
	calls []modelCall
	reply string
	err   error
	gate  chan struct{}
}

func (f *meetingModelFake) JSONMax(ctx context.Context, system, user string, _ int, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, modelCall{system, user})
	reply, err, gate := f.reply, f.err, f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(reply), out)
}

func (f *meetingModelFake) called() []modelCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// dayFixture is the attending routes' fixture with the knowledge base's model answering recaps, in an owner's zone
// where it is about noon now (Etc/GMT±n), so the recap time is either well before now or well after it whatever the
// hour the test runs at.
type dayFixture struct {
	*meetingFixture
	model  *dayModel
	offset int // the owner's zone, hours east of UTC
	owner  *time.Location
}

func newDayFixture(t *testing.T) *dayFixture {
	t.Helper()
	m := newMeetingFixture(t)
	offset := 12 - time.Now().UTC().Hour() // -11 … 12: local time is 12:xx
	d := &dayFixture{meetingFixture: m, model: &dayModel{}, offset: offset, owner: etcZone(t, offset)}
	setSettings(t, m.s, func(v *Settings) { v.Timezone = d.owner.String() }) // before the knowledge base opens in it
	e := m.engine()
	e.Model, e.ModelFor = d.model, func(string) kb.Model { return d.model }
	// The hub's Lark bot (not paired) and channels (none), which the day's push asks of: it waits for them (kb_day.go).
	m.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return FeishuStatus{}, nil })
	m.f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) { return map[string]any{"channels": []channel.View{}}, nil })
	return d
}

func segmentsPath(id string) string { return "/api/v1/recordings/" + id + "/segments" }

// dayLines are three sentences of the day: one at 08:01 and two after 09:00 (two hours of its clock), with an
// interruption between.
func dayLines(base, revision int64) map[string]any {
	return map[string]any{"base": base, "revision": revision, "from": 0, "segments": []map[string]any{
		{"start": 60, "end": 65, "text": "Morning standup about the export."},
		{"start": 3720, "end": 3725, "text": "Ada: the export drops big entries."},
		{"start": 3800, "end": 3805, "text": "Bo will fix the flaky upload test."}},
		"coverage": map[string]any{"to": 3900, "gaps": []map[string]any{{"from": 600, "to": 1500, "reason": "interrupted"}}}}
}

// setEvening sets the evening routine's time (enabled or not, it is the recap time's).
func setEvening(t *testing.T, s *Service, at string, enabled bool) {
	t.Helper()
	sc, err := s.lockedSchedule(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.mu.Unlock()
	for i := range sc.tasks {
		if sc.tasks[i].ID == "evening" {
			sc.tasks[i].Time, sc.tasks[i].Enabled = at, enabled
			return
		}
	}
	t.Fatal("no evening routine")
}

// minutesModel answers every knowledge-base call with reply.
type minutesModel struct{ reply string }

// toolText is a tool result's text.
func toolText(res any) string {
	switch v := res.(type) {
	case proto.ToolResult:
		var b strings.Builder
		for _, c := range v.Content {
			b.WriteString(c.Text)
		}
		return b.String()
	case string:
		return v
	}
	raw, _ := json.Marshal(res)
	return string(raw)
}

// zoneApart is a time zone whose offset at t is neither UTC's nor this machine's, for a test that a clock time is
// written in the user's zone (Settings.Timezone) and not in the fallback.
func zoneApart(t *testing.T, at time.Time) *time.Location {
	t.Helper()
	_, local := at.In(time.Local).Zone()
	for _, name := range []string{"Asia/Kathmandu", "America/St_Johns", "Australia/Eucla"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		if _, off := at.In(loc).Zone(); off != 0 && off != local {
			return loc
		}
	}
	t.Fatal("no time zone apart from UTC and this machine's")
	return nil
}

func photosPath(id string) string { return "/api/v1/recordings/" + id + "/photos" }

// pngData is a w × h PNG in base64.
func pngData(t *testing.T, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// jpegData is a w × h JPEG in base64.
func jpegData(t *testing.T, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// photoBody is a POST …/photos body: a 16 × 9 PNG taken at seconds into the recording, with fields over it.
func photoBody(t *testing.T, id string, at float64, fields map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"id": id, "at": at, "mime": "image/png", "data": pngData(t, 16, 9)}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

// roomFixture is a meetingFixture whose node starts rooms, with the feeder's timers off: the test looks itself.
type roomFixture struct {
	*meetingFixture
	prepared []map[string]any
	mu       sync.Mutex
	due      []func() // the feeder's looks set and not run yet (look)
}

func newRoomFixture(t *testing.T) *roomFixture {
	t.Helper()
	m := newMeetingFixture(t)
	r := &roomFixture{meetingFixture: m}
	// The feeder's looks wait for the test (look): each timer it sets is kept, never run by itself.
	recAfter(m.s, func(_ time.Duration, fn func()) func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.due = append(r.due, fn)
		return func() bool { return true }
	})
	m.f.mu.Lock()
	m.f.nodes = map[string]nodeAnswer{DeviceAssistantGuestPrepare: func(_ string, params json.RawMessage) (any, error) {
		var a map[string]any
		_ = json.Unmarshal(params, &a)
		r.prepared = append(r.prepared, a)
		return map[string]any{"cwd": "/home/me/.persona/guest-meeting/" + path.Base(a["dir"].(string)), "kind": "meeting", "start_ids": true}, nil
	}}
	m.f.sessions["server"] = []proto.Session{{SessionID: "c-main", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle}}
	m.f.known["c-main"] = true
	m.f.hello.Locale = "en"
	m.f.mu.Unlock()
	recTimings(t, m.s, map[string]time.Duration{"askRoomPoll": 5 * time.Millisecond}, nil) // the room asks' polls short
	return r
}

// isCode: err is an rpc error with code.
func isCode(err error, code string) bool {
	var e *rpc.Error
	return errors.As(err, &e) && e.Code == code
}

// phoneSegs are upload segments.
func phoneSegs(lines ...fakeLine) []map[string]any {
	out := []map[string]any{}
	for _, l := range lines {
		out = append(out, map[string]any{"start": l.start, "end": l.end, "text": l.text})
	}
	return out
}

// look looks at the recordings once, as the feeder's timers would: each look it set since the last one.
func (r *roomFixture) look(string) {
	r.mu.Lock()
	due := r.due
	r.due = nil
	r.mu.Unlock()
	for _, fn := range due {
		fn()
	}
}

// roomUp uploads id as a room meeting in role and starts its room; the room's session id, listed idle.
func (r *roomFixture) roomUp(id, role string) string {
	r.t.Helper()
	return r.roomUpWith(id, role, nil)
}

// roomUpWith is roomUp with more fields on the upload (its started_at, say).
func (r *roomFixture) roomUpWith(id, role string, more map[string]any) string {
	r.t.Helper()
	fields := map[string]any{"room": map[string]any{"attendance": role}, "title": "Design review", "private": false}
	maps.Copy(fields, more)
	out := r.upload(id, 1, fields)
	if room, _ := out["room"].(map[string]any); room["state"] != "none" || out["attendance"] != role {
		r.t.Fatalf("upload: %v", out)
	}
	r.look(id)
	m := r.meeting(id)
	if m.Room == nil || m.Room.State != kb.RoomReady {
		r.t.Fatalf("room %+v", m.Room)
	}
	r.f.mu.Lock()
	r.f.sessions["server"] = append(r.f.sessions["server"], proto.Session{SessionID: m.Room.SessionID, Assistant: true, Status: proto.StatusIdle,
		State: proto.StateIdle, Origin: OriginMeeting})
	r.f.mu.Unlock()
	return m.Room.SessionID
}

func (r *roomFixture) sends() []map[string]any {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return slices.Clone(r.f.sends)
}

// dayModel answers a chunk's notes and the day's merge with fixed answers citing lines 1–3 (the knowledge base keeps
// of a chunk's notes only what cites its own lines), and counts them.
type dayModel struct {
	mu            sync.Mutex
	notes, merges int
}

func (m *dayModel) JSONMax(_ context.Context, system, _ string, _ int, out any) error {
	m.mu.Lock()
	reply := dayNotesReply
	if strings.Contains(system, "写这一天的全天回顾") {
		reply, m.merges = dayMergeReply, m.merges+1
	} else {
		m.notes++
	}
	m.mu.Unlock()
	return json.Unmarshal([]byte(reply), out)
}

// etcZone is the IANA zone hours east of UTC (Etc/GMT's signs are the other way round).
func etcZone(t *testing.T, hours int) *time.Location {
	t.Helper()
	name := "Etc/GMT"
	switch {
	case hours > 0:
		name = fmt.Sprintf("Etc/GMT-%d", hours)
	case hours < 0:
		name = fmt.Sprintf("Etc/GMT+%d", -hours)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// putDay PUTs day recording id at revision (its base), started at 08:00 today in tz, with fields over the defaults;
// the 200's body.
func (d *dayFixture) putDay(id string, revision int64, tz *time.Location, fields map[string]any) map[string]any {
	d.t.Helper()
	start, day := today8(tz)
	body := map[string]any{"revision": revision, "kind": "day", "day": day, "tz": tz.String(), "part": 1,
		"started_at": start.Format(time.RFC3339), "device": "watch", "language": "en-US", "segments": []any{}, "complete": false,
		"coverage": map[string]any{"to": 0}}
	for k, v := range fields {
		body[k] = v
	}
	return d.do("PUT", "/api/v1/recordings/"+id, "", body, http.StatusOK)
}

// recap runs the recap lane until recording id's recap is ready (or a few rounds went by).
func (d *dayFixture) recap(id string) *kb.DayRecap {
	d.t.Helper()
	e := d.engine()
	for range 5 {
		e.WriteDayRecaps(context.Background(), 1)
		if r, ok := e.Store.Recording(id); ok && r.RecapState == kb.MinutesReady {
			rc, err := e.ReadDayRecap(id)
			if err != nil {
				d.t.Fatal(err)
			}
			return rc
		}
	}
	r, _ := d.engine().Store.Recording(id)
	d.t.Fatalf("no recap of %s: %+v", id, r)
	return nil
}

// addPhoto POSTs a photo of recording id; the photo the answer carries.
func (m *meetingFixture) addPhoto(id string, body map[string]any) map[string]any {
	m.t.Helper()
	p, ok := m.do("POST", photosPath(id), "", body, http.StatusOK)["photo"].(map[string]any)
	if !ok {
		m.t.Fatalf("no photo for %v", body["id"])
	}
	return p
}

// photosOf are the photos GET …/photos lists.
func (m *meetingFixture) photosOf(id string) []map[string]any {
	m.t.Helper()
	var out []map[string]any
	for _, p := range m.do("GET", photosPath(id), "", nil, http.StatusOK)["photos"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

// minutesFor writes minutes for recording id from reply, as the minutes round would.
func (m *meetingFixture) minutesFor(id, reply string) {
	m.t.Helper()
	e := m.engine()
	model := minutesModel{reply: reply}
	e.Model, e.ModelFor = model, func(string) kb.Model { return model }
	if n := e.WriteMinutes(context.Background(), 5); n < 1 {
		m.t.Fatalf("no minutes written for %s", id)
	}
}

// earlierMeeting is a meeting of a week ago with minutes: a decision and Bo's action item, confirmed as a to-do. The
// to-do's id.
func (m *meetingFixture) earlierMeeting(transcript string) string {
	m.t.Helper()
	m.do("PUT", "/api/v1/recordings/"+earlier, "", map[string]any{"revision": 1, "started_at": time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"ended_at": time.Now().Add(-7*24*time.Hour + time.Hour).UTC().Format(time.RFC3339), "device": "phone", "kind": "meeting",
		"title": "Design review", "complete": true, "segments": []map[string]any{{"start": 0, "end": 9, "text": transcript},
			{"start": 10, "end": 19, "text": "We keep the cap"}, {"start": 20, "end": 29, "text": "Bo adds the test"}}}, http.StatusOK)
	m.minutesFor(earlier, `{"title":"Design review","points":[{"text":"Exports over 4 MB are dropped","line":1,"who":"Ada"}],`+
		`"decisions":[{"text":"Keep the cap for this release","line":2}],"actions":[{"who":"Bo","what":"Add the regression test","by":"Friday","line":3},`+
		`{"who":"Ada","what":"Write the release note","line":3}],"open":[{"text":"Per entry or per recording?","line":2}]}`)
	out := m.do("POST", "/api/v1/recordings/"+earlier+"/minutes/todos", "", map[string]any{"version": 1, "actions": []string{"a1"}}, http.StatusOK)
	// Material for later meetings only once marked not private (#3862).
	m.do("PUT", "/api/v1/recordings/"+earlier+"/private", "", map[string]any{"private": false}, http.StatusOK)
	return out["todos"].([]any)[0].(map[string]any)["id"].(string)
}

func (m minutesModel) JSONMax(_ context.Context, _, _ string, _ int, out any) error {
	return json.Unmarshal([]byte(m.reply), out)
}

// meeting is recording id's meeting.json now.
func (r *roomFixture) meeting(id string) kb.Meeting {
	r.t.Helper()
	m, _, err := r.engine().MeetingRead(id)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

// today8 is 08:00 of today in loc, and today's date there.
func today8(loc *time.Location) (time.Time, string) {
	n := time.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 8, 0, 0, 0, loc), n.Format(time.DateOnly)
}

// sentOnce are the once-only sends the fake hub took (session.send_once).
func (r *roomFixture) sentOnce() []map[string]any {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return slices.Clone(r.f.sentOnce)
}
