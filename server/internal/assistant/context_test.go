package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The snapshot goes with every message now: it says the time of the message, and a slow source doesn't hold the
// message up once there is an earlier snapshot to go on.
func TestSnapshotIsNowAndDoesNotWait(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.slowSnapshots = 4 * time.Second
	s := newStorelessService(t, f)
	ctx := context.Background()
	loc := s.location(ctx)
	old := time.Now().Add(-10 * time.Minute).In(loc)
	s.live.snap = &prompts.Snapshot{Now: old, Zone: loc.String(), Chats: &prompts.SnapshotChats{AtMe: []prompts.SnapshotChat{{Name: "发布群", Unread: 2}}}}
	s.live.at = time.Now().Add(-10 * time.Minute)

	start := time.Now()
	got := s.contextSnapshot(ctx, "", false)
	if d := time.Since(start); d > snapshotWait+time.Second {
		t.Fatalf("waited %v for a slow source", d)
	}
	now := time.Now().In(loc)
	if !strings.Contains(got, "- 现在："+now.Format("2006-01-02")+" ") || !strings.Contains(got, now.Format("15:04")+"（"+loc.String()+"）") {
		t.Fatalf("not the time now (%s):\n%s", now.Format("15:04"), got)
	}
	if !strings.Contains(got, "发布群（2 条）") {
		t.Fatalf("the earlier snapshot's chats are gone:\n%s", got)
	}
}

// A snapshot asks the connectors for their live state once and takes both the unread chats and the rest of today's
// meetings from that answer: each ask has every connector program build its whole state (#2885).
func TestSnapshotAsksTheConnectorsOnce(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	now := time.Now()
	f.liveStates = []any{connector.SnapshotData{
		Chats:    &connector.SnapshotChats{AtMe: []connector.SnapshotChat{{Name: "发布群", Unread: 2, Preview: "明天上线"}}},
		Meetings: []connector.SnapshotMeeting{{Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: "周会"}, {Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), Title: "早会"}},
	}}
	s := newStorelessService(t, f)

	done := make(chan struct{})
	s.takeSnapshot(done)
	<-done
	f.mu.Lock()
	asks := f.snapshotAsks
	f.mu.Unlock()
	if asks != 1 {
		t.Fatalf("asked the connectors %d times for one snapshot, want 1", asks)
	}
	snap := s.live.snap
	if snap.Chats == nil || len(snap.Chats.AtMe) != 1 || snap.Chats.AtMe[0].Name != "发布群" || snap.Chats.AtMe[0].Preview != "明天上线" {
		t.Fatalf("chats: %+v", snap.Chats)
	}
	if snap.Calendar == nil || len(snap.Calendar.Events) != 1 || snap.Calendar.Events[0].Title != "周会" {
		t.Fatalf("calendar (the meeting that is over left out): %+v", snap.Calendar)
	}
}

// The snapshot links agents to the hub's web app (public_url, its trailing slash dropped), and leaves the links out
// when the hub does not say where that is; the assistant's own conversations are not among the agents (#2194).
func TestSnapshotLinksAgentsToTheWebApp(t *testing.T) {
	agent := proto.Session{MachineID: "mac-a", SessionID: "b00502b8-82a3", State: proto.StateNeedsYou, Title: "修 CI", UpdatedAt: time.Now()}
	mine := proto.Session{MachineID: "mac-a", SessionID: "c0ffee00-0001", State: proto.StateNeedsYou, Assistant: true, UpdatedAt: time.Now()}
	snap := func(f *fakeHub) string {
		f.setSessions("mac-a", agent, mine)
		s := newStorelessService(t, f)
		done := make(chan struct{})
		s.takeSnapshot(done)
		return prompts.RenderSnapshot(*s.live.snap)
	}

	f := newFakeHub()
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{PublicURL: "https://hub.example.com/"}, nil
	})
	got := snap(f)
	if !strings.Contains(got, "链接 https://hub.example.com/s/mac-a/b00502b8-82a3）") {
		t.Fatalf("no link to the agent:\n%s", got)
	}
	if strings.Contains(got, "c0ffee00") {
		t.Fatalf("the assistant's own conversation is listed:\n%s", got)
	}

	f = newFakeHub()
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return nil, errors.New("no app") })
	if got := snap(f); strings.Contains(got, "链接") || !strings.Contains(got, "session_id=b00502b8") {
		t.Fatalf("a link without a web address, or the agent is gone:\n%s", got)
	}
}

// The assistant sees the same three states as clients, including working agents whose workflows need time.
func TestGoldenSnapshotAndAgentsListUltracode(t *testing.T) {
	on, off := true, false
	f := newFakeHub()
	f.setSessions("box",
		proto.Session{SessionID: "needson", Title: "审查方案", State: proto.StateNeedsYou, Ultracode: &on, UpdatedAt: time.Now()},
		proto.Session{SessionID: "doneoff", Title: "修复测试", State: proto.StateUnread, Ultracode: &off, UpdatedAt: time.Now()},
		proto.Session{SessionID: "unknown", Title: "读代码", State: proto.StateWorking},
		proto.Session{SessionID: "workingon", Title: "实现功能", State: proto.StateWorking, Ultracode: &on},
		proto.Session{SessionID: "assistant", Title: "主对话", State: proto.StateWorking, Assistant: true, Ultracode: &on})
	s := newStorelessService(t, f)
	snap := prompts.Snapshot{Now: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), Zone: "UTC"}
	snap.Needs, snap.Unread, snap.Awaiting, snap.Waiting, snap.Done, snap.Muted, snap.Working, snap.Workflows = s.snapshotAgents(context.Background(),
		[]Device{{ID: "box", Name: "电脑", Online: true}}, "")
	if len(snap.Needs) != 1 || snap.Needs[0].Ultracode != "on" || len(snap.Unread) != 1 || snap.Unread[0].Ultracode != "off" ||
		len(snap.Working) != 2 || snap.Working[0].Ultracode != "" || snap.Working[1].Ultracode != "on" {
		t.Fatalf("states changed in the snapshot: %+v", snap)
	}
	golden(t, "assistant_snapshot.ultracode", prompts.RenderSnapshot(snap))
	text := s.describeAgents(context.Background(), "all", "", "")
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.Contains(line, "session_id=needson"), strings.Contains(line, "session_id=workingon"):
			if !strings.Contains(line, "ultracode=on") {
				t.Errorf("on missing: %s", line)
			}
		case strings.Contains(line, "session_id=doneoff"):
			if !strings.Contains(line, "ultracode=off") {
				t.Errorf("off missing: %s", line)
			}
		case strings.Contains(line, "session_id=unknown"):
			if strings.Contains(line, "ultracode=") {
				t.Errorf("unknown claimed a value: %s", line)
			}
		}
	}
	if strings.Contains(text, "session_id=assistant") || strings.Count(text, "ultracode=") != 3 {
		t.Fatalf("agents_list lost state or included the assistant:\n%s", text)
	}
}
