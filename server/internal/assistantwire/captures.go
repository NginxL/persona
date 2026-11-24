package assistantwire

import "time"

// captureRoutes are quick capture's (#13502, docs/assistant/health.md "Quick capture"): what the user jots down from
// the share sheet, the watch, Siri or the web goes into a list of its own, never into a conversation; only a
// full-access client (the user's own devices) adds to it or reads it.
var captureRoutes = []string{
	"GET /api/v1/assistant/captures",
	"POST /api/v1/assistant/captures",
}

func init() { Routes = append(Routes, captureRoutes...) }

// DeviceTrackInboxWrite: {dir, slug, name, content} → {path}: one file written into a track's inbox/ on the
// assistant's machine (nodeproto.TrackInbox). dir is the track's directory relative to the home, as on
// assistant.prepare; name a plain file name. A node without the capability is never asked.
const DeviceTrackInboxWrite = "assistant.track_inbox_write"

// Where a capture comes from (CaptureCreate.Source).
const (
	CaptureSourceShareIOS     = "share_ios"
	CaptureSourceShareAndroid = "share_android"
	CaptureSourceWatch        = "watch"
	CaptureSourceSiri         = "siri"
	CaptureSourceWeb          = "web"
	CaptureSourceFeishu       = "feishu"
	CaptureSourceShortcut     = "shortcut"
)

// What a capture is (CaptureCreate.Kind): text, a link, or a voice note already transcribed on the device.
const (
	CaptureKindText  = "text"
	CaptureKindURL   = "url"
	CaptureKindVoice = "voice"
)

// A capture's status: new (not sorted yet), sorted (sorted and left for the user), filed (written into a track's
// inbox/), dismissed (the user's), failed (could not be sorted; still listed).
const (
	CaptureNew       = "new"
	CaptureSorted    = "sorted"
	CaptureFiled     = "filed"
	CaptureDismissed = "dismissed"
	CaptureFailed    = "failed"
)

// CaptureCreate is POST /api/v1/assistant/captures. ClientID is made by the device once per capture and sent again
// with every retry (an offline watch's queue): the same ClientID is one capture, delivered once.
type CaptureCreate struct {
	ClientID string `json:"client_id"`
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	// SpaceHint is the track the user chose for it, if any: it is then tagged, never sorted elsewhere.
	SpaceHint string `json:"space_hint,omitempty"`
}

// CaptureAccepted answers POST /api/v1/assistant/captures: 202 for a new capture, 200 with Duplicate for a ClientID
// already delivered (its first capture, as it is now).
type CaptureAccepted struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// CaptureSort is what sorting made of a capture: the track it fits (Track "" for none), what to do with it, a short
// title, whether it needs a deep look, the model's confidence, and which model and when.
type CaptureSort struct {
	Track      string    `json:"track,omitempty"`
	Action     string    `json:"action,omitempty"` // keep, discuss or research
	Title      string    `json:"title,omitempty"`
	Deep       bool      `json:"deep,omitempty"`
	Confidence float64   `json:"confidence,omitempty"`
	Model      string    `json:"model,omitempty"`
	At         time.Time `json:"at,omitzero"`
	// AutoFile: it goes into Track's inbox/ without the user (the track was chosen, or the confidence was high); it
	// stays sorted until the file is written.
	AutoFile bool `json:"auto_file,omitempty"`
}

// CaptureFiledTo is where a filed capture went: kind space_file and ref the file's path in the track's directory.
type CaptureFiledTo struct {
	Kind  string `json:"kind"`
	Track string `json:"track,omitempty"`
	Ref   string `json:"ref"`
}

// Capture is one capture as the user's apps see it. Categories are the information categories it carries (its
// target track's default ones included); Tagged says they were told (untagged ones count as sensitive outside the
// list).
type Capture struct {
	ID         string          `json:"id"`
	ClientID   string          `json:"client_id"`
	CreatedAt  time.Time       `json:"created_at"`
	Source     string          `json:"source"`
	Kind       string          `json:"kind"`
	Text       string          `json:"text"`
	URL        string          `json:"url,omitempty"`
	SpaceHint  string          `json:"space_hint,omitempty"`
	Status     string          `json:"status"`
	Sort       *CaptureSort    `json:"sort,omitempty"`
	FiledTo    *CaptureFiledTo `json:"filed_to,omitempty"`
	Categories []string        `json:"categories"`
	Tagged     bool            `json:"tagged"`
}

// Captures is GET /api/v1/assistant/captures[?status=]: the newest first.
type Captures struct {
	Captures []Capture `json:"captures"`
}
