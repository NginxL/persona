ALTER TABLE conversation_histories ADD COLUMN summary bytea NOT NULL DEFAULT '' CHECK (octet_length(summary) <= 65536);
ALTER TABLE conversation_histories ADD COLUMN summary_known boolean NOT NULL DEFAULT false;
