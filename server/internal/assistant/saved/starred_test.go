package saved

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type starTestStore struct {
	mu       sync.Mutex
	json     []byte
	revision int64
	conflict bool
}

func (s *starTestStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != StarredKey || s.json == nil {
		return false, nil
	}
	return true, json.Unmarshal(s.json, out)
}

func (s *starTestStore) CompareSetting(_ context.Context, key string, rev int64, next any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != StarredKey || rev != s.revision || s.conflict {
		return false, nil
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	s.json, s.revision = raw, rev+1
	return true, nil
}

func newStarTestBook(store any) (*starBook, *http.ServeMux, *[]string) {
	mux := http.NewServeMux()
	var events []string
	b := &starBook{h: Host{
		Store:   func() any { return store },
		Event:   func(_ context.Context, e map[string]any) { events = append(events, e["type"].(string)) },
		Handle:  func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, fn) },
		Full:    func(r *http.Request) bool { return r.Header.Get("Role") == "full" },
		BodyErr: func(w http.ResponseWriter, _ error, _ error) { w.WriteHeader(413) },
	}}
	b.routes()
	return b, mux, &events
}

func doStar(t *testing.T, mux *http.ServeMux, method, suffix, role, body string, status int, out any) {
	t.Helper()
	r := httptest.NewRequest(method, starPath+suffix, strings.NewReader(body))
	r.Header.Set("Role", role)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status %d, want %d: %s", method, suffix, w.Code, status, w.Body)
	}
	if out != nil {
		if err := json.NewDecoder(w.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: %v", method, suffix, err)
		}
	}
}

func testStar(item string) StarInput {
	return StarInput{
		StarSource: StarSource{Machine: "laptop", Session: "review", Item: item, Conversation: "agent"},
		Role:       "assistant", Title: "Release review", Speaker: "Reviewer", Text: "Save the decision",
	}
}

func TestStarredSnapshotRoutes(t *testing.T) {
	store := &starTestStore{}
	_, mux, events := newStarTestBook(store)
	in := testStar("first")
	in.Text = strings.Repeat("🐈", StarTextMax+2)
	in.Omitted = 3
	raw, _ := json.Marshal(in)
	var posted struct{ Star Star }
	doStar(t, mux, "POST", "", "full", string(raw), 200, &posted)
	star := posted.Star
	if star.ID == "" || star.Omitted != 5 || len([]rune(star.Text)) != StarTextMax {
		t.Fatalf("truncation or identity: %q, %d, %d", star.ID, star.Omitted, len([]rune(star.Text)))
	}
	in.Text = "Edited text"
	raw, _ = json.Marshal(in)
	doStar(t, mux, "POST", "", "full", string(raw), 200, &posted)
	if posted.Star.ID != star.ID || posted.Star.Text != star.Text || !posted.Star.StarredAt.Equal(star.StarredAt) {
		t.Fatal("retry overwrote the saved snapshot")
	}
	in.Session, in.Item = "other", "first"
	raw, _ = json.Marshal(in)
	doStar(t, mux, "POST", "", "full", string(raw), 200, &posted)

	// A fresh book knows no transcript and reads the saved projection.
	_, fresh, _ := newStarTestBook(store)
	var list struct {
		Stars    []Star
		CanWrite bool `json:"can_write"`
	}
	doStar(t, fresh, "GET", "", "full", "", 200, &list)
	if len(list.Stars) != 2 || list.Stars[0].ID != posted.Star.ID || !list.CanWrite {
		t.Fatalf("unexpected collection: count %d, writable %v", len(list.Stars), list.CanWrite)
	}
	doStar(t, fresh, "GET", "?session=laptop%2Freview", "read", "", 200, &list)
	if len(list.Stars) != 1 || list.Stars[0].ID != star.ID || list.CanWrite {
		t.Fatal("conversation filter or permission failed")
	}
	var response map[string]json.RawMessage
	doStar(t, fresh, "GET", "?index=1", "full", "", 200, &response)
	if response["stars"] != nil || strings.Contains(string(response["refs"]), "text") ||
		!strings.Contains(string(response["refs"]), star.ID) {
		t.Fatal("the compact index leaked snapshot details or lost a reference")
	}

	var deleted struct{ Removed *Star }
	doStar(t, mux, "DELETE", "/"+star.ID, "full", "", 200, &deleted)
	if deleted.Removed == nil || deleted.Removed.ID != star.ID || deleted.Removed.Text != star.Text {
		t.Fatal("remove did not return the saved snapshot for Undo")
	}
	raw, _ = json.Marshal(deleted.Removed.StarInput)
	doStar(t, mux, "POST", "", "full", string(raw), 200, &posted)
	if posted.Star.ID == star.ID || posted.Star.Omitted != star.Omitted {
		t.Fatal("Undo must preserve omitted count and make a new occurrence")
	}
	doStar(t, mux, "DELETE", "/"+star.ID, "full", "", 200, &deleted)
	if deleted.Removed != nil || len(*events) != 4 {
		t.Fatalf("duplicated writes broadcast: %v", *events)
	}
	for _, event := range *events {
		if event != "assistant_starred" {
			t.Fatalf("wrong notification: %s", event)
		}
	}
}

