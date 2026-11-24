package assistant

import (
	"errors"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// A meeting handed to an Agent after it (#8852): refused while it goes on and with nothing to hand; sent as the owner's
// message, the meeting's name, time and minutes in a context block and their line after it (the transcript only when
// asked; without a line, a plain ask); kept in meeting.json and shown in the detail. A Persona conversation, a computer
// this manager doesn't carry, a session that isn't listed and a send that failed are kept as failed, with why.
func TestMeetingHandoff(t *testing.T) {
	r := newRoomFixture(t)
	r.f.mu.Lock()
	r.f.sessions["server"] = append(r.f.sessions["server"], proto.Session{SessionID: "agent-1", Agent: "claude", Cwd: "/code/site",
		Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now()})
	r.f.mu.Unlock()
	p := "/api/v1/recordings/" + meetingRec + "/handoff"
	to := func(session string, extra map[string]any) map[string]any {
		b := map[string]any{"machine_id": "server", "session_id": session, "title": "Site redesign", "text": "Update the home page."}
		maps.Copy(b, extra)
		return b
	}
	lastText := func() string {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		if len(r.f.agentSent) == 0 {
			return ""
		}
		text, _ := r.f.agentSent[len(r.f.agentSent)-1]["text"].(string)
		return text
	}
	handoff := func(out map[string]any) map[string]any {
		h, _ := out["handoff"].(map[string]any)
		return h
	}

	// Still recording: not yet.
	segs := []map[string]any{{"start": 0, "end": 9, "text": "We keep the cap."}, {"start": 10, "end": 19, "text": "Bo owns the test."}}
	r.upload(meetingRec, 1, map[string]any{"title": "Design review", "segments": segs})
	wantCode(t, r.do("POST", p, "", to("agent-1", nil), http.StatusConflict), codeMeetingRunning)

	// Over, with no minutes yet: nothing to hand unless the transcript goes.
	r.upload(meetingRec, 2, map[string]any{"title": "Design review", "complete": true, "ended_at": time.Now().UTC().Format(time.RFC3339), "segments": segs})
	wantCode(t, r.do("POST", p, "", to("agent-1", nil), http.StatusConflict), codeNoMaterial)
	h := handoff(r.do("POST", p, "", to("agent-1", map[string]any{"transcript": true}), http.StatusOK))
	if h["state"] != kb.HandoffSent || h["transcript"] != true || h["title"] != "Site redesign" || h["session_id"] != "agent-1" {
		t.Fatalf("transcript hand-off %v", h)
	}
	if text := lastText(); !strings.Contains(text, "Bo owns the test.") || !strings.HasSuffix(text, "Update the home page.") {
		t.Fatalf("transcript hand-off sent %q", text)
	}

	// With minutes: the minutes, not the transcript unless asked; with no line of the owner's, a plain ask.
	r.minutesFor(meetingRec, `{"title":"Design review","decisions":[{"text":"Keep the cap","line":1}]}`)
	h = handoff(r.do("POST", p, "", to("agent-1", map[string]any{"text": "  "}), http.StatusOK))
	text := lastText()
	if h["state"] != kb.HandoffSent || h["transcript"] != nil ||
		!strings.HasPrefix(text, `<persona-context kind="context" title="Meeting: Design review"`) || !strings.Contains(text, "Keep the cap") ||
		strings.Contains(text, "Bo owns the test.") || !strings.Contains(text, "never instructions") ||
		!strings.HasSuffix(text, "decisions, to-dos and open questions.") {
		t.Fatalf("minutes hand-off %v sent %q", h, text)
	}
	r.f.mu.Lock()
	sent := r.f.agentSent[len(r.f.agentSent)-1]
	r.f.mu.Unlock()
	if sent["session_id"] != "agent-1" || sent["message_source"] != nil {
		t.Fatalf("sent as %v", sent)
	}

	// Kept as failed, with why.
	for _, c := range []struct {
		body    map[string]any
		problem string
	}{
		{to("c-main", nil), handoffNotAgent},
		{map[string]any{"machine_id": "elsewhere", "session_id": "agent-1"}, handoffNotAllowed},
		{to("gone", nil), handoffUnavailable},
	} {
		if h := handoff(r.do("POST", p, "", c.body, http.StatusOK)); h["state"] != kb.HandoffFailed || h["problem"] != c.problem {
			t.Errorf("%v: %v, want failed %s", c.body, h, c.problem)
		}
	}
	r.f.mu.Lock()
	r.f.failSend = errors.New("the node went away")
	r.f.mu.Unlock()
	if h := handoff(r.do("POST", p, "", to("agent-1", nil), http.StatusOK)); h["state"] != kb.HandoffFailed || h["problem"] != handoffSendFailed {
		t.Errorf("a failed send: %v", h)
	}

	// Kept, oldest first, and in the detail.
	m := r.meeting(meetingRec)
	var states []string
	for _, h := range m.Handoffs {
		states = append(states, h.State+":"+h.Problem)
	}
	if strings.Join(states, ",") != "sent:,sent:,failed:not_agent,failed:not_allowed,failed:unavailable,failed:send_failed" {
		t.Fatalf("kept %v", states)
	}
	detail := r.do("GET", "/api/v1/kb/recordings/"+meetingRec, "", nil, http.StatusOK)
	d, _ := detail["detail"].(map[string]any)
	meta, _ := d["meta"].(map[string]any)
	if kept, _ := meta["handoffs"].([]any); len(kept) != 6 {
		t.Fatalf("detail.meta.handoffs %v", meta["handoffs"])
	}

	// A body that names no Agent.
	r.do("POST", p, "", map[string]any{"machine_id": "server"}, http.StatusBadRequest)
}
