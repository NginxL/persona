package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

var transcriptionReadyInterval = 30 * time.Second

const transcriptionInfoTimeout = 3 * time.Second

func meetingTranscriptionDefaultOf(v Settings) string {
	if v.MeetingTranscriptionDefault != nil && *v.MeetingTranscriptionDefault == "hub" {
		return "hub"
	}
	return "device"
}

func meetingTranscriptionExtensionOf(v Settings) string {
	if v.MeetingTranscriptionExtension != nil {
		return *v.MeetingTranscriptionExtension
	}
	return ""
}

func meetingGlossaryOf(v Settings) []string {
	if v.MeetingGlossary != nil {
		if terms, err := cleanGlossary(*v.MeetingGlossary); err == nil {
			return terms
		}
	}
	return []string{}
}

// cleanGlossary keeps spellings, not instructions: one line per term, with the first spelling of a repeat kept.
func cleanGlossary(terms []string) ([]string, error) {
	out := make([]string, 0, min(len(terms), 200))
	for _, term := range terms {
		term = strings.Join(strings.Fields(term), " ")
		if utf8.RuneCountInString(term) > 40 {
			return nil, i18n.Err("assistant-meeting.bad_glossary")
		}
		if term == "" || slices.ContainsFunc(out, func(old string) bool { return strings.EqualFold(old, term) }) {
			continue
		}
		out = append(out, term)
		if len(out) > 200 {
			return nil, i18n.Err("assistant-meeting.bad_glossary")
		}
	}
	return out, nil
}

// readMeetingTranscriptionSettings decodes the raw fields so even wrong JSON types have their documented codes.
// A missing field leaves it unchanged; null is not a string or a list of strings.
func (s *Service) readMeetingTranscriptionSettings(v *Settings, mode, glossary, extension json.RawMessage) string {
	if mode != nil {
		if err := json.Unmarshal(mode, &v.MeetingTranscriptionDefault); err != nil || v.MeetingTranscriptionDefault == nil ||
			(*v.MeetingTranscriptionDefault != "device" && *v.MeetingTranscriptionDefault != "hub") {
			return "bad_transcription_mode"
		}
	}
	if glossary != nil {
		var terms []*string
		if err := json.Unmarshal(glossary, &terms); err != nil || terms == nil {
			return "bad_glossary"
		}
		values := make([]string, 0, len(terms))
		for _, term := range terms {
			if term == nil {
				return "bad_glossary"
			}
			values = append(values, *term)
		}
		clean, err := cleanGlossary(values)
		if err != nil {
			return "bad_glossary"
		}
		v.MeetingGlossary = &clean
	}
	if extension != nil {
		if err := json.Unmarshal(extension, &v.MeetingTranscriptionExtension); err != nil || v.MeetingTranscriptionExtension == nil {
			return "bad_transcription_extension"
		}
		v.MeetingTranscriptionInstall = nil // the body's never counts
		if ref := *v.MeetingTranscriptionExtension; ref != "" {
			parts := strings.Split(ref, "/")
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.TrimSpace(ref) != ref ||
				s.trans == nil || !s.trans.IsTranscriptionContribution(ref) {
				return "bad_transcription_extension"
			}
			// The installation named now: one uninstalled and installed again, or another extension installed with its
			// id, is not chosen until the owner names it again (transcriptionRefOf).
			install := s.trans.TranscriptionInstall(ref)
			v.MeetingTranscriptionInstall = &install
		}
	}
	return ""
}

// transcriptionRefOf is the contribution v chooses (meeting_transcription_extension: "" the built-in one) and whether
// it may transcribe: another extension's only while it is the installation the owner named (a choice stored before
// installations were pinned is taken as it is).
func (s *Service) transcriptionRefOf(v Settings) (ref string, ok bool) {
	ref = meetingTranscriptionExtensionOf(v)
	if ref == "" || v.MeetingTranscriptionInstall == nil {
		return ref, true
	}
	return ref, s.trans != nil && s.trans.TranscriptionInstall(ref) == *v.MeetingTranscriptionInstall
}

// errNotChosen: the extension the owner named was uninstalled (and another installed with its id): it is not
// chosen until the owner names it again.
var errNotChosen = errors.New("the transcription extension the owner named is not installed as it was named")

