package assistant

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
)

// The channels (docs/extensions/channels.md): the ways of talking to Persona from a chat app — the Feishu bot (feishu*.go) and
// the bots plugins contribute (the channel extension point, channels_plugin.go). A message from a channel goes into the
// conversation the channel talks to, the turn is followed and its answer sent back the same way (channels_runs.go);
// what is asked in a conversation a channel drives goes there too. All of that is the assistant's. The hub keeps the
// transport — the open platform's long connection, pairing, the plugins' programs — hands the assistant what comes in
// (channels.receive, feishu.message, feishu.card_action) and posts what it is given (app.call feishu.*, channels.*).
// While the assistant is not connected the hub answers the owner's messages "the assistant can't be reached right now" itself.

// The channels' state: each plugin channel's settings and the conversations its messages went to lately (channels),
// the runs, the asks waiting in text and the messages taken (channels.state). The same keys the hub kept before; only
// the assistant reads and writes them now.
const (
	channelsKey      = "channels"
	channelsStateKey = "channels.state"
)

type channelPrefs struct {
	Target   *channel.Target `json:"target,omitempty"`
	Features map[string]bool `json:"features,omitempty"`
	// Recent are the conversations its messages went to lately (newest first, a few).
	Recent []channel.Conversation `json:"recent,omitempty"`
	// Threads are the conversations of its threads (capabilities.threads), by the thread key its messages carry.
	Threads map[string]*channelThread `json:"threads,omitempty"`
}

const channelRecentKeep = 5

// channelThread is the conversation one of a channel's threads talks to (the thread's first message started it), and
// when a message of the thread last went there.
type channelThread struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
}

// A thread nobody wrote in for channelThreadsKeepFor starts a new conversation; past channelThreadsKeep the oldest go.
const (
	channelThreadsKeep    = 500
	channelThreadsKeepFor = 30 * 24 * time.Hour
)

type channelsState struct {
	Runs []*channel.Run `json:"runs,omitempty"`
	Asks []*channel.Ask `json:"asks,omitempty"`
	// Seen are, by channel, the ids of the latest messages taken (the chat apps deliver some again).
	Seen map[string][]string `json:"seen,omitempty"`
}

const channelSeenKeep = 300

// channelsBook is the channels' state in memory.
type channelsBook struct {
	mu sync.Mutex
	// receiving is the last accepted message for each channel/thread, until its route and binding are settled.
	receiving map[channelAdmissionKey]chan struct{}
	loaded    bool
	prefs     map[string]*channelPrefs
	// st is what the runs wait on (channels_runs.go), loaded apart from prefs.
	st       channelsState
	stLoaded bool
	live     map[string]*chanLive // run id -> its live progress (not persisted)
	// answered: conversation -> when its last run was answered (no "finished" push, no mirror for it)
	answered map[[2]string]time.Time
	// notified: channel + conversation (machine, session) -> when its notify_user copy arrived
	notified map[[3]string]time.Time
	// answering: the runs whose answer is going out now (finishChannelRun): a later message is not folded into them
	answering map[string]bool
	// follows: how many runs follow each conversation (session.follow)
	follows map[[2]string]int
	// followRetry: the followed conversations whose session.follow failed -> when a run's watcher asks again
	followRetry map[[2]string]time.Time
	// announced: the cards this process heard of (interaction.announced, or pending when it first connected); one
	// that came while it was not connected is taken when it is back (channelsCatchUp).
	announced map[string]bool
	connected bool
	// ended: the cards that ended lately (interaction.ended), by when: one still being sent to a channel then is closed
	// (or not asked) as soon as it is out.
	ended map[string]time.Time
	// lastEnded: channel -> the latest text ask there that ended without being answered there (answered in the app,
	// in a terminal, withdrawn), by when: a late reply to it is told so instead of going into the conversation.
	lastEnded map[string]endedAsk
	// generation changes on unbind so an in-flight message or question cannot restore the old binding's state.
	generation map[string]uint64
	// list is the hub's list of the channels, kept a moment (the runs look at it every second).
	list   []channel.View
	listAt time.Time
	now    func() time.Time // tests
}

func (c *channelsBook) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (s *Service) loadChannelsLocked(ctx context.Context) {
	c := &s.chans
	if c.loaded {
		return
	}
	c.prefs = map[string]*channelPrefs{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, channelsKey, &c.prefs); err != nil {
			s.logf("channels: load: %v", err)
			return // not marked loaded: the next look tries again
		}
	}
	if c.prefs == nil {
		c.prefs = map[string]*channelPrefs{}
	}
	c.loaded = true
}

