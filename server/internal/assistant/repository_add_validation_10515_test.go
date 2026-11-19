package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRepositoryAddValidationFailureIsDefinitelyNotApplied10515(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{name: "argument envelope", args: `null`, want: "repository arguments"},
		{name: "remote validation", args: `{"url":"https://token@code.example.com/example/project"}`, want: "repository_remote"},
		{name: "metadata validation", args: `{"url":"https://github.com/example/project","name":"bad\nname"}`, want: "repository_metadata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, checks := repositoryService(t, ruleStore{&memStore{}})
			out := repositoryAddCall(context.Background(), s, "repository_add", tc.args)
			text := strings.ToLower(out.Content[0].Text)
			if !out.IsError || !strings.Contains(text, strings.ToLower(tc.want)) {
				t.Fatalf("validation lost its existing diagnostic: %+v, want %q", out, tc.want)
			}
			if (!strings.Contains(text, "not applied") && !strings.Contains(text, "nothing was applied")) || strings.Contains(text, "uncertain") {
				t.Fatalf("pre-execution validation was not honestly definite: %+v", out)
			}
			doc, err := s.repositoryDocument(context.Background())
			if err != nil || len(doc.Repositories) != 0 || *checks != 0 || len(s.pendingQuestions()) != 0 {
				t.Fatalf("invalid input approved, checked, or saved a repository: doc=%+v err=%v checks=%d cards=%d", doc, err, *checks, len(s.pendingQuestions()))
			}
		})
	}
}

func TestRepositoryAddFreshOwnerAllowStillAppliesOnce10515(t *testing.T) {
	s, checks := repositoryService(t, ruleStore{&memStore{}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	allow := true
	var cardID string
	done := answerRepositoryCard(t, s, proto.Resolution{Allow: &allow}, func(card proto.Interaction) {
		cardID = card.ID
		doc, err := s.repositoryDocument(ctx)
		if err != nil || len(doc.Repositories) != 0 || *checks != 0 {
			t.Errorf("repository changed before its fresh owner Allow: doc=%+v err=%v checks=%d", doc, err, *checks)
		}
	})
	out := repositoryAddCall(ctx, s, "repository_add", repoExample)
	<-done
	var binding RepositoryBinding
	if out.IsError || json.Unmarshal([]byte(out.Content[0].Text), &binding) != nil || binding.ID == "" || *checks != 1 {
		t.Fatalf("fresh owner Allow did not apply the valid operation once: %+v checks=%d", out, *checks)
	}
	if err := s.resolve(cardID, proto.Resolution{Allow: &allow}); err == nil {
		t.Fatal("the consumed repository card accepted another owner Allow")
	}
	doc, err := s.repositoryDocument(context.Background())
	if err != nil || len(doc.Repositories) != 1 || doc.Repositories[0].ID != binding.ID || *checks != 1 {
		t.Fatalf("duplicate Allow changed the repository or applied twice: doc=%+v err=%v checks=%d", doc, err, *checks)
	}
}
