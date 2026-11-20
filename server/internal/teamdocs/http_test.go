package teamdocs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentAPIRejectsMalformedRequests(t *testing.T) {
	const base = "/v1/projects/team/documents/brief/"
	tests := []struct {
		name, method, endpoint, body string
	}{
		{"missing expected", "POST", "revisions", `{"content":"new"}`},
		{"null expected", "POST", "revisions", `{"expected_revision":null,"content":"new"}`},
		{"unquoted number required", "POST", "revisions", `{"expected_revision":"0","content":"new"}`},
		{"negative expected", "POST", "revisions", `{"expected_revision":-1,"content":"new"}`},
		{"noninteger expected", "POST", "revisions", `{"expected_revision":1.5,"content":"new"}`},
		{"missing content", "POST", "revisions", `{"expected_revision":0}`},
		{"invalid utf8", "POST", "revisions", `{"expected_revision":0,"content":"` + "\xff" + `"}`},
		{"unpaired surrogate", "POST", "revisions", `{"expected_revision":0,"content":"\ud800"}`},
		{"unknown key", "POST", "approvals", `{"revision":1,"unknown":true}`},
		{"wrong case", "POST", "approvals", `{"Revision":1}`},
		{"duplicate key", "POST", "approvals", `{"revision":1,"revision":2}`},
		{"trailing object", "POST", "approvals", `{"revision":1} {}`},
		{"missing approval number", "POST", "approvals", `{}`},
		{"zero approval number", "POST", "approvals", `{"revision":0}`},
		{"pinned zero", "GET", "revisions/0", ""},
		{"pinned overflow", "GET", "revisions/9223372036854775808", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, base+tc.endpoint, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer fixture")
			w := httptest.NewRecorder()
			Handler(nil).ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
		})
	}
}

func TestDocumentAPIAuthorization(t *testing.T) {
	for _, headers := range [][]string{nil, {"Basic fixture"}, {"Bearer "}, {"Bearer first", "Bearer second"}} {
		r := httptest.NewRequest("GET", "/v1/projects/team/documents/brief/approved", nil)
		for _, h := range headers {
			r.Header.Add("Authorization", h)
		}
		w := httptest.NewRecorder()
		Handler(nil).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected authorization reply: %d", w.Code)
		}
	}
}
