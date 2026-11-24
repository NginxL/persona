package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// A supervisor's accepted closes a task. The three-tier delivery gate (assistant.delivery_gate, default "merged";
// legacy assistant.done_needs_delivery maps true -> "live" and explicit false -> "off") controls how far a
// PR/Issue-backed task must go before task_review accepted can close it:
//
//   - off: close strictly on done_when with no delivery gate.
//   - merged (default): tasks with linked PR(s) (t.PRURL, review link, or repository pull_request links) or linked
//     issue(s) (repository issue links) stay open until every PR is merged and every linked issue is closed, unless
//     outcome is "cannot", the task is read-only, or the brief explicitly scopes it as PR-only/draft-only.
//   - live (legacy done_needs_delivery=true): in addition to merged + linked issue closure, a GitHub PR must also be
//     contained in the running hub (the deploy check, deploy_check.go), or at least merged where the running hub cannot
//     be checked against. PR-only criteria do not override live (#6428).
//
// A review request or issue on another host is read by the code host claiming that host (code_host.go): refused while
// readably open/unmerged, accepted once merged/closed. A host no code host reads, or one that cannot answer now, never
// holds the task: note says its delivery was not checked, for the supervisor's reply.

var (
	ciIssueURL            = lazyre.New(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/issues/([1-9][0-9]*)\b`)
	prOnlyScopeRe         = lazyre.New(`(?i)\b(?:pr[\s-]only|draft[\s-]only|draft\s+pr\s+only|only\s+(?:open|create|submit)\s+(?:a\s+)?(?:draft\s+)?pr|stop\s+at\s+(?:the\s+)?(?:draft\s+)?pr)\b|(?:只|仅|只需|只要)(?:开|提|提交|创建)(?:草稿\s*)?(?:PR|合并请求)|(?:只|仅)(?:做|停)到\s*(?:草稿\s*)?PR|仅\s*PR|只\s*PR`) // i18n-ignore: matches PR-only scope text
	mergeCloseCriterionRe = lazyre.New(`(?i)\b(?:merged?|close[sd]?\s+(?:the\s+)?(?:linked\s+)?issue|issue\s+closed)\b|合入|合并|关闭\s*(?:关联\s*)?issue|issue\s*(?:已)?关闭`)                                                                                                                            // i18n-ignore: matches merge/issue criteria text
)

// acceptedDeliveryError refuses accepting task t while its delivery gate (off / merged / live) is not satisfied;
// nil when nothing holds it. note, when set, is what the reply adds about a delivery that could not be checked.
func (s *Service) acceptedDeliveryError(ctx context.Context, t Todo, link, outcome string) (note string, err error) {
	if outcome == outcomeCannot || t.DoneAt != nil {
		return "", nil
	}
	gate := s.deliveryGate(ctx)
	if gate == deliveryGateOff {
		return "", nil
	}
	if gate == deliveryGateMerged {
		var briefPtr *TaskBrief
		if b, ok := s.briefOfTodo(t.ID); ok {
			briefPtr = &b
		}
		if s.taskIsReadonly(ctx, t) && !hasMergeCloseCriterion(briefPtr, t) {
			return "", nil
		}
		if prOnlyScoped(briefPtr, t) {
			return "", nil
		}
	}

	pr := t.PRURL
	explicitLinkPR := false
	if ciPRURL.MatchString(link) || otherReviewLink(link) {
		pr = link
		explicitLinkPR = true
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

	repoPRs, repoIssues, err := s.todoLinkedDeliveryRefs(ctx, t.ID)
	if err != nil {
		s.logf("task %s: linked repository delivery status temporarily unverifiable: %v", t.ID, err)
		return "", fmt.Errorf("%s", i18n.T(s.lang(ctx), "assistant-reports.delivery_verification_unavailable",
			i18n.Args{"task": t.ID, "pr": cmp.Or(pr, t.ID), "reason": fmt.Sprint(err)}))
	}
	prs := make([]string, 0, 1+len(repoPRs))
	seenPR := map[string]bool{}
	addPR := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		key := u
		if ciPRURL.MatchString(u) {
			key = triageNormalizePRURL(u)
		}
		if seenPR[key] {
			return
		}
		seenPR[key] = true
		prs = append(prs, u)
	}
	addPR(pr)
	for _, u := range repoPRs {
		addPR(u)
	}

	var notes []string
	for i, curPR := range prs {
		allowNonPRCleanup := i == 0 && !explicitLinkPR && curPR == t.PRURL
		var n string
		if ciPRURL.MatchString(curPR) {
			n, err = s.checkGitHubPRDelivery(ctx, t, curPR, gate, outcome, allowNonPRCleanup)
		} else {
			n, err = s.otherHostDelivery(ctx, t, curPR, gate)
		}
		if err != nil {
			return "", err
		}
		if n != "" {
			notes = append(notes, n)
		}
	}

	for _, issueURL := range repoIssues {
		n, err := s.checkIssueDelivery(ctx, t, issueURL, gate)
		if err != nil {
			return "", err
		}
		if n != "" {
			notes = append(notes, n)
		}
	}

	return strings.Join(notes, ""), nil
}

// checkGitHubPRDelivery checks one GitHub PR against gate ("merged" or "live").
func (s *Service) checkGitHubPRDelivery(ctx context.Context, t Todo, pr, gate, outcome string, allowNonPRCleanup bool) (string, error) {
	m := ciPRURL.FindStringSubmatch(pr)
	if m == nil {
		return "", nil
	}
	number, _ := strconv.Atoi(m[2])
	if gate == deliveryGateMerged {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		merged, _, err := s.forge().PRMerge(cctx, m[1], number)
		cancel()
		if err != nil {
			if errors.Is(err, errGitHubUnconfigured) {
				s.logf("task %s: delivery %s not checked: GitHub is not connected (%v)", t.ID, pr, err)
				return i18n.T(s.lang(ctx), "assistant-reports.delivery_host_not_connected", i18n.Args{"pr": pr, "host": "github.com"}), nil
			}
			if allowNonPRCleanup {
				if handled, note, cerr := s.tryForgetNonPR(ctx, t, pr, m[1], number, outcome); handled {
					return note, cerr
				}
			}
			s.logf("task %s: delivery status temporarily unverifiable: %v", t.ID, err)
			return "", fmt.Errorf("%s", i18n.T(s.lang(ctx), "assistant-reports.delivery_verification_unavailable",
				i18n.Args{"task": t.ID, "pr": pr, "reason": fmt.Sprint(err)}))
		}
		if !merged {
			return "", notMergedError(s, t, pr)
		}
		return "", nil
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
	if r.Err != nil && allowNonPRCleanup {
		if handled, note, cerr := s.tryForgetNonPR(ctx, t, pr, m[1], number, outcome); handled {
			return note, cerr
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

// tryForgetNonPR checks whether a failing PR lookup on pr was actually a non-PR (e.g. an issue number saved as a PR);
// when confirmed, it drops the false PR record and re-evaluates acceptedDeliveryError on the refreshed task.
func (s *Service) tryForgetNonPR(ctx context.Context, t Todo, pr, repo string, number int, outcome string) (handled bool, note string, err error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	isPR, err := s.forge().IsPR(cctx, repo, number)
	cancel()
	if err != nil || isPR {
		return false, "", nil
	}
	for _, previous := range t.PRs {
		if previous != pr {
			return true, "", fmt.Errorf("task %s: earlier PR references have no delivery ownership evidence; "+
				"report this task's explicit delivery artifact or completed non-PR closeout before accepting", t.ID)
		}
	}
	if err := s.forgetTaskPR(ctx, t.ID, pr, ""); err != nil {
		return true, "", err
	}
	current, ok := s.todoNow(t.ID)
	if !ok {
		return true, "", fmt.Errorf("task %s no longer exists", t.ID)
	}
	if current.PRURL == pr {
		return true, "", fmt.Errorf("task %s: its non-PR record changed during verification; retry the review", t.ID)
	}
	// A concurrent, genuine delivery still has to pass the gate; reference history is not ownership proof.
	note, err = s.acceptedDeliveryError(ctx, current, "", outcome)
	return true, note, err
}

// tryForgetOwnedNonPR applies a task-owned, completed non-PR closeout even when the stale PR exists and is unmerged.
// It removes only the disproved association, then checks all remaining delivery evidence again.
func (s *Service) tryForgetOwnedNonPR(ctx context.Context, t Todo, pr, outcome string) (handled bool, note string, err error) {
	replacement, noPR, err := s.reportedTaskPR(ctx, t, pr)
	if err != nil || (!noPR && replacement == "") {
		return false, "", nil
	}
	if !noPR && replacement == pr {
		return false, "", nil
	}
	if err := s.forgetTaskPR(ctx, t.ID, pr, replacement); err != nil {
		return true, "", err
	}
	current, ok := s.todoNow(t.ID)
	if !ok {
		return true, "", fmt.Errorf("task %s no longer exists", t.ID)
	}
	if current.PRURL != replacement {
		return true, "", fmt.Errorf("task %s: its delivery record changed during verification; retry the review", t.ID)
	}
	note, err = s.acceptedDeliveryError(ctx, current, "", outcome)
	return true, note, err
}

// checkIssueDelivery verifies that a linked repository issue is closed before closing the task.
func (s *Service) checkIssueDelivery(ctx context.Context, t Todo, issueURL, gate string) (string, error) {
	if m := ciIssueURL.FindStringSubmatch(issueURL); m != nil {
		f, ok := s.forge().(issueStateForge)
		if !ok {
			return "", nil
		}
		number, _ := strconv.Atoi(m[2])
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		state, err := f.IssueState(cctx, m[1], number)
		cancel()
		if err != nil {
			if gate == deliveryGateMerged && errors.Is(err, errGitHubUnconfigured) {
				s.logf("task %s: linked issue %s not checked: GitHub is not connected (%v)", t.ID, issueURL, err)
				return i18n.T(s.lang(ctx), "assistant-reports.delivery_host_not_connected", i18n.Args{"pr": issueURL, "host": "github.com"}), nil
			}
			s.logf("task %s: linked issue %s temporarily unverifiable: %v", t.ID, issueURL, err)
			return "", fmt.Errorf("%s", i18n.T(s.lang(ctx), "assistant-reports.delivery_verification_unavailable",
				i18n.Args{"task": t.ID, "pr": issueURL, "reason": fmt.Sprint(err)}))
		}
		if state != "closed" {
			return "", issueOpenError(s, t, issueURL, state)
		}
		return "", nil
	}
	return s.otherHostIssueDelivery(ctx, t, issueURL)
}

// otherHostIssueDelivery checks a linked issue on a non-GitHub provider host, matching otherHostDelivery's graceful
// convention: an unreadable or disconnected host never holds the task.
func (s *Service) otherHostIssueDelivery(ctx context.Context, t Todo, issueURL string) (string, error) {
	host, ok := repository.RemoteHost(issueURL)
	if !ok || s.repoExt == nil || slices.Contains(repository.BuiltinHosts, host) {
		return "", nil
	}
	wait, cancel := providerCall(withProviderBudget(ctx))
	s.awaitProviders(wait)
	cancel()
	info, ok := s.repoExt.RepositoryProviderForHost(host)
	if !ok {
		return "", nil
	}
	lang := s.lang(ctx)
	if info.State != repository.ProviderAvailable {
		err := &repository.ProviderError{Code: "provider_off", Provider: info.Name}
		s.logf("task %s: issue %s not checked on %s: %v", t.ID, issueURL, info.Name, err)
		return i18n.T(lang, "assistant-reports.delivery_host_unchecked", i18n.Args{"pr": issueURL, "host": host, "reason": err.Error()}), nil
	}
	call, cancel := providerCall(withProviderBudget(ctx))
	d, err := s.repoExt.RepositoryDescribe(call, info.ID, issueURL)
	cancel()
	if err != nil {
		s.logf("task %s: issue %s not checked on %s: %v", t.ID, issueURL, info.Name, err)
		return i18n.T(lang, "assistant-reports.delivery_host_unchecked", i18n.Args{"pr": issueURL, "host": host, "reason": err.Error()}), nil
	}
	if d.State == "" || strings.EqualFold(d.State, "closed") {
		return "", nil
	}
	return "", issueOpenError(s, t, issueURL, d.State)
}

// todoLinkedDeliveryRefs reads saved repository pull_request and issue links for task id. Only delivery-role links are
// returned for gate verification; reference links are display-only. A pull_request still stored with the default
// delivery role that the forge can prove was opened by someone else is downgraded to reference first, so a merely
// referenced foreign PR no longer blocks acceptance.
func (s *Service) todoLinkedDeliveryRefs(ctx context.Context, id string) (prs, issues []string, err error) {
	if _, ok := s.store().(todoRepositoryStore); !ok {
		return nil, nil, nil
	}
	set, err := s.savedTodoRepositories(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	seenIssue := map[string]bool{}
	for _, row := range set.Repositories {
		for _, l := range row.Links {
			u := strings.TrimSpace(l.CanonicalURL)
			if u == "" {
				continue
			}
			switch l.Kind {
			case "pull_request":
				role := l.Role
				if role == "" {
					role = repository.LinkRoleDelivery
				}
				if role == repository.LinkRoleDelivery && s.provenForeignPR(ctx, u) {
					if err := s.setTodoLinkRole(ctx, id, u, repository.LinkRoleReference); err != nil {
						s.logf("task %s: reference role for %s not saved: %v", id, u, err)
					} else {
						role = repository.LinkRoleReference
					}
				}
				if role == repository.LinkRoleDelivery {
					prs = append(prs, u)
				}
			case "issue":
				key := strings.ToLower(u)
				if !seenIssue[key] {
					seenIssue[key] = true
					issues = append(issues, u)
				}
			}
		}
	}
	return prs, issues, nil
}

// provenForeignPR reports whether u is a GitHub PR the forge can prove was opened by someone other than the
// authenticated viewer. False covers every non-provable case (non-GitHub host, unconnected forge, unknown author):
// the delivery default must stay conservative.
func (s *Service) provenForeignPR(ctx context.Context, u string) bool {
	m := ciPRURL.FindStringSubmatch(u)
	if m == nil {
		return false
	}
	own, ok := s.forge().(prOwnershipForge)
	if !ok {
		return false
	}
	number, _ := strconv.Atoi(m[2])
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	owned, known, err := own.PROwnedByViewer(cctx, m[1], number)
	if err != nil {
		s.logf("PR %s ownership not checked: %v", u, err)
		return false
	}
	return known && !owned
}

// setTodoLinkRole rewrites the role of one saved link (by canonical URL) in a single store transaction, so a
// concurrent edit to another link cannot be reverted. It is the supported alternative to deleting a misattributed
// link.
func (s *Service) setTodoLinkRole(ctx context.Context, id, canonicalURL, role string) error {
	linksStore, ok := s.store().(todoRepositoryRoleStore)
	if !ok {
		return repository.Problem("store_unavailable")
	}
	_, err := linksStore.SetTodoRepositoryLinkRole(ctx, id, canonicalURL, role)
	return err
}

// taskIsReadonly reports whether task t was planned as a read-only task.
func (s *Service) taskIsReadonly(ctx context.Context, t Todo) bool {
	if t.PlanID == "" {
		return false
	}
	p, err := s.loadPlan(ctx, t.PlanID)
	if err != nil || p == nil {
		return false
	}
	for _, pt := range p.Tasks {
		if pt.TodoID == t.ID || pt.ReuseTodo == t.ID {
			return pt.Readonly
		}
	}
	return false
}

// activeCriteria returns the task's current acceptance criteria (ledger DoneWhen, else brief Criteria).
func activeCriteria(brief *TaskBrief, t Todo) []string {
	if t.Supervision != nil && len(t.Supervision.DoneWhen) > 0 {
		return t.Supervision.DoneWhen
	}
	if brief != nil && len(brief.Criteria) > 0 {
		return brief.Criteria
	}
	return nil
}

// hasMergeCloseCriterion reports whether any active criterion explicitly requires PR merge or issue closure.
func hasMergeCloseCriterion(brief *TaskBrief, t Todo) bool {
	for _, c := range activeCriteria(brief, t) {
		if mergeCloseCriterionRe.MatchString(c) {
			return true
		}
	}
	return false
}

// prOnlyScoped reports whether the task's authoritative scope explicitly opts out of merging (PR-only / draft-only).
// Any active acceptance criterion requiring merge or issue closure takes precedence and returns false.
func prOnlyScoped(brief *TaskBrief, t Todo) bool {
	if hasMergeCloseCriterion(brief, t) {
		return false
	}
	criteria := activeCriteria(brief, t)
	texts := make([]string, 0, len(criteria)+3)
	texts = append(texts, criteria...)
	texts = append(texts, t.Title)
	if brief != nil {
		texts = append(texts, brief.Goal)
		if brief.Request != nil {
			texts = append(texts, brief.Request.Text)
		}
	}
	for _, text := range texts {
		if prOnlyScopeRe.MatchString(text) {
			return true
		}
	}
	return false
}

// notMergedError refuses accepting t under delivery_gate=merged while its PR is not merged yet.
func notMergedError(s *Service, t Todo, pr string) error {
	s.logf("task %s: accepted refused (merged gate): %s: unmerged", t.ID, pr)
	return fmt.Errorf("task %s is not accepted: %s is not merged yet, and the delivery gate (delivery_gate=merged) "+
		"keeps PR/Issue tasks open until the PR is merged and linked issues are closed. Keep it open: park it with "+
		"waiting_on external_event (evidence.kind=external_event, url=%s, pr_event=terminal) or waiting_on someone_else "+
		"(evidence.kind=merge_review) until merge, send what is missing to its agent (rework / agent_send), or cancel it "+
		"if the work is dropped. If nothing could be delivered at all, accept it with outcome cannot", t.ID, pr, pr)
}

// issueOpenError refuses accepting t while a linked issue is still open.
func issueOpenError(s *Service, t Todo, issueURL, state string) error {
	st := cmp.Or(strings.TrimSpace(state), "open")
	s.logf("task %s: accepted refused (delivery gate): %s: %s", t.ID, issueURL, st)
	return fmt.Errorf("task %s is not accepted: linked issue %s is still %s, and the delivery gate keeps PR/Issue "+
		"tasks open until linked issues are closed. Keep it open: park it with waiting_on external_event or someone_else "+
		"until the PR merges and closes the issue (or close the issue once verified), send what is missing to its agent "+
		"(rework / agent_send), or cancel it if the work is dropped. If nothing could be delivered at all, accept it with "+
		"outcome cannot", t.ID, issueURL, st)
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
func (s *Service) otherHostDelivery(ctx context.Context, t Todo, pr, gate string) (string, error) {
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
	if gate == deliveryGateMerged {
		return "", notMergedError(s, t, pr)
	}
	return "", notLiveError(s, t, pr, deployResult{State: deployUnmerged})
}
