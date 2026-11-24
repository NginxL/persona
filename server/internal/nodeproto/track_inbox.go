package nodeproto

// TrackInbox: the node answers assistant.track_inbox_write, one file written into a track's inbox/ on the assistant's
// machine (quick capture, docs/assistant/health.md "Quick capture"): the directory must be that track's own
// (tracks/<slug>) and a real folder, the name a plain file name, the write atomic and never through a link. A node
// without it is not asked: captures meant for a track stay in the list.
const TrackInbox = "track_inbox_v1"

func init() { Announced = append(Announced, TrackInbox) }
