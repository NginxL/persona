package quota

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

func TestDoUserAgent(t *testing.T) {
	var got string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer gw.Close()
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"default", nil, modelhttp.UserAgent()},
		{"plugin", map[string]string{"user-agent": "plugin/1"}, "plugin/1"},
		{"empty plugin", map[string]string{"user-agent": ""}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Do(context.Background(), nil, gw.URL, "", Request{URL: "/usage", Headers: tc.headers}); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("User-Agent = %q, want %q", got, tc.want)
			}
		})
	}
}

// A plugin's request goes to the launcher's own gateway with the key put in, and the key never comes back.
func TestDoPutsTheKeyInAndTakesItOut(t *testing.T) {
	var got struct{ auth, path, query, body string }
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.auth, got.path, got.query, got.body = r.Header.Get("Authorization"), r.URL.Path, r.URL.RawQuery, string(b)
		w.Header().Set("Set-Cookie", "session=s1")
		w.Header().Set("X-Echo", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"key":"`+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+`","ok":true}`)
	}))
	defer gw.Close()
	key := "sk-example-123"
	resp, err := Do(context.Background(), nil, gw.URL+"/v1", key, Request{Method: "post", URL: "/me/usage?k=" + KeyPlaceholder + "&tz=Asia/Shanghai",
		Headers: map[string]string{"Authorization": "Bearer " + KeyPlaceholder, "Host": "elsewhere.example.com"}, Body: `{"key":"` + KeyPlaceholder + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer "+key || got.path != "/me/usage" || !strings.Contains(got.query, "k="+key) || got.body != `{"key":"`+key+`"}` {
		t.Fatalf("the gateway got %+v", got)
	}
	if strings.Contains(resp.Body, key) || strings.Contains(strings.Join(resp.Headers["X-Echo"], ""), key) {
		t.Fatalf("the key came back: %+v", resp)
	}
	if resp.Status != 200 || resp.Headers["Set-Cookie"][0] != "session=s1" || !strings.Contains(resp.Body, KeyPlaceholder) {
		t.Fatalf("%+v", resp)
	}
}

// A key the URL escapes (/, +, =, space, &) is taken out in its escaped form too: from the error of a request that
// failed and from a gateway that echoes the URL back. With no key the error is left as it is.
func TestDoTakesOutTheEscapedKey(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.URL.RawQuery)
		_, _ = io.WriteString(w, `{"url":"`+r.URL.String()+`"}`)
	}))
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	defer gw.Close()
	req := Request{URL: "/usage?k=" + KeyPlaceholder}
	for _, key := range []string{"sk-a/b+c=d", "sk a&b"} {
		forms := []string{key, url.QueryEscape(key), url.PathEscape(key)}
		resp, err := Do(context.Background(), nil, gw.URL+"/v1", key, req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Do(context.Background(), nil, down.URL+"/v1", key, req)
		if err == nil {
			t.Fatal("a closed gateway answered")
		}
		for _, f := range forms {
			if strings.Contains(err.Error(), f) || strings.Contains(resp.Body, f) || strings.Contains(strings.Join(resp.Headers["X-Echo"], ""), f) {
				t.Fatalf("key %q came back as %q: err=%v resp=%+v", key, f, err, resp)
			}
		}
		if !strings.Contains(err.Error(), KeyPlaceholder) || !strings.Contains(resp.Body, KeyPlaceholder) {
			t.Fatalf("no placeholder where the key was: err=%v resp=%+v", err, resp)
		}
	}
	_, err := Do(context.Background(), nil, down.URL+"/v1", "", Request{URL: "/usage"})
	if err == nil || strings.Contains(err.Error(), KeyPlaceholder) {
		t.Fatalf("with no key the error was rewritten: %v", err)
	}
}

