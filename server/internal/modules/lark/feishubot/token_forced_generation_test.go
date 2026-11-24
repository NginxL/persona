package feishubot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Public-call regressions cover the HTTP paths. Exercise the forced=true join
// directly as well, so a successful shared exchange cannot trigger another fetch.
type forcedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *forcedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}
func TestTenantTokenForcedWaitersShareGeneration(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("leader failure=%t", fail), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var fetches atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					n := fetches.Add(1)
					if n == 2 {
						close(entered)
						<-release
						if fail {
							fmt.Fprint(w, `{"code":10001}`)
							return
						}
					}
					fmt.Fprintf(w, `{"code":0,"tenant_access_token":"synthetic-%d","expire":7200}`, n)
					return
				}
				if r.URL.Path == "/open-apis/bot/v3/info" {
					fmt.Fprint(w, `{"code":0}`)
					return
				}
				if r.Header.Get("Authorization") == "Bearer synthetic-1" {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"code":99991663}`)
					return
				}
				fmt.Fprint(w, "resource")
			}))
			defer srv.Close()
			defer unblock()
			c := New("synthetic-app", "synthetic-secret", srv.URL)
			if _, err := c.BotInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			leader := make(chan error, 1)
			go func() {
				_, _, err := c.Resource(context.Background(), "synthetic-message", "synthetic-key", "file", 1024)
				leader <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("forced refresh did not start")
			}
			const count = 8
			results := make(chan error, count)
			for range count {
				ctx := &forcedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
				go func() {
					token, err := c.tenantToken(ctx, true)
					want := "synthetic-2"
					if fail {
						want = "synthetic-3"
					}
					if err == nil && token != want {
						err = errors.New("forced waiter used the wrong generation")
					}
					results <- err
				}()
				select {
				case <-ctx.waiting:
				case <-time.After(3 * time.Second):
					t.Fatal("forced caller did not join")
				}
			}
			unblock()
			err := <-leader
			if fail {
				var e *APIError
				if !errors.As(err, &e) || e.Code != 10001 {
					t.Fatalf("leader error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			for range count {
				select {
				case err := <-results:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("forced waiter did not finish")
				}
			}
			want := int32(2)
			if fail {
				want = 3
			}
			if fetches.Load() != want {
				t.Fatalf("token exchanges=%d, want %d", fetches.Load(), want)
			}
		})
	}
}
