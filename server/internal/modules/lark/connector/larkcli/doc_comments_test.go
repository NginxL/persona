package larkcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"

	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

func docTestConnector(t *testing.T, scenario string) *Connector {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "..", "larkcli", "testdata", "fake-lark-cli.sh"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKCLI_FAKE_SCENARIO", scenario)
	t.Setenv("LARKCLI_FAKE_PHASE", "initial")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return &Connector{
		runner: adapter.New(adapter.Config{Bin: bin, Profile: "example", Timeout: time.Second, Now: func() time.Time { return now }}),
		docs:   newDocumentInbox(),
		now:    func() time.Time { return now },
	}
}

func TestDocumentCommentsMentionParticipationCursorAndDedup(t *testing.T) {
	c := docTestConnector(t, "comments_reply")
	link := "https://example.feishu.cn/docx/doc_example"
	if err := c.SetOptions(json.RawMessage(`{"documents":["` + link + `"]}`)); err != nil {
		t.Fatal(err)
	}
	if !c.pollDocuments(context.Background()) {
		t.Fatal("first comment round did not move")
	}
	things, err := c.Collect(context.Background())
	if err != nil || len(things) != 1 {
		t.Fatalf("things = %+v, err = %v", things, err)
	}
	first := things[0]
	if first.Kind != "doc" || first.Title != "Example design document" || !strings.Contains(first.Preview, "please review") {
		t.Fatalf("first thing = %+v", first)
	}
	if !strings.Contains(first.URL, "comment_id=comment_example") || first.Extra == nil || len(first.Extra.Doc) == 0 {
		t.Fatalf("comment link/block missing: %+v", first)
	}
	if c.pollDocuments(context.Background()) {
		t.Fatal("same comments were collected twice")
	}
	things, _ = c.Collect(context.Background())
	if len(things) != 1 || things[0].Last != first.Last {
		t.Fatalf("dedup changed the item: %+v", things)
	}

	t.Setenv("LARKCLI_FAKE_PHASE", "reply")
	if !c.pollDocuments(context.Background()) {
		t.Fatal("new reply did not move the item")
	}
	things, _ = c.Collect(context.Background())
	if len(things) != 1 || things[0].Thread != first.Thread || things[0].Last == first.Last || !strings.Contains(things[0].Preview, "missing step") {
		t.Fatalf("reply did not update one thread: %+v", things)
	}
}

func TestDocumentCommentsUnwatchedAndResolvedAreSkipped(t *testing.T) {
	c := docTestConnector(t, "comments")
	if c.pollDocuments(context.Background()) {
		t.Fatal("default-empty watch list polled a document")
	}
	things, _ := c.Collect(context.Background())
	if len(things) != 0 {
		t.Fatalf("unwatched things = %+v", things)
	}
	if err := c.SetOptions(json.RawMessage(`{"documents":["https://example.feishu.cn/docx/doc_example"]}`)); err != nil {
		t.Fatal(err)
	}
	if !c.pollDocuments(context.Background()) {
		t.Fatal("mention was not collected")
	}
	things, _ = c.Collect(context.Background())
	if len(things) != 1 || strings.Contains(things[0].Thread, "comment_solved") {
		t.Fatalf("resolved or unexpected comments = %+v", things)
	}
}

func TestUnreadableDocumentIsProbedAgainLater(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("LARKCLI_FAKE_LOG", logPath)
	c := docTestConnector(t, "comments_unreadable")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	if err := c.SetOptions(json.RawMessage(`{"documents":["https://example.feishu.cn/docx/doc_example"]}`)); err != nil {
		t.Fatal(err)
	}
	count := func(what string) int {
		log, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(log), what)
	}
	if c.pollDocuments(context.Background()) {
		t.Fatal("unreadable document produced an item")
	}
	st := c.Status(context.Background())
	if st.State != connectorwire.StateOK || !strings.Contains(st.Error, "Example design document") {
		t.Fatalf("settings warning = %+v", st)
	}
	// Within the retry interval the document is not probed again.
	now = now.Add(documentUnreadableRetry / 2)
	if c.pollDocuments(context.Background()) {
		t.Fatal("unreadable document produced an item on retry")
	}
	if got := count("+list-comments"); got != 1 {
		t.Fatalf("unreadable document was retried %d times before the interval", got)
	}
	if got := count("+inspect"); got != 1 {
		t.Fatalf("unreadable document metadata was retried %d times before the interval", got)
	}
	// The owner shares it again; after the interval it is read and the warning goes.
	t.Setenv("LARKCLI_FAKE_PHASE", "reply")
	now = now.Add(documentUnreadableRetry)
	if !c.pollDocuments(context.Background()) {
		t.Fatal("a readable document again produced nothing")
	}
	if st := c.Status(context.Background()); st.Error != "" {
		t.Fatalf("warning kept after the document became readable: %+v", st)
	}
}

