package nodeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// `personad ask` and `personad notify` (internal/scriptcli) reach the phone through the node's socket like the hook
// does (docs/integrations/script-cli.md): an ask is a "ask" request, a notification a "notify" one (node/scriptcli.go serves both).

// Ask results (AskReply.Status).
const (
	AskAnswered    = "answered"    // an option was chosen (Answer), or the card was allowed
	AskDenied      = "denied"      // the card was denied
	AskDismissed   = "dismissed"   // handed back to the computer, or answered without an answer
	AskTimeout     = "timeout"     // nobody answered in time
	AskUnavailable = "unavailable" // there is no hub to ask, or asking it failed
	AskInvalid     = "invalid"     // the request is wrong (Error says how)
)

// Limits of an ask: the phone draws a short list of short labels.
const (
	AskOptionsMost  = 12
	AskOptionMost   = 100 // runes
	AskQuestionMost = 1000
	// AskTimeoutMost bounds how long one ask waits for the phone.
	AskTimeoutMost = 24 * time.Hour
)

// AskRequest is `personad ask`'s question.
type AskRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"` // none: allow / deny
	// Format opts the question's card into rich rendering. Only "markdown" is preserved; everything else stays plain.
	Format string `json:"format,omitempty"`
	// TimeoutSeconds: 0 is HookTimeout.
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Cwd            string `json:"cwd,omitempty"` // where the caller runs: names the card when no session does
}

// Normalize trims the question and the options (empty and repeated ones go) and says what is wrong with the request.
func (a *AskRequest) Normalize() error {
	a.Question = strings.TrimSpace(a.Question)
	a.Format = strings.TrimSpace(a.Format)
	if a.Format != "markdown" {
		a.Format = ""
	}
	switch {
	case a.Question == "":
		return errors.New("the question is empty")
	case utf8.RuneCountInString(a.Question) > AskQuestionMost:
		return fmt.Errorf("the question is longer than %d characters", AskQuestionMost)
	case a.TimeoutSeconds < 0 || time.Duration(a.TimeoutSeconds)*time.Second > AskTimeoutMost:
		return fmt.Errorf("the timeout must be between 1 second and %s", AskTimeoutMost)
	}
	var opts []string
	seen := map[string]bool{}
	for _, o := range a.Options {
		o = strings.TrimSpace(o)
		if o == "" || seen[o] {
			continue
		}
		if utf8.RuneCountInString(o) > AskOptionMost {
			return fmt.Errorf("an option is longer than %d characters", AskOptionMost)
		}
		seen[o] = true
		opts = append(opts, o)
	}
	if len(opts) > AskOptionsMost {
		return fmt.Errorf("more than %d options", AskOptionsMost)
	}
	a.Options = opts
	return nil
}

// Timeout is how long the ask waits for the phone.
func (a AskRequest) Timeout() time.Duration {
	if a.TimeoutSeconds <= 0 {
		return HookTimeout
	}
	return time.Duration(a.TimeoutSeconds) * time.Second
}

// AskReply is the node's answer to an ask.
type AskReply struct {
	Status string `json:"status"`
	Answer string `json:"answer,omitempty"` // the option chosen (or what was typed on the card)
	Error  string `json:"error,omitempty"`
	// Retry: the node is going away under the question (a hub or node restart); ask the next one with the same request.
	Retry bool `json:"retry,omitempty"`
}

// NotifyRequest is `personad notify`'s notification.
type NotifyRequest struct {
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Silent bool   `json:"silent,omitempty"` // no sound
	Error  bool   `json:"error,omitempty"`  // time-sensitive, marked ⚠️
}

// NotifyReply is the node's answer to a notification: Sent is the hub having taken it (not the phone showing it).
type NotifyReply struct {
	Sent   bool   `json:"sent"`
	Reason string `json:"reason,omitempty"` // why not, when the hub did not send it (muted)
	// Queued: the manager is away; the node keeps it and hands it over when the manager is back (standby).
	Queued bool   `json:"queued,omitempty"`
	Error  string `json:"error,omitempty"` // why the hub could not be asked
}

// AskNode asks the node to put a question in front of the phone and returns the answer. Like Hook, when the node goes
// away while the phone is deciding it reconnects to the next one with the same request (DialResumable). The error is
// ErrNoNode when nothing listens on the socket, else a transport failure; both mean the phone could not be asked.
func AskNode(ctx context.Context, dial Dialer, a AskRequest, pane string) (AskReply, error) {
	in, _ := json.Marshal(a)
	line, err := DialResumable(ctx, dial, Request{Kind: "ask", Input: in, Pane: pane, Attempt: NewAttempt()})
	if err != nil {
		return AskReply{}, err
	}
	var rep AskReply
	_ = json.Unmarshal(line, &rep)
	return rep, nil
}

// NotifyNode asks the node to send a notification. The error is ErrNoNode when nothing listens on the socket, else a
// transport failure; a failure on the node's or the hub's side is NotifyReply.Error.
func NotifyNode(ctx context.Context, dial Dialer, nr NotifyRequest, pane string) (NotifyReply, error) {
	in, _ := json.Marshal(nr)
	line, err := dial(ctx, Request{Kind: "notify", Input: in, Pane: pane})
	if err != nil {
		return NotifyReply{}, err
	}
	var rep NotifyReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return NotifyReply{}, fmt.Errorf("bad answer from the Persona node: %w", err)
	}
	return rep, nil
}
