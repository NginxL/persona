package githubwire

import (
	"context"
	"errors"
	"regexp"
	"sync"
)

// Where the token comes from (docs/extensions/connectors.md "Login supports"): one computer's gh login, which the user
// picks in the app (manager.json's github.auth.from_machine is only the default), or the device flow, which needs no
// computer.
const (
	SourceMachine = "machine"
	SourceDevice  = "device"
)

// TokenSource asks the machine for its gh login's token for host.
type TokenSource func(ctx context.Context, host string) (string, error)

// Why a token could not be had (TokenSource's errors, errors.Is).
var (
	ErrNoMachine   = errors.New("github.auth.from_machine is not one of the manager's machines")
	ErrOffline     = errors.New("the machine is offline")
	ErrNodeTooOld  = errors.New("the machine's personad is too old")
	ErrNoGh        = errors.New("gh is not installed")
	ErrNotSignedIn = errors.New("gh is not signed in")
)

// RequiredScope is a scope the connector needs and what does not work without it.
type RequiredScope struct {
	Scope   string   `json:"scope"`
	Affects []string `json:"affects"`
}

// MachineLogin is how a computer's gh login looks to GitHub.
type MachineLogin struct {
	// State: signed_in, not_signed_in (gh has no login, or GitHub refused it), no_gh, too_old (its personad),
	// offline, error.
	State   string          `json:"state"`
	Account string          `json:"account,omitempty"`
	Scopes  []string        `json:"scopes,omitempty"`
	Missing []RequiredScope `json:"missing,omitempty"`
	Error   string          `json:"error,omitempty"`
}

var tokenLike = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`\b(gh[opsur]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|[0-9a-f]{40})\b`)
})

// Scrub blanks anything that looks like a GitHub token in s (an error, a message): tokens never leave the hub.
func Scrub(s string) string { return tokenLike().ReplaceAllString(s, "[token]") }
