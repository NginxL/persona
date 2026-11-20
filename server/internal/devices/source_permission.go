package devices

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// rewriteSourcePermission removes every spelling encoding/json could decode as
// authority. Only a process-local trusted context may replace the source proof.
func rewriteSourcePermission(params json.RawMessage, proof proto.SourcePermission) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, err
	}
	changed := false
	for k := range fields {
		if strings.EqualFold(k, "source_permission") || strings.EqualFold(k, "from_mode") {
			delete(fields, k)
			changed = true
		}
	}
	if proof.Valid() {
		if fields == nil {
			fields = make(map[string]json.RawMessage)
		}
		fields["source_permission"], _ = json.Marshal(proof)
		changed = true
	}
	if !changed {
		return params, nil
	}
	return json.Marshal(fields)
}

func sourcePermissionParams(ctx context.Context, params json.RawMessage, supported bool) (json.RawMessage, bool, error) {
	p := devicewire.SourcePermissionFrom(ctx)
	if !supported {
		p = proto.SourcePermission{}
	}
	out, err := rewriteSourcePermission(params, p)
	return out, p.Valid(), err
}
