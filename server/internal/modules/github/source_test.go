package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

func TestMissingScopes(t *testing.T) {
	for _, c := range []struct {
		granted []string
		want    []string
	}{
		{[]string{"gist", "read:org", "repo", "workflow"}, nil}, // gh's default
		{[]string{"repo"}, nil},
		{[]string{"public_repo", "notifications"}, []string{"repo"}},
		{[]string{"read:org"}, []string{"repo", "notifications"}},
		{nil, []string{"repo", "notifications"}},
	} {
		var got []string
		for _, m := range MissingScopes(c.granted) {
			got = append(got, m.Scope)
			if len(m.Affects) == 0 {
				t.Errorf("%v: %s affects nothing", c.granted, m.Scope)
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: missing %v, want %v", c.granted, got, c.want)
		}
	}
	if m := MissingScopes([]string{"public_repo"}); m[0].Affects[2] != "评论和 review 只能发到公开仓库" {
		t.Errorf("public_repo: %v", m[0].Affects)
	}
}

// A computer's gh login as GitHub sees it: who, what it may do, what is missing; and the machine's own failures.
func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer full":
			w.Header().Set("X-OAuth-Scopes", "gist, read:org, repo, workflow")
		case "Bearer narrow":
			w.Header().Set("X-OAuth-Scopes", "read:org")
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"example-user"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{}, "", nil)
	c.api = srv.URL
	tok := func(t string, err error) TokenSource {
		return func(context.Context, string) (string, error) { return t, err }
	}
	ctx := context.Background()
	if m := c.Probe(ctx, tok("full", nil)); m.State != "signed_in" || m.Account != "example-user" || len(m.Scopes) != 4 || m.Missing != nil {
		t.Errorf("full: %+v", m)
	}
	if m := c.Probe(ctx, tok("narrow", nil)); m.State != "signed_in" || len(m.Missing) != 2 {
		t.Errorf("narrow: %+v", m)
	}
	if m := c.Probe(ctx, tok("stale", nil)); m.State != "not_signed_in" {
		t.Errorf("stale: %+v", m)
	}
	for err, want := range map[error]string{ErrOffline: "offline", ErrNoGh: "no_gh", ErrNodeTooOld: "too_old", ErrNotSignedIn: "not_signed_in"} {
		if m := c.Probe(ctx, tok("", err)); m.State != want {
			t.Errorf("%v: %+v", err, m)
		}
	}
}

// Choosing a computer forgets the device flow's token; the scopes of the token in use are read at each check.
func TestChoosingASource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo")
		if r.Header.Get("Authorization") == "Bearer from-mac" {
			w.Header().Set("X-OAuth-Scopes", "public_repo")
		}
		_, _ = w.Write([]byte(`{"login":"example-user"}`))
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	sec := &memSecrets{data: []byte(`{"client_id":"Ov23liExample","token":"gho_device"}`)}
	c := New(Config{}, "", nil)
	c.api = srv.URL
	c.UseSecrets(sec)
	c.check(ctx)
	if scopes, known := c.Scopes(); c.Source() != SourceDevice || !known || len(scopes) != 1 || c.Status(ctx).Via != "设备码登录" {
		t.Fatalf("device: %s %v %+v", c.Source(), scopes, c.Status(ctx))
	}
	c.UseMachine(ctx, "mac", "示例 Mac", func(context.Context, string) (string, error) { return "from-mac", nil })
	c.check(ctx)
	if st := c.Status(ctx); c.Source() != SourceMachine || c.MachineID() != "mac" || st.Via != "来自 示例 Mac 的 gh 登录" || st.State != connectorwire.StateOK {
		t.Fatalf("machine: %+v", st)
	}
	if scopes, _ := c.Scopes(); len(MissingScopes(scopes)) == 0 {
		t.Fatalf("public_repo only and nothing missing: %v", scopes)
	}
	if strings.Contains(string(sec.data), "token") {
		t.Fatalf("the device token stayed: %s", sec.data)
	}
	c.UseDevice()
	c.check(ctx)
	if st := c.Status(ctx); st.State != connectorwire.StateNotSignedIn || st.Error != notSignedIn {
		t.Fatalf("device without a token: %+v", st)
	}
}
