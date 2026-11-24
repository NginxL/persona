package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/imessagewire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The node is synthetic; no Messages database or external account is opened.
type archiveSwitchNode struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	stop    sync.Once
	page    func(context.Context, string) (json.RawMessage, error)
	mu      sync.Mutex
	cursors []string
}

func (n *archiveSwitchNode) Has(id string) bool { return id == "example" }

func (n *archiveSwitchNode) Views() []devices.View {
	return []devices.View{{Summary: devices.Summary{ID: "example", Name: "Example", Online: true, Reachable: true},
		Info: proto.NodeInfo{OS: "darwin"}}}
}

func (n *archiveSwitchNode) CallNode(ctx context.Context, _ string, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "node.configure", "sessions.list":
		return json.RawMessage(`{}`), nil
	case "connector.list":
		return json.RawMessage(`{"connectors":[{"id":"imessage","status":"ok"}]}`), nil
	case "connector.chats":
		return json.RawMessage(`{"chats":[]}`), nil
	case "connector.changes":
		var query struct {
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(params, &query); err != nil {
			return nil, err
		}
		n.mu.Lock()
		n.cursors = append(n.cursors, query.Cursor)
		n.mu.Unlock()
		n.once.Do(func() { close(n.entered) })
		select {
		case <-n.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if n.page != nil {
			return n.page(ctx, query.Cursor)
		}
		at := time.Now().UTC().Format(time.RFC3339Nano)
		return json.RawMessage(fmt.Sprintf(`{"next":"9","batches":[{"chat":{"id":"imessage-0123456789ab","name":"Example","last":%q},"messages":[{"id":"9","at":%q,"kind":"message","text":"Synthetic message"}]}]}`, at, at)), nil
	default:
		return nil, fmt.Errorf("unexpected synthetic node method %s", method)
	}
}

func (n *archiveSwitchNode) unblock() { n.stop.Do(func() { close(n.release) }) }

// Hold the manager reply or the subsequent commit request at a test barrier.
type delayedArchiveReply struct {
	Extensions
	mode     string
	returned chan struct{}
	release  chan struct{}
	once     sync.Once
	stop     sync.Once
}

func (d *delayedArchiveReply) AppCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	if d.mode == "begin" && p.Method == assistantwire.ConnectorsArchiveBegin {
		d.once.Do(func() { close(d.returned) })
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, true, ctx.Err()
		}
	}
	out, handled, err := d.Extensions.AppCall(ctx, p)
	if d.mode == "reply" && p.Method == assistantwire.ConnectorsChanges && err == nil {
		d.once.Do(func() { close(d.returned) })
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, handled, ctx.Err()
		}
	}
	return out, handled, err
}

func (d *delayedArchiveReply) unblock() { d.stop.Do(func() { close(d.release) }) }

