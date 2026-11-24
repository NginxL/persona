package assistantwire

// A track's SPACE.md, read and written on the assistant's machine (#13472, docs/assistant/health.md "Tracks and
// kinds"): the apps edit the part the user writes; the track's own conversations replace the state block Persona keeps
// (space_state_update). The node answers either through DeviceAssistantSpaceMD (capability nodeproto.TrackSpaceMD).

// trackSpaceMDRoutes read and write a track's SPACE.md: full-access clients only, a write only the user's.
var trackSpaceMDRoutes = []string{
	"GET /api/v1/assistant/tracks/{slug}/space-md",
	"PUT /api/v1/assistant/tracks/{slug}/space-md",
}

func init() { Routes = append(Routes, trackSpaceMDRoutes...) }

// DeviceAssistantSpaceMD: SpaceMDCall → SpaceMD, a track's SPACE.md on the assistant's machine.
const DeviceAssistantSpaceMD = "assistant.space_md"

// SpaceMDStateMax is the most bytes a SPACE.md's state block may hold (space_state_update).
const SpaceMDStateMax = 2048

// What a SpaceMDCall does.
const (
	// SpaceMDGet reads the SPACE.md.
	SpaceMDGet = "get"
	// SpaceMDPutHuman replaces it whole with Content, when it is still the one BaseDigest names and every managed
	// block and marker is left as it was, byte for byte.
	SpaceMDPutHuman = "put_human"
	// SpaceMDPutState replaces the inner text of its state block with State, and nothing else.
	SpaceMDPutState = "put_state"
)

// Why a SpaceMDCall wrote nothing (SpaceMD.Refused).
const (
	// SpaceMDStale: the SPACE.md is no longer the one BaseDigest names (SpaceMD is the current one).
	SpaceMDStale = "stale"
	// SpaceMDManaged: Content changes a managed block or its markers, or State holds a marker.
	SpaceMDManaged = "managed"
	// SpaceMDTooLarge: Content is over 64 KB, or State over SpaceMDStateMax.
	SpaceMDTooLarge = "too_large"
	// SpaceMDNoState: the SPACE.md has no single state block (the user took it out).
	SpaceMDNoState = "no_state_block"
	// SpaceMDMissing: the track's directory or its SPACE.md is not there.
	SpaceMDMissing = "missing"
)

// SpaceMDCall is DeviceAssistantSpaceMD's params.
type SpaceMDCall struct {
	Op string `json:"op"`
	// Dir is the track's directory relative to the machine's home, as assistant.prepare's dir; Slug its track's.
	Dir  string `json:"dir"`
	Slug string `json:"slug"`
	// Content and BaseDigest are SpaceMDPutHuman's; State is SpaceMDPutState's.
	Content    string `json:"content,omitempty"`
	BaseDigest string `json:"base_digest,omitempty"`
	State      string `json:"state,omitempty"`
}

// SpaceMD is a track's SPACE.md as it is now (after a write, the one written): the routes' answer and
// DeviceAssistantSpaceMD's.
type SpaceMD struct {
	Content string `json:"content"`
	// Digest is the hex SHA-256 of Content: a write names it as its base_digest.
	Digest string `json:"digest"`
	// State is the inner text of the state block, "" when it is empty or missing.
	State string `json:"state"`
	// Refused says why nothing was written (SpaceMDStale…); never in a route's answer.
	Refused string `json:"refused,omitempty"`
}

// SpaceMDPut is PUT /api/v1/assistant/tracks/{slug}/space-md's body.
type SpaceMDPut struct {
	Content    string `json:"content"`
	BaseDigest string `json:"base_digest"`
}