// transcriptionTarget is the contribution a job dials now (transcriptionRefOf with the settings in effect) and the
// choice it is (transcriptionChoice); errNotChosen when it may not transcribe.
func (s *Service) transcriptionTarget(ctx context.Context) (ref, choice string, err error) {
	v := s.settings(ctx)
	ref, ok := s.transcriptionRefOf(v)
	if !ok {
		return ref, s.transcriptionChoice(v), errNotChosen
	}
	return ref, s.transcriptionChoice(v), nil
}

// transcriptionReadiness caches whether the chosen contribution can transcribe (meeting_transcription_ready), never
// the user's choice. An answer is for one choice (choice, transcriptionChoice's): another choice, or other
// transcribers, drops it before another probe can publish an old result; a registry change or the periodic tick only
// has it probed again, and the answer stands meanwhile, so an unrelated settings change never makes a ready hub look
// unavailable. Slow probes do not hold up readers of the cache.
type transcriptionReadiness struct {
	ready     atomic.Bool
	probe     sync.Mutex
	mu        sync.Mutex
	at        time.Time // when the answer was published (zero: none stands)
	stale     bool      // probed again at the next look; the answer stands meanwhile
	choice    string    // the choice the answer is for (or will be, once at is set)
	gen       uint64
	wake      chan struct{}
	published chan struct{} // closed when an answer is published
}

// transcriptionChoice is what a readiness answer, and a live job, is for: the contribution v chooses, with the
// installation it is pinned to.
func (s *Service) transcriptionChoice(v Settings) string {
	choice := meetingTranscriptionExtensionOf(v) + "|"
	if v.MeetingTranscriptionInstall != nil {
		choice += *v.MeetingTranscriptionInstall
	}
	return choice
}

// transcriptionReadinessChanged: the extensions' registry changed: the answer is probed again (and stands meanwhile),
// and a live job of an extension no longer installed as the owner named it is ended.
func (s *Service) transcriptionReadinessChanged() {
	c := &s.transcriptionReady
	c.mu.Lock()
	c.stale = true
	c.wakeLocked()
	c.mu.Unlock()
	if s.rec != nil {
		s.rec.TranscriptionChanged() // never under the registry's callback
	}
}

// transcriptionReadinessReset: the transcribers changed (SetExtensions): no answer stands until one is probed.
func (s *Service) transcriptionReadinessReset() {
	c := &s.transcriptionReady
	c.mu.Lock()
	c.resetLocked(c.choice)
	c.mu.Unlock()
}

// transcriptionSettingsChanged: the settings changed; only another choice of contribution drops the answer.
func (s *Service) transcriptionSettingsChanged(ctx context.Context) {
	v, ok := s.settingsLastGood(ctx)
	if !ok {
		return
	}
	choice := s.transcriptionChoice(v)
	c := &s.transcriptionReady
	c.mu.Lock()
	if choice != c.choice {
		c.resetLocked(choice)
	}
	c.mu.Unlock()
	if s.rec != nil {
		s.rec.TranscriptionJobsCheck(ctx)
	}
}

// resetLocked drops the answer: none stands for choice until a probe publishes one (an older probe never does).
func (c *transcriptionReadiness) resetLocked(choice string) {
	c.gen++
	c.at, c.stale, c.choice = time.Time{}, false, choice
	c.ready.Store(false)
	c.wakeLocked()
}

