package extmgr

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Repository providers (docs/extensions/repository-providers.md): an extension's repository_providers contribution
// names the repositories on the hosts it claims. The assistant asks here (repository.Extensions) and nowhere else:
// lists never call a program, every call is bounded, and every answer is re-checked before it is returned. An
// address is sent only to the provider that claims its host, and never with credentials in it.

// repoCallTimeout bounds one repo.parse, repo.check, repo.link or repo.describe.
var repoCallTimeout = 5 * time.Second

// repoWordsMost is the most characters of a provider's words kept, the "…" of a longer one left out: an error's
// why stays within 200.
const repoWordsMost = 199

var _ repository.Extensions = (*Manager)(nil)

// startedNow says whether Run has started everything, without waiting for it.
func (h *Manager) startedNow() bool {
	select {
	case <-h.started:
		return true
	default:
		return false
	}
}

// RepositoryProviders are the installed repository_providers contributions, on or off; none before startup.
func (h *Manager) RepositoryProviders() []repository.ProviderInfo {
	if !h.startedNow() || h.extensions == nil {
		return nil
	}
	var out []repository.ProviderInfo
	for _, p := range h.extensions.reg.RepositoryProviders() {
		info, _ := h.extensions.repositoryProvider(p)
		out = append(out, info)
	}
	return out
}

// RepositoryProvider is the provider ext:<extension>/<contribution>, on or off, followed through the extension's
// renames: the answer's ID is the current one.
func (h *Manager) RepositoryProvider(id string) (repository.ProviderInfo, bool) {
	ext, cid, ok := repository.SplitExtensionProvider(id)
	if !ok || !h.startedNow() || h.extensions == nil {
		return repository.ProviderInfo{}, false
	}
	p, ok := h.extensions.reg.RepositoryProviderOf(ext, cid)
	if !ok {
		return repository.ProviderInfo{}, false
	}
	info, _ := h.extensions.repositoryProvider(p)
	return info, true
}

// RepositoryProviderForHost is the installed provider, on or off, that claims host.
func (h *Manager) RepositoryProviderForHost(hostName string) (repository.ProviderInfo, bool) {
	if !h.startedNow() || h.extensions == nil {
		return repository.ProviderInfo{}, false
	}
	p, ok := h.extensions.reg.RepositoryProviderForHost(hostName)
	if !ok {
		return repository.ProviderInfo{}, false
	}
	info, _ := h.extensions.repositoryProvider(p)
	return info, true
}

// RepositoryParse is the repository remote names, as provider's program says (repo.parse). An address that is not
// on the provider's hosts, or carries credentials, is never sent: the error is repository.ErrRemote (wrapped), the
// same as the registry's for a bad address. Other errors are *repository.ProviderError.
func (h *Manager) RepositoryParse(ctx context.Context, provider, remote string) (repository.Parsed, error) {
	info, cid, hp, err := h.repositoryTarget(ctx, provider)
	if info.ID != "" {
		if bad := sendableRemote(info, remote); bad != nil {
			return repository.Parsed{}, fmt.Errorf("%w: %s", repository.ErrRemote, answerWhy(bad))
		}
	}
	if err != nil {
		return repository.Parsed{}, err
	}
	var res wire.RepoParseResult
	if err := repositoryCall(ctx, info, hp, "repo.parse", wire.RepoParseParams{Contribution: cid, URL: remote}, &res); err != nil {
		return repository.Parsed{}, err
	}
	parsed, err := repository.CheckParseAnswer(info, repository.ParseAnswer{InstanceURL: res.InstanceURL, Path: res.Path,
		CanonicalURL: res.CanonicalURL, CloneURL: res.CloneURL, DisplayName: res.DisplayName})
	if err != nil {
		hostName, _ := repository.RemoteHost(remote)
		return repository.Parsed{}, h.extensions.refusedAnswer(info, hp, "repo.parse", hostName, err)
	}
	return parsed, nil
}

// RepositoryCheck asks id's provider whether the repository is there and its account can see it (repo.check).
func (h *Manager) RepositoryCheck(ctx context.Context, id repository.Identity) error {
	info, cid, hp, err := h.repositoryTarget(ctx, id.Provider)
	if err != nil {
		return err
	}
	if !repositoryOnHosts(info, id) {
		return &repository.ProviderError{Code: "failed", Provider: info.Name, Why: "the repository is not on its hosts"}
	}
	return repositoryCall(ctx, info, hp, "repo.check", wire.RepoCheckParams{Contribution: cid, CanonicalURL: id.CanonicalURL}, &wire.Empty{})
}

