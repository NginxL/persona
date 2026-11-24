package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Per-chat rules (#5278): from a row of the sent-as-user ledger the user can say "don't ask again in this chat", which
// adds a rule that lets writes into that one chat through without a card. The rules are the gate's own (Rule), made
// by ChatRuleFor from what the row says and never from anything the user typed, and checked again by UserRule.Validate
// wherever they are loaded or applied: a user's rule only allows, and only into one chat. They are kept by the
// manager (extmgr) and set on the Registry (SetUserRules), after the hand-written connector_gate rules, so a
// hand-written deny or confirm that matches still wins; ConfirmAlways and a health conversation still clamp an allow
// to a card, as for any other rule.
//
// What "one chat" is, per connector (the shapes below are the only ones there are):
//
//   - Lark: the messages of one conversation, `POST /conversations/<id>/messages` ("<METHOD> <path>", how the
//     node's gate reads a call: without the gate's path prefix and the query). The id is a path segment the node
//     compares literally, so a pattern cannot stand in for it, and one with a pattern character is refused. The rule
//     is the route and the conversation: what the body says is not matched (a text may mention people, or quote a
//     message, as a confirmed one could). Not covered, and so still asked: a reply (`POST /messages/<id>/replies`
//     names no conversation), images, files and attachments (a file leaves the computer, which a row that said text
//     did not allow), edits, reactions and everything else of a conversation.
//   - GitHub: `github_comment` on one repository and issue or pull request number: Match {repo, number}, the
//     argument fields the gate sees (the tool's own, prompts/mcp/github). A review is another action and stays asked.
//
// Mail, an extension's own record (sent.record) and any connector this file does not know have no one-chat shape:
// ChatRuleFor says so with a reason.

// githubConnector and githubComment are the GitHub connector's id and its action that comments on an issue or a pull
// request (modules/github).
const (
	githubConnector = "github"
	githubComment   = "github_comment"
	githubReview    = "github_review"
)

// larkMessages is the one route of a conversation a Lark rule of the user's allows; larkCreationRoutes are the routes
// that create a message there, whose rows offer the rule (the same four as the ledger's: extmgr nodeCreationRoutes).
const larkMessages = "messages"

var larkCreationRoutes = []string{larkMessages, "images", "files", "attachments"}

var (
	// larkConversation is a conversation id fit for a rule's path: letters, digits and a few separators, no "/", no
	// space and no character a pattern uses (* ? [ \), and not "." or "..".
	larkConversation = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.:@=+-]{0,199}$`)
	// githubRepo is "owner/name" (the connector's own check, without "..": githubRepoOK adds that); githubRef is a
	// row's target.ref, "owner/name#12": a whole number from 1 and at most nine digits, so a JSON number holds it
	// exactly.
	githubRepo = lazyre.New(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	githubRef  = lazyre.New(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]{0,8})$`)
)

// githubRepoOK is "owner/name" with nothing a pattern uses and nothing that changes an API path ("..", as the
// connector checks it).
func githubRepoOK(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && githubRepo.MatchString(repo) && owner != "." && owner != ".." && name != "." && name != ".."
}

// UserRule is one rule the user made from a row of the ledger. Row and Summary say where it came from, for the list the
// user sees; they play no part in what the gate does.
type UserRule struct {
	Rule    Rule      `json:"rule"`
	At      time.Time `json:"at,omitzero"`       // when it was made
	Row     int64     `json:"row,omitempty"`     // the ledger row it was made from (the ledger is pruned, the row may be gone)
	Summary string    `json:"summary,omitempty"` // that row's one line
}

// Chat is the chat the rule is for: a Lark conversation's id, or "owner/name#number" for GitHub; "" for a rule that is
// not one-chat shaped.
func (u UserRule) Chat() string {
	id, _ := chatOf(u.Rule)
	_, chat, _ := strings.Cut(id, ":")
	return chat
}

// ID names the chat the rule is for, the same for the same chat: "lark:<conversation>" or
// "github:<owner/name>#<number>"; "" for a rule that is not one-chat shaped (Validate says why). Two rules of one chat
// are one rule: adding the chat again changes nothing.
func (u UserRule) ID() string {
	id, _ := chatOf(u.Rule)
	return id
}

