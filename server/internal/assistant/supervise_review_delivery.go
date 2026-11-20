package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// A supervisor's accepted closes a task, so with assistant.done_needs_delivery on ("a finished task closes only once
// live") it is held to what the judge's finish is held to (done.go): a task whose work is a GitHub PR is accepted only
// once the running hub contains the PR's merge (the deploy check, deploy_check.go), or, where the running hub cannot be
// checked against (another repository, a hub that does not say its commit), once the PR is merged. A check that fails
// refuses too: it never says live. "cannot" (nothing was delivered) and tasks with no PR are not held.

//
// A review request on another host is read by the code host that host has (code_host.go): refused while it is
// readably not merged, accepted once merged. A host no code host reads, or one that cannot answer now, never holds the
// task: note says its delivery was not checked, for the supervisor's reply.

// acceptedDeliveryError refuses accepting task t while its PR (link, when it names one, else the task's) is not live;
// nil when nothing holds it. note, when set, is what the reply adds about a delivery that could not be checked.
func (s *Service) acceptedDeliveryError(ctx context.Context, t Todo, link, outcome string) (note string, err error) {
	if outcome == outcomeCannot || t.DoneAt != nil || !s.doneNeedsDelivery(ctx) {
		return "", nil
	}
	pr := t.PRURL
	if ciPRURL.MatchString(link) || otherReviewLink(link) {
		pr = link
	} else {
		replacement, noPR, err := s.reportedTaskPR(ctx, t, pr)
		if err != nil {
			return "", err
		}
		if noPR || replacement != "" && replacement != pr {
			if err := s.forgetTaskPR(ctx, t.ID, pr, replacement); err != nil {
				return "", err
			}
			current, ok := s.todoNow(t.ID)
			if !ok {
				return "", fmt.Errorf("task %s no longer exists", t.ID)
			}
			if current.PRURL != replacement {
				return "", fmt.Errorf("task %s: its delivery record changed during verification; retry the review", t.ID)
			}
			t, pr = current, current.PRURL
		}
	}
	m := ciPRURL.FindStringSubmatch(pr)
	if m == nil {
		return s.otherHostDelivery(ctx, t, pr)
	}
	r := s.checkDeployed(ctx, pr)
	if r.State == deployUnverified {
		// Nothing to check it against: it is at least merged.
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, err := githubHost{forge: s.forge()}.ReviewStatus(cctx, pr)
		cancel()
		switch {
		case err != nil:
			r = deployResult{State: deployUnknown, Err: err}
		case !status.Merged():
			r = deployResult{State: deployUnmerged}
		default:
			return "", nil
		}
	}
	if r.Err != nil {
		number, _ := strconv.Atoi(m[2])
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		isPR, err := s.forge().IsPR(cctx, m[1], number)
		cancel()
		if err == nil && !isPR {
			for _, previous := range t.PRs {
				if previous != pr {
					return "", fmt.Errorf("task %s: earlier PR references have no delivery ownership evidence; "+
						"report this task's explicit delivery artifact or completed non-PR closeout before accepting", t.ID)
				}
			}
			if err := s.forgetTaskPR(ctx, t.ID, pr, ""); err != nil {
				return "", err
			}
			current, ok := s.todoNow(t.ID)
			if !ok {
				return "", fmt.Errorf("task %s no longer exists", t.ID)
			}
			if current.PRURL == pr {
				return "", fmt.Errorf("task %s: its non-PR record changed during verification; retry the review", t.ID)
			}
			// A concurrent, genuine delivery still has to pass the gate; reference history is not ownership proof.
			return s.acceptedDeliveryError(ctx, current, "", outcome)
		}
	}
	if !r.held() {
		return "", nil
	}
	if r.State == deployUnknown || r.State == deployNotIn && r.Err != nil {
		s.logf("task %s: delivery status temporarily unverifiable: %v", t.ID, r.Err)
		return "", fmt.Errorf("%s", i18n.T(s.lang(ctx), "assistant-reports.delivery_verification_unavailable",
			i18n.Args{"task": t.ID, "pr": pr, "reason": fmt.Sprint(r.Err)}))
	}
	return "", notLiveError(s, t, pr, r)
}

// notLiveError refuses accepting t while its PR pr is held as r says.
func notLiveError(s *Service, t Todo, pr string, r deployResult) error {
	why := map[string]string{
		deployUnmerged: "the PR is not merged",
		deployOut:      "the PR is merged but the running hub does not contain it yet",
		deployNotIn:    "the running hub's deployed commits do not name the PR",
		deployUnknown:  "whether the PR is merged and live could not be checked",
	}[r.State]
	if r.Err != nil {
		why += " (" + r.Err.Error() + ")"
	}
	s.logf("task %s: accepted refused: %s: %s", t.ID, pr, cmp.Or(r.State, "held"))
	return fmt.Errorf("task %s is not accepted: %s is not live: %s, and the user's to-dos close only once their work is "+
		"live (done_needs_delivery). Keep it open: send what is missing to its agent (rework, or agent_send to merge and "+
		"deploy), park it with waiting_on external_event until the merge or deploy, or cancel it if the work is dropped. "+
		"If nothing could be delivered at all, accept it with outcome cannot", t.ID, pr, why)
}

// otherReviewLink: link is exactly a review request on a host other than GitHub (prLink's merge request form).
func otherReviewLink(link string) bool {
	return link != "" && !ciPRURL.MatchString(link) && prLink.FindString(link) == link
}

// otherHostDelivery holds t while its review request pr, not a GitHub pull request, is readably not merged by the code
// host of its host; a host none reads, or one that cannot answer now, only gets the reply's note. A pr that is no
// review request at all (none recorded) holds nothing, as before.
func (s *Service) otherHostDelivery(ctx context.Context, t Todo, pr string) (string, error) {
	if !otherReviewLink(pr) {
		return "", nil
	}
	wait, cancel := providerCall(withProviderBudget(ctx))
	s.awaitProviders(wait)
	cancel()
	h, host := s.codeHostFor(pr)
	lang := s.lang(ctx)
	if h == nil {
		s.logf("task %s: delivery %s not checked: no code host reads %s", t.ID, pr, host)
		return i18n.T(lang, "assistant-reports.delivery_host_not_connected", i18n.Args{"pr": pr, "host": host}), nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	status, err := h.ReviewStatus(cctx, pr)
	cancel()
	switch {
	case errors.Is(err, errReviewUnsupported):
		s.logf("task %s: delivery %s not checked: %s cannot read its state", t.ID, pr, h.Name())
		return i18n.T(lang, "assistant-reports.delivery_host_not_connected", i18n.Args{"pr": pr, "host": host}), nil
	case err != nil:
		s.logf("task %s: delivery %s not checked on %s: %v", t.ID, pr, h.Name(), err)
		return i18n.T(lang, "assistant-reports.delivery_host_unchecked", i18n.Args{"pr": pr, "host": host, "reason": err.Error()}), nil
	case status.Merged():
		return "", nil
	}
	return "", notLiveError(s, t, pr, deployResult{State: deployUnmerged})
}
