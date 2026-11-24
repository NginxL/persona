-- When the supervision watch handed a card-less pending_review task back to its
-- supervisor once (a review whose owner question exists only in chat, with no
-- ask or pending interaction, supervise_cardless.go): the time it was told, so a
-- restart does not tell the same review round again. NULL when the current
-- review round was never handed back this way; leaving the state clears it.
ALTER TABLE todo_supervision ADD COLUMN IF NOT EXISTS cardless_told_at timestamptz;
