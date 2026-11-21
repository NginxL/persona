package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

const (
	defaultIdleAutoCompactMinutes = 30
	minIdleAutoCompactMinutes     = 1
	maxIdleAutoCompactMinutes     = 1440
	defaultIdleAutoCompactTokens  = 300000
	minIdleAutoCompactTokens      = 10000
	maxIdleAutoCompactTokens      = 10000000

	// The main conversation's idle process is let go this long after its last activity (#12008). Unset keeps the
	// node's own 20 minutes; the range stays below the prompt cache's one-hour lifetime (assistantwire).
	defaultIdleReleaseMinutes = 20
	minIdleReleaseMinutes     = assistantwire.MinReleaseMinutes
	maxIdleReleaseMinutes     = assistantwire.MaxReleaseMinutes
)

func (s *Service) idleAutoCompact(ctx context.Context) bool {
	v := s.settings(ctx).IdleAutoCompact
	return v == nil || *v
}

func (s *Service) idleAutoCompactMinutes(ctx context.Context) int {
	v := s.settings(ctx).IdleAutoCompactMinutes
	if v == nil || *v < minIdleAutoCompactMinutes || *v > maxIdleAutoCompactMinutes {
		return defaultIdleAutoCompactMinutes
	}
	return *v
}

func (s *Service) idleAutoCompactTokens(ctx context.Context) int {
	v := s.settings(ctx).IdleAutoCompactTokens
	if v == nil || *v < minIdleAutoCompactTokens || *v > maxIdleAutoCompactTokens {
		return defaultIdleAutoCompactTokens
	}
	return *v
}

func (s *Service) idleReleaseMinutes(ctx context.Context) int {
	v := s.settings(ctx).IdleReleaseMinutes
	if v == nil || *v < minIdleReleaseMinutes || *v > maxIdleReleaseMinutes {
		return defaultIdleReleaseMinutes
	}
	return *v
}

// idleCompactionPolicy is what the main conversation's sends carry (idle_compaction). The release fields go only once
// saved, so a hub where neither was set keeps the node's own release rule: 20 minutes, and compaction first at 75% of
// the window. A saved idle_auto_compact_tokens is also the release-time threshold, whatever idle_auto_compact says:
// that switch turns off only the compaction before unattended messages, and compaction before a release ran before it.
func (s *Service) idleCompactionPolicy(ctx context.Context) assistantwire.IdleCompaction {
	cfg := s.settings(ctx)
	c := assistantwire.IdleCompaction{
		Enabled: s.idleAutoCompact(ctx), IdleMinutes: s.idleAutoCompactMinutes(ctx), ContextTokens: s.idleAutoCompactTokens(ctx),
	}
	if v := cfg.IdleReleaseMinutes; v != nil && assistantwire.ReleaseMinutesValid(*v) {
		c.ReleaseMinutes = *v
	}
	if v := cfg.IdleAutoCompactTokens; v != nil && *v >= minIdleAutoCompactTokens && *v <= maxIdleAutoCompactTokens {
		c.ReleaseTokens = *v
	}
	return c
}
