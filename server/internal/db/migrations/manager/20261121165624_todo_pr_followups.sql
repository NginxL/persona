-- Post-closure PR follow-ups (#12371). When a to-do is accepted while its own delivery PRs are still open, a
-- lightweight subscription keeps watching them without reopening the to-do. The PR sweep polls these rows
-- independently of the to-do's DoneAt, notifies the original supervisor conversation of conflicts, current-head
-- change requests and terminal states, then removes the subscription. All state lives in Postgres, never in a JSON
-- setting.
CREATE TABLE todo_pr_followups (
    followup_id text PRIMARY KEY,
    todo_id text NOT NULL REFERENCES todos (id) ON DELETE CASCADE,
    pr_url text NOT NULL,
    -- supervisor is the conversation that reviewed the to-do; origin is the to-do's own dispatch conversation.
    supervisor text NOT NULL,
    origin text NOT NULL DEFAULT '',
    -- Last observed forge facts; empty state means the PR has not been polled yet.
    head_oid text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT '',
    review text NOT NULL DEFAULT '',
    changes_requested_at timestamptz,
    last_observed_at timestamptz,
    created_at timestamptz NOT NULL,
    -- Deduplication keys so one head's conflict or one current-head review wakes the supervisor only once.
    notified_conflict_head text NOT NULL DEFAULT '',
    notified_review_head text NOT NULL DEFAULT '',
    UNIQUE (todo_id, pr_url)
);

-- The sweep reads every active subscription each pass; keep the common lookup indexed.
CREATE INDEX todo_pr_followups_active ON todo_pr_followups (last_observed_at);
