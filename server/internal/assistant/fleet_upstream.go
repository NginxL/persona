package assistant

import (
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modelretry"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- consecutive upstream 5xx / account-pool exhaustion visibility (#13372) ----
//
// A single non-pool-exhaustion 5xx failure stays unmarked so normal transient retries run quietly. Explicit upstream
// account-pool exhaustion (modelretry.AccountExhausted) marks TurnErrorUpstreamUnavailable immediately; otherwise >= 2
// consecutive failed turns of upstream 5xx errors with no substantive assistant work in between mark it. Nodes compute
// the authoritative streak from Claude Code and Codex transcripts (surviving hub restarts); the hub keeps a bounded
// fallback book over observed stopped rows for rolling upgrades and non-transcript sessions.

const (
	upstreamStallAfter = 2
	upstreamStreakKeep = 48 * time.Hour
)

type upstreamStreak struct {
	rowAt
	turnErr    string
	count      int
	sawWorking bool
	at         time.Time
}

func (u upstreamStreak) sameErrorText(c proto.Session) bool {
	if u.turnErr != "" {
		if cur := strings.TrimSpace(c.TurnError); cur != "" {
			return cur == u.turnErr
		}
	}
	return !u.textKnown || c.LastText == u.text
}

// holdsRow reports whether c is still stopped on the same failed turn u recorded. Trailing metadata appends while idle
// (ai-title, last-prompt, cost-state, #5967) grow Rev without starting a new turn or moving UpdatedAt: when no working
// turn ran since and UpdatedAt and the error text still match within quietSlack, the row is the same turn.
func (u upstreamStreak) holdsRow(c proto.Session) bool {
	if u.rev != 0 && c.Rev != 0 && c.Rev == u.rev && !u.sawWorking {
		return true
	}
	if u.sawWorking {
		return false
	}
	if c.UpdatedAt.Before(u.seen) || c.UpdatedAt.After(u.seen.Add(quietSlack)) {
		return false
	}
	return u.sameErrorText(c)
}

// upstreamServerFailure returns the turn's upstream 5xx/pool-exhaustion error text when c carries one, and false when
// the turn succeeded or failed for another reason (auth, sign-in, 429/usage limit, tool search, unrecoverable, silent).
func upstreamServerFailure(c proto.Session) (string, bool) {
	if proto.SilentTurnKind(c.TurnErrorKind) || c.TurnErrorKind == proto.TurnErrorUsageLimit ||
		c.TurnErrorKind == proto.TurnErrorUnrecoverable {
		return "", false
	}
	text, _ := latestTurnError(c)
	if text == "" || proto.UnrecoverableTurnErrorCode(text) != "" {
		return "", false
	}
	switch turnErrorCode(text) {
	case proto.TurnErrorNotLoggedIn, proto.TurnErrorAuthFailed, proto.TurnErrorRateLimited,
		proto.TurnErrorToolSearchUnsupported, proto.TurnErrorModelFallbackUnavailable:
		return "", false
	}
	if !modelretry.UpstreamServerError(text) {
		return "", false
	}
	return text, true
}

// noteUpstreamRow updates the hub's consecutive upstream-5xx streak for a raw session row from machine. Working rows
// mark that a new turn ran so the next stopped failure counts even inside quietSlack; offline rows leave the count as is.
func (s *Service) noteUpstreamRow(machine string, c proto.Session, now time.Time) {
	ref := [2]string{machine, c.SessionID}
	if c.Status == proto.StatusWorking {
		s.fleet.mu.Lock()
		if prev, had := s.fleet.upstream[ref]; had && !prev.sawWorking {
			prev.sawWorking = true
			prev.at = now
			s.fleet.upstream[ref] = prev
		}
		s.fleet.mu.Unlock()
		return
	}
	if c.Status != proto.StatusDone && c.Status != proto.StatusIdle {
		return
	}
	text, ok := upstreamServerFailure(c)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if !ok {
		delete(s.fleet.upstream, ref)
		return
	}
	if s.fleet.upstream == nil {
		s.fleet.upstream = map[[2]string]upstreamStreak{}
	}
	for k, v := range s.fleet.upstream {
		if now.Sub(v.at) > upstreamStreakKeep {
			delete(s.fleet.upstream, k)
		}
	}
	prev, had := s.fleet.upstream[ref]
	if had && prev.holdsRow(c) {
		prev.rev = c.Rev
		prev.at = now
		s.fleet.upstream[ref] = prev
		return
	}
	if c.TurnErrorAfterWork && !modelretry.AccountExhausted(text) {
		delete(s.fleet.upstream, ref)
		return
	}
	n := 1
	if had {
		n = prev.count + 1
	}
	s.fleet.upstream[ref] = upstreamStreak{
		rowAt:   rowOf(c),
		turnErr: strings.TrimSpace(c.TurnError),
		count:   n,
		at:      now,
	}
}

func (s *Service) pruneGoneUpstream(machine string, list []proto.Session) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if len(s.fleet.upstream) == 0 {
		return
	}
	hasForMachine := false
	for k := range s.fleet.upstream {
		if k[0] == machine {
			hasForMachine = true
			break
		}
	}
	if !hasForMachine {
		return
	}
	listed := make(map[string]bool, len(list))
	for _, c := range list {
		listed[c.SessionID] = true
	}
	now := time.Now()
	for k, v := range s.fleet.upstream {
		if now.Sub(v.at) > upstreamStreakKeep || (k[0] == machine && !listed[k[1]]) {
			delete(s.fleet.upstream, k)
		}
	}
}

