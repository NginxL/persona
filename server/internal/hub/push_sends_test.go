package hub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
)

// A push posts to APNs from its own goroutine and logs when APNs answers (push_send.go); Pusher.sends counts the post
// until it has logged, so whoever closes what it posts to waits for it first, as gradingHub's cleanup does before closing
// its fake APNs. Without that, a post still on its way logged to a test that had completed: a panic under -race
// (review of #8287, TestQuietCardPushHonoursTheManagersLevel).
func TestPusherSendsCountsAPostUntilItHasLogged(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	answer := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(answer) // runs first: a failed check must not leave the server waiting on its handler
	var mu sync.Mutex
	var logged []string
	p := newPusher(t.TempDir(), nil, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	p.store = newGradingStore()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	p.direct.Base = srv.URL
	p.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "tok-phone", Topic: "app.example"})

	p.send(map[string]any{"aps": map[string]any{"alert": "hi"}}, "", nil)
	drained := make(chan struct{})
	go func() {
		p.sends.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("sends drained while APNs had not answered the post")
	case <-time.After(100 * time.Millisecond):
	}
	answer()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("sends did not drain after APNs answered")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "push accepted") }) {
		t.Fatalf("drained before the post logged APNs' answer: %q", logged)
	}
}
