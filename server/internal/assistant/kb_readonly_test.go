package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

func readonlyService(t *testing.T, dir string) *Service {
	t.Helper()
	f := newFakeHub()
	f.hello.KB = &KBSettings{ReadOnly: true}
	return kbServiceOn(t, f, newKBStore(), dir, nil)
}

func readonlyArchiveSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			out[path] = string(b) + info.ModTime().String()
		} else {
			out[path] = "directory"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKBReadOnlyAbsentArchiveAndWriteRefusals(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	s := readonlyService(t, dir)
	for _, path := range []string{"/api/v1/kb/projects", "/api/v1/kb-search?q=example", "/api/v1/recordings"} {
		if strings.HasPrefix(path, "/api/v1/recordings") && !s.recordingOn() {
			continue // the recording module's route, absent without it
		}
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), "recording_deleted") {
			t.Fatal(r.Body)
		}
	}
	for _, req := range []struct{ method, path string }{
		{"PUT", "/api/v1/kb/projects/example"},
		{"PUT", "/api/v1/recordings/example"},
		{"DELETE", "/api/v1/recordings/example"},
		{"DELETE", "/api/v1/kb/projects/example"},
		{"POST", "/api/v1/kb-trash/example-1/restore"},
	} {
		if strings.HasPrefix(req.path, "/api/v1/recordings") && !s.recordingOn() {
			continue
		}
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest(req.method, req.path, strings.NewReader(`{}`)))
		if r.Code != 403 || !strings.Contains(r.Body.String(), "kb_read_only") {
			t.Fatalf("%s %s: %d %s", req.method, req.path, r.Code, r.Body)
		}
	}
	ctx := context.Background()
	for _, method := range []string{assistantwire.MethodKBUpdate, assistantwire.MethodKBExtensionUpdate,
		assistantwire.MethodKBPostsPut, assistantwire.MethodKBPostsComments, assistantwire.MethodKBPostsPending,
		assistantwire.MethodKBArchivedChats, assistantwire.MethodKBForget} {
		body := `{"kind":"projects","id":"example"}`
		if method == assistantwire.MethodKBExtensionUpdate {
			body = `{"extension":"feed","params":{"kind":"projects","id":"example"}}`
		}
		if method == assistantwire.MethodKBForget {
			body = `{"source":"feed"}`
		}
		_, handled, err := s.kbRequest(ctx, method, json.RawMessage(body))
		if !handled || err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("%s: handled=%v err=%v", method, handled, err)
		}
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e.Model != nil || e.ModelFor != nil || e.Index != nil || e.Unindex != nil {
		t.Fatal("read-only engine has processing callbacks")
	}
	s.kbAgentTurn(ctx, assistantwire.KBAgentTurn{Line: "example"})
	kbRoundNow(ctx, s, e)
	s.kbConnectors(ctx, e)
	s.kbCollectItems(ctx, e, false)
	if _, _, _, err := (busArchive{s: s}).Changes(ctx, "", 1); !errors.Is(err, kb.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := (busArchiveMedia{busArchive{s: s}}).ArchiveMedia(ctx, ""); !errors.Is(err, kb.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := (busItems{s: s}).Items(ctx); !errors.Is(err, kb.ErrReadOnly) {
		t.Fatal(err)
	}
	if s.kbWaiting() {
		t.Fatal("read-only mode queued an agent turn")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("created absent archive: %v", err)
	}
}

func TestKBReadOnlyExistingArchiveRemainsUnchanged(t *testing.T) {
	dir := t.TempDir()
	st, err := kb.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutChat(&kb.Chat{ID: "chat", Name: "Example chat", Source: "feed", Kind: "group"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSummary(kb.KindChat, "chat", "Example archive"); err != nil {
		t.Fatal(err)
	}
	engine := &kb.Engine{Store: st}
	if _, err := engine.Apply(kb.KindProject, "example", kb.Update{Summary: "Example archive"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(st.Path(kb.KindChat, "chat", "messages"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		st.Path(kb.KindChat, "chat", "messages", ".zone"):         "UTC",
		st.Path(kb.KindChat, "chat", "messages", "2026-11-01.md"): "- 23:30 Example message\n",
		st.Path(kb.KindProject, "example", "log.md"):              "# Timeline · UTC\n\n- 2026-11-01 23:30 Example outcome\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := readonlyArchiveSnapshot(t, dir)
	s := readonlyService(t, dir)
	for _, path := range []string{"/api/v1/kb/projects", "/api/v1/kb/projects/example", "/api/v1/kb/chats/chat",
		"/api/v1/kb/chats/chat/days/2026-11-01", "/api/v1/kb-search?q=Example", "/api/v1/kb-search?q=Example&results=1"} {
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 200 || !strings.Contains(strings.ToLower(r.Body.String()), "example") {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body)
		}
	}

	for method, raw := range map[string]string{
		assistantwire.MethodKBList:   `{"kind":"projects"}`,
		assistantwire.MethodKBGet:    `{"kind":"projects","id":"example"}`,
		assistantwire.MethodKBDay:    `{"id":"chat","day":"2026-11-01"}`,
		assistantwire.MethodKBSearch: `{"query":"Example"}`,
		assistantwire.MethodKBRecent: `{"since":"2000-01-01T00:00:00Z","kinds":["projects"]}`,
	} {
		out, handled, err := s.kbRequest(context.Background(), method, json.RawMessage(raw))
		if !handled || err != nil || out == nil {
			t.Fatalf("%s: %v %v", method, out, err)
		}
	}
	e, err := s.kbGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.Store.Since("chat", time.Time{}, 1000)
	e.Store.FirstAt("chat")
	s.kb.mu.Lock()
	s.kb.waiting = []assistantwire.KBAgentTurn{{Line: "must not be written"}}
	s.kb.mu.Unlock()
	s.kbTakeWaiting(e)
	kbRoundNow(context.Background(), s, e)
	if _, err := e.Apply(kb.KindProject, "example", kb.Update{Summary: "overwrite"}); !errors.Is(err, kb.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := e.DeleteRecording("example", time.Now()); !errors.Is(err, kb.ErrReadOnly) {
		t.Fatal(err)
	}
	if after := readonlyArchiveSnapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("reading or refused writes modified the archive")
	}
}

func TestKBReadOnlyCorruptArchiveIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects", "example")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "meta.json"), []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	s := readonlyService(t, dir)
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/kb/projects", nil))
	if r.Code != 503 || !strings.Contains(r.Body.String(), "kb_unavailable") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}

func TestKBReadOnlyManagerConfiguration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(file, []byte(`{"kb":{"enabled":true,"read_only":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := readOwnConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	h := c.over(Hello{})
	if h.KB == nil || h.KB.Off || !h.KB.ReadOnly {
		t.Fatalf("effective settings: %+v", h.KB)
	}
	f := newFakeHub()
	f.hello = h
	dir := filepath.Join(t.TempDir(), "absent")
	s := kbServiceOn(t, f, newKBStore(), dir, nil)
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/kb/projects", nil))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("created data: %v", err)
	}
}

func TestKBReadOnlyPreservesConfiguredMarkdownSearch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("Example existing notes"), 0600); err != nil {
		t.Fatal(err)
	}
	f := newFakeHub()
	f.hello.KB = &KBSettings{ReadOnly: true, ExtraDirs: []string{dir}}
	s := kbServiceOn(t, f, newKBStore(), filepath.Join(t.TempDir(), "absent"), nil)
	e, err := s.kbGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hits := e.Search("Example", true, 20)
	if len(hits) == 0 {
		t.Fatal("configured existing Markdown was not searched")
	}
}
