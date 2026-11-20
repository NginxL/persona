package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// This fixture supplies the storage/transaction boundary, as a real module does.
// Only the portable handler and validation run in the package under test.
type memoryProvider struct {
	mu     sync.Mutex
	doc    Document
	writes int
	err    error
}

func (p *memoryProvider) Read(context.Context) (Document, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, _ := json.Marshal(p.doc)
	var copy Document
	_ = json.Unmarshal(raw, &copy)
	return copy, p.err
}

func (p *memoryProvider) Update(ctx context.Context, u Update) (Document, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	if p.err != nil {
		return Document{}, p.err
	}
	values, err := ApplyUpdate(p.doc, u)
	if err != nil {
		return Document{}, err
	}
	p.writes++
	p.doc.Values = values
	p.doc.Revision = fmt.Sprintf("revision-%d", p.writes+1)
	raw, _ := json.Marshal(p.doc)
	var copy Document
	_ = json.Unmarshal(raw, &copy)
	return copy, nil
}

func callHandler(h http.Handler, method, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "/module/preferences", strings.NewReader(body)))
	return w
}

func TestHTTPReadPatchResetAndConflict(t *testing.T) {
	p := &memoryProvider{doc: example(t)}
	h := NewHandler(func(*http.Request) (Provider, error) { return p, nil })
	w := callHandler(h, "GET", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	var read Document
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || read.ID != p.doc.ID || len(read.Groups[0].Fields) != 9 {
		t.Fatalf("read: %#v %v", read, err)
	}
	w = callHandler(h, "PATCH", `{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"compact":true,"label":"Custom"}}`)
	if w.Code != 200 || p.writes != 1 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || read.Revision != "revision-2" || string(read.Values["label"]) != `"Custom"` || len(read.Values) != 9 {
		t.Fatalf("save must return the complete canonical snapshot: %#v %v", read, err)
	}
	w = callHandler(h, "PATCH", `{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"label":"Lost update"}}`)
	if w.Code != 409 || p.writes != 1 || string(p.doc.Values["label"]) != `"Custom"` {
		t.Fatalf("stale write: %d %s", w.Code, w.Body.String())
	}
	w = callHandler(h, "PATCH", `{"schema_version":1,"id":"example-preferences","revision":"revision-2","reset":["label"]}`)
	if w.Code != 200 || string(p.doc.Values["label"]) != `"Preview"` || string(p.doc.Values["compact"]) != "true" {
		t.Fatalf("partial reset: %d %s", w.Code, w.Body.String())
	}
}

func TestHTTPRejectsMalformedAndInvalidUpdatesBeforeCommit(t *testing.T) {
	p := &memoryProvider{doc: example(t)}
	h := NewHandler(func(*http.Request) (Provider, error) { return p, nil })
	for _, c := range []struct {
		body string
		code int
	}{
		{"", 400}, {"null", 400}, {"{}", 400},
		{`{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"compact":true},"typo":true}`, 400},
		{`{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":null,"reset":["label"]}`, 400},
		{`{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"compact":true}} {}`, 400},
		{`{"schema_version":2,"id":"example-preferences","revision":"revision-1","changes":{"compact":true}}`, 400},
		{`{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"compact":true,"layout":"unknown"}}`, 422},
		{`{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"managed":false}}`, 422},
		{`{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"compact":null}}`, 422},
		{`{"schema_version":1,"id":"another-resource","revision":"revision-1","changes":{"compact":true}}`, 409},
		{strings.Repeat(" ", MaxBytes+1), 413},
	} {
		w := callHandler(h, "PATCH", c.body)
		if w.Code != c.code || p.writes != 0 || string(p.doc.Values["compact"]) != "false" {
			t.Fatalf("got %d, writes %d, body %.160s; request %.160s", w.Code, p.writes, w.Body.String(), c.body)
		}
	}
	w := callHandler(h, "PATCH", `{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"label":""}}`)
	var failure Error
	if json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Code != "configuration_invalid" || len(failure.FieldErrors) != 1 || failure.FieldErrors[0].ID != "label" || failure.FieldErrors[0].Code != "min_length" {
		t.Fatalf("field error was lost: %s", w.Body.String())
	}
}

func TestHTTPRevisionIsCheckedAtTheCommitBoundary(t *testing.T) {
	p := &memoryProvider{doc: example(t)}
	h := NewHandler(func(*http.Request) (Provider, error) { return p, nil })
	start := make(chan struct{})
	results := make(chan int, 2)
	for range 2 {
		go func() {
			<-start
			results <- callHandler(h, "PATCH", `{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"compact":true}}`).Code
		}()
	}
	close(start)
	a, b := <-results, <-results
	if (a != 200 || b != 409) && (a != 409 || b != 200) {
		t.Fatalf("concurrent writers got %d and %d", a, b)
	}
	if p.writes != 1 {
		t.Fatalf("commits: %d", p.writes)
	}
}

func TestHTTPRejectsNonContractUpdateKeysBeforeCallingProvider(t *testing.T) {
	p := &memoryProvider{doc: example(t), err: errors.New("provider update should not be called")}
	h := NewHandler(func(*http.Request) (Provider, error) { return p, nil })
	for _, tc := range []struct {
		key, value string
	}{
		{"Schema_Version", "1"},
		{"ID", `"example-preferences"`},
		{"Revision", `"revision-1"`},
		{"Changes", `{"compact":true}`},
		{"RESET", `["label"]`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			body := fmt.Sprintf(`{"schema_version":1,"id":"example-preferences","revision":"revision-1","changes":{"compact":true},%q:%s}`, tc.key, tc.value)
			w := callHandler(h, http.MethodPatch, body)
			var e Error
			if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Code != "configuration_bad_request" {
				t.Fatalf("got %d %s; want configuration_bad_request before calling Update", w.Code, w.Body)
			}
		})
	}
}

