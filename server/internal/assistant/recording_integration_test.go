package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/recordinghost"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The recap time is the evening routine's, 30 minutes before (kb's): its time whether or not it is on; none in the
// schedule is the default.
func TestDayRecapClockIsTheEveningRoutines(t *testing.T) {
	s := newServiceOn(t, newFakeHub(), newKBStore())
	if h, m := s.eveningClock(); h != 21 || m != 30 {
		t.Fatalf("default %d:%d", h, m)
	}
	setEvening(t, s, "19:15", false)
	if h, m := s.eveningClock(); h != 19 || m != 15 {
		t.Fatalf("off %d:%d", h, m)
	}
	sc, _ := s.lockedSchedule(context.Background())
	sc.tasks = slices.DeleteFunc(sc.tasks, func(t Task) bool { return t.ID == "evening" })
	sc.mu.Unlock()
	if h, m := s.eveningClock(); h != -1 || m != -1 {
		t.Fatalf("without one %d:%d", h, m)
	}
	e := &kb.Engine{} // the recording module's name for a day recording (its Hook)
	s.recordingHook(e)
	if e.DayRecordingName == nil {
		return // no recording module in this build
	}
	if got := e.DayRecordingName("2026-11-11"); got != i18n.T(s.lang(context.Background()), "assistant-day-recap.name", i18n.Args{"date": "2026-11-11"}) {
		t.Fatalf("name %q", got)
	}
}

// The manager's hello names recording_day, which the hub lists only from the default manager.
func TestBusHelloNamesRecordingDay(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://[fd00::20]:18660", Token: "t"})
	p, _ := b.client.HelloParams().(map[string]any)
	caps, _ := p["capabilities"].([]string)
	// The recording module's capabilities, only when the build has it (persona_without_recording leaves them out).
	if on := recordinghost.InBuild(); slices.Contains(caps, assistantwire.CapabilityRecordingDay) != on ||
		slices.Contains(caps, assistantwire.CapabilityMeetingAttendance) != on {
		t.Fatalf("capabilities %v (module in the build: %v)", caps, on)
	}
}

// The section stays about 4000 runes: entries from the middle of the day go first, the day's start and its latest
// hours stay.
func TestEveningFactsDayIsBounded(t *testing.T) {
	d := &prompts.BriefDay{Date: "2026-11-11", From: "08:00", To: "22:00"}
	for i := range 200 {
		d.Timeline = append(d.Timeline, prompts.BriefDayEntry{From: fmt.Sprintf("e%03d", i), To: "x", Title: strings.Repeat("长", 50)})
	}
	for i := range 50 {
		d.Actions = append(d.Actions, prompts.BriefDayAction{Who: "Bo", What: fmt.Sprintf("Thing %d", i)})
	}
	d.Parts = []prompts.BriefDayPart{{Part: 1, Made: "21:00", Upto: "20:58", Summary: strings.Repeat("概", 300)}}
	briefDayBound(d)
	if n := prompts.BriefDayRunes(d); n > briefDayRunes || n < briefDayRunes-200 {
		t.Fatalf("%d runes", n)
	}
	if d.Left == 0 || len(d.Timeline)+d.Left != 200 || d.Timeline[0].From != "e000" || d.Timeline[len(d.Timeline)-1].From != "e199" {
		t.Fatalf("kept %d, left %d: first %s, last %s", len(d.Timeline), d.Left, d.Timeline[0].From, d.Timeline[len(d.Timeline)-1].From)
	}
	if len(d.Actions) != 50 || d.ActionsLeft != 0 {
		t.Fatalf("actions went before the timeline: %d", len(d.Actions))
	}
}

// The day's push is done only once the phone, the Lark bot and every channel with pushes on took it (day-recap.md
// "The push"): one that failed leaves it due in the knowledge base, and telling it again (with those that took it,
// which the knowledge base keeps) sends it to the others, with the same id and keys; a refusal that sending again
// cannot help counts as taken: the hub's, a channel with nowhere to send it, a channel's own coded refusal.
func TestDayRecapPushTakenByAll(t *testing.T) {
	d := newDayFixture(t)
	setEvening(t, d.s, "06:30", true)
	larkFails, chanFails := 1, 1 // the answers run under d.f.mu
	var chanErr error
	d.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Pushes: true}}, nil
	})
	d.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		if larkFails > 0 {
			larkFails--
			return nil, errors.New("Lark is busy")
		}
		return map[string]any{"message_id": "om_day"}, nil
	})
	d.f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "examplechat", Source: channel.SourcePlugin, Kind: channel.KindIM,
			Installed: true, Enabled: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Push: true}}}}, nil
	})
	d.f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
		if chanFails > 0 {
			chanFails--
			return nil, errors.New("the channel's program restarts")
		}
		return map[string]any{}, chanErr
	})
	d.s.chans.mu.Lock()
	d.s.loadChannelsLocked(context.Background())
	d.s.channelPrefLocked("examplechat").Features = map[string]bool{channel.FeaturePushes: true}
	d.s.chans.mu.Unlock()
	d.f.mu.Lock()
	d.f.failNotify = 1
	d.f.mu.Unlock()

	// Written at the recap time, the recap takes the push, which nobody takes: it stays due.
	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	rc := d.recap(dayRec)
	e := d.engine()
	r, _ := e.Store.Recording(dayRec)
	posts, sends := d.f.appParams(AppFeishu, FeishuPost), d.f.appParams(AppChannels, ChannelsSend)
	if !r.RecapPush || r.RecapPushed || r.RecapPushTries != 1 || r.RecapPushTook != nil || len(d.f.pushed()) != 0 || len(posts) != 1 || len(sends) != 1 {
		t.Fatalf("a push nobody took: %d posts, %d sends, %+v", len(posts), len(sends), r)
	}
	took := r.RecapPushTook
	tell := func() error {
		t.Helper()
		r, _ := e.Store.Recording(dayRec)
		r.RecapPushTook = took // what the knowledge base keeps of the calls before (kb's tellDayRecap)
		now, err := e.DayRecapWritten(context.Background(), *r, *rc)
		took = append(took, now...)
		return err
	}
	// Told again: the phone and the channel take it, the Lark bot not yet.
	d.f.mu.Lock()
	larkFails = 1
	d.f.mu.Unlock()
	if err := tell(); err == nil {
		t.Fatal("done before the Lark bot took it")
	}
	if slices.Sort(took); !slices.Equal(took, []string{channel.App, "examplechat"}) {
		t.Fatalf("took it: %v", took)
	}
	// Once more: only the Lark bot gets it again, and takes it.
	if err := tell(); err != nil {
		t.Fatal(err)
	}
	id := "ntf_dayrecap_" + rc.Day
	pushes := d.f.pushed()
	posts, sends = d.f.appParams(AppFeishu, FeishuPost), d.f.appParams(AppChannels, ChannelsSend)
	if len(pushes) != 1 || pushes[0].ID != id || len(posts) != 3 || len(sends) != 2 || len(took) != 3 || took[2] != channel.Feishu {
		t.Fatalf("sent again to those that took it: %d pushes, %d posts, %d sends, took %v", len(pushes), len(posts), len(sends), took)
	}
	for _, p := range posts {
		if p["uuid"] != id+"_0" {
			t.Errorf("the Lark copy's key %v", p["uuid"])
		}
	}
	for _, p := range sends {
		if p["ref"] != id+":examplechat" || p["kind"] != channel.OutPush || p["text"] != "🔔 "+pushes[0].Title+"\n\n"+pushes[0].Body {
			t.Errorf("the channel's copy %v", p)
		}
	}
	// Told afresh, nothing taken yet, everything goes again: the hub refusing the phone's and the channel having
	// nowhere to send it, or refusing it with a code of its own, are taken as they are, not sent again and again.
	for _, refusal := range []error{&rpc.Error{Code: CodeCannotSend, Message: "no conversation yet"}, &rpc.Error{Code: "bot_blocked", Message: "the user blocked the bot"}} {
		took = nil
		d.f.mu.Lock()
		d.f.refuseNotify = &rpc.Error{Code: "no_device", Message: "another manager's"}
		chanErr = refusal
		d.f.mu.Unlock()
		if err := tell(); err != nil {
			t.Fatalf("a refusal is sent again: %v", err)
		}
		if slices.Sort(took); !slices.Equal(took, []string{channel.App, "examplechat", channel.Feishu}) {
			t.Errorf("refused, taken: %v", took)
		}
	}
	if posts := d.f.appParams(AppFeishu, FeishuPost); len(posts) != 5 || posts[4]["uuid"] != id+"_0" {
		t.Fatalf("the Lark bot's copy again: %v", posts)
	}
	// A send that got no answer (no code: a time-out, the program not running) is not a refusal: sent again.
	took = nil
	d.f.mu.Lock()
	chanErr = errors.New("channels.send timed out")
	d.f.mu.Unlock()
	if err := tell(); err == nil || slices.Contains(took, "examplechat") {
		t.Fatalf("a channel that did not answer took it: %v %v", err, took)
	}
}

