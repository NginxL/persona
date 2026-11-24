package recording

import (
	"net/http"
	"sync"
)

// Recordings is the recording module's state in one manager: what the assistant's Service held of it before.
type Recordings struct {
	h Host

	// minutesMu is held while confirmed action items of meeting minutes become to-dos (kb_minutes.go): one
	// confirmation at a time reads and records what the others made.
	minutesMu sync.Mutex
	attend    attendState       // meeting_feed.go: feeding the meetings Persona attends, and their turns
	room      roomState         // meeting_room_tools.go: the meetings' rooms, their asks and sources
	meetLive  meetingLiveEvents // meeting_events.go: the meeting_live events, at most one a second per recording
	justMe    justMeBook        // meeting_justme.go: the Just me turns
	meetLocks meetingLocks      // meeting_followup.go: one creation at a time per recording
	// asks are the questions of attended meetings being answered (meeting_ask.go).
	asks meetingAsks
	// hubTr is the meetings transcribed on the hub (meeting_transcribe.go); sockets makes socketMux, ServeSocket's
	// (meeting_audio.go).
	hubTr     hubRuns
	sockets   sync.Once
	socketMux *http.ServeMux
}

// New is the module's state, reaching the core through h.
func New(h Host) *Recordings {
	s := &Recordings{h: h}
	// The meeting turns' clock is the feeder's, and the Just me turns' the Host's (a test's, else the real one).
	s.attend.turns.now = s.attendNow
	s.justMe.now = s.hostNow
	if h != nil {
		one, lists := h.Timings()
		timingsWith(one, lists, h.Logf) // says once what it does not know
	}
	return s
}
