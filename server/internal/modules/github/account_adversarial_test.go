package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// More of the connector moving between GitHub accounts (#7832, account_switch_test.go): what a review tried
// against it. Restarts, three accounts, items the connector no longer holds, the device flow, and reads of every
// kind in flight across the change.

const (
	tokenC = "gho_exampleaccountc00000000000000000000001"
	loginC = "carol-c"
)

// A restart between two accounts: the manager that read as A is gone, and the next one reads the computer's gh
// login, which is B's by now (gh auth switch leaves A's token valid, so nothing met a 401 before). Only the store
// is left. The hub still holds A's open item: it is closed, and not answered as B.
func TestRestartAsAnotherAccount(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	kept := &memSecrets{}
	c1 := f.connectorWith(t, kept, tokenA)
	pollOnce(t, c1)
	a := collected(t, c1)

	c2 := f.connectorWith(t, kept, tokenB)
	pollOnce(t, c2)
	retired(t, c2, a)
	refused(t, f, c2, a)
}

// A restart as the same account keeps everything: the items the hub holds are answered as before (with the
// account's next token), nothing is closed, and the things read again carry the moves the hub knows.
func TestRestartAsTheSameAccountKeepsTheInbox(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	kept := &memSecrets{}
	c1 := f.connectorWith(t, kept, tokenA)
	pollOnce(t, c1)
	a := collected(t, c1)
	ctx := context.Background()

	c2 := f.connectorWith(t, kept, tokenA2)
	// Before anything is read again: the item is the account's, though the connector holds no thing of it.
	if err := c2.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: a.Last, Text: "看过了"}); err != nil {
		t.Fatalf("reply after a restart as the same account: %v", err)
	}
	if posted := f.posts(); len(posted) != 1 || !strings.HasPrefix(posted[0], tokenA2+" "+issue12+"/comments ") {
		t.Errorf("posted %v", posted)
	}
	pollOnce(t, c2)
	if got := collected(t, c2); got.Last != a.Last {
		t.Errorf("the thing's move changed over a restart: %q, was %q (the hub takes it for a new move)", got.Last, a.Last)
	}
	if over := c2.Settle(ctx, []string{a.Thread, "example-org/example-repo#99"}); len(over) != 0 {
		t.Errorf("items closed over a restart as the same account: %v", over)
	}
}

// A restart after a change, before the hub asked which items are over: the next manager still closes the first
// account's items, the second account's moves keep their mark (so the hub's items of them do not move), and a
// further restart changes nothing.
func TestRestartKeepsTheEpochAndWhatIsUnsettled(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.feeds[loginB] = "[" + note("7", "assign", "Issue", "https://api.github.com"+api7, t1) + "]"
	f.visible[loginB][api7] = `{"title":"Example bug","state":"open","user":{"login":"carol"},"body":"出错了","html_url":"https://github.com/example-org/example-repo/issues/7","comments":0}`
	const thread7 = "example-org/example-repo#7"
	kept := &memSecrets{}
	c1 := f.connectorWith(t, kept, tokenA)
	pollOnce(t, c1)
	a := collected(t, c1)
	switchTo(c1, tokenB)
	pollOnce(t, c1)
	b, ok := things(t, c1)[thread7]
	if !ok || b.Last == t1 {
		t.Fatalf("%s's thing, with its move marked: %+v", loginB, things(t, c1))
	}
	ctx := context.Background()

	c2 := f.connectorWith(t, kept, tokenB) // the hub never asked c1
	pollOnce(t, c2)
	if got := things(t, c2)[thread7]; got.Last != b.Last {
		t.Errorf("%s's move changed over the restart: %q, was %q", loginB, got.Last, b.Last)
	}
	if over := c2.Settle(ctx, []string{a.Thread, thread7}); over[a.Thread] == "" || over[thread7] != "" {
		t.Errorf("after the restart: over %v, want only %s's item", over, loginA)
	}
	refused(t, f, c2, a)

	c3 := f.connectorWith(t, kept, tokenB)
	pollOnce(t, c3)
	if got := things(t, c3)[thread7]; got.Last != b.Last {
		t.Errorf("%s's move changed over the second restart: %q, was %q", loginB, got.Last, b.Last)
	}
	if over := c3.Settle(ctx, []string{thread7, "example-org/example-repo#99"}); len(over) != 0 {
		t.Errorf("items closed after the hub had settled: %v", over)
	}
	if err := c3.Reply(ctx, connectorwire.InboxReply{Thread: thread7, Last: b.Last, Text: "看过了"}); err != nil {
		t.Errorf("%s's reply to its own item after two restarts: %v", loginB, err)
	}
}