// After a restart the push goes only to the destinations that did not take it: the knowledge base keeps those that
// did with the recording (the assistant keeps nothing), so the phone and a channel that took it get no second copy.
func TestDayRecapPushAfterRestart(t *testing.T) {
	d := newDayFixture(t)
	setEvening(t, d.s, "06:30", true)
	larkFails := 1
	d.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Pushes: true}}, nil
	})
	d.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		if larkFails > 0 {
			larkFails--
			return nil, errors.New("Lark is busy")
		}
		return map[string]any{"message_id": "om_day"}, nil
	})
	d.f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "examplechat", Source: channel.SourcePlugin, Kind: channel.KindIM,
			Installed: true, Enabled: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Push: true}}}}, nil
	})
	d.f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	d.s.chans.mu.Lock()
	d.s.loadChannelsLocked(context.Background())
	d.s.channelPrefLocked("examplechat").Features = map[string]bool{channel.FeaturePushes: true}
	d.s.chans.mu.Unlock()

	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	rc := d.recap(dayRec)
	e := d.engine()
	r, _ := e.Store.Recording(dayRec)
	if !r.RecapPush || !slices.Equal(r.RecapPushTook, []string{channel.App, "examplechat"}) || len(d.f.pushed()) != 1 {
		t.Fatalf("the phone and the channel took it, the Lark bot not: %+v", r)
	}
	// Restarted: told with the recording as stored.
	took, err := e.DayRecapWritten(context.Background(), *r, *rc)
	if err != nil || !slices.Equal(took, []string{channel.Feishu}) {
		t.Fatalf("after a restart: took %v, %v", took, err)
	}
	if p, s, posts := d.f.pushed(), d.f.appParams(AppChannels, ChannelsSend), d.f.appParams(AppFeishu, FeishuPost); len(p) != 1 || len(s) != 1 || len(posts) != 2 {
		t.Fatalf("sent again to those that took it: %d pushes, %d sends, %d posts", len(p), len(s), len(posts))
	}
}

// The phone's key among a push's destinations is a built-in channel's id, which no plugin channel has: a plugin
// channel named "phone" that took it does not stand for the phone, nor the phone for it.
func TestDayRecapPushPhoneKeyIsReserved(t *testing.T) {
	if !slices.Contains(channel.Reserved, dayPushPhone) {
		t.Fatalf("the phone's key %q can be a plugin channel's id", dayPushPhone)
	}
	d := newDayFixture(t)
	d.f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "phone", Name: "phone", Source: channel.SourcePlugin, Kind: channel.KindIM,
			Installed: true, Enabled: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Push: true}}}}, nil
	})
	d.f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	d.s.chans.mu.Lock()
	d.s.loadChannelsLocked(context.Background())
	d.s.channelPrefLocked("phone").Features = map[string]bool{channel.FeaturePushes: true}
	d.s.chans.mu.Unlock()
	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	d.do("POST", "/api/v1/recordings/"+dayRec+"/minutes", "", nil, http.StatusAccepted)
	rc := d.recap(dayRec)
	e := d.engine()
	r, _ := e.Store.Recording(dayRec)
	r.RecapPushTook = []string{"phone"} // the plugin channel took it, the phone not yet
	took, err := e.DayRecapWritten(context.Background(), *r, *rc)
	if err != nil || !slices.Contains(took, dayPushPhone) || slices.Contains(took, "phone") || len(d.f.pushed()) != 1 ||
		len(d.f.appParams(AppChannels, ChannelsSend)) != 0 {
		t.Fatalf("took %v, %v: %d pushes, %d sends", took, err, len(d.f.pushed()), len(d.f.appParams(AppChannels, ChannelsSend)))
	}
}

// A channel's copy of a push sent again has a ref of its own (the push's id and the channel's): a program with
// several channels and one cache of refs sends each its copy.
func TestDayRecapPushRefPerChannel(t *testing.T) {
	d := newDayFixture(t)
	views := []channel.View{}
	for _, id := range []string{"chata", "chatb"} {
		views = append(views, channel.View{ID: id, Name: id, Source: channel.SourcePlugin, Kind: channel.KindIM, Installed: true, Enabled: true,
			State: channel.StateConnected, Capabilities: channel.Capabilities{Push: true}})
	}
	d.f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) { return map[string]any{"channels": views}, nil })
	d.f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	d.s.chans.mu.Lock()
	d.s.loadChannelsLocked(context.Background())
	for _, v := range views {
		d.s.channelPrefLocked(v.ID).Features = map[string]bool{channel.FeaturePushes: true}
	}
	d.s.chans.mu.Unlock()
	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	d.do("POST", "/api/v1/recordings/"+dayRec+"/minutes", "", nil, http.StatusAccepted)
	rc := d.recap(dayRec)
	r, _ := d.engine().Store.Recording(dayRec)
	if _, err := d.engine().DayRecapWritten(context.Background(), *r, *rc); err != nil {
		t.Fatal(err)
	}
	refs := map[string]any{}
	for _, p := range d.f.appParams(AppChannels, ChannelsSend) {
		refs[p["channel"].(string)] = p["ref"]
	}
	id := "ntf_dayrecap_" + rc.Day
	if len(refs) != 2 || refs["chata"] != id+":chata" || refs["chatb"] != id+":chatb" {
		t.Fatalf("refs %v", refs)
	}
}