func TestStarredValidationAndCapacity(t *testing.T) {
	store := &starTestStore{}
	b, mux, _ := newStarTestBook(store)
	doStar(t, mux, "POST", "", "read", "{}", 403, nil)
	doStar(t, mux, "DELETE", "/no", "read", "", 403, nil)
	for _, body := range []string{
		`{}`, `{"machine_id":"machine","session_id":"s","item_id":"line","conversation":"agent","role":"tool","text":"result"}`,
		`{"machine_id":"machine","session_id":"s","item_id":"line","conversation":"agent","role":"user","text":"\u0000 \t"}`,
		`{"machine_id":"machine","session_id":"..","item_id":"line","conversation":"agent","role":"user","text":"valid text"}`,
		`{}{}`,
	} {
		doStar(t, mux, "POST", "", "full", body, 400, nil)
	}
	doStar(t, mux, "POST", "", "full", strings.Repeat(" ", 64<<10+1), 413, nil)
	doStar(t, mux, "GET", "?session=machine", "full", "", 400, nil)
	_, noStore, _ := newStarTestBook(nil)
	doStar(t, noStore, "GET", "", "full", "", 503, nil)
	ctx := t.Context()
	doc := starCollection{Revision: 1, Stars: make([]Star, StarredMax)}
	for i := range doc.Stars {
		doc.Stars[i] = Star{StarInput: testStar(fmt.Sprintf("item-%d", i)), ID: fmt.Sprintf("star-%d", i)}
	}
	if ok, err := store.CompareSetting(ctx, StarredKey, 0, doc); err != nil || !ok {
		t.Fatalf("seed full collection: %v, %v", ok, err)
	}
	if _, err := b.add(ctx, testStar("extra")); err != errStarLimit {
		t.Fatalf("adding to a full collection: %v", err)
	}
	if got, err := b.add(ctx, doc.Stars[0].StarInput); err != nil || got.ID != doc.Stars[0].ID {
		t.Fatalf("retrying an existing star at capacity: %q, %v", got.ID, err)
	}
	store.conflict = true
	doStar(t, mux, "DELETE", "/star-0", "full", "", 409, nil)
}

func TestStarredConcurrentProcessEdits(t *testing.T) {
	store := &starTestStore{}
	one, _, _ := newStarTestBook(store)
	two, _, _ := newStarTestBook(store)
	ctx := t.Context()
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, b := range []*starBook{one, two} {
		wg.Go(func() {
			prefix := "a"
			if b == two {
				prefix = "b"
			}
			for i := range 20 {
				if _, err := b.add(ctx, testStar(fmt.Sprintf("%s%d", prefix, i))); err != nil {
					errors <- err
					break
				}
			}
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	got, err := loadStars(ctx, store)
	if err != nil || len(got.Stars) != 40 {
		t.Fatalf("concurrent writes lost a star: %d, %v", len(got.Stars), err)
	}
	for i, star := range got.Stars {
		if star.ID == "" || i > 0 && star.StarredAt.After(got.Stars[i-1].StarredAt) {
			t.Fatal("collection is not ordered newest-first")
		}
	}
}

func TestStarredDistinguishesReusedTranscriptPosition(t *testing.T) {
	store := &starTestStore{}
	_, mux, _ := newStarTestBook(store)
	positional := testStar("line12#0")
	post := func(input StarInput) Star {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var posted struct{ Star Star }
		doStar(t, mux, "POST", "", "full", string(raw), 200, &posted)
		return posted.Star
	}
	legacy := post(positional)
	positional.SourceFingerprint = strings.Repeat("a", 32)
	original := post(positional)
	positional.Text = "Different words at an earlier transcript position"
	positional.SourceFingerprint = strings.Repeat("b", 32)
	replacement := post(positional)
	if legacy.ID == original.ID || original.ID == replacement.ID || legacy.ID == replacement.ID {
		t.Fatal("a reconstructed or legacy source was mistaken for the same message")
	}
	if duplicate := post(positional); duplicate.ID != replacement.ID {
		t.Fatal("retrying the same fingerprint produced a duplicate")
	}

	var index struct{ Refs []starRef }
	doStar(t, mux, "GET", "?index=1", "full", "", 200, &index)
	if len(index.Refs) != 3 || index.Refs[0].SourceFingerprint != replacement.SourceFingerprint ||
		index.Refs[1].SourceFingerprint != original.SourceFingerprint || index.Refs[2].SourceFingerprint != "" {
		t.Fatalf("compacted index collapsed positional references: %+v", index.Refs)
	}
	doStar(t, mux, "DELETE", "/"+replacement.ID, "full", "", 200, nil)
	var list struct{ Stars []Star }
	doStar(t, mux, "GET", "", "full", "", 200, &list)
	if len(list.Stars) != 2 || list.Stars[0].ID != original.ID || list.Stars[1].ID != legacy.ID {
		t.Fatal("unstar removed another snapshot at the same position")
	}
}

func TestStarredKeepsConversationKindsIndependent(t *testing.T) {
	_, mux, _ := newStarTestBook(&starTestStore{})
	post := func(input StarInput) Star {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var response struct{ Star Star }
		doStar(t, mux, "POST", "", "full", string(raw), 200, &response)
		return response.Star
	}
	for _, item := range []string{"runtime-message#0", "line12#0"} {
		agent := testStar(item)
		if strings.HasPrefix(item, "line") {
			agent.SourceFingerprint = strings.Repeat("a", 32)
		}
		assistant := agent
		assistant.Conversation = "assistant"
		assistant.Text = "An independent snapshot in the assistant chat"
		a, b := post(agent), post(assistant)
		if a.ID == b.ID || b.Text != assistant.Text || b.Conversation != "assistant" {
			t.Fatal("equal item IDs in different conversation kinds were collapsed")
		}
		if again := post(assistant); again.ID != b.ID {
			t.Fatal("retry of the same assistant source created a second snapshot")
		}
		doStar(t, mux, "DELETE", "/"+b.ID, "full", "", 200, nil)
		if again := post(agent); again.ID != a.ID || again.Text != a.Text {
			t.Fatal("removing one conversation's snapshot also removed the other")
		}
	}
}
