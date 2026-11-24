package mail

import (
	"net/http"

	"github.com/nwverse/persona/server/internal/i18n"
)

// UnknownAccountError names an account that is no longer connected to this manager. It keeps the expected
// failure's code, status and localized summary when a caller exposes it through another API, such as translation.
type UnknownAccountError struct {
	Account string
}

func (e *UnknownAccountError) Error() string { return e.Text(i18n.En) }

// Text is the account-specific explanation in the client's language.
func (e *UnknownAccountError) Text(l i18n.Lang) string {
	return i18n.T(l, "mail-errors.account_unknown", i18n.Args{"account": e.Account})
}

func (*UnknownAccountError) Code() string    { return "mail_account_unknown" }
func (*UnknownAccountError) HTTPStatus() int { return http.StatusNotFound }

// IncompleteSearchError means at least one account could not be read in an all-account search or inbox listing.
// It deliberately excludes upstream details, which can contain credentials or account identifiers.
type IncompleteSearchError struct{}

func (*IncompleteSearchError) Error() string { return i18n.T(i18n.En, "mail-errors.search_incomplete") }

// Text describes the incomplete result in the client's language.
func (*IncompleteSearchError) Text(l i18n.Lang) string {
	return i18n.T(l, "mail-errors.search_incomplete")
}

func (*IncompleteSearchError) Code() string    { return "mail_search_incomplete" }
func (*IncompleteSearchError) HTTPStatus() int { return http.StatusBadGateway }

// UnreachableError is an account whose server could not be reached, or whose connection ended under the operation,
// on the first try and again on the one reconnect (withConnAt). It says so in words; the raw error is in the log and
// stays in its chain for errors.Is / errors.As.
type UnreachableError struct {
	Account string
	err     error
}

func (e *UnreachableError) Error() string { return e.Text(i18n.En) }

// Text says it in the client's language.
func (*UnreachableError) Text(l i18n.Lang) string { return i18n.T(l, "mail-errors.unreachable") }

func (e *UnreachableError) Unwrap() error { return e.err }
func (*UnreachableError) Code() string    { return "mail_unreachable" }
func (*UnreachableError) HTTPStatus() int { return http.StatusBadGateway }
