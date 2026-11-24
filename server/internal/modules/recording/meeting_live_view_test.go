package recording

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// meeting_topics_set: the room replaces its meeting's topics whole; GET …/live has them ([] before the first) with
// topics_at and their revision, which goes up only when they change; a list over a limit is refused naming it, and
// changes nothing; meeting_live says topics changed; the next batch shows the room the topics so far, and the
// follow-up's seed lists them. A room started before the tool existed sets none and is shown none.
func TestMeetingTopics(t *testing.T) {
	r := newRoomFixture(t)
	sid := r.roomUp(meetingRec, "participant")
	r.olderRoom(meetingRec) // the rules and the topics in every batch (a lean room's: meeting_room_lean_test.go)
	view := r.do("GET", livePath(meetingRec), "", nil, http.StatusOK)
	if topics, ok := view["topics"].([]any); !ok || len(topics) != 0 || view["topics_at"] != nil || view["revs"].(map[string]any)["topics"] != float64(0) {
		t.Fatalf("before the first: %v", view)
	}

	if res, text := r.topicsCall(sid, `{"topics":[{"title":" Export\ncap ","state":"done","at":5,"note":"Kept at 4 MB."},`+
		`{"title":"Pricing","state":"CURRENT","at":61},{"title":"Hiring","state":"open"}]}`); res.IsError || text != "Topics saved." {
		t.Fatalf("set: %s", text)
	}
	view = r.do("GET", livePath(meetingRec)+"?parts=topics", "", nil, http.StatusOK)
	raw, _ := json.Marshal(view["topics"]) // a map's keys in order
	if string(raw) != `[{"at":5,"note":"Kept at 4 MB.","state":"done","title":"Export cap"},{"at":61,"state":"current","title":"Pricing"},{"state":"open","title":"Hiring"}]` {
		t.Fatalf("topics %s", raw)
	}
	at, err := time.Parse(time.RFC3339, view["topics_at"].(string))
	if err != nil || time.Since(at) > time.Minute || view["revs"].(map[string]any)["topics"] != float64(1) || len(view) != 3 {
		t.Fatalf("topics_at and revs %v", view)
	}
	if res, text := r.topicsCall(sid, `{"topics":[{"title":"Export cap","state":"done","at":5,"note":"Kept at 4 MB."},`+
		`{"title":"Pricing","state":"current","at":61},{"title":"Hiring","state":"open"}]}`); res.IsError || !strings.Contains(text, "nothing changed") {
		t.Fatalf("the same again: %s", text)
	}
	if l, _, _ := r.engine().LiveRead(meetingRec); l.TopicsRev != 1 {
		t.Fatalf("the same list moved the revision: %d", l.TopicsRev)
	}

	many := make([]map[string]any, kb.MaxTopics+1)
	for i := range many {
		many[i] = map[string]any{"title": fmt.Sprintf("t%d", i), "state": "open"}
	}
	manyArgs, _ := json.Marshal(map[string]any{"topics": many})
	for name, c := range map[string]struct{ args, want string }{
		"too many":      {string(manyArgs), "over the 20"},
		"long title":    {`{"topics":[{"title":"` + strings.Repeat("字", kb.MaxTopicTitle+1) + `","state":"open"}]}`, "over the 80"},
		"long note":     {`{"topics":[{"title":"x","state":"open","note":"` + strings.Repeat("a", kb.MaxTopicNote+1) + `"}]}`, "over the 200"},
		"two current":   {`{"topics":[{"title":"a","state":"current"},{"title":"b","state":"current"}]}`, "at most one"},
		"bad state":     {`{"topics":[{"title":"a","state":"later"}]}`, "not done, current or open"},
		"no title":      {`{"topics":[{"title":"  ","state":"open"}]}`, "topic 1 has no title"},
		"negative at":   {`{"topics":[{"title":"a","state":"open","at":-1}]}`, "0 or more seconds"},
		"no topics":     {`{}`, "topics is required"},
		"not a list":    {`{"topics":"Pricing"}`, "topics is required"},
		"a text number": {`{"topics":[{"title":"a","state":"open","at":"5"}]}`, "topics is required"},
	} {
		if res, text := r.topicsCall(sid, c.args); !res.IsError || !strings.Contains(text, c.want) {
			t.Errorf("%s: %v %s", name, res.IsError, text)
		}
	}
	if l, _, _ := r.engine().LiveRead(meetingRec); len(l.Topics) != 3 || l.TopicsRev != 1 {
		t.Fatalf("a refused call changed the topics: %+v", l.Topics)
	}
	waitFor(t, "a meeting_live event for the topics", func() bool {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		for _, ev := range r.f.liveEvents {
			changed, _ := ev["changed"].([]any)
			if slices.Contains(changed, any("topics")) && ev["revs"].(map[string]any)["topics"] == float64(1) {
				return true
			}
		}
		return false
	})

	// The batches show the room the topics so far.
	r.do("POST", livePath(meetingRec), "", `{"from":0,"segments":[{"start":70,"end":74,"text":"Bo: on pricing, we keep the tiers."}]}`, http.StatusOK)
	before := len(r.sends())
	r.look(meetingRec)
	sends := r.sends()
	if len(sends) != before+1 {
		t.Fatalf("no batch: %v", sends[before:])
	}
	if text, _ := sends[len(sends)-1]["text"].(string); !strings.Contains(text, "meeting_topics_set") ||
		!strings.Contains(text, "- current: Pricing (from 1:01 into the meeting)") || !strings.Contains(text, "- done: Export cap (from 0:05 into the meeting): Kept at 4 MB.") {
		t.Fatalf("batch %s", text)
	}

	// The follow-up's seed lists them.
	rec, _ := r.engine().Store.Recording(meetingRec)
	m, _, _ := r.engine().MeetingRead(meetingRec)
	if seed := r.s.followupSeed(context.Background(), r.engine(), *rec, m, "Draft the email."); !strings.Contains(seed, "- open: Hiring") {
		t.Fatalf("seed %s", seed)
	}

	// A room started before the tool existed: not listed, refused, and its batches show no topics.
	if err := r.s.guestPut(context.Background(), sid, &guestConv{MachineID: "server", Tools: []string{"meeting_lines", "meeting_earlier", "meeting_photo_note"},
		Kind: guestKindMeeting, Recording: meetingRec}); err != nil {
		t.Fatal(err)
	}
	if res, text := r.topicsCall(sid, `{"topics":[]}`); !res.IsError || !strings.Contains(text, "unknown tool") {
		t.Fatalf("an older room set topics: %s", text)
	}
	if _, err := r.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error {
		l.FedAt = l.FedAt.Add(-2 * feedBatchEvery)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.do("POST", livePath(meetingRec), "", `{"from":1,"segments":[{"start":80,"end":84,"text":"Ada: next, hiring."}]}`, http.StatusOK)
	before = len(r.sends())
	r.look(meetingRec)
	sends = r.sends()
	if len(sends) != before+1 {
		t.Fatalf("no batch: %v", sends[before:])
	}
	if text, _ := sends[len(sends)-1]["text"].(string); strings.Contains(text, "topics") || !strings.Contains(text, "Write nothing in reply.") {
		t.Fatalf("an older room's batch %s", text)
	}
}

// meeting_highlights_set: the room replaces its meeting's highlights whole, each {text, said_at}, said_at the [HH:MM]
// of a batch line, which the hub turns into the start of that minute's first line and the end of its last (placeholders
// never count). GET …/live has them ([] before the first), the earliest first, with highlights_at and their revision,
// which goes up only when the resolved list changes; a list over a limit is refused naming it, and changes nothing;
// meeting_live says highlights changed; the POST 200 and detail.meta never carry them; the next batch shows them after
// the topics as "- [HH:MM] text", and the follow-up's seed lists them. A room started before the tool sets none and
// is shown none.
func TestMeetingHighlights(t *testing.T) {
	r := newRoomFixture(t)
	start := time.Now().Add(-time.Hour).Truncate(time.Minute).UTC() // a whole minute, so each minute's lines are known
	// The user's zone is neither UTC nor this machine's: said_at is resolved in the zone the batches are written in.
	loc := zoneApart(t, start)
	setSettings(t, r.s, func(v *Settings) { v.Timezone = loc.String() })
	sid := r.roomUpWith(meetingRec, "participant", map[string]any{"started_at": start.Format(time.RFC3339)})
	r.olderRoom(meetingRec) // the rules and the lists in every batch (a lean room's: meeting_room_lean_test.go)
	view := r.do("GET", livePath(meetingRec), "", nil, http.StatusOK)
	if raw, _ := json.Marshal(view["highlights"]); string(raw) != `[]` || view["highlights_at"] != nil || view["revs"].(map[string]any)["highlights"] != float64(0) {
		t.Fatalf("before the first: %v", view)
	}
	out := r.do("POST", livePath(meetingRec), "", `{"from":0,"segments":[{"start":5,"end":9,"text":"Ada: the export cap stays at 4 MB."},`+
		`{"start":2,"end":70,"text":""},{"start":40,"end":58,"text":"Bo: so we keep it for this release."},`+
		`{"start":61,"end":64,"text":"Ada: who owns the hiring plan?"},{"start":125,"end":128,"text":"Bo: nobody yet."},{"start":190,"end":192,"text":""}]}`, http.StatusOK)
	if _, has := out["highlights"]; has || out["highlights_at"] != nil {
		t.Fatalf("POST …/live: %v", out)
	}
	rec, _ := r.engine().Store.Recording(meetingRec)
	clock := func(sec float64) string { return kb.LiveClock(*rec, sec, loc) }
	m0, m1, m3 := clock(5), clock(61), clock(190)
	if clock(59) != m0 || clock(60) != m1 {
		t.Fatalf("the recording does not start on a minute: %s %s %s", m0, clock(59), m1)
	}
	// The first line's minute in UTC or in this machine's zone is no minute of the meeting in the user's.
	for _, other := range []*time.Location{time.UTC, time.Local} {
		said := kb.LiveClock(*rec, 5, other)
		if res, text := r.highlightsCall(sid, `{"highlights":[{"text":"The cap stays.","said_at":"`+said+`"}]}`); !res.IsError ||
			!strings.Contains(text, "highlight 1's said_at "+said+" is not the minute of any line of the meeting so far") {
			t.Fatalf("a said_at in %s: %s", other, text)
		}
	}

	set := `{"highlights":[{"text":"Open: who owns\nthe hiring plan?","said_at":"[` + m1 + `]"},` +
		`{"text":" The export cap stays for this release. ","said_at":" ` + m0 + ` "}]}`
	if res, text := r.highlightsCall(sid, set); res.IsError || text != "Highlights saved." {
		t.Fatalf("set: %s", text)
	}
	view = r.do("GET", livePath(meetingRec)+"?parts=highlights", "", nil, http.StatusOK)
	raw, _ := json.Marshal(view["highlights"]) // a map's keys in order
	if string(raw) != `[{"at":5,"end":58,"text":"The export cap stays for this release."},{"at":61,"end":64,"text":"Open: who owns the hiring plan?"}]` {
		t.Fatalf("highlights %s", raw)
	}
	at, err := time.Parse(time.RFC3339, view["highlights_at"].(string))
	if revs, _ := json.Marshal(view["revs"]); err != nil || time.Since(at) > time.Minute || string(revs) != `{"highlights":1}` || len(view) != 3 {
		t.Fatalf("highlights_at and revs %v", view)
	}
	view = r.do("GET", livePath(meetingRec), "", nil, http.StatusOK)
	if hs, _ := view["highlights"].([]any); len(hs) != 2 || view["highlights_at"] == nil || view["revs"].(map[string]any)["highlights"] != float64(1) {
		t.Fatalf("GET …/live %v", view)
	}
	// The same list again, its said_at written either way, is the same resolved list: nothing changes.
	same := `{"highlights":[{"text":"The export cap stays for this release.","said_at":"` + m0 + `"},` +
		`{"text":"Open: who owns the hiring plan?","said_at":"` + m1 + `"}]}`
	if res, text := r.highlightsCall(sid, same); res.IsError || text != "The highlights are as they were: nothing changed." {
		t.Fatalf("the same again: %s", text)
	}
	if l, _, _ := r.engine().LiveRead(meetingRec); l.HighlightsRev != 1 {
		t.Fatalf("the same list moved the revision: %d", l.HighlightsRev)
	}

	five := make([]map[string]any, kb.MaxHighlights+1)
	for i := range five {
		five[i] = map[string]any{"text": fmt.Sprintf("point %d", i), "said_at": m0}
	}
	fiveArgs, _ := json.Marshal(map[string]any{"highlights": five})
	one := func(item string) string { return `{"highlights":[` + item + `]}` }
	for name, c := range map[string]struct{ args, want string }{
		"no highlights":   {`{}`, "highlights is required"},
		"null":            {`{"highlights":null}`, "highlights is required"},
		"not a list":      {`{"highlights":"The cap stays."}`, "highlights is required"},
		"five":            {string(fiveArgs), "5 highlights, over the 4"},
		"not an object":   {one(`"The cap stays."`), "highlight 1 is not an object with a string text and a string said_at"},
		"a null item":     {one(`null`), "highlight 1 is not an object"},
		"a number text":   {one(`{"text":5,"said_at":"` + m0 + `"}`), "highlight 1 is not an object"},
		"no said_at":      {one(`{"text":"The cap stays."}`), "highlight 1 is not an object"},
		"no text":         {one(`{"text":" \n ","said_at":"` + m0 + `"}`), "highlight 1 has no text"},
		"long text":       {one(`{"text":"` + strings.Repeat("字", kb.MaxHighlightText+1) + `","said_at":"` + m0 + `"}`), "over the 80"},
		"an offset":       {one(`{"text":"The cap stays.","said_at":"1:02:03"}`), "not the H:MM or HH:MM of a batch line"},
		"no hour 24":      {one(`{"text":"The cap stays.","said_at":"24:05"}`), "not the H:MM or HH:MM"},
		"a minute unsaid": {one(`{"text":"The cap stays.","said_at":"` + m3 + `"}`), "is not the minute of any line of the meeting so far"},
	} {
		if res, text := r.highlightsCall(sid, c.args); !res.IsError || !strings.Contains(text, c.want) || !strings.Contains(text, "Nothing was saved") && !strings.Contains(text, "is required") {
			t.Errorf("%s: %v %s", name, res.IsError, text)
		}
	}
	if l, _, _ := r.engine().LiveRead(meetingRec); len(l.Highlights) != 2 || l.HighlightsRev != 1 {
		t.Fatalf("a refused call changed the highlights: %+v", l.Highlights)
	}
	waitFor(t, "a meeting_live event for the highlights", func() bool {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		for _, ev := range r.f.liveEvents {
			changed, _ := ev["changed"].([]any)
			if slices.Contains(changed, any("highlights")) && ev["revs"].(map[string]any)["highlights"] == float64(1) {
				return true
			}
		}
		return false
	})

	// A kept highlight whose minute gained a line gets the later end: the same list is a change then.
	r.do("POST", livePath(meetingRec), "", `{"from":6,"segments":[{"start":55,"end":59.5,"text":"Ada: agreed."}]}`, http.StatusOK)
	if res, text := r.highlightsCall(sid, same); res.IsError || text != "Highlights saved." {
		t.Fatalf("a grown minute: %s", text)
	}
	if l, _, _ := r.engine().LiveRead(meetingRec); l.HighlightsRev != 2 || l.Highlights[0].End != 59.5 {
		t.Fatalf("a grown minute %+v", l)
	}
	d, err := r.engine().Get(kb.KindRecording, meetingRec, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Meta), "highlights") || strings.Contains(string(d.Meta), "export cap stays") {
		t.Fatalf("detail.meta %s", d.Meta)
	}

	// The batch shows the room the highlights so far after the topics, each "- [HH:MM] text", never a line that
	// starts with "[" (the apps read those as the meeting's lines).
	before := len(r.sends())
	r.look(meetingRec)
	sends := r.sends()
	if len(sends) != before+1 {
		t.Fatalf("no batch: %v", sends[before:])
	}
	text, _ := sends[len(sends)-1]["text"].(string)
	want := "Highlights so far:\n- [" + m0 + "] The export cap stays for this release.\n- [" + m1 + "] Open: who owns the hiring plan?\n</persona-context>"
	if !strings.Contains(text, "meeting_highlights_set") || !strings.Contains(text, want) ||
		strings.Index(text, "Topics so far:") > strings.Index(text, "Highlights so far:") {
		t.Fatalf("batch %s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[") && !strings.Contains(line, "Ada: ") && !strings.Contains(line, "Bo: ") {
			t.Errorf("a line of the batch that is not the meeting's starts with [: %q", line)
		}
	}

	// The follow-up's seed lists them, at their place in the transcript.
	m, _, _ := r.engine().MeetingRead(meetingRec)
	if seed := r.s.followupSeed(context.Background(), r.engine(), *rec, m, "Draft the email."); !strings.Contains(seed,
		"- [0:05] The export cap stays for this release.\n- [1:01] Open: who owns the hiring plan?") {
		t.Fatalf("seed %s", seed)
	}
	// Only [] clears them.
	if res, text := r.highlightsCall(sid, `{"highlights":[]}`); res.IsError || text != "Highlights saved." {
		t.Fatalf("clear: %s", text)
	}
	if view = r.do("GET", livePath(meetingRec)+"?parts=highlights", "", nil, http.StatusOK); len(view["highlights"].([]any)) != 0 ||
		view["revs"].(map[string]any)["highlights"] != float64(3) {
		t.Fatalf("cleared %v", view)
	}

	// A room started before the tool existed: refused, and its batches show no highlights (the topics they still do).
	if err := r.s.guestPut(context.Background(), sid, &guestConv{MachineID: "server", Tools: []string{"meeting_lines", "meeting_earlier",
		"meeting_photo_note", "meeting_topics_set"}, Kind: guestKindMeeting, Recording: meetingRec}); err != nil {
		t.Fatal(err)
	}
	if res, text := r.highlightsCall(sid, same); !res.IsError || !strings.Contains(text, "unknown tool") {
		t.Fatalf("an older room set highlights: %s", text)
	}
	if _, err := r.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error {
		l.FedAt = l.FedAt.Add(-2 * feedBatchEvery)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.do("POST", livePath(meetingRec), "", `{"from":7,"segments":[{"start":200,"end":204,"text":"Ada: next, hiring."}]}`, http.StatusOK)
	before = len(r.sends())
	r.look(meetingRec)
	sends = r.sends()
	if len(sends) != before+1 {
		t.Fatalf("no batch: %v", sends[before:])
	}
	if text, _ := sends[len(sends)-1]["text"].(string); strings.Contains(text, "ighlights") || !strings.Contains(text, "Topics so far:") {
		t.Fatalf("an older room's batch %s", text)
	}
}

// topicsCall is the room session's meeting_topics_set with args, and its text.
func (r *roomFixture) topicsCall(session, args string) (proto.ToolResult, string) {
	r.t.Helper()
	return r.roomCall(session, toolTopicsSet, args)
}

// highlightsCall is the room session's meeting_highlights_set with args, and its text.
func (r *roomFixture) highlightsCall(session, args string) (proto.ToolResult, string) {
	r.t.Helper()
	return r.roomCall(session, toolHighlightsSet, args)
}

// roomCall is the room session's call of tool with args, as its MCP server makes it, and its text.
func (r *roomFixture) roomCall(session, tool, args string) (proto.ToolResult, string) {
	r.t.Helper()
	// The core routes a room's mcp.call to its meeting (the assistant's TestMeetingRoomTools); here it goes straight in.
	rec, _, _ := r.s.RoomOf(context.Background(), "server", session)
	var out proto.ToolResult
	if tools, _, _ := r.s.GuestOf(context.Background(), "server", session); !slices.Contains(tools, tool) {
		out = proto.TextResult("unknown tool "+tool, true) // the core's guest filter (guest.go)
	} else {
		out = r.s.roomToolCall(context.Background(), rec, tool, json.RawMessage(args))
	}
	res := out
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return res, b.String()
}

// GET …/live?from=: the live log's positions from n (a placeholder's text "" included), at most 500 with more when
// there are more; none without from; a from that is not a whole number of 0 or more is 400, after 404. The lines'
// revision is the live log's length; ?parts=lines gives the lines only beside from. POST …/live's 200 has neither
// lines nor topics.
func TestMeetingLiveLines(t *testing.T) {
	r := newRoomFixture(t)
	r.do("GET", livePath(meetingRec)+"?from=x", "", nil, http.StatusNotFound)
	r.roomUp(meetingRec, "listener")
	segs := make([]map[string]any, 0, 200)
	for i := range 200 {
		text := fmt.Sprintf("line %d", i)
		if i == 1 {
			text = ""
		}
		segs = append(segs, map[string]any{"start": i, "end": i + 1, "text": text})
	}
	for from := 0; from < 600; from += 200 {
		for i := range segs {
			segs[i]["text"] = fmt.Sprintf("line %d", from+i)
			if from+i == 1 {
				segs[i]["text"] = ""
			}
		}
		out := r.do("POST", livePath(meetingRec), "", map[string]any{"from": from, "segments": segs}, http.StatusOK)
		if _, has := out["lines"]; has || out["topics"] != nil {
			t.Fatalf("POST …/live: %v", out)
		}
	}

	view := r.do("GET", livePath(meetingRec), "", nil, http.StatusOK)
	if _, has := view["lines"]; has || view["revs"].(map[string]any)["lines"] != float64(600) {
		t.Fatalf("without from: %v", view)
	}
	page := r.do("GET", livePath(meetingRec)+"?from=0", "", nil, http.StatusOK)
	lines, _ := page["lines"].([]any)
	if len(lines) != maxLiveLines || page["more"] != true {
		t.Fatalf("first page: %d lines, more %v", len(lines), page["more"])
	}
	first, second := lines[0].(map[string]any), lines[1].(map[string]any)
	if first["n"] != float64(0) || first["text"] != "line 0" || first["start"] != float64(0) || first["end"] != float64(1) ||
		second["n"] != float64(1) || second["text"] != "" {
		t.Fatalf("lines %v %v", first, second)
	}
	last := lines[len(lines)-1].(map[string]any)["n"].(float64)
	rest := r.do("GET", livePath(meetingRec)+fmt.Sprintf("?from=%d", int(last)+1), "", nil, http.StatusOK)
	if lines, _ := rest["lines"].([]any); len(lines) != 100 || rest["more"] != nil || lines[0].(map[string]any)["n"] != float64(500) {
		t.Fatalf("second page: %v", rest["more"])
	}
	if end := r.do("GET", livePath(meetingRec)+"?from=900", "", nil, http.StatusOK); len(end["lines"].([]any)) != 0 || end["more"] != nil {
		t.Fatalf("past the end: %v", end)
	}
	for _, bad := range []string{"-1", "1.5", "abc", "", "+3", "99999999999999999999"} {
		wantCode(t, r.do("GET", livePath(meetingRec)+"?from="+bad, "", nil, http.StatusBadRequest), "bad_request")
	}

	only := r.do("GET", livePath(meetingRec)+"?parts=lines", "", nil, http.StatusOK)
	if raw, _ := json.Marshal(only); string(raw) != `{"revs":{"lines":600}}` {
		t.Fatalf("parts=lines without from: %s", raw)
	}
	only = r.do("GET", livePath(meetingRec)+"?parts=lines&from=599", "", nil, http.StatusOK)
	if raw, _ := json.Marshal(only); string(raw) != `{"lines":[{"end":200,"n":599,"start":199,"text":"line 599"}],"revs":{"lines":600}}` {
		t.Fatalf("parts=lines from 599: %s", raw)
	}
	waitFor(t, "a meeting_live event for the lines", func() bool {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		for _, ev := range r.f.liveEvents {
			changed, _ := ev["changed"].([]any)
			if slices.Contains(changed, any("lines")) && ev["revs"].(map[string]any)["lines"] == float64(600) {
				return true
			}
		}
		return false
	})
	d, err := r.engine().Get(kb.KindRecording, meetingRec, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Meta), "line 1") || strings.Contains(string(d.Meta), "topics") || strings.Contains(string(d.Meta), "lines") {
		t.Fatalf("detail.meta %s", d.Meta)
	}
}
