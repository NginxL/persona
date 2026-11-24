package extmgr

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A relayed call (#12904) reaches the extension's socket with the token put on it as the gate says, the path, query,
// passed headers and body as the node sent them; the caller's own Authorization never; and the answer comes back whole.
func TestRelayToSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "gr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "x.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	type seen struct{ method, path, query, auth, accept, body string }
	got := make(chan seen, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Accept"), string(b)}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "no=1")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	auth := &proto.GateAuth{Header: "Authorization", Value: "Bearer ${T}"}
	ans, err := relayToSocket(context.Background(), sock, "quota-x", auth, map[string]string{"T": "tok"}, proto.GateRelayParams{
		Command: "quota-x", Method: "POST", Path: "/accounts", Query: "fresh=1", Body: []byte(`{"a":1}`),
		Header: map[string][]string{"Accept": {"application/json"}, "Authorization": {"Bearer theirs"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := <-got
	if s != (seen{"POST", "/accounts", "fresh=1", "Bearer tok", "application/json", `{"a":1}`}) {
		t.Errorf("the extension saw %+v", s)
	}
	if ans.Status != http.StatusTeapot || string(ans.Body) != `{"ok":true}` || ans.Header["Content-Type"][0] != "application/json" {
		t.Errorf("answer %+v", ans)
	}
	if _, ok := ans.Header["Set-Cookie"]; ok {
		t.Error("a header outside the few passed back came back")
	}

	if _, err := relayToSocket(context.Background(), filepath.Join(dir, "gone.sock"), "quota-x", auth, nil,
		proto.GateRelayParams{Method: "GET", Path: "/accounts"}); err == nil {
		t.Error("no socket: no error")
	}
}
