-- The recordings the phone saved into a track instead of the knowledge base (#13501, docs/assistant/health.md "Tracks
-- and kinds"): the transcript is a file in the track's recordings/ on the assistant's machine; this row is what the
-- manager needs to answer the phone's sync (its revision, its tombstone) and to remove the file when it is deleted or
-- past recording_days.
--   id: the recording's UUID, lowercase, as the phone names it.
--   slug: the track it went to.
--   file_name: the transcript's name in recordings/, kept from the first upload so a later one rewrites the same file.
--   deleted_at: the tombstone (a late upload is refused until the retention window ends); file_removed says the node
--   removed the file (a deletion while the computer was away is retried).
CREATE TABLE IF NOT EXISTS assistant_space_recordings (
    id           text PRIMARY KEY,
    slug         text NOT NULL,
    file_name    text NOT NULL,
    revision     bigint NOT NULL,
    started_at   timestamptz NOT NULL,
    complete     boolean NOT NULL DEFAULT false,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    file_removed boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS assistant_space_recordings_started ON assistant_space_recordings (started_at);
