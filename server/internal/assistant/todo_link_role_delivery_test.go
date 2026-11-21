package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
)

const (
	ownDeliveryPR = "https://github.com/o/r/pull/21"
	foreignPR     = "https://github.com/o/r/pull/6206"
)

// linkRoleTask is a supervised, merge-criterion task on a real Postgres store with the merged delivery gate.
func linkRoleTask(t *testing.T) (*Service, Todo, *fakeForge) {
	t.Helper()
	st := repositorystore.New(todoTestDB(t))
	s, _, todo := newSupervisedServiceOn(t, st)
	setDeliveryGate(t, s, deliveryGateMerged)
	forge := &fakeForge{} // nothing is merged or owned by default
	s.deploy.forge = forge
	s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	s.deploy.listPaths = []string{}
	return s, todo, forge
}

func linkRoles(t *testing.T, s *Service, todoID string) map[string]string {
	t.Helper()
	set, err := s.savedTodoRepositories(context.Background(), todoID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range set.Repositories {
		for _, l := range row.Links {
			role := l.Role
			if role == "" {
				role = repository.LinkRoleDelivery
			}
			got[l.CanonicalURL] = role
		}
	}
	return got
}

// A reference-role PR is display-only: an open, unmerged PR the task merely cites must not block acceptance.
// Removing the role filtering makes this test fail with the not-merged delivery error.
func TestReferenceRolePRDoesNotBlockAcceptance(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := context.Background()
	if err := s.importReportedLinks(ctx, todo.ID, []string{foreignPR}, "pull_request",
		map[string]string{foreignPR: repository.LinkRoleReference}); err != nil {
		t.Fatal(err)
	}
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleReference {
		t.Fatalf("foreign PR stored as %q, want reference", got)
	}
	if _, err := acceptReview(s, todo.ID, nil); err != nil {
		t.Fatalf("a reference PR blocked acceptance: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("task stayed open although its only linked PR was a reference")
	}
}

// A delivery-role PR keeps the old gate: unmerged delivery still refuses acceptance.
func TestDeliveryRolePRStillBlocksAcceptance(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := context.Background()
	if err := s.importReportedLinks(ctx, todo.ID, []string{ownDeliveryPR}, "pull_request",
		map[string]string{ownDeliveryPR: repository.LinkRoleDelivery}); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptReview(s, todo.ID, nil); err == nil ||
		!strings.Contains(err.Error(), "not merged yet") {
		t.Fatalf("unmerged delivery PR was accepted: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt != nil {
		t.Fatal("task closed with an unmerged delivery PR")
	}
}

// A default-role PR the forge proves belongs to someone else is downgraded to reference at the gate, then accepted.
// Without the ownership downgrade the same open PR hard-blocks acceptance and can only be removed by hand.
func TestProvenForeignPRDowngradedToReference(t *testing.T) {
	s, todo, forge := linkRoleTask(t)
	ctx := context.Background()
	if err := s.importReportedLinks(ctx, todo.ID, []string{foreignPR}, "pull_request", nil); err != nil {
		t.Fatal(err)
	}
	forge.ownedPRs = map[string]bool{"o/r#6206": false} // known, opened by another author
	if _, err := acceptReview(s, todo.ID, nil); err != nil {
		t.Fatalf("a proven-foreign PR blocked acceptance: %v", err)
	}
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleReference {
		t.Fatalf("foreign PR kept role %q, want reference", got)
	}
}

// When ownership cannot be established, the conservative delivery default stays and the unmerged PR still holds the
// task: a forge outage must never silently drop a delivery requirement.
func TestUnknownOwnershipKeepsDeliveryGate(t *testing.T) {
	s, todo, forge := linkRoleTask(t)
	ctx := context.Background()
	if err := s.importReportedLinks(ctx, todo.ID, []string{foreignPR}, "pull_request", nil); err != nil {
		t.Fatal(err)
	}
	forge.ownedPRs = map[string]bool{} // no key: unknown ownership
	if _, err := acceptReview(s, todo.ID, nil); err == nil ||
		!strings.Contains(err.Error(), "not merged yet") {
		t.Fatalf("unknown-ownership PR lost the delivery gate: %v", err)
	}
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleDelivery {
		t.Fatalf("unknown-ownership PR role changed to %q", got)
	}
}

// The role is editable through the stored set: flipping reference back to delivery makes the gate hold again.
func TestLinkRoleEditRoundTrip(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := context.Background()
	if err := s.importReportedLinks(ctx, todo.ID, []string{foreignPR}, "pull_request",
		map[string]string{foreignPR: repository.LinkRoleReference}); err != nil {
		t.Fatal(err)
	}
	if err := s.setTodoLinkRole(ctx, todo.ID, foreignPR, repository.LinkRoleDelivery); err != nil {
		t.Fatal(err)
	}
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleDelivery {
		t.Fatalf("role edit did not persist: %q", got)
	}
}

// The real report path: an Agent reply whose prose frames a PR as related work stores it as reference at import,
// while the same reply's own delivery PR keeps the delivery role. Without the prose classifier both land as delivery.
func TestAdvanceTaskReplyClassifiesReferenceProse(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	reply := "Done.\nRelated work: " + foreignPR + " tracks the upstream dependency.\n" +
		"Our delivery PR is " + ownDeliveryPR + ".\n"
	s.advanceTaskReply(context.Background(), todo, "box", todo.Agents[0].SessionID, reply)
	got := linkRoles(t, s, todo.ID)
	if got[foreignPR] != repository.LinkRoleReference {
		t.Fatalf("related-work PR role = %q, want reference", got[foreignPR])
	}
	if got[ownDeliveryPR] != repository.LinkRoleDelivery {
		t.Fatalf("delivery PR role = %q, want delivery", got[ownDeliveryPR])
	}
}
