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

// `personad ask`, `personad notify` and the direction write commands (internal/scriptcli) reach the manager through
// the node's socket like the hook does (docs/integrations/script-cli.md). Ask and notify use the "ask" and "notify"
// kinds; direction creation and progress use "direction.create" and "direction.progress".

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

// The node.sock kinds and statuses of the two direction writes. Neither write is resumable: a caller that did not get
// a reply must not send it again automatically.
const (
	DirectionCreateKind   = "direction.create"
	DirectionProgressKind = "direction.progress"

	DirectionOK          = "ok"
	DirectionInvalid     = "invalid"
	DirectionFailed      = "failed"
	DirectionUnavailable = "unavailable"
)

// DirectionCreateRequest is `personad create-direction`'s input. MachineID and SessionID explicitly place an existing
// session under the new direction; the caller never infers them from its directory, pane or transcripts.
type DirectionCreateRequest struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// DirectionProgressRequest is `personad write-progress`'s input. The three text fields always travel, including as
// empty strings: each write replaces the whole text summary rather than patching only the flags the caller supplied.
type DirectionProgressRequest struct {
	ID       string   `json:"id"`
	Reached  string   `json:"reached"`
	Doing    string   `json:"doing"`
	NeedsYou string   `json:"needs_you"`
	Links    []string `json:"links,omitempty"`
}

// DirectionReply is the node's answer to either direction write. Leader stays as JSON so this thin client does not
// import the manager's leader contract; a successful reply always carries an object.
type DirectionReply struct {
	Status string          `json:"status"`
	Leader json.RawMessage `json:"leader,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// CreateDirectionNode makes one direction.create request. It never retries, including after a transport failure.
func CreateDirectionNode(ctx context.Context, dial Dialer, req DirectionCreateRequest) (DirectionReply, error) {
	return directionNode(ctx, dial, DirectionCreateKind, req)
}

// WriteDirectionProgressNode makes one direction.progress request. It never retries, including after a transport
// failure, because the manager may already have taken the first write.
func WriteDirectionProgressNode(ctx context.Context, dial Dialer, req DirectionProgressRequest) (DirectionReply, error) {
	return directionNode(ctx, dial, DirectionProgressKind, req)
}

func directionNode(ctx context.Context, dial Dialer, kind string, input any) (DirectionReply, error) {
	in, _ := json.Marshal(input)
	line, err := dial(ctx, Request{Kind: kind, Input: in})
	if err != nil {
		return DirectionReply{}, err
	}
	var rep DirectionReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return DirectionReply{}, fmt.Errorf("bad answer from the Persona node: %w", err)
	}
	if rep.Status == "" && rep.Error != "" {
		// An old node answers an unknown kind with only {"error":"unknown kind"}.
		rep.Status = DirectionUnavailable
	}
	switch rep.Status {
	case DirectionOK:
		var leader map[string]json.RawMessage
		if len(rep.Leader) == 0 || json.Unmarshal(rep.Leader, &leader) != nil || leader == nil {
			return DirectionReply{}, errors.New("bad answer from the Persona node: an ok reply has no leader object")
		}
	case DirectionInvalid, DirectionFailed, DirectionUnavailable:
	default:
		return DirectionReply{}, fmt.Errorf("bad answer from the Persona node: unknown status %q", rep.Status)
	}
	return rep, nil
}