// The hub asks which items are over before the new account's inbox was read (another source moved, so it
// collects): nothing is closed yet. Once it was read, a thread both accounts have is held, and its item stays for
// the new account's thing to take over; everything the connector holds no thing of is closed, once.
func TestSettleWaitsForTheNewAccountsFirstRead(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c)
	ctx := context.Background()
	const other = "example-org/example-repo#99" // an item of A's the connector no longer holds

	switchTo(c, tokenB)
	if over := c.Settle(ctx, []string{a.Thread, other}); len(over) != 0 {
		t.Errorf("closed before %s's inbox was read: %v", loginB, over)
	}
	pollOnce(t, c)
	b := collected(t, c)
	if b.Kind != "mention" || b.Last == a.Last {
		t.Fatalf("%s's own thing, as another move than %s's: %+v", loginB, loginA, b)
	}
	if over := c.Settle(ctx, []string{a.Thread, other}); over[a.Thread] != "" || over[other] == "" {
		t.Errorf("over %v, want %s closed and the shared thread kept", over, other)
	}
	if over := c.Settle(ctx, []string{other}); len(over) != 0 {
		t.Errorf("closed again after the hub had asked: %v", over)
	}
}

// Three accounts in a row. A and C were notified of the same move (t1); B, in between, holds the thread at
// another move (t2). C's thing must not carry A's Last, or a reply to A's item, arriving late, is posted as C.
func TestThreeAccountsDelayedReply(t *testing.T) {
	f := newTwoAccounts(t)
	f.mu.Lock()
	f.logins[tokenC] = loginC
	f.visible[loginC] = map[string]string{}
	f.mu.Unlock()
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t2)
	f.reviewRequest(loginC, "5", "mention", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c)

	switchTo(c, tokenB)
	pollOnce(t, c)
	collected(t, c)
	switchTo(c, tokenC)
	pollOnce(t, c)
	if st := c.Status(context.Background()); st.Account != loginC {
		t.Fatalf("not reading as %s: %+v", loginC, st)
	}
	if got := collected(t, c); got.Last == a.Last {
		t.Errorf("%s's thing carries the Last of %s's item (%s)", loginC, loginA, a.Last)
	}
	refused(t, f, c, a)
}

// The hub's item of A is at an earlier move (t1) than A's thing when the account changes (t2: the hub has not
// collected the later move yet). B was notified of the t1 move too. Every move of B's is marked, not only one that
// meets A's newest: the reply to A's item is not posted as B.
func TestDelayedReplyToAnEarlierMove(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c) // the item as the hub holds it, and as the reply on its way names it

	f.reviewRequest(loginA, "1", "review_requested", t2) // a later move only A is notified of
	c.inbox.mu.Lock()
	c.inbox.fullAt = time.Time{} // the next poll reads the list (no If-Modified-Since): the fake always answers 200 anyway
	c.inbox.mu.Unlock()
	pollOnce(t, c)
	if got := collected(t, c); got.Last == a.Last {
		t.Fatalf("A's thing did not move: %+v", got)
	}

	switchTo(c, tokenB)
	pollOnce(t, c)
	if b := collected(t, c); b.Kind != "mention" {
		t.Fatalf("not B's thing: %+v", b)
	}
	refused(t, f, c, a)
}

// A thing the user answered through Persona is done (replied) in the connector while the hub keeps its item
// open, waiting for the other side. The account changes: the item is A's and open, and the hub is told to close it.
func TestRepliedThingIsRetiredForTheHub(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c)
	ctx := context.Background()
	if err := c.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: a.Last, Text: "看过了"}); err != nil {
		t.Fatal(err)
	}

	switchTo(c, tokenB)
	pollOnce(t, c)
	if _, ok := things(t, c)[a.Thread]; ok {
		t.Fatalf("still in the inbox")
	}
	if why := c.Settle(ctx, []string{a.Thread})[a.Thread]; why == "" {
		t.Errorf("the hub is not told to close %s's item it keeps open after the reply through Persona", loginA)
	}
}

