package githubwire

import (
	"context"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// Connector is the built-in GitHub connector as the extension manager drives it: a connector (connectorwire) with its
// status, options and sign-in, plus how its token is chosen, the gh logins it probes on the user's computers, the
// REST reads the assistant's checks use, and the CI-check snapshot of a pull request.
type Connector interface {
	connectorwire.Connector
	connectorwire.Accounts
	connectorwire.Options
	connectorwire.Runner
	// BuiltinGitHub marks the module's connector: an extension's GitHub connector (a proxy that implements every
	// connector interface) is never one.
	BuiltinGitHub()
	// UseSecrets hands it the store of its device-flow token.
	UseSecrets(connectorwire.Secrets)
	// Source is where the token comes from now (SourceMachine, SourceDevice); MachineID the computer's id for a machine.
	Source() string
	MachineID() string
	// UseMachine reads the token of computer id (name for messages) through fetch; UseDevice goes back to the device flow.
	UseMachine(ctx context.Context, id, name string, fetch TokenSource)
	UseDevice()
	// Scopes are what the token in use may do; known is false until GitHub said.
	Scopes() (scopes []string, known bool)
	// MissingScopes are the scopes a token with granted lacks and what goes wrong for it.
	MissingScopes(granted []string) []RequiredScope
	// Probe asks a computer for its gh login (fetch) and GitHub who it is and what it may do; the token goes nowhere.
	Probe(ctx context.Context, fetch TokenSource) MachineLogin
	// Get reads a REST path into out.
	Get(ctx context.Context, path string, out any) (http.Header, error)
	// PRChecks is a pull request's checks, refusing an incomplete read; PRChecksRateReset when the rate limit that
	// stopped it ends (zero when none did).
	PRChecks(ctx context.Context, repo string, number int) (PRCheckSnapshot, error)
	PRChecksRateReset() time.Time
}

// Factory makes the connector for cfg, reading the token of machine (its display name) through fetch; with a nil
// fetch the user signs in with the device flow.
type Factory func(cfg Config, machine string, fetch TokenSource) Connector
