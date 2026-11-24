package recording

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// When the hub has no sentences, the phone's text fills the live log (docs/assistant/meeting-minutes.md "Audio from
// the phone"): the room, the topics, other devices and heard asks read the live log, and the phone keeps transcribing
// on the device in hub mode. The fallback starts when the audio socket has been down for fallbackAfter (counting from
// the first hub-mode upload while it never sent start), when the live job ended with a fatal error, or when a problem
// other than no_interim has stood for fallbackAfter. Then each hub-mode upload's segments join the live log (source
// device): those that end after the hub's last sentence and that the hub's sentences, taken together, cover no more
// than half of, while the role at their end is not off and they are not Persona's echo (marked by the phone, or by the
// echo rule against the timeline, kb.EchoOf). It ends once the hub's sentences come again (hubRun.acceptLocked).

// hubUploaded is told of a stored hub-mode upload of recording id with its segments: the run learns the meeting's role,
// its timeline and the phone's text; a socket that never started counts as down from the first one; in the fallback
// the segments join the live log. A complete upload has the final passes looked at.
func (s *Recordings) hubUploaded(e *kb.Engine, id string, segs []kb.RecordingSegment, complete bool) {
	defer func() {
		if complete {
			s.kickFinal()
		}
	}()
	r := s.hubRunFor(e, id)
	if r == nil {
		return
	}
	m, rec, err := e.MeetingRead(id)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.role = rec.AttendanceRole()
	if t := m.Transcription; t != nil && t.Timeline != nil {
		r.timeline.Merge(*t.Timeline)
		// The upload's timeline repairs a speaking off a socket lost; while the socket that saw the span open is up,
		// its own speaking off will come, and nothing the upload says closes the span before it.
		if !r.openSeenLocked() {
			r.reconcileOpenLocked()
		}
	}
	r.device = slices.Clone(segs)
	for _, sg := range segs {
		r.deviceTo = max(r.deviceTo, sg.End)
	}
	if !r.started && r.downSince.IsZero() {
		r.downSince = time.Now() // never connected: down from the first hub-mode upload
	}
	if r.fallback {
		r.fillLocked()
	}
	r.notifyLocked()
}

// fallbackCheckLocked starts the fallback when it is due.
func (r *hubRun) fallbackCheckLocked(now time.Time) {
	if r.fallback || r.stopped || r.ended {
		return
	}
	down := !r.phoneReady && !r.downSince.IsZero() && now.Sub(r.downSince) >= r.s.tm().fallbackAfter
	standing := r.upstream != "" && now.Sub(r.upstreamAt) >= r.s.tm().fallbackAfter
	if !down && !r.fatal && !standing {
		return
	}
	r.setFallbackLocked(true)
	r.fillLocked()
}

// setFallbackLocked turns the fallback on or off, in meeting.json too (transcription.fallback).
func (r *hubRun) setFallbackLocked(on bool) {
	r.fallback = on
	if _, err := r.e.TranscriptionUpdate(r.id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.Fallback = on
		return nil
	}); err != nil && !errors.Is(err, kb.ErrNotFound) && !errors.Is(err, kb.ErrRecordingDeleted) {
		r.s.logf("recording %s: transcription fallback: %v", r.id, err)
	}
	r.notifyLocked()
}

// fillLocked appends the phone's segments the fallback takes to the live log (source device).
func (r *hubRun) fillLocked() {
	hub := make([][2]float64, 0, len(r.hub))
	for _, sg := range r.hub {
		hub = append(hub, [2]float64{sg.Start, sg.End})
	}
	after := max(r.lastHubEnd, r.filledTo)
	var add []kb.LiveSegment
	for _, sg := range r.device {
		switch {
		case sg.End <= after || sg.Text == "":
		case kb.CoveredShare(sg.Start, sg.End, hub) > 0.5:
		case r.timeline.RoleAt(sg.End, r.role) == kb.AttendanceOff:
		case sg.Echo || kb.EchoOf(r.timeline, sg.Start, sg.End, sg.Text): // Persona's echo
		default:
			add = append(add, kb.LiveSegment{Start: sg.Start, End: sg.End, Text: sg.Text, Source: kb.SourceDevice})
		}
	}
	if len(add) == 0 {
		return
	}
	slices.SortStableFunc(add, func(a, b kb.LiveSegment) int {
		switch {
		case a.Start < b.Start:
			return -1
		case a.Start > b.Start:
			return 1
		}
		return 0
	})
	if _, err := r.e.LiveAppendHub(r.id, add); err != nil {
		if !errors.Is(err, kb.ErrLiveFull) {
			r.s.logf("recording %s: live lines from the phone: %v", r.id, err)
		}
		return
	}
	for _, sg := range add {
		r.filled = append(r.filled, [2]float64{sg.Start, sg.End})
		r.filledTo = max(r.filledTo, sg.End)
	}
	r.notifyLocked()
}

// inFallback: the live log is filled from the phone now.
func (r *hubRun) inFallback() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fallback
}

// awaitDevice waits until an upload's segments reach recording second at (a heard ask in the fallback), or ctx ends.
func (r *hubRun) awaitDevice(ctx context.Context, at float64) {
	for {
		r.mu.Lock()
		if r.deviceTo >= at || r.stopped {
			r.mu.Unlock()
			return
		}
		wake := r.wake
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}
