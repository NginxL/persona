package assistant

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu / Lark module's assistant layer (internal/modules/lark, docs/architecture/core-and-modules.md "Lark",
// #13404). Core code reaches the owner's chat only through the outbound channel port (s.chat(), larkhost.Outbound):
// what the channel supports (Status, Features), a card (Post), a line under a message (Note, Reply), a card edited in
// place (UpdateCard) and a line for a to-do's chat thread (ThreadPost). With the module in the build the port is the
// module's layer; without it (persona_without_lark, persona_core_only) it is larkhost.None, which posts nothing and
// says so. The port's implementation still lives in this package's feishu*.go and is served to the module through
// larkHost until it moves there.

// larkLayer is the module's layer, made once per service on first use (a service a test makes without New has one
// too), and the module's times over its defaults (Options.LarkTimings; tests change them while it runs).
type larkLayer struct {
	once    sync.Once
	layer   larkhost.Layer
	mu      sync.Mutex
	timings map[string]time.Duration
	lists   map[string][]time.Duration
}

// larkModule is the lark module's layer, or larkhost.None without the module.
func (s *Service) larkModule() larkhost.Layer {
	s.lark.once.Do(func() {
		s.lark.layer = larkhost.None{}
		if l, ok := larkhost.New(larkHost{s}); ok {
			s.lark.layer = l
		}
	})
	return s.lark.layer
}

// chat is the outbound channel port: the lark module's layer, or larkhost.None without the module.
func (s *Service) chat() larkhost.Outbound { return s.larkModule() }

// setLarkTiming sets one of the lark module's times (tests).
func (s *Service) setLarkTiming(name string, d time.Duration) {
	s.lark.mu.Lock()
	defer s.lark.mu.Unlock()
	if s.lark.timings == nil {
		s.lark.timings = map[string]time.Duration{}
	}
	s.lark.timings[name] = d
}

// setLarkTimingList sets one of the lark module's lists of times (tests).
func (s *Service) setLarkTimingList(name string, l []time.Duration) {
	s.lark.mu.Lock()
	defer s.lark.mu.Unlock()
	if s.lark.lists == nil {
		s.lark.lists = map[string][]time.Duration{}
	}
	s.lark.lists[name] = l
}

// feishuSink is the Feishu bot as the channels' sink (channels_runs.go): the lark module's turns (larkhost.Turns).
type feishuSink struct{ s *Service }

func (f feishuSink) progress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string) {
	f.s.larkModule().TurnProgress(ctx, r, took, steps, live)
}

func (f feishuSink) folded(ctx context.Context, o *channel.Run, owner *chanLive) {
	var st any
	if owner != nil {
		f.s.chans.mu.Lock()
		st = owner.sink
		f.s.chans.mu.Unlock()
	}
	f.s.larkModule().TurnFolded(ctx, o, st)
}

func (f feishuSink) recalled(ctx context.Context, m channel.Folded) {
	f.s.larkModule().TurnRecalled(ctx, m)
}

func (f feishuSink) answer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error {
	return f.s.larkModule().TurnAnswer(ctx, r, answer, note, took, final, quiet, end)
}

func (f feishuSink) progressAfter(r *channel.Run) time.Duration {
	return f.s.larkModule().TurnProgressAfter(r)
}
func (f feishuSink) progressIdle() time.Duration { return f.s.larkModule().TurnProgressIdle() }

// larkHost is the core as the lark module reaches it (larkhost.Host).
type larkHost struct{ s *Service }

func (h larkHost) Status(ctx context.Context) (FeishuStatus, error) { return h.s.feishuStatus(ctx) }

func (h larkHost) Features(ctx context.Context) (FeishuFeatures, bool) {
	return h.s.feishuFeatures(ctx)
}

func (h larkHost) Post(ctx context.Context, to larkhost.Target, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	return h.s.botPostTo(ctx, to, replyTo, card, fallback, key)
}

func (h larkHost) Note(ctx context.Context, to larkhost.Target, replyTo, text string) {
	h.s.botNoteTo(ctx, to, replyTo, text)
}

func (h larkHost) Reply(ctx context.Context, to larkhost.Target, replyTo, text, key string, full bool) error {
	return h.s.botReplyTo(ctx, to, replyTo, text, key, full)
}

func (h larkHost) UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return h.s.botUpdateCard(ctx, messageID, card)
}

func (h larkHost) ThreadPost(todoID string, ev larkhost.ThreadEvent) {
	h.s.threadPost(todoID, threadEvent{kind: threadEventKind(ev.Kind), agent: ev.Agent, text: ev.Text, dm: ev.DM})
}

func (h larkHost) Logf(format string, args ...any) { h.s.logf(format, args...) }
func (h larkHost) RunCtx() context.Context         { return h.s.runCtx() }

func (h larkHost) Timings() (map[string]time.Duration, map[string][]time.Duration) {
	h.s.lark.mu.Lock()
	defer h.s.lark.mu.Unlock()
	return maps.Clone(h.s.lark.timings), maps.Clone(h.s.lark.lists)
}

func (h larkHost) Lang(ctx context.Context) i18n.Lang { return h.s.botLang(ctx) }

