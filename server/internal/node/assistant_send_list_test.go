package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A message's send returns once its line is written, without building the session list: with the list unable to be
// built at all (its offline scan held), the send still returns at once, and the list goes out once it can be built.
// Building it twice inside every send held a send up past the manager's 90 s on a computer with a few hundred
// sessions: the message was in, the manager heard a failure and sent it again.
func TestAssistantSendDoesNotWaitForTheList(t *testing.T) {
	old := listPushWait
	listPushWait = 100 * time.Millisecond
	t.Cleanup(func() { listPushWait = old })
	interjectClaude(t, "2.1.200")
	n := codexTestNode(t)
	withFakeHub(t, n)
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "first", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.stopAssistant(id) })
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	sdkWait(t, "its init", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.claudeVersion != ""
	})
	building := func() bool {
		n.listPush.mu.Lock()
		defer n.listPush.mu.Unlock()
		return n.listPush.building
	}
	waitWithin(t, time.Minute, "the first send's list", func() bool { return !building() })

	offlineCache.Lock() // no list can be built until it is let go
	held := true
	defer func() {
		if held {
			offlineCache.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := n.assistantSend(context.Background(), id, "", "claude", "second", nil, "", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		offlineCache.Unlock()
		held = false
		t.Fatal("the send waited for the session list")
	}
	waitFor(t, "the second message written", func() bool { return strings.Contains(interjectLog(t), "second") })
	if !building() {
		t.Fatal("the send asked for no list: its row would not show it working until the next poll")
	}

	offlineCache.Unlock()
	held = false
	waitWithin(t, time.Minute, "the list asked for by the send to be built and pushed", func() bool { return !building() })
}

// Asks for a list while one is being built fold into one more build: a burst of sends builds two lists, not one each.
func TestNotifySessionsSoonFoldsAsks(t *testing.T) {
	n := codexTestNode(t)
	withFakeHub(t, n)
	offlineCache.Lock()
	for range 20 {
		n.notifySessionsSoon()
	}
	n.listPush.mu.Lock()
	building, again := n.listPush.building, n.listPush.again
	n.listPush.mu.Unlock()
	offlineCache.Unlock()
	if !building || !again {
		t.Fatalf("20 asks during a build: building %v, another after it %v; want one build running and one more after it", building, again)
	}
	waitFor(t, "the builds to end", func() bool {
		n.listPush.mu.Lock()
		defer n.listPush.mu.Unlock()
		return !n.listPush.building
	})
}

// A message under a once_id goes in once: the manager tries a relay again under the same id when no answer came in its
// time, and that try, whether the first still runs or has been answered, writes nothing more. The id is no
// client_msg_id: no line is tied to it, since a tied line reads as the owner's own message. Another id goes in as usual.
func TestAssistantSendWithAOnceIDGoesOnce(t *testing.T) {
	interjectClaude(t, "2.1.200")
	n := codexTestNode(t)
	withFakeHub(t, n)
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "first", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.stopAssistant(id) })
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	sdkWait(t, "its init", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.claudeVersion != ""
	})
	send := func(text, onceID string) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"session_id": id, "text": text, "once_id": onceID, "resume_only": true})
		out, err := n.handle(context.Background(), "assistant.send", params)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := out.(map[string]any)["session_id"].(string); got != id {
			t.Fatalf("answered %v, want the conversation %s", out, id)
		}
	}
	send("relayed once", "relay-1")
	send("relayed once", "relay-1")
	send("relayed once", "relay-1")
	send("another one", "relay-2")
	waitFor(t, "the other message", func() bool { return strings.Contains(interjectLog(t), "another one") })
	if got := strings.Count(interjectLog(t), "relayed once"); got != 1 {
		t.Fatalf("a message sent three times under one id was written %d times, want once", got)
	}
	if n.echoes.known(id) {
		t.Fatal("a once_id was recorded to tie a line to: the relayed line would read as the owner's")
	}
}

// Normally the list that shows the conversation working goes out before the send answers, as when it was built inside
// the send: the manager drains its queues and counts rounds by the lists it is pushed.
func TestAssistantSendAnswersAfterItsList(t *testing.T) {
	interjectClaude(t, "2.1.200")
	n := codexTestNode(t)
	withFakeHub(t, n)
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "first", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.stopAssistant(id) })
	pushed := func() int {
		n.listPush.mu.Lock()
		defer n.listPush.mu.Unlock()
		return n.listPush.pushed
	}
	waitWithin(t, time.Minute, "the first send's list", func() bool {
		n.listPush.mu.Lock()
		defer n.listPush.mu.Unlock()
		return !n.listPush.building
	})
	before := pushed()
	if _, err := n.assistantSend(context.Background(), id, "", "claude", "second", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if pushed() == before {
		t.Fatal("the send answered before a list built after its line went out")
	}
}
