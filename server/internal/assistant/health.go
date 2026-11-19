package assistant

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/compat"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The health space (docs/assistant/health.md, #2691): the assistant's conversations about health run in a directory of their
// own next to the assistant's, so Claude Code's auto memory and the instructions are the space's own. Its
// conversations are listed apart (GET /api/v1/assistant?space=health), and the conversation tools of one space see
// only that space's conversations.

// SpaceHealth is the health space's name (Session.Space, StartParams.Space, ?space=).
const SpaceHealth = identity.SpaceHealth

// healthRel is this manager's health space directory on a machine, relative to its home (node assistant.prepare):
// health for the built-in manager, health-<id> for another (identity.HealthDirName), in the state directory, spelled as
// assistantRel is.
func (s *Service) healthRel() string { return path.Join(compat.StateDir, identity.HealthDirName(s.id)) }

// healthCwd is this manager's health space directory on d, or "" while d has not reported its home.
func (s *Service) healthCwd(d Device) string {
	return stateDirOn(d.Home, d.DataDir, identity.HealthDirName(s.id))
}

// spaceCwd is the directory of space ("" the assistant's own) on d.
func (s *Service) spaceCwd(d Device, space string) string {
	if space == SpaceHealth {
		return s.healthCwd(d)
	}
	return s.assistantCwd(d)
}

// healthClaudeMD is the health space's CLAUDE.md for machine here (prompts/assistant/health_claude_md.md).
func (s *Service) healthClaudeMD(ctx context.Context, here string) string {
	hello := s.hello(ctx)
	d := prompts.AssistantHealthClaudeMD{Owner: strings.TrimSpace(hello.Owner), Notes: s.notes(ctx), Language: answerLanguage(hello.Locale)}
	if name := identity.HealthDirName(s.id); name != identity.SpaceHealth {
		d.HealthDir = name // another manager's own space (the built-in's is the template's default)
	}
	devices := s.devices(ctx)
	if i := slices.IndexFunc(devices, func(d Device) bool { return d.ID == here }); i >= 0 {
		if cwd := s.assistantCwd(devices[i]); cwd != "" {
			d.StateDir = path.Dir(cwd)
		}
	}
	return prompts.RenderAssistantHealthClaudeMD(d)
}

// spaceOf is the space of an assistant conversation: its own Space when the hub set it, else its directory's.
func spaceOf(c proto.Session) string {
	if c.Space != "" {
		return c.Space
	}
	return identity.SpaceOf(c.Cwd)
}

// validSpace: "" (the assistant's own) or "health".
func validSpace(space string) bool { return space == "" || space == SpaceHealth }

// What leaves the health space (docs/assistant/health.md "Outbound", #3194): the user may share their health with anyone they
// like (a doctor, a coach, family), so nothing is refused for being health. What must not happen is it leaving without
// them knowing: a push or a channel says only that there is news, and a hand-off to somewhere outside the space is
// confirmed on the phone each time. The space is the conversation's (the trusted session), never a guess at the text.

// inHealth: conversation self runs in the health space.
func (s *Service) inHealth(ctx context.Context, self string) bool {
	space, _ := s.selfSpace(ctx, self)
	return space == SpaceHealth
}

// todoInHealth applies the health space's existing outbound privacy rule to a task opened from one of its conversations.
func (s *Service) todoInHealth(ctx context.Context, t Todo) bool {
	return t.Origin != "" && s.inHealth(ctx, t.Origin)
}

// ixInHealth: interaction ix belongs to a health conversation (its directory, else its conversation's space). A
// directory inside the space counts (identity.InHealthSpace): this only hides, so a `cd` that left the hook in a
// subfolder must not show the card (#2368). spaceOf, which classifies, stays exact.
func (s *Service) ixInHealth(ctx context.Context, ix proto.Interaction) bool {
	if ix.Cwd != "" {
		return identity.InHealthSpace(ix.Cwd)
	}
	return ix.SessionID != "" && ix.MachineID == s.settings(ctx).MachineID && s.inHealth(ctx, ix.SessionID)
}

// healthNotice is what a push or a channel says for a health conversation: that there is news, not the news.
func healthNotice(l i18n.Lang) (title, body string) {
	return i18n.T(l, "assistant-health.notice_title"), i18n.T(l, "assistant-health.notice_body")
}

// confirmOutbound asks the phone before health conversation self hands what (said as summary) to somewhere outside
// the space (tool: another agent, a new one): true when the user allows it, at once when self is not in the space.
func (s *Service) confirmOutbound(ctx context.Context, machine, self, tool, summary string, input json.RawMessage) (bool, error) {
	if !s.inHealth(ctx, self) {
		return true, nil
	}
	l := s.lang(ctx)
	now := time.Now().UTC()
	ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self, Kind: proto.IxPermission, ToolName: tool,
		Summary: i18n.T(l, "assistant-health.outbound", i18n.Args{"what": summary}), ToolInput: input, SessionTitle: "Persona",
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if c, ok := s.session(ctx, machine, self); ok {
		ix.Cwd = c.Cwd
	}
	r, err := s.ask(ctx, ix)
	if err != nil {
		return false, err
	}
	return r.Allow != nil && *r.Allow, nil
}
