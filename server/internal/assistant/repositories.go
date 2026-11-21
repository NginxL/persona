package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/i18n"
)

const repositoriesKey = "assistant.repositories"
const repositoriesMax = 200

// providersListWait is how long GET …/repositories/providers waits for the providers to be listed after a restart.
var providersListWait = 2 * time.Second

type RepositoryBinding struct {
	repository.Identity
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`
	Purpose  string   `json:"purpose,omitempty"`
	CloneURL string   `json:"clone_url,omitempty"`
	// ProviderName is an extension provider's name when the reference was saved (absent for GitHub and GitLab).
	ProviderName string    `json:"provider_name,omitempty"`
	VerifiedAt   time.Time `json:"verified_at"`
}

type repositoryRemoved struct {
	Binding RepositoryBinding `json:"binding"`
	Receipt string            `json:"receipt"`
	At      time.Time         `json:"at"`
}

type repositoryDocument struct {
	Revision     int64               `json:"revision"`
	Repositories []RepositoryBinding `json:"repositories"`
	Removed      []repositoryRemoved `json:"removed,omitempty"`
}

func (doc repositoryDocument) taskCatalog() repository.Catalog {
	out := repository.Catalog{Revision: doc.Revision, Repositories: make([]repository.CatalogEntry, 0, len(doc.Repositories))}
	for _, entry := range doc.Repositories {
		out.Repositories = append(out.Repositories, repository.CatalogEntry{Identity: entry.Identity, ID: entry.ID, CloneURL: entry.CloneURL,
			ProviderName: entry.ProviderName})
	}
	return out
}

type repositoryInput struct {
	URL      string   `json:"url"`
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Purpose  string   `json:"purpose"`
	CloneURL *string  `json:"clone_url,omitempty"`
}

type repositoryProblem string

func (e repositoryProblem) Error() string { return string(e) }

func repositoryText(value string, max int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	})
}

// binding is in for the built-in providers, offline (Service.repositoryBinding routes an extension provider's address).
func (in repositoryInput) binding() (RepositoryBinding, error) {
	id, err := repository.Parse(in.URL, in.Provider)
	if err != nil {
		return RepositoryBinding{}, repositoryProblem("remote")
	}
	out := RepositoryBinding{Identity: id}
	if in.CloneURL != nil {
		transport, err := repository.CloneTransport(*in.CloneURL, id)
		if err != nil {
			return out, repositoryProblem("remote")
		}
		out.CloneURL = transport
	}
	return in.details(out)
}

// details are in's name, purpose and aliases on out, checked.
func (in repositoryInput) details(out RepositoryBinding) (RepositoryBinding, error) {
	out.Name, out.Purpose = strings.TrimSpace(in.Name), strings.TrimSpace(in.Purpose)
	if !repositoryText(out.Name, 80) || !repositoryText(out.Purpose, 500) || len(in.Aliases) > 10 {
		return out, repositoryProblem("metadata")
	}
	for _, alias := range in.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || !repositoryText(alias, 80) {
			return out, repositoryProblem("metadata")
		}
		if !slices.ContainsFunc(out.Aliases, func(v string) bool { return strings.EqualFold(v, alias) }) {
			out.Aliases = append(out.Aliases, alias)
		}
	}
	return out, nil
}

func (s *Service) repositoryDocument(ctx context.Context) (repositoryDocument, error) {
	doc := repositoryDocument{Repositories: []RepositoryBinding{}}
	st := s.store()
	if st == nil {
		return doc, repositoryProblem("store")
	}
	if _, err := st.Setting(ctx, repositoriesKey, &doc); err != nil {
		return doc, repositoryProblem("store")
	}
	if doc.Repositories == nil {
		doc.Repositories = []RepositoryBinding{}
	}
	return doc, nil
}

// changeRepositories serializes local writers and uses the existing Settings CAS
// to refuse concurrent changes from another process. Failed reads never erase data.
func (s *Service) changeRepositories(ctx context.Context, change func(*repositoryDocument) error) error {
	s.repositoriesMu.Lock()
	defer s.repositoriesMu.Unlock()
	doc, err := s.repositoryDocument(ctx)
	if err != nil {
		return err
	}
	st, ok := s.store().(standingCASStore)
	if !ok {
		return repositoryProblem("store")
	}
	expected := doc.Revision
	if err := change(&doc); err != nil {
		return err
	}
	doc.Revision++
	saved, err := st.CompareSetting(ctx, repositoriesKey, expected, doc)
	if err != nil {
		return repositoryProblem("store")
	}
	if !saved {
		return repositoryProblem("conflict")
	}
	return nil
}

func (s *Service) checkRepository(ctx context.Context, id repository.Identity) error {
	if repository.IsExtensionProvider(id.Provider) {
		return s.checkProviderRepository(ctx, id)
	}
	if s.repositoryCheck != nil {
		return s.repositoryCheck(ctx, id)
	}
	cfg, err := s.readRepositoryConfig()
	if err != nil {
		return repositoryProblem("store")
	}
	return cfg.checker().Check(ctx, id)
}

func (s *Service) saveRepository(ctx context.Context, id string, binding RepositoryBinding) (RepositoryBinding, error) {
	err := s.changeRepositories(ctx, func(doc *repositoryDocument) error {
		index := slices.IndexFunc(doc.Repositories, func(b RepositoryBinding) bool { return b.ID == id })
		if id != "" && index < 0 {
			return repositoryProblem("not_found")
		}
		// A repository saved before its extension was renamed is the same one under the provider's current id.
		binding.Identity = s.savedIdentity(*doc, id, binding.Identity)
		if slices.ContainsFunc(doc.Repositories, func(b RepositoryBinding) bool { return b.ID != id && b.Identity == binding.Identity }) {
			return repositoryProblem("duplicate")
		}
		if id == "" && len(doc.Repositories) >= repositoriesMax {
			return repositoryProblem("full")
		}
		if index >= 0 && doc.Repositories[index].Identity == binding.Identity {
			binding.ID, binding.VerifiedAt = id, doc.Repositories[index].VerifiedAt
			if binding.CloneURL == "" {
				binding.CloneURL = doc.Repositories[index].CloneURL
			}
		} else {
			if err := s.checkRepository(ctx, binding.Identity); err != nil {
				return err
			}
			binding.VerifiedAt = time.Now().UTC()
			binding.ID = id
			if binding.ID == "" {
				binding.ID = "repo_" + randomHex(16)
			}
		}
		if index < 0 {
			doc.Repositories = append(doc.Repositories, binding)
		} else {
			doc.Repositories[index] = binding
		}
		return nil
	})
	return binding, err
}

func (s *Service) repositoryByID(ctx context.Context, id string) (RepositoryBinding, error) {
	doc, err := s.repositoryDocument(ctx)
	if err != nil {
		return RepositoryBinding{}, err
	}
	for _, binding := range doc.Repositories {
		if binding.ID == id {
			return binding, nil
		}
	}
	return RepositoryBinding{}, repositoryProblem("not_found")
}

func (s *Service) unbindRepository(ctx context.Context, id string) (string, error) {
	receipt := ""
	err := s.changeRepositories(ctx, func(doc *repositoryDocument) error {
		index := slices.IndexFunc(doc.Repositories, func(b RepositoryBinding) bool { return b.ID == id })
		if index < 0 {
			return repositoryProblem("not_found")
		}
		receipt = randomHex(16)
		doc.Removed = append(doc.Removed, repositoryRemoved{doc.Repositories[index], receipt, time.Now().UTC()})
		// A bounded undo history, kept in the manager's database rather than in a
		// browser draft. Retrying a lost DELETE cannot manufacture another receipt.
		if len(doc.Removed) > 20 {
			doc.Removed = doc.Removed[len(doc.Removed)-20:]
		}
		doc.Repositories = slices.Delete(doc.Repositories, index, index+1)
		return nil
	})
	return receipt, err
}

func (s *Service) restoreRepository(ctx context.Context, id, receipt string) (RepositoryBinding, error) {
	var binding RepositoryBinding
	err := s.changeRepositories(ctx, func(doc *repositoryDocument) error {
		index := slices.IndexFunc(doc.Removed, func(b repositoryRemoved) bool {
			return b.Binding.ID == id && b.Receipt == receipt && time.Since(b.At) < 10*time.Minute
		})
		if receipt == "" || index < 0 {
			return repositoryProblem("conflict")
		}
		binding = doc.Removed[index].Binding
		if len(doc.Repositories) >= repositoriesMax || slices.ContainsFunc(doc.Repositories, func(b RepositoryBinding) bool {
			return b.ID == id || s.sameRepository(b.Identity, binding.Identity)
		}) {
			return repositoryProblem("conflict")
		}
		doc.Repositories = append(doc.Repositories, binding)
		doc.Removed = slices.Delete(doc.Removed, index, index+1)
		return nil // restore saved evidence, never fabricate a new verification
	})
	return binding, err
}

func repositoryError(w http.ResponseWriter, err error) {
	code, status := "store", http.StatusServiceUnavailable
	var problem repositoryProblem
	var check *repository.CheckError
	var provider *repository.ProviderError
	switch {
	case errors.As(err, &problem):
		code = string(problem)
		status = map[string]int{"store": 503, "remote": 400, "metadata": 400, "duplicate": 409, "conflict": 409, "not_found": 404, "full": 409, "readonly": 403}[code]
	case errors.As(err, &check):
		code, status = check.Code, http.StatusUnprocessableEntity
	case errors.As(err, &provider):
		code, status = providerCode(provider), http.StatusUnprocessableEntity
		if code == "no_provider" || code == "provider_off" || code == providersStartingCode {
			status = http.StatusConflict
		}
		apierr.WriteCode(w, status, "repository_"+code, i18n.Err("assistant-repositories."+code, providerArgs(provider)))
		return
	}
	if status == 0 { // a code without a status is never written as one
		code, status = "store", http.StatusServiceUnavailable
	}
	apierr.WriteCode(w, status, "repository_"+code, i18n.Err("assistant-repositories."+code))
}

// Strict, bounded input excludes client-supplied verification, credentials, IDs,
// scope selectors and trailing JSON at every mutation/preview boundary.
func repositoryBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if !fullRole(r) {
		repositoryError(w, repositoryProblem("readonly"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		repositoryError(w, repositoryProblem("metadata"))
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		repositoryError(w, repositoryProblem("metadata"))
		return false
	}
	return true
}

func (s *Service) repositoryRoutes() {
	s.handle("GET /api/v1/assistant/repositories", func(w http.ResponseWriter, r *http.Request) {
		doc, err := s.repositoryDocument(r.Context())
		if err != nil {
			repositoryError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "repositories": doc.Repositories, "catalog_revision": doc.Revision})
	})
	s.handle("GET /api/v1/assistant/repositories/{id}", func(w http.ResponseWriter, r *http.Request) {
		binding, err := s.repositoryByID(r.Context(), r.PathValue("id"))
		if err != nil {
			repositoryError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "repository": binding})
	})
	// The sources of repositories: GitHub, GitLab, then the extensions' providers, on or off. Right after a restart it
	// waits a moment for the extensions to start, then answers the providers listed by then.
	s.handle("GET /api/v1/assistant/repositories/providers", func(w http.ResponseWriter, r *http.Request) {
		wait, cancel := context.WithTimeout(r.Context(), providersListWait)
		s.awaitProviders(wait)
		cancel()
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "providers": s.repositoryProviders()})
	})
	s.handle("POST /api/v1/assistant/repositories/preview", func(w http.ResponseWriter, r *http.Request) {
		var in repositoryInput
		if !repositoryBody(w, r, &in) {
			return
		}
		binding, err := s.repositoryBinding(withProviderBudget(r.Context()), in, "")
		if err != nil {
			repositoryError(w, err)
			return
		}
		out := map[string]any{"manager": s.managerIdentity(), "identity": binding.Identity}
		if binding.ProviderName != "" {
			out["provider_name"] = binding.ProviderName
		}
		apierr.WriteJSON(w, out)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var in repositoryInput
		if !repositoryBody(w, r, &in) {
			return
		}
		ctx := withProviderBudget(r.Context()) // the provider's parse and check share one budget
		binding, err := s.repositoryBinding(ctx, in, r.PathValue("id"))
		if err == nil {
			binding, err = s.saveRepository(ctx, r.PathValue("id"), binding)
		}
		if err != nil {
			repositoryError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "repository": binding})
	}
	s.handle("POST /api/v1/assistant/repositories", save)
	s.handle("PUT /api/v1/assistant/repositories/{id}", save)
	s.handle("DELETE /api/v1/assistant/repositories/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			repositoryError(w, repositoryProblem("readonly"))
			return
		}
		receipt, err := s.unbindRepository(r.Context(), r.PathValue("id"))
		if err != nil {
			repositoryError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "receipt": receipt})
	})
	s.handle("POST /api/v1/assistant/repositories/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Receipt string `json:"receipt"`
		}
		if !repositoryBody(w, r, &in) {
			return
		}
		binding, err := s.restoreRepository(r.Context(), r.PathValue("id"), in.Receipt)
		if err != nil {
			repositoryError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "repository": binding})
	})
}
