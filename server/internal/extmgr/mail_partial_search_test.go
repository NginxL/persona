package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

type subscriptionMailClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *subscriptionMailClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *subscriptionMailClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.advance(d)
	return nil
}
func (c *subscriptionMailClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type subscriptionMailConnectors struct{ conn connector.Connector }

func (c subscriptionMailConnectors) Find(_ context.Context, id string) (subscriptions.ConnectorInfo, bool) {
	return subscriptions.ConnectorInfo{ID: "mail", Conn: c.conn, Status: connector.StateOK, Enabled: true}, id == "mail"
}
func (c subscriptionMailConnectors) All(ctx context.Context) []subscriptions.ConnectorInfo {
	info, _ := c.Find(ctx, "mail")
	return []subscriptions.ConnectorInfo{info}
}

// Each fixture is a real in-memory IMAP server reached through an independent loopback listener.
func subscriptionIMAP(t *testing.T, subject string) int {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("me@example.com", "secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return mem.NewSession(), nil, nil
	}, InsecureAuth: true, Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	client, err := imapclient.DialInsecure(fmt.Sprintf("127.0.0.1:%d", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Login("me@example.com", "secret").Wait(); err != nil {
		t.Fatal(err)
	}
	raw := "From: a@example.com\r\nTo: me@example.com\r\nSubject: " + subject + "\r\nDate: Mon, 02 Jan 2006 15:04:05 -0700\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSynthetic message"
	cmd := client.Append("INBOX", int64(len(raw)), nil)
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	return port
}

func importSubscriptionAccount(t *testing.T, s Mail, id string, port int, password string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"id": id, "name": id, "email": id + "@example.com", "provider": "imap",
		"auth": "password", "username": "me@example.com", "password": password, "imap_host": "127.0.0.1",
		"imap_port": port, "imap_security": "none", "smtp_host": "127.0.0.1", "smtp_port": 2525, "smtp_security": "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportAccount(data); err != nil {
		t.Fatal(err)
	}
}

func TestMailSubscriptionRejectsIncompleteSearch(t *testing.T) {
	portA := subscriptionIMAP(t, "Quarterly A")
	portB := subscriptionIMAP(t, "Quarterly B")
	h, s := managerWithMail(t)
	importSubscriptionAccount(t, s, "account_a", portA, "secret")
	importSubscriptionAccount(t, s, "account_b", portB, "synthetic-wrong")
	c := mailConnector{h: h}
	store := subscriptions.NewMemStore()
	clock := &subscriptionMailClock{now: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	subs := subscriptions.New(subscriptions.Options{Store: store, Clock: clock, Connectors: subscriptionMailConnectors{conn: c}})
	recipe := subscriptions.Recipe{Mode: "items", Tool: "search", Args: json.RawMessage(`{"query":"quarterly"}`), ItemsPath: "$.messages",
		Fields: &connector.SubscriptionFields{ID: "$.id", Title: "$.title", Sender: "$.sender", Time: "$.time", Body: "$.body"}, Kind: "sub_mail", GroupBy: "source"}
	run := func() subscriptions.DryRun {
		t.Helper()
		dr, err := subs.DryRun(context.Background(), subscriptions.DryRunRequest{Connector: "mail", Source: "mail", Recipe: &recipe})
		if err != nil {
			t.Fatal(err)
		}
		return dr
	}
	if dr := run(); dr.OK || dr.Error == nil || dr.Error.Code != subscriptions.CodeCallFailed || dr.Count != 0 || len(dr.Items) != 0 {
		t.Errorf("mixed search became subscription data: %+v", dr)
	}
	recipe.Args = json.RawMessage(`{"query":"no-match-anywhere"}`)
	if dr := run(); dr.OK || dr.Error == nil || dr.Error.Code != subscriptions.CodeCallFailed {
		t.Errorf("empty healthy account hid subscription failure: %+v", dr)
	}
	recipe.Args = json.RawMessage(`{"account_id":"account_a","query":"quarterly"}`)
	if dr := run(); !dr.OK || dr.Error != nil || dr.Count != 1 {
		t.Errorf("explicit healthy account = %+v", dr)
	}
	recipe.Args = json.RawMessage(`{"query":"quarterly"}`)
	if err := s.DeleteAccount("account_b"); err != nil {
		t.Fatal(err)
	}
	importSubscriptionAccount(t, s, "account_b", portB, "secret")
	if dr := run(); !dr.OK || dr.Error != nil || dr.Count != 2 {
		t.Fatalf("repaired search = %+v", dr)
	}
	// A scheduled subscription must neither advance its cursor nor keep a partial item after a failed account.
	ctx := context.Background()
	sub, err := subs.Create(ctx, subscriptions.CreateRequest{Connector: "mail", Source: "mail", Recipe: &recipe}, subscriptions.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount("account_b"); err != nil {
		t.Fatal(err)
	}
	importSubscriptionAccount(t, s, "account_b", portB, "synthetic-wrong")
	on := true
	if _, err := subs.Update(ctx, sub.ID, subscriptions.Patch{Revision: sub.Revision, ToInbox: &on}, subscriptions.Actor{}); err != nil {
		t.Fatal(err)
	}
	subs.RunDue(ctx)
	failed, runs, err := subs.Get(ctx, sub.ID)
	if err != nil || failed.RunState != subscriptions.StateFailing || failed.Error == nil || failed.Error.Code != subscriptions.CodeCallFailed || failed.SinceAt != nil || failed.NextRunAt == nil || len(store.Items()) != 0 || store.SeenCount(sub.ID) != 0 || len(runs) != 1 || runs[0].Error == nil {
		t.Fatalf("incomplete scheduled run = %+v, runs %+v, err %v", failed, runs, err)
	}
	if err := s.DeleteAccount("account_b"); err != nil {
		t.Fatal(err)
	}
	importSubscriptionAccount(t, s, "account_b", portB, "secret")
	clock.advance(failed.NextRunAt.Sub(clock.Now()))
	subs.RunDue(ctx)
	recovered, runs, err := subs.Get(ctx, sub.ID)
	if err != nil || recovered.RunState != subscriptions.StateOK || recovered.Error != nil || recovered.SinceAt == nil || len(store.Items()) != 2 || store.SeenCount(sub.ID) != 2 || len(runs) != 2 || runs[0].Error != nil {
		t.Fatalf("scheduled retry = %+v, runs %+v, items %+v, err %v", recovered, runs, store.Items(), err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if dr, err := subs.DryRun(canceled, subscriptions.DryRunRequest{Connector: "mail", Source: "mail", Recipe: &recipe}); err != nil || dr.OK || dr.Error == nil || len(dr.Items) != 0 {
		t.Errorf("canceled subscription = %+v, %v", dr, err)
	}
	recipe.Args = json.RawMessage(`{"query":"no-match-anywhere"}`)
	if dr := run(); !dr.OK || dr.Error != nil || dr.Count != 0 {
		t.Errorf("healthy empty search = %+v", dr)
	}
	if err := s.DeleteAccount("account_a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount("account_b"); err != nil {
		t.Fatal(err)
	}
	importSubscriptionAccount(t, s, "account_a", portA, "synthetic-wrong")
	importSubscriptionAccount(t, s, "account_b", portB, "synthetic-wrong")
	if dr := run(); dr.OK || dr.Error == nil || dr.Error.Code != subscriptions.CodeCallFailed || len(dr.Items) != 0 {
		t.Errorf("all-failed search became subscription data: %+v", dr)
	}
}
