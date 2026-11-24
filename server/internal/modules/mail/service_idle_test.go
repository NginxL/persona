package mail

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

// fakeClock is a settable s.now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countLogins makes s dial addr itself and counts the logins.
func countLogins(s *Service, addr string) *atomic.Int32 {
	var n atomic.Int32
	s.imapDial = func(*MailAccount) (*imapclient.Client, error) {
		c, err := imapclient.DialInsecure(addr, nil)
		if err != nil {
			return nil, err
		}
		if err := c.Login("me@example.com", "secret").Wait(); err != nil {
			c.Close()
			return nil, err
		}
		n.Add(1)
		return c, nil
	}
	return &n
}

// Two inbox mail rounds inboxMailPoll (3 minutes) apart, the idle sweeper running in between, log in once: the
// second round reuses the first one's connection after it answered a NOOP.
func TestCachedLoginOutlivesMailRounds(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	clock := &fakeClock{t: time.Now()}
	s.now = clock.now
	logins := countLogins(s, fmt.Sprintf("%s:%d", srv.host, srv.port))
	srv.appendMessage(t, "INBOX", rawMessage("Hello", "a@example.com", "x"))
	ctx := context.Background()

	for round := 0; round < 2; round++ {
		if round > 0 {
			clock.advance(3 * time.Minute)
			s.sweepOnce()
		}
		res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if len(res.Messages) != 1 {
			t.Fatalf("round %d: %d messages, want 1", round, len(res.Messages))
		}
	}
	t.Logf("logins across two rounds 3 minutes apart: %d", logins.Load())
	if n := logins.Load(); n != 1 {
		t.Fatalf("%d logins across two rounds, want 1", n)
	}

	// Past idleTimeout the sweeper still closes it, and the next round logs in again.
	clock.advance(idleTimeout + time.Minute)
	s.sweepOnce()
	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("after idle timeout: %v", err)
	}
	if n := logins.Load(); n != 2 {
		t.Fatalf("%d logins after the idle timeout, want 2", n)
	}
}

// blackhole is a TCP proxy in front of an IMAP server that can stop delivering the bytes of the connections it
// already has, without closing them, the way a NAT that forgot an idle flow does. New connections go through.
type blackhole struct {
	ln    net.Listener
	mu    sync.Mutex
	gates []*atomic.Bool
}

func newBlackhole(t *testing.T, target string) *blackhole {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &blackhole{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				client.Close()
				continue
			}
			dropped := &atomic.Bool{}
			b.mu.Lock()
			b.gates = append(b.gates, dropped)
			b.mu.Unlock()
			t.Cleanup(func() { client.Close(); server.Close() })
			go pipeUnless(server, client, dropped)
			go pipeUnless(client, server, dropped)
		}
	}()
	return b
}

// pipeUnless copies src to dst, discarding what comes once dropped is set.
func pipeUnless(dst io.Writer, src io.Reader, dropped *atomic.Bool) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 && !dropped.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// drop silently stops every connection open so far.
func (b *blackhole) drop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range b.gates {
		g.Store(true)
	}
}

// A cached connection the network silently dropped while idle is found by the NOOP within the probe's wait and
// replaced, instead of the round hanging for networkTimeout.
func TestSilentlyDroppedConnIsRedialed(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	clock := &fakeClock{t: time.Now()}
	s.now = clock.now
	s.probe = connProbe{after: time.Minute, wait: 200 * time.Millisecond}
	proxy := newBlackhole(t, fmt.Sprintf("%s:%d", srv.host, srv.port))
	logins := countLogins(s, proxy.ln.Addr().String())
	srv.appendMessage(t, "INBOX", rawMessage("Hello", "a@example.com", "x"))
	ctx := context.Background()

	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("first round: %v", err)
	}
	proxy.drop()
	clock.advance(3 * time.Minute)

	start := time.Now()
	res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"})
	took := time.Since(start)
	t.Logf("round on a silently dropped connection: %v, %d logins", took.Round(time.Millisecond), logins.Load())
	if err != nil {
		t.Fatalf("round after the drop: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("%d messages, want 1", len(res.Messages))
	}
	if n := logins.Load(); n != 2 {
		t.Fatalf("%d logins, want 2 (the dropped connection replaced)", n)
	}
	if took > 5*time.Second {
		t.Fatalf("the round took %v: the dead connection was used instead of probed", took)
	}
}
