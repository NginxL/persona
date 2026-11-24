package recording

import (
	"cmp"
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/transcribejob"
)

// The final pass (docs/assistant/meeting-minutes.md "Gaps and the final pass"): once a hub-mode recording is complete,
// no audio socket is open (or it sent end) and no gap is left, or finalGapWait after the complete upload, the stored
// audio goes through the contribution's replay job at 4x, with the transcript of record as reference frames. From its
// start the recording takes no more audio (transcription_final). The final transcript is the pass's sentences in its
// windows, the live sentences in the windows it kept and wherever it did not get to; the echo rule marks Persona's echo
// among its sentences as among the live ones (kept, with the mark). A pass that fails is tried again after each of
// finalRetries from its first window not done, and a try that never reached the upstream (upstream_unavailable with
// nothing billed) after each of finalUnreachableRetries, counted apart (finalRetryAfter); then the state is final
// with the live sentences, or failed when the hub has none. A recording that never completes is complete for the pass
// feedStale after its last upload. Once the pass stored its text the knowledge base makes the minutes (again,
// quietly) and deletes the audio when no minutes will come (kb.FinalTranscriptStored).

// finalPassState is one recording's final pass in this process: running (cancel ends the try, its recording deleted
// or trimmed), when it may be tried again, the windows it finished (kept across tries) and where they end, and
// whether the try running billed anything.
type finalPassState struct {
	running bool
	cancel  context.CancelFunc
	nextAt  time.Time
	doneTo  float64
	windows []finalWindow
	billed  bool
}

// finalWindow is one window of a replay: its span, whether the hub's text stays (kept), the pass's sentences in it.
type finalWindow struct {
	from, to  float64
	kept      bool
	sentences []kb.RecordingSegment
}

// replayFrame is how much audio one binary frame of a replay carries (one second, under the 64 KiB a frame may be).
const replayFrame = kb.AudioRate * 2

var (
	errNoReplay  = errors.New("the transcription contribution has no replay")
	errNoDone    = errors.New("the replay job did not finish")
	errFinalDone = errors.New("the final pass already ran")
)

