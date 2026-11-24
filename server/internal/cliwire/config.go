package cliwire

import (
	"context"
	"time"
)

// Config controls the executable and profile. It contains no credentials.
type Config struct {
	Bin     string
	Profile string
	Timeout time.Duration
	// InboxPoll and InboxPageLimit are consumed by the built-in larkcli
	// connector. Keeping them here lets tests and embedders construct the
	// adapter and connector together without another configuration object.
	InboxPoll      time.Duration
	InboxPageLimit int
	// Now is injectable for expiry tests; production uses time.Now.
	Now func() time.Time
	// ResolveProfile gates execution until the host knows the saved identity. It is
	// evaluated for every invocation, including a runner retained across recovery.
	ResolveProfile func(context.Context) (string, error)
}

// ConfigInfo is the public, non-secret part of one lark-cli profile. The CLI
// has emitted both JSON and key/value output across its releases, so callers
// should use AppID and ignore the original presentation.
type ConfigInfo struct {
	AppID string
	Brand string
}
