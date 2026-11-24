package node

import "github.com/nwverse/persona/server/internal/lazyre"

// The kind of an assistant process's track (#13472, docs/extensions/extensions.md "Assistant"): next to PERSONA_SPACE
// (its track's slug, from its directory), the process gets PERSONA_TRACK_KIND, the kind the manager last named for its
// directory on assistant.prepare (track_kind): orchestration for the assistant's own, space for the health space, a
// track's own kind otherwise. A gated command or an extension's script may serve some kinds only. A directory the node
// has not been told of since it started (a process resumed before the first prepare), or a manager from before, gives
// none: such a command takes the kind as unknown.

// trackKindEnv is the variable.
const trackKindEnv = "PERSONA_TRACK_KIND"

// trackKindPattern is a kind's slug (a track slug's form).
var trackKindPattern = lazyre.New(`^[a-z][a-z0-9-]{1,31}$`)

// trackKindValue is kind when it is a kind's slug, else "" (nothing goes into the environment).
func trackKindValue(kind string) string {
	if trackKindPattern.MatchString(kind) {
		return kind
	}
	return ""
}

// trackKind is the kind of the track of assistant directory cwd as its last assistant.prepare said it.
func (n *Node) trackKind(cwd string) string {
	key := assistantPrepareDir(cwd)
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.assistantPrepared[key].trackKind
}
