package feishubot_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/modules/lark/feishubot"
)

const tokenPath = "/open-apis/auth/v3/tenant_access_token/internal"

func TestTokenWaitCancellation(t *testing.T) {
	for _, method := range []string{"bot info", "resource", "unkeyed send"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", method, deadline), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				var tokens, calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == tokenPath {
						tokens.Add(1)
						close(entered)
						<-release
						fmt.Fprint(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
						return
					}
					calls.Add(1)
					fmt.Fprint(w, `{"code":0,"data":{"bot":{"open_id":"synthetic-bot"}}}`)
				}))
				defer srv.Close()
				defer unblock()
				c := feishubot.New("synthetic-app", "synthetic-secret", srv.URL)
				leader := make(chan error, 1)
				go func() { _, err := c.BotInfo(context.Background()); leader <- err }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("token fetch never started")
				}
				ctx, cancel := context.WithCancel(context.Background())
				want := error(context.Canceled)
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
					want = context.DeadlineExceeded
				}
				defer cancel()
				waiting := &observingContext{Context: ctx, waiting: make(chan struct{})}
				waiter := make(chan error, 1)
				go func() {
					var err error
					switch method {
					case "bot info":
						_, err = c.BotInfo(waiting)
					case "resource":
						_, _, err = c.Resource(waiting, "synthetic-message", "synthetic-key", "file", 1024)
					case "unkeyed send":
						_, err = c.Send(waiting, "chat_id", "synthetic-chat", feishubot.TypeText, `{"text":"test"}`, "")
					}
					waiter <- err
				}()
				select {
				case <-waiting.waiting:
				case <-time.After(time.Second):
					t.Fatal("caller could not reach a cancellable token wait")
				}
				if !deadline {
					cancel()
				}
				select {
				case err := <-waiter:
					if !errors.Is(err, want) {
						t.Fatalf("wait error = %v, want %v", err, want)
					}
				case <-time.After(time.Second):
					t.Fatal("cancelled caller remained blocked behind another token fetch")
				}
				if tokens.Load() != 1 || calls.Load() != 0 {
					t.Fatalf("before release: token fetches=%d API calls=%d", tokens.Load(), calls.Load())
				}
				unblock()
				if err := <-leader; err != nil {
					t.Fatal(err)
				}
				if _, err := c.BotInfo(context.Background()); err != nil {
					t.Fatal(err)
				}
				if tokens.Load() != 1 || calls.Load() != 2 {
					t.Fatalf("cache: token fetches=%d API calls=%d", tokens.Load(), calls.Load())
				}
			})
		}
	}
}

// observingContext lets a test know that a surviving caller reached the refresh wait.
type observingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func awaitTokenSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for token synchronization")
	}
}

func TestTokenWaitLeaderRecovery(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "failure"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var fetches, active, peak, calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/" { // a local node's port probe (node/ports.go)
					http.NotFound(w, r)
					return
				}
				if r.URL.Path == tokenPath {
					n := fetches.Add(1)
					current := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
					}
					if n == 1 {
						close(entered)
						<-release
						if mode == "failure" {
							fmt.Fprint(w, `{"code":10001,"msg":"synthetic rejection"}`)
							return
						}
					}
					fmt.Fprint(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
					return
				}
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("unexpected authorization")
				}
				fmt.Fprint(w, `{"code":0,"data":{"bot":{"open_id":"synthetic-bot"}}}`)
			}))
			defer srv.Close()
			defer unblock()
			c := feishubot.New("synthetic-app", "synthetic-secret", srv.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			leader := make(chan error, 1)
			go func() {
				_, _, err := c.Resource(ctx, "synthetic-message", "synthetic-key", "file", 1024)
				leader <- err
			}()
			awaitTokenSignal(t, entered)
			const count = 12
			results := make(chan error, count)
			for range count {
				waiting := &observingContext{Context: context.Background(), waiting: make(chan struct{})}
				go func() {
					info, err := c.BotInfo(waiting)
					if err == nil && info.OpenID != "synthetic-bot" {
						err = errors.New("unexpected bot identity")
					}
					results <- err
				}()
				awaitTokenSignal(t, waiting.waiting)
			}
			if fetches.Load() != 1 {
				t.Fatal("waiters started duplicate token exchanges")
			}
			if mode == "cancel" {
				cancel()
				select {
				case err := <-leader:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("leader error = %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("leader cancellation did not finish")
				}
			}
			unblock()
			if mode != "cancel" {
				err := <-leader
				if mode == "success" && err != nil {
					t.Fatal(err)
				}
				var apiErr *botcard.APIError
				if mode == "failure" && (!errors.As(err, &apiErr) || apiErr.Code != 10001) {
					t.Fatalf("leader error = %v", err)
				}
			}
			for range count {
				select {
				case err := <-results:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("surviving waiter did not recover")
				}
			}
			want := int32(1)
			if mode != "success" {
				want = 2
			}
			if fetches.Load() != want {
				t.Fatalf("token exchanges = %d, want %d", fetches.Load(), want)
			}
			// Cancellation can return before the server observes the disconnected request.
			if mode != "cancel" && peak.Load() != 1 {
				t.Fatalf("concurrent token exchanges = %d", peak.Load())
			}
			before := calls.Load()
			if _, err := c.BotInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			if fetches.Load() != want || calls.Load() != before+1 {
				t.Fatal("successful refresh was not cached")
			}
		})
	}
}

func TestTokenWaitClientIsolation(t *testing.T) {
	var fetches atomic.Int32
	gateway := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == tokenPath {
				fetches.Add(1)
				var in map[string]string
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				token := in["app_id"] + ":" + in["app_secret"] + ":" + r.Host
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": token, "expire": 7200})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bot": map[string]string{"open_id": r.Header.Get("Authorization")}}})
		}))
	}
	a, b := gateway(), gateway()
	defer a.Close()
	defer b.Close()
	for _, tc := range []struct{ id, secret, domain string }{
		{"synthetic-app-a", "synthetic-secret-a", a.URL},
		{"synthetic-app-b", "synthetic-secret-a", a.URL},
		{"synthetic-app-a", "synthetic-secret-b", a.URL},
		{"synthetic-app-a", "synthetic-secret-a", b.URL},
	} {
		c := feishubot.New(tc.id, tc.secret, tc.domain)
		for range 2 {
			info, err := c.BotInfo(context.Background())
			want := "Bearer " + tc.id + ":" + tc.secret + ":" + tc.domain[len("http://"):]
			if err != nil || info.OpenID != want {
				t.Fatal("client did not retain its own credentials and API domain")
			}
		}
	}
	if fetches.Load() != 4 {
		t.Fatalf("token exchanges = %d, want one per client", fetches.Load())
	}
}
