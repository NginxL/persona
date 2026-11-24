package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func (s *Service) toolRepositoryAdd(ctx context.Context, machine, self string, raw json.RawMessage) (string, error) {
	var in repositoryInput
	if err := repositoryArguments(raw, &in); err != nil {
		return "", repositoryAddNotApplied(err)
	}
	binding, err := s.repositoryBinding(withProviderBudget(ctx), in, "")
	if err != nil {
		return "", repositoryAddNotApplied(s.repositoryToolError(ctx, err))
	}
	doc, err := s.repositoryDocument(ctx)
	if err != nil {
		return "", repositoryAddNotApplied(s.repositoryToolError(ctx, err))
	}
	for _, saved := range doc.Repositories {
		if s.sameRepository(saved.Identity, binding.Identity) {
			return "", repositoryAddNotApplied(s.repositoryToolError(ctx, repositoryProblem("duplicate")))
		}
	}
	if machine == "" || self == "" {
		return "", repositoryAddNotApplied(errors.New("a calling conversation is required"))
	}
	shown, _ := json.Marshal(in)
	if repository.IsExtensionProvider(binding.Provider) {
		shown = providerRepositoryCard(binding)
	}
	summary := i18n.T(s.lang(ctx), "assistant-repository_tools.add", i18n.Args{"url": binding.CanonicalURL})
	now := time.Now().UTC()
	outcome, err := s.waitPreparedCard(ctx, proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self,
		Kind: proto.IxPermission, ToolName: "repository_add", Summary: summary, ToolInput: shown, SessionTitle: "Persona",
		CreatedAt: now, ExpiresAt: now.Add(settingCardWait)}, approvalops.Operation{
		Replacement: machine + "\x00" + self + "\x00repository\x00" + binding.Identity.Provider + "\x00" + binding.Identity.CanonicalURL,
		Denied:      "The user declined the change: nothing changed.",
		Unanswered:  prompts.RenderSettingUnanswered(),
		Apply: func(ctx context.Context, _ proto.Resolution) (string, error) {
			// A duplicate added while the owner read or discussed the card makes
			// this proposal stale; saving the old binding is not an approval of it.
			doc, err := s.repositoryDocument(ctx)
			if err != nil {
				return "", s.repositoryToolError(ctx, err)
			}
			for _, saved := range doc.Repositories {
				if s.sameRepository(saved.Identity, binding.Identity) {
					return "", approvalops.ErrStale
				}
			}
			saved, err := s.saveRepository(ctx, "", binding)
			if err != nil {
				return "", s.repositoryToolError(ctx, err)
			}
			out, err := json.Marshal(saved)
			return string(out), err
		},
	})
	return outcome.Text, err
}

// providerRepositoryCard is the card's details for an extension provider's repository: what approving saves, its
// provider, the name (the provider's own for it when the Agent gave none) and the address it is cloned from, which the
// provider may have chosen.
func providerRepositoryCard(binding RepositoryBinding) json.RawMessage {
	shown, _ := json.Marshal(struct {
		URL          string   `json:"url"`
		Provider     string   `json:"provider"`
		ProviderName string   `json:"provider_name"`
		Name         string   `json:"name"`
		Aliases      []string `json:"aliases"`
		Purpose      string   `json:"purpose"`
		CloneURL     string   `json:"clone_url"`
		TaskWorkflow string   `json:"task_workflow,omitempty"`
	}{binding.CanonicalURL, binding.Provider, binding.ProviderName, binding.Name, binding.Aliases, binding.Purpose,
		cmp.Or(binding.CloneURL, binding.CanonicalURL), binding.TaskWorkflow})
	return shown
}

// repositoryAddNotApplied marks only failures proven to precede card creation and Apply.
// saveRepository and other Apply errors deliberately keep their original uncertainty.
func repositoryAddNotApplied(err error) error {
	return fmt.Errorf("%w; nothing was applied (%w)", err, approvalops.ErrNotApplied)
}

func (s *Service) repositoryToolError(ctx context.Context, err error) error {
	code := "store"
	var problem repositoryProblem
	var check *repository.CheckError
	var provider *repository.ProviderError
	if errors.As(err, &problem) {
		code = string(problem)
	} else if errors.As(err, &check) {
		code = check.Code
	} else if errors.As(err, &provider) {
		code = providerCode(provider)
		return fmt.Errorf("repository_%s: %s", code, i18n.T(s.lang(ctx), "assistant-repositories."+code, providerArgs(provider)))
	}
	return fmt.Errorf("repository_%s: %s", code, i18n.T(s.lang(ctx), "assistant-repositories."+code))
}

func (s *Service) confirmRepositoryAdd(ctx context.Context, machine, self, tool, summary string, shown json.RawMessage, expires time.Time) (bool, string, error) {
	if machine == "" || self == "" {
		return false, "", errors.New("a calling conversation is required")
	}
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	now := time.Now().UTC()
	r, err := s.ask(ctx, proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self,
		Kind: proto.IxPermission, ToolName: tool, Summary: summary, ToolInput: shown, SessionTitle: "Persona",
		CreatedAt: now, ExpiresAt: expires})
	if err != nil {
		return false, "", err
	}
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	if r.UserAllowed() {
		return true, "", nil
	}
	if r.UserDenied() {
		return false, "The user declined the change: nothing changed.", nil
	}
	return false, prompts.RenderSettingUnanswered(), nil
}
