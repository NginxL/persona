package nodeproto

// TrackRecordings: the node answers assistant.space_recording, writing a recording's transcript into a track's
// recordings/ (or removing it) on the assistant's machine (#13501, docs/assistant/health.md "Tracks and kinds"). A
// node without it has no such method: the manager does not save a recording into a track there.
const TrackRecordings = "track_recordings_v1"

func init() { Announced = append(Announced, TrackRecordings) }
