package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// The connector moving from one GitHub account to another (#7832): what was read as the first is retired, never
// answered or served as the second, by whichever way the account changes. Every test here runs two accounts
// against one fake GitHub.

const (
	tokenA  = "gho_exampleaccounta00000000000000000000001"
	tokenA2 = "gho_exampleaccounta00000000000000000000002" // account A's next token
	tokenB  = "gho_exampleaccountb00000000000000000000001"
	loginA  = "alice-a"
	loginB  = "bob-b"

	thread12 = "example-org/example-repo#12"
	issue12  = "/repos/example-org/example-repo/issues/12"
)

// twoAccounts is a GitHub that knows who each token is: every account has its own unread notifications and its
// own view of the paths it may read. It notes what was asked as whom, and can keep one answer back (hold).
type twoAccounts struct {
	srv *httptest.Server

	mu      sync.Mutex
	logins  map[string]string            // token -> login; a token not here is refused (401)
	feeds   map[string]string            // login -> its unread notifications (JSON)
	visible map[string]map[string]string // login -> path -> what it reads there (JSON)
	since   map[string][]string          // login -> the If-Modified-Since of each of its notification reads
	reads   map[string]int               // "login path" -> GETs answered
	posted  []string                     // "token path body" of every POST
	held    map[string]*heldAnswer       // "token path" -> the next answer to it waits
	wrote   chan struct{}                // a POST arrived (never blocks: one is kept)
}

// heldAnswer is one answer kept back: entered closes when the request has arrived, let lets it go on.
type heldAnswer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *heldAnswer) let() { h.once.Do(func() { close(h.release) }) }

func newTwoAccounts(t *testing.T) *twoAccounts {
	f := &twoAccounts{
		logins:  map[string]string{tokenA: loginA, tokenA2: loginA, tokenB: loginB},
		feeds:   map[string]string{},
		visible: map[string]map[string]string{loginA: {}, loginB: {}},
		since:   map[string][]string{},
		reads:   map[string]int{},
		held:    map[string]*heldAnswer{},
		wrote:   make(chan struct{}, 1),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *twoAccounts) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
		http.NotFound(w, r)
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	h := f.held[tok+" "+r.URL.Path]
	delete(f.held, tok+" "+r.URL.Path)
	f.mu.Unlock()
	if h != nil {
		close(h.entered)
		select {
		case <-h.release:
		case <-r.Context().Done():
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	login, ok := f.logins[tok]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		return
	}
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4000")
	switch {
	case r.URL.Path == "/user":
		_, _ = w.Write([]byte(`{"login":"` + login + `"}`))
	case r.URL.Path == "/notifications":
		f.since[login] = append(f.since[login], r.Header.Get("If-Modified-Since"))
		w.Header().Set("Last-Modified", fixtureDay.Format(http.TimeFormat))
		feed := f.feeds[login]
		if feed == "" || r.URL.Query().Get("page") != "1" {
			feed = "[]"
		}
		_, _ = w.Write([]byte(feed))
	case r.Method == http.MethodPost:
		b, _ := io.ReadAll(r.Body)
		f.posted = append(f.posted, tok+" "+r.URL.Path+" "+string(b))
		select {
		case f.wrote <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case f.visible[login][r.URL.Path] != "":
		f.reads[login+" "+r.URL.Path]++
		w.Header().Set("ETag", `"`+login+`"`)
		_, _ = w.Write([]byte(f.visible[login][r.URL.Path]))
	case strings.HasSuffix(r.URL.Path, "/comments") || strings.HasSuffix(r.URL.Path, "/reviews"):
		_, _ = w.Write([]byte(`[]`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}
}

// hold keeps the next answer to path asked with token back until let, or until the test ends.
func (f *twoAccounts) hold(t *testing.T, token, path string) *heldAnswer {
	h := &heldAnswer{entered: make(chan struct{}), release: make(chan struct{})}
	f.mu.Lock()
	f.held[token+" "+path] = h
	f.mu.Unlock()
	t.Cleanup(h.let) // before the server closes: a failed assertion must not leave it waiting
	return h
}

// revoke makes GitHub refuse token from now on.
func (f *twoAccounts) revoke(token string) {
	f.mu.Lock()
	delete(f.logins, token)
	f.mu.Unlock()
}

func (f *twoAccounts) posts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posted...)
}

func (f *twoAccounts) sinceOf(login string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.since[login]...)
}

// reviewRequest puts PR 12 in login's unread notifications (as reason, at the time at) and lets it read the PR.
func (f *twoAccounts) reviewRequest(login, id, reason, at string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feeds[login] = "[" + note(id, reason, "PullRequest", "https://api.github.com"+api12, at) + "]"
	f.visible[login][api12] = openPR(`{"login":"` + login + `"}`)
	f.visible[login][issue12] = `{"comments":0}`
}

// connector is a connector reading a computer's gh login, which hands out tokens in turn, signed in with the first.
func (f *twoAccounts) connector(t *testing.T, tokens ...string) *Connector {
	return f.connectorWith(t, nil, tokens...)
}

// connectorWith is connector with the hub's store for its sign-in: what a manager keeps across a restart. A second
// connector with the same store is the manager started again.
func (f *twoAccounts) connectorWith(t *testing.T, kept *memSecrets, tokens ...string) *Connector {
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: tokens}).fetch)
	c.api = f.srv.URL
	if kept != nil {
		c.UseSecrets(kept)
	}
	c.check(context.Background())
	if st := c.Status(context.Background()); st.State != connectorwire.StateOK || st.Account != f.logins[tokens[0]] {
		t.Fatalf("not signed in as %s: %+v", f.logins[tokens[0]], st)
	}
	return c
}

