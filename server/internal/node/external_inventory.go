package node

import (
	"context"

	"github.com/nwverse/persona/server/internal/proto"
)

// externalLauncherList is source-scoped discovery. It never enters a host runtime or the login shell, and a failed
// first load is not a successful empty directory. Warm non-refresh reads keep the registry's stale-while-revalidate policy.
func (n *Node) externalLauncherList(ctx context.Context, refresh bool) (map[string]any, error) {
	if n.external == nil {
		return nil, externalRefusal(ctx, "adapter_disabled")
	}
	r := n.external
	r.mu.Lock()
	cold := r.at.IsZero()
	r.mu.Unlock()
	if err := r.refresh(ctx, refresh || cold); err != nil {
		return nil, externalAdapterError(ctx, err)
	}
	r.mu.Lock()
	list := r.launchersLocked(r.now())
	r.mu.Unlock()
	return map[string]any{"source": proto.ExternalLauncherSource, "launchers": typedLaunchers(list)}, nil
}

// externalInventoryTarget resolves only a reserved identity through the node's trusted registration. Neither configured
// nor an agent/source hint from the hub can name a command. A cold miss joins one bounded, single-flight inventory load;
// the complete sanitized inventory and the exact local adapter/target identity are still checked after the load.
func (n *Node) externalInventoryTarget(ctx context.Context, name string, refresh bool) (proto.ExternalLauncher, error) {
	if !proto.IsExternalLauncher(name) {
		return proto.ExternalLauncher{}, externalRefusal(ctx, "adapter_target_not_found")
	}
	if n.external == nil {
		return proto.ExternalLauncher{}, externalRefusal(ctx, "adapter_disabled")
	}
	r := n.external
	target, ok := r.lookup(name)
	if refresh || !ok {
		if err := r.refresh(ctx, true); err != nil {
			return proto.ExternalLauncher{}, externalAdapterError(ctx, err)
		}
		target, ok = r.lookup(name)
	}
	if !ok || target.AdapterID != r.config.ID || proto.ExternalLauncherName(target.AdapterID, target.ID) != name {
		return proto.ExternalLauncher{}, externalRefusal(ctx, "adapter_target_not_found")
	}
	return target, nil
}

func (n *Node) externalLauncherModels(ctx context.Context, name string, refresh bool) (proto.LauncherModels, error) {
	target, err := n.externalInventoryTarget(ctx, name, refresh)
	if err != nil {
		return proto.LauncherModels{}, err
	}
	return proto.LauncherModels{Agent: "claude", Models: target.Models}, nil
}
