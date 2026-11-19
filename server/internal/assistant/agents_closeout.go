package assistant

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A closeout is what an agent says of its own work as a turn ends (docs/assistant/agent-work.md "Closeout"): done or
// not, what changed, merged and deployed or not, what was verified, what is missing. The node holds a stop once to
// get one (node/closeout_stop.go) when the manager says so here; the triage reads it before it asks a model.

// closeoutCheck answers a node whose agent is about to stop, after a turn that did work, without a closeout: the
// reason to hold the stop with, or none. Only under the triage's main switch and its closeout switch, and never for
// an assistant conversation or a review's reviewer.
func (s *Service) closeoutCheck(ctx context.Context, machine string, p assistantwire.CloseoutCheckRequest) assistantwire.CloseoutCheckResponse {
	var out assistantwire.CloseoutCheckResponse
	if sw := s.triageSwitches(ctx); !sw.Enabled || !sw.Closeout {
		return out
	}
	if p.SessionID == "" || s.isReviewer(machine, p.SessionID) {
		return out
	}
	if c, ok := s.session(ctx, machine, p.SessionID); ok && c.Assistant {
		return out
	}
	out.Reason = renderCloseoutRequired(answerLanguage(s.hello(ctx).Locale))
	s.workEvent(machine, p.SessionID, "closeout_asked", nil)
	return out
}

func renderCloseoutRequired(language string) string {
	return prompts.RenderAgentCloseoutRequired(prompts.AgentCloseoutRequired{Form: closeout.Example, Language: language})
}

// closeoutDone is the work state of a turn whose closeout says done: no model is asked. The line is the first thing
// it says it did.
func closeoutDone(c proto.Closeout, artifacts []proto.WorkArtifact, lang i18n.Lang) *proto.WorkState {
	w := &proto.WorkState{Phase: proto.WorkDone, Closeout: &c, Artifacts: workArtifacts(artifacts), By: "rule"}
	if len(c.Did) > 0 {
		w.Line = clip(oneLine(c.Did[0]), 60)
	}
	if w.Line == "" {
		w.Line = i18n.T(lang, "assistant-agents_triage.done")
	}
	return w
}

// withCloseout lays a closeout that is not "done" over what the model read from the same reply. The agent's own word
// that it is not finished wins over a model that read the reply as done: the state is then unsure, as for any turn
// nobody can call finished, with the closeout kept for whoever looks.
func withCloseout(w *proto.WorkState, c proto.Closeout, artifacts []proto.WorkArtifact) *proto.WorkState {
	if w.Phase == proto.WorkDone {
		w = &proto.WorkState{Artifacts: w.Artifacts}
	}
	w.Closeout = &c
	if len(w.Artifacts) == 0 {
		w.Artifacts = workArtifacts(artifacts)
	}
	return w
}

// workArtifacts keeps the kinds a work state lists (a closeout may also name a branch).
func workArtifacts(in []proto.WorkArtifact) []proto.WorkArtifact {
	var out []proto.WorkArtifact
	for _, a := range in {
		if strings.TrimSpace(a.Ref) != "" {
			out = append(out, a)
		}
	}
	return out
}

// checkCloseout looks at the closeout's pull request itself (deploy_check.go: the pull request's merge, and whether
// the running hub contains it) and keeps what it found on c. Nothing when the closeout names no pull request by its
// link, or nothing could be told.
func (s *Service) checkCloseout(ctx context.Context, c *proto.Closeout, artifacts []proto.WorkArtifact) {
	for _, a := range artifacts {
		if a.Kind != "pr" || ciPRURL.FindString(a.Ref) == "" {
			continue
		}
		c.Checked = closeoutChecked(s.checkDeployed(ctx, a.Ref))
		return
	}
}

func closeoutChecked(r deployResult) *proto.CloseoutChecked {
	switch r.State {
	case deployIn:
		checked := &proto.CloseoutChecked{Deployed: proto.CloseoutYes, LocalDeployment: r.Receipt != "" && r.Receipt != r.Merge}
		if r.Merge != "" {
			checked.Merged = proto.CloseoutYes
		} else if r.NotMerged {
			checked.Merged = proto.CloseoutNo
		}
		return checked
	case deployOut:
		return &proto.CloseoutChecked{Merged: proto.CloseoutYes, Deployed: proto.CloseoutNo}
	case deployUnmerged:
		return &proto.CloseoutChecked{Merged: proto.CloseoutNo, Deployed: proto.CloseoutNo}
	case deployNotIn:
		checked := &proto.CloseoutChecked{Deployed: proto.CloseoutNo}
		if r.Merge != "" {
			checked.Merged = proto.CloseoutYes
		}
		return checked
	}
	if r.Merge != "" {
		return &proto.CloseoutChecked{Merged: proto.CloseoutYes} // upstream merge was found, deployment could not be checked
	}
	return nil
}

// closeoutDisproved: the closeout says merged or deployed and the hub found it is not.
func closeoutDisproved(c proto.Closeout) bool {
	k := c.Checked
	return k != nil && (c.Merged == proto.CloseoutYes && k.Merged == proto.CloseoutNo ||
		c.Deployed == proto.CloseoutYes && k.Deployed == proto.CloseoutNo)
}

// closeoutDelivery is the delivery a closeout states, for a reply with no delivery line of its own (delivery.go):
// the same claim in the other form. ok false when the closeout says nothing of merging or deploying and is not done.
func closeoutDelivery(c proto.Closeout, artifacts []proto.WorkArtifact) (d TodoDelivery, ok bool) {
	switch {
	case c.Deployed == proto.CloseoutYes:
		d.State = deliveryLive
	case c.Merged == proto.CloseoutYes:
		d.State = deliveryMerged
	case c.Merged == proto.CloseoutNo && hasPR(artifacts):
		d.State = deliveryPR
	case c.Merged == "" && c.Deployed == "" && c.Status == proto.CloseoutDone:
		d.State = deliveryNone
	default:
		return d, false
	}
	if d.State != deliveryLive && d.State != deliveryNone {
		d.Gate = cmpOr(c.Missing, c.Waiting)
		if externalDeliveryWait.MatchString(d.Gate) || externalDeliveryWait.MatchString(c.Waiting) {
			d.Unblock = unblockOther
		}
	}
	return d, true
}

func hasPR(artifacts []proto.WorkArtifact) bool {
	for _, a := range artifacts {
		if a.Kind == "pr" {
			return true
		}
	}
	return false
}
