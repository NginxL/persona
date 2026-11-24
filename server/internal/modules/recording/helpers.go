package recording

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// The text helpers the assistant's package names so (internal/textfmt).
var (
	clip    = textfmt.Clip
	oneLine = textfmt.OneLine
	short   = textfmt.Short
)

// The core's names for what the module hands it: the card sources of its pushes (the assistant's card_layers.go), the
// kind of a meeting room's guest conversation and the Claude profile its conversations start in (meeting_profile.go).
const (
	cardMinutesReady = "minutes_ready"
	cardDayRecap     = "day_recap"
	guestKindMeeting = "meeting"
	meetingProfile   = "legacy"
)

// cutRunes is s cut to its first n runes, trimmed.
func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// unknownMethod: a node answered that it does not know the method (an older node).
func unknownMethod(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown method")
}

// nodeOutdated: the hub refused a call for something the node is too old for.
func nodeOutdated(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == proto.CodeNodeOutdated
}

// busTranscript reads a conversation's items through the hub (channel.ReadTurn's transcript).
type busTranscript struct {
	s                *Recordings
	machine, session string
}

func (b busTranscript) ItemsAfter(ctx context.Context, after int) ([]proto.Item, bool, int, error) {
	var res struct {
		Items     []proto.Item `json:"items"`
		MoreAfter bool         `json:"more_after"`
		ToSeq     int          `json:"to_seq"`
	}
	err := b.s.call(ctx, b.machine, DeviceSessionItems, map[string]any{"session_id": b.session, "after_seq": after, "lazy_images": true}, &res)
	return res.Items, res.MoreAfter, res.ToSeq, err
}

func (b busTranscript) Full(ctx context.Context, id string) (proto.Item, error) {
	var full proto.Item
	err := b.s.call(ctx, b.machine, "session.item", map[string]any{"session_id": b.session, "item_id": id}, &full)
	return full, err
}

// The roles a request comes with (assistantwire.RoleHeader; the assistant's handler.go names them so).
const (
	roleFull      = "full"
	roleExtension = "extension"
)

// fullRole: the request came from a device that may do everything (RoleHeader; none: a test's own request).
func fullRole(r *http.Request) bool {
	role := r.Header.Get(assistantwire.RoleHeader)
	return role == "" || role == roleFull
}

// todoWriter says whether r may change todos: a full device, or an extension token (todos.write).
func todoWriter(r *http.Request) bool {
	return fullRole(r) || r.Header.Get(assistantwire.RoleHeader) == roleExtension
}

// The knowledge-base routes' answers while it is off or not open (the assistant's kb_routes.go says them so).
var (
	errKBRouteOff     = i18n.Err("assistant-kb_routes.disabled")
	errKBRouteNotOpen = i18n.Err("assistant-kb_routes.not_open")
)

// spaceOf is the track a conversation is in ("" none).
func spaceOf(c proto.Session) string {
	if c.Space != "" {
		return c.Space
	}
	return identity.SpaceOf(c.Cwd)
}

// startWaitEnded: the call's wait ended (its context's deadline or cancel), here or as the hub relays it.
func startWaitEnded(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == "" &&
		(re.Message == context.DeadlineExceeded.Error() || re.Message == context.Canceled.Error())
}

// latestTurnError is the session's latest turn error and its kind, or the cut-off reply that ended its turn.
func latestTurnError(sess proto.Session) (text, kind string) {
	if text := strings.TrimSpace(sess.TurnError); text != "" {
		return text, sess.TurnErrorKind
	}
	if text := strings.TrimSpace(sess.LastText); assistantwire.CutOff(text) {
		return text, ""
	}
	return "", ""
}

// The to-dos of action items: which kind of action list (the core makes each to-do's id of it).
const (
	todosOfMinutes = "minutes"
	todosOfRecap   = "recap"
)

// actionTodos makes a to-do of each of the actions named of list (the core's to-dos, Host.ActionTodos), after
// checking each is one: an unknown one makes none.
func (s *Recordings) actionTodos(ctx context.Context, list []kb.MinutesAction, actions []string, kind, recording string, version int,
	note func(kb.MinutesAction) string) (out []any, made, byTask map[string]string, err error) {
	for _, a := range actions {
		if !slices.ContainsFunc(list, func(m kb.MinutesAction) bool { return m.ID == a }) {
			return nil, nil, nil, unknownActionError(a)
		}
	}
	return s.h.ActionTodos(ctx, list, actions, kind, recording, version, note)
}
