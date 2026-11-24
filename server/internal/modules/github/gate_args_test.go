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

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
)

// signedIn is the connector with a login nothing has to check: Registry.Call asks the status before it gates a write.
type signedIn struct{ *Connector }

func (signedIn) Status(context.Context) connectorwire.Status {
	return connectorwire.Status{State: connectorwire.StateOK}
}

// This connector reads its arguments with encoding/json, which takes one of two names that differ only in capitals, or
// of one name given twice, by rules a map of the same text does not follow. Through the Registry, with the real
// connector and a fake GitHub: a call that gives a field more than once is refused before any rule is read and GitHub
// is not written to, whatever the rules are and whatever the repeat is (other capitals, the very same name, a null).
// A call that spells a field once in other capitals is read by a deny or a confirm as if it were spelled right, so it
// cannot get past either; an allow does not read it (the user is asked, and the card says where the comment would go).
// A call that gives each field once goes the way it always did.
func TestAGitHubCommentThatGivesAFieldMoreThanOnceIsNotLetThrough(t *testing.T) {
	const allowed, other = "example-org/example-repo", "other-org/other-repo"
	var mu sync.Mutex
	var seen []string // "METHOD path body" of every request GitHub got
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(b))
		mu.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":3000000001,"html_url":"https://github.com/` + allowed + `/issues/12#issuecomment-3000000001"}`))
			return
		}
		_, _ = w.Write([]byte(`{"title":"An issue"}`))
	}))
	t.Cleanup(srv.Close)

	onlyThatIssue := connector.Rule{Connector: "github", Action: "github_comment", Match: map[string]any{"repo": allowed, "number": float64(12)}, Decision: connector.Allow}
	denyOther := connector.Rule{Connector: "github", Action: "github_comment", Match: map[string]any{"repo": other}, Decision: connector.Deny}
	confirmOther := connector.Rule{Connector: "github", Action: "github_comment", Match: map[string]any{"repo": other}, Decision: connector.Confirm}
	allowAny := connector.Rule{Connector: "github", Action: "github_comment", Decision: connector.Allow}
	denyAll := connector.Rule{Connector: "github", Action: "*", Decision: connector.Deny}
	once := `{"repo":"` + allowed + `","number":12,"body":"ok"}`
	comment := "/repos/" + allowed + "/issues/12/comments {\"body\":\"ok\"}"
	otherComment := "/repos/" + other + "/issues/12/comments {\"body\":\"ok\"}"

	for _, tc := range []struct {
		name    string
		rules   []connector.Rule
		args    string
		answer  bool // what the user says, when asked
		asked   int
		writes  []string // what GitHub was asked to write, as "path body"
		refused bool
		field   string // what the refusal names, when there is no card and no rule said it
	}{
		{"an allow for that issue, called as it says", []connector.Rule{onlyThatIssue}, once, false, 0, []string{comment}, false, ""},
		{"the repository in other capitals as well", []connector.Rule{onlyThatIssue}, `{"repo":"` + allowed + `","number":12,"body":"ok","REPO":"` + other + `"}`, true, 0, nil, true, `"repo"`},
		{"the number in other capitals as well", []connector.Rule{onlyThatIssue}, `{"repo":"` + allowed + `","number":12,"body":"ok","Number":99}`, true, 0, nil, true, `"number"`},
		{"the repository given twice", []connector.Rule{onlyThatIssue}, `{"repo":"` + other + `","number":12,"body":"ok","repo":"` + allowed + `"}`, true, 0, nil, true, `"repo"`},
		{"the second repository is null", []connector.Rule{onlyThatIssue}, `{"repo":"` + allowed + `","number":12,"body":"ok","repo":null}`, true, 0, nil, true, `"repo"`},
		{"the number given twice, the second null", []connector.Rule{onlyThatIssue}, `{"repo":"` + allowed + `","number":12,"body":"ok","number":null}`, true, 0, nil, true, `"number"`},
		{"a deny of the other repository, then an allow, the repository twice", []connector.Rule{denyOther, allowAny}, `{"repo":"` + allowed + `","number":12,"body":"ok","REPO":"` + other + `"}`, true, 0, nil, true, `"repo"`},
		{"a confirm for the other repository, then a deny of everything, the repository twice", []connector.Rule{confirmOther, denyAll}, `{"repo":"` + allowed + `","number":12,"body":"ok","REPO":"` + other + `"}`, true, 0, nil, true, `"repo"`},
		{"no rule at all, the repository twice", nil, `{"repo":"` + allowed + `","number":12,"body":"ok","Repo":"` + allowed + `"}`, true, 0, nil, true, `"repo"`},
		{"a deny of the other repository, the repository in other capitals only", []connector.Rule{denyOther, allowAny}, `{"REPO":"` + other + `","number":12,"body":"ok"}`, true, 0, nil, true, ""},
		{"a confirm for the other repository, the repository in other capitals only, declined", []connector.Rule{confirmOther, denyAll}, `{"REPO":"` + other + `","number":12,"body":"ok"}`, false, 1, nil, true, ""},
		{"a confirm for the other repository, the repository in other capitals only, agreed to", []connector.Rule{confirmOther, denyAll}, `{"REPO":"` + other + `","number":12,"body":"ok"}`, true, 1, []string{otherComment}, false, ""},
		{"an allow for that issue, the repository in other capitals only: asked, declined", []connector.Rule{onlyThatIssue}, `{"Repo":"` + allowed + `","number":12,"body":"ok"}`, false, 1, nil, true, ""},
		{"a deny of the other repository then an allow, called as it says", []connector.Rule{denyOther, allowAny}, once, false, 0, []string{comment}, false, ""},
		{"a deny of the other repository, called for it", []connector.Rule{denyOther, allowAny}, `{"repo":"` + other + `","number":12,"body":"ok"}`, true, 0, nil, true, ""},
	} {
		mu.Lock()
		seen = nil
		mu.Unlock()
		gh := New(Config{Auth: AuthConfig{FromMachine: "mac"}, APIRoot: srv.URL}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
		reg := connector.NewRegistry(tc.rules)
		if err := reg.Add(signedIn{gh}); err != nil {
			t.Fatal(err)
		}
		var cards []string
		res := reg.Call(context.Background(), "github_comment", json.RawMessage(tc.args), func(_ context.Context, summary string, _ json.RawMessage) bool {
			cards = append(cards, summary)
			return tc.answer
		})
		if len(cards) != tc.asked {
			t.Errorf("%s: asked %d times, want %d", tc.name, len(cards), tc.asked)
		}
		if tc.asked > 0 && strings.Contains(tc.args, other) && !strings.Contains(cards[0], other+"#12") {
			t.Errorf("%s: the card does not say where the comment goes: %q", tc.name, cards[0])
		}
		if res.IsError != tc.refused {
			t.Errorf("%s: error %v (%+v), want %v", tc.name, res.IsError, res, tc.refused)
		}
		if tc.field != "" && (len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, tc.field) || !strings.Contains(res.Content[0].Text, "more than once")) {
			t.Errorf("%s: the refusal does not name %s: %+v", tc.name, tc.field, res)
		}
		mu.Lock()
		var writes []string
		for _, s := range seen {
			if !strings.HasPrefix(s, "GET ") {
				writes = append(writes, strings.TrimPrefix(s, "POST "))
			}
		}
		mu.Unlock()
		if strings.Join(writes, "\n") != strings.Join(tc.writes, "\n") {
			t.Errorf("%s: GitHub was written to with %q, want %q", tc.name, writes, tc.writes)
		}
	}
}
