package proto

// The pictures one message may carry (#12973, docs/protocol/protocol.md "Pictures in a message"). The hub publishes
// them in hello (image_limits) and holds the routes that take a message's pictures to the same numbers; the clients
// size their pickers by them, scale a picture down to MaxSide and turn what does not fit into file attachments.
//
// The numbers come from what the pictures meet on the way (#12973): the request body is the hub's own limit on the
// routes that carry them; a picture's bytes are the lowest per-image limit among the Claude platforms (5 MB of base64
// on Amazon Bedrock); 20 pictures a message is claude.ai's, and no runtime Persona drives takes fewer in one message.
const (
	// MessageBodyLimit bounds the body of a request that carries a message's pictures: sending a message
	// (POST /api/v1/sessions/{mid}/{sid}/messages), starting an Agent with its first message (POST /api/v1/sessions)
	// and starting an assistant conversation (POST /api/v1/assistant/conversations).
	MessageBodyLimit = 32 << 20
	// ImageBytesLimit is one picture's decoded bytes a client aims for: 5,000,000 bytes of base64. The hub does not
	// refuse a larger one, since older web clients send originals of up to 8 MB and the runtimes scale them down.
	ImageBytesLimit = 3_750_000
	// ImageMaxSide is the long side a client scales a picture down to before it goes.
	ImageMaxSide = 1600
	// ImagesPerMessage is how many pictures one message may carry; the hub refuses more (CodeImagesOverLimit).
	ImagesPerMessage = 20
	// CodeImagesOverLimit: a message carries more pictures than its ImageLimits allow (413, with limit and got).
	CodeImagesOverLimit = "images_over_limit"
)

// ImageLimits is one set of the limits, in hello's image_limits and in a 413 images_over_limit's limit.
type ImageLimits struct {
	RequestBytes int64 `json:"request_bytes"`
	ImageBytes   int64 `json:"image_bytes"`
	MaxSide      int   `json:"max_side"`
	PerMessage   int   `json:"per_message"`
}

// ImageLimitsTable is hello's image_limits: the limits for every session, and complete sets for the agents
// (Session.Agent, "claude" for an empty one) that need others. No agent needs others today.
type ImageLimitsTable struct {
	ImageLimits
	Agents map[string]ImageLimits `json:"agents,omitempty"`
}

// DefaultImageLimits is the limits for a session no entry of Agents names.
var DefaultImageLimits = ImageLimits{RequestBytes: MessageBodyLimit, ImageBytes: ImageBytesLimit, MaxSide: ImageMaxSide,
	PerMessage: ImagesPerMessage}

// HubImageLimits is what the hub publishes and checks.
var HubImageLimits = ImageLimitsTable{ImageLimits: DefaultImageLimits}

// For is the limits a message to a session of agent is held to.
func (t ImageLimitsTable) For(agent string) ImageLimits {
	if agent == "" {
		agent = "claude"
	}
	if l, ok := t.Agents[agent]; ok {
		return l
	}
	return t.ImageLimits
}
