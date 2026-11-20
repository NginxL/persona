package link

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type reviewBlockingIdleTransport struct {
	started chan struct{}
	release chan struct{}
}

func (t *reviewBlockingIdleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.Canceled
}

func (t *reviewBlockingIdleTransport) CloseIdleConnections() {
	close(t.started)
	<-t.release
}

func TestReviewDirectWinnerIsNotOrphanedWhenOldReadAlreadyReturned(t *testing.T) {
	hub := newSSHTestHub(t, "")
	directAccepted := make(chan *websocket.Conn, 1)
	directEnded := make(chan struct{}, 1)
	direct := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		directAccepted <- c
		if _, _, err := c.Read(context.Background()); err != nil {
			directEnded <- struct{}{}
		}
	}))
	direct.TLS = hub.srv.TLS.Clone()
	direct.StartTLS()
	t.Cleanup(direct.Close)
	t.Cleanup(func() {
		select {
		case c := <-directAccepted:
			c.CloseNow()
		default:
		}
	})

	path := writeDirectHandoffConfig(t, hub, direct)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	oldServerConn := make(chan *websocket.Conn, 1)
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		oldServerConn <- c
		_, _, _ = c.Read(context.Background())
	}))
	t.Cleanup(oldServer.Close)
	oldClient, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(oldServer.URL, "http")+Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldClient.CloseNow() })
	oldPeer := <-oldServerConn

	transport := &reviewBlockingIdleTransport{started: make(chan struct{}), release: make(chan struct{})}
	old := &link{
		conn:    oldClient,
		ep:      Endpoint{Kind: KindSSH},
		streams: &http.Client{Transport: transport},
		token:   cfg.Token,
	}
	logStarted := make(chan struct{})
	releaseLog := make(chan struct{})
	var logOnce, cleanupOnce sync.Once
	releaseLogFn := func() { logOnce.Do(func() { close(releaseLog) }) }
	releaseCleanupFn := func() { cleanupOnce.Do(func() { close(transport.release) }) }
	t.Cleanup(releaseLogFn)
	t.Cleanup(releaseCleanupFn)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type serveResult struct {
		handoff *directHandoff
		err     error
	}
	serveDone := make(chan serveResult, 1)
	published := make(chan *directHandoff, 1)
	go func() {
		handoff, err := old.serve(ctx, cfg, Options{
			Path:             path,
			PingEvery:        time.Hour,
			PingTimeout:      time.Second,
			PairCheck:        time.Hour,
			DirectRetryEvery: 10 * time.Millisecond,
			directReady:      published,
			Logf: func(format string, args ...any) {
				if strings.Contains(format, "direct hub reachable again") {
					close(logStarted)
					<-releaseLog
				}
			},
		}, &reporter{}, nil)
		serveDone <- serveResult{handoff: handoff, err: err}
	}()

	select {
	case <-logStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("direct candidate did not reach the handoff")
	}
	oldPeer.CloseNow()
	select {
	case <-transport.started:
		// The read loop has already selected a nil handoff and is in serve's defers.
	case <-time.After(5 * time.Second):
		t.Fatal("old link did not enter cleanup")
	}
	releaseLogFn()
	select {
	case handoff := <-published:
		// The candidate was published after the read loop returned. Put it back so serve's cleanup must reclaim it.
		published <- handoff
	case <-time.After(5 * time.Second):
		t.Fatal("direct candidate was not published after the read loop returned")
	}
	releaseCleanupFn()

	var result serveResult
	select {
	case result = <-serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("old link serve did not return")
	}
	if result.handoff != nil {
		result.handoff.candidate.closeNow()
		t.Fatal("interleaving unexpectedly returned a handoff")
	}
	select {
	case <-directEnded:
		// The candidate was reclaimed.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("accepted direct candidate was orphaned after the old read loop returned a nil handoff")
	}
}
