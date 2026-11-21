// Package saved holds the assistant's saved prompts (the user's own "/" prompts) saved Agent searches, and starred messages: their
// rules, storage and HTTP routes. The assistant Service lends it what it needs through Host.
package saved

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// Host is what the package takes from the assistant Service.
type Host struct {
	// Store is the assistant's current store (nil when there is none).
	Store func() any
	// Event tells the apps what changed.
	Event func(ctx context.Context, ev map[string]any)
	// Handle adds one of the routes the hub relays.
	Handle func(pattern string, fn http.HandlerFunc)
	// Full says whether the request came from a device that may do everything.
	Full func(r *http.Request) bool
	// BodyErr answers a body that could not be read: too large, or bad.
	BodyErr func(w http.ResponseWriter, err, bad error)
}

// Register adds the saved content routes to the host.
func Register(h Host) {
	(&promptBook{h: h}).routes()
	(&searchBook{h: h}).routes()
	(&starBook{h: h}).routes()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