func TestNewLoginProbesUnreadableDocumentsAtOnce(t *testing.T) {
	c := docTestConnector(t, "comments_unreadable")
	if err := c.SetOptions(json.RawMessage(`{"documents":["https://example.feishu.cn/docx/doc_example"]}`)); err != nil {
		t.Fatal(err)
	}
	c.pollDocuments(context.Background())
	link := "https://example.feishu.cn/docx/doc_example"
	c.docs.mu.Lock()
	skipped := c.docs.skipUnreadable(c.docs.docs[link], c.currentTime())
	c.docs.mu.Unlock()
	if !skipped {
		t.Fatal("document not marked unreadable")
	}
	c.docs.setLogin("another login")
	c.docs.mu.Lock()
	skipped = c.docs.skipUnreadable(c.docs.docs[link], c.currentTime())
	c.docs.mu.Unlock()
	if skipped {
		t.Fatal("a new login did not make the document due for a probe")
	}
}

func TestMissingCommentScopeIsTheConnectorsStateNotTheDocuments(t *testing.T) {
	c := docTestConnector(t, "comments_missing_scope")
	links := `["https://example.feishu.cn/docx/doc_example","https://example.feishu.cn/docx/doc_other"]`
	if err := c.SetOptions(json.RawMessage(`{"documents":` + links + `}`)); err != nil {
		t.Fatal(err)
	}
	if c.pollDocuments(context.Background()) {
		t.Fatal("missing scope produced an item")
	}
	c.docs.mu.Lock()
	for link, doc := range c.docs.docs {
		if doc.Unreadable {
			t.Errorf("%s marked unreadable for a missing scope", link)
		}
	}
	c.docs.mu.Unlock()
	st := c.Status(context.Background())
	if !slices.Contains(st.MissingScopes, "docs:document.comment:read") {
		t.Fatalf("status does not name the missing scope: %+v", st)
	}
	// The scope is granted: the next round reads the documents with no wait and the state clears.
	t.Setenv("LARKCLI_FAKE_PHASE", "reply")
	if !c.pollDocuments(context.Background()) {
		t.Fatal("documents not read after the scope was granted")
	}
	if st := c.Status(context.Background()); len(st.MissingScopes) != 0 {
		t.Fatalf("missing scope kept after it was granted: %+v", st)
	}
}

func TestDocumentCommentRateLimitBackoff(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("LARKCLI_FAKE_LOG", logPath)
	c := docTestConnector(t, "comments_rate_limited")
	if err := c.SetOptions(json.RawMessage(`{"documents":["https://example.feishu.cn/docx/doc_example"]}`)); err != nil {
		t.Fatal(err)
	}
	c.pollDocuments(context.Background())
	c.pollDocuments(context.Background())
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(log), "+list-comments"); got != 1 {
		t.Fatalf("rate-limited document was retried immediately %d times: %s", got, log)
	}
}

func TestWikiSpaceExpansionIsBoundedAndOptionsRoundTrip(t *testing.T) {
	c := docTestConnector(t, "success")
	if err := c.SetOptions(json.RawMessage(`{"documents":[{"url":"https://example.feishu.cn/docx/doc_example"}],"wiki_spaces":["Example space"]}`)); err != nil {
		t.Fatal(err)
	}
	var options DocumentOptions
	if err := json.Unmarshal(c.Options(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Documents) != 1 || len(options.WikiSpaces) != 1 || options.WikiSpaces[0] != "Example space" {
		t.Fatalf("options round trip = %+v", options)
	}
	if !c.pollDocuments(context.Background()) {
		t.Fatal("wiki document was not collected")
	}
	things, _ := c.Collect(context.Background())
	if len(things) != 2 {
		t.Fatalf("wiki expansion = %+v", things)
	}
	for _, thing := range things {
		if thing.Kind != "doc" || thing.Title == "" {
			t.Fatalf("bad wiki thing = %+v", thing)
		}
	}
}

func TestOwnedDocumentKeepsEveryCommentWhenAsked(t *testing.T) {
	link := "https://example.feishu.cn/docx/doc_example"
	for _, tc := range []struct {
		name, options, owner string
		want                 bool
	}{
		{"option off", `{"documents":["` + link + `"]}`, "ou_example_user", false},
		{"someone else's document", `{"documents":["` + link + `"],"owned_all_comments":true}`, "ou_example_other", false},
		{"my document", `{"documents":["` + link + `"],"owned_all_comments":true}`, "ou_example_user", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LARKCLI_FAKE_OWNER", tc.owner)
			c := docTestConnector(t, "comments_owned")
			if err := c.SetOptions(json.RawMessage(tc.options)); err != nil {
				t.Fatal(err)
			}
			if got := c.pollDocuments(context.Background()); got != tc.want {
				t.Fatalf("an unmentioned comment kept = %v, want %v", got, tc.want)
			}
			if !tc.want {
				return
			}
			things, _ := c.Collect(context.Background())
			if len(things) != 1 || !strings.Contains(things[0].Preview, "start next week") {
				t.Fatalf("things = %+v", things)
			}
		})
	}
}

