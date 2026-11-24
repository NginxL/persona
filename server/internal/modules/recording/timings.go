package recording

import (
	"slices"
	"time"
)

// timings are the module's times: the hub transcription's, the asks' and the room asks'. Each Recordings has its own,
// the defaults below with what the core's Host.Timings overrides (assistant.Options.RecordingTimings: a test and tuning
// knob, never a user setting). TestDefaultTimings pins the defaults.
type timings struct {
	// Hub transcription (meeting_transcribe.go).
	audioStartWait   time.Duration // the phone's start frame comes within this
	audioAckEvery    time.Duration // the hub acknowledges the audio it stored this often
	audioPingEvery   time.Duration // the hub pings a socket this often; one that answers nothing is closed
	audioEndWait     time.Duration // after the phone's end, its last lines come within this
	fallbackAfter    time.Duration // the socket down, or a problem standing, this long: the phone's text fills
	jobRetry         time.Duration // a job that could not be opened, or failed, is opened again after this
	jobReadyWait     time.Duration // a job answers ready within this, else it counts as upstream_unavailable
	jobIdle          time.Duration // a job with no audio this long is ended (sessions bill silence)
	jobAwayIdle      time.Duration // and this long while no phone socket is up (hubRun.idleLimitLocked)
	jobStall         time.Duration // a job behind (audio skipped) that acknowledged nothing this long has failed
	jobDoneWait      time.Duration // a replay job sends done within this of the manager's end
	echoHoldMost     time.Duration // a speaking span open longer than this is assumed open from then on (hubRun.releaseHoldLocked)
	hubSegmentsEvery time.Duration // the hub's live sentences are written to hub_segments.json this often
	hubTick          time.Duration
	interimShown     time.Duration // GET …/live shows a caption at most this old
	finalEvery       time.Duration // the final passes are looked at this often (and when kicked)
	finalGapWait     time.Duration // the final pass waits at most this long after the complete upload for gaps
	// When the final pass is tried again: finalRetries after a try that failed (it may have billed), and
	// finalUnreachableRetries after one that never reached the upstream (upstream_unavailable, nothing billed), which
	// costs nothing while the hub's route to the service can be down for longer than finalRetries span (#9535).
	finalRetries, finalUnreachableRetries []time.Duration
	// The most audio sent ahead of a job's last ack: live (more is skipped, the job moved on with seek) and replay.
	liveAhead, replayAhead time.Duration

	// Asks (meeting_ask.go).
	askHold     time.Duration // the request waits this long for its answer, then answers 202 pending
	askLiveWait time.Duration // a heard question waits this long for the live sentences up to the tap
	askTimeout  time.Duration // an answer not there by then failed: model_timeout
	askLivePoll time.Duration

	// Room asks (meeting_room_ask.go).
	askRoomPoll, askRoomWait time.Duration
}

// defaultTimings are the times a manager runs with.
func defaultTimings() timings {
	return timings{
		audioStartWait: 10 * time.Second, audioAckEvery: 5 * time.Second, audioPingEvery: 30 * time.Second,
		audioEndWait: 15 * time.Second, fallbackAfter: 30 * time.Second, jobRetry: time.Minute, jobReadyWait: 30 * time.Second,
		jobIdle: time.Minute, jobAwayIdle: 10 * time.Second, jobStall: 30 * time.Second, jobDoneWait: 5 * time.Minute,
		echoHoldMost: 3 * time.Minute, hubSegmentsEvery: 15 * time.Second, hubTick: 250 * time.Millisecond,
		interimShown: 10 * time.Second, finalEvery: time.Minute, finalGapWait: 30 * time.Minute,
		finalRetries:            []time.Duration{time.Minute, 5 * time.Minute},
		finalUnreachableRetries: []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour},
		liveAhead:               30 * time.Second, replayAhead: time.Minute,
		askHold: 20 * time.Second, askLiveWait: 15 * time.Second, askTimeout: 3 * time.Minute, askLivePoll: 250 * time.Millisecond,
		askRoomPoll: time.Second, askRoomWait: 3 * time.Minute, // askRoomWait: as long as askTimeout
	}
}

// defaults are defaultTimings, made once: read only.
var defaults = defaultTimings()

// tm are the module's times now: the defaults with what the core's Host.Timings overrides at the moment (a test may
// change them between calls).
func (s *Recordings) tm() timings {
	if s.h == nil {
		return defaults
	}
	one, lists := s.h.Timings()
	if len(one) == 0 && len(lists) == 0 {
		return defaults
	}
	return timingsWith(one, lists, nil)
}

// byName are tm's times by their names, which Host.Timings uses.
func (tm *timings) byName() map[string]*time.Duration {
	return map[string]*time.Duration{
		"audioStartWait": &tm.audioStartWait, "audioAckEvery": &tm.audioAckEvery, "audioPingEvery": &tm.audioPingEvery,
		"audioEndWait": &tm.audioEndWait, "fallbackAfter": &tm.fallbackAfter, "jobRetry": &tm.jobRetry,
		"jobReadyWait": &tm.jobReadyWait, "jobIdle": &tm.jobIdle, "jobAwayIdle": &tm.jobAwayIdle, "jobStall": &tm.jobStall,
		"jobDoneWait": &tm.jobDoneWait, "echoHoldMost": &tm.echoHoldMost, "hubSegmentsEvery": &tm.hubSegmentsEvery,
		"hubTick": &tm.hubTick, "interimShown": &tm.interimShown, "finalEvery": &tm.finalEvery, "finalGapWait": &tm.finalGapWait,
		"liveAhead": &tm.liveAhead, "replayAhead": &tm.replayAhead, "askHold": &tm.askHold, "askLiveWait": &tm.askLiveWait,
		"askTimeout": &tm.askTimeout, "askLivePoll": &tm.askLivePoll, "askRoomPoll": &tm.askRoomPoll, "askRoomWait": &tm.askRoomWait,
	}
}

// timingsWith are the defaults with what one and lists override (by name; an unknown name is logged and ignored).
func timingsWith(one map[string]time.Duration, lists map[string][]time.Duration, logf func(string, ...any)) timings {
	tm := defaultTimings()
	by := tm.byName()
	for name, d := range one {
		if p, ok := by[name]; ok {
			*p = d
		} else if logf != nil {
			logf("recording: unknown timing %q", name)
		}
	}
	for name, l := range lists {
		switch name {
		case "finalRetries":
			tm.finalRetries = slices.Clone(l)
		case "finalUnreachableRetries":
			tm.finalUnreachableRetries = slices.Clone(l)
		default:
			if logf != nil {
				logf("recording: unknown timing list %q", name)
			}
		}
	}
	return tm
}