func (s *Service) upstreamMarks(machine string) map[string]upstreamStreak {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if len(s.fleet.upstream) == 0 {
		return nil
	}
	var out map[string]upstreamStreak
	now := time.Now()
	for k, v := range s.fleet.upstream {
		if now.Sub(v.at) > upstreamStreakKeep {
			delete(s.fleet.upstream, k)
			continue
		}
		if k[0] == machine {
			if out == nil {
				out = map[string]upstreamStreak{}
			}
			out[k[1]] = v
		}
	}
	return out
}

func putUpstreamTurn(c *proto.Session, marks map[string]upstreamStreak) {
	if c.TurnError == "" || (c.State != proto.StateUnread && c.State != proto.StateIdle) {
		return
	}
	u, ok := marks[c.SessionID]
	if !ok || u.count < upstreamStallAfter || !u.holdsRow(*c) {
		return
	}
	if c.TurnErrorCode == "" || (c.TurnErrorCode == proto.TurnErrorModelRoute && modelretry.UpstreamServerError(c.TurnError)) {
		c.TurnErrorCode = proto.TurnErrorUpstreamUnavailable
	}
}

func (s *Service) upstreamAttempts(machine string, c proto.Session) int {
	s.fleet.mu.Lock()
	u, ok := s.fleet.upstream[[2]string{machine, c.SessionID}]
	s.fleet.mu.Unlock()
	if ok && u.holdsRow(c) && u.count >= upstreamStallAfter {
		return u.count
	}
	if c.TurnErrorCode == proto.TurnErrorUpstreamUnavailable && !modelretry.AccountExhausted(c.TurnError) {
		return upstreamStallAfter
	}
	return 1
}

func (s *Service) upstreamSnapshotLine(l i18n.Lang, machine string, c proto.Session) string {
	retrying := (c.Work != nil && c.Work.WakeAt != nil) || c.Loop != nil || c.TurnErrorResumes
	errText := clip(oneLine(c.TurnError), 120)
	if n := s.upstreamAttempts(machine, c); n >= upstreamStallAfter {
		if retrying {
			return workT(l, "upstream_unavailable_streak_retrying", i18n.Args{"n": n, "error": errText})
		}
		return workT(l, "upstream_unavailable_streak_stopped", i18n.Args{"n": n, "error": errText})
	}
	if retrying {
		return workT(l, "upstream_unavailable_retrying", i18n.Args{"error": errText})
	}
	return workT(l, "upstream_unavailable_stopped", i18n.Args{"error": errText})
}
