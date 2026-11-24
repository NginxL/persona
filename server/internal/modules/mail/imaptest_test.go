package mail

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// testIMAP is an in-memory IMAP server plus the host/port to reach it.
type testIMAP struct {
	server *imapmemserver.Server
	user   *imapmemserver.User
	host   string
	port   int
	ln     net.Listener
	// blindSearch makes sessions opened from now on answer every keyword SEARCH with nothing, refuseSearch with NO
	// (see keywordSearchSession); searchDelay (nanoseconds) makes them take that long to answer it.
	blindSearch, refuseSearch atomic.Bool
	searchDelay               atomic.Int64
}

// keywordSearchSession answers every SEARCH that looks for words (TEXT, BODY, a header) with no results, the way
// NetEase 163's IMAP server does, or refuses it with NO; searches without words (ALL, UID ranges, dates, flags) work.
type keywordSearchSession struct {
	imapserver.SessionIMAP4rev2 // the memory server's session, so the server keeps advertising IMAP4rev2
	refuse                      bool
	delay                       time.Duration
}

func (s keywordSearchSession) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	if !hasKeyword(criteria) {
		return s.SessionIMAP4rev2.Search(kind, criteria, options)
	}
	time.Sleep(s.delay)
	switch {
	case s.refuse:
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "keyword search is not supported"}
	case kind == imapserver.NumKindUID:
		return &imap.SearchData{All: imap.UIDSet{}}, nil
	default:
		return &imap.SearchData{All: imap.SeqSet{}}, nil
	}
}

// hasKeyword reports whether criteria looks for words anywhere, including under NOT and OR.
func hasKeyword(criteria *imap.SearchCriteria) bool {
	if len(criteria.Text) > 0 || len(criteria.Body) > 0 || len(criteria.Header) > 0 {
		return true
	}
	for i := range criteria.Not {
		if hasKeyword(&criteria.Not[i]) {
			return true
		}
	}
	for i := range criteria.Or {
		if hasKeyword(&criteria.Or[i][0]) || hasKeyword(&criteria.Or[i][1]) {
			return true
		}
	}
	return false
}

// newTestIMAP starts an in-memory IMAP server with one user and a few mailboxes.
func newTestIMAP(t *testing.T) *testIMAP {
	t.Helper()
	return newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}})
}

// newTestIMAPCaps is newTestIMAP advertising only caps.
func newTestIMAPCaps(t *testing.T, caps imap.CapSet) *testIMAP {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("me@example.com", "secret")
	for _, name := range []string{"INBOX", "Sent", "Drafts", "Trash", "Archive", "Junk"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatalf("create mailbox %s: %v", name, err)
		}
	}
	mem.AddUser(user)

	ts := &testIMAP{server: mem, user: user, host: "127.0.0.1"}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			if blind, refuse := ts.blindSearch.Load(), ts.refuseSearch.Load(); blind || refuse {
				return keywordSearchSession{SessionIMAP4rev2: mem.NewSession().(imapserver.SessionIMAP4rev2), refuse: refuse,
					delay: time.Duration(ts.searchDelay.Load())}, nil, nil
			}
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         caps,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })

	ts.port, ts.ln = ln.Addr().(*net.TCPAddr).Port, ln
	return ts
}

// ignoreListReturn puts a proxy in front of the server that drops every LIST
// command carrying RETURN options without answering it, the way QQ Mail's IMAP
// server (no LIST-EXTENDED, LIST-STATUS or SPECIAL-USE) leaves it hanging.
// Everything else passes through, so s.port then points at the proxy.
func (s *testIMAP) ignoreListReturn(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	target := fmt.Sprintf("%s:%d", s.host, s.port)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go proxyDroppingListReturn(c, target)
		}
	}()
	s.port = ln.Addr().(*net.TCPAddr).Port
}

// proxyDroppingListReturn relays one client connection to target line by line,
// leaving out LIST commands with RETURN options.
func proxyDroppingListReturn(client net.Conn, target string) {
	defer client.Close()
	server, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer server.Close()
	go func() { _, _ = io.Copy(client, server) }()
	r := bufio.NewReader(client)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && !isListReturn(line) {
			if _, werr := server.Write(line); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// isListReturn reports whether line is a tagged LIST command with RETURN options.
func isListReturn(line []byte) bool {
	upper := strings.ToUpper(string(line))
	fields := strings.Fields(upper)
	return len(fields) > 1 && fields[1] == "LIST" && strings.Contains(upper, " RETURN (")
}

// appendMessage stores raw in mailbox via a throwaway client.
func (s *testIMAP) appendMessage(t *testing.T, mailbox string, raw string, flags ...imap.Flag) imap.UID {
	t.Helper()
	c, err := imapclient.DialInsecure(fmt.Sprintf("%s:%d", s.host, s.port), &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: s.host},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Login("me@example.com", "secret").Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	cmd := c.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: flags})
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatalf("append write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}
	data, err := cmd.Wait()
	if err != nil {
		t.Fatalf("append wait: %v", err)
	}
	return data.UID
}

// testAccount returns a MailAccount pointed at the test server.
func (s *testIMAP) testAccount() *MailAccount {
	return &MailAccount{
		ID:           "acc_test",
		Name:         "Test",
		Email:        "me@example.com",
		Provider:     ProviderIMAP,
		Auth:         AuthPassword,
		Username:     "me@example.com",
		Password:     "secret",
		IMAPHost:     s.host,
		IMAPPort:     s.port,
		IMAPSecurity: "none",
		SMTPHost:     s.host,
		SMTPPort:     2525,
		SMTPSecurity: "none",
	}
}

// rawMessage builds a simple raw message.
func rawMessage(subject, from, body string) string {
	return "From: " + from + "\r\n" +
		"To: me@example.com\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 -0700\r\n" +
		"Message-Id: <" + subject + "@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		body
}
