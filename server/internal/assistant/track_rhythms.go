package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A track's rhythms (#13472, docs/assistant/health.md "Tracks and kinds"): when a track is created, each rhythm of its
// effective config (its kind's, or its own override's) becomes one scheduled task of the track: it runs in the track
// (Task.Space), on its cron, switched on or off as the rhythm says. From then on they are ordinary scheduled tasks the
// user changes or deletes as any other; a later change of the kind's rhythms reaches only tracks created after it.

// trackRhythmCreatedBy marks a scheduled task made from a track's rhythm (Task.CreatedBy).
const trackRhythmCreatedBy = "track"

// rhythmTask is the scheduled task rhythm r gives track slug: a cron task in the track, a new conversation each run
// for topic_policy new and one continued for continue.
func rhythmTask(slug string, r assistantwire.TrackRhythm, now time.Time) Task {
	t := Task{ID: "rt_" + randomHex(4), Title: r.Title, Repeat: repeatCron, Cron: r.Cron, Enabled: r.Enabled, Space: slug,
		FreshEachRun: r.TopicPolicy == "new", CreatedBy: trackRhythmCreatedBy, CreatedAt: now.UTC()}
	t.setLiteralPrompt(r.Prompt)
	return t
}

// createTrackRhythms makes track e's scheduled tasks from its rhythms, all in one save: none is made when one of them
// is not a valid task. A rhythm's own time zone is not kept yet: a scheduled task runs in the user's (logged when they
// differ).
func (s *Service) createTrackRhythms(ctx context.Context, e trackEntry) error {
	if len(e.Config.Rhythms) == 0 {
		return nil
	}
	loc, now := s.location(ctx), time.Now()
	made := make([]Task, 0, len(e.Config.Rhythms))
	for _, r := range e.Config.Rhythms {
		if r.TZ != "" && r.TZ != loc.String() {
			s.logf("tracks: %q: rhythm %q asks for %s; it runs in the user's time zone (%s)", e.Slug, r.Title, r.TZ, loc)
		}
		t := rhythmTask(e.Slug, r, now)
		if err := checkTask(&t, now); err != nil {
			return err
		}
		rearm(Task{}, &t, now)
		made = append(made, t)
	}
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return err
	}
	tasks := append(slices.Clone(sc.tasks), made...)
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		return err
	}
	s.broadcastSchedule()
	return nil
}
