-- Model usage by purpose (docs/assistant/model-config.md "Counting", #10498): the manager's own model calls (purposes on
-- any route, extensions' model.complete) added up per UTC day, purpose, route, provider and model, so Settings › Models
-- can show each purpose's last 7 days. One row per day and key, never per call; rows older than 60 days are dropped as
-- new calls come in.
CREATE TABLE IF NOT EXISTS model_usage (
    day                         date   NOT NULL,
    purpose                     text   NOT NULL,
    route                       text   NOT NULL,
    provider                    text   NOT NULL,
    model                       text   NOT NULL,
    calls                       bigint NOT NULL DEFAULT 0,
    failures                    bigint NOT NULL DEFAULT 0,
    input_tokens                bigint NOT NULL DEFAULT 0,
    output_tokens               bigint NOT NULL DEFAULT 0,
    cache_read_input_tokens     bigint NOT NULL DEFAULT 0,
    cache_creation_input_tokens bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (day, purpose, route, provider, model)
);