// A thing older than forgetAfter is dropped from the connector by A's own poll, while the hub keeps its open
// item (open items never leave the hub). When the account changes the connector holds nothing of it: it is closed
// all the same, as everything is that the account in use has no thing of.
func TestOldOpenItemIsRetired(t *testing.T) {
	f := newTwoAccounts(t)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	now := time.Now()
	f.reviewRequest(loginA, "1", "review_requested", now.Add(-forgetAfter+time.Hour).UTC().Format(time.RFC3339))
	var ns []notification
	if err := json.Unmarshal([]byte(f.feeds[loginA]), &ns); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c.applyNotifications(ctx, ns, true, now)
	a := collected(t, c)
	// Two hours on, still as A: the thing is past forgetAfter and A's list read forgets it. The hub's item is open.
	c.applyNotifications(ctx, ns, true, now.Add(2*time.Hour))
	if _, ok := things(t, c)[a.Thread]; ok {
		t.Fatalf("the thing was not forgotten")
	}

	switchTo(c, tokenB)
	pollOnce(t, c)
	if why := c.Settle(ctx, []string{a.Thread})[a.Thread]; why == "" {
		t.Errorf("%s's open item, older than forgetAfter, is not retired after the change to %s", loginA, loginB)
	}
}

// counting is a fake GitHub in front of f that counts the requests by path.
func counting(t *testing.T, f *twoAccounts) (string, func(path string) int) {
	var mu sync.Mutex
	n := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n[r.URL.Path]++
		mu.Unlock()
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return n[path]
	}
}

// Same account: polls, thread reads and replies ask /user no more than the sign-in check did.
func TestSameAccountAsksUserOnce(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.visible[loginA][api7] = `{"title":"as A reads it"}`
	c := f.connector(t, tokenA, tokenA2)
	api, count := counting(t, f)
	c.api = api
	ctx := context.Background()
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
	if err := c.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: a.Last, Text: "看过了"}); err != nil {
		t.Fatal(err)
	}
	if n := count("/user"); n != 0 {
		t.Errorf("/user asked %d times with the account known", n)
	}
	// A refresh to the same account's next token: one question for the new token (Run's check asks too, kicked).
	f.revoke(tokenA)
	pollOnce(t, c)
	if n := count("/user"); n != 1 {
		t.Errorf("/user asked %d times over a same-account refresh, want 1", n)
	}
	if n := count("/notifications"); n != 5 { // 3 + the refused one + its retry
		t.Errorf("/notifications asked %d times", n)
	}
	if err := c.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: a.Last, Text: "再说一句"}); err != nil {
		t.Errorf("reply after the refresh: %v", err)
	}
}

// A -> B -> A without the hub settling in between: A's own item must still be answerable as A.
func TestRoundTripReplyAsTheFirstAccount(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	c := f.connector(t, tokenA)
	pollOnce(t, c)
	a := collected(t, c)
	switchTo(c, tokenB)
	pollOnce(t, c)
	switchTo(c, tokenA)
	pollOnce(t, c)
	back := collected(t, c)
	ctx := context.Background()
	// The hub's item is still the one it made (Last a.Last) until it collects again.
	errOld := c.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: a.Last, Text: "old"})
	errNew := c.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: back.Last, Text: "new"})
	t.Logf("A->B->A: Last before %q, after %q; reply to the item as it was: %v; as read again: %v", a.Last, back.Last, errOld, errNew)
	for _, p := range f.posts() {
		if !strings.HasPrefix(p, tokenA+" ") {
			t.Errorf("posted as another account: %s", p)
		}
	}
	if errNew != nil {
		t.Errorf("reply to A's own thing after A -> B -> A: %v", errNew)
	}
}

const (
	repoPath = "/repos/example-org/example-repo"
	ciMain   = "ci:example-org/example-repo@main"
)

