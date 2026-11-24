package recording

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/transcribejob"
)

// A transcription contribution's jobs (docs/assistant/meeting-minutes.md "The `transcription` extension point"):
// the live job of a meeting being recorded (one per recording, opened when its audio comes, again at once when its
// socket drops, every jobRetry after a fatal error, ended after jobIdle without audio) and the final pass's replay
// job (meeting_transcribe_final.go), each on a socket of internal/transcribejob. The manager writes the live log and
// the hub's transcript; the extension stores nothing.

// problemOfJob is the socket's problem for a job's fatal error code (an open set: what is not the contribution's
// say about its upstream reads as failed). The kind goes to the phone, which says why and what to do (#13446,
// spec/fixtures/meeting-transcription-problems): the service refused, is busy, can't be reached (or no job opens), or
// anything else; never the error's message, which may carry a log id.
func problemOfJob(code string) string {
	switch code {
	case extwire.TEUpstreamForbidden:
		return problemUpstreamForbidden
	case extwire.TEUpstreamBusy:
		return problemUpstreamBusy
	case extwire.TENoUpstream, extwire.TEUpstreamUnavailable:
		return problemUpstreamUnavailable
	}
	return problemUpstreamFailed
}

// ---- the live job ----

// liveJob is a recording's live job. Its frames to the extension go through q to one writer, so the phone's socket
// never waits on the extension; what does not fit in q (the job behind) is skipped and the job moved on with seek.
// The fields after jmu's are its run's, under its mu.
type liveJob struct {
	run    *hubRun
	ctx    context.Context
	cancel context.CancelFunc
	q      chan jobOut
	jmu    sync.Mutex
	jc     *transcribejob.Conn // under jmu; set once dialed
	closed bool                // under jmu

	choice     string // the contribution it was dialed for (transcriptionChoice), "" until then
	startS     int64  // the start's at, in samples
	sentS      int64  // the end of the audio queued
	ackedS     int64  // the extension's last ack
	ready      bool
	ending     bool
	behind     bool      // audio was skipped: the next frame is a seek
	behindAt   time.Time // since when it is behind
	ackAt      time.Time // its last ack (its ready before one)
	flushAsked bool
	usage      float64 // the job's billed seconds so far (its last usage frame)
}

// jobOut is one frame to a job: text when f is set, else audio.
type jobOut struct {
	f   *extwire.TranscriptionFrame
	pcm []byte
}

// jobQueue is how many frames a live job's writer holds (about 50 s of the phone's 100 ms frames).
const jobQueue = 512

// ensureJobLocked is the run's live job, opened from sample from when there is none (or the one there is was ended
// for want of audio: it finishes on its own) and one may be opened now.
func (r *hubRun) ensureJobLocked(from int64) *liveJob {
	if j := r.job; j != nil && !(j.ending && !r.ended) {
		return j
	}
	r.job = nil
	if r.stopped || time.Now().Before(r.jobRetryAt) {
		return nil
	}
	ctx, cancel := context.WithCancel(r.ctx)
	j := &liveJob{run: r, ctx: ctx, cancel: cancel, q: make(chan jobOut, jobQueue), startS: from, sentS: from, ackedS: from}
	r.job = j
	r.s.hubTr.wg.Add(1)
	go func() {
		defer r.s.hubTr.wg.Done()
		r.runJob(j)
	}()
	return j
}

