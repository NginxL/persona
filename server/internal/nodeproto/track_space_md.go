package nodeproto

// TrackSpaceMD: the node answers assistant.space_md, a track's SPACE.md read and written in the track's own directory
// (TrackSpaces): the whole file the user edits in the apps, kept when it changes a block Persona manages, and the
// state block alone, which the track's conversations replace (space_state_update), each write after a backup in
// .space/history/ (docs/assistant/health.md "Tracks and kinds"). A node without it does not know the method: the
// manager answers that the computer needs an update.
const TrackSpaceMD = "track_space_md_v1"

func init() { Announced = append(Announced, TrackSpaceMD) }
