package assistant

import (
	"cmp"
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/db"
)

// modelUsageDays: Settings › Models shows each purpose's calls of the last 7 UTC days, today's included
// (docs/assistant/model-config.md "Counting").
const modelUsageDays = 7

// modelUsageStore keeps the purposes' model calls added up by day (db.DB); without it they are only logged.
type modelUsageStore interface {
	AddModelCall(ctx context.Context, at time.Time, c db.ModelCall) error
	ModelUsageByPurpose(ctx context.Context, now time.Time, days int) (map[string]db.ModelUsage, error)
}

func (s *Service) modelUsageStore() modelUsageStore {
	if s.store == nil {
		return nil
	}
	st, _ := s.store().(modelUsageStore)
	return st
}

// countedClient is c's client, reporting each finished call of purpose on provider: a model_usage log line, and the
// call added to the day's counts. A call that never ran (paused, nothing configured) or was stopped by its caller
// (shutdown, an abandoned request; as kbCountedModel does) is neither.
func (s *Service) countedClient(purpose, operation, provider string, c ai.Config) *ai.Client {
	route := "api"
	if c.RunLauncher != "" {
		route = "cli"
	} else if c.ViaMachine != "" {
		route = "api_via_node"
	}
	return s.aiClient(c).WithResult(func(reply ai.Reply, err error) {
		u := reply.Usage
		paused, _ := modelPaused(err)
		if u == (ai.Usage{}) && (paused || ai.IsNotConfigured(err) || errors.Is(err, context.Canceled)) {
			return
		}
		result := "ok"
		if err != nil {
			result = "error"
		}
		model := cmp.Or(reply.Model, c.Model)
		s.logf("model_usage purpose=%q operation=%q route=%s provider=%q model=%q result=%s usage_reported=%t input_tokens=%d output_tokens=%d cache_read_input_tokens=%d cache_creation_input_tokens=%d",
			purpose, operation, route, provider, model, result, u != (ai.Usage{}),
			u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
		st := s.modelUsageStore()
		if st == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if werr := st.AddModelCall(ctx, time.Now(), db.ModelCall{Purpose: purpose, Route: route, Provider: provider, Model: model,
			Failed: err != nil, InputTokens: int64(u.InputTokens), OutputTokens: int64(u.OutputTokens),
			CacheReadInputTokens: int64(u.CacheReadInputTokens), CacheCreationInputTokens: int64(u.CacheCreationInputTokens)}); werr != nil {
			s.logf("model_usage: count: %v", werr)
		}
	})
}

// addModelUsage gives each listed purpose what its calls of the last modelUsageDays added up to: its own, and those of
// the uses under it that are not listed themselves (Information processing's titles, translation…). A purpose without
// calls gets none.
func (s *Service) addModelUsage(ctx context.Context, purposes []ModelPurpose) {
	st := s.modelUsageStore()
	if st == nil {
		return
	}
	byPurpose, err := st.ModelUsageByPurpose(ctx, time.Now(), modelUsageDays)
	if err != nil {
		s.logf("model_usage: read: %v", err)
		return
	}
	at := map[string]int{}
	for i, p := range purposes {
		at[p.ID] = i
	}
	for id, u := range byPurpose {
		i, ok := at[id]
		if !ok {
			i, ok = at[modelCategoryID(id)]
		}
		if !ok || u.Calls == 0 {
			continue
		}
		sum := purposes[i].Usage
		if sum == nil {
			sum = &db.ModelUsage{}
			purposes[i].Usage = sum
		}
		sum.Calls += u.Calls
		sum.Failures += u.Failures
		sum.InputTokens += u.InputTokens
		sum.OutputTokens += u.OutputTokens
		sum.CacheReadInputTokens += u.CacheReadInputTokens
		sum.CacheCreationInputTokens += u.CacheCreationInputTokens
	}
}
