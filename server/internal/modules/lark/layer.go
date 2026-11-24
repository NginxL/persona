package lark

import (
	"context"
	"net/url"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Host is what the module's assistant layer needs of the core: the assistant's Service, through
// internal/larkhost.Host, which has the same methods (internal/modules/all checks it). Each takes and gives only what
// both sides can name: the standard library's, internal/botcard's, internal/channel's and the contract packages'.
type Host interface {
	// The outbound channel port, which the core still serves from its feishu*.go (Layer forwards to it until that
	// code moves here).
	Status(ctx context.Context) (assistantwire.FeishuStatus, error)
	Features(ctx context.Context) (assistantwire.FeishuFeatures, bool)
	Post(ctx context.Context, to assistantwire.ChannelTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error)
	Note(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text string)
	Reply(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text, key string, full bool) error
	UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error
	ThreadPost(todoID string, ev assistantwire.ThreadEvent)

	// What the turn rendering (turn.go, cot.go, cot_worker.go, stream.go) needs of the core.
	Logf(format string, args ...any)
	RunCtx() context.Context
	// Timings are the module's times over its defaults (timings.go): a test and tuning knob of the core's
	// (assistant.Options.LarkTimings), never a user setting.
	Timings() (one map[string]time.Duration, lists map[string][]time.Duration)
	Lang(ctx context.Context) i18n.Lang
	// SendAs labels the cards posted within ctx for the log line of each card that lands.
	SendAs(ctx context.Context, kind, machine, session string) context.Context
	// Call is a call of the hub's feishu.* transport (assistantwire.Feishu*).
	Call(ctx context.Context, method string, params, out any) error
	PostContent(ctx context.Context, to assistantwire.ChannelTarget, replyTo, content, fallback, key string) (string, error)
	React(ctx context.Context, messageID, emoji string) (string, error)
	Unreact(ctx context.Context, messageID, reactionID string)
	Messages(ctx context.Context, messageID string) ([]botcard.Item, error)
	WorkbenchStamp(ctx context.Context, c *botcard.Card) *botcard.Card
	LinkNamer(ctx context.Context) func(*url.URL) string
	OpenButton(ctx context.Context, machine, session string) botcard.Button
	// RememberRun binds a message the bot posted in the private chat to run r's conversation: replying to it talks there.
	RememberRun(messageID string, r *channel.Run)
	// Unreachable: the call did not reach the hub or did not come back; Offline: the hub is not connected.
	Unreachable(err error) bool
	Offline(err error) bool
	StepParts(l i18n.Lang, it proto.Item) (verb, detail string)
	ProgressCard(l i18n.Lang, r *channel.Run, took time.Duration, steps []string, live string) *botcard.Card
	ProcessPanel(l i18n.Lang, c *botcard.Card, steps []string)
	// MissingPiecesNote says which pieces of an answer did not get to Feishu (1-based) and where the whole is.
	MissingPiecesNote(l i18n.Lang, missing []int) string
	AnswerCards(l i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) (cards []*botcard.Card, fallbacks []string)

	// The channels' runs, under their lock.
	Now() time.Time
	RunExt(r *channel.Run, key string) string
	RunExts(r *channel.Run, keys ...string) map[string]string
	SetRunExt(r *channel.Run, key, value string)
	// SetLiveRunExt sets a value on the live run as well when r is a copy of it (an answer works on a copy).
	SetLiveRunExt(r *channel.Run, key, value string)
	RunTo(ext map[string]string) assistantwire.ChannelTarget
	// RunState is the module's own state of live run id (made with make the first time); done closes when the run
	// ends. False when the run is not live or, with kept, no longer among the channels' runs.
	RunState(id string, kept bool, make func() any) (state any, done <-chan struct{}, ok bool)
	RunItems(id string) ([]proto.Item, bool)
	RunSteps(id string) []string
	// PersistRunExt sets ext on run id when it is still live with state and still() holds (asked under the lock):
	// live says it was, kept that the run is still among the channels' runs (and has ext now). SaveRuns saves them.
	PersistRunExt(id string, state any, still func() bool, ext map[string]string) (live, kept bool)
	SaveRuns()
}

// Layer is the module's assistant layer, what the core asks of it (internal/larkhost.Layer): the outbound channel port
// through which core code reaches the owner's chat, and the Feishu turns (turn.go).
type Layer struct {
	h     Host
	turns *turns
}

// New is the layer of one manager's host.
func New(h Host) *Layer { return &Layer{h: h, turns: newTurns(h)} }

// Status is the bot as the hub has it now: on and paired, its switches.
func (l *Layer) Status(ctx context.Context) (assistantwire.FeishuStatus, error) {
	return l.h.Status(ctx)
}

// Features are the bot's switches and whether it can post now.
func (l *Layer) Features(ctx context.Context) (assistantwire.FeishuFeatures, bool) {
	return l.h.Features(ctx)
}

// Post sends a card as a reply to replyTo, else into to's chat.
func (l *Layer) Post(ctx context.Context, to assistantwire.ChannelTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	return l.h.Post(ctx, to, replyTo, card, fallback, key)
}

// Note writes a line of text under replyTo; a failure is only logged.
func (l *Layer) Note(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text string) {
	l.h.Note(ctx, to, replyTo, text)
}

// Reply writes a line of text under replyTo and says whether it went.
func (l *Layer) Reply(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text, key string, full bool) error {
	return l.h.Reply(ctx, to, replyTo, text, key, full)
}

// UpdateCard edits a card the bot sent.
func (l *Layer) UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return l.h.UpdateCard(ctx, messageID, card)
}

// ThreadPost queues a line for a to-do's chat thread.
func (l *Layer) ThreadPost(todoID string, ev assistantwire.ThreadEvent) { l.h.ThreadPost(todoID, ev) }

// TurnProgress shows a run's progress: its progress card, and its agent message in a topic.
func (l *Layer) TurnProgress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string) {
	l.turns.sink().progress(ctx, r, took, steps, live)
}

// TurnFolded closes the progress of run o, folded into a later message; owner is the later run's state (RunState),
// nil when it has none.
func (l *Layer) TurnFolded(ctx context.Context, o *channel.Run, owner any) {
	p, _ := owner.(*runLive)
	l.turns.sink().folded(ctx, o, p)
}

// TurnRecalled takes the marks off a recalled message that was folded into a run.
func (l *Layer) TurnRecalled(ctx context.Context, m channel.Folded) { l.turns.sink().recalled(ctx, m) }

// TurnAnswer sends a run's answer (channels_runs.go's channelSink.answer).
func (l *Layer) TurnAnswer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error {
	return l.turns.sink().answer(ctx, r, answer, note, took, final, quiet, end)
}

// Wait waits until the turns' background work (the agent messages' workers) has ended, or ctx ends.
func (l *Layer) Wait(ctx context.Context) error { return l.turns.wait(ctx) }

// TurnProgressAfter and TurnProgressIdle are when the first progress card goes out and how long it may stay unchanged.
func (l *Layer) TurnProgressAfter(r *channel.Run) time.Duration {
	return l.turns.sink().progressAfter(r)
}
func (l *Layer) TurnProgressIdle() time.Duration { return l.turns.sink().progressIdle() }