// ciFailure puts a failed run of main in login's unread notifications and lets it read the repository and its runs.
func (f *twoAccounts) ciFailure(login, id string, at time.Time, runs string) {
	b, _ := json.Marshal(map[string]any{"id": id, "reason": "ci_activity", "updated_at": at.UTC().Format(time.RFC3339),
		"subject":    map[string]any{"title": "CI workflow run failed for main branch", "url": nil, "type": "CheckSuite"},
		"repository": map[string]string{"full_name": "example-org/example-repo", "html_url": "https://github.com/example-org/example-repo"}})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feeds[login] = "[" + string(b) + "]"
	f.visible[login][repoPath] = `{"default_branch":"main"}`
	f.visible[login][repoPath+"/actions/runs"] = `{"workflow_runs":` + runs + `}`
	f.visible[login][repoPath+"/branches/main"] = `{}`
}

// A CI follow-up in flight across the change: A's check of the branch answers after the connector became B, which
// has a CI item of the same branch. Nothing panics and B's item is not moved by what A read.
func TestCIFollowUpInFlightAcrossAnAccountChange(t *testing.T) {
	old := ciRecheck
	ciRecheck = 0
	t.Cleanup(func() { ciRecheck = old })
	f := newTwoAccounts(t)
	now := time.Now()
	later := `[{"name":"CI","status":"completed","conclusion":"failure","updated_at":"` + now.Add(-10*time.Minute).UTC().Format(time.RFC3339) + `"}]`
	c := f.connector(t, tokenA)
	ctx := context.Background()
	pollOnce(t, c) // an empty list: the state is there
	f.ciFailure(loginA, "1", now.Add(-2*time.Hour), later)
	f.ciFailure(loginB, "2", now.Add(-3*time.Hour), `[]`)
	var ns []notification
	if err := json.Unmarshal([]byte(f.feeds[loginA]), &ns); err != nil {
		t.Fatal(err)
	}
	c.applyNotifications(ctx, ns, true, now) // without the follow-up, which the test runs itself
	if _, ok := things(t, c)[ciMain]; !ok {
		t.Fatalf("A's CI thing is not in the inbox: %+v", things(t, c))
	}

	h := f.hold(t, tokenA, repoPath+"/actions/runs")
	done := make(chan bool, 1)
	go func() { done <- c.followUpCI(ctx) }()
	await(t, h.entered, "A's follow-up to reach GitHub")
	switchTo(c, tokenB)
	if err := json.Unmarshal([]byte(f.feeds[loginB]), &ns); err != nil {
		t.Fatal(err)
	}
	c.applyNotifications(ctx, ns, true, now)
	b, ok := things(t, c)[ciMain]
	if !ok {
		t.Fatalf("B's CI thing is not in the inbox: %+v", things(t, c))
	}
	h.let()
	if moved := await(t, done, "A's follow-up to return"); moved {
		t.Errorf("A's follow-up says it moved something after the change")
	}
	if got := things(t, c)[ciMain]; got.Last != b.Last || !got.At.Equal(b.At) || got.Done != b.Done {
		t.Errorf("B's CI thing was moved by A's follow-up: %+v", got)
	}
}

// A CI notification's detail read in flight across the change: A's read of the repository answers after the
// connector became B. Nothing of it lands in B's state.
func TestCIDescribeInFlightAcrossAnAccountChange(t *testing.T) {
	f := newTwoAccounts(t)
	now := time.Now()
	f.ciFailure(loginA, "1", now.Add(-2*time.Hour), `[]`)
	c := f.connector(t, tokenA)
	h := f.hold(t, tokenA, repoPath)
	polled := make(chan error, 1)
	go func() {
		_, _, err := c.poll(context.Background())
		polled <- err
	}()
	await(t, h.entered, "A's read of the repository to reach GitHub")
	switchTo(c, tokenB)
	h.let()
	if err := await(t, polled, "A's poll to return"); err != nil {
		t.Errorf("A's poll: %v", err)
	}
	st := &c.inbox
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.things) != 0 || len(st.ci) != 0 || len(st.repos) != 0 || len(st.seen) != 0 || st.lastModified != "" {
		t.Errorf("A's poll wrote into B's state: things %v ci %v repos %v seen %v cursor %q", st.things, st.ci, st.repos, st.seen, st.lastModified)
	}
}

