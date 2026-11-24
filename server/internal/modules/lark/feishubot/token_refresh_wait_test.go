package feishubot_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/modules/lark/feishubot"
)

// The cache still considers T0 valid when the API has already rejected it.
func TestTokenWaitDuringForcedRefresh(t *testing.T) {
	for _, mode := range []string{"cancel", "success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var fetches, stale atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tokenPath {
					n := fetches.Add(1)
					if n == 2 {
						close(entered)
						<-release
						if mode == "failure" {
							fmt.Fprint(w, `{"code":10001,"msg":"synthetic rejection"}`)
							return
						}
					}
					fmt.Fprintf(w, `{"code":0,"tenant_access_token":"synthetic-%d","expire":7200}`, n)
					return
				}
				if strings.Contains(r.URL.Path, "/resources/") && r.Header.Get("Authorization") == "Bearer synthetic-1" {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"code":99991663,"msg":"synthetic token rejection"}`)
					return
				}
				if r.URL.Path == "/open-apis/bot/v3/info" {
					if fetches.Load() > 1 && r.Header.Get("Authorization") == "Bearer synthetic-1" {
						stale.Add(1)
					}
					fmt.Fprint(w, `{"code":0,"data":{"bot":{"open_id":"synthetic-bot"}}}`)
					return
				}
				fmt.Fprint(w, "synthetic resource")
			}))
			defer srv.Close()
			defer unblock()
			c := feishubot.New("synthetic-app", "synthetic-secret", srv.URL)
			if _, err := c.BotInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			leader := make(chan error, 1)
			go func() {
				_, _, err := c.Resource(context.Background(), "synthetic-message", "synthetic-key", "file", 1024)
				leader <- err
			}()
			awaitTokenSignal(t, entered)
			if mode == "cancel" {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				_, err := c.BotInfo(ctx)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("waiting call error = %v, want deadline exceeded", err)
				}
				if stale.Load() != 0 {
					t.Fatal("waiting call used the rejected cached token")
				}
			} else {
				const count = 8
				result := make(chan error, count)
				for range count {
					ctx := &observingContext{Context: context.Background(), waiting: make(chan struct{})}
					go func() { _, err := c.BotInfo(ctx); result <- err }()
					awaitTokenSignal(t, ctx.waiting)
				}
				if stale.Load() != 0 {
					t.Error("concurrent callers bypassed the forced refresh")
				}
				unblock()
				for range count {
					if err := <-result; err != nil {
						t.Fatal(err)
					}
				}
			}
			unblock()
			err := <-leader
			if mode == "failure" {
				var e *botcard.APIError
				if !errors.As(err, &e) || e.Code != 10001 {
					t.Fatalf("leader error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := int32(2)
			if mode == "failure" {
				want = 3
			}
			if fetches.Load() != want || stale.Load() != 0 {
				t.Fatalf("fetches=%d, stale API calls=%d", fetches.Load(), stale.Load())
			}
		})
	}
}

// Both first API answers are transient. A's forced refresh is already in flight
// before B enters its final attempt, which must wait rather than spend it on T0.
func TestTokenWaitFinalAttempt(t *testing.T) {
	for _, mode := range []string{"resource", "keyed send"} {
		t.Run(mode, func(t *testing.T) {
			entered, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var fetches, hits, stale atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tokenPath {
					n := fetches.Add(1)
					if n == 2 {
						close(entered)
						<-release
					}
					fmt.Fprintf(w, `{"code":0,"tenant_access_token":"synthetic-%d","expire":7200}`, n)
					return
				}
				if r.URL.Path == "/open-apis/bot/v3/info" {
					fmt.Fprint(w, `{"code":0}`)
					return
				}
				if strings.Contains(r.URL.Path, "/messages/leader/resources/") {
					if r.Header.Get("Authorization") == "Bearer synthetic-1" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"code":99991663}`)
					} else {
						fmt.Fprint(w, "resource")
					}
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages") {
					// Not this client's: a loopback port is open to anything on the machine (probes send GET /).
					http.NotFound(w, r)
					return
				}
				n := hits.Add(1)
				if mode == "keyed send" {
					// Every retry must retain the public send's idempotency UUID.
					var body struct {
						UUID string `json:"uuid"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UUID != "stable-synthetic-uuid" {
						t.Error("send lost its UUID")
					}

				}
				if n <= 2 {
					if n == 2 {
						close(second)
						<-entered
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, `{"code":0}`)
					return
				}
				if r.Header.Get("Authorization") == "Bearer synthetic-1" {
					stale.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"code":99991663}`)
					return
				}
				if mode == "resource" {
					fmt.Fprint(w, "resource")
				} else {
					fmt.Fprint(w, `{"code":0,"data":{"message_id":"synthetic-message"}}`)
				}
			}))
			defer srv.Close()
			defer unblock()
			c := feishubot.New("synthetic-app", "synthetic-secret", srv.URL)
			if _, err := c.BotInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			waiter := make(chan error, 1)
			go func() {
				var err error
				if mode == "resource" {
					_, _, err = c.Resource(context.Background(), "synthetic-message", "synthetic-key", "file", 1024)
				} else {
					_, err = c.Send(context.Background(), "chat_id", "synthetic-chat", feishubot.TypeText, `{"text":"test"}`, "stable-synthetic-uuid")
				}
				waiter <- err
			}()
			awaitTokenSignal(t, second)
			leader := make(chan error, 1)
			go func() {
				_, _, err := c.Resource(context.Background(), "leader", "synthetic-key", "file", 1024)
				leader <- err
			}()
			awaitTokenSignal(t, entered)
			// A real retry's bounded backoff separates B's second and third attempts.
			select {
			case err := <-waiter:
				t.Fatalf("final attempt escaped the gated refresh: %v", err)
			case <-time.After(time.Second):
			}
			unblock()
			if err := <-leader; err != nil {
				t.Fatal(err)
			}
			if err := <-waiter; err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 3 || fetches.Load() != 2 || stale.Load() != 0 {
				t.Fatalf("API calls=%d token exchanges=%d stale calls=%d", hits.Load(), fetches.Load(), stale.Load())
			}
		})
	}
}

