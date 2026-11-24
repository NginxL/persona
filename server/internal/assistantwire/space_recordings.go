package assistantwire

// DeviceAssistantSpaceRecording (#13501, nodeproto.TrackRecordings): {dir, slug, name, content?, remove?} → {path?,
// removed?}. dir is a track's directory relative to the home (as on assistant.prepare), slug its track, name the
// transcript's file name in recordings/; with content the node writes it there (atomically, 0600), with remove it
// removes the file (none there is no error).
const DeviceAssistantSpaceRecording = "assistant.space_recording"
