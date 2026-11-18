package db

import (
	"context"
	"time"
)

// ---- model_usage (docs/assistant/model-config.md "Counting") ----

// ModelUsageKeep is how many days of model usage are kept.
const ModelUsageKeep = 60

// ModelCall is one finished model call of a purpose and what it spent.
type ModelCall struct {
	Purpose, Route, Provider, Model string
	Failed                          bool
	InputTokens                     int64
	OutputTokens                    int64
	CacheReadInputTokens            int64
	CacheCreationInputTokens        int64
}

// ModelUsage is what a purpose's calls added up to.
type ModelUsage struct {
	Calls                    int64 `json:"calls"`
	Failures                 int64 `json:"failures"`
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// AddModelCall adds c to at's UTC day and drops the days older than ModelUsageKeep, in one statement.
func (d *DB) AddModelCall(ctx context.Context, at time.Time, c ModelCall) error {
	day := utcDay(at)
	failed := 0
	if c.Failed {
		failed = 1
	}
	_, err := d.pool.Exec(ctx, `WITH gone AS (DELETE FROM model_usage WHERE day < $1)
		INSERT INTO model_usage (day, purpose, route, provider, model, calls, failures, input_tokens, output_tokens,
			cache_read_input_tokens, cache_creation_input_tokens)
		VALUES ($2, $3, $4, $5, $6, 1, $7, $8, $9, $10, $11)
		ON CONFLICT (day, purpose, route, provider, model) DO UPDATE SET calls = model_usage.calls + 1,
			failures = model_usage.failures + EXCLUDED.failures,
			input_tokens = model_usage.input_tokens + EXCLUDED.input_tokens,
			output_tokens = model_usage.output_tokens + EXCLUDED.output_tokens,
			cache_read_input_tokens = model_usage.cache_read_input_tokens + EXCLUDED.cache_read_input_tokens,
			cache_creation_input_tokens = model_usage.cache_creation_input_tokens + EXCLUDED.cache_creation_input_tokens`,
		day.AddDate(0, 0, -ModelUsageKeep), day, c.Purpose, c.Route, c.Provider, c.Model, failed,
		c.InputTokens, c.OutputTokens, c.CacheReadInputTokens, c.CacheCreationInputTokens)
	return err
}

// ModelUsageByPurpose adds up each purpose's calls over the last days UTC days up to now's (today included).
func (d *DB) ModelUsageByPurpose(ctx context.Context, now time.Time, days int) (map[string]ModelUsage, error) {
	rows, err := d.pool.Query(ctx, `SELECT purpose, sum(calls)::bigint, sum(failures)::bigint, sum(input_tokens)::bigint,
		sum(output_tokens)::bigint, sum(cache_read_input_tokens)::bigint, sum(cache_creation_input_tokens)::bigint FROM model_usage WHERE day >= $1 GROUP BY purpose`,
		utcDay(now).AddDate(0, 0, 1-days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ModelUsage{}
	for rows.Next() {
		var purpose string
		var u ModelUsage
		if err := rows.Scan(&purpose, &u.Calls, &u.Failures, &u.InputTokens, &u.OutputTokens, &u.CacheReadInputTokens,
			&u.CacheCreationInputTokens); err != nil {
			return nil, err
		}
		out[purpose] = u
	}
	return out, rows.Err()
}