// Validate says whether the rule is one the gate may run for a user: it allows (a user's rule never confirms or
// denies), it is exactly the one-chat shape ChatRuleFor makes, and the gate can use it (ValidateRules).
func (u UserRule) Validate() error {
	if u.Rule.Decision != Allow {
		return fmt.Errorf("a rule of the user's only allows, not %q", u.Rule.Decision)
	}
	if _, ok := chatOf(u.Rule); !ok {
		return errors.New("a rule of the user's allows writes into one chat and nothing wider: a Lark conversation's " +
			"messages, or one GitHub issue's or pull request's comments")
	}
	if err := ValidateRules([]Rule{u.Rule}); err != nil {
		return fmt.Errorf("the gate cannot use it: %s", strings.TrimPrefix(err.Error(), "connector_gate[0]: "))
	}
	return nil
}

// chatOf reads a rule back as one of the two one-chat shapes, the way ChatRuleFor builds them, and says which chat it
// pins; false for anything else (a pattern where an id should be, an extra match field, another action).
func chatOf(r Rule) (string, bool) {
	switch {
	case r.Connector == Lark:
		conv, route, ok := larkWrite(r.Action)
		if !ok || route != larkMessages || len(r.Match) != 0 {
			return "", false
		}
		return Lark + ":" + conv, true
	case r.Connector == githubConnector && r.Action == githubComment:
		repo, ok := r.Match["repo"].(string)
		n, whole := wholeNumber(r.Match["number"])
		if len(r.Match) != 2 || !ok || !whole || !githubRepoOK(repo) || n < 1 || n > 999999999 {
			return "", false
		}
		return githubConnector + ":" + repo + "#" + strconv.FormatInt(n, 10), true
	}
	return "", false
}