// The section stays within 4000 runes whatever a valid day holds: 500 gaps of as many reasons, a dozen parts. The
// reasons with the most time and the first and latest parts are listed, the others counted.
func TestEveningFactsDayBoundsGapsAndParts(t *testing.T) {
	d := newDayFixture(t)
	var gaps []map[string]any
	for i := range 500 {
		reason := fmt.Sprintf("reason-%03d-", i)
		gaps = append(gaps, map[string]any{"from": 4000 + 2*i, "to": 4001 + 2*i, "reason": reason + strings.Repeat("x", 32-len(reason))})
	}
	lines := dayLines(100, 101)
	lines["coverage"] = map[string]any{"to": 6000, "gaps": gaps}
	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", lines, http.StatusOK)
	d.do("POST", "/api/v1/recordings/"+dayRec+"/minutes", "", nil, http.StatusAccepted)
	d.recap(dayRec)
	for part := 2; part <= 12; part++ {
		d.putDay(fmt.Sprintf("77777777-7777-4777-8777-%012d", part), int64(100*part), d.owner, map[string]any{"part": part, "duration": 600})
	}
	f := d.s.briefFactsAt(context.Background(), time.Now(), "evening")
	if f.Day == nil {
		t.Fatal("no day")
	}
	if n := prompts.BriefDayRunes(f.Day); n > briefDayRunes {
		t.Errorf("%d runes", n)
	}
	day := f.Day
	if len(day.Gaps) != briefDayReasons || day.GapReasonsLeft != 494 || day.GapsLeft != 494 || day.GapMinutesLeft != 494 ||
		!strings.HasPrefix(day.Gaps[0].Reason, "reason-000-") {
		t.Errorf("gaps %d listed, %d reasons left (%d, %d min): %+v", len(day.Gaps), day.GapReasonsLeft, day.GapsLeft, day.GapMinutesLeft, day.Gaps[0])
	}
	if len(day.Parts) != briefDayParts || day.PartsLeft != 7 || day.Parts[0].Part != 1 || day.Parts[len(day.Parts)-1].Part != 12 {
		t.Errorf("parts %+v, %d left", day.Parts, day.PartsLeft)
	}
	text := f.modelText()
	for _, want := range []string{"；另有 494 种原因共 494 段 494 分钟", "  - 中间另有 7 个部分没列出\n", "Standup"} {
		if !strings.Contains(text, want) {
			t.Errorf("facts lack %q:\n%s", want, text)
		}
	}
}

// At its longest, every part of the section is bounded: 30 parts with long summaries, 500 gap reasons each with its
// longest stretches, a long timeline and many to-dos still make at most 4000 runes.
func TestEveningFactsDayBoundsEverything(t *testing.T) {
	d := &prompts.BriefDay{Date: "2026-11-11", From: "11-10 23:58", To: "11-11 23:59", Transcribed: "11-11 23:30"}
	for i := range 500 {
		reason := fmt.Sprintf("reason-%03d-", i)
		g := prompts.BriefDayGap{Reason: reason + strings.Repeat("x", 32-len(reason)), Count: 5 + i%7, Minutes: 10 + i%50, More: true}
		for range briefDayRanges {
			g.Ranges = append(g.Ranges, "11-10 23:59–11-11 00:30")
		}
		d.Gaps = append(d.Gaps, g)
	}
	for i := range 30 {
		d.Parts = append(d.Parts, prompts.BriefDayPart{Part: i + 1, Made: "11-12 00:10", Upto: "11-11 23:59", Final: true, Partial: true,
			State: "failed", Summary: strings.Repeat("概", briefDaySummary)})
	}
	for i := range 200 {
		d.Timeline = append(d.Timeline, prompts.BriefDayEntry{From: fmt.Sprintf("e%03d", i), To: "11-11 23:59", Title: strings.Repeat("长", briefDayTitle)})
	}
	for range 50 {
		d.Actions = append(d.Actions, prompts.BriefDayAction{Who: strings.Repeat("人", 40), What: strings.Repeat("事", briefDayWhat)})
	}
	briefDayBound(d)
	if n := prompts.BriefDayRunes(d); n > briefDayRunes {
		t.Fatalf("%d runes", n)
	}
	if len(d.Parts) != briefDayParts || d.PartsLeft != 25 || d.Parts[0].Part != 1 || d.Parts[len(d.Parts)-1].Part != 30 {
		t.Errorf("parts %d, %d left", len(d.Parts), d.PartsLeft)
	}
	// The reasons with the most time (59 minutes: every 50th from the 49th), in the day's order.
	if len(d.Gaps) != briefDayReasons || d.GapReasonsLeft != 494 || !strings.HasPrefix(d.Gaps[0].Reason, "reason-049-") ||
		!strings.HasPrefix(d.Gaps[5].Reason, "reason-299-") {
		t.Errorf("gaps %d listed, %d left: %s … %s", len(d.Gaps), d.GapReasonsLeft, d.Gaps[0].Reason, d.Gaps[len(d.Gaps)-1].Reason)
	}
}