func waitArchiveSwitch(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

type archiveSwitchRig struct {
	ctx     context.Context
	node    *archiveSwitchNode
	delayed *delayedArchiveReply
	h       *extmgr.Manager
	s       *Service
	bus     *BusHub
	e       *kb.Engine
	store   *archiveSwitchStore
}

type archiveDropBegin struct {
	acquired    chan struct{}
	reconnect   chan struct{}
	dropped     atomic.Bool
	connections atomic.Int32
	release     sync.Once
}

type archiveSwitchStore struct {
	*memStore
	writes             chan bool
	failAfterWrite     atomic.Bool
	failAIRead         atomic.Bool
	waitAIContext      atomic.Bool
	stallReservation   atomic.Bool
	reservationEntered chan struct{}
	reservationRelease chan struct{}
	stallOnce          sync.Once
	releaseOnce        sync.Once
}

func (s *archiveSwitchStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == "imessage.ai" && s.failAIRead.Load() {
		return false, errors.New("synthetic settings read failure")
	}
	return s.memStore.Setting(ctx, key, out)
}

func (s *archiveSwitchStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == "imessage.archive_reservations" && s.stallReservation.Load() {
		s.stallOnce.Do(func() { close(s.reservationEntered) })
		select {
		case <-s.reservationRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := s.memStore.PutSetting(ctx, key, value); err != nil {
		return err
	}
	if on, ok := value.(bool); key == "imessage.ai" && ok {
		s.writes <- on
		if s.waitAIContext.Load() {
			<-ctx.Done()
			return ctx.Err()
		}
		if s.failAfterWrite.Swap(false) {
			return errors.New("synthetic lost setting acknowledgement")
		}
	}
	return nil
}

func (s *archiveSwitchStore) releaseReservation() {
	s.releaseOnce.Do(func() { close(s.reservationRelease) })
}

// A remote relay exercises BusHub's client.Call path, with a synthetic bus peer
// forwarding app.call to the same manager methods used by the local adapter.
func newArchiveSwitchRig(t *testing.T, remote, holdReply, oldPeer bool, drop ...*archiveDropBegin) *archiveSwitchRig {
	mode := ""
	if holdReply {
		mode = "reply"
	}
	return newArchiveSwitchRigMode(t, remote, mode, oldPeer, drop...)
}

func newArchiveSwitchRigMode(t *testing.T, remote bool, mode string, oldPeer bool, drop ...*archiveDropBegin) *archiveSwitchRig {
	needsIMessageModule(t)
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	node := &archiveSwitchNode{entered: make(chan struct{}), release: make(chan struct{})}
	store := &archiveSwitchStore{memStore: &memStore{}, writes: make(chan bool, 32),
		reservationEntered: make(chan struct{}), reservationRelease: make(chan struct{})}
	var ext Extensions
	var srv *httptest.Server
	if remote {
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var dropBegin *archiveDropBegin
			if len(drop) > 0 {
				dropBegin = drop[0]
			}
			if dropBegin != nil && dropBegin.connections.Add(1) > 1 {
				select {
				case <-dropBegin.reconnect:
				case <-r.Context().Done():
					return
				}
			}
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			conn := bus.NewConn(r.Context(), ws)
			peer := rpc.New("h", conn)
			peer.OnRequest = func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
				switch method {
				case assistantwire.MethodHello:
					return map[string]any{"kb": map[string]any{}}, nil
				case assistantwire.MethodApp:
					var p assistantwire.AppCall
					if err := json.Unmarshal(raw, &p); err != nil {
						return nil, err
					}
					if oldPeer && (p.Method == assistantwire.ConnectorsArchiveBegin || p.Method == assistantwire.ConnectorsArchiveAbort) {
						return nil, &rpc.Error{Code: "not_found", Message: "unknown method"}
					}
					out, handled, err := ext.AppCall(ctx, p)
					if dropBegin != nil && p.Method == assistantwire.ConnectorsArchiveBegin && err == nil && dropBegin.dropped.CompareAndSwap(false, true) {
						close(dropBegin.acquired)
						_ = ws.CloseNow()
						return nil, errors.New("synthetic lost acknowledgement")
					}
					if !handled && err == nil {
						return nil, errors.New("app method not handled")
					}
					return out, err
				default:
					return map[string]any{}, nil
				}
			}
			_ = peer.Serve(r.Context(), conn)
		}))
	}
	hub := "http://127.0.0.1:1"
	if remote {
		hub = srv.URL
	}
	bh := NewBusHub(BusOptions{Hub: hub, Token: "synthetic"})
	s := New(Options{Hub: bh, Store: func() Store { return store }, KB: &KBOptions{Dir: t.TempDir()}})
	s.inbox.init()
	bh.svc = s
	h := extmgr.New(extmgr.Options{Dir: t.TempDir(), Config: &extmgr.Config{IMessage: &imessagewire.Config{Machine: "example"}},
		Store: store, Devices: node, Assistant: bh.Answer, AssistantEvent: bh.TellLocal})
	delayed := &delayedArchiveReply{Extensions: h, mode: mode, returned: make(chan struct{}), release: make(chan struct{})}
	ext = h
	if mode != "" {
		ext = delayed
	}
	var served chan struct{}
	if remote {
		served = make(chan struct{})
		go func() { _ = bh.Serve(ctx, s); close(served) }()
		connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
		err := bh.Client().Wait(connectCtx)
		connectCancel()
		if err != nil {
			cancel()
			waitArchiveSwitch(t, served, "bus shutdown")
			srv.Close()
			t.Fatalf("connect synthetic bus: %v", err)
		}
	} else {
		bh.opts.Extensions = ext
		bh.hello = Hello{KB: &KBSettings{}}
		bh.helloOnce.Do(func() { close(bh.helloed) })
	}
	done := make(chan struct{})
	go func() { _ = h.Run(ctx); close(done) }()
	t.Cleanup(func() {
		node.unblock()
		delayed.unblock()
		store.releaseReservation()
		if len(drop) > 0 && drop[0] != nil {
			drop[0].release.Do(func() { close(drop[0].reconnect) })
		}
		cancel()
		waitArchiveSwitch(t, done, "manager shutdown")
		if served != nil {
			waitArchiveSwitch(t, served, "bus shutdown")
			srv.Close()
		}
	})
	waitArchiveSwitch(t, h.Started(), "manager startup")
	status := httptest.NewRecorder()
	h.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/imessage", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &archiveSwitchRig{ctx: ctx, node: node, delayed: delayed, h: h, s: s, bus: bh, e: &kb.Engine{Store: st}, store: store}
}

