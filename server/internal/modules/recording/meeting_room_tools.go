package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The meeting tools a room conversation has (docs/assistant/meeting-minutes.md "Meeting tools"), served by the hub and
// each bound to the room's own recording: they take no recording id. The room is listed and may call these only;
// every other tool call is refused (guestRefuses).

// meetingRoomToolNames are the meeting tools a room starts with: phase 1's, meeting_photo_note (phase 3),
// meeting_topics_set (phase 2) and meeting_highlights_set (phase 2b). A room started before a tool was added keeps the
// list it started with (the guest book's, the node's allowed tools).
func meetingRoomToolNames() []string {
	return []string{"meeting_lines", "meeting_earlier", "meeting_photo_note", toolTopicsSet, toolHighlightsSet}
}

// toolTopicsSet is the room's tool that replaces its meeting's topics (docs/assistant/meeting-minutes.md "Phase 2:
// live view"); a room's batches show the topics only to a room that has it (roomFeed).
const toolTopicsSet = "meeting_topics_set"

// toolHighlightsSet is the room's tool that replaces its meeting's highlights (docs/assistant/meeting-minutes.md
// "Phase 2b: highlights"); a room's batches show the highlights only to a room that has it (roomFeed).
const toolHighlightsSet = "meeting_highlights_set"

// meetingRoomTools are a room's tools as its MCP server lists them.
func meetingRoomTools() []proto.Tool {
	var out []proto.Tool
	for _, name := range meetingRoomToolNames() {
		t := prompts.Tool(name)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// meetingLinesRunes is how much meeting_lines gives at most, the newest kept.
const meetingLinesRunes = 12000

// roomToolCall runs a room's call of tool name for recording.
func (s *Recordings) roomToolCall(ctx context.Context, recording, name string, args json.RawMessage) proto.ToolResult {
	e, err := s.kbGet(ctx)
	if err != nil {
		return proto.TextResult("the meeting's material is not available now", true)
	}
	switch name {
	case "meeting_lines":
		var a struct {
			From *float64 `json:"from"`
			To   *float64 `json:"to"`
		}
		if len(args) > 0 && json.Unmarshal(args, &a) != nil {
			return proto.TextResult("from and to are seconds from the meeting's start", true)
		}
		l, rec, err := e.LiveRead(recording)
		if err != nil {
			return proto.TextResult("this meeting's lines are not available", true)
		}
		return proto.TextResult(meetingLinesText(rec, l.Segments, a.From, a.To, s.location(ctx)), false)
	case "meeting_earlier":
		var a struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(args, &a) != nil {
			return proto.TextResult("query is required", true)
		}
		earlier, err := e.EarlierMaterial(recording, a.Query)
		if err != nil {
			return proto.TextResult("earlier meetings are not available", true)
		}
		var srcs []kb.AskSource
		for _, em := range earlier {
			srcs = append(srcs, kb.AskSource{Kind: kb.KindRecording, ID: em.ID, Name: em.Name, When: em.When})
			for _, p := range em.Projects {
				srcs = append(srcs, kb.AskSource{Kind: kb.KindProject, ID: p.Slug, Name: p.Name})
			}
		}
		s.room.noteSources(recording, srcs)
		in := s.answerInput(ctx, kb.MeetingMaterial{EarlierAvailable: true, Earlier: earlier}, kb.LiveAsk{QuestionSource: askTyped}, nil)
		out, err := json.Marshal(map[string]any{"earlier_meetings": in.Earlier})
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(string(out), false)
	case "meeting_photo_note":
		return photoNoteTool(e, recording, args) // meeting_photos.go
	case toolTopicsSet:
		return topicsSetTool(e, recording, args, s.attendNow())
	case toolHighlightsSet:
		return highlightsSetTool(e, recording, args, s.location(ctx), s.attendNow())
	}
	return proto.TextResult("unknown tool "+name, true)
}

// meetingLinesText is recording r's live lines said between from and to (seconds; nil: open) as "[m:ss · HH:MM]
// text", the offset from the start and then the clock time a batch gives the line in loc (kb.LiveClock, what a
// highlight's said_at copies), at most meetingLinesRunes, the newest kept.
func meetingLinesText(r kb.Recording, segs []kb.LiveSegment, from, to *float64, loc *time.Location) string {
	var lines []string
	runes := 0
	for i := len(segs) - 1; i >= 0; i-- {
		sg := segs[i]
		if sg.Text == "" || from != nil && sg.End < *from || to != nil && sg.Start > *to {
			continue
		}
		line := fmt.Sprintf("[%s · %s] %s", meetingClock(sg.Start), kb.LiveClock(r, sg.Start, loc), oneLine(sg.Text))
		n := utf8.RuneCountInString(line) + 1
		if runes+n > meetingLinesRunes {
			break
		}
		runes += n
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "(nothing said in that time)"
	}
	slices.Reverse(lines)
	return strings.Join(lines, "\n")
}

// roomState is what the manager holds in memory for the rooms: the asks waiting for a room to come up (woken when it
// is ready, unavailable or ended), one ask at a time per room (a photo's send takes the slot too), the earlier meetings
// meeting_earlier gave during the ask being answered (its sources), and the failed sends of photo messages.
type roomState struct {
	mu         sync.Mutex
	waiters    map[string][]chan struct{} // by recording: asks waiting for its room
	turn       map[string]chan struct{}   // by recording: held while an ask runs in its room
	sources    map[string][]kb.AskSource  // by recording: what meeting_earlier gave since the running ask was sent
	photoTries map[string]int             // by client_msg_id: failed sends of a photo's message (meeting_photos.go)
}

// wait is a channel closed when recording's room changes (wake).
func (r *roomState) wait(recording string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.waiters == nil {
		r.waiters = map[string][]chan struct{}{}
	}
	ch := make(chan struct{})
	r.waiters[recording] = append(r.waiters[recording], ch)
	return ch
}

// wake: recording's room changed (ready, unavailable, ended).
func (r *roomState) wake(recording string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.waiters[recording] {
		close(ch)
	}
	delete(r.waiters, recording)
}

// take holds recording's room for one ask: false while another one runs there (done is closed when it ends).
func (r *roomState) take(recording string) (release func(), done <-chan struct{}, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turn == nil {
		r.turn = map[string]chan struct{}{}
	}
	if ch := r.turn[recording]; ch != nil {
		return nil, ch, false
	}
	ch := make(chan struct{})
	r.turn[recording] = ch
	if r.sources == nil {
		r.sources = map[string][]kb.AskSource{}
	}
	delete(r.sources, recording)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.turn[recording] == ch {
			delete(r.turn, recording)
			close(ch)
		}
	}, nil, true
}

// noteSources keeps what meeting_earlier gave in recording's room while an ask runs there.
func (r *roomState) noteSources(recording string, srcs []kb.AskSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turn[recording] == nil {
		return
	}
	if r.sources == nil {
		r.sources = map[string][]kb.AskSource{}
	}
	r.sources[recording] = append(r.sources[recording], srcs...)
}

// takeSources are the sources of the ask that ran in recording's room: each once, at most 6.
func (r *roomState) takeSources(recording string) []kb.AskSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []kb.AskSource{}
	for _, src := range r.sources[recording] {
		if len(out) >= 6 {
			break
		}
		if !slices.ContainsFunc(out, func(x kb.AskSource) bool { return x.Kind == src.Kind && x.ID == src.ID }) {
			out = append(out, src)
		}
	}
	delete(r.sources, recording)
	return out
}