// Past meetings through the kb tools: kb_list dates the calendar items, kb_get on one names the recordings made at it,
// kb_search kind=recordings finds the recording by the calendar's title, and kb_get on the recording says after the
// minutes where each action item stands now: a suggestion only, or its to-do open (and when due), done, cancelled or
// deleted.
func TestMinutesPastMeetingTools(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	f := newFakeHub()
	s := kbServiceOn(t, f, newKBStore(), t.TempDir(), func(string, ...any) {}) // the push's goroutines log late
	setSettings(t, s, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	loc, _ := time.LoadLocation("Asia/Shanghai")
	e := kbEng(t, s)
	ctx := context.Background()
	start := time.Now().In(loc).Truncate(time.Hour).AddDate(0, 0, -3)
	due := start.AddDate(0, 0, 2).Format("2006-01-02")
	model := minutesModel{reply: `{"title":"Design review","decisions":[{"text":"Keep the cap for this release.","line":2}],"actions":[` +
		`{"who":"Bo","what":"Add the regression test","by":"Friday","due_date":"` + due + `","line":3},{"who":"Ada","what":"Fix the export","line":3},` +
		`{"what":"Mail the notes","line":3},{"what":"Book the room","line":3},{"what":"Draft the plan","line":3}],` +
		`"open":[{"text":"Per entry or per recording?","line":3}]}`}
	e.Model, e.ModelFor = model, func(string) kb.Model { return model }
	if err := e.Store.PutItem(&kb.Item{Kind: kb.KindMeeting, ID: "m1", Title: "Design review", At: start, URL: "https://meet.example.com/m1"}); err != nil {
		t.Fatal(err)
	}
	const id = "44444444-4444-4444-8444-444444444444"
	up := kb.RecordingUpload{Revision: 1, StartedAt: start, Duration: 30, Device: "phone", Complete: true, Kind: kb.RecordingMeeting,
		Segments: []kb.RecordingSegment{{Start: 0, End: 9, Text: "Ada: the export drops large entries"},
			{Start: 10, End: 19, Text: "we keep the cap"}, {Start: 20, End: 29, Text: "Bo adds the regression test by Friday"}}}
	if st, err := e.PutRecording(id, up, start.Add(time.Minute)); err != nil || st.Status != kb.RecordingStored {
		t.Fatalf("put: %v %v", st, err)
	}
	if n := e.WriteMinutes(ctx, 2); n != 1 {
		t.Fatalf("wrote %d", n)
	}

	clock := start.Format("2006-01-02 15:04")
	if out, err := runTool(s, "c1", "kb_list", map[string]any{"kind": "meetings"}); err != nil ||
		out != "- m1 · Design review [- 相关0]  "+clock+" https://meet.example.com/m1\n" {
		t.Fatalf("kb_list meetings: %q %v", out, err)
	}
	if out, err := runTool(s, "c1", "kb_get", map[string]any{"kind": "meetings", "id": "m1"}); err != nil ||
		!strings.HasSuffix(out, "\nRecorded at it (kb_get kind=recordings for the minutes and the transcript):\n- "+id+" · Design review "+clock+" 0min phone minutes\n") {
		t.Fatalf("kb_get meeting:\n%s (%v)", out, err)
	}
	out, err := runTool(s, "c1", "kb_search", map[string]any{"query": "design REVIEW", "kind": "recordings"})
	if err != nil || !strings.HasPrefix(out, "recordings/"+id+"/") {
		t.Fatalf("kb_search kind=recordings: %q %v", out, err)
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(l, "recordings/") {
			t.Fatalf("kb_search kind=recordings found %q", l)
		}
	}
	if _, err := runTool(s, "c1", "kb_search", map[string]any{"query": "design review", "kind": "minutes"}); err == nil ||
		!strings.Contains(err.Error(), "one of projects, chats") {
		t.Fatalf("kb_search, an unknown kind: %v", err)
	}

	// Confirmed: a1 stays open (renamed), a2 is done, a3 deleted, a4 cancelled; a5 is never confirmed.
	// Through the recording module's route, as the apps confirm them.
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/recordings/"+id+"/minutes/todos", strings.NewReader(`{"version":1,"actions":["a1","a2","a3","a4"]}`)))
	var confirmed struct{ Todos []Todo }
	if err := json.Unmarshal(w.Body.Bytes(), &confirmed); w.Code != http.StatusOK || err != nil || len(confirmed.Todos) != 4 {
		t.Fatalf("confirm: %d %s", w.Code, w.Body)
	}
	todos := confirmed.Todos
	renamed, done := "Bo: Add the regression test for exports", true
	if _, err := s.editTodo(ctx, todos[0].ID, db.TodoEdit{Title: &renamed}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.editTodo(ctx, todos[1].ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteTodo(ctx, todos[2].ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.todos.mu.Lock()
	s.todos.todos[todos[3].ID].CancelledAt, s.todos.todos[todos[3].ID].DoneAt = &now, &now
	s.todos.mu.Unlock()
	dueDay, _ := time.Parse("2006-01-02", due)
	out, err = runTool(s, "c1", "kb_get", map[string]any{"kind": "recordings", "id": id})
	want := "\nAction items as they stand (one becomes a to-do only when the user confirms it):\n" +
		"- a1 Bo: Add the regression test (by Friday, " + due + ") → to-do " + todos[0].ID + ` "` + renamed + `" open · due ` + dueDay.Format("2006-01-02 Mon") + " (the whole day) (" +
		"- a2 Ada: Fix the export → to-do " + todos[1].ID + " done " + todoNow(s, todos[1].ID).DoneAt.In(loc).Format("2006-01-02") + "\n" +
		"- a3 Mail the notes → its to-do " + todos[2].ID + " was deleted\n" +
		"- a4 Book the room → to-do " + todos[3].ID + " cancelled\n" +
		"- a5 Draft the plan → suggested only, not a to-do\n"
	if err != nil || !strings.Contains(out, "minutes.md:\n# Design review") {
		t.Fatalf("kb_get recording:\n%s (%v)", out, err)
	}
	actions, rest, _ := strings.Cut(out, "\nAction items")
	if !strings.Contains(actions, "minutes.md:") || !strings.Contains(rest, "transcript.md:") || strings.Contains(actions, "transcript.md:") {
		t.Fatalf("not minutes, action items, transcript in that order:\n%s", out)
	}
	open, closed, _ := strings.Cut(want, " (the whole day) (")
	if !strings.Contains(out, open+" (the whole day) (") || !strings.Contains(out, ")\n"+closed) {
		t.Fatalf("kb_get recording, the action items:\n%s\nwant\n%s", out, want)
	}
}

// kb_get on a meeting imported from a connected app lists the recording whose calendar match is an overlapping event
// but whose imported meeting it is (#8872).
func TestKBGetImportedMeetingListsItsRecording(t *testing.T) {
	f := newFakeHub()
	s := kbServiceOn(t, f, newKBStore(), t.TempDir(), func(string, ...any) {})
	setSettings(t, s, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	loc, _ := time.LoadLocation("Asia/Shanghai")
	e := kbEng(t, s)
	e.Model, e.ModelFor = nil, nil // the round matches, writes no minutes
	start := time.Now().In(loc).Truncate(time.Hour).AddDate(0, 0, -3)
	for _, it := range []*kb.Item{
		{Kind: kb.KindEvent, ID: "e1", Title: "Design review", At: start.Add(-5 * time.Minute)},
		{Kind: kb.KindMeeting, ID: "m1", Title: "Design review (video)", At: start.Add(time.Minute), URL: "https://meet.example.com/m1"},
	} {
		if err := e.Store.PutItem(it); err != nil {
			t.Fatal(err)
		}
	}
	const id = "55555555-5555-4555-8555-555555555555"
	up := kb.RecordingUpload{Revision: 1, StartedAt: start, Duration: 120, Device: "phone", Complete: true, Kind: kb.RecordingMeeting,
		Segments: []kb.RecordingSegment{{Start: 0, End: 9, Text: "we keep the cap"}}}
	if st, err := e.PutRecording(id, up, start.Add(3*time.Minute)); err != nil || st.Status != kb.RecordingStored {
		t.Fatalf("put: %v %v", st, err)
	}
	e.WriteMinutes(context.Background(), 2)
	if r, _ := e.Store.Recording(id); r.Meeting == nil || r.Meeting.ID != "e1" || r.ImportedMeeting == nil || r.ImportedMeeting.ID != "m1" {
		t.Fatalf("matched %+v, imported %+v", r.Meeting, r.ImportedMeeting)
	}
	for _, kind := range []string{"meetings", "events"} {
		id1 := map[string]string{"meetings": "m1", "events": "e1"}[kind]
		out, err := runTool(s, "c1", "kb_get", map[string]any{"kind": kind, "id": id1})
		if err != nil || !strings.Contains(out, "\nRecorded at it (kb_get kind=recordings for the minutes and the transcript):\n- "+id+" · ") {
			t.Fatalf("kb_get %s:\n%s (%v)", kind, out, err)
		}
	}
}

// In the fleet the session's row and its computer are the manager's own; a turn that just ended keeps a batch back
// until its push is decided, a meeting turn's end does not. These are the core's answers to the recording module's
// attendee check (recordingHost); the module's verdicts on them are its TestAttendeeVerdict.
func TestAttendeeCheckFleet(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	s, ctx := ft.s, context.Background()
	h := recordingHost{s}
	work := proto.Session{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p1", UpdatedAt: time.Now()}
	done := work
	done.Status, done.LastText = proto.StatusDone, "Done."
	ft.sessions("mac", done)
	if ours, online := h.AttendeeMachine(ctx, "mac"); !ours || !online {
		t.Fatalf("the fleet's computer: ours %v online %v", ours, online)
	}
	if row, found, err := h.AttendeeRow(ctx, "mac", "s1"); err != nil || !found || row.Status != proto.StatusDone {
		t.Fatalf("a done Agent: %+v %v %v", row, found, err)
	}
	if h.TurnSettling("mac", "s1", time.Now()) {
		t.Fatal("a done Agent no turn was followed for is settling")
	}
	if ours, _ := h.AttendeeMachine(ctx, "elsewhere"); ours {
		t.Fatal("a computer this manager does not carry is its")
	}
	if _, found, _ := h.AttendeeRow(ctx, "mac", "gone"); found {
		t.Fatal("a session that is not listed was found")
	}
	ft.sessions("mac", work)
	if row, _, _ := h.AttendeeRow(ctx, "mac", "s1"); row.Status != proto.StatusWorking {
		t.Fatalf("working: %+v", row)
	}
	ft.sessions("mac", done)
	if !h.TurnSettling("mac", "s1", time.Now()) {
		t.Fatal("a turn that just ended is not settling")
	}
	if h.TurnSettling("mac", "s1", time.Now().Add(time.Minute)) {
		t.Fatal("its push decided, still settling")
	}
	if h.IsWorkMachine("mac") {
		t.Fatal("a personal computer reads as work")
	}
	ft.d.mu.Lock()
	ft.d.machines["mac"].trust = "work"
	ft.d.mu.Unlock()
	if !h.IsWorkMachine("mac") {
		t.Fatal("a work computer does not read as work")
	}
}

// Before the upgrade only the caller (403) and the knowledge base (503) refuse the socket.
func TestAudioSocketRefusedBeforeTheUpgrade(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, nil)
	url := "ws" + strings.TrimPrefix(h.sockets.URL, "http") + "/api/v1/recordings/" + meetingRec + "/audio"
	_, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: http.Header{RoleHeader: {"answer"}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("answer role: %v %v", err, resp)
	}
	noKB := newTestService(t, newFakeHub())
	srv := httptest.NewServer(http.HandlerFunc(noKB.ServeSocket))
	defer srv.Close()
	_, resp, err = websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/recordings/"+meetingRec+"/audio", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no knowledge base: %v %v", err, resp)
	}
}

// A Just me turn that stops with background work running has not ended: the turn that work's report wakes keeps the
// mark, also when the report comes long after the stop, so the woken turn's end (the held work's one completion push,
// carrying the private answer) is never pushed. Once that turn is over its tracker entry goes too, and the owner's
// next ordinary turn drops the mark (#3862, docs/assistant/meeting-minutes.md "Just me"). The delayed checks run by
// hand here: their timers are an hour.
func TestJustMeMarkHoldsThroughBackgroundWork(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	ft := newFleetTestNoStore(t)
	ft.f.mu.Lock()
	ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 3_600_000, LongTurnMS: 60_000}
	ft.f.mu.Unlock()
	var clock struct {
		sync.RWMutex
		at time.Time
	}
	clock.at = time.Now()
	now := func() time.Time {
		clock.RLock()
		defer clock.RUnlock()
		return clock.at
	}
	advance := func(d time.Duration) {
		clock.Lock()
		clock.at = clock.at.Add(d)
		clock.Unlock()
	}
	// A turn's end is read asynchronously: advancing the fixture must not race with justMe.clock.
	recClock(ft.s, now)
	const key = "mac/s1"
	check := func() { // the delayed check of the latest end
		t.Helper()
		book := ft.s.turnsBook()
		book.mu.Lock()
		tr := book.turns[key]
		book.mu.Unlock()
		if tr == nil {
			t.Fatal("no turn tracked")
		}
		ft.s.notifyFinishedNow("mac", "s1", tr.gen, ft.s.notifyTimings())
	}
	work := proto.Session{SessionID: "s1", Title: "Main", Cwd: "/code/app", Agent: "claude", Assistant: true, Status: proto.StatusWorking,
		PaneID: "p1", UpdatedAt: now()}
	ft.s.justMeSent("mac", "s1")
	ft.sessions("mac", work)
	stopped := work
	stopped.Status, stopped.LastText = proto.StatusDone, "Looking it up in the background."
	stopped.Background = []proto.BackgroundTask{{ID: "b1", Kind: "agent"}}
	ft.sessions("mac", stopped)
	check() // held: its work runs
	advance(10 * time.Minute)
	woken := work
	woken.WokeByTask = true
	ft.sessions("mac", woken)
	if !ft.s.justMeNow("mac", "s1") {
		t.Fatal("the turn the work's report woke lost the mark")
	}
	finished := woken
	finished.Status, finished.LastText = proto.StatusDone, "PRIVATE ANSWER"
	ft.sessions("mac", finished)
	check()
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 0); len(got) != 0 {
		t.Fatalf("the Just me work's end was pushed: %+v", got)
	}
	if !ft.s.turnsBook().startOf(key).IsZero() {
		t.Fatal("the Just me turn stays tracked: the next turn would take its start and its hold")
	}
	advance(time.Minute)
	ft.sessions("mac", work) // the owner's next message
	if ft.s.justMeNow("mac", "s1") {
		t.Fatal("the next ordinary turn is still marked")
	}
}

// persona_settings reads and changes assistant.meeting_attendance_default: the same value asks nothing, another role
// asks on the phone with the roles named in the user's language, anything else is refused.
func TestMeetingAttendanceDefaultSettingsTool(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	const key = "assistant.meeting_attendance_default"
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	get := func() string { out, _ := tool(`{"action":"get","key":"` + key + `"}`); return out }
	set := func(value string) (string, bool) {
		return tool(`{"action":"set","key":"` + key + `","value":` + value + `}`)
	}
	if out := get(); out != "off" {
		t.Fatalf("default: %s", out)
	}
	if out, isErr := set(`"off"`); isErr || !strings.Contains(out, "unchanged") || len(s.pendingQuestions()) != 0 {
		t.Fatalf("set to what it is: %s", out)
	}
	if out, isErr := set(`"chair"`); !isErr || !strings.Contains(out, "off, listener or participant") {
		t.Fatalf("an unknown role: %s", out)
	}
	role := func(r string) string { return i18n.T(i18n.Default, "assistant-settings_tool.meeting_role_"+r) }
	card := i18n.T(i18n.Default, "assistant-settings_tool.change_meeting_attendance_default", i18n.Args{"before": role("off"), "after": role("participant")})
	go func() {
		q, ok := awaitCard(s)
		if !ok || q.Summary != card {
			t.Errorf("card %+v, want %q", q, card)
			return
		}
		allow := true
		raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
			t.Error(err)
		}
	}()
	if out, _ := set(`"participant"`); !strings.HasPrefix(out, key+" is participant") || get() != "participant" {
		t.Fatalf("approved: %s", out)
	}
}