// switchTo chooses another computer, whose gh login is token's account, and lets the connector check it.
func switchTo(c *Connector, token string) {
	ctx := context.Background()
	c.UseMachine(ctx, "mac-2", "Mac 2", func(context.Context, string) (string, error) { return token, nil })
	c.check(ctx)
}

// await is what ch gives, within a bound.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// collected is A's review request as the hub holds it: in the inbox and replyable.
func collected(t *testing.T, c *Connector) connectorwire.InboxThing {
	t.Helper()
	th, ok := things(t, c)[thread12]
	if !ok || !th.CanReply || th.Done != "" {
		t.Fatalf("the review request is not in the inbox to reply to: %+v", things(t, c))
	}
	return th
}

// refused checks that a reply to th's item was turned away without anything sent to GitHub.
func refused(t *testing.T, f *twoAccounts, c *Connector, th connectorwire.InboxThing) {
	t.Helper()
	err := c.Reply(context.Background(), connectorwire.InboxReply{Thread: th.Thread, Last: th.Last, Text: "看过了"})
	if err == nil {
		t.Errorf("a reply to the other account's item was accepted")
	}
	if posted := f.posts(); len(posted) != 0 {
		t.Errorf("the reply went to GitHub: %v", posted)
	}
}

// retired checks that th left the inbox and that the hub is told to close its item (it asks once the account in
// use has read its inbox: the tests poll first).
func retired(t *testing.T, c *Connector, th connectorwire.InboxThing) {
	t.Helper()
	if still, ok := things(t, c)[th.Thread]; ok {
		t.Errorf("the other account's thing is still in the inbox: %+v", still)
	}
	if why := c.Settle(context.Background(), []string{th.Thread})[th.Thread]; why == "" {
		t.Errorf("the hub is not told to close the other account's item")
	}
}

// Switch, then poll: choosing another account's login retires what was read as the first. Its things leave the
// inbox, the hub is told to close their items, a reply to one is refused before any request, and the new
// account's first poll does not carry the old one's If-Modified-Since.
func TestAccountSwitchRetiresTheInbox(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c)

	switchTo(c, tokenB)
	pollOnce(t, c)
	if st := c.Status(context.Background()); st.Account != loginB {
		t.Fatalf("not reading as %s: %+v", loginB, st)
	}
	retired(t, c, a)
	refused(t, f, c, a)
	if since := f.sinceOf(loginB); len(since) != 1 || since[0] != "" {
		t.Errorf("%s's notifications were read with %s's cursor: If-Modified-Since %q", loginB, loginA, since)
	}
}

