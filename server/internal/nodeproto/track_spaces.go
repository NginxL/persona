package nodeproto

// TrackSpaces: assistant.prepare takes space, a track's own directory (tracks/<slug>): the node keeps its folders
// (inbox/, recordings/, .space/history/), writes its SPACE.md once from the text given, and writes CLAUDE.md and
// AGENTS.md as the instructions given followed by the SPACE.md it reads (docs/assistant/health.md "Tracks and
// kinds"). A node without it would write the instructions alone, without a SPACE.md: a track's conversation is not
// started on it.
const TrackSpaces = "track_spaces_v1"

func init() { Announced = append(Announced, TrackSpaces) }
