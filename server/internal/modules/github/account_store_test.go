package github

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// The account kept across restarts (#7832, account.go restore / keepAccount / keepSettled): a store that fails,
// is rewritten by whoever held the id, or no longer opens; what a restart finds; and what the steady state costs.

// r2Store is the manager's store for the connector's sign-in, which can be made to fail and counts its calls.
type r2Store struct {
	mu                 sync.Mutex
	data               []byte
	failLoad, failSave bool
	loads, saves       int
}

var errR2Store = errors.New("github: the stored secret does not open (secret.key replaced?)")

func (s *r2Store) Load(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.failLoad {
		return nil, errR2Store
	}
	return s.data, nil
}

func (s *r2Store) Save(_ context.Context, d []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.failSave {
		return errR2Store
	}
	s.data = d
	return nil
}

func (s *r2Store) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = nil
	return nil
}

func (s *r2Store) fail(load, save bool) {
	s.mu.Lock()
	s.failLoad, s.failSave = load, save
	s.mu.Unlock()
}

func (s *r2Store) put(blob string) {
	s.mu.Lock()
	s.data = []byte(blob)
	s.mu.Unlock()
}

func (s *r2Store) kept() deviceLogin {
	s.mu.Lock()
	defer s.mu.Unlock()
	var d deviceLogin
	_ = json.Unmarshal(s.data, &d)
	return d
}

func (s *r2Store) counts() (loads, saves int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads, s.saves
}

// r2Connector is a manager started on store, reading a computer's gh login (token); it is not checked to be
// signed in.
func r2Connector(f *twoAccounts, store connectorwire.Secrets, token string) *Connector {
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{token}}).fetch)
	c.api = f.srv.URL
	c.UseSecrets(store)
	c.check(context.Background())
	return c
}

func r2SignedIn(t *testing.T, c *Connector, login string) {
	t.Helper()
	if st := c.Status(context.Background()); st.State != connectorwire.StateOK || st.Account != login {
		t.Fatalf("not signed in as %s: %+v", login, st)
	}
}

const r2Thread7 = "example-org/example-repo#7"

// r2Assign puts issue 7 in login's unread notifications, assigned to it.
func r2Assign(f *twoAccounts, login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feeds[login] = "[" + note("7", "assign", "Issue", "https://api.github.com"+api7, t1) + "]"
	f.visible[login][api7] = `{"title":"Example bug","state":"open","user":{"login":"carol"},"body":"出错了","html_url":"https://github.com/example-org/example-repo/issues/7","comments":0}`
}

// The store is rewritten under the connector: a plugin held the id "github" (it is handed the same store) and
// saved its own sign-in, without the account. The hub gives the connector back (Reload: "to read it again"). The
// account is kept again, or the next start would take the next login for the first.
func TestReloadReadsAndKeepsTheAccountAgain(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	s := &r2Store{}
	c1 := r2Connector(f, s, tokenA)
	r2SignedIn(t, c1, loginA)
	pollOnce(t, c1)
	a := collected(t, c1)
	if r := s.kept().Account; r == nil || r.Login != loginA {
		t.Fatalf("the account is not kept: %+v", r)
	}

	s.put(`{"token":"gho_examplepluginsignin0000000000000000001"}`) // the plugin's sign-in under the same id
	c1.Reload()
	c1.check(context.Background())
	pollOnce(t, c1)
	if r := s.kept().Account; r == nil || r.Login != loginA {
		t.Errorf("after Reload the store keeps no account (%+v) and the connector does not save it again", r)
	}

	c2 := r2Connector(f, s, tokenB) // the manager starts again; the computer's gh login is B's by now
	r2SignedIn(t, c2, loginB)
	pollOnce(t, c2)
	retired(t, c2, a)
	refused(t, f, c2, a)
}