// A token refresh that changes the login: GitHub refuses A's token in the middle of a poll, and the computer
// hands over B's (gh was switched there). Nobody chose a source and no check ran: the poll that met the 401 must
// not read B's notifications into A's inbox, and A's things are retired.
func TestTokenRefreshToAnotherAccountRetiresTheInbox(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.feeds[loginB] = "[" + note("7", "assign", "Issue", "https://api.github.com"+api7, t1) + "]"
	f.visible[loginB][api7] = `{"title":"Example bug","state":"open","user":{"login":"carol"},"body":"出错了","html_url":"https://github.com/example-org/example-repo/issues/7","comments":0}`
	c := f.connector(t, tokenA, tokenB)
	pollOnce(t, c)
	a := collected(t, c)

	f.revoke(tokenA)
	pollOnce(t, c)
	if st := c.Status(context.Background()); st.Account != loginB {
		t.Errorf("the status still names %s after the computer's token became %s's: %+v", loginA, loginB, st)
	}
	if still, ok := things(t, c)[thread12]; ok {
		t.Errorf("%s's thing is still in the inbox after the poll that met the 401: %+v", loginA, still)
	}
	refused(t, f, c, a)
	for _, since := range f.sinceOf(loginB) {
		if since != "" {
			t.Errorf("%s's notifications were read with %s's cursor: If-Modified-Since %q", loginB, loginA, since)
		}
	}

	// From here on the inbox is B's (Watch polls again at once: the change wakes it).
	pollOnce(t, c)
	retired(t, c, a)
	got := things(t, c)
	if _, ok := got["example-org/example-repo#7"]; !ok || len(got) != 1 {
		t.Errorf("%s's inbox: %+v", loginB, got)
	}
}

// A delayed reply: both accounts have the same PR unread, notified of the same move at the same time. The thread
// alone does not say whose item a reply answers, so a reply to the item as A read it, arriving after the change,
// is refused, while B's own thing is another move to the hub (its Last differs) and can be answered as B.
func TestDelayedReplyToAnotherAccountsItemIsRefused(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c)

	switchTo(c, tokenB)
	pollOnce(t, c)
	b := collected(t, c)
	if b.Kind != "mention" {
		t.Fatalf("the thread's thing is not %s's own: %+v", loginB, b)
	}
	if b.Last == a.Last {
		t.Errorf("%s's thing carries the Last of %s's item (%s): the hub keeps the old item as it was", loginB, loginA, a.Last)
	}
	if why := c.Settle(context.Background(), []string{thread12})[thread12]; why != "" {
		t.Errorf("the thread %s took over is closed: %s", loginB, why)
	}
	refused(t, f, c, a)

	if err := c.Reply(context.Background(), connectorwire.InboxReply{Thread: thread12, Last: b.Last, Text: "看过了"}); err != nil {
		t.Fatalf("%s's reply to its own thing: %v", loginB, err)
	}
	if posted := f.posts(); len(posted) != 1 || !strings.HasPrefix(posted[0], tokenB+" "+issue12+"/comments ") {
		t.Errorf("posted %v", posted)
	}
}

// The thread cache: an answer kept for one account is not served to the next, which reads its own.
func TestThreadCacheIsNotServedToAnotherAccount(t *testing.T) {
	f := newTwoAccounts(t)
	f.visible[loginA][api7] = `{"title":"as A reads it"}`
	f.visible[loginB][api7] = `{"title":"as B reads it"}`
	c := f.connector(t, tokenA)
	ctx := context.Background()
	var it struct {
		Title string `json:"title"`
	}
	if err := c.cachedGet(ctx, api7, &it); err != nil || it.Title != "as A reads it" {
		t.Fatalf("as %s: %q %v", loginA, it.Title, err)
	}

	switchTo(c, tokenB)
	if err := c.cachedGet(ctx, api7, &it); err != nil || it.Title != "as B reads it" {
		t.Errorf("as %s, within threadFresh of %s's read: %q %v", loginB, loginA, it.Title, err)
	}
}