// meeting_photo_note: the room's note on a photo of its own meeting that went into it, within 1000 characters;
// refused for an unknown photo, one not forwarded, one withdrawn, another meeting's, and by a room started before the
// tool existed.
func TestMeetingPhotoNoteTool(t *testing.T) {
	r := newRoomFixture(t)
	sid := r.roomUp(meetingRec, "listener")
	other := r.roomUp(meetingOther, "listener")
	r.addPhoto(meetingRec, photoBody(t, photoOne, 5, nil))
	for _, id := range []string{photoOne, photoTwo, photoThree} {
		r.addPhoto(meetingOther, photoBody(t, id, 5, nil))
	}
	r.look(meetingRec)
	for range 3 {
		r.look(meetingOther)
	}
	r.addPhoto(meetingRec, photoBody(t, photoTwo, 6, nil)) // queued, not in the room yet
	d := newFleetDevs()
	d.add("server", "Server")
	r.s.devs = d
	call := func(session, args string) proto.ToolResult {
		t.Helper()
		out, ok, err := r.s.NodeRequest(context.Background(), "server", "mcp.call",
			json.RawMessage(`{"name":"meeting_photo_note","args":`+args+`,"session_id":"`+session+`","assistant":true,"guest":true}`))
		if !ok || err != nil {
			t.Fatalf("call: %v %v", ok, err)
		}
		return out.(proto.ToolResult)
	}
	text := func(res proto.ToolResult) string {
		var b strings.Builder
		for _, c := range res.Content {
			b.WriteString(c.Text)
		}
		return b.String()
	}
	rev := r.engine().PhotosRev(meetingRec)
	if res := call(sid, `{"photo":1,"text":"  The Q3 budget on the board is 40k.  "}`); res.IsError {
		t.Fatalf("note: %s", text(res))
	}
	if ps := r.photosOf(meetingRec); ps[0]["note"] != "The Q3 budget on the board is 40k." || r.engine().PhotosRev(meetingRec) != rev+1 {
		t.Fatalf("noted %v", ps[0])
	}
	for name, c := range map[string]struct{ args, want string }{
		"too long":      {`{"photo":1,"text":"` + strings.Repeat("字", kb.MaxPhotoNote+1) + `"}`, "1000"},
		"empty":         {`{"photo":1,"text":"  "}`, "empty"},
		"no text":       {`{"photo":1}`, "required"},
		"not a number":  {`{"photo":"one","text":"x"}`, "number"},
		"unknown":       {`{"photo":9,"text":"x"}`, "no photo 9"},
		"not forwarded": {`{"photo":2,"text":"x"}`, "no photo 2"},
		"another's":     {`{"photo":3,"text":"x"}`, "no photo 3"}, // the other meeting's photo 3 went into its own room
	} {
		if res := call(sid, c.args); !res.IsError || !strings.Contains(text(res), c.want) {
			t.Errorf("%s: %v %s", name, res.IsError, text(res))
		}
	}
	if ps := r.photosOf(meetingOther); ps[2]["note"] != nil {
		t.Fatalf("another meeting's photo noted %v", ps[2])
	}
	r.do("DELETE", photosPath(meetingRec)+"/1", "", nil, http.StatusOK)
	if res := call(sid, `{"photo":1,"text":"again"}`); !res.IsError || !strings.Contains(text(res), "withdrawn") {
		t.Fatalf("withdrawn: %s", text(res))
	}

	// A room started before the tool existed keeps the tools it started with: not listed, refused.
	if err := r.s.guestPut(context.Background(), other, &guestConv{MachineID: "server", Tools: []string{"meeting_lines", "meeting_earlier"},
		Kind: guestKindMeeting, Recording: meetingOther}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range r.s.sessionMCPTools(context.Background(), "server", other, true, true) {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, []string{"meeting_lines", "meeting_earlier"}) {
		t.Fatalf("an older room's tools %v", names)
	}
	if res := call(other, `{"photo":1,"text":"x"}`); !res.IsError || !strings.Contains(text(res), "unknown tool") {
		t.Fatalf("an older room noted: %s", text(res))
	}
}

// kb_get has none of a photo deleted before the minutes were made again (#6563); the follow-up's seed has none either
// (the recording module's TestMeetingPhotoDeletedBeforeTheFollowup).
func TestMeetingPhotoDeletedBeforeKBGet(t *testing.T) {
	m := newMeetingFixture(t)
	m.upload(meetingRec, 1, map[string]any{"title": "Design review", "complete": true, "ended_at": time.Now().UTC().Format(time.RFC3339),
		"segments": []map[string]any{{"start": 0, "end": 9, "text": "We keep the cap."}}})
	m.addPhoto(meetingRec, photoBody(t, photoOne, 5, map[string]any{"text": "PHOTO-ONLY-WORDS"}))
	m.minutesFor(meetingRec, `{"title":"Design review","headline":"The cap stays","conclusions":[{"text":"We **keep** the cap","line":1}],`+
		`"topics":[{"title":"Board","points":[{"text":"PHOTO-ONLY-WORDS","photo":1}]},`+
		`{"title":"Export","points":[{"text":"Entries over the cap are **dropped**","line":1}],"decisions":[{"text":"We keep the cap","line":1}]}],`+
		`"actions":[{"what":"Send the release note","line":1}]}`)
	m.do("DELETE", photosPath(meetingRec)+"/1", "", nil, http.StatusOK)
	if out, _ := runTool(m.s, "c1", "kb_get", map[string]any{"kind": "recordings", "id": meetingRec}); strings.Contains(out, "PHOTO-ONLY") {
		t.Fatalf("kb_get:\n%s", out)
	}
}

// kb_get prints a recording's photos (photos.md) after the minutes' action items and before the transcript; a deleted
// photo is not there.
func TestMeetingPhotosInKBGet(t *testing.T) {
	m := newMeetingFixture(t)
	m.upload(meetingRec, 1, map[string]any{"title": "Design review", "complete": true, "ended_at": time.Now().UTC().Format(time.RFC3339),
		"segments": []map[string]any{{"start": 0, "end": 9, "text": "Bo adds the test."}}})
	m.addPhoto(meetingRec, photoBody(t, photoOne, 5, map[string]any{"caption": "Whiteboard", "text": "Q3 budget"}))
	m.addPhoto(meetingRec, photoBody(t, photoTwo, 6, map[string]any{"text": "DELETED-PHOTO-TEXT"}))
	m.do("DELETE", photosPath(meetingRec)+"/2", "", nil, http.StatusOK)
	m.minutesFor(meetingRec, `{"title":"Design review","actions":[{"who":"Bo","what":"Add the test","line":1}]}`)
	out, err := runTool(m.s, "c1", "kb_get", map[string]any{"kind": "recordings", "id": meetingRec})
	if err != nil {
		t.Fatal(err)
	}
	actions, photos, transcript := strings.Index(out, "Action items as they stand"), strings.Index(out, "\nphotos.md:\n[P1] "), strings.Index(out, "\ntranscript.md:")
	if actions < 0 || photos < actions || transcript < photos || !strings.Contains(out, "photo: Whiteboard · Q3 budget\n") || strings.Contains(out, "DELETED-PHOTO") {
		t.Fatalf("kb_get:\n%s", out)
	}
	m.do("DELETE", photosPath(meetingRec)+"/1", "", nil, http.StatusOK)
	if out, _ := runTool(m.s, "c1", "kb_get", map[string]any{"kind": "recordings", "id": meetingRec}); strings.Contains(out, "photos.md") {
		t.Fatalf("kb_get without photos:\n%s", out)
	}
}

// Issue #6048: A meeting's room conversation runs in <state dir>/guest-meeting/<uuid>.
// The manager must count guest-meeting/<id> rows as the assistant's guest conversations:
// 1. isAssistantSession / m.isAssistant returns true for guest-meeting/<id> rows and OriginMeeting, false for normal agents.
// 2. Agent auto-naming never runs on it (no auto-naming).
// 3. Decorated inventory sets Assistant = true so the room never appears in apps' Agents lists or voice list_agents.
// 4. The attending rule in meeting_attendee applies and disallows it from attending another meeting.
func TestMeetingRoomCountedAsAssistant(t *testing.T) {
	roomCwd := filepath.Join("/home/me", ".persona", "guest-meeting", "550e8400-e29b-41d4-a716-446655440000")
	normalCwd := filepath.Join("/home/me", "code", "app")

	roomSess := proto.Session{
		SessionID: "room-sess-1",
		Cwd:       roomCwd,
		Status:    proto.StatusWorking,
		State:     proto.StateWorking,
		Origin:    OriginMeeting,
	}
	normalAgent := proto.Session{
		SessionID: "agent-sess-1",
		Cwd:       normalCwd,
		Status:    proto.StatusWorking,
		State:     proto.StateWorking,
	}

	m := fleetMachine{ID: "server", Home: "/home/me", manager: ""}

	// Path 1: isAssistantSession recognition.
	if !m.isAssistant(roomSess) {
		t.Errorf("m.isAssistant(roomSess) = false for meeting room cwd %s, want true", roomCwd)
	}
	if m.isAssistant(normalAgent) {
		t.Errorf("m.isAssistant(normalAgent) = true for %s, want false", normalCwd)
	}

	// Path 2: Auto-naming must never run on meeting room sessions.
	f := healthHub()
	f.devices = []Device{{ID: "server", Name: "服务器", Kind: "local", Home: "/home/me", Online: true, Reachable: true}}
	f.sessions["server"] = []proto.Session{roomSess, normalAgent}
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t)}))
	ctx := context.Background()
	s.originSet(ctx, "server", roomSess.SessionID, OriginMeeting, "rec-1", "")

	// Even if Assistant was not set in incoming raw session, tryAgentName checks m.isAssistant and rejects naming.
	rawRoomSess := roomSess
	rawRoomSess.Assistant = false
	if s.tryAgentName(ctx, "server", rawRoomSess, time.Now()) {
		t.Error("tryAgentName returned true for meeting room session, want false")
	}

	// Path 3: decorateInventory sets Assistant = true so the room never appears in apps' Agents lists or voice list_agents.
	decorated := s.decorateInventory("server", []proto.Session{rawRoomSess, normalAgent}, true)
	if len(decorated) != 2 {
		t.Fatalf("decorateInventory returned %d sessions, want 2", len(decorated))
	}
	if !decorated[0].Assistant {
		t.Errorf("decorated[0].Assistant = false for meeting room row, want true")
	}
	if decorated[1].Assistant {
		t.Errorf("decorated[1].Assistant = true for normal agent row, want false")
	}

	// Path 4: the core tells the recording module the room's origin, by which it never attends another meeting (the
	// module's TestAttendeeVerdict and its room tests).
	if origins, ok := (recordingHost{s}).ConversationOrigins(); !ok || origins[roomSess.SessionID].Origin != OriginMeeting {
		t.Errorf("the room's origin %+v (%v), want %q", origins[roomSess.SessionID], ok, OriginMeeting)
	}
}