func TestHTTPAuthorizationAndFailureDoNotInventDefaults(t *testing.T) {
	p := &memoryProvider{doc: example(t)}
	h := NewHandler(func(*http.Request) (Provider, error) {
		return nil, &Error{Code: "configuration_forbidden"}
	})
	for _, method := range []string{"GET", "PATCH"} {
		w := callHandler(h, method, "")
		if w.Code != 403 || strings.Contains(w.Body.String(), "values") {
			t.Fatalf("authorization: %d %s", w.Code, w.Body.String())
		}
	}
	p.err = errors.New("private provider detail")
	h = NewHandler(func(*http.Request) (Provider, error) { return p, nil })
	w := callHandler(h, "GET", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "private provider detail") || strings.Contains(w.Body.String(), "values") {
		t.Fatalf("failure became defaults or leaked details: %d %s", w.Code, w.Body.String())
	}
	p.err = nil
	p.doc.Values["compact"] = []byte(`"bad"`)
	w = callHandler(h, "GET", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "values") {
		t.Fatalf("invalid snapshot published: %d %s", w.Code, w.Body.String())
	}
	w = callHandler(h, "DELETE", "")
	if w.Code != 405 || w.Header().Get("Allow") != "GET, PATCH" {
		t.Fatalf("method: %d %s", w.Code, w.Body.String())
	}
}

type languageWriter struct {
	*httptest.ResponseRecorder
	language i18n.Lang
}

func (w languageWriter) Lang() i18n.Lang { return w.language }

func TestHTTPErrorLocalization(t *testing.T) {
	seen := map[string]bool{}
	for _, lang := range []i18n.Lang{"en", "zh-Hans", "ja"} {
		w := languageWriter{httptest.NewRecorder(), lang}
		writeError(w, &Error{Code: "configuration_conflict"})
		var e Error
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != "configuration_conflict" || e.Message == "" || strings.Contains(e.Message, "configuration.") || seen[e.Message] {
			t.Fatalf("%s: %s, %v", lang, w.Body.String(), err)
		}
		seen[e.Message] = true
	}
}