// A stored secret that no longer opens (the store's own words: "secret.key replaced?"). A computer's gh login
// does not need the store and goes on working, and a device sign-in writes a new secret over the old one: the way
// out it always was.
func TestUnreadableStoreCanBeLeft(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	s := &r2Store{data: []byte("sealed with another key")}
	s.fail(true, false)
	ctx := context.Background()

	c := r2Connector(f, s, tokenA)
	if st := c.Status(ctx); st.State != connectorwire.StateOK {
		t.Errorf("a computer's gh login, which keeps nothing in the store: %s %q", st.State, st.Error)
	}

	// The user signs in again with the device flow, which is how every other unreadable sign-in is replaced.
	d := &deviceWeb{}
	c.web = d.server(t)
	c.UseDevice()
	d.give(tokenA)
	if _, err := c.LoginStep(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	p, err := c.LoginStep(ctx, "device", nil)
	_, saves := s.counts()
	if err != nil || !p.Done {
		t.Errorf("device sign-in over an unreadable secret: %+v, %v (saves tried: %d)", p, err, saves)
	}
	if err := c.Logout(ctx); err != nil {
		t.Errorf("sign-out: %v", err)
	}
	if _, saves := s.counts(); saves == 0 {
		t.Errorf("neither sign-in nor sign-out wrote the store: it stays unreadable for good")
	}
}

// A save that fails when the hub asks which items are over. That A's items are closed is saved before the hub
// is told, so with a store that cannot be written nothing is closed yet (A's item stays open, unanswerable), and
// once it can, they are. A restart then finds it done: B has read its notification on GitHub meanwhile, and its
// own open item, which the new process holds no thing of, is not closed as "the account changed".
func TestFailedSaveDoesNotCloseTheAccountInUsesItems(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	r2Assign(f, loginB)
	s := &r2Store{}
	c1 := r2Connector(f, s, tokenA)
	pollOnce(t, c1)
	a := collected(t, c1)
	switchTo(c1, tokenB)
	pollOnce(t, c1)
	if _, ok := things(t, c1)[r2Thread7]; !ok {
		t.Fatalf("B's thing: %+v", things(t, c1))
	}
	ctx := context.Background()

	s.fail(false, true)
	if over := c1.Settle(ctx, []string{a.Thread, r2Thread7}); len(over) != 0 {
		t.Errorf("closed without it being saved: %v (a restart would close by exclusion again)", over)
	}
	refused(t, f, c1, a)
	s.fail(false, false)
	if over := c1.Settle(ctx, []string{a.Thread, r2Thread7}); over[a.Thread] == "" || over[r2Thread7] != "" {
		t.Fatalf("the hub's next round: over %v", over)
	}

	f.mu.Lock()
	f.feeds[loginB] = "" // read on GitHub: no longer in the unread list
	f.mu.Unlock()
	c2 := r2Connector(f, s, tokenB)
	r2SignedIn(t, c2, loginB)
	pollOnce(t, c2)
	if over := c2.Settle(ctx, []string{r2Thread7}); len(over) != 0 {
		t.Errorf("after the restart %s's own open item is closed: %v", loginB, over)
	}
}

// Same account, steady state: one /user at the start, one save the first time ever, none after; a restart
// loads once and saves nothing.
func TestSteadyStateCost(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.visible[loginA][api7] = `{"title":"as A reads it"}`
	s := &r2Store{}
	api, count := counting(t, f)
	ctx := context.Background()
	start := func() *Connector {
		c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{tokenA}}).fetch)
		c.api = api
		c.UseSecrets(s)
		c.check(ctx)
		return c
	}
	use := func(c *Connector) {
		for range 3 {
			pollOnce(t, c)
		}
		a := collected(t, c)
		var it struct {
			Title string `json:"title"`
		}
		if err := c.cachedGet(ctx, api7, &it); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Thread(ctx, r2Thread7, "", 10); err != nil {
			t.Logf("thread page: %v", err)
		}
		if err := c.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: a.Last, Text: "看过了"}); err != nil {
			t.Fatal(err)
		}
		_ = c.Settle(ctx, []string{a.Thread})
		_, _ = c.Collect(ctx)
	}
	c1 := start()
	use(c1)
	loads, saves := s.counts()
	if n := count("/user"); n != 1 || loads != 1 || saves != 1 {
		t.Errorf("first start: /user %d, loads %d, saves %d; want 1, 1, 1", n, loads, saves)
	}
	c1.check(ctx) // Run's check, every half hour
	if n, _ := count("/user"), 0; n != 2 {
		t.Errorf("/user %d after a second check", n)
	}
	c2 := start()
	use(c2)
	loads, saves = s.counts()
	if n := count("/user"); n != 3 || loads != 2 || saves != 1 {
		t.Errorf("after a restart: /user %d, loads %d, saves %d; want 3, 2, 1", n, loads, saves)
	}
}

// Replies whose move the connector did not mint: nothing unmarked or oddly marked is answered once the
// account has changed, and after a restart as the same account every mark it gave still answers.
func TestReplyAdmissionByMark(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t1)
	s := &r2Store{}
	c := r2Connector(f, s, tokenA)
	pollOnce(t, c)
	switchTo(c, tokenB)
	pollOnce(t, c)
	b := collected(t, c)
	ctx := context.Background()
	for _, last := range []string{"", t1, t1 + "#0", t1 + "#", t1 + "#x", t1 + "#-1", t1 + "#18446744073709551616", t1 + "#1#0", "#", t1 + "#2", t1 + "#01x"} {
		if err := c.Reply(ctx, connectorwire.InboxReply{Thread: thread12, Last: last, Text: "x"}); err == nil {
			t.Errorf("a reply to the move %q was sent as %s (epoch 1)", last, loginB)
		}
	}
	if posted := f.posts(); len(posted) != 0 {
		t.Fatalf("posted %v", posted)
	}
	c2 := r2Connector(f, s, tokenB) // a restart as B: no thing is held
	if err := c2.Reply(ctx, connectorwire.InboxReply{Thread: thread12, Last: b.Last, Text: "看过了"}); err != nil {
		t.Errorf("B's reply to its own item after a restart: %v", err)
	}
	if posted := f.posts(); len(posted) != 1 || !strings.HasPrefix(posted[0], tokenB+" ") {
		t.Errorf("posted %v", posted)
	}
}