// The room's tools: its meeting tools only, bound to its recording; meeting_lines reads its lines, meeting_earlier
// only earlier meetings marked not private; any other tool is refused.
func TestMeetingRoomTools(t *testing.T) {
	r := newRoomFixture(t)
	r.engine().EarlierMeetingsInAnswers = true
	r.earlierMeeting("Exports over four megabytes are dropped")
	sid := r.roomUp(meetingRec, "listener")
	r.do("POST", livePath(meetingRec), "", `{"from":0,"segments":[{"start":61,"end":64,"text":"Ada: keep the cap."},{"start":3700,"end":3705,"text":"Bo: later."}]}`, http.StatusOK)
	rec, _ := r.engine().Store.Recording(meetingRec)
	loc := zoneApart(t, rec.StartedAt) // the user's, neither UTC nor this machine's: the batches' zone
	setSettings(t, r.s, func(v *Settings) { v.Timezone = loc.String() })
	tools := r.s.sessionMCPTools(context.Background(), "server", sid, true, true)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, []string{"meeting_lines", "meeting_earlier", "meeting_photo_note", "meeting_topics_set", "meeting_highlights_set"}) {
		t.Fatalf("tools %v", names)
	}
	d := newFleetDevs() // the nodes' requests are the fleet's
	d.add("server", "Server")
	r.s.devs = d
	call := func(name, args string) proto.ToolResult {
		out, ok, err := r.s.NodeRequest(context.Background(), "server", "mcp.call", json.RawMessage(`{"name":"`+name+`","args":`+args+`,"session_id":"`+sid+`","assistant":true,"guest":true}`))
		if !ok || err != nil {
			t.Fatalf("%s: %v %v", name, ok, err)
		}
		return out.(proto.ToolResult)
	}
	text := func(res proto.ToolResult) string {
		var b strings.Builder
		for _, c := range res.Content {
			b.WriteString(c.Text)
		}
		return b.String()
	}
	// Each line's offset, then the clock time a batch gives it in the user's zone (what a highlight's said_at copies).
	clock := func(sec float64) string { return kb.LiveClock(*rec, sec, loc) }
	if got := text(call("meeting_lines", `{"to":100}`)); got != "[1:01 · "+clock(61)+"] Ada: keep the cap." {
		t.Fatalf("lines %q", got)
	}
	if got := text(call("meeting_lines", `{"from":3000}`)); got != "[1:01:40 · "+clock(3700)+"] Bo: later." {
		t.Fatalf("lines past an hour %q", got)
	}
	if got := text(call("meeting_earlier", `{"query":"export cap"}`)); !strings.Contains(got, "Keep the cap for this release") {
		t.Fatalf("earlier %q", got)
	}
	r.do("PUT", "/api/v1/recordings/"+earlier+"/private", "", map[string]any{"private": true}, http.StatusOK)
	if got := text(call("meeting_earlier", `{"query":"export cap"}`)); strings.Contains(got, "Keep the cap") {
		t.Fatalf("a private meeting was material: %q", got)
	}
	if res := call("kb_search", `{"query":"x"}`); !res.IsError {
		t.Fatalf("another tool ran: %v", res)
	}
}

