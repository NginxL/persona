package assistant

import "context"

const (
	defaultIdleAutoCompactMinutes = 30
	minIdleAutoCompactMinutes     = 1
	maxIdleAutoCompactMinutes     = 1440
	defaultIdleAutoCompactTokens  = 300000
	minIdleAutoCompactTokens      = 10000
	maxIdleAutoCompactTokens      = 10000000
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