func TestOwnedAllCommentsRoundTripsThroughOptions(t *testing.T) {
	c := docTestConnector(t, "comments")
	if err := c.SetOptions(json.RawMessage(`{"documents":[],"owned_all_comments":true}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c.Options()), `"owned_all_comments":true`) {
		t.Fatalf("options = %s", c.Options())
	}
}

func TestOwnedAllCommentsCoversRecentOwnDocumentsWithoutAWatchList(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fake.log")
	t.Setenv("LARKCLI_FAKE_LOG", logPath)
	t.Setenv("LARKCLI_FAKE_OWNED", "1")
	c := docTestConnector(t, "comments_owned")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	if err := c.SetOptions(json.RawMessage(`{"documents":[],"owned_all_comments":true}`)); err != nil {
		t.Fatal(err)
	}
	if !c.pollDocuments(context.Background()) {
		t.Fatal("a comment on my recently edited document did not come in")
	}
	things, _ := c.Collect(context.Background())
	if len(things) != 1 || things[0].Title != "Example launch plan" || things[0].Tier != "" {
		t.Fatalf("things = %+v (want the owned document's comment, left to triage)", things)
	}
	count := func(what string) int {
		log, _ := os.ReadFile(logPath)
		return strings.Count(string(log), what)
	}
	if got := count("--doc-types doc,docx,sheet,bitable,file,slides,wiki"); got != 1 {
		t.Fatalf("owned documents listed without the commentable types (%d)", got)
	}
	if got := count("doc_old"); got != 0 {
		t.Fatalf("a document untouched for months was read %d times", got)
	}
	// The next round, two minutes on: no new listing and no second read of the same document yet.
	now = now.Add(2 * time.Minute)
	c.pollDocuments(context.Background())
	if got := count("+search"); got != 1 {
		t.Fatalf("owned documents listed %d times within the refresh interval", got)
	}
	if got := count("+list-comments"); got != 1 {
		t.Fatalf("the owned document was read %d times within its poll interval", got)
	}
	// Turned off: the owned documents go.
	if err := c.SetOptions(json.RawMessage(`{"documents":[]}`)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(ownedPollEvery)
	c.pollDocuments(context.Background())
	if got := count("+list-comments"); got != 1 {
		t.Fatalf("an owned document was read after the option was turned off")
	}
}

func TestTriageRulesRoundTripAndReachTheItems(t *testing.T) {
	c := docTestConnector(t, "comments_owned")
	t.Setenv("LARKCLI_FAKE_OWNER", "ou_example_user")
	rules := "Build and alert bots are FYI unless they name me."
	opts, _ := json.Marshal(map[string]any{"documents": []string{"https://example.feishu.cn/docx/doc_example"}, "owned_all_comments": true, "triage_rules": " " + rules + " "})
	if err := c.SetOptions(opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c.Options()), rules) {
		t.Fatalf("options = %s", c.Options())
	}
	c.pollDocuments(context.Background())
	things, _ := c.Collect(context.Background())
	if len(things) == 0 || things[0].Rules != rules {
		t.Fatalf("things = %+v", things)
	}
	long, _ := json.Marshal(map[string]any{"triage_rules": strings.Repeat("x", maxTriageRules+1)})
	if err := c.SetOptions(long); err == nil {
		t.Fatal("over-long rules accepted")
	}
}