// finalPasses looks at the hub-mode recordings every finalEvery, and when kicked (a complete upload, a socket's end,
// the last gap uploaded), until ctx ends; restarted, it resumes the passes that were running.
func (s *Recordings) finalPasses(ctx context.Context) {
	t := time.NewTicker(s.tm().finalEvery)
	defer t.Stop()
	s.kickFinal() // makes the channel
	s.hubTr.mu.Lock()
	kick := s.hubTr.kick
	s.hubTr.mu.Unlock()
	for {
		s.finalRound(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

// finalRound is one look at every hub-mode recording still needing the manager (while the assistant is connected
// and not on standby: the knowledge base is its, kbLoop).
func (s *Recordings) finalRound(ctx context.Context) {
	if !s.kbConnected() || s.hello(ctx).Assistant.Standby {
		return
	}
	e, err := s.kbGet(ctx)
	if err != nil || e.Store.ReadOnly() || s.kbReadOnly(ctx) {
		return
	}
	s.hubSweep(e)
	ids, err := e.HubRecordings()
	if err != nil {
		return
	}
	now := time.Now()
	for _, id := range ids {
		s.finalLook(ctx, e, id, now)
	}
	s.hubTr.mu.Lock()
	for id, st := range s.hubTr.passes { // a recording gone, or done: nothing left of its pass
		if !st.running && !slices.Contains(ids, id) {
			delete(s.hubTr.passes, id)
		}
	}
	s.hubTr.mu.Unlock()
}

// finalLook starts recording id's final pass when it is due.
func (s *Recordings) finalLook(ctx context.Context, e *kb.Engine, id string, now time.Time) {
	_, rec, err := e.MeetingRead(id)
	if err != nil || !rec.HubTranscribed() || rec.Trimmed() {
		return
	}
	t := rec.Conv.Transcription
	if t.State == kb.TranscriptionFinal || t.State == kb.TranscriptionFailed {
		return
	}
	s.hubTr.mu.Lock()
	if s.hubTr.passes == nil {
		s.hubTr.passes = map[string]*finalPassState{}
	}
	st := s.hubTr.passes[rec.ID]
	if st == nil {
		st = &finalPassState{}
		s.hubTr.passes[rec.ID] = st
	}
	busy := st.running || now.Before(st.nextAt)
	s.hubTr.mu.Unlock()
	if busy {
		return
	}
	complete := !t.CompleteAt.IsZero()
	stale := !complete && !rec.Updated.IsZero() && now.Sub(rec.Updated) >= feedStale // never completes: its end
	if !complete && !stale {
		return
	}
	if t.FinalFrom.IsZero() {
		if t.State == kb.TranscriptionLive {
			if _, err := e.TranscriptionUpdate(rec.ID, func(t *kb.MeetingTranscription, _ kb.Recording) error {
				if t.State == kb.TranscriptionLive {
					t.State = kb.TranscriptionFinishing
				}
				return nil
			}); err != nil {
				s.logf("recording %s: transcription finishing: %v", rec.ID, err)
				return
			}
		}
		run := s.hubRunOf(rec.ID)
		open := run != nil && run.socketOpen()
		waited := complete && now.Sub(t.CompleteAt) >= s.tm().finalGapWait
		if !stale && !waited && (open || len(t.Gaps) > 0) {
			return // waits for the socket's end and the gap uploads
		}
	}
	s.hubTr.mu.Lock()
	if st.running {
		s.hubTr.mu.Unlock()
		return
	}
	pctx, cancel := context.WithCancel(ctx)
	st.running, st.cancel = true, cancel
	s.hubTr.mu.Unlock()
	s.hubTr.wg.Add(1)
	go func() {
		defer s.hubTr.wg.Done()
		defer cancel()
		defer func() {
			s.hubTr.mu.Lock()
			st.running, st.cancel = false, nil
			s.hubTr.mu.Unlock()
		}()
		if err := s.finalPass(pctx, e, rec.ID, st); err != nil {
			s.logf("recording %s: final pass: %v", rec.ID, err)
		}
	}()
}

// finalCancel ends recording id's final pass running in this process, if any (the recording deleted or trimmed): its
// replay job is closed at once rather than transcribing what is gone; the try is not counted.
func (s *Recordings) finalCancel(id string) {
	id, ok := kb.RecordingID(id)
	if !ok {
		return
	}
	s.hubTr.mu.Lock()
	var cancel context.CancelFunc
	if st := s.hubTr.passes[id]; st != nil {
		cancel = st.cancel
	}
	s.hubTr.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// finalPass is one try of recording id's final pass.
func (s *Recordings) finalPass(ctx context.Context, e *kb.Engine, id string, st *finalPassState) error {
	now := time.Now()
	s.hubTr.mu.Lock()
	doneTo := st.doneTo
	s.hubTr.mu.Unlock()
	t, err := e.TranscriptionUpdate(id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		if t.State == kb.TranscriptionFinal || t.State == kb.TranscriptionFailed {
			return errFinalDone
		}
		t.State = kb.TranscriptionFinishing
		if t.FinalFrom.IsZero() {
			t.FinalFrom = now.UTC() // transcription_final from here
		}
		t.FinalTries++
		t.FinalDone = doneTo // the apps' progress: what this process has done (none after a restart, which starts over)
		return nil
	})
	switch {
	case errors.Is(err, errFinalDone):
		return nil
	case err != nil:
		return err
	}
	s.hubTr.mu.Lock()
	st.billed = false
	s.hubTr.mu.Unlock()
	s.hubStop(id, codeTranscriptionFinal) // the live pass's last sentences are written first
	h, err := e.HubTranscript(id)
	if err != nil {
		return err
	}
	var tl kb.Timeline // the complete upload's: its speaking spans mark Persona's echo in the final transcript
	if t.Timeline != nil {
		tl = *t.Timeline
	}
	live := h.Segments
	if h.Pass == kb.HubPassFinal { // stored before a crash, before the state
		st.windows, live = []finalWindow{{from: 0, to: math.Inf(1), sentences: h.Segments}}, nil
		return s.finalEnd(e, id, st, live, tl)
	}
	held, err := e.AudioHeld(id)
	if err != nil {
		return err
	}
	if len(held) == 0 {
		return s.finalEnd(e, id, st, live, tl) // no audio at all
	}
	err = s.replay(ctx, e, id, held, tl, st)
	switch {
	case err == nil, errors.Is(err, errNoReplay):
		return s.finalEnd(e, id, st, live, tl)
	case ctx.Err() != nil:
		return err // the manager stops (resumed after the restart), or the recording went (finalCancel)
	}
	s.logf("recording %s: final pass try %d failed: %v", id, t.FinalTries, err)
	s.hubTr.mu.Lock()
	billed := st.billed
	s.hubTr.mu.Unlock()
	var je *transcribejob.Error
	// A busy service (#13446) that billed nothing was never reached either, as when busy was upstream_unavailable.
	unreachable := errors.As(err, &je) && (je.Code == extwire.TEUpstreamUnavailable || je.Code == extwire.TEUpstreamBusy) && !billed
	if unreachable {
		u, uerr := e.TranscriptionUpdate(id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
			t.FinalUnreachable++
			return nil
		})
		if uerr != nil {
			return uerr
		}
		t = u
	}
	if wait, ok := finalRetryAfter(s.tm(), t, unreachable); ok {
		s.hubTr.mu.Lock()
		st.nextAt = time.Now().Add(wait)
		s.hubTr.mu.Unlock()
		return nil
	}
	return s.finalEnd(e, id, st, live, tl)
}

// finalRetryAfter is how long after a failed try of the final pass the next one starts, false when none is left. A try
// that never reached the upstream (unreachable: counted in FinalUnreachable) waits by finalUnreachableRetries; any
// other failed try by finalRetries, counted among the tries that were not unreachable (#9535).
func finalRetryAfter(tm timings, t kb.MeetingTranscription, unreachable bool) (time.Duration, bool) {
	waits, n := tm.finalRetries, t.FinalTries-t.FinalUnreachable
	if unreachable {
		waits, n = tm.finalUnreachableRetries, t.FinalUnreachable
	}
	if n < 1 || n > len(waits) {
		return 0, false
	}
	return waits[n-1], true
}

// finalEnd stores recording id's final transcript (the windows done, the live sentences elsewhere), Persona's echo in
// it marked by timeline tl (the live sentences judged before a late timeline repaired a span too), and its state:
// final, or failed when the hub has no sentence of its own; then the minutes are told.
func (s *Recordings) finalEnd(e *kb.Engine, id string, st *finalPassState, live []kb.RecordingSegment, tl kb.Timeline) error {
	merged := mergeFinal(st.windows, live)
	for i, sg := range merged {
		if !sg.Echo {
			merged[i].Echo = kb.EchoOf(tl, sg.Start, sg.End, sg.Text)
		}
	}
	state := kb.TranscriptionFinal
	if len(merged) == 0 {
		state = kb.TranscriptionFailed
	}
	if len(merged) > 0 || len(live) > 0 {
		if _, err := e.HubTranscriptPut(id, kb.HubPassFinal, merged); err != nil {
			if errors.Is(err, kb.ErrNotFound) || errors.Is(err, kb.ErrRecordingDeleted) {
				return nil
			}
			return err
		}
	}
	now := time.Now().UTC()
	if _, err := e.TranscriptionUpdate(id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.State, t.FinalAt, t.Problem, t.Fallback = state, now, "", false
		return nil
	}); err != nil {
		return err
	}
	s.hubTr.mu.Lock()
	delete(s.hubTr.passes, id)
	s.hubTr.mu.Unlock()
	s.logf("recording %s: final pass %s: %d sentence(s), %d window(s)", id, state, len(merged), len(st.windows))
	if err := e.FinalTranscriptStored(id); err != nil {
		s.logf("recording %s: final transcript stored: %v", id, err)
	}
	s.kbWake() // the minutes, from the final transcript
	return nil
}

// mergeFinal is the final transcript: the pass's sentences in the windows it did not keep, and the live sentences
// (whose middle is) elsewhere, in time order.
func mergeFinal(windows []finalWindow, live []kb.RecordingSegment) []kb.RecordingSegment {
	var out []kb.RecordingSegment
	var passed [][2]float64
	for _, w := range windows {
		if !w.kept {
			out = append(out, w.sentences...)
			passed = append(passed, [2]float64{w.from, w.to})
		}
	}
	for _, sg := range live {
		mid := (sg.Start + sg.End) / 2
		if !slices.ContainsFunc(passed, func(p [2]float64) bool { return p[0] <= mid && mid < p[1] }) {
			out = append(out, sg)
		}
	}
	slices.SortStableFunc(out, func(a, b kb.RecordingSegment) int { return cmp.Compare(a.Start, b.Start) })
	return out
}

// replay runs a replay job over recording id's audio held from the first window not done: the transcript of record
// as reference frames first, then the audio, at most replayAhead ahead of the job's ack, with seek over holes. The
// windows it finishes join st (also when it fails later).
func (s *Recordings) replay(ctx context.Context, e *kb.Engine, id string, held [][2]float64, tl kb.Timeline, st *finalPassState) error {
	ref, _, err := s.transcriptionTarget(ctx)
	if err != nil {
		return err
	}
	ictx, cancel := context.WithTimeout(ctx, s.tm().jobReadyWait)
	info, err := s.h.TranscriptionInfo(ictx, ref)
	cancel()
	if err != nil {
		return err
	}
	if !slices.Contains(info.Features, extwire.TranscriptionReplay) {
		return errNoReplay // the live sentences are final
	}
	s.hubTr.mu.Lock()
	doneTo := st.doneTo
	s.hubTr.mu.Unlock()
	pos := int64(-1)
	for _, h := range held {
		if h[1] > doneTo {
			pos = int64(math.Round(max(h[0], doneTo) * kb.AudioRate))
			break
		}
	}
	if pos < 0 {
		return nil // every window done
	}
	record, err := e.RecordOf(id)
	if err != nil {
		return err
	}
	_, rec, err := e.MeetingRead(id)
	if err != nil {
		return err
	}
	jctx, jcancel := context.WithCancel(ctx)
	defer jcancel()
	rctx, rcancel := context.WithTimeout(jctx, s.tm().jobReadyWait)
	defer rcancel()
	jc, err := s.dialJob(rctx, ref)
	if err != nil {
		return err
	}
	start := extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: extwire.TranscriptionReplay, Speed: 4,
		Language: recordingLanguage(rec), Glossary: s.transcriptionGlossary(ctx, rec), At: extwire.F(float64(pos) / kb.AudioRate)}
	if err := jc.Send(rctx, start); err != nil {
		jc.Close()
		return err
	}
	if _, err := jc.AwaitReady(rctx); err != nil {
		jc.Close()
		return err
	}
	rcancel()
	for _, sg := range record { // the transcript of record, before the first audio
		if sg.End <= doneTo || sg.Text == "" {
			continue
		}
		if err := jc.Send(jctx, extwire.TranscriptionFrame{Type: extwire.TFReference, From: extwire.F(sg.Start), To: extwire.F(sg.End),
			Text: extwire.F(sg.Text)}); err != nil {
			jc.Close()
			return err
		}
	}

	var mu sync.Mutex
	acked := pos
	// heard is when the job last sent anything: one that keeps its socket open but stops answering (its upstream
	// stalled) is given up after jobDoneWait without a frame, and the pass is tried again.
	var heard atomic.Int64
	heard.Store(time.Now().UnixNano())
	wake := make(chan struct{}, 1)
	result := make(chan error, 1)
	exited := make(chan struct{})
	defer func() {
		jc.Close()
		<-exited
	}()
	// sendFailed is the try's error once a send to the job failed: the job's own (its fatal error, read before the
	// socket it closed after it), when the reader has it within a second, else the send's. Which one it is decides the
	// retry schedule (finalRetryAfter).
	sendFailed := func(err error) error {
		t := time.NewTimer(time.Second)
		defer t.Stop()
		select {
		case r := <-result:
			return cmp.Or(r, err)
		case <-t.C:
			return err
		}
	}
	go func() {
		defer close(exited)
		var cur []kb.RecordingSegment
		var usage float64
		for {
			f, err := jc.Read(jctx)
			if err != nil {
				result <- err
				return
			}
			heard.Store(time.Now().UnixNano())
			switch f.Type {
			case extwire.TFSentence:
				if f.Start != nil && f.End != nil && f.Text != nil {
					sg := kb.RecordingSegment{Start: *f.Start, End: max(*f.End, *f.Start), Text: *f.Text, Source: kb.SourceHub}
					if sg.Text != "" { // Persona's echo kept, marked (kb/echo.go)
						sg.Echo = kb.EchoOf(tl, sg.Start, sg.End, sg.Text)
						cur = append(cur, sg)
					}
				}
			case extwire.TFWindow:
				if f.From != nil && f.To != nil {
					w := finalWindow{from: *f.From, to: *f.To, kept: f.Kept}
					if !f.Kept {
						w.sentences = cur
					}
					cur = nil
					s.hubTr.mu.Lock()
					st.windows = append(st.windows, w)
					st.doneTo = max(st.doneTo, w.to)
					doneTo := st.doneTo
					s.hubTr.mu.Unlock()
					s.finalProgress(e, id, doneTo)
				}
			case extwire.TFAck:
				if f.At != nil {
					mu.Lock()
					acked = max(acked, int64(*f.At*kb.AudioRate))
					mu.Unlock()
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			case extwire.TFUsage:
				if add := f.Seconds - usage; add > 0 {
					usage = f.Seconds
					s.hubTr.mu.Lock()
					st.billed = true // a failure from here is not "unreachable"
					s.hubTr.mu.Unlock()
					s.logf("recording %s: transcription usage %.1f s billed (replay job: %.1f s, %d session(s))", id, add, f.Seconds, f.Sessions)
					s.addUsage(e, id, add)
				}
			case extwire.TFWarning:
				s.logf("recording %s: transcription warning %s", id, f.Code)
			case extwire.TFError:
				if f.Fatal {
					result <- &transcribejob.Error{Code: f.Code}
					return
				}
				s.logf("recording %s: transcription error %s", id, f.Code)
			case extwire.TFDone:
				if len(cur) > 0 { // sentences after the last window: a window of their own
					s.hubTr.mu.Lock()
					from := st.doneTo
					to := from
					for _, sg := range cur {
						to = max(to, sg.End)
					}
					st.windows = append(st.windows, finalWindow{from: from, to: math.Nextafter(to, math.Inf(1)), sentences: cur})
					st.doneTo = max(st.doneTo, to)
					s.hubTr.mu.Unlock()
				}
				result <- nil
				return
			}
		}
	}()

	buf := make([]byte, replayFrame)
	ahead := int64(s.tm().replayAhead.Seconds() * kb.AudioRate)
	for {
		for {
			mu.Lock()
			behind := pos - acked
			mu.Unlock()
			if behind <= ahead {
				break
			}
			left := s.tm().jobDoneWait - time.Since(time.Unix(0, heard.Load()))
			if left <= 0 {
				return errNoDone // the job stopped answering: tried again
			}
			t := time.NewTimer(left)
			select {
			case <-wake:
			case <-t.C:
			case err := <-result:
				t.Stop()
				return cmp.Or(err, errNoDone)
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
			t.Stop()
		}
		n, err := e.AudioRead(id, float64(pos)/kb.AudioRate, buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if n == 0 { // a hole: on to the next audio held
			next := nextHeld(held, pos)
			if next <= pos {
				break
			}
			pos = next
			if err := jc.Send(jctx, extwire.TranscriptionFrame{Type: extwire.TFSeek, At: extwire.F(float64(pos) / kb.AudioRate)}); err != nil {
				return sendFailed(err)
			}
			continue
		}
		if err := jc.SendAudio(jctx, buf[:n]); err != nil {
			return sendFailed(err)
		}
		pos += int64(n / 2)
	}
	if err := jc.Send(jctx, extwire.TranscriptionFrame{Type: extwire.TFEnd}); err != nil {
		return sendFailed(err)
	}
	done := time.NewTimer(s.tm().jobDoneWait)
	defer done.Stop()
	select {
	case err := <-result:
		return err
	case <-done.C:
		return errNoDone
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finalProgress tells the apps how far recording id's final pass is (#13461): the end of the last window it finished,
// once per window. A failure to write it costs only the progress shown.
func (s *Recordings) finalProgress(e *kb.Engine, id string, doneTo float64) {
	if _, err := e.TranscriptionUpdate(id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		if t.State == kb.TranscriptionFinishing {
			t.FinalDone = max(t.FinalDone, doneTo)
		}
		return nil
	}); err != nil && !errors.Is(err, kb.ErrNotFound) && !errors.Is(err, kb.ErrRecordingDeleted) {
		s.logf("recording %s: final pass progress: %v", id, err)
	}
}

// nextHeld is the first sample of the audio held after sample pos (-1: none).
func nextHeld(held [][2]float64, pos int64) int64 {
	for _, h := range held {
		if from := int64(math.Round(h[0] * kb.AudioRate)); from > pos {
			return from
		}
	}
	return -1
}
