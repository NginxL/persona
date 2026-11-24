package streams

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestEventAckIgnoresNonPositiveCounts(t *testing.T) {
	m := New(func(string, any) error { return nil })
	if err := m.Open("s1"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.Event(EventAck, json.RawMessage(`{"id":"s1","n":-1000000}`))

	m.mu.Lock()
	s := m.streams["s1"]
	m.mu.Unlock()
	s.mu.Lock()
	inflight := s.inflight
	s.mu.Unlock()
	if inflight != 0 {
		t.Fatalf("inflight after negative EventAck = %d, want 0", inflight)
	}
}

func TestEventDataIgnoresEmptyFrames(t *testing.T) {
	m := New(func(string, any) error { return nil })
	if err := m.Open("s1"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.Event(EventData, json.RawMessage(`{"id":"s1"}`))
	m.Event(EventData, json.RawMessage(`{"id":"s1","data":""}`))

	m.mu.Lock()
	s := m.streams["s1"]
	m.mu.Unlock()
	s.mu.Lock()
	qlen := len(s.queue)
	s.mu.Unlock()
	if qlen != 0 {
		t.Fatalf("queued slice count after empty EventData frames = %d, want 0", qlen)
	}
}

func TestEventWindowExceededDoesNotBlockCaller(t *testing.T) {
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	t.Cleanup(release)

	closedCh := make(chan dataMsg, 2)
	m := New(func(event string, params any) error {
		<-unblock
		if event == EventClose {
			if msg, ok := params.(dataMsg); ok {
				closedCh <- msg
			}
		}
		return nil
	})
	if err := m.Open("s1"); err != nil {
		t.Fatalf("Open: %v", err)
	}

	oversized, err := json.Marshal(dataMsg{ID: "s1", Data: make([]byte, window+1)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	returned := make(chan struct{})
	go func() {
		m.Event(EventData, oversized)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		release()
		<-returned
		t.Fatal("Mux.Event blocked on m.send when aborting window-exceeded stream")
	}

	small, _ := json.Marshal(dataMsg{ID: "s1", Data: []byte("x")})
	m.Event(EventData, small)

	release()
	select {
	case msg := <-closedCh:
		if msg.ID != "s1" || msg.Err == "" {
			t.Fatalf("EventClose = %+v, want s1 with non-empty error", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected async EventClose after releasing send")
	}
	waitFor(t, func() bool { return m.Len() == 0 }, "aborted stream removed from Mux")
}