// saveChannelsLocked writes the settings, once they were read: saving what is in memory after a failed read would
// write one channel's settings over every other's.
func (s *Service) saveChannelsLocked() {
	st := s.store()
	if st == nil || !s.chans.loaded {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, channelsKey, s.chans.prefs); err != nil {
		s.logf("channels: save: %v", err)
	}
}

// channelPrefLocked is channel id's settings (created on first use; the caller holds s.chans.mu).
func (s *Service) channelPrefLocked(id string) *channelPrefs {
	p := s.chans.prefs[id]
	if p == nil {
		p = &channelPrefs{}
		s.chans.prefs[id] = p
	}
	if p.Features == nil {
		p.Features = map[string]bool{}
		for k, v := range channel.DefaultFeatures {
			p.Features[k] = v
		}
	}
	return p
}

// loadChannelStateLocked reads the runs once (the caller holds s.chans.mu); the runs an older hub kept in the Feishu
// bot's state come over the first time.
//
// The state is decoded into a fresh value, never into c.st: runs and asks started while it could not be read are
// already in c.st, and their watchers read them without the lock. Decoding in place would reuse those pointers and
// write the stored runs' fields over them. They are kept next to what was stored instead.
func (s *Service) loadChannelStateLocked(ctx context.Context) {
	c := &s.chans
	if c.stLoaded {
		return
	}
	var stored channelsState
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, channelsStateKey, &stored); err != nil {
			s.logf("channels: load state: %v", err)
			return
		}
	}
	if stored.Seen == nil {
		stored.Seen = map[string][]string{}
	}
	var kept int
	stored.Runs, kept = keepLive(stored.Runs, c.st.Runs, func(r *channel.Run) string { return r.ID })
	var asks int
	stored.Asks, asks = keepLive(stored.Asks, c.st.Asks, func(a *channel.Ask) string { return a.ID })
	c.st = stored
	c.stLoaded = true
	if kept+asks > 0 {
		s.saveChannelStateLocked()
		s.logf("channels: %d runs and %d asks started while the state was unreadable kept", kept, asks)
	}
	if old := s.takeLegacyBotRuns(ctx); len(old) > 0 {
		c.st.Runs = append(c.st.Runs, old...)
		s.saveChannelStateLocked()
		s.logf("channels: %d waiting turns of the Feishu bot moved to the channels' runs", len(old))
	}
}

// keepLive is stored with the items held in memory added: one with the id of a stored item takes its place (it is
// the one being watched), the others are appended; n is how many came from memory.
func keepLive[T any](stored, live []*T, id func(*T) string) (out []*T, n int) {
	if len(live) == 0 {
		return stored, 0
	}
	at := make(map[string]int, len(stored))
	for i, x := range stored {
		at[id(x)] = i
	}
	for _, x := range live {
		if i, ok := at[id(x)]; ok {
			stored[i] = x
		} else {
			at[id(x)] = len(stored)
			stored = append(stored, x)
		}
		n++
	}
	return stored, n
}

// saveChannelStateLocked writes the runs, asks and seen messages, once they were read (the same as the settings).
func (s *Service) saveChannelStateLocked() error {
	st := s.store()
	if st == nil || !s.chans.stLoaded {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, channelsStateKey, s.chans.st); err != nil {
		s.logf("channels: save state: %v", err)
		return err
	}
	return nil
}

// bindChannelThread notes the conversation only while its accepted binding generation is current.
// Omitting generation is for seeding an existing binding; receive paths always supply their accepted generation.
func (s *Service) bindChannelThread(id, thread, machine, session string, generation ...uint64) bool {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(generation) > 0 && c.generation[id] != generation[0] {
		return false
	}
	s.loadChannelsLocked(context.Background())
	p := s.channelPrefLocked(id)
	if p.Threads == nil {
		p.Threads = map[string]*channelThread{}
	}
	now := c.clock().UTC()
	p.Threads[thread] = &channelThread{MachineID: machine, SessionID: session, At: now}
	pruneChannelThreads(p.Threads, now)
	s.saveChannelsLocked()
	return true
}

// unbindChannelThread forgets thread's conversation: its next message starts a new one (/new in the thread).
func (s *Service) unbindChannelThread(id, thread string, generation ...uint64) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(generation) > 0 && c.generation[id] != generation[0] {
		return
	}
	s.loadChannelsLocked(context.Background())
	p := s.channelPrefLocked(id)
	if _, ok := p.Threads[thread]; !ok {
		return
	}
	delete(p.Threads, thread)
	s.saveChannelsLocked()
}