func (r *archiveSwitchRig) startArchive() <-chan struct{} {
	done := make(chan struct{})
	go func() { r.s.kbConnectors(r.ctx, r.e); close(done) }()
	return done
}

func (r *archiveSwitchRig) setAsync(ctx context.Context, on bool) <-chan *httptest.ResponseRecorder {
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/imessage/settings", bytes.NewBufferString(fmt.Sprintf(`{"ai":%t}`, on))).WithContext(ctx)
		r.h.ServeHTTP(w, req)
		response <- w
	}()
	return response
}

func waitArchiveSetting(t *testing.T, response <-chan *httptest.ResponseRecorder, on bool) {
	t.Helper()
	var w *httptest.ResponseRecorder
	select {
	case w = <-response:
	case <-time.After(10 * time.Second):
		t.Fatal("settings response timed out")
	}
	var got struct {
		AI bool `json:"ai"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || got.AI != on {
		t.Fatalf("set ai=%t: %d %s (%v)", on, w.Code, w.Body.String(), err)
	}
}

func assertArchivePending(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusServiceUnavailable || body.Code != "imessage_archive_pending" {
		t.Fatalf("expected a retryable archive boundary: %d %s (%v)", w.Code, w.Body.String(), err)
	}
}

// Test the real settings route, manager, bus adapter and assistant archive loop together.
func TestIMessageDisableDuringArchiveRead(t *testing.T) {
	for _, tc := range []struct {
		name         string
		disable      bool
		reenable     bool
		replied      bool
		beforeCommit bool
		remote       bool
	}{
		{name: "enabled"},
		{name: "node read pending", disable: true},
		{name: "node read pending across off and on", disable: true, reenable: true},
		{name: "manager reply returned", disable: true, replied: true},
		{name: "manager reply returned across off and on", disable: true, reenable: true, replied: true},
		{name: "remote bus node read pending", disable: true, remote: true},
		{name: "remote bus reply across off and on", disable: true, reenable: true, replied: true, remote: true},
		{name: "page received before commit", disable: true, beforeCommit: true},
		{name: "remote page before commit across off and on", disable: true, reenable: true, beforeCommit: true, remote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := ""
			if tc.replied {
				mode = "reply"
			} else if tc.beforeCommit {
				mode = "begin"
			}
			r := newArchiveSwitchRigMode(t, tc.remote, mode, false)
			archived := r.startArchive()
			waitArchiveSwitch(t, r.node.entered, "archive node read")
			if tc.replied || tc.beforeCommit {
				r.node.unblock()
				waitArchiveSwitch(t, r.delayed.returned, "manager reply")
			}
			if tc.disable {
				waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
			}
			if tc.reenable {
				waitArchiveSetting(t, r.setAsync(r.ctx, true), true)
			}
			r.node.unblock()
			r.delayed.unblock()
			waitArchiveSwitch(t, archived, "archive completion")
			chat, exists := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
			cursor, err := os.ReadFile(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json"))
			if tc.disable {
				if exists || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("retired read was committed: chat=%+v cursor=%s error=%v", chat, cursor, err)
				}
			} else if !exists || chat.Messages != 1 || err != nil || string(cursor) != `{"cursor":"9"}` {
				t.Fatalf("enabled read: chat=%+v cursor=%s error=%v", chat, cursor, err)
			}
			if tc.reenable {
				r.s.kb.mu.Lock()
				r.s.kb.archivedAt["imessage"] = time.Time{}
				r.s.kb.mu.Unlock()
				waitArchiveSwitch(t, r.startArchive(), "new generation retry")
				chat, exists = r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
				cursor, err = os.ReadFile(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json"))
				if !exists || chat.Messages != 1 || err != nil || string(cursor) != `{"cursor":"9"}` {
					t.Fatalf("reenabled retry: chat=%+v cursor=%s error=%v", chat, cursor, err)
				}
			}
		})
	}
}

func TestIMessageUncertainOffRetiresPendingPage(t *testing.T) {
	r := newArchiveSwitchRigMode(t, false, "begin", false)
	archived := r.startArchive()
	waitArchiveSwitch(t, r.node.entered, "archive node read")
	r.node.unblock()
	waitArchiveSwitch(t, r.delayed.returned, "pending page commit")
	r.store.failAIRead.Store(true) // stop background reconciliation from hiding the transition being tested
	r.store.failAfterWrite.Store(true)
	response := r.setAsync(r.ctx, false)
	select {
	case w := <-response:
		if w.Code == http.StatusOK {
			t.Fatalf("an uncertain settings write returned success: %s", w.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("uncertain off did not return")
	}
	var saved bool
	if ok, err := r.store.memStore.Setting(r.ctx, "imessage.ai", &saved); !ok || err != nil || saved {
		t.Fatalf("the synthetic off was not persisted: saved=%v found=%v err=%v", saved, ok, err)
	}
	// Even after a subsequent on, the page admitted before the failed off is retired.
	waitArchiveSetting(t, r.setAsync(r.ctx, true), true)
	r.store.failAIRead.Store(false)
	r.delayed.unblock()
	waitArchiveSwitch(t, archived, "retired pending page")
	if chat, exists := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab")); exists {
		t.Fatalf("the earlier generation committed after uncertain off/on: %+v", chat)
	}
	if _, err := os.Stat(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the earlier generation advanced its cursor: %v", err)
	}
}

func TestIMessageCancelledRetireWhileReservationStoreIsBusy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enable    bool
		reenable  bool
		waitWrite bool
	}{
		{name: "off timeout"},
		{name: "off timeout then on", reenable: true},
		{name: "on write failure", enable: true},
		{name: "on write failure then on", enable: true, reenable: true},
		{name: "on write deadline then on", enable: true, reenable: true, waitWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newArchiveSwitchRigMode(t, false, "begin", false)
			archived := r.startArchive()
			waitArchiveSwitch(t, r.node.entered, "archive node read")
			r.node.unblock()
			waitArchiveSwitch(t, r.delayed.returned, "old page before commit")
			changes, handled, err := r.h.AppCall(r.ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors,
				Method: assistantwire.ConnectorsChanges, Params: json.RawMessage(`{"id":"imessage","cursor":"","fence_version":1}`)})
			var page assistantwire.ChangesResult
			if !handled || err != nil || json.Unmarshal(changes, &page) != nil || page.Generation == "" {
				t.Fatalf("read another synthetic page: %s %v", changes, err)
			}
			req := assistantwire.ArchiveFence{ID: "imessage", Generation: page.Generation, Token: strings.Repeat("a", 32)}
			raw, _ := json.Marshal(req)
			r.store.stallReservation.Store(true)
			r.store.failAIRead.Store(true)
			if tc.waitWrite {
				r.store.waitAIContext.Store(true)
			} else if tc.enable {
				r.store.failAfterWrite.Store(true)
			}
			beginDone := make(chan error, 1)
			go func() {
				_, _, err := r.h.AppCall(r.ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors,
					Method: assistantwire.ConnectorsArchiveBegin, Params: raw})
				beginDone <- err
			}()
			waitArchiveSwitch(t, r.store.reservationEntered, "stalled reservation store")
			deadline, cancel := context.WithTimeout(r.ctx, 300*time.Millisecond)
			response := r.setAsync(deadline, tc.enable)
			select {
			case w := <-response:
				if w.Code == http.StatusOK {
					t.Fatalf("a timed-out settings change returned success: %s", w.Body.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("settings change ignored its deadline")
			}
			cancel()
			r.store.waitAIContext.Store(false)
			r.store.releaseReservation()
			select {
			case <-beginDone:
			case <-time.After(10 * time.Second):
				t.Fatal("stalled reservation did not return")
			}
			// End the synthetic reservation without changing its generation.
			if _, handled, err := r.h.AppCall(r.ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors,
				Method: assistantwire.ConnectorsArchiveEnd, Params: raw}); !handled || err != nil {
				t.Fatalf("release synthetic reservation: handled=%v err=%v", handled, err)
			}
			if tc.reenable {
				waitArchiveSetting(t, r.setAsync(r.ctx, true), true)
			}
			r.delayed.unblock()
			waitArchiveSwitch(t, archived, "old page after cancelled setting")
			if chat, ok := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab")); ok {
				t.Fatalf("cancelled retirement allowed the old page to commit: %+v", chat)
			}
			if _, err := os.Stat(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled retirement advanced the cursor: %v", err)
			}
		})
	}
}

func TestIMessageDisableWaitsForAdmittedCommit(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote=%t", remote), func(t *testing.T) {
			r := newArchiveSwitchRig(t, remote, false, false)
			entered, finish, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			r.e.Changed = func(change kb.Change) {
				if change.Kind == kb.KindChat && change.What == "messages" {
					once.Do(func() {
						close(entered) // message committed; the page and its cursor are still in progress
						<-release
						close(finish)
					})
				}
			}
			r.e.ObserveChanges()
			r.node.unblock()
			archived := r.startArchive()
			waitArchiveSwitch(t, entered, "commit entry")
			ctx, cancel := context.WithTimeout(r.ctx, 300*time.Millisecond)
			response := r.setAsync(ctx, false)
			select {
			case <-r.store.writes:
			case <-time.After(10 * time.Second):
				t.Fatal("disable did not reach persistence")
			}
			select {
			case w := <-response:
				assertArchivePending(t, w)
			case <-time.After(10 * time.Second):
				t.Fatal("cancelled disable did not return")
			}
			cancel()
			// Retry while the same admitted page is still committing; it may finish before the successful response.
			response = r.setAsync(r.ctx, false)
			select {
			case <-r.store.writes:
			case <-time.After(10 * time.Second):
				t.Fatal("disable retry did not reach persistence")
			}
			secondCtx, secondCancel := context.WithTimeout(r.ctx, 300*time.Millisecond)
			second := r.setAsync(secondCtx, true)
			select {
			case w := <-second:
				if w.Code != http.StatusRequestTimeout {
					t.Fatalf("cancelled settings request: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a settings request did not stop waiting after cancellation")
			}
			secondCancel()
			observed := make(chan bool, 1)
			go func() {
				w := <-response
				finished := false
				select {
				case <-finish:
					finished = true
				default:
				}
				cursor, err := os.ReadFile(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json"))
				observed <- w.Code == http.StatusOK && finished && err == nil && string(cursor) == `{"cursor":"9"}`
			}()
			releaseOnce.Do(func() { close(release) })
			waitArchiveSwitch(t, archived, "admitted page completion")
			select {
			case ok := <-observed:
				if !ok {
					t.Fatal("successful response preceded commit completion or cursor publication")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("disable retry did not return")
			}
			chat, ok := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
			if !ok || chat.Messages != 1 {
				t.Fatalf("already committed data was lost: %+v", chat)
			}
		})
	}
}

func TestIMessageDisableAndForgetDoNotDeadlock(t *testing.T) {
	r := newArchiveSwitchRig(t, false, false, false)
	r.s.kb.cur.Store(&kbOpened{eng: r.e, set: KBSettings{}})
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	r.e.Changed = func(change kb.Change) {
		if change.Kind == kb.KindChat && change.What == "messages" {
			once.Do(func() { close(entered); <-release })
		}
	}
	r.e.ObserveChanges()
	r.node.unblock()
	archived := r.startArchive()
	waitArchiveSwitch(t, entered, "admitted commit")
	off := r.setAsync(r.ctx, false)
	select {
	case <-r.store.writes:
	case <-time.After(10 * time.Second):
		t.Fatal("disable did not reach persistence")
	}
	forgotten := make(chan error, 1)
	go func() {
		_, err := r.s.kbForget(r.ctx, "imessage")
		forgotten <- err
	}()
	releaseOnce.Do(func() { close(release) })
	waitArchiveSetting(t, off, false)
	waitArchiveSwitch(t, archived, "archive after disable")
	select {
	case err := <-forgotten:
		if err != nil {
			t.Fatalf("forget failed after the archive released its lock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("disable and forget deadlocked")
	}
}

func TestIMessageArchiveFailureRetriesWithoutAdvancingCursor(t *testing.T) {
	r := newArchiveSwitchRig(t, false, false, false)
	checkpoint := filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")
	if err := os.MkdirAll(checkpoint, 0o700); err != nil {
		t.Fatal(err)
	}
	r.node.unblock()
	waitArchiveSwitch(t, r.startArchive(), "failed archive page")
	chat, ok := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
	if !ok || chat.Messages != 1 {
		t.Fatalf("expected the committed message before the checkpoint failure: %+v", chat)
	}
	if err := os.Remove(checkpoint); err != nil {
		t.Fatal(err)
	}
	waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
	waitArchiveSetting(t, r.setAsync(r.ctx, true), true)
	r.s.kb.mu.Lock()
	r.s.kb.archivedAt["imessage"] = time.Time{}
	r.s.kb.mu.Unlock()
	waitArchiveSwitch(t, r.startArchive(), "checkpoint retry")
	chat, ok = r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
	cursor, err := os.ReadFile(checkpoint)
	if !ok || chat.Messages != 1 || err != nil || string(cursor) != `{"cursor":"9"}` {
		t.Fatalf("retry duplicated data or lost the cursor: chat=%+v cursor=%s err=%v", chat, cursor, err)
	}
}

func TestIMessageFailedIngestJournalStaysReservedUntilRecovered(t *testing.T) {
	r := newArchiveSwitchRig(t, false, false, false)
	at := time.Now().Add(-time.Minute).UTC()
	r.node.page = func(_ context.Context, cursor string) (json.RawMessage, error) {
		if cursor != "" {
			return nil, fmt.Errorf("unexpected cursor %q", cursor)
		}
		timestamp := at.Format(time.RFC3339Nano)
		return json.RawMessage(fmt.Sprintf(`{"next":"9","batches":[{"chat":{"id":"imessage-0123456789ab","name":"Example","last":%q},"messages":[{"id":"9","at":%q,"kind":"message","text":"Synthetic recovery message"}]}]}`, timestamp, timestamp)), nil
	}
	chatID := kb.ChatID("imessage", "0123456789ab")
	messages := r.e.Store.Path(kb.KindChat, chatID, "messages")
	if err := os.MkdirAll(messages, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(messages, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(messages, 0o700) })
	probe := filepath.Join(messages, "probe")
	if err := os.WriteFile(probe, []byte("synthetic"), 0o600); err == nil {
		_ = os.Remove(probe)
		t.Skip("the test filesystem does not enforce directory write permissions")
	}
	r.node.unblock()
	waitArchiveSwitch(t, r.startArchive(), "failed journal write")
	if _, err := os.Stat(r.e.Store.Path(kb.KindChat, chatID, ".ingest.json")); err != nil {
		t.Fatalf("expected an unfinished ingest journal: %v", err)
	}
	deadline, cancel := context.WithTimeout(r.ctx, 300*time.Millisecond)
	response := r.setAsync(deadline, false)
	select {
	case w := <-response:
		assertArchivePending(t, w)
	case <-time.After(10 * time.Second):
		t.Fatal("disable did not respect its deadline")
	}
	cancel()
	if err := os.Chmod(messages, 0o700); err != nil {
		t.Fatal(err)
	}
	waitArchiveSwitch(t, r.startArchive(), "journal recovery")
	chat, ok := r.e.Store.Chat(chatID)
	if !ok || chat.Messages != 1 {
		t.Fatalf("journal was not recovered under the reservation: %+v", chat)
	}
	checkpoint := filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")
	if _, err := os.Stat(checkpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed page advanced its cursor: %v", err)
	}
	waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
	waitArchiveSetting(t, r.setAsync(r.ctx, true), true)
	r.s.kb.mu.Lock()
	r.s.kb.archivedAt["imessage"] = time.Time{}
	r.s.kb.mu.Unlock()
	waitArchiveSwitch(t, r.startArchive(), "recovered page retry")
	chat, ok = r.e.Store.Chat(chatID)
	cursor, err := os.ReadFile(checkpoint)
	if !ok || chat.Messages != 1 || err != nil || string(cursor) != `{"cursor":"9"}` {
		t.Fatalf("recovered page was duplicated or skipped: chat=%+v cursor=%s err=%v", chat, cursor, err)
	}
}

func TestIMessageDisableKeepsCompletedArchive(t *testing.T) {
	r := newArchiveSwitchRig(t, false, false, false)
	r.node.unblock()
	waitArchiveSwitch(t, r.startArchive(), "completed archive")
	waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
	chat, ok := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
	cursor, err := os.ReadFile(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json"))
	if !ok || chat.Messages != 1 || err != nil || string(cursor) != `{"cursor":"9"}` {
		t.Fatalf("disable changed already committed data: chat=%+v cursor=%s err=%v", chat, cursor, err)
	}
}

func TestIMessageUnknownRemoteFenceFailsClosed(t *testing.T) {
	r := newArchiveSwitchRig(t, true, false, true)
	r.node.unblock()
	waitArchiveSwitch(t, r.startArchive(), "old peer archive")
	if _, ok := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab")); ok {
		t.Fatal("an old peer without commit reservations wrote a message")
	}
	if _, err := os.Stat(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an old peer advanced the cursor: %v", err)
	}
	waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
}

func TestIMessageMultipleBatchesResumeAtLastCommittedCursor(t *testing.T) {
	r := newArchiveSwitchRig(t, true, false, false)
	secondRead, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	r.node.page = func(ctx context.Context, cursor string) (json.RawMessage, error) {
		at := time.Now().UTC().Format(time.RFC3339Nano)
		if cursor == "" {
			return json.RawMessage(fmt.Sprintf(`{"next":"5","more":true,"batches":[{"chat":{"id":"imessage-0123456789ab","name":"Example","last":%q},"messages":[{"id":"1","at":%q,"kind":"message","text":"Synthetic first"}]},{"chat":{"id":"imessage-0123456789ac","name":"Other","last":%q},"messages":[{"id":"2","at":%q,"kind":"message","text":"Synthetic second"}]}]}`, at, at, at, at)), nil
		}
		if cursor != "5" {
			return nil, fmt.Errorf("unexpected cursor %q", cursor)
		}
		enteredOnce.Do(func() { close(secondRead) })
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.RawMessage(fmt.Sprintf(`{"next":"9","batches":[{"chat":{"id":"imessage-0123456789ab","name":"Example","last":%q},"messages":[{"id":"9","at":%q,"kind":"message","text":"Synthetic third"}]}]}`, at, at)), nil
	}
	r.node.unblock()
	archived := r.startArchive()
	waitArchiveSwitch(t, secondRead, "second page read")
	checkpoint := filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")
	if cursor, err := os.ReadFile(checkpoint); err != nil || string(cursor) != `{"cursor":"5"}` {
		t.Fatalf("first page checkpoint: %s %v", cursor, err)
	}
	waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
	waitArchiveSetting(t, r.setAsync(r.ctx, true), true)
	releaseOnce.Do(func() { close(release) })
	waitArchiveSwitch(t, archived, "retired second page")
	first, firstOK := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
	other, otherOK := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ac"))
	if cursor, err := os.ReadFile(checkpoint); err != nil || string(cursor) != `{"cursor":"5"}` || !firstOK || !otherOK || first.Messages != 1 || other.Messages != 1 {
		t.Fatalf("retired page changed committed data: cursor=%s err=%v first=%+v other=%+v", cursor, err, first, other)
	}
	r.s.kb.mu.Lock()
	r.s.kb.archivedAt["imessage"] = time.Time{}
	r.s.kb.mu.Unlock()
	waitArchiveSwitch(t, r.startArchive(), "second page retry")
	first, firstOK = r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
	if cursor, err := os.ReadFile(checkpoint); err != nil || string(cursor) != `{"cursor":"9"}` || !firstOK || first.Messages != 2 {
		t.Fatalf("retry did not resume: cursor=%s err=%v first=%+v", cursor, err, first)
	}
	r.node.mu.Lock()
	cursors := append([]string(nil), r.node.cursors...)
	r.node.mu.Unlock()
	if fmt.Sprint(cursors) != "[ 5 5]" {
		t.Fatalf("node cursors = %q", cursors)
	}
}

func TestIMessageCancelledArchiveReadCanRetry(t *testing.T) {
	r := newArchiveSwitchRig(t, false, false, false)
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	go func() { r.s.kbConnectors(ctx, r.e); close(done) }()
	waitArchiveSwitch(t, r.node.entered, "cancelled node read")
	cancel()
	waitArchiveSwitch(t, done, "cancelled archive")
	if _, err := os.Stat(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled read advanced cursor: %v", err)
	}
	waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
	waitArchiveSetting(t, r.setAsync(r.ctx, true), true)
	r.node.unblock()
	r.s.kb.mu.Lock()
	r.s.kb.archivedAt["imessage"] = time.Time{}
	r.s.kb.mu.Unlock()
	waitArchiveSwitch(t, r.startArchive(), "cancelled read retry")
	chat, ok := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab"))
	cursor, err := os.ReadFile(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json"))
	if !ok || chat.Messages != 1 || err != nil || string(cursor) != `{"cursor":"9"}` {
		t.Fatalf("cancelled read retry: chat=%+v cursor=%s err=%v", chat, cursor, err)
	}
}

func TestIMessageUnknownLegacyManagerIsSkipped(t *testing.T) {
	f := newFakeHub()
	f.answerApp(AppConnectors, assistantwire.ConnectorsArchives, func(json.RawMessage) (any, error) {
		return assistantwire.Archives{Archives: []assistantwire.ArchiveSource{{ID: "imessage"}, {ID: "example"}}}, nil
	})
	f.answerApp(AppConnectors, assistantwire.ConnectorsChanges, func(json.RawMessage) (any, error) {
		return assistantwire.ChangesResult{Next: "1"}, nil
	})
	s := newKBService(t, f)
	e := kbEng(t, s)
	s.kbConnectors(context.Background(), e)
	params := f.appParams(AppConnectors, assistantwire.ConnectorsChanges)
	if len(params) != 1 || params[0]["id"] != "example" {
		t.Fatalf("legacy iMessage must be skipped while other connectors work: %v", params)
	}
}

func TestIMessageLostBeginAcknowledgementFailsClosedAndRecovers(t *testing.T) {
	drop := &archiveDropBegin{acquired: make(chan struct{}), reconnect: make(chan struct{})}
	r := newArchiveSwitchRig(t, true, false, false, drop)
	r.node.unblock()
	archived := r.startArchive()
	waitArchiveSwitch(t, drop.acquired, "source reservation before disconnect")
	waitArchiveSwitch(t, archived, "disconnected archive")
	if _, ok := r.e.Store.Chat(kb.ChatID("imessage", "0123456789ab")); ok {
		t.Fatal("an unacknowledged reservation wrote a message")
	}
	if _, err := os.Stat(filepath.Join(r.e.Store.Dir, "cursors", "imessage.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unacknowledged reservation advanced the cursor: %v", err)
	}
	deadline, cancel := context.WithTimeout(r.ctx, 300*time.Millisecond)
	response := r.setAsync(deadline, false)
	select {
	case w := <-response:
		assertArchivePending(t, w)
	case <-time.After(10 * time.Second):
		t.Fatal("disable did not respect its deadline")
	}
	cancel()
	drop.release.Do(func() { close(drop.reconnect) })
	connected, stop := context.WithTimeout(r.ctx, 10*time.Second)
	defer stop()
	if err := r.bus.Client().Wait(connected); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if failed := r.s.retryArchiveReservations(connected); len(failed) != 0 {
		t.Fatalf("retrying the uncertain reservation: %v", failed)
	}
	waitArchiveSetting(t, r.setAsync(r.ctx, false), false)
}
