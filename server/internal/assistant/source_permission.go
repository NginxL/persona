package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A digest speaks for every source it contains. A single attested report must not
// confer its permission class on an unknown source or one in a different class.
func supervisionSourceContext(ctx context.Context, kept []superviseSignal) context.Context {
	var p proto.SourcePermission
	for i, g := range kept {
		if !g.SourcePermission.Valid() || g.SourcePermission.Session != g.Session || (i > 0 && p.Class != g.SourcePermission.Class) {
			return devicewire.WithSourcePermission(ctx, proto.SourcePermission{})
		}
		p = g.SourcePermission
	}
	return devicewire.WithSourcePermission(ctx, p)
}
