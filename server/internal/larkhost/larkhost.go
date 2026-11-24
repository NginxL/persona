// Package larkhost is the seam between the core and the Feishu / Lark module's assistant layer (internal/modules/lark,
// docs/architecture/core-and-modules.md "Lark", #13404): what the core asks of the module (Layer, whose outbound
// half is the channel port Outbound) and what the module asks of the core (Host), each in terms both sides name (the
// standard library, internal/botcard and the contracts).
//
// The composition root (internal/modules/all) provides the module in the init of its file, under the module's build
// constraint; without it nothing is provided and the core reaches the owner's chat through None, a channel switched
// off: its status says neither on nor ready (no error: nothing to try again), it supports nothing, every post answers
// ErrNoChannel, and notes and thread lines are dropped.
//
// The module is provided while the program initializes, before main runs, and only read afterwards.
package larkhost

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ModuleID is the lark module's id (its module.json).
const ModuleID = "lark"

// Target says where a post or a note goes (assistantwire.ChannelTarget: the zero value is the owner's private chat).
type Target = assistantwire.ChannelTarget

// ThreadEvent is one thing to tell the chat thread of a to-do (assistantwire.ThreadEvent).
type ThreadEvent = assistantwire.ThreadEvent

// What a line in a to-do's chat thread tells (assistantwire.ThreadKind).
const (
	ThreadDecision      = assistantwire.ThreadDecision
	ThreadAgentFinished = assistantwire.ThreadAgentFinished
	ThreadAgentNeeds    = assistantwire.ThreadAgentNeeds
	ThreadTodoAccepted  = assistantwire.ThreadTodoAccepted
	ThreadTodoDone      = assistantwire.ThreadTodoDone
	ThreadTodoFailed    = assistantwire.ThreadTodoFailed
)

// Outbound is the outbound channel port: how core code reaches the owner's chat. Status and Features say what the
// channel supports now; Post, Note, Reply and UpdateCard send; ThreadPost queues a line for a to-do's chat thread (nothing
// for a to-do bound to none).
type Outbound interface {
	Status(ctx context.Context) (assistantwire.FeishuStatus, error)
	Features(ctx context.Context) (f assistantwire.FeishuFeatures, ready bool)
	// Post sends card as a reply to replyTo, else into to's chat; fallback is the rich text it goes as when the card
	// is refused, and key, when not "", makes a send that may be made again land once.
	Post(ctx context.Context, to Target, replyTo string, card *botcard.Card, fallback, key string) (messageID string, err error)
	// Note writes a line of text under replyTo (a failure is only logged); Reply does and says whether it went, with
	// key as Post's and full when the text is a task's whole answer.
	Note(ctx context.Context, to Target, replyTo, text string)
	Reply(ctx context.Context, to Target, replyTo, text, key string, full bool) error
	UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error
	ThreadPost(todoID string, ev ThreadEvent)
}

// ErrNoChannel: this build has no chat channel (the lark module is left out).
var ErrNoChannel = errors.New("no chat channel in this build")

// None is Outbound without a channel: as a bot that is switched off.
type None struct{}

func (None) Status(context.Context) (assistantwire.FeishuStatus, error) {
	return assistantwire.FeishuStatus{}, nil
}
func (None) Features(context.Context) (assistantwire.FeishuFeatures, bool) {
	return assistantwire.FeishuFeatures{}, false
}
func (None) Post(context.Context, Target, string, *botcard.Card, string, string) (string, error) {
	return "", ErrNoChannel
}
func (None) Note(context.Context, Target, string, string)                      {}
func (None) Reply(context.Context, Target, string, string, string, bool) error { return ErrNoChannel }
func (None) UpdateCard(context.Context, string, *botcard.Card) error           { return ErrNoChannel }
func (None) ThreadPost(string, ThreadEvent)                                    {}

// Turns is how the Feishu bot's runs show in the chat (the channels' sink of channel.Feishu): the progress card and
// agent message, a folded or recalled message's marks, and the answer.
type Turns interface {
	TurnProgress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string)
	// TurnFolded: o went into a later run; owner is that run's state (Host.RunState), nil when it has none.
	TurnFolded(ctx context.Context, o *channel.Run, owner any)
	TurnRecalled(ctx context.Context, m channel.Folded)
	TurnAnswer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error
	TurnProgressAfter(r *channel.Run) time.Duration
	TurnProgressIdle() time.Duration
	// Wait waits until the turns' background work has ended, or ctx ends (the service stops; tests drain).
	Wait(ctx context.Context) error
}