// A main chat an older hub stored on the Agent SDK does not keep its room from starting: the room takes the main chat's
// launcher and model, and neither names a runtime.
func TestMeetingRoomRunsOnTheCLI(t *testing.T) {
	r := newRoomFixture(t)
	ctx := context.Background()
	if err := r.s.store().PutSetting(ctx, modelRecordKey("assistant"), json.RawMessage(`{"revision":1,"binding":{"runtime":"agentsdk"}}`)); err != nil {
		t.Fatal(err)
	}
	main := map[string]any{}
	if err := r.s.applyModel(ctx, "assistant", main); err != nil || main["runtime"] != nil {
		t.Fatalf("the main chat %v %v", main, err)
	}
	r.roomUp(meetingRec, "participant")
	sends := r.sends()
	if len(sends) != 1 || sends[0]["runtime"] != nil || sends[0]["launcher"] != main["launcher"] {
		t.Fatalf("room start %v, main chat %v", sends, main)
	}
}

// A room's row in the lists the apps and the extensions read (GET /api/v1/sessions) is the assistant's (its
// guest-meeting/<recording id> directory counts as the assistant's since #6077) and says its origin meeting, its
// recording and its title, so that the voice extension refuses an ordinary call bound to it (meeting_room) and the apps
// leave its voice button out. Another row the book has an origin for is not an assistant's conversation and says none.
func TestMeetingRoomRowSaysItsOrigin(t *testing.T) {
	ft := newFleetTest(t)
	machine, _ := ft.s.fleetMachine("mac")
	sid := meetingSessionID(meetingRec, partRoom)
	room := proto.Session{SessionID: sid, Agent: "claude", Status: proto.StatusIdle, Title: "The meeting is starting.",
		Cwd: path.Join(path.Dir(machine.assistantCwd()), "guest-meeting", meetingRec)}
	agent := proto.Session{SessionID: "agent", Agent: "claude", Cwd: "/home/u/src", PaneID: "p1", Status: proto.StatusIdle, Title: "Fix the build"}
	ft.d.setSessions("mac", []proto.Session{room, agent})
	ctx := context.Background()
	started := time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)
	ft.s.originSet(ctx, "mac", sid, OriginMeeting, meetingRec, "")
	ft.s.titleFixed("mac", sid, started, titleMeeting, "Design review")
	ft.s.originSet(ctx, "mac", "agent", OriginTask, "task1", "")
	rows := map[string]proto.Session{}
	for _, sess := range ft.s.decoratedUnarchived("mac") {
		rows[sess.SessionID] = sess
	}
	if r := rows[sid]; !r.Assistant || r.Origin != OriginMeeting || r.OriginRef != meetingRec || !strings.Contains(r.Title, "Design review") {
		t.Fatalf("the room's row: assistant %v, origin %q %q, title %q", r.Assistant, r.Origin, r.OriginRef, r.Title)
	}
	if r := rows["agent"]; r.Origin != "" || r.OriginRef != "" || r.Title != "Fix the build" {
		t.Fatalf("an agent's row: origin %q %q, title %q", r.Origin, r.OriginRef, r.Title)
	}
	if r, ok := ft.s.decoratedSession("mac", sid); !ok || r.Origin != OriginMeeting || r.OriginRef != meetingRec {
		t.Fatalf("the room's row alone: %v %q %q", ok, r.Origin, r.OriginRef)
	}
}

