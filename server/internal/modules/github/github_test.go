package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

const (
	oldToken = "gho_example000000000000000000000000000001"
	newToken = "gho_example000000000000000000000000000002"
)

// fakeGitHub answers /user for the tokens in valid, and counts requests.
func fakeGitHub(t *testing.T, valid ...string) (*httptest.Server, *int) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		n++
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		for _, v := range valid {
			if tok == v && r.URL.Path == "/user" {
				_, _ = w.Write([]byte(`{"login":"alice"}`))
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials ` + tok + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// machine hands out tokens in turn (the last one again and again), or err.
type machine struct {
	tokens  []string
	err     error
	fetched int
}

func (m *machine) fetch(context.Context, string) (string, error) {
	m.fetched++
	if m.err != nil {
		return "", m.err
	}
	tok := m.tokens[0]
	if len(m.tokens) > 1 {
		m.tokens = m.tokens[1:]
	}
	return tok, nil
}

func newTest(t *testing.T, m *machine, valid ...string) *Connector {
	srv, _ := fakeGitHub(t, valid...)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", m.fetch)
	c.api = srv.URL
	return c
}

func TestSignedInThroughTheMachine(t *testing.T) {
	m := &machine{tokens: []string{oldToken}}
	c := newTest(t, m, oldToken)
	c.check(context.Background())
	c.check(context.Background())
	st := c.Status(context.Background())
	if st.State != connectorwire.StateOK || st.Account != "alice" || st.Via != "来自 Mac 的 gh 登录" || st.LastSync.IsZero() {
		t.Fatalf("%+v", st)
	}
	if m.fetched != 1 {
		t.Fatalf("the token was fetched %d times, not kept in memory", m.fetched)
	}
	if got := c.Manifest().Hosts; len(got) != 1 || got[0] != "github.com" {
		t.Fatalf("hosts %v", got)
	}
}

// A refused token is fetched again once (the machine may have signed in again); refused again, the login has expired.
func TestRefusedTokenIsFetchedAgainOnce(t *testing.T) {
	m := &machine{tokens: []string{oldToken, newToken}}
	c := newTest(t, m, newToken)
	c.check(context.Background())
	if st := c.Status(context.Background()); st.State != connectorwire.StateOK || m.fetched != 2 {
		t.Fatalf("%+v after %d fetches", st, m.fetched)
	}

	m = &machine{tokens: []string{oldToken}}
	c = newTest(t, m)
	c.check(context.Background())
	st := c.Status(context.Background())
	if st.State != connectorwire.StateNotSignedIn || !strings.Contains(st.Error, "Mac 上的 gh 登录失效了") || m.fetched != 2 {
		t.Fatalf("%+v after %d fetches", st, m.fetched)
	}
}

// A machine that went offline leaves the token in memory in use; with none in memory, the status says so.
func TestOfflineMachine(t *testing.T) {
	m := &machine{tokens: []string{oldToken}}
	c := newTest(t, m, oldToken)
	c.check(context.Background())
	m.err = ErrOffline
	c.check(context.Background())
	if st := c.Status(context.Background()); st.State != connectorwire.StateOK {
		t.Fatalf("offline with a token in memory: %+v", st)
	}

	c = newTest(t, &machine{err: ErrOffline}, oldToken)
	c.check(context.Background())
	if st := c.Status(context.Background()); st.State != connectorwire.StateNotSignedIn || st.Error != "GitHub 未授权（Mac 离线）：它连上后会自动再读" {
		t.Fatalf("%+v", st)
	}
	c.fetch = (&machine{tokens: []string{oldToken}}).fetch
	c.MachineOnline("other")
	if len(c.poke) != 0 {
		t.Fatal("another machine poked it")
	}
	c.MachineOnline("mac")
	if len(c.poke) != 1 {
		t.Fatal("its machine connecting did not poke it")
	}
}

func TestEveryFailureSaysWhatToDo(t *testing.T) {
	for err, want := range map[error]string{
		ErrNoMachine:   "不是已有的电脑",
		ErrNodeTooOld:  "Mac 的 personad 太旧",
		ErrNoGh:        "Mac 上没有 gh",
		ErrNotSignedIn: "gh auth login --insecure-storage",
	} {
		c := newTest(t, &machine{err: err})
		c.check(context.Background())
		if st := c.Status(context.Background()); st.State == connectorwire.StateOK || !strings.Contains(st.Error, want) {
			t.Errorf("%v: %+v", err, st)
		}
	}
}

// Tokens never get into an error or a status, even when GitHub echoes one back.
func TestTokensNeverShow(t *testing.T) {
	m := &machine{tokens: []string{oldToken}}
	c := newTest(t, m)
	_, err := c.Get(context.Background(), "/repos/example-org/example-repo", nil)
	if err == nil || strings.Contains(err.Error(), oldToken) {
		t.Fatalf("%v", err)
	}
	c.check(context.Background())
	if st := c.Status(context.Background()); strings.Contains(st.Error, "gho_") {
		t.Fatalf("%+v", st)
	}
	for _, s := range []string{oldToken, "ghp_abcdefghijklmnopqrstuvwxyz", "github_pat_11ABCDEFG0123456789_abcdef"} {
		if got := Scrub("token " + s + " here"); got != "token [token] here" {
			t.Errorf("Scrub: %q", got)
		}
	}
}