// runJob opens job j and reads it to its end.
func (r *hubRun) runJob(j *liveJob) {
	s := r.s
	ctx := s.runCtx()
	r.mu.Lock()
	e := r.e
	r.mu.Unlock()
	_, rec, err := e.LiveRead(r.id)
	if err != nil {
		r.jobFailed(j, extwire.TEUpstreamUnavailable, err)
		return
	}
	start := extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: extwire.TranscriptionLive, Language: recordingLanguage(rec),
		Glossary: s.transcriptionGlossary(ctx, rec), At: extwire.F(float64(j.startS) / kb.AudioRate)}
	rctx, cancel := context.WithTimeout(j.ctx, r.s.tm().jobReadyWait)
	defer cancel()
	ref, choice, err := s.transcriptionTarget(ctx)
	if err != nil {
		r.jobFailed(j, extwire.TEUpstreamUnavailable, err)
		return
	}
	r.mu.Lock()
	j.choice = choice
	r.mu.Unlock()
	jc, err := s.dialJob(rctx, ref)
	if err != nil {
		r.jobFailed(j, extwire.TEUpstreamUnavailable, err)
		return
	}
	if !j.setConn(jc) {
		return // closed meanwhile
	}
	if err := jc.Send(rctx, start); err != nil {
		r.jobFailed(j, extwire.TEUpstreamUnavailable, err)
		return
	}
	ready, err := jc.AwaitReady(rctx)
	if err != nil {
		code := extwire.TEUpstreamUnavailable // no ready within jobReadyWait counts as unavailable
		var je *transcribejob.Error
		if errors.As(err, &je) {
			code = je.Code
		}
		r.jobFailed(j, code, err)
		return
	}
	cancel()
	r.jobReady(j, ready.Features)
	go j.writeLoop()
	s.transcriptionJobsCheck(ctx) // the choice may have changed while it was dialed
	for {
		f, err := jc.Read(j.ctx)
		if err != nil {
			r.jobDropped(j, err)
			return
		}
		switch f.Type {
		case extwire.TFInterim:
			if f.Start != nil && f.Text != nil {
				r.mu.Lock()
				if !r.stopped {
					r.interimLocked(*f.Start, *f.Text)
				}
				r.mu.Unlock()
			}
		case extwire.TFSentence:
			if f.Start != nil && f.End != nil && f.Text != nil {
				r.mu.Lock()
				if !r.stopped {
					r.sentenceLocked(hubSentence{Start: *f.Start, End: *f.End, Text: *f.Text})
				}
				r.mu.Unlock()
			}
		case extwire.TFAck:
			if f.At != nil {
				r.mu.Lock()
				j.ackedS, j.ackAt = max(j.ackedS, int64(*f.At*kb.AudioRate)), time.Now()
				r.mu.Unlock()
			}
		case extwire.TFFlushed:
			if f.At != nil {
				r.mu.Lock()
				r.flushedTo, j.flushAsked = max(r.flushedTo, *f.At), false
				r.notifyLocked()
				r.mu.Unlock()
			}
		case extwire.TFWarning:
			s.logf("recording %s: transcription warning %s", r.id, f.Code)
			if f.Code == extwire.TWNoInterim {
				r.mu.Lock()
				r.noInterim = true
				r.tellProblemLocked()
				r.mu.Unlock()
			}
		case extwire.TFUsage:
			r.usage(j, f.Seconds, f.Sessions)
		case extwire.TFError:
			if f.Fatal {
				r.jobFatal(j, f.Code)
				return
			}
			s.logf("recording %s: transcription error %s", r.id, f.Code)
		case extwire.TFDone:
			r.jobDone(j)
			return
		}
	}
}

// writeLoop sends job j's queued frames in order until it ends.
func (j *liveJob) writeLoop() {
	for {
		select {
		case <-j.ctx.Done():
			return
		case out := <-j.q:
			var err error
			if out.f != nil {
				err = j.jc.Send(j.ctx, *out.f)
			} else {
				err = j.jc.SendAudio(j.ctx, out.pcm)
			}
			if err != nil {
				j.close() // the reader sees it dropped
				return
			}
		}
	}
}

// queueLocked puts out in j's queue; false when it is full.
func (j *liveJob) queueLocked(out jobOut) bool {
	select {
	case j.q <- out:
		return true
	default:
		return false
	}
}

// feedLocked gives job j the phone's audio pcm from sample from (the live edge): at most liveAhead ahead of its last
// ack; past that it is skipped (the final pass has it) and the job moved on with seek.
func (j *liveJob) feedLocked(from int64, pcm []byte) {
	if j.ending {
		return
	}
	if j.ready && j.sentS-j.ackedS > int64(j.run.s.tm().liveAhead.Seconds()*kb.AudioRate) {
		if !j.behind {
			j.run.s.logf("recording %s: transcription job behind, audio skipped", j.run.id)
		}
		j.setBehindLocked(true)
		return
	}
	if j.behind || from != j.sentS {
		if !j.queueLocked(jobOut{f: &extwire.TranscriptionFrame{Type: extwire.TFSeek, At: extwire.F(float64(from) / kb.AudioRate)}}) {
			j.setBehindLocked(true)
			return
		}
		j.setBehindLocked(false)
	}
	if !j.queueLocked(jobOut{pcm: pcm}) {
		j.setBehindLocked(true)
		return
	}
	j.sentS = from + int64(len(pcm)/2)
}

// setBehindLocked notes whether job j skips audio, and since when.
func (j *liveJob) setBehindLocked(behind bool) {
	switch {
	case !behind:
		j.behindAt = time.Time{}
	case !j.behind:
		j.behindAt = time.Now()
	}
	j.behind = behind
}

// flushLocked asks job j for its sentences up to now (an ask needs them).
func (j *liveJob) flushLocked() {
	if j.queueLocked(jobOut{f: &extwire.TranscriptionFrame{Type: extwire.TFFlush}}) {
		j.flushAsked = true
	}
}

// setConn gives job j its socket; false (and the socket closed) when j was closed meanwhile.
func (j *liveJob) setConn(jc *transcribejob.Conn) bool {
	j.jmu.Lock()
	defer j.jmu.Unlock()
	if j.closed {
		jc.Close()
		return false
	}
	j.jc = jc
	return true
}

// close ends job j's socket.
func (j *liveJob) close() {
	j.jmu.Lock()
	if j.closed {
		j.jmu.Unlock()
		return
	}
	j.closed = true
	jc := j.jc
	j.jmu.Unlock()
	j.cancel()
	if jc != nil {
		jc.Close()
	}
}

