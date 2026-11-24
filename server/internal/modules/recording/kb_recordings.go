package recording

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// The phone's recording sync (docs/protocol/protocol.md "Recording sync", kb/recordings.go): transcripts into the knowledge base,
// keyed by the recording's UUID. The hub relays these like the other knowledge-base routes.

// codeRecordingDeleted: the recording was deleted here; the phone drops it instead of uploading it again.
const codeRecordingDeleted = "recording_deleted"

// maxTitleBody is the most a rename's body may be (#8877): a title of 200 characters with room for JSON escaping.
const maxTitleBody = 4 << 10

func (s *Recordings) recordingRoutes(engine func(http.ResponseWriter, *http.Request) *kb.Engine) {
	s.handle("PUT /api/v1/recordings/{id}", func(w http.ResponseWriter, r *http.Request) {
		var u kb.RecordingUpload
		// The transcript is at most 4 MB of text; JSON escaping and the segments' numbers on top.
		if err := s.decodeLargeBody(w, r, &u, 16<<20); err != nil {
			if engine(w, r) != nil { // a knowledge base that is off or read-only answers first, as before
				apierr.Write(w, http.StatusBadRequest, err)
			}
			return
		}
		// One saved into a track (space_recordings.go) goes there, and stays there whatever a later upload names.
		if strings.TrimSpace(u.Space) != "" || s.inSpaceRecording(r.Context(), r.PathValue("id")) {
			s.putSpaceRecordingRoute(w, r, u)
			return
		}
		e := engine(w, r)
		if e == nil {
			return
		}
		meeting := strings.EqualFold(strings.TrimSpace(u.Kind), kb.RecordingMeeting)
		if meeting {
			// Whether hub transcription is ready is read under the knowledge base's locks (the first upload naming the
			// hub), as it stands: probed here first, and waited for while no answer stands (the choice just changed),
			// so a probe in flight never makes a ready hub look unavailable.
			s.transcriptionReadyAwait()
		}
		put, err := e.PutRecording(r.PathValue("id"), u, time.Now())
		status := put.Status
		var full kb.DayFullError
		switch {
		case errors.Is(err, kb.ErrRecordingDeleted):
			apierr.WriteCode(w, http.StatusGone, codeRecordingDeleted, err)
			return
		case errors.Is(err, kb.ErrKindChanged): // to or from an all-day recording (kb_day.go)
			apierr.WriteCode(w, http.StatusConflict, codeKindChanged, i18n.Err("assistant-day-recap.kind_changed"))
			return
		case errors.As(err, &full): // a day over its limits: the phone splits it into parts
			writeDayFull(w, full.Next)
			return
		case errors.Is(err, kb.ErrBadRecording):
			apierr.Write(w, http.StatusBadRequest, err)
			return
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		id, _ := kb.RecordingID(r.PathValue("id"))
		day := dayUpload(u)
		if status == kb.RecordingStored && (u.Complete || day || put.TitleDue) {
			// its summary and minutes now, not at the next round; a day's recap repaired or final; its live title (#8873)
			s.kbWake()
		}
		// The role in effect, always (an older manager leaves it out, so the phone stops attending), and what was
		// wrong with the upload's attendance fields, which never fail it.
		out := map[string]any{"id": id, "revision": u.Revision, "status": status, "attendance": put.Attendance}
		if put.NameSource != "" { // what the hub calls it and why (#8873): none while it is named only by its start
			out["name"], out["name_source"] = put.Name, put.NameSource
		}
		if day {
			putDayFields(out, put)
		}
		if put.AttendanceProblem != "" {
			out["attendance_problem"] = put.AttendanceProblem
		}
		// The transcription mode in effect (meeting_transcribe.go): on every meeting upload, and on another kind that
		// asked for the hub (not_meeting); only an older manager leaves it out, and the phone then stays on the device.
		if meeting || put.TranscriptionProblem != "" {
			out["transcription"] = put.Transcription
			if put.TranscriptionProblem != "" {
				out["transcription_problem"] = put.TranscriptionProblem
			}
		}
		if status == kb.RecordingStored && put.Transcription == kb.TranscriptionHub {
			s.hubUploaded(e, id, put.Segments, u.Complete) // the timeline, the phone's text (as stored), the fallback
		}
		if put.Room { // a room meeting (meeting_room.go): its room's state, at least none; only an older manager leaves it out
			room := &kb.RoomView{State: kb.RoomNone}
			if put.Meeting != nil && put.Meeting.Room != nil {
				room = put.Meeting.RoomView()
			}
			out["room"] = room
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("DELETE /api/v1/recordings/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := kb.RecordingID(r.PathValue("id")); ok {
			if s.h.DeleteSpaceRecording(w, r, id) { // saved into a track (the core's space_recordings.go): answered
				return
			}
		}
		e := engine(w, r)
		if e == nil {
			return
		}
		deleted, err := e.DeleteRecording(r.PathValue("id"), time.Now())
		switch {
		case errors.Is(err, kb.ErrBadRecording):
			apierr.Write(w, http.StatusBadRequest, err)
			return
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		if deleted { // its room goes with it (meeting_room.go); the retention pass asks again if the node is away
			go s.meetingRoomsSweep(context.WithoutCancel(r.Context()), e)
			s.hubStop(r.PathValue("id"), codeRecordingDeleted) // its audio socket and job (meeting_transcribe.go)
		}
		apierr.WriteJSON(w, map[string]any{"deleted": deleted})
	})
	// The owner's name from the web (#8877): it names the recording until the phone renames it again.
	s.handle("PUT /api/v1/recordings/{id}/title", func(w http.ResponseWriter, r *http.Request) {
		if !meetingCaller(w, r) {
			return
		}
		e := engine(w, r)
		if e == nil {
			return
		}
		raw, ok := meetingBody(w, r, maxTitleBody, "4 KiB")
		if !ok {
			return
		}
		var in struct {
			Title *string `json:"title"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Title == nil {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_title"))
			return
		}
		rec, err := e.RenameRecording(r.PathValue("id"), *in.Title, time.Now())
		switch {
		case errors.Is(err, kb.ErrBadTitle):
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_title"))
			return
		case errors.Is(err, kb.ErrBadRecording):
			apierr.Write(w, http.StatusBadRequest, err)
			return
		case err != nil:
			writeMeetingError(w, err)
			return
		}
		out := map[string]any{}
		if src := rec.NameSource(); src != "" { // as the upload's answer: none while it is named only by its start
			out["name"], out["name_source"] = rec.Name, src
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("GET /api/v1/recordings", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		kept, deleted := e.RecordingStates()
		// Those saved into a track are listed too (with space), so the phone sends them no more than the others.
		if more, gone := s.spaceRecordingStates(r.Context()); len(more)+len(gone) > 0 {
			kept, deleted = append(kept, more...), append(deleted, gone...)
			slices.SortStableFunc(kept, func(a, b kb.RecordingState) int { return b.StartedAt.Compare(a.StartedAt) })
			slices.Sort(deleted)
		}
		apierr.WriteJSON(w, map[string]any{"days": e.RecordingDays(), "recordings": kept, "deleted": deleted})
	})
	s.minutesRoutes(engine)
	s.meetingRoutes()
	s.handoffRoutes() // a meeting handed to an Agent after it (#8852)
	s.dayRoutes()
	// Hub transcription's gap uploads (meeting_audio.go); its audio socket is ServeSocket's.
	s.handle("PUT /api/v1/recordings/{id}/audio", s.audioPut)
}
