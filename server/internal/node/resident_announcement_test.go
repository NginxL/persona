package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestResidentReportsUpdatePreflightFailure(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	peer, manager := rpc.New("node", a), rpc.New("manager", b)
	failures := make(chan proto.NodeUpdatePreflightFailure, 1)
	manager.OnEvent = func(event string, params json.RawMessage) {
		var failure proto.NodeUpdatePreflightFailure
		if event == nodeproto.EventUpdatePreflightFailed && json.Unmarshal(params, &failure) == nil {
			failures <- failure
		}
	}
	go func() { _ = manager.Serve(ctx, b) }()
	reload := &ResidentReload{}
	reload.peer(peer, func() { _ = a.Close() })
	reload.ReportUpdatePreflightFailure(ctx, "update-one", errors.New("exit status 23"))
	select {
	case got := <-failures:
		want := proto.NodeUpdatePreflightFailure{ID: "update-one", Error: "exit status 23"}
		if got != want {
			t.Fatalf("failure = %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("manager did not receive the preflight failure")
	}
	if leave, err := reload.enter(); err != nil {
		t.Fatalf("preflight failure suspended the resident: %v", err)
	} else {
		leave()
	}
}

type heldReloadNotice struct {
	net.Conn
	started, release, finished chan struct{}
}

func (w *heldReloadNotice) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	defer close(w.finished)
	return w.Conn.Write(p)
}

func TestResidentAnnouncementAbortCannotArriveLate(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "writing", true: "queued-behind-old-event"}[queued], func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			writer := &heldReloadNotice{Conn: a, started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(writer.release) })
			peer, manager := rpc.New("n", writer), rpc.New("m", b)
			events := make(chan string, 4)
			manager.OnEvent = func(event string, _ json.RawMessage) { events <- event }
			served := make(chan struct{})
			go func() { defer close(served); _ = manager.Serve(ctx, b) }()
			reload := &ResidentReload{}
			closed := make(chan struct{})
			var disconnect sync.Once
			reload.peer(peer, func() {
				disconnect.Do(func() { _ = a.Close(); close(closed) })
			})
			if queued {
				go func() { _ = peer.Notify("older-event", nil) }()
				select {
				case <-writer.started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			announcement, abort := context.WithCancel(ctx)
			defer abort()
			result := make(chan error, 1)
			go func() { result <- reload.Announce(announcement) }()
			if !queued {
				select {
				case <-writer.started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			abort()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("stalled announcement did not abort: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-closed:
			default:
				t.Fatal("unconfirmed control write left the physical connection alive")
			}
			reload.Resume()
			reload.CancelAnnouncement()
			release.Do(func() { close(writer.release) })
			select {
			case <-writer.finished:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-served:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case event := <-events:
				t.Fatalf("control message arrived after abort: %s", event)
			default:
			}
		})
	}
}
