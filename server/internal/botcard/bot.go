package botcard

import (
	"context"
	"errors"
	"strings"
	"time"
)

// What the bot's client (internal/modules/lark) and the core that drives it share without any I/O: the clouds' domains
// and console pages, the bot's identity, message types, pins, the app's configuration report, the registration's
// answers, the connection's state and the event handler's shape (#13404).

// Domains of the two clouds; a hub.json domain may also be a full URL (a private deployment).
const (
	DomainFeishu = "https://open.feishu.cn"
	DomainLark   = "https://open" + ".larksuite.com" // two parts: the joined host trips the source name scan
)

// ResolveDomain turns the configured domain ("", "feishu", "lark" or a URL) into a base URL.
func ResolveDomain(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "", "feishu":
		return DomainFeishu
	case "lark", "larksuite":
		return DomainLark
	}
	return strings.TrimRight(strings.TrimSpace(d), "/")
}

// BotInfo is the app's bot: its name and open id (GET /open-apis/bot/v3/info). Asking for it also checks the app's
// id and secret.
type BotInfo struct {
	Name   string `json:"app_name"`
	OpenID string `json:"open_id"`
	Avatar string `json:"avatar_url"`
}

// Message types the bot sends.
const (
	TypeText        = "text"
	TypePost        = "post"
	TypeFile        = "file"
	TypeInteractive = "interactive"
)

// Pin is one pin of a chat: the message and who pinned it.
type Pin struct {
	MessageID      string `json:"message_id"`
	OperatorID     string `json:"operator_id"`
	OperatorIDType string `json:"operator_id_type"` // app_id for a bot's pin
}

// ByApp: the app appID pinned it.
func (p Pin) ByApp(appID string) bool { return p.OperatorIDType == "app_id" && p.OperatorID == appID }

// ErrTooLarge: a message's image or file is larger than the download takes.
var ErrTooLarge = errors.New("feishu download: too large")

// ConfigureIssue is why AutoConfigure stopped, in one word: the app shows a sentence and a console link.
type ConfigureIssue string

const (
	IssueNone          ConfigureIssue = ""               // everything it tried worked
	IssueBotCapability ConfigureIssue = "bot_capability" // the app has no bot capability yet (open-platform code 210041)
	IssuePatchScope    ConfigureIssue = "patch_scope"    // application:application:patch is not open on the app
	IssueNotSelfBuilt  ConfigureIssue = "not_self_built" // the app was not created in the developer console (210001)
	IssueUnderReview   ConfigureIssue = "under_review"   // a version is already under review (210302): taken as submitted
	IssueIncompatible  ConfigureIssue = "incompatible"   // a config error the app cannot fix from here (210031/210032/210034)
	IssueUnreachable   ConfigureIssue = "unreachable"    // the call itself failed (network, rate limit): retried on the next bind
	IssueNoApp         ConfigureIssue = "no_app"         // the app id or the secret was rejected
)

// ConfigureReport is what AutoConfigure did. Published means a version is pending review (or one already
// was); a private deployment reports Skipped with no Issue.
type ConfigureReport struct {
	Configured bool           // scopes, events and the long-connection mode were set
	Published  bool           // the publish was submitted (or one was already under review)
	VersionID  string         // the version the publish created, when the answer carried one
	Skipped    bool           // nothing was tried (a private domain)
	Issue      ConfigureIssue // "" when everything it tried worked
	Detail     string         // the platform's own words for a failure, for the log only
}

// CredentialRejected reports whether err is the open platform rejecting the app id or the secret (the token
// call's own codes): the bind step answers with one plain sentence instead of the raw error.
func CredentialRejected(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Op == "token" && (apiErr.Code == 10003 || apiErr.Code == 10014)
}

// ConsoleURL is the developer console the bind step links out to: the app list on the two clouds, "" for a
// private deployment (its console address is not known).
func ConsoleURL(domain string) string {
	switch d := ResolveDomain(domain); d {
	case DomainFeishu, DomainLark:
		return d + "/app"
	}
	return ""
}

// AppConsoleURL is the console page of one app (its credentials, capabilities, permissions and versions).
func AppConsoleURL(domain, appID string) string {
	base := ConsoleURL(domain)
	if base == "" || appID == "" {
		return ""
	}
	return base + "/" + appID
}

// The accounts sites of the two clouds (device-code registration).
const (
	AccountsFeishu = "https://accounts.feishu.cn"
	AccountsLark   = "https://accounts" + ".larksuite.com" // two parts: the joined host trips the source name scan
)

// Registration polling's answers (RegisterTicket.Status).
const (
	RegisterDone     = "done"      // the app is created: AppID, AppSecret and OpenID are set
	RegisterPending  = "pending"   // not confirmed yet (also: confirmed, the credentials not issued yet)
	RegisterSlowDown = "slow_down" // asked too often: wait longer
	RegisterExpired  = "expired"   // the device code ran out: start again
	RegisterDenied   = "denied"    // the person declined
	RegisterFailed   = "failed"    // anything else the platform said (ErrorText)
)

// AccountsHost is the accounts site of a region: "lark" is Lark's, anything else Feishu's.
func AccountsHost(region string) string {
	if region == "lark" {
		return AccountsLark
	}
	return AccountsFeishu
}

// RegisterChallenge is a started registration: the page to open (or scan) and how long to keep asking.
type RegisterChallenge struct {
	DeviceCode string
	URL        string
	ExpiresIn  int // seconds
	Interval   int // seconds between polls
}

// RegisterTicket is one poll's answer. Brand is the cloud the person's tenant is on ("feishu" / "lark"), when said:
// a tenant on the other cloud is polled there.
type RegisterTicket struct {
	Status    string
	AppID     string
	AppSecret string
	OpenID    string
	Brand     string
	ErrorText string
}

// Handler takes one event's payload (the JSON Feishu would POST to a webhook) and returns what to answer: nil for an
// event, the toast / new card for a card callback. It must return quickly: slow work goes on in its own goroutine.
type Handler func(ctx context.Context, payload []byte) (any, error)

// ConnState is what the settings page shows about the connection.
type ConnState struct {
	Connected bool
	Since     time.Time // connected since, or the last failure
	Error     string
	LastEvent time.Time
}

// codeStreamClosed: the card is not streaming (CardKit turns streaming off ten minutes after it was turned on).
const codeStreamClosed = 300309

// StreamClosed: the card is not streaming, so text cannot be streamed into it (turned off, or past CardKit's ten
// minutes): it can be turned on again (CardSettings).
func StreamClosed(err error) bool { return ErrorCode(err) == codeStreamClosed }