// RepositoryLink is one of repo's issues or pull requests, named by in's address or by its kind and number, as the
// provider says (repo.link). An address that is not on the provider's hosts, or carries credentials, is never sent
// (repository.ErrRemote, wrapped).
func (h *Manager) RepositoryLink(ctx context.Context, repo repository.Identity, in repository.LinkInput) (repository.TaskLink, error) {
	info, cid, hp, err := h.repositoryTarget(ctx, repo.Provider)
	if err != nil {
		return repository.TaskLink{}, err
	}
	if !repositoryOnHosts(info, repo) {
		return repository.TaskLink{}, &repository.ProviderError{Code: "failed", Provider: info.Name, Why: "the repository is not on its hosts"}
	}
	if in.URL != "" {
		if bad := sendableRemote(info, in.URL); bad != nil {
			return repository.TaskLink{}, fmt.Errorf("%w: %s", repository.ErrRemote, answerWhy(bad))
		}
	}
	params := wire.RepoLinkParams{Contribution: cid, URL: in.URL, Kind: in.Kind, Number: in.Number,
		Repo: wire.RepoIdentity{InstanceURL: repo.InstanceURL, Path: repo.Path, CanonicalURL: repo.CanonicalURL}}
	var res wire.RepoLinkResult
	if err := repositoryCall(ctx, info, hp, "repo.link", params, &res); err != nil {
		return repository.TaskLink{}, err
	}
	link, err := repository.CheckLinkAnswer(repo, in, repository.LinkAnswer{Kind: res.Kind, Number: res.Number,
		CanonicalURL: res.CanonicalURL, Title: res.Title})
	if err != nil {
		hostName, _ := repository.RemoteHost(repo.InstanceURL)
		return repository.TaskLink{}, h.extensions.refusedAnswer(info, hp, "repo.link", hostName, err)
	}
	return link, nil
}

// RepositoryDescribe is a stored link's title and state, as provider says (repo.describe, which a provider may
// leave out: then the error is failed).
func (h *Manager) RepositoryDescribe(ctx context.Context, provider, canonicalURL string) (repository.Description, error) {
	info, cid, hp, err := h.repositoryTarget(ctx, provider)
	if err != nil {
		return repository.Description{}, err
	}
	if !linkOnHosts(info, canonicalURL) {
		return repository.Description{}, &repository.ProviderError{Code: "failed", Provider: info.Name, Why: "the link is not on its hosts"}
	}
	var res wire.RepoDescribeResult
	if err := repositoryCall(ctx, info, hp, "repo.describe", wire.RepoDescribeParams{Contribution: cid, CanonicalURL: canonicalURL}, &res); err != nil {
		return repository.Description{}, err
	}
	return repository.CheckDescribeAnswer(res.Title, res.State), nil
}

// repositoryProvider is p as the assistant lists it, and its program when it is switched on (extension, no problem,
// contribution) and has one: a call goes to that program, which answers provider_off itself when it does not run
// (host.ErrNotRunning). It is listed available only when its program takes a call now (programAnswers), off otherwise.
func (ps *extensions) repositoryProvider(p extension.RepositoryProvider) (repository.ProviderInfo, *hubExtension) {
	info := repository.ProviderInfo{ID: repository.ExtensionProvider(p.Extension, p.Contribution), Name: p.Name, Hosts: p.Hosts,
		State: repository.ProviderOff}
	for _, k := range p.LinkKinds {
		info.LinkKinds = append(info.LinkKinds, repository.LinkLabel{Kind: k.Kind, Label: k.Label})
	}
	if !p.On {
		return info, nil
	}
	hp := ps.get(p.Extension)
	if hp == nil || hp.proc == nil {
		return info, nil
	}
	if programAnswers(hp.proc) {
		info.State = repository.ProviderAvailable
	}
	return info, hp
}

// programAnswers says whether proc takes a call now, as host.Proc.Call does: it is running, or starting (a call waits
// for it), or started by calls (on_call) and idle or failed, since the next call starts it again. One stopped or
// failed that a call does not start (it waits out a backoff before the host tries it again) and one without the
// protocol answer no call. It reads the program's state only, never asks it anything.
func programAnswers(proc *host.Proc) bool {
	spec := proc.Spec()
	if !spec.RPC {
		return false
	}
	switch state, _ := proc.Status(); state {
	case host.StateRunning, host.StateStarting:
		return true
	case host.StateStopped, host.StateError:
		return spec.OnCall
	}
	return false
}

// repositoryTarget is provider's contribution and the program to ask: no_provider when it is not installed (info.ID
// is then ""), provider_off when it is switched off or has no program (one that does not run answers provider_off
// when called). Without an
// installed provider the error names none, so the caller names it by what it saved (the provider_name kept with the
// repository). It waits for startup as long as ctx lets it.
func (h *Manager) repositoryTarget(ctx context.Context, provider string) (info repository.ProviderInfo, cid string, hp *hubExtension, err error) {
	ext, cid, ok := repository.SplitExtensionProvider(provider)
	if !ok {
		return info, "", nil, &repository.ProviderError{Code: "no_provider", Provider: provider}
	}
	if !h.startedNow() && !h.ready(ctx) || h.extensions == nil {
		return info, cid, nil, &repository.ProviderError{Code: "provider_off"}
	}
	p, ok := h.extensions.reg.RepositoryProviderOf(ext, cid)
	if !ok {
		return info, cid, nil, &repository.ProviderError{Code: "no_provider"}
	}
	info, hp = h.extensions.repositoryProvider(p)
	if hp == nil {
		return info, cid, nil, &repository.ProviderError{Code: "provider_off", Provider: info.Name}
	}
	return info, cid, hp, nil
}

