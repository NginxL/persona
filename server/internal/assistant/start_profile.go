package assistant

import (
	"context"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// meetingProfile is the Claude profile (config_dir) a meeting's conversations, its room and its follow-up, start in, and
// the conversations the hub starts by itself (startConversation): the one the assistant's conversations had before each
// new one got a private profile, a config directory with no login in it (docs/assistant/meeting-minutes.md "The room
// conversation", #6015).
const meetingProfile = "legacy"

// startSignedIn starts p in meetingProfile, or as it is where there is no profile to start in: on a node too old to be
// told one, whose conversations all start in that one (the hub refuses the profile before anything starts,
// devices/machine.go), and on a launcher that runs another agent than Claude Code, which alone has profiles. startOn
// leaves the profile out for a launcher it knows runs another agent; one it cannot tell (the machine's launcher list
// did not come, …) the node refuses it for before anything starts (#6884). Either way the second start is the only one.
func (s *Service) startSignedIn(ctx context.Context, p StartParams) (proto.Session, error) {
	p.ConfigDir = meetingProfile
	sess, err := s.start(ctx, p)
	if nodeOutdated(err) || profileRefused(err) {
		p.ConfigDir = ""
		return s.start(ctx, p)
	}
	return sess, err
}

// nodeOutdated: the hub refused a call for something the node is too old for.
func nodeOutdated(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == proto.CodeNodeOutdated
}

// profileRefused: the node refused a config_dir because the launcher runs another agent than Claude Code, before
// starting anything (assistantwire.ConfigDirClaudeOnly; its own words, also from a node older than the constant).
func profileRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), assistantwire.ConfigDirClaudeOnly)
}

// unknownMethod: a node answered that it does not know the method (an older node).
func unknownMethod(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown method")
}
