package recording

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The notes role (#8851): a meeting Persona was not invited to keeps a room that is fed the live sentences as the
// note-taker, so its topics and highlights still come, while a question is not_attending. Inviting Persona (listener)
// and removing it again (notes) switch the same room, and asks follow the role.
func TestMeetingRoomNotes(t *testing.T) {
	r := newRoomFixture(t)
	sid := r.roomUp(meetingRec, "notes")
	starts := r.sends()
	if len(starts) != 1 || !strings.Contains(starts[0]["text"].(string), "Role: notes") {
		t.Fatalf("start %v", starts)
	}
	r.do("POST", livePath(meetingRec), "", `{"from":0,"segments":[{"start":1,"end":3,"text":"Ada: we keep the cap."}]}`, http.StatusOK)
	r.look(meetingRec)
	sends := r.sends()
	if len(sends) != 2 {
		t.Fatalf("no batch: %v", sends)
	}
	if text, _ := sends[1]["text"].(string); sends[1]["session_id"] != sid || !strings.Contains(text, "you attend as the note-taker") ||
		!strings.Contains(text, "Ada: we keep the cap.") {
		t.Fatalf("batch %v", sends[1])
	}
	if live := r.do("GET", livePath(meetingRec), "", nil, http.StatusOK); live["attendance"] != "notes" {
		t.Fatalf("live %v", live)
	}
	question := map[string]any{"id": askOne, "question": "Who owns the test?"}
	wantCode(t, r.do("POST", askPath(meetingRec), "", question, http.StatusConflict), codeNotAttending)

	// Persona invited: the same room answers.
	if out := r.upload(meetingRec, 2, map[string]any{"room": map[string]any{"attendance": "listener"}}); out["attendance"] != "listener" {
		t.Fatalf("upload %v", out)
	}
	r.f.mu.Lock()
	r.f.items = []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "Who owns the test?", ClientMsgID: askMsgID(askOne, 1)},
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "Bo owns it."},
	}
	r.f.mu.Unlock()
	a := meetingAskOf(t, r.do("POST", askPath(meetingRec), "", question, http.StatusOK))
	if a["state"] != "answered" || a["via"] != "room" || a["answer"] != "Bo owns it." || a["speak"] == true {
		t.Fatalf("ask %v", a)
	}
	if m := r.meeting(meetingRec); m.Room.SessionID != sid || m.Room.Attendance != "listener" {
		t.Fatalf("room %+v", m.Room)
	}

	// Persona removed: notes again, in the same room, and a new question is refused.
	r.upload(meetingRec, 3, map[string]any{"room": map[string]any{"attendance": "notes"}})
	wantCode(t, r.do("POST", askPath(meetingRec), "", map[string]any{"id": askTwo, "question": "And the cap?"}, http.StatusConflict), codeNotAttending)
	if m := r.meeting(meetingRec); m.Room.SessionID != sid || m.Room.Attendance != "notes" {
		t.Fatalf("room %+v", m.Room)
	}
	for _, s := range r.sends() {
		if s["new_session_id"] != nil && s["new_session_id"] != sid {
			t.Fatalf("a second room: %v", s)
		}
	}
}
