package assistant

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Repository providers (docs/extensions/repository-providers.md): GitHub and GitLab work offline as before; an
// extension's repository_providers contribution answers for the hosts it claims (s.repoExt, the extensions' manager).
// A provider is never asked inside a store transaction or under todos.writing, and the calls of one request share one
// repository.CheckTimeout budget.

// The extensions' manager is what SetExtensions picks the providers up from, by assertion: a change to either side
// that would quietly leave the manager without them fails the build instead.
var _ interface {
	repository.Extensions
	Started() <-chan struct{}
} = (*extmgr.Manager)(nil)

// repositoryRegistry routes an address to its provider; without the extensions only the built-ins answer.
func (s *Service) repositoryRegistry() repository.Registry {
	return repository.Registry{Extensions: s.repoExt}
}

// repositoryProviders are the built-ins, then the extensions' providers by name and id.
func (s *Service) repositoryProviders() []repository.ProviderInfo {
	out := repository.Builtins()
	if s.repoExt == nil {
		return out
	}
	ext := slices.Clone(s.repoExt.RepositoryProviders())
	slices.SortStableFunc(ext, func(a, b repository.ProviderInfo) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return append(out, ext...)
}

// repositoryProvider is an installed extension provider by id (on or off; no call).
func (s *Service) repositoryProvider(id string) (repository.ProviderInfo, bool) {
	if s.repoExt == nil {
		return repository.ProviderInfo{}, false
	}
	return s.repoExt.RepositoryProvider(id)
}

type providerBudgetKey struct{}

// providerBudget is how long one request's provider calls may take together (repository.CheckTimeout; tests shorten it).
var providerBudget = repository.CheckTimeout

// withProviderBudget starts a request's provider budget: the provider calls made under ctx (parse, check, link,
// describe), and a wait for the providers to be listed, end together after providerBudget. The store's own work is not
// bound by it.
func withProviderBudget(ctx context.Context) context.Context {
	if _, ok := ctx.Value(providerBudgetKey{}).(time.Time); ok {
		return ctx
	}
	return context.WithValue(ctx, providerBudgetKey{}, time.Now().Add(providerBudget))
}

// providerCall is ctx for one provider call, ending with its request's budget (a budget of its own when none started).
func providerCall(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Value(providerBudgetKey{}).(time.Time)
	if !ok {
		deadline = time.Now().Add(providerBudget)
	}
	return context.WithDeadline(ctx, deadline)
}

// providerProblem is err as a provider error that names the provider: its own words, else saved (the name kept with
// the repository), else the contribution's id. An answer Persona refused, or any other error, is failed.
func providerProblem(err error, provider, saved string) *repository.ProviderError {
	var out repository.ProviderError
	var pe *repository.ProviderError
	var answer *repository.AnswerError
	switch {
	case errors.As(err, &pe):
		out = *pe
	case errors.As(err, &answer):
		out = repository.ProviderError{Code: "failed", Why: "an answer Persona refused: " + answer.Why}
	default:
		out = repository.ProviderError{Code: "failed", Why: err.Error()}
	}
	if out.Provider == "" {
		out.Provider = saved
	}
	if out.Provider == "" {
		_, out.Provider, _ = repository.SplitExtensionProvider(provider)
	}
	if utf8.RuneCountInString(out.Why) > 200 {
		out.Why = string([]rune(out.Why)[:199]) + "…"
	}
	return &out
}

// noProvider is the error for a provider that is not installed.
func noProvider(provider, saved string) *repository.ProviderError {
	return providerProblem(&repository.ProviderError{Code: "no_provider"}, provider, saved)
}

// providerCode is a provider error's code as the catalog (repository_<code>) and the to-dos (task_repositories_<code>)
// say it; an unknown one is provider_failed.
func providerCode(pe *repository.ProviderError) string {
	switch pe.Code {
	case "no_provider", "provider_off", providersStartingCode:
		return pe.Code
	case "not_found", "not_signed_in", "refused":
		return "provider_" + pe.Code
	}
	return "provider_failed"
}

// providerArgs are a provider error's text placeholders.
func providerArgs(pe *repository.ProviderError) i18n.Args {
	return i18n.Args{"provider": pe.Provider, "why": pe.Why}
}

// repositoryBinding is a catalog input normalized by its provider: the built-ins offline as before (binding), an
// extension's address through it (repo.parse). A PUT that sends back a saved extension repository's own address keeps
// that identity without asking anyone, so its details stay editable while the provider is off or gone.
func (s *Service) repositoryBinding(ctx context.Context, in repositoryInput, id string) (RepositoryBinding, error) {
	if saved, ok := s.savedProviderRepository(ctx, in, id); ok {
		out := RepositoryBinding{Identity: saved.Identity, ProviderName: saved.ProviderName}
		if in.CloneURL != nil {
			if *in.CloneURL == "" || *in.CloneURL != saved.CloneURL {
				if err := s.providersReady(ctx, saved.Provider, saved.ProviderName); err != nil {
					return out, err
				}
				info, ok := s.repositoryProvider(saved.Provider)
				if !ok {
					return out, noProvider(saved.Provider, saved.ProviderName)
				}
				if repository.CheckProviderClone(info, *in.CloneURL) != nil {
					return out, repositoryProblem("remote")
				}
			}
			out.CloneURL = *in.CloneURL
		}
		return in.details(out)
	}
	if providerAddress(in) {
		// Right after a restart the providers are not listed yet: wait for them rather than call one missing.
		if err := s.providersReady(ctx, in.Provider, ""); err != nil {
			return RepositoryBinding{}, err
		}
	}
	registry := s.repositoryRegistry()
	info, extension, err := registry.Provider(in.URL, in.Provider)
	if err != nil {
		return RepositoryBinding{}, catalogProviderError(err, in.Provider, "")
	}
	if !extension {
		return in.binding()
	}
	call, cancel := providerCall(ctx)
	parsed, err := registry.Parse(call, in.URL, info.ID)
	cancel()
	if err != nil {
		return RepositoryBinding{}, catalogProviderError(err, info.ID, info.Name)
	}
	// A repository saved before its extension was renamed keeps the identity it was saved with.
	out := RepositoryBinding{Identity: s.catalogIdentity(ctx, id, parsed.Identity), ProviderName: cmp.Or(parsed.ProviderName, info.Name),
		CloneURL: parsed.CloneURL}
	if in.CloneURL != nil {
		if repository.CheckProviderClone(info, *in.CloneURL) != nil {
			return out, repositoryProblem("remote")
		}
		out.CloneURL = *in.CloneURL
	}
	if strings.TrimSpace(in.Name) == "" && repositoryText(parsed.DisplayName, 80) {
		in.Name = parsed.DisplayName
	}
	return in.details(out)
}

// providerAddress: in names an extension provider, or an address on a host no built-in owns (one a provider may claim).
func providerAddress(in repositoryInput) bool {
	if in.Provider != "" {
		return repository.IsExtensionProvider(in.Provider)
	}
	host, ok := repository.RemoteHost(in.URL)
	return ok && !slices.Contains(repository.BuiltinHosts, host)
}

// providersReady waits, within ctx's provider budget, for the providers to be listed (right after a restart); still
// not listed, the error is providers_starting, never a provider missing or an address no one takes.
func (s *Service) providersReady(ctx context.Context, provider, saved string) error {
	if s.providersListed() {
		return nil
	}
	wait, cancel := providerCall(ctx)
	s.awaitProviders(wait)
	cancel()
	if s.providersListed() {
		return nil
	}
	return providerProblem(providersStarting, provider, saved)
}

// catalogIdentity is identity as the catalog saved it: a saved repository that is the same one under an earlier id of
// its extension (sameRepository) keeps its own, id's first. No call.
func (s *Service) catalogIdentity(ctx context.Context, id string, identity repository.Identity) repository.Identity {
	if !repository.IsExtensionProvider(identity.Provider) {
		return identity
	}
	doc, err := s.repositoryDocument(ctx)
	if err != nil {
		return identity
	}
	return s.savedIdentity(doc, id, identity)
}

// savedIdentity is catalogIdentity for doc.
func (s *Service) savedIdentity(doc repositoryDocument, id string, identity repository.Identity) repository.Identity {
	if !repository.IsExtensionProvider(identity.Provider) {
		return identity
	}
	saved := make([]repository.Identity, 0, len(doc.Repositories))
	for _, b := range doc.Repositories {
		if b.ID == id {
			saved = append([]repository.Identity{b.Identity}, saved...)
		} else {
			saved = append(saved, b.Identity)
		}
	}
	return s.storedIdentity(identity, saved)
}

// catalogProviderError is a failed parse as the catalog says it: an address no provider takes is remote.
func catalogProviderError(err error, provider, saved string) error {
	if errors.Is(err, repository.ErrRemote) {
		return repositoryProblem("remote")
	}
	return providerProblem(err, provider, saved)
}

// savedProviderRepository is the saved extension repository a PUT of id sends back unchanged (its canonical address,
// and its provider, under the id it was saved with or its extension's current one, or none). Built-in addresses never
// match, so their path is the one it always was.
func (s *Service) savedProviderRepository(ctx context.Context, in repositoryInput, id string) (RepositoryBinding, bool) {
	if id == "" || in.Provider == repository.GitHub || in.Provider == repository.GitLab {
		return RepositoryBinding{}, false
	}
	if host, ok := repository.RemoteHost(in.URL); !ok || slices.Contains(repository.BuiltinHosts, host) {
		return RepositoryBinding{}, false
	}
	doc, err := s.repositoryDocument(ctx)
	if err != nil {
		return RepositoryBinding{}, false
	}
	for _, saved := range doc.Repositories {
		if saved.ID == id {
			return saved, repository.IsExtensionProvider(saved.Provider) && in.URL == saved.CanonicalURL &&
				(in.Provider == "" || s.sameProvider(in.Provider, saved.Provider))
		}
	}
	return RepositoryBinding{}, false
}

// checkProviderRepository asks an extension provider whether its repository exists (repo.check).
func (s *Service) checkProviderRepository(ctx context.Context, id repository.Identity) error {
	if s.repoExt == nil {
		return noProvider(id.Provider, "")
	}
	call, cancel := providerCall(ctx)
	defer cancel()
	if err := s.repoExt.RepositoryCheck(call, id); err != nil {
		return providerProblem(err, id.Provider, "")
	}
	return nil
}
