package assistant

import (
	"cmp"
	"context"
	"slices"
)

// Keeping a direction's sessions listed however quiet they are (#11515). A computer lists a quiet session only for a
// while (node sessions_offline.go: a transcript written in the last seven days, and only the newest of those), and the
// automatic archive takes a closed one off the lists after archive_closed_days (agent_archive_auto.go). Either way a
// session placed under a direction would read not_listed for being quiet, and could not be moved to another one. So the
// profile's keep (keptSessions, node.configure keep_sessions) also takes the sessions placed under the current
// directions, and the automatic archive leaves the ones the owner filed under one.

// directionKeep are the sessions placed under the current directions (assistant.leaders without the main one) that
// their computers keep listing: placed as the read places them (directionPlacements), from the manager's own records
// only, as the look is frozen and asks no computer. Per direction, the owner's filings come first, newest filing
// first, then the conversations its leader conversation opened, newest first, and at most directionSessionsPerDirection
// of them: a leader that opens many conversations never pushes the owner's filings out. Sorted by computer and session,
// so an unchanged list is not sent again (Devices.SetKeep). An error when the book, the recorded openers, the deletion
// record or the directions could not be read: the profile then leaves keep out, and the nodes keep what they list.
func (s *Service) directionKeep(ctx context.Context, cfg Settings) ([]SessionRef, error) {
	current := func(id string) bool { return id != "" && id != cfg.Main && slices.Contains(cfg.Leaders, id) }
	if !slices.ContainsFunc(cfg.Leaders, current) {
		return nil, nil
	}
	rules, err := s.directionRulesNow(ctx, cfg)
	if err != nil {
		return nil, err
	}
	records, err := s.directionSessionRecords(ctx)
	if err != nil {
		return nil, err
	}
	look := newDirectionLook(s, nil)
	look.frozen = true
	placed, err := s.directionPlacements(ctx, rules, records, current, look)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(placed, func(a, b directionCandidate) int {
		return cmp.Or(cmp.Compare(a.direction, b.direction),
			cmp.Compare(cond(a.source == directionSourceOwner, 0, 1), cond(b.source == directionSourceOwner, 0, 1)),
			b.at.Compare(a.at), directionKeyOrder([2]string{a.machine, a.session}, [2]string{b.machine, b.session}))
	})
	var out []SessionRef
	held := map[string]int{}
	for _, c := range placed {
		if held[c.direction] < directionSessionsPerDirection {
			held[c.direction]++
			out = append(out, SessionRef{MachineID: c.machine, SessionID: c.session})
		}
	}
	slices.SortFunc(out, func(a, b SessionRef) int {
		return directionKeyOrder([2]string{a.MachineID, a.SessionID}, [2]string{b.MachineID, b.SessionID})
	})
	return out, nil
}

// directionFiledSessions are the sessions the owner filed under a current direction (the book's live entries that name
// one), by canonical computer and session: the automatic archive leaves them, as it leaves an open to-do's. An error
// when the book could not be read.
func (s *Service) directionFiledSessions(ctx context.Context, cfg Settings) (map[[2]string]bool, error) {
	current := func(id string) bool { return id != "" && id != cfg.Main && slices.Contains(cfg.Leaders, id) }
	if !slices.ContainsFunc(cfg.Leaders, current) {
		return nil, nil
	}
	records, err := s.directionSessionRecords(ctx)
	if err != nil {
		return nil, err
	}
	out := map[[2]string]bool{}
	for k, e := range s.directionEntries(records) {
		if e.live() && current(e.Direction) {
			out[k] = true
		}
	}
	return out, nil
}