// The recording module present or absent (docs/architecture/core-and-modules.md "Recording"): left out of the build
// (persona_without_recording, persona_core_only) or switched off (the modules row), the core serves none of its
// routes (404 as an unknown route), announces none of its capabilities, offers no meeting room tool and has no
// audio socket; in the build and on, all of them are there.
func TestRecordingFollowsItsModule(t *testing.T) {
	for _, c := range []struct {
		name string
		off  []string
		on   bool
	}{{"build", nil, recordinghost.InBuild()}, {"switched off", []string{recordinghost.ModuleID}, false}} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeHub()
			s := withSettings(t, f, New(Options{Hub: f, Logf: testLogf(t), ModulesOff: c.off}))
			if s.recordingOn() != c.on {
				t.Fatalf("recording layer %v, want %v", s.recordingOn(), c.on)
			}
			for _, r := range []struct{ method, path string }{{"GET", "/api/v1/recordings"}, {"PUT", "/api/v1/recordings/" + meetingRec},
				{"GET", "/api/v1/recordings/" + meetingRec + "/live"}, {"POST", "/api/v1/recordings/" + meetingRec + "/minutes"}} {
				w := httptest.NewRecorder()
				s.ServeHTTP(w, httptest.NewRequest(r.method, r.path, strings.NewReader("{}")))
				if (w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "404 page not found")) == c.on {
					t.Errorf("%s %s: %d %s (module on: %v)", r.method, r.path, w.Code, w.Body, c.on)
				}
			}
			w := httptest.NewRecorder()
			s.ServeSocket(w, httptest.NewRequest("GET", "/api/v1/recordings/"+meetingRec+"/audio", nil))
			if (w.Code == http.StatusNotFound) == c.on {
				t.Errorf("the audio socket: %d (module on: %v)", w.Code, c.on)
			}
			caps := withRecording(slices.Clone(recordingCapabilities), s)
			if (len(caps) == len(recordingCapabilities)) != c.on || !c.on && len(caps) != 0 {
				t.Errorf("capabilities %v (module on: %v)", caps, c.on)
			}
			if (len(s.meetingRoomTools()) > 0) != c.on || (len(s.meetingRoomToolNames()) > 0) != c.on {
				t.Errorf("room tools %v (module on: %v)", s.meetingRoomToolNames(), c.on)
			}
		})
	}
}