// threadFixture lets login read PR 12 as a thread page with two failed Actions jobs, everything marked with who.
func (f *twoAccounts) threadFixture(login, who string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.visible[login]
	v[issue12] = `{"title":"as ` + who + `","state":"open","user":{"login":"x"},"pull_request":{},"created_at":"` + t1 + `"}`
	v[issue12+"/events"] = `[]`
	v[api12] = `{"head":{"ref":"feat","sha":"abc"},"base":{"ref":"main"}}`
	v[repoPath+"/commits/abc/check-runs"] = `{"check_runs":[
		{"id":1,"name":"build","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}},
		{"id":2,"name":"test","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}}]}`
	v[repoPath+"/actions/jobs/1/logs"] = "log 1 as " + who
	v[repoPath+"/actions/jobs/2/logs"] = "log 2 as " + who
}

// A thread page is one account's or fails (Thread's own words). A's page is reading a failed job's log when the
// connector becomes B, which reads the same page: A's page must not come back made of both accounts' answers.
func TestThreadPageIsOneAccountsOrFails(t *testing.T) {
	f := newTwoAccounts(t)
	f.threadFixture(loginA, "A")
	f.threadFixture(loginB, "B")
	c := f.connector(t, tokenA)
	ctx := context.Background()

	h := f.hold(t, tokenA, repoPath+"/actions/jobs/1/logs")
	type result struct {
		page connectorwire.ThreadPage
		err  error
	}
	late := make(chan result, 1)
	go func() {
		p, err := c.Thread(ctx, thread12, "", 50)
		late <- result{p, err}
	}()
	await(t, h.entered, "A's page to reach the first log")
	switchTo(c, tokenB)
	if p, err := c.Thread(ctx, thread12, "", 50); err != nil || p.Chat.Name != "as B" {
		t.Fatalf("B's page: %q %v", p.Chat.Name, err)
	}
	h.let()
	r := await(t, late, "A's page to return")
	if r.err != nil {
		return // it failed: fine
	}
	var logs []string
	for _, m := range r.page.Messages {
		if m.Check != nil {
			logs = append(logs, m.Check.Log)
		}
	}
	t.Errorf("the page that began as %s came back after the change without an error: title %q, check logs %q", loginA, r.page.Chat.Name, logs)
}

// Thread pages, CI polls and replies while the account changes back and forth, under the race detector: nothing
// panics, and a page that came back with the same account before and after is that account's throughout.
func TestThreadPagesAndCIWhileAccountsChange(t *testing.T) {
	old := ciRecheck
	ciRecheck = 0
	t.Cleanup(func() { ciRecheck = old })
	f := newTwoAccounts(t)
	now := time.Now()
	later := `[{"name":"CI","status":"completed","conclusion":"failure","updated_at":"` + now.Add(-10*time.Minute).UTC().Format(time.RFC3339) + `"}]`
	f.ciFailure(loginA, "1", now.Add(-2*time.Hour), later)
	f.ciFailure(loginB, "2", now.Add(-2*time.Hour), later)
	f.threadFixture(loginA, "A")
	f.threadFixture(loginB, "B")
	who := map[string]string{loginA: "A", loginB: "B"}
	c := f.connector(t, tokenA)
	ctx := context.Background()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wrong := make(chan string, 8)
	say := func(s string) {
		select {
		case wrong <- s:
		default:
		}
	}
	pages := make(chan struct{}, 1)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				before := c.Status(ctx).Account
				p, err := c.Thread(ctx, thread12, "", 50)
				after := c.Status(ctx).Account
				if err != nil || before != after || after == "" {
					continue // a page that came back at all began and ended under one account (Thread)
				}
				select {
				case pages <- struct{}{}:
				default:
				}
				if p.Chat.Name != "as "+who[after] {
					say("page title " + p.Chat.Name + " read as " + after)
				}
				for _, m := range p.Messages {
					if m.Check != nil && m.Check.Log != "" && !strings.HasSuffix(m.Check.Log, "as "+who[after]) {
						say("check log " + m.Check.Log + " read as " + after)
					}
				}
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _, _ = c.poll(ctx)
				_, _ = c.Collect(ctx)
				_ = c.Settle(ctx, []string{ciMain, thread12})
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = c.SetOptions(c.Options())
				c.check(ctx)
			}
		}
	}()
	for i := range 80 {
		switchTo(c, []string{tokenB, tokenA}[i%2])
		select {
		case <-pages:
		default:
		}
		await(t, pages, "a page read as the account in use")
	}
	close(stop)
	wg.Wait()
	close(wrong)
	for w := range wrong {
		t.Errorf("another account's answer in a page: %s", w)
	}
}