// topicsSetTool is the room's meeting_topics_set {topics} for recording, its own meeting: the whole list replaces the
// topics at now. A list that breaks a limit is refused saying which (kb.CleanTopics), and nothing changes.
func topicsSetTool(e *kb.Engine, recording string, args json.RawMessage, now time.Time) proto.ToolResult {
	var a struct {
		Topics *[]kb.LiveTopic `json:"topics"`
	}
	if json.Unmarshal(args, &a) != nil || a.Topics == nil {
		return proto.TextResult("topics is required, and is the whole list of the meeting's topics: each {title, state, at?, note?}, "+
			"title and note text, state done, current or open, at a number of seconds from the meeting's start", true)
	}
	changed, err := e.TopicsSet(recording, *a.Topics, now)
	switch {
	case err == nil && changed:
		return proto.TextResult("Topics saved.", false)
	case err == nil:
		return proto.TextResult("The topics are as they were: nothing changed.", false)
	case errors.Is(err, kb.ErrBadTopics):
		return proto.TextResult(err.Error()+". Nothing was saved: send the whole list again within the limits.", true)
	case errors.Is(err, kb.ErrReadOnly):
		return proto.TextResult("the meeting's topics can't be changed now", true)
	}
	return proto.TextResult("this meeting's topics are not available now", true)
}

// highlightsSetTool is the room's meeting_highlights_set {highlights} for recording, its own meeting: the whole list of
// {text, said_at} replaces the highlights at now, each said_at resolved in loc, the zone the batches are written in
// (kb.HighlightsSet). A list that is not one, or breaks a limit, is refused saying which, and nothing changes.
func highlightsSetTool(e *kb.Engine, recording string, args json.RawMessage, loc *time.Location, now time.Time) proto.ToolResult {
	var a struct {
		Highlights *[]json.RawMessage `json:"highlights"` // nil when absent or null
	}
	if json.Unmarshal(args, &a) != nil || a.Highlights == nil {
		return proto.TextResult("highlights is required, and is the whole list of the meeting's highlights: each {text, said_at}, "+
			"text one short sentence, said_at the HH:MM of the batch line it was made in", true)
	}
	said := make([]kb.HighlightSaid, len(*a.Highlights))
	for i, raw := range *a.Highlights {
		var h struct {
			Text   *string `json:"text"`
			SaidAt *string `json:"said_at"`
		}
		if json.Unmarshal(raw, &h) != nil || h.Text == nil || h.SaidAt == nil { // null, not an object, not strings
			said[i].Malformed = true // refused in its place, after the count (kb.CleanHighlights)
			continue
		}
		said[i] = kb.HighlightSaid{Text: *h.Text, SaidAt: *h.SaidAt}
	}
	changed, err := e.HighlightsSet(recording, said, loc, now)
	switch {
	case err == nil && changed:
		return proto.TextResult("Highlights saved.", false)
	case err == nil:
		return proto.TextResult("The highlights are as they were: nothing changed.", false)
	case errors.Is(err, kb.ErrBadHighlights):
		return proto.TextResult(err.Error()+". Nothing was saved: send the whole list again within the limits.", true)
	case errors.Is(err, kb.ErrReadOnly):
		return proto.TextResult("the meeting's highlights can't be changed now", true)
	}
	return proto.TextResult("this meeting's highlights are not available now", true)
}