func (h larkHost) SendAs(ctx context.Context, kind, machine, session string) context.Context {
	return feishuSendAs(ctx, kind, machine, session)
}

func (h larkHost) Call(ctx context.Context, method string, params, out any) error {
	return h.s.appCall(ctx, AppFeishu, method, params, out)
}

func (h larkHost) PostContent(ctx context.Context, to larkhost.Target, replyTo, content, fallback, key string) (string, error) {
	return h.s.botPostContent(ctx, to, replyTo, content, fallback, key)
}

func (h larkHost) React(ctx context.Context, messageID, emoji string) (string, error) {
	return h.s.botReact(ctx, messageID, emoji)
}

func (h larkHost) Unreact(ctx context.Context, messageID, reactionID string) {
	h.s.botUnreact(ctx, messageID, reactionID)
}

func (h larkHost) Messages(ctx context.Context, messageID string) ([]botcard.Item, error) {
	return h.s.botMessages(ctx, messageID)
}

func (h larkHost) WorkbenchStamp(ctx context.Context, c *botcard.Card) *botcard.Card {
	return h.s.workbenchStamp(ctx, c)
}

func (h larkHost) LinkNamer(ctx context.Context) func(*url.URL) string { return h.s.linkNamer(ctx) }

func (h larkHost) OpenButton(ctx context.Context, machine, session string) botcard.Button {
	return h.s.openButton(ctx, machine, session)
}

func (h larkHost) RememberRun(messageID string, r *channel.Run) {
	h.s.rememberBotThread(messageID, botTargetOf(r), r.Label)
}

func (h larkHost) Unreachable(err error) bool { return unreachable(err) }
func (h larkHost) Offline(err error) bool     { return errors.Is(err, bus.ErrOffline) }

func (h larkHost) StepParts(l i18n.Lang, it proto.Item) (string, string) { return botStepParts(l, it) }

func (h larkHost) ProgressCard(l i18n.Lang, r *channel.Run, took time.Duration, steps []string, live string) *botcard.Card {
	return progressCard(l, r.Label, took, steps, live, h.s.runWaiting(r), h.s.runLiveView(r.ID), botTargetOf(r))
}

func (h larkHost) ProcessPanel(l i18n.Lang, c *botcard.Card, steps []string) {
	processPanel(l, c, steps)
}

func (h larkHost) MissingPiecesNote(l i18n.Lang, missing []int) string {
	return missingPiecesNote(l, missing)
}

func (h larkHost) AnswerCards(l i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) ([]*botcard.Card, []string) {
	acs := answerCards(l, name, answer, quiet, steps, foot)
	cards, fallbacks := make([]*botcard.Card, len(acs)), make([]string, len(acs))
	for i, ac := range acs {
		cards[i], fallbacks[i] = ac.card, ac.fallback
	}
	return cards, fallbacks
}

func (h larkHost) Now() time.Time                           { return h.s.chansNow() }
func (h larkHost) RunExt(r *channel.Run, key string) string { return h.s.runExt(r, key) }
func (h larkHost) RunExts(r *channel.Run, keys ...string) map[string]string {
	return h.s.runExts(r, keys...)
}
func (h larkHost) SetRunExt(r *channel.Run, key, value string) { h.s.setRunExt(r, key, value) }

func (h larkHost) SetLiveRunExt(r *channel.Run, key, value string) {
	if real, _ := h.s.channelRunByID(r.ID); real != nil && real != r {
		h.s.setRunExt(real, key, value)
	}
	h.s.setRunExt(r, key, value)
}

func (h larkHost) RunTo(ext map[string]string) larkhost.Target { return runTo(ext) }

func (h larkHost) RunState(id string, kept bool, make func() any) (any, <-chan struct{}, bool) {
	c := &h.s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.live[id]
	if p == nil || kept && !slices.ContainsFunc(c.st.Runs, func(r *channel.Run) bool { return r.ID == id }) {
		return nil, nil, false
	}
	if p.sink == nil {
		p.sink = make()
	}
	return p.sink, p.done, true
}

func (h larkHost) RunItems(id string) ([]proto.Item, bool) {
	p, items := h.s.runItems(id)
	return items, p != nil
}

func (h larkHost) RunSteps(id string) []string { return h.s.runSteps(id) }

func (h larkHost) PersistRunExt(id string, state any, still func() bool, ext map[string]string) (live, kept bool) {
	c := &h.s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.live[id]
	if p == nil || p.sink != state || !still() {
		return false, false
	}
	for _, r := range c.st.Runs {
		if r.ID != id {
			continue
		}
		if r.Ext == nil {
			r.Ext = map[string]string{}
		}
		for k, v := range ext {
			r.Ext[k] = v
		}
		return true, true
	}
	return true, false
}

func (h larkHost) SaveRuns() {
	c := &h.s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	h.s.saveChannelStateLocked()
}

// larkWait waits, at most d, for the lark module's background work (the Feishu agent messages' workers) to end.
func (s *Service) larkWait(d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := s.larkModule().Wait(ctx); err != nil {
		s.logf("lark: background work still running at the end: %v", err)
	}
}