// r2TrySignIn is a device sign-in as token's account that may fail (the store does, now and then).
func r2TrySignIn(c *Connector, d *deviceWeb, token string) bool {
	ctx := context.Background()
	d.give(token)
	if _, err := c.LoginStep(ctx, "", nil); err != nil {
		return false
	}
	p, err := c.LoginStep(ctx, "device", nil)
	return err == nil && p.Done
}

// The store under load: sign-ins, sign-outs, a computer's login, Reload, checks, polls, Settle and replies at
// once, with a store that fails now and then. Nothing races, hangs or panics; a reply never goes out with another
// account's token; and once the store works and the last sign-in is done, it keeps that sign-in's token and the
// account in use.
func r2StoreUnderLoad(t *testing.T, reload bool) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t1)
	s := &r2Store{}
	d := &deviceWeb{}
	c := New(Config{}, "", nil)
	c.api, c.web = f.srv.URL, d.server(t)
	c.UseSecrets(s)
	signInDevice(t, c, d, tokenA)
	ctx := context.Background()
	owner := map[string]string{"review_requested": loginA, "mention": loginB}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	loop := func(do func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					do()
				}
			}
		}()
	}
	loop(func() {
		if th, ok := things(t, c)[thread12]; ok {
			_ = c.Reply(ctx, connectorwire.InboxReply{Thread: thread12, Last: th.Last, Text: owner[th.Kind]})
		}
	})
	loop(func() {
		_, _, _ = c.poll(ctx)
		_ = c.Settle(ctx, []string{thread12, r2Thread7})
	})
	loop(func() {
		c.check(ctx)
		c.keepAccount(ctx)
		c.restore(ctx)
		if reload {
			c.Reload()
		}
	})
	n := 0
	loop(func() { // the store fails for a while, now and then
		n++
		s.fail(n%7 == 0, n%5 == 0)
	})
	for i := range 200 {
		switch i % 5 {
		case 0:
			r2TrySignIn(c, d, tokenB)
		case 1:
			_ = c.Logout(ctx)
		case 2:
			r2TrySignIn(c, d, tokenA)
		case 3:
			switchTo(c, tokenB)
		case 4:
			c.UseDevice()
			r2TrySignIn(c, d, tokenA)
		}
	}
	close(stop)
	wg.Wait()
	s.fail(false, false)

	for _, p := range f.posts() {
		tok, rest, _ := strings.Cut(p, " ")
		var body struct {
			Body string `json:"body"`
		}
		_, raw, _ := strings.Cut(rest, " ")
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("post %q: %v", p, err)
		}
		if f.logins[tok] != body.Body {
			t.Errorf("a reply to %s's item went out with %s's token", body.Body, f.logins[tok])
		}
	}

	c.UseDevice()
	if !r2TrySignIn(c, d, tokenB) {
		t.Fatalf("the last sign-in, with the store working: %+v", c.Status(ctx))
	}
	c.check(ctx)
	c.mu.Lock()
	login, epoch, tok := c.acct.login, c.acct.epoch, c.token
	c.mu.Unlock()
	kept := s.kept()
	if kept.Token != tokenB || tok != tokenB {
		t.Errorf("the store keeps token %q, memory %q; want %s's", kept.Token, tok, loginB)
	}
	if kept.Account == nil || kept.Account.Login != login || kept.Account.Epoch != epoch || login != loginB {
		t.Errorf("the store keeps account %+v; in use %s at epoch %d", kept.Account, login, epoch)
	}
	// A restart agrees with what was in use.
	c2 := New(Config{}, "", nil)
	c2.api = f.srv.URL
	c2.UseSecrets(s)
	c2.check(ctx)
	c2.mu.Lock()
	login2, epoch2 := c2.acct.login, c2.acct.epoch
	c2.mu.Unlock()
	if login2 != login || epoch2 != epoch {
		t.Errorf("after a restart: %s at epoch %d; before it %s at epoch %d", login2, epoch2, login, epoch)
	}
}

func TestStoreUnderLoad(t *testing.T)           { r2StoreUnderLoad(t, false) }
func TestStoreUnderLoadWithReload(t *testing.T) { r2StoreUnderLoad(t, true) }
