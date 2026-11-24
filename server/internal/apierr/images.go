package apierr

import (
	"maps"
	"net/http"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ImagesOverLimit answers 413 images_over_limit when a message carries more pictures than l allows (#12973) and
// reports whether it did. Only the message's own pictures count, never those earlier in its conversation; their
// bytes are not checked here: the body's limit holds them, and a larger picture than l.ImageBytes still goes (an older
// web client sends originals). The body says in the request's language how many there were and how many fit, with
// the limits and the count as numbers (docs/protocol/protocol.md "Pictures in a message").
func ImagesOverLimit(w http.ResponseWriter, l proto.ImageLimits, images int) bool {
	if images <= l.PerMessage {
		return false
	}
	body := CodeBody(w, http.StatusRequestEntityTooLarge, proto.CodeImagesOverLimit,
		i18n.Err("apierr-images.over_limit", i18n.Args{"n": images, "max": l.PerMessage}))
	maps.Copy(body, map[string]any{"limit": l, "got": map[string]any{"images": images}, "retryable": false, "delivered": "none"})
	WriteBody(w, http.StatusRequestEntityTooLarge, body)
	return true
}