// endJobLocked tells job j there is no more audio: it sends its last sentences and done, within audioEndWait.
func (r *hubRun) endJobLocked(j *liveJob) {
	if j.ending {
		return
	}
	j.ending = true
	if !j.queueLocked(jobOut{f: &extwire.TranscriptionFrame{Type: extwire.TFEnd}}) {
		r.dropJobLocked(j)
		return
	}
	time.AfterFunc(r.s.tm().audioEndWait, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.dropJobLocked(j)
	})
}

// jobStalledLocked: job j stays connected but took no audio for jobStall (its upstream hangs): it is closed as failed,
// upstream_failed stands (so the fallback comes once it stood fallbackAfter), and a new job is opened with the next
// audio; its sentences end the problem.
func (r *hubRun) jobStalledLocked(j *liveJob) {
	j.ending = true // closed by the manager: no drop of its own
	r.dropJobLocked(j)
	r.dropped = true
	r.setUpstreamLocked(problemUpstreamFailed)
}

// dropJobLocked closes job j and forgets it.
func (r *hubRun) dropJobLocked(j *liveJob) {
	j.close()
	if r.job == j {
		r.job = nil
	}
	r.notifyLocked()
}

// jobReady: job j accepted its start.
func (r *hubRun) jobReady(j *liveJob, features []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j.ready, j.ackAt = true, time.Now()
	r.dropped = false
	if !slices.Contains(features, extwire.TranscriptionInterim) {
		r.noInterim = true
		r.tellProblemLocked()
	}
	r.notifyLocked()
}

// jobFailed: job j could not be opened (code, the contribution's or upstream_unavailable): tried again after
// jobRetry. Right after a dropped job the upstream counts as failed.
func (r *hubRun) jobFailed(j *liveJob, code string, err error) {
	r.s.logf("recording %s: transcription job not opened: %v", r.id, err)
	r.mu.Lock()
	defer r.mu.Unlock()
	j.close()
	if r.job == j {
		r.job = nil
	}
	if r.stopped {
		return
	}
	p := problemOfJob(code)
	if r.dropped {
		p = problemUpstreamFailed
	}
	r.jobRetryAt = time.Now().Add(r.s.tm().jobRetry)
	r.setUpstreamLocked(p)
	r.notifyLocked()
}

// jobFatal: job j ended with a fatal error: the fallback starts, a new job is opened after jobRetry.
func (r *hubRun) jobFatal(j *liveJob, code string) {
	r.s.logf("recording %s: transcription job failed: %s", r.id, code)
	r.mu.Lock()
	defer r.mu.Unlock()
	j.close()
	if r.job == j {
		r.job = nil
	}
	if r.stopped {
		return
	}
	r.fatal = true
	r.jobRetryAt = time.Now().Add(r.s.tm().jobRetry)
	r.setUpstreamLocked(problemOfJob(code))
	r.fallbackCheckLocked(time.Now())
	r.notifyLocked()
}

// jobDropped: job j's socket ended without done: one ended by the manager is over; otherwise it is opened again at
// once from the live edge (with the next audio).
func (r *hubRun) jobDropped(j *liveJob, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ending := j.ending
	j.close()
	if r.job == j {
		r.job = nil
	}
	if !ending && !r.stopped {
		r.s.logf("recording %s: transcription job dropped: %v", r.id, err)
		r.dropped = true
	}
	if ending && r.ended {
		r.putHubLocked()
	}
	r.notifyLocked()
}

// jobDone: job j sent its last sentences.
func (r *hubRun) jobDone(j *liveJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j.close()
	if r.job == j {
		r.job = nil
	}
	if r.ended {
		r.putHubLocked()
	}
	r.notifyLocked()
}

// usage is job j's usage frame: the upstream's billed seconds of the job so far. Logged with the recording id and
// added to the transcription's total (diagnostics, never shown to apps).
func (r *hubRun) usage(j *liveJob, seconds float64, sessions int) {
	r.mu.Lock()
	add := seconds - j.usage
	if add > 0 {
		j.usage = seconds
	}
	e := r.e
	r.mu.Unlock()
	if add <= 0 {
		return
	}
	r.s.logf("recording %s: transcription usage %.1f s billed (live job: %.1f s, %d session(s))", r.id, add, seconds, sessions)
	r.s.addUsage(e, r.id, add)
}

// addUsage adds seconds to recording id's billed transcription seconds.
func (s *Recordings) addUsage(e *kb.Engine, id string, seconds float64) {
	if _, err := e.TranscriptionUpdate(id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.UsageSeconds += seconds
		return nil
	}); err != nil && !errors.Is(err, kb.ErrNotFound) && !errors.Is(err, kb.ErrRecordingDeleted) {
		s.logf("recording %s: transcription usage: %v", id, err)
	}
}
