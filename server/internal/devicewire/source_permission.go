package devicewire

import (
	"context"

	"github.com/nwverse/persona/server/internal/proto"
)

type sourcePermissionKey struct{}

// WithSourcePermission is internal, process-local provenance. Wire parameters never
// grant it: only the assistant's authenticated node request installs this value.
func WithSourcePermission(ctx context.Context, p proto.SourcePermission) context.Context {
	return context.WithValue(ctx, sourcePermissionKey{}, p)
}

func SourcePermissionFrom(ctx context.Context) proto.SourcePermission {
	p, _ := ctx.Value(sourcePermissionKey{}).(proto.SourcePermission)
	return p
}