// wholeNumber is v as a whole number: a rule read from JSON has float64s, one built in Go may have an int (userRuleSet
// gives the gate a float64 either way).
func wholeNumber(v any) (int64, bool) {
	switch v := v.(type) {
	case float64:
		n := int64(v)
		return n, float64(n) == v
	case int:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}

// larkPath reads a Lark action as `POST /conversations/<id>/<route>`, exactly: no trimming, no extra segment. A write
// that is not that shape (a reply, another method) is not one. The id is whatever the segment is.
func larkPath(action string) (conversation, route string, ok bool) {
	method, p, found := strings.Cut(action, " ")
	if !found || method != "POST" || !strings.HasPrefix(p, "/") {
		return "", "", false
	}
	segs := strings.Split(p[1:], "/")
	if len(segs) != 3 || segs[0] != "conversations" || segs[2] == "" {
		return "", "", false
	}
	return segs[1], segs[2], true
}

// larkWrite is larkPath with a conversation id fit for a rule (larkConversation).
func larkWrite(action string) (conversation, route string, ok bool) {
	conversation, route, ok = larkPath(action)
	return conversation, route, ok && larkConversation.MatchString(conversation)
}

// The reasons ChatRuleFor gives for a row that offers no rule (NoChatRuleError.Code), stable for a client to word.
const (
	NoChatRecorded = "recorded" // an extension recorded the send itself (sent.record): it never passed the gate
	NoChatMail     = "mail"     // mail goes to people, not into a chat
	NoChatReview   = "review"   // a GitHub review (approve, request changes) is not a comment
	NoChatReply    = "reply"    // a Lark reply: its path names the message, not the conversation
	NoChatRoute    = "route"    // another write of a connector that has a one-chat shape
	NoChatTarget   = "target"   // the row does not name a chat that can be pinned
	NoChatCommand  = "command"  // a Lark row made through a command that is not the Lark gate's (the caller checks, ChatRuleFor cannot)
	NoChatUnknown  = "unknown"  // a connector with no one-chat shape
)

// NoChatRuleError is why a row offers no one-chat rule. Code is one of the NoChat… constants.
type NoChatRuleError struct {
	Code   string
	Reason string
}

func (e *NoChatRuleError) Error() string { return "no rule for one chat: " + e.Reason }

func noChatRule(code, format string, args ...any) error {
	return &NoChatRuleError{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// ChatRuleFor is the narrowest rule that lets writes into the chat a ledger row went to through without a card, from
// the row's connector, action and target (db.SentAsUser's fields), or a *NoChatRuleError saying why the row offers
// none. Pure: nothing is stored and the clock is not read (At is the caller's).
//
// The row's target.command (the gated command a Lark write went through) is not looked at here: this package does not
// know which commands are the Lark gate's, and a row of the node's gate proxy says "lark" for any gate with credentials.
// The caller that knows the gates refuses the rows of other commands and the rows that name none, before this is asked
// (extmgr chatRuleOfRow, NoChatCommand).
//
// It builds a rule only when the row's action is itself a write into a chat of a known shape, and the row's target
// names the same chat: a row an extension recorded, a mail, a Lark reply, a GitHub review and anything it does not know
// give none, and no rule it gives has a "*" or any pattern, or an empty match, in it.
func ChatRuleFor(connectorID, action string, target json.RawMessage) (UserRule, error) {
	switch {
	case action == MethodSentRecord:
		return UserRule{}, noChatRule(NoChatRecorded, "an extension recorded this send itself: it did not pass the gate, "+
			"so no rule changes whether it asks")
	case connectorID == "mail" || action == "mail_send":
		return UserRule{}, noChatRule(NoChatMail, "mail goes to people, not into a chat: each mail is confirmed")
	case connectorID == Lark:
		return larkChatRule(action, target)
	case connectorID == githubConnector:
		return githubChatRule(action, target)
	}
	return UserRule{}, noChatRule(NoChatUnknown, "the gate has no one-chat rule for %q", connectorID)
}

func larkChatRule(action string, target json.RawMessage) (UserRule, error) {
	method, p, _ := strings.Cut(action, " ")
	if method == "POST" && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/replies") {
		return UserRule{}, noChatRule(NoChatReply, "a reply names the message it answers, not the conversation, so no rule "+
			"can pin it to one chat: replies are confirmed")
	}
	conv, route, ok := larkPath(action)
	if !ok {
		return UserRule{}, noChatRule(NoChatRoute, "%q is not a message sent into a conversation (POST /conversations/<id>/messages)", action)
	}
	if !slices.Contains(larkCreationRoutes, route) {
		return UserRule{}, noChatRule(NoChatRoute, "%q does not create a message in the conversation", action)
	}
	if !larkConversation.MatchString(conv) {
		return UserRule{}, noChatRule(NoChatTarget, "the conversation id %q has characters a rule cannot pin to one chat", conv)
	}
	var t struct {
		Conversation string `json:"conversation"`
	}
	_ = json.Unmarshal(target, &t)
	if t.Conversation != conv {
		return UserRule{}, noChatRule(NoChatTarget, "the row's target names conversation %q, its action %q: no rule is made "+
			"from a row that does not agree with itself", t.Conversation, conv)
	}
	return UserRule{Rule: Rule{Connector: Lark, Action: "POST /conversations/" + conv + "/" + larkMessages, Decision: Allow}}, nil
}

func githubChatRule(action string, target json.RawMessage) (UserRule, error) {
	switch action {
	case githubReview:
		return UserRule{}, noChatRule(NoChatReview, "a review (approve, request changes) is not a comment: reviews are confirmed")
	case githubComment:
	default:
		return UserRule{}, noChatRule(NoChatRoute, "%q is not a comment on an issue or a pull request", action)
	}
	var t struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal(target, &t)
	m := githubRef.FindStringSubmatch(t.Ref)
	if m == nil || !githubRepoOK(m[1]) {
		return UserRule{}, noChatRule(NoChatTarget, "the row's target does not name one issue or pull request (owner/name#number)")
	}
	n, _ := strconv.ParseInt(m[2], 10, 64)
	// number is a float64: the gate matches the arguments as JSON gave them (a number is a float64 there), and a Match
	// is compared with them as they are (reflect.DeepEqual), so an int would never match.
	return UserRule{Rule: Rule{Connector: githubConnector, Action: githubComment,
		Match: map[string]any{"repo": m[1], "number": float64(n)}, Decision: Allow}}, nil
}

// userRuleSet is the rules of list that pass Validate, in order and one to a chat (the first), and an error naming each
// that did not: a rule that fails is left out, which can only make the gate ask more.
func userRuleSet(list []UserRule) ([]Rule, error) {
	var out []Rule
	var errs []error
	seen := map[string]bool{}
	for i, u := range list {
		if err := u.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("user rule %d (%s %s): %w", i, u.Rule.Connector, u.Rule.Action, err))
			continue
		}
		if id := u.ID(); !seen[id] {
			seen[id] = true
			r := u.Rule
			r.fromUser = true
			r.Match = maps.Clone(r.Match)
			if n, whole := wholeNumber(r.Match["number"]); whole && r.Connector == githubConnector {
				// The gate compares a Match with the arguments as JSON gave them (reflect.DeepEqual), where a number is a
				// float64: a rule built in Go with an int would pass Validate and never match. It is set as the gate reads it.
				r.Match["number"] = float64(n)
			}
			out = append(out, r)
		}
	}
	return out, errors.Join(errs...)
}