// repositoryCall asks hp's program within repoCallTimeout. A failure is a *repository.ProviderError: the codes the
// protocol names pass, with the provider's words on one line, clipped; a program that does not run is provider_off.
func repositoryCall(ctx context.Context, info repository.ProviderInfo, hp *hubExtension, method string, params, out any) error {
	cctx, cancel := context.WithTimeout(ctx, repoCallTimeout)
	defer cancel()
	err := hp.proc.Call(cctx, method, params, out)
	if err == nil {
		return nil
	}
	pe := &repository.ProviderError{Code: "failed", Provider: info.Name}
	var re *rpc.Error
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		pe.Why = "no answer in time"
	case ctx.Err() != nil:
		pe.Why = "canceled"
	case cctx.Err() != nil:
		pe.Why = "no answer in " + repoCallTimeout.String()
	case errors.Is(err, host.ErrNotRunning):
		pe.Code = "provider_off"
	case !errors.As(err, &re):
		pe.Why = repoWords(err.Error())
	case re.Code == wire.RepoErrorCodeNotFound, re.Code == wire.RepoErrorCodeNotSignedIn, re.Code == wire.RepoErrorCodeRefused:
		pe.Code, pe.Why = re.Code, repoWords(re.Message)
	case re.Code == extension.CodeUnknownMethod, re.Code == extension.CodeNoContribution:
		pe.Why = "it does not provide repositories (an older version?)"
	default:
		pe.Why = repoWords(re.Message)
	}
	return pe
}

// repoWords is a provider's words as an error's why: one line with no control characters (the hub logs it), clipped.
func repoWords(s string) string {
	return providerWords(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s), repoWordsMost)
}

// refusedAnswer logs an answer that broke a rule, with the host and the rule only, and fails the call.
func (ps *extensions) refusedAnswer(info repository.ProviderInfo, hp *hubExtension, method, hostName string, err error) error {
	why := answerWhy(err)
	ps.logf("plugin %s: %s answer refused (%s): %s", hp.id, method, hostName, why)
	return &repository.ProviderError{Code: "failed", Provider: info.Name, Why: "an answer Persona refused: " + why}
}

// answerWhy is the rule an answer or address broke.
func answerWhy(err error) string {
	var ae *repository.AnswerError
	if errors.As(err, &ae) {
		return ae.Why
	}
	return err.Error()
}

// sendableRemote: remote may go to info's program. Beyond CheckProviderRemote, which reads credentials only in an
// http(s) address's query: an ssh or git@ address has no query or fragment (as the built-in rules say), and an http(s)
// address's fragment, read as a query, is held to the same rule.
func sendableRemote(info repository.ProviderInfo, remote string) error {
	if err := repository.CheckProviderRemote(info, remote); err != nil {
		return err
	}
	u, err := url.Parse(remote)
	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") || err == nil && u.Scheme == "ssh" {
		if strings.ContainsAny(remote, "?#") {
			return &repository.AnswerError{Why: "an ssh address with a query or fragment"}
		}
		return nil
	}
	if err != nil {
		return &repository.AnswerError{Why: "not an address"}
	}
	if u.Fragment == "" {
		return nil
	}
	probe := *u
	probe.RawQuery, probe.ForceQuery, probe.Fragment, probe.RawFragment = u.EscapedFragment(), false, "", ""
	return repository.CheckProviderRemote(info, probe.String())
}

// repositoryOnHosts: id is shaped as repo.parse answered it (an https instance on one of info's hosts, canonical_url
// = instance_url/path), so it may go to info's program.
func repositoryOnHosts(info repository.ProviderInfo, id repository.Identity) bool {
	u, err := url.Parse(id.InstanceURL)
	return err == nil && u.Scheme == "https" && u.User == nil && slices.Contains(info.Hosts, u.Hostname()) && id.Path != "" &&
		id.CanonicalURL == id.InstanceURL+"/"+id.Path
}

// linkOnHosts: raw is an https address on one of info's hosts with no credentials, query or fragment.
func linkOnHosts(info repository.ProviderInfo, raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 2048 && u.Scheme == "https" && u.User == nil && u.RawQuery == "" && !u.ForceQuery &&
		u.Fragment == "" && slices.Contains(info.Hosts, u.Hostname())
}