func (c *transcriptionReadiness) wakeLocked() {
	if c.wake == nil {
		c.wake = make(chan struct{}, 1)
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// transcriptionReadyNow is meeting_transcription_ready, probed first when the answer is stale and no probe runs.
func (s *Service) transcriptionReadyNow() bool {
	s.refreshTranscriptionReady(s.runCtx())
	return s.transcriptionReady.ready.Load()
}

// transcriptionReadyCached is the answer as it stands, without a probe: the knowledge base asks it under its locks
// (the first upload naming the hub), after the upload's handler waited for one (transcriptionReadyAwait).
func (s *Service) transcriptionReadyCached() bool { return s.transcriptionReady.ready.Load() }

// transcriptionReadyAwait is transcriptionReadyNow for a mode decided now (a meeting upload): while no answer stands
// (none yet, or the choice just changed) it waits for the probe in flight, or its own, at most a probe's time.
func (s *Service) transcriptionReadyAwait() bool {
	c := &s.transcriptionReady
	ctx, cancel := context.WithTimeout(s.runCtx(), transcriptionInfoTimeout+time.Second)
	defer cancel()
	for {
		s.refreshTranscriptionReady(ctx)
		c.mu.Lock()
		stands := !c.at.IsZero()
		if c.published == nil {
			c.published = make(chan struct{})
		}
		published := c.published
		c.mu.Unlock()
		if stands || ctx.Err() != nil {
			return c.ready.Load()
		}
		select {
		case <-published:
		case <-ctx.Done():
		}
	}
}

func (s *Service) refreshTranscriptionReady(ctx context.Context) {
	c := &s.transcriptionReady
	if !c.probe.TryLock() {
		return
	}
	defer c.probe.Unlock()
	for ctx.Err() == nil {
		c.mu.Lock()
		at, stale, gen := c.at, c.stale, c.gen
		c.mu.Unlock()
		if !at.IsZero() && !stale && time.Since(at) < transcriptionReadyInterval {
			return
		}
		ready, choice := false, ""
		if s.trans != nil {
			probeCtx, cancel := context.WithTimeout(ctx, transcriptionInfoTimeout)
			v, ok := s.settingsLastGood(probeCtx)
			if ok {
				choice = s.transcriptionChoice(v)
				if ref, chosen := s.transcriptionRefOf(v); chosen {
					info, err := s.trans.TranscriptionInfo(probeCtx, ref)
					// meeting-minutes.md "Which one transcribes": protocol 1 with live and no setup.
					ready = err == nil && info.Protocol == extwire.TranscriptionProtocol && info.Setup == "" &&
						slices.Contains(info.Features, extwire.TranscriptionLive)
				}
			}
			cancel()
		}
		c.mu.Lock()
		if c.gen != gen {
			c.mu.Unlock()
			continue // the choice changed during the probe: never publish its old answer
		}
		c.ready.Store(ready)
		c.at, c.stale, c.choice = time.Now(), false, choice
		if c.published != nil {
			close(c.published)
		}
		c.published = make(chan struct{})
		c.mu.Unlock()
		return
	}
}

func (s *Service) transcriptionReadyLoop(ctx context.Context) {
	c := &s.transcriptionReady
	c.mu.Lock()
	if c.wake == nil {
		c.wake = make(chan struct{}, 1)
	}
	wake := c.wake
	c.mu.Unlock()
	ticker := time.NewTicker(transcriptionReadyInterval)
	defer ticker.Stop()
	s.refreshTranscriptionReady(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Probe on each tick even if the previous probe finished just after its tick; the answer stands meanwhile.
			c.mu.Lock()
			c.stale = true
			c.mu.Unlock()
		case <-wake:
		}
		s.refreshTranscriptionReady(ctx)
	}
}

func (s *Service) getMeetingTranscriptionDefault(ctx context.Context) (string, error) {
	out, err := json.Marshal(map[string]any{
		"meeting_transcription_default": meetingTranscriptionDefaultOf(s.settings(ctx)),
		"meeting_transcription_ready":   s.transcriptionReadyNow(),
	})
	return string(out), err
}

func (s *Service) setMeetingTranscriptionDefault(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var mode string
	if err := json.Unmarshal(raw, &mode); err != nil || (mode != "device" && mode != "hub") {
		return nil, i18n.Err("assistant-meeting.bad_transcription_mode")
	}
	if s.store() == nil {
		return nil, i18n.Err("assistant-settings_tool.meeting_transcription_no_database")
	}
	before := meetingTranscriptionDefaultOf(s.settings(ctx))
	if mode == before {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.meeting_transcription_unchanged")}, nil
	}
	ready := s.transcriptionReadyNow()
	return &ownChange{cardText: func(l i18n.Lang) string {
		key := "assistant-settings_tool.change_meeting_transcription_default"
		if !ready {
			key = "assistant-settings_tool.meeting_transcription_not_ready"
		}
		return i18n.T(l, key, i18n.Args{
			"before": i18n.T(l, "assistant-settings_tool.meeting_transcription_"+before),
			"after":  i18n.T(l, "assistant-settings_tool.meeting_transcription_"+mode),
		})
	}, apply: func(ctx context.Context) (string, error) {
		// The database may have gone away while the approval card was open.
		if s.store() == nil {
			return "", i18n.Err("assistant-settings_tool.meeting_transcription_no_database")
		}
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MeetingTranscriptionDefault = &mode; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.meeting_transcription_applied", i18n.Args{
			"mode": i18n.T(s.lang(ctx), "assistant-settings_tool.meeting_transcription_"+mode),
		}), nil
	}}, nil
}