// An ETag read in flight across the reset: GitHub answers A's read after the connector became B. The late answer
// is nobody's: it is not served, it does not land in B's cache, and nothing panics on the cache that was reset.
func TestThreadReadInFlightAcrossAnAccountChange(t *testing.T) {
	f := newTwoAccounts(t)
	f.visible[loginA][api7] = `{"title":"as A reads it"}`
	f.visible[loginB][api7] = `{"title":"as B reads it"}`
	c := f.connector(t, tokenA)
	ctx := context.Background()
	type title struct {
		Title string `json:"title"`
	}

	h := f.hold(t, tokenA, api7)
	late := make(chan error, 1)
	go func() {
		var it title
		late <- c.cachedGet(ctx, api7, &it)
	}()
	await(t, h.entered, "A's read to reach GitHub")
	switchTo(c, tokenB)
	h.let()
	if err := await(t, late, "A's read to return"); err == nil {
		t.Errorf("a read answered as %s after the account changed was served", loginA)
	}

	var it title
	if err := c.cachedGet(ctx, api7, &it); err != nil || it.Title != "as B reads it" {
		t.Errorf("as %s after %s's late answer: %q %v", loginB, loginA, it.Title, err)
	}
}

// An out-of-order /user answer: a check of A's token is still waiting for GitHub when the user chooses B, whose
// check answers first. A's answer, arriving last, must not put A back as the account in use.
func TestLateUserAnswerDoesNotRestoreTheAccount(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c)
	ctx := context.Background()

	h := f.hold(t, tokenA, "/user")
	checked := make(chan struct{})
	go func() {
		defer close(checked)
		c.check(ctx)
	}()
	await(t, h.entered, "A's check to reach GitHub")
	switchTo(c, tokenB)
	pollOnce(t, c)
	h.let()
	await(t, checked, "A's check to return")

	if st := c.Status(ctx); st.State != connectorwire.StateOK || st.Account != loginB {
		t.Errorf("A's late /user answer changed the status: %+v", st)
	}
	pollOnce(t, c)
	retired(t, c, a)
	refused(t, f, c, a)
}

