package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A run whose conversation its machine does not list while the machine is online ends as gone past
// channel.MissingAfter, without reading the conversation. That ending is logged with the run, the conversation, how
// long it waited and how many messages it answered: before, nothing in the manager's log told a conversation the node
// left out of its list (a session scope it ran outside of) from one that was really closed.
func TestCheckChannelRunLogsAConversationNotListed(t *testing.T) {
	f := newFakeHub().withMachine()
	var sent []map[string]any
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{MaxText: 200}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		sent = append(sent, m)
		return map[string]any{}, nil
	})
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: logf}))
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	s.chans.now = func() time.Time { return now }
	// The machine is online and lists another conversation, not the run's.
	f.setSessions("server", proto.Session{SessionID: "other", MachineID: "server", Status: proto.StatusIdle})
	r := &channel.Run{ID: "examplechat_m3", Channel: "examplechat", MachineID: "server", SessionID: "s1", ReplyTo: "m3",
		MsgIDs: []string{"wechat-1", "wechat-2", "wechat-3"}, Folded: []channel.Folded{{ReplyTo: "m1"}, {ReplyTo: "m2"}},
		Started: now.Add(-channel.MissingAfter - 30*time.Second)}
	prog := &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.live = map[string]*chanLive{r.ID: prog}
	s.chans.mu.Unlock()

	s.checkChannelRun(r.ID)

	if n := len(s.channelRuns("examplechat")); n != 0 {
		t.Fatalf("%d runs left; want the run ended", n)
	}
	if len(sent) != 1 {
		t.Fatalf("got %d messages sent, want the gone note: %v", len(sent), sent)
	}
	if text, _ := sent[0]["text"].(string); !strings.Contains(text, i18n.T("", "channel-turns.gone")) {
		t.Fatalf("the channel got %q, want the gone note", text)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "channels: examplechat_m3: server/s1 is not listed while its machine is online, 3m30s after it was asked: ending the run (3 messages) as gone"
	for _, l := range lines {
		if l == want {
			return
		}
	}
	t.Fatalf("no log line %q in %q", want, lines)
}
