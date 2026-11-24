package mail

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
)

// fakeSOCKS5Proxy is a minimal SOCKS5 server (RFC 1928 CONNECT, user/pass auth of RFC 1929 when wantUser is set)
// that counts tunnels and records the destination the client asked for.
func fakeSOCKS5Proxy(t *testing.T, wantUser, wantPass string) (addr string, tunnels *int32, dest *atomic.Value) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	tunnels, dest = new(int32), new(atomic.Value)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
					return
				}
				io.ReadFull(c, make([]byte, hdr[1]))
				if wantUser != "" {
					c.Write([]byte{5, 2})
					b := make([]byte, 2)
					io.ReadFull(c, b)
					u := make([]byte, b[1])
					io.ReadFull(c, u)
					l := make([]byte, 1)
					io.ReadFull(c, l)
					p := make([]byte, l[0])
					io.ReadFull(c, p)
					if string(u) != wantUser || string(p) != wantPass {
						c.Write([]byte{1, 1})
						return
					}
					c.Write([]byte{1, 0})
				} else {
					c.Write([]byte{5, 0})
				}
				req := make([]byte, 4)
				if _, err := io.ReadFull(c, req); err != nil || req[1] != 1 {
					return
				}
				var host string
				switch req[3] {
				case 1:
					ip := make([]byte, 4)
					io.ReadFull(c, ip)
					host = net.IP(ip).String()
				case 3:
					n := make([]byte, 1)
					io.ReadFull(c, n)
					name := make([]byte, n[0])
					io.ReadFull(c, name)
					host = string(name)
				default:
					return
				}
				pb := make([]byte, 2)
				io.ReadFull(c, pb)
				target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
				dest.Store(target)
				up, err := net.Dial("tcp", target)
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				atomic.AddInt32(tunnels, 1)
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	return ln.Addr().String(), tunnels, dest
}

// A Gmail account's IMAP connection can go through a SOCKS5 proxy, with or without a password (#9389).
func TestGmailThroughSOCKS5Proxy(t *testing.T) {
	for name, tc := range map[string]struct{ user, pass, scheme string }{
		"no auth":   {"", "", "socks5://"},
		"user:pass": {"user", "pass", "socks5://user:pass@"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newTestIMAP(t)
			srv.appendMessage(t, "INBOX", rawMessage("via socks", "a@example.com", "hello"))
			s := newServiceWithAccount(t, srv)
			addr, tunnels, _ := fakeSOCKS5Proxy(t, tc.user, tc.pass)
			if err := s.SetGoogleProxy(tc.scheme + addr); err != nil {
				t.Fatal(err)
			}
			accts, _ := s.ListAccounts()
			id := accts[0].ID
			s.mu.Lock()
			s.accts[id].Provider = ProviderGmail
			s.mu.Unlock()
			s.dropConn(id)
			res, err := s.ListMessages(context.Background(), ListOptions{AccountID: id})
			if err != nil {
				t.Fatalf("list through socks5: %v", err)
			}
			if len(res.Messages) != 1 || res.Messages[0].Subject != "via socks" {
				t.Fatalf("messages = %+v", res.Messages)
			}
			if atomic.LoadInt32(tunnels) == 0 {
				t.Fatal("the proxy carried nothing")
			}
		})
	}
}

// The SOCKS5 proxy gets the server's name (it resolves it, so a hub that cannot resolve Gmail's can still reach it);
// a wrong password and an unreachable destination are proxy errors, which the connection test explains.
func TestDialTCPSOCKS5(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	addr, _, dest := fakeSOCKS5Proxy(t, "user", "pass")

	good, _ := url.Parse("socks5://user:pass@" + addr)
	c, err := dialTCP(context.Background(), good, echo.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()
	if got := dest.Load(); got != echo.Addr().String() {
		t.Fatalf("proxy asked for %v, want %s", got, echo.Addr())
	}

	bad, _ := url.Parse("socks5://user:nope@" + addr)
	_, err = dialTCP(context.Background(), bad, echo.Addr().String())
	if connectFailure("imap.gmail.com", 993, true, err) == nil {
		t.Fatalf("bad proxy password: %v, not explained as a proxy failure", err)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := closed.Addr().String()
	closed.Close()
	du, _ := url.Parse("socks5://" + dead)
	if _, err = dialTCP(context.Background(), du, "imap.gmail.com:993"); err == nil {
		t.Fatal("a dead proxy connected")
	}
}