// Layer is what the core asks of the lark module: the outbound port and the Feishu turns. It grows as the
// assistant's Feishu files move into the module (#13404).
type Layer interface {
	Outbound
	Turns
}

// Host is what the lark module asks of the core. While the outbound port's implementation still lives in the core
// (internal/assistant's feishu*.go), the host serves it under the same names, and the module's layer forwards to it;
// each later move takes code into the module and drops the host methods it no longer needs.
type Host interface {
	Outbound

	// What the turn rendering needs of the core.
	Logf(format string, args ...any)
	RunCtx() context.Context
	// Timings are the module's times over its defaults: a test and tuning knob of the core's
	// (assistant.Options.LarkTimings), never a user setting.
	Timings() (one map[string]time.Duration, lists map[string][]time.Duration)
	Lang(ctx context.Context) i18n.Lang
	SendAs(ctx context.Context, kind, machine, session string) context.Context
	Call(ctx context.Context, method string, params, out any) error
	PostContent(ctx context.Context, to Target, replyTo, content, fallback, key string) (string, error)
	React(ctx context.Context, messageID, emoji string) (string, error)
	Unreact(ctx context.Context, messageID, reactionID string)
	Messages(ctx context.Context, messageID string) ([]botcard.Item, error)
	WorkbenchStamp(ctx context.Context, c *botcard.Card) *botcard.Card
	LinkNamer(ctx context.Context) func(*url.URL) string
	OpenButton(ctx context.Context, machine, session string) botcard.Button
	RememberRun(messageID string, r *channel.Run)
	Unreachable(err error) bool
	Offline(err error) bool
	StepParts(l i18n.Lang, it proto.Item) (verb, detail string)
	ProgressCard(l i18n.Lang, r *channel.Run, took time.Duration, steps []string, live string) *botcard.Card
	ProcessPanel(l i18n.Lang, c *botcard.Card, steps []string)
	MissingPiecesNote(l i18n.Lang, missing []int) string
	AnswerCards(l i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) (cards []*botcard.Card, fallbacks []string)

	// The channels' runs, under their lock.
	Now() time.Time
	RunExt(r *channel.Run, key string) string
	RunExts(r *channel.Run, keys ...string) map[string]string
	SetRunExt(r *channel.Run, key, value string)
	SetLiveRunExt(r *channel.Run, key, value string)
	RunTo(ext map[string]string) Target
	RunState(id string, kept bool, make func() any) (state any, done <-chan struct{}, ok bool)
	RunItems(id string) ([]proto.Item, bool)
	RunSteps(id string) []string
	PersistRunExt(id string, state any, still func() bool, ext map[string]string) (live, kept bool)
	SaveRuns()
}

// The Feishu turns without a channel: nothing is shown and an answer has nowhere to go (a run of the bot kept from a
// build that had it ends).
func (None) TurnProgress(context.Context, *channel.Run, time.Duration, []string, string) {}
func (None) TurnFolded(context.Context, *channel.Run, any)                               {}
func (None) TurnRecalled(context.Context, channel.Folded)                                {}
func (None) TurnAnswer(context.Context, *channel.Run, string, string, time.Duration, bool, bool, channel.End) error {
	return nil
}
func (None) TurnProgressAfter(*channel.Run) time.Duration { return channel.ProgressAfter }
func (None) TurnProgressIdle() time.Duration              { return channel.ProgressIdle }
func (None) Wait(context.Context) error                   { return nil }

var provided func(Host) Layer

// Provide provides the module: new makes its layer for one manager's host (init only).
func Provide(new func(Host) Layer) {
	if provided != nil {
		panic("larkhost: the lark module provided twice")
	}
	provided = new
}

// InBuild says whether the lark module is in this build.
func InBuild() bool { return provided != nil }

// New is the module's layer for h; false when the module is not in this build.
func New(h Host) (Layer, bool) {
	if provided == nil {
		return nil, false
	}
	return provided(h), true
}