// deviceWeb is GitHub's web host for the device flow: every code is authorized at once, for the token next says.
type deviceWeb struct {
	mu   sync.Mutex
	next string
}

func (d *deviceWeb) give(token string) {
	d.mu.Lock()
	d.next = token
	d.mu.Unlock()
}

func (d *deviceWeb) server(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"` + d.next + `","token_type":"bearer"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// deviceConnector is a connector with no computer, signed in with the device flow as token's account.
func (f *twoAccounts) deviceConnector(t *testing.T, d *deviceWeb, token string) *Connector {
	c := New(Config{}, "", nil)
	c.api, c.web = f.srv.URL, d.server(t)
	c.UseSecrets(&memSecrets{})
	signInDevice(t, c, d, token)
	return c
}

func signInDevice(t *testing.T, c *Connector, d *deviceWeb, token string) {
	t.Helper()
	ctx := context.Background()
	d.give(token)
	if _, err := c.LoginStep(ctx, "", nil); err != nil {
		t.Fatalf("device code: %v", err)
	}
	p, err := c.LoginStep(ctx, "device", nil)
	if err != nil || !p.Done {
		t.Fatalf("device sign-in: %+v %v", p, err)
	}
}

// Sign out and in again as another account with the device flow: what was read as the first is retired.
func TestDeviceSignInAsAnotherAccount(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	d := &deviceWeb{}
	c := f.deviceConnector(t, d, tokenA)
	pollOnce(t, c)
	a := collected(t, c)
	ctx := context.Background()

	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	// Signed out: A's thing is still listed (nobody else reads), but nothing can be answered.
	if err := c.Reply(ctx, connectorwire.InboxReply{Thread: a.Thread, Last: a.Last, Text: "x"}); err == nil {
		t.Errorf("a reply went out signed out")
	}
	signInDevice(t, c, d, tokenB)
	if st := c.Status(ctx); st.Account != loginB {
		t.Fatalf("not %s: %+v", loginB, st)
	}
	pollOnce(t, c)
	retired(t, c, a)
	refused(t, f, c, a)

	// A second code while signed in (no sign-out): the token is replaced in place.
	signInDevice(t, c, d, tokenA)
	pollOnce(t, c)
	back := collected(t, c)
	if back.Last == a.Last {
		t.Logf("back as A the thing carries the same Last")
	}
	signInDevice(t, c, d, tokenB)
	pollOnce(t, c)
	retired(t, c, back)
	refused(t, f, c, back)
}

// The device flow and a computer's login taking turns while replies, polls and thread reads run: a reply never
// reaches GitHub with the token of another account than its item's.
func TestDeviceAndMachineTakeTurns(t *testing.T) {
	f := newTwoAccounts(t)
	f.reviewRequest(loginA, "1", "review_requested", t1)
	f.reviewRequest(loginB, "9", "mention", t1)
	d := &deviceWeb{}
	c := f.deviceConnector(t, d, tokenA)
	ctx := context.Background()
	owner := map[string]string{"review_requested": loginA, "mention": loginB}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
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
	go func() {
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
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.check(ctx)
				c.Reload()
			}
		}
	}()
	for i := range 60 {
		switch i % 4 {
		case 0: // a computer signed in as B
			switchTo(c, tokenB)
		case 1: // the device flow, as A
			c.UseDevice()
			signInDevice(t, c, d, tokenA)
		case 2: // sign out, and in as B
			_ = c.Logout(ctx)
			signInDevice(t, c, d, tokenB)
		case 3: // a computer signed in as A
			switchTo(c, tokenA)
		}
		select {
		case <-f.wrote:
		default:
		}
		await(t, f.wrote, "a reply as the account in use")
	}
	close(stop)
	wg.Wait()
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
}