// Pause the caller after its waited exchange completes, before it can consult
// the token result. This makes a later refresh win the scheduling race reliably.
type delayedWakeContext struct {
	context.Context
	waiting, woke, resume chan struct{}
	once                  sync.Once
	paused                atomic.Bool
}

func (c *delayedWakeContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}
func (c *delayedWakeContext) Err() error {
	select {
	case <-c.waiting:
		if c.paused.CompareAndSwap(false, true) {
			close(c.woke)
			<-c.resume
		}
	default:
	}
	return c.Context.Err()
}

func TestTokenWaitLatestAfterWake(t *testing.T) {
	for _, scenario := range []string{"success/in flight", "success/completed", "failure/in flight", "failure/completed"} {
		t.Run(scenario, func(t *testing.T) {
			initialFailure := strings.HasPrefix(scenario, "failure/")
			completed := strings.HasSuffix(scenario, "completed")
			first, second := make(chan struct{}), make(chan struct{})
			releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
			var firstOnce, secondOnce, resumeOnce sync.Once
			unblockFirst := func() { firstOnce.Do(func() { close(releaseFirst) }) }
			unblockSecond := func() { secondOnce.Do(func() { close(releaseSecond) }) }
			ctx := &delayedWakeContext{Context: context.Background(), waiting: make(chan struct{}), woke: make(chan struct{}), resume: make(chan struct{})}
			resume := func() { resumeOnce.Do(func() { close(ctx.resume) }) }
			var fetches atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tokenPath {
					n := fetches.Add(1)
					switch n {
					case 1:
						close(first)
						<-releaseFirst
						if initialFailure {
							fmt.Fprint(w, `{"code":10001}`)
							return
						}
					case 2:
						close(second)
						<-releaseSecond
					default:
						t.Error("waiter redundantly forced another exchange")
					}
					fmt.Fprintf(w, `{"code":0,"tenant_access_token":"synthetic-%d","expire":7200}`, n)
					return
				}
				if strings.Contains(r.URL.Path, "/resources/") {
					if r.Header.Get("Authorization") == "Bearer synthetic-1" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"code":99991663}`)
					} else {
						fmt.Fprint(w, "resource")
					}
					return
				}
				identity := "latest"
				if fetches.Load() >= 2 && r.Header.Get("Authorization") == "Bearer synthetic-1" {
					identity = "superseded"
				}
				fmt.Fprintf(w, `{"code":0,"data":{"bot":{"open_id":%q}}}`, identity)
			}))
			defer srv.Close()
			defer unblockFirst()
			defer unblockSecond()
			defer resume()
			c := feishubot.New("synthetic-app", "synthetic-secret", srv.URL)
			leader := make(chan error, 1)
			go func() {
				var err error
				if initialFailure {
					_, _, err = c.Resource(context.Background(), "leader", "synthetic-key", "file", 1024)
				} else {
					_, err = c.BotInfo(context.Background())
				}
				leader <- err
			}()
			awaitTokenSignal(t, first)
			type answer struct {
				info feishubot.BotInfo
				err  error
			}
			waiter := make(chan answer, 1)
			go func() { info, err := c.BotInfo(ctx); waiter <- answer{info, err} }()
			awaitTokenSignal(t, ctx.waiting)
			unblockFirst()
			awaitTokenSignal(t, ctx.woke)
			if err := <-leader; initialFailure {
				var e *botcard.APIError
				if !errors.As(err, &e) || e.Code != 10001 {
					t.Fatalf("leader error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			refresher := make(chan error, 1)
			go func() {
				var err error
				if initialFailure {
					_, err = c.BotInfo(context.Background())
				} else {
					_, _, err = c.Resource(context.Background(), "synthetic-message", "synthetic-key", "file", 1024)
				}
				refresher <- err
			}()
			awaitTokenSignal(t, second)
			if completed {
				unblockSecond()
				if err := <-refresher; err != nil {
					t.Fatal(err)
				}
			}
			resume()
			if !completed {
				select {
				case result := <-waiter:
					t.Fatalf("waiter bypassed the latest in-flight exchange: identity=%s error=%v", result.info.OpenID, result.err)
				case <-time.After(50 * time.Millisecond):
				}
				unblockSecond()
				if err := <-refresher; err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-waiter:
				if result.err != nil || result.info.OpenID != "latest" {
					t.Fatalf("waiter reused superseded exchange: identity=%s error=%v", result.info.OpenID, result.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("waiter did not finish")
			}
			if fetches.Load() != 2 {
				t.Fatalf("token exchanges=%d, want 2", fetches.Load())
			}
		})
	}
}