// Watch tells the hub when an account's things have left, at once (the change wakes it), so the hub settles their
// items without waiting a poll, or for the new account's inbox to move.
func TestWatchTellsTheHubWhenAnAccountsThingsLeave(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	c := f.connector(t, tokenA)
	ctx, cancel := context.WithCancel(context.Background())
	changes, stopped := make(chan connectorwire.Scope, 64), make(chan error, 1)
	go func() {
		stopped <- c.Watch(ctx, func(s connectorwire.Scope) {
			select {
			case changes <- s:
			default:
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	await(t, changes, "A's review request to come in")
	a := collected(t, c)

	switchTo(c, tokenB)
	await(t, changes, "Watch to say the inbox changed after the account did")
	retired(t, c, a)
}

// A refresh outside the inbox: a tool's read meets the 401 and the computer hands over B's token. Nothing asked
// GitHub whose it is yet, so what was kept and collected is still A's: none of it is served or answered with B's
// token, and the first thing that belongs to an account finds out and retires it.
func TestRefreshOutsideTheInboxDoesNotServeTheOldAccount(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.visible[loginA][api7] = `{"title":"as A reads it"}`
	f.visible[loginB][api7] = `{"title":"as B reads it"}`
	c := f.connector(t, tokenA, tokenB)
	pollOnce(t, c)
	a := collected(t, c)
	ctx := context.Background()
	var it struct {
		Title string `json:"title"`
	}
	if err := c.cachedGet(ctx, api7, &it); err != nil || it.Title != "as A reads it" {
		t.Fatalf("as %s: %q %v", loginA, it.Title, err)
	}

	f.revoke(tokenA)
	if _, err := c.Get(ctx, api7, &it); err != nil || it.Title != "as B reads it" { // a tool's read: the token in use, whoever's
		t.Fatalf("a read after the refresh: %q %v", it.Title, err)
	}
	it.Title = ""
	if err := c.cachedGet(ctx, api7, &it); err == nil && it.Title != "as B reads it" {
		t.Errorf("served %q, kept for %s, with %s's token in use", it.Title, loginA, loginB)
	}
	refused(t, f, c, a)
	pollOnce(t, c)
	retired(t, c, a)
	if err := c.cachedGet(ctx, api7, &it); err != nil || it.Title != "as B reads it" {
		t.Errorf("as %s: %q %v", loginB, it.Title, err)
	}
}

// A same-account refresh keeps everything: GitHub refuses the token, the computer hands over the same account's
// next one, and the inbox, the cursor and the reply go on as before. So does the same account read from another
// computer.
func TestSameAccountRefreshKeepsTheInbox(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	c := f.connector(t, tokenA, tokenA2)
	pollOnce(t, c)
	a := collected(t, c)
	ctx := context.Background()

	f.revoke(tokenA)
	pollOnce(t, c)
	if got := collected(t, c); got.Last != a.Last {
		t.Fatalf("the thing changed over a refresh: %+v", got)
	}
	if since := f.sinceOf(loginA); since[len(since)-1] == "" {
		t.Errorf("the refresh lost the notifications cursor: If-Modified-Since %q", since)
	}
	if why := c.Settle(ctx, []string{thread12})[thread12]; why != "" {
		t.Errorf("the item is closed over a refresh: %s", why)
	}

	c.UseMachine(ctx, "mac-2", "Mac 2", func(context.Context, string) (string, error) { return tokenA2, nil })
	c.check(ctx)
	pollOnce(t, c)
	collected(t, c)
	if err := c.Reply(ctx, connectorwire.InboxReply{Thread: thread12, Last: a.Last, Text: "看过了"}); err != nil {
		t.Fatalf("reply after a same-account refresh: %v", err)
	}
	if posted := f.posts(); len(posted) != 1 || !strings.HasPrefix(posted[0], tokenA2+" "+issue12+"/comments ") {
		t.Errorf("posted %v", posted)
	}
}

// Many changes of account while replies, polls and thread reads run: whatever the order, a reply never reaches
// GitHub with the token of an account other than the one its item was read as. Both accounts have the same PR
// unread, notified at the same time: the worst case, in which neither the thread nor the time tells the items
// apart. The reply's text names the account its item was collected as; the fake GitHub compares it with the token.
func TestRepliesNeverCrossAccounts(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t1)
	f.visible[loginA][api7] = `{"title":"as A reads it"}`
	f.visible[loginB][api7] = `{"title":"as B reads it"}`
	c := f.connector(t, tokenA)
	ctx := context.Background()
	owner := map[string]string{"review_requested": loginA, "mention": loginB} // whose inbox a thing of this kind is in
	title := map[string]string{loginA: "as A reads it", loginB: "as B reads it"}

	const rounds = 60
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // the user, answering whatever item the hub shows
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if th, ok := things(t, c)[thread12]; ok {
				_ = c.Reply(ctx, connectorwire.InboxReply{Thread: thread12, Last: th.Last, Text: owner[th.Kind]})
			}
		}
	}()
	go func() { // Watch's polls
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _, _ = c.poll(ctx)
			}
		}
	}()
	served := make(chan string, 1)
	go func() { // the thread page: what it serves is what the account the read ran as may read
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			var it struct {
				Title string `json:"title"`
			}
			before := c.Status(ctx).Account
			err := c.cachedGet(ctx, api7, &it)
			after := c.Status(ctx).Account
			if err == nil && after != "" && before == after && it.Title != title[after] {
				select {
				case served <- "read as " + after + ": " + it.Title:
				default:
				}
			}
		}
	}()
	// Each account gets to answer before the next change, so every change has replies, polls and reads around it.
	for i := range rounds {
		switchTo(c, []string{tokenB, tokenA}[i%2])
		select {
		case <-f.wrote:
		default:
		}
		await(t, f.wrote, "a reply as the account in use")
	}
	close(stop)
	wg.Wait()

	select {
	case wrong := <-served:
		t.Errorf("the thread cache served another account's answer: %s", wrong)
	default:
	}
	posted := f.posts()
	for _, p := range posted {
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
	t.Logf("%d replies posted over %d account changes", len(posted), rounds)
}
