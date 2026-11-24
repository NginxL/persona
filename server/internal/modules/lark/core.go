package lark

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu turns (turn.go, cot.go, cot_worker.go, stream.go): how a run of the Feishu bot shows in the chat, its
// progress card, its agent message and its answer. The channels' pipeline (the runs, their state and their lock) is
// the core's; it hands each run to the layer (Layer.Turn*), and the module keeps only its own state of each live run
// (runLive, through Host.RunState) and of every run (turns).

// turns is the module's state of the Feishu turns, one per layer.
type turns struct {
	h  Host
	mu sync.Mutex
	// kitDown: CardKit would not make a progress card (stream.go); until then they are sent as messages.
	kitDown time.Time
	// workers are the agent messages' workers still running (Layer.Wait).
	workers sync.WaitGroup
}

// wait waits for the workers, or ctx.
func (t *turns) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { t.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newTurns(h Host) *turns { return &turns{h: h} }

// sink is the Feishu bot as the channels' sink.
func (t *turns) sink() feishuSink { return feishuSink{s: core{h: t.h, t: t}} }

// core is the assistant as the turn code calls it: the host's methods under the names the code has always used.
type core struct {
	h Host
	t *turns
}

func (s core) logf(format string, args ...any)       { s.h.Logf(format, args...) }
func (s core) runCtx() context.Context               { return s.h.RunCtx() }
func (s core) botLang(ctx context.Context) i18n.Lang { return s.h.Lang(ctx) }
func (s core) chansNow() time.Time                   { return s.h.Now() }

func (s core) sendAs(ctx context.Context, kind, machine, session string) context.Context {
	return s.h.SendAs(ctx, kind, machine, session)
}

func (s core) appCall(ctx context.Context, method string, params, out any) error {
	return s.h.Call(ctx, method, params, out)
}

func (s core) feishuFeatures(ctx context.Context) (assistantwire.FeishuFeatures, bool) {
	return s.h.Features(ctx)
}

func (s core) botPostTo(ctx context.Context, to feishuTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	return s.h.Post(ctx, to, replyTo, card, fallback, key)
}

func (s core) botPostContent(ctx context.Context, to feishuTarget, replyTo, content, fallback, key string) (string, error) {
	return s.h.PostContent(ctx, to, replyTo, content, fallback, key)
}

func (s core) botNoteTo(ctx context.Context, to feishuTarget, messageID, text string) {
	s.h.Note(ctx, to, messageID, text)
}

func (s core) botUpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return s.h.UpdateCard(ctx, messageID, card)
}

func (s core) botReact(ctx context.Context, messageID, emoji string) (string, error) {
	return s.h.React(ctx, messageID, emoji)
}

func (s core) botUnreact(ctx context.Context, messageID, reactionID string) {
	s.h.Unreact(ctx, messageID, reactionID)
}

func (s core) botMessages(ctx context.Context, messageID string) ([]botcard.Item, error) {
	return s.h.Messages(ctx, messageID)
}

func (s core) workbenchStamp(ctx context.Context, c *botcard.Card) *botcard.Card {
	return s.h.WorkbenchStamp(ctx, c)
}

func (s core) runExt(r *channel.Run, key string) string { return s.h.RunExt(r, key) }

func (s core) runExts(r *channel.Run, keys ...string) map[string]string {
	return s.h.RunExts(r, keys...)
}

func (s core) setRunExt(r *channel.Run, key, value string) { s.h.SetRunExt(r, key, value) }

func (s core) runSteps(id string) []string { return s.h.RunSteps(id) }

func (s core) runTo(ext map[string]string) feishuTarget { return s.h.RunTo(ext) }

func (s core) unreachable(err error) bool { return s.h.Unreachable(err) }

func (s core) botStepParts(l i18n.Lang, it proto.Item) (string, string) { return s.h.StepParts(l, it) }

func (s core) linkNamer(ctx context.Context) func(*url.URL) string { return s.h.LinkNamer(ctx) }

func (s core) openButton(ctx context.Context, machine, session string) botcard.Button {
	return s.h.OpenButton(ctx, machine, session)
}

func (s core) rememberRun(messageID string, r *channel.Run) { s.h.RememberRun(messageID, r) }

func (s core) processPanel(l i18n.Lang, c *botcard.Card, steps []string) {
	s.h.ProcessPanel(l, c, steps)
}

// answerCards are an answer's cards (the core's answerCards): each with the text it goes as when refused.
func (s core) answerCards(l i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) []botAnswerCard {
	cards, fallbacks := s.h.AnswerCards(l, name, answer, quiet, steps, foot)
	out := make([]botAnswerCard, len(cards))
	for i, c := range cards {
		out[i] = botAnswerCard{card: c, fallback: fallbacks[i]}
	}
	return out
}

// botAnswerCard is one card of an answer and the text it goes as when refused.
type botAnswerCard struct {
	card     *botcard.Card
	fallback string
}

// feishuTarget is where a post goes (the outbound port's target).
type feishuTarget = assistantwire.ChannelTarget

// readChatKey is the run's channel value a "/read" in a group is answered by (the core's feishu_read.go).
const readChatKey = "read_chat"

// readRun: the run answers a "/read" in a group.
func readRun(ext map[string]string) bool { return ext[readChatKey] != "" }

// cloneExt is a copy of a run's channel values.
func cloneExt(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// runLive is the module's state of one live run (Host.RunState), beside the core's: the progress card as last sent
// and its entity, the agent message's cursor and its worker. sentCard, backoff, quietUntil, kit and, for a private
// run, cot are used under the run's progress lock (the core holds it around TurnProgress and TurnAnswer); mu guards
// native and cot where a topic run's worker shares them.
type runLive struct {
	mu     sync.Mutex
	cot    cotProgress
	native *cotWorker
	// sentCard is the progress card last sent (its hash), not sent again; backoff and quietUntil hold updates back
	// after Feishu's rate limit.
	sentCard   [32]byte
	backoff    time.Duration
	quietUntil time.Time
	// kit is the progress card as a CardKit entity, streamed into (stream.go); nil before it is sent, or for a card
	// sent as a message.
	kit *cardKit
}

// Abort stops the run's agent-message worker (the core dropped the run). Never called under the channels' lock.
func (p *runLive) Abort() {
	p.mu.Lock()
	w := p.native
	p.mu.Unlock()
	if w != nil {
		w.abort()
	}
}

// NextTurn forgets the agent message of a held run's turn: the next turn gets its own (called under the channels'
// lock).
func (p *runLive) NextTurn() {
	p.mu.Lock()
	p.cot, p.native = cotProgress{}, nil
	p.mu.Unlock()
}

// live is run id's state, nil when the run is not live or no longer among the channels' runs.
func (s core) live(id string) *runLive {
	st, _, ok := s.h.RunState(id, true, func() any { return &runLive{} })
	if !ok {
		return nil
	}
	p, _ := st.(*runLive) // nil for a state the module did not make
	return p
}
