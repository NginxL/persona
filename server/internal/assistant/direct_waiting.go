package assistant

import "github.com/nwverse/persona/server/internal/proto"

// directUserInteraction is the main conversation's card rule. Agent plans,
// ordinary permissions and terminal dialogs belong to the supervisor; an owner
// proposal is the one permission that explicitly requires the owner's approval.
func directUserInteraction(ix proto.Interaction) bool {
	if ix.Delegating {
		return false
	}
	switch ix.Kind {
	case proto.IxQuestion, proto.IxElicitation:
		return true
	case proto.IxPermission:
		return ix.OwnerProposal
	default:
		return false
	}
}
