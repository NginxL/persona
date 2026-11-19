package node

import (
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Session rows carry the model a transcript reports, which a gateway may rewrite (a routed "passthrough/vendor/model"
// for the clean id the launcher was asked for). normalizeSessionRowModel folds such an echo back to the launcher's own
// declared spelling where the declaration allows the reading, so the apps compare a session's model with the launcher's
// list by the id both name. A row whose model matches nothing stays as it came.
func (n *Node) normalizeSessionRowModel(s *proto.Session) {
	if s == nil || s.Model == "" || s.SourceKind == proto.SourceRuntime {
		if s != nil && isBracketedModel(s.Model) {
			s.Model = ""
		}
		return
	}
	// A pick made in the app stands until its transcript catches up: do not fold it away meanwhile.
	n.mu.Lock()
	pending := n.modes[s.SessionID].Model
	n.mu.Unlock()
	if pending != nil && time.Since(pending.At) <= pendingModelTTL && s.Model == pending.Model {
		return
	}
	declared := n.declaredModelsFor(s)
	s.Model = proto.NormalizeSessionModel(s.Model, declared)
}

// declaredModelsFor is every model id that may be legitimately declared for s: the models of its launcher, or, for a
// row with no launcher (an offline CLI transcript listed for the apps), every managed launcher's, so a prefixed echo
// still finds its clean id. The launchers' last shell list and the launchers.models cache (read, never fetched) add
// their ids too.
func (n *Node) declaredModelsFor(s *proto.Session) []string {
	var out []string
	if s.Launcher != "" {
		if ml, ok := n.managedLauncher(s.Launcher); ok {
			out = append(out, managedModelIDs(ml)...)
		}
		out = append(out, n.cachedLauncherModelIDs(s.Launcher)...)
		for _, l := range n.cachedLaunchers() {
			if l.Name == s.Launcher && l.Model != "" {
				out = append(out, l.Model)
			}
		}
		return out
	}
	if !strings.Contains(s.Model, "/") {
		return nil
	}
	for _, ml := range n.allManagedLaunchers() {
		out = append(out, managedModelIDs(ml)...)
	}
	for _, l := range n.cachedLaunchers() {
		if l.Model != "" {
			out = append(out, l.Model)
		}
	}
	return out
}

// managedModelIDs is every model a managed launcher names: its main, small, subagent and tier models, and the models of
// its gateway.
func managedModelIDs(ml proto.ManagedLauncher) []string {
	out := []string{ml.Model, ml.SmallModel, ml.SubagentModel, ml.OpusModel, ml.SonnetModel, ml.HaikuModel, ml.FableModel}
	for _, pm := range ml.Models {
		out = append(out, pm.ID)
	}
	return out
}

// allManagedLaunchers is every managed launcher this node keeps, secrets withheld ones as their catalog entry.
func (n *Node) allManagedLaunchers() []proto.ManagedLauncher {
	n.managed.mu.Lock()
	defer n.managed.mu.Unlock()
	out := make([]proto.ManagedLauncher, 0, len(n.managed.byID))
	for _, l := range n.managed.byID {
		out = append(out, l)
	}
	return out
}

// cachedLauncherModelIDs is the ids of what the launchers.models cache already holds for name on this node, read under
// its lock without asking any CLI.
func (n *Node) cachedLauncherModelIDs(name string) []string {
	prefix := n.home + "\x00" + name + "\x00"
	launcherModelsCache.Lock()
	defer launcherModelsCache.Unlock()
	var out []string
	for key, res := range launcherModelsCache.got {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		for _, m := range res.Models {
			out = append(out, m.ID, m.Resolved)
			out = append(out, m.Aliases...)
		}
	}
	return out
}

// isBracketedModel: a stand-in marker such as "<synthetic>", not a model id.
func isBracketedModel(id string) bool {
	return strings.HasPrefix(id, "<") && strings.HasSuffix(id, ">")
}