// The key goes nowhere it did not already go: another host, scheme or a redirect off the gateway is refused.
func TestDoStaysOnTheGateway(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		t.Errorf("the key reached another host: %s", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hop" {
			http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
			return
		}
		if r.URL.Path == "/here" {
			http.Redirect(w, r, "/landed", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	defer gw.Close()
	h := map[string]string{"Authorization": "Bearer " + KeyPlaceholder}
	for _, u := range []string{other.URL + "/x", "//" + strings.TrimPrefix(other.URL, "http://") + "/x", "https://" + strings.TrimPrefix(gw.URL, "http://") + "/x",
		"http://user@" + strings.TrimPrefix(gw.URL, "http://") + "/x"} {
		if _, err := Do(context.Background(), nil, gw.URL, "k", Request{URL: u, Headers: h}); err == nil {
			t.Errorf("%s: sent", u)
		}
	}
	if _, err := Do(context.Background(), nil, gw.URL, "k", Request{URL: "/hop", Headers: h}); err == nil {
		t.Error("followed a redirect off the gateway")
	}
	r, err := Do(context.Background(), nil, gw.URL, "k", Request{URL: "/here"})
	if err != nil || r.Body != "/landed" {
		t.Fatal(r, err)
	}
	if r, err := Do(context.Background(), nil, gw.URL, "k", Request{URL: gw.URL + "/abs"}); err != nil || r.Body != "/abs" {
		t.Fatal(r, err)
	}
	if _, err := Do(context.Background(), nil, gw.URL, "k", Request{Method: "PATCH", URL: "/x"}); err == nil {
		t.Error("PATCH is not allowed")
	}
	if _, err := Do(context.Background(), nil, "ftp://x", "k", Request{URL: "/x"}); err == nil {
		t.Error("an ftp gateway")
	}
}

func TestRoot(t *testing.T) {
	for in, want := range map[string]string{"https://gw.example.com/v1": "https://gw.example.com", "https://gw.example.com/api/v1/": "https://gw.example.com/api",
		"http://127.0.0.1:8080": "http://127.0.0.1:8080"} {
		if u, err := Root(in); err != nil || u.String() != want {
			t.Errorf("%s: %v %v", in, u, err)
		}
	}
}

// The key replaces the placeholder in the address path too, escaped as a path segment, for a path and for an absolute
// address; the gateway's echo of it is taken out again.
func TestDoPutsTheKeyInThePath(t *testing.T) {
	var got struct{ path, raw, query string }
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.raw, got.query = r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery
		_, _ = io.WriteString(w, r.URL.EscapedPath())
	}))
	defer gw.Close()
	for _, c := range []struct{ key, wantPath, wantRaw string }{
		{"sk-example-123", "/api/keys/sk-example-123/usage", "/api/keys/sk-example-123/usage"},
		{"sk-a/b c?d", "/api/keys/sk-a/b c?d/usage", "/api/keys/sk-a%2Fb%20c%3Fd/usage"},
	} {
		for _, address := range []string{"/keys/" + KeyPlaceholder + "/usage?tz=UTC", gw.URL + "/api/keys/" + KeyPlaceholder + "/usage?tz=UTC"} {
			resp, err := Do(context.Background(), nil, gw.URL+"/api/v1", c.key, Request{URL: address})
			if err != nil {
				t.Fatal(err)
			}
			if got.path != c.wantPath || got.raw != c.wantRaw || got.query != "tz=UTC" {
				t.Fatalf("key %q, address %q: the gateway got path %q (%q) and query %q", c.key, address, got.path, got.raw, got.query)
			}
			if strings.Contains(resp.Body, c.key) || strings.Contains(resp.Body, url.PathEscape(c.key)) || !strings.Contains(resp.Body, KeyPlaceholder) {
				t.Fatalf("key %q, address %q: the key came back or was not marked: %+v", c.key, address, resp)
			}
		}
	}
}

// A quota or usage read is Persona's background request at an allowed gateway, whatever source headers the extension
// sent; at a gateway outside the allowlist no source goes at all.
func TestDoMarksItsSourceAsBackground(t *testing.T) {
	var got http.Header
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer gw.Close()
	forged := map[string]string{"x-persona-role": "main", "X-Persona-Todo": "todo_forged", "User-Agent": ""}
	u, _ := url.Parse(gw.URL)
	t.Setenv(modelhttp.SourceHostsEnv, u.Hostname())
	if _, err := Do(context.Background(), nil, gw.URL, "", Request{URL: "/usage", Headers: forged}); err != nil {
		t.Fatal(err)
	}
	if got.Get(modelhttp.RoleHeader) != "background" || got.Get(modelhttp.TodoHeader) != "" || got.Get(modelhttp.SessionHeader) != "" {
		t.Fatalf("allowed gateway: headers %v", got)
	}
	t.Setenv(modelhttp.SourceHostsEnv, "")
	if _, err := Do(context.Background(), nil, gw.URL, "", Request{URL: "/usage", Headers: forged}); err != nil {
		t.Fatal(err)
	}
	for name := range got {
		if modelhttp.SourceHeader(name) {
			t.Fatalf("source sent outside the allowlist: %v", got)
		}
	}
}
