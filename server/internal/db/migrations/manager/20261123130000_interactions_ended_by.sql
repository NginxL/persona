-- Who or what ended a card: only the user's own answer on a device is their decision; a card that expired, was
-- withdrawn or cleared, or that someone else answered for them is not, and the history says which it was.
--   ended_by: 'device' (the user answered it on a device: the apps' POST /api/v1/interactions/{id}/resolve),
--   'extension' (an extension answered it through that route), 'expired' (its time ran out unanswered), 'withdrawn'
--   (the asker went away first: its call ended, its hook disconnected), 'cleared' (a newer card replaced it, or a restart
--   cleared it), 'assistant' / 'default' / 'terminal' (answered for the user: proto.Resolution.AnsweredBy),
--   'remembered' (a kept "Always allow"), 'authorization' (a standing authorization); '' when it is not known (a card
--   ended before this column, or by a way that does not say).
ALTER TABLE interactions ADD COLUMN IF NOT EXISTS ended_by text NOT NULL DEFAULT '';