// channelThreadOf is thread's conversation, nil when it has none (or it went quiet too long ago).
func (s *Service) channelThreadOf(ctx context.Context, id, thread string) *channelThread {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelsLocked(ctx)
	t := s.channelPrefLocked(id).Threads[thread]
	if t == nil || c.clock().Sub(t.At) > channelThreadsKeepFor {
		return nil
	}
	cp := *t
	return &cp
}

// pruneChannelThreads drops the threads quiet for longer than channelThreadsKeepFor, then the oldest beyond
// channelThreadsKeep.
func pruneChannelThreads(threads map[string]*channelThread, now time.Time) {
	for key, t := range threads {
		if t == nil || now.Sub(t.At) > channelThreadsKeepFor {
			delete(threads, key)
		}
	}
	if len(threads) <= channelThreadsKeep {
		return
	}
	keys := slices.Collect(maps.Keys(threads))
	slices.SortFunc(keys, func(a, b string) int { return threads[a].At.Compare(threads[b].At) })
	for _, key := range keys[:len(keys)-channelThreadsKeep] {
		delete(threads, key)
	}
}

// noteChannelConversation remembers that channel id's message went to a conversation (the detail's recent list, the
// conversation's source marks).
func (s *Service) noteChannelConversation(id, machine, session string) {
	c := &s.chans
	c.mu.Lock()
	s.loadChannelsLocked(context.Background())
	p := s.channelPrefLocked(id)
	p.Recent = slices.DeleteFunc(p.Recent, func(x channel.Conversation) bool { return x.MachineID == machine && x.SessionID == session })
	p.Recent = append([]channel.Conversation{{MachineID: machine, SessionID: session, At: time.Now().UTC()}}, p.Recent...)
	if len(p.Recent) > channelRecentKeep {
		p.Recent = p.Recent[:channelRecentKeep]
	}
	s.saveChannelsLocked()
	c.mu.Unlock()
}

// channelsReport is what the hub shows of the channels (Snapshot.Channels): the channels each conversation's messages
// came from, the conversations a channel's turn is followed in or was answered in within the hour; nil while the
// state can't be read.
func (s *Service) channelsReport() *ChannelsReport {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx := context.Background()
	s.loadChannelsLocked(ctx)
	s.loadChannelStateLocked(ctx)
	if !c.loaded || !c.stLoaded {
		return nil
	}
	out := &ChannelsReport{Marks: []ChannelMark{}, Turns: []ChannelTurn{}}
	type mark struct {
		id string
		at time.Time
	}
	marks := map[[2]string][]mark{}
	for id, p := range c.prefs {
		for _, r := range p.Recent {
			k := [2]string{r.MachineID, r.SessionID}
			marks[k] = append(marks[k], mark{id, r.At})
		}
	}
	for k, ms := range marks {
		slices.SortFunc(ms, func(a, b mark) int {
			if n := b.at.Compare(a.at); n != 0 {
				return n
			}
			return compareStrings(a.id, b.id)
		})
		m := ChannelMark{MachineID: k[0], SessionID: k[1]}
		for _, x := range ms {
			m.Channels = append(m.Channels, x.id)
		}
		out.Marks = append(out.Marks, m)
	}
	busy := map[[2]string]bool{}
	for _, r := range c.st.Runs {
		k := [2]string{r.MachineID, r.SessionID}
		if !busy[k] {
			busy[k] = true
			out.Turns = append(out.Turns, ChannelTurn{MachineID: k[0], SessionID: k[1]})
		}
	}
	now := c.clock()
	for k, at := range c.answered {
		if now.Sub(at) > time.Hour {
			delete(c.answered, k)
			continue
		}
		if !busy[k] {
			out.Turns = append(out.Turns, ChannelTurn{MachineID: k[0], SessionID: k[1], Answered: at.UTC()})
		}
	}
	slices.SortFunc(out.Marks, func(a, b ChannelMark) int {
		return compareStrings(a.MachineID+"/"+a.SessionID, b.MachineID+"/"+b.SessionID)
	})
	slices.SortFunc(out.Turns, func(a, b ChannelTurn) int {
		return compareStrings(a.MachineID+"/"+a.SessionID, b.MachineID+"/"+b.SessionID)
	})
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// channelsChanged reports the snapshot again: the hub shows the channels' marks and turns from it.
func (s *Service) channelsChanged() { go s.publish(context.Background()) }

// channelLoop sweeps every few seconds: turns whose watcher was lost (a restart), every channel's.
func (s *Service) channelLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.sweepChannelRuns()
	}
}
