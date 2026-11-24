package assistant

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// Exercise the manager's real upload/detail/search routes and index callback;
// all storage and HTTP traffic belong to this synthetic fixture.
func TestRecordingFailedUploadHTTPRetainsAcceptedRevision(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	index := newKBStore()
	service := kbServiceOn(t, newFakeHub(), index, t.TempDir(), nil)
	engine := kbEng(t, service)
	server := httptest.NewServer(service)
	defer server.Close()
	const id = "11111111-1111-4111-8111-111111111111"
	upload := kb.RecordingUpload{Revision: 1, StartedAt: time.Now().UTC(), Device: "phone", Title: "Accepted sample", Segments: []kb.RecordingSegment{{Text: "accepted orchard", End: 1}}}
	request := func(method, path string, body any, want int) string {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s (%v)", method, path, resp.StatusCode, raw, err)
		}
		return string(raw)
	}
	path := "/api/v1/recordings/" + id
	request("PUT", path, upload, 200)
	temp := engine.Store.Path(kb.KindRecording, id, "meta.json.tmp")
	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	upload.Revision, upload.Title, upload.Segments[0].Text = 2, "Rejected sample", "rejected zeppelin"
	request("PUT", path, upload, 500)
	detail := request("GET", "/api/v1/kb/recordings/"+id, nil, 200)
	if !strings.Contains(detail, "accepted orchard") || strings.Contains(detail, "rejected zeppelin") || !strings.Contains(detail, `"revision":1`) {
		t.Fatalf("detail = %s", detail)
	}
	search := request("GET", "/api/v1/kb-search?q=rejected", nil, 200)
	if strings.Contains(search, id) {
		t.Fatalf("rejected search = %s", search)
	}
	states := request("GET", "/api/v1/recordings", nil, 200)
	if !strings.Contains(states, `"revision":1`) {
		t.Fatalf("states = %s", states)
	}
	index.mu.Lock()
	name := index.rows[kb.KindRecording+"/"+id]
	index.mu.Unlock()
	if name != "Accepted sample" {
		t.Fatalf("index published rejected metadata: %q", name)
	}
	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}
	request("PUT", path, upload, 200)
	detail = request("GET", "/api/v1/kb/recordings/"+id, nil, 200)
	if !strings.Contains(detail, "rejected zeppelin") || !strings.Contains(detail, `"revision":2`) {
		t.Fatalf("retry detail = %s", detail)
	}
}

// GET /api/v1/kb-search takes kind as kb_search does (#3862 phase 6: the voice search_knowledge finds meetings with
// kind=recordings): the lines, and the ranked results, of that kind only; an unknown kind is a 400.
func TestKBSearchKind(t *testing.T) {
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	e := kbEng(t, s)
	c := &kb.Chat{ID: kb.ChatID("lark", "f1"), Source: "lark", SourceID: "f1", Kind: "group", Name: "Orchard group", Created: time.Now()}
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.AppendMessages(c, []kb.Line{{At: time.Now(), Sender: "Ada", Text: "the orchard vendor"}}); err != nil {
		t.Fatal(err)
	}
	const id = "33333333-3333-4333-8333-333333333333"
	if _, err := e.PutRecording(id, kb.RecordingUpload{Revision: 1, StartedAt: time.Now().UTC(), Device: "phone", Kind: "meeting", Title: "Vendor review",
		Segments: []kb.RecordingSegment{{Text: "we keep the orchard vendor", End: 1}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	get := func(path string, want int) string {
		t.Helper()
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != want {
			t.Fatalf("%s: %d %s, want %d", path, r.Code, r.Body, want)
		}
		return r.Body.String()
	}
	var all, only struct{ Hits []kb.Hit }
	_ = json.Unmarshal([]byte(get("/api/v1/kb-search?q=orchard", 200)), &all)
	_ = json.Unmarshal([]byte(get("/api/v1/kb-search?q=orchard&kind=recordings", 200)), &only)
	kinds := func(hits []kb.Hit) map[string]bool {
		out := map[string]bool{}
		for _, h := range hits {
			k, _, _ := strings.Cut(h.Path, "/")
			out[k] = true
		}
		return out
	}
	if k := kinds(all.Hits); !k["chats"] || !k[kb.KindRecording] {
		t.Fatalf("every kind: %+v", all.Hits)
	}
	if k := kinds(only.Hits); len(k) != 1 || !k[kb.KindRecording] {
		t.Fatalf("kind=recordings: %+v", only.Hits)
	}
	var ranked struct{ Results []kb.Result }
	_ = json.Unmarshal([]byte(get("/api/v1/kb-search?q=orchard&kind=recordings&results=1", 200)), &ranked)
	if len(ranked.Results) != 1 || ranked.Results[0].Entry.Kind != kb.KindRecording || ranked.Results[0].Entry.ID != id {
		t.Fatalf("results of kind=recordings: %+v", ranked.Results)
	}
	if body := get("/api/v1/kb-search?q=orchard&kind=nope", 400); !strings.Contains(body, "recordings") {
		t.Fatalf("an unknown kind: %s", body)
	}
}

// A recording the knowledge base holds stays there (#13501): a later upload naming a track is refused with
// recording_in_kb instead of writing a second copy into the track.
func TestRecordingInKBRefusesSpace(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	service := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	engine := kbEng(t, service)
	server := httptest.NewServer(service)
	defer server.Close()
	const id = "22222222-2222-4222-8222-222222222222"
	upload := kb.RecordingUpload{Revision: 1, StartedAt: time.Now().UTC(), Device: "phone", Title: "Kept sample", Segments: []kb.RecordingSegment{{Text: "kept", End: 1}}}
	put := func(u kb.RecordingUpload) (int, string) {
		t.Helper()
		data, err := json.Marshal(u)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("PUT", server.URL+"/api/v1/recordings/"+id, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := put(upload); code != 200 {
		t.Fatalf("first upload: %d %s", code, body)
	}
	upload.Revision, upload.Space = 2, "work"
	code, body := put(upload)
	if code != http.StatusConflict || !strings.Contains(body, codeRecordingInKB) {
		t.Fatalf("upload naming a space: %d %s", code, body)
	}
	if r, ok := engine.Store.Recording(id); !ok || r.Revision != 1 {
		t.Fatalf("knowledge base copy changed: %+v %v", r, ok)
	}
}
