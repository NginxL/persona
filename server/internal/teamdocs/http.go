package teamdocs

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Handler exposes the project member API. Operator grants and revocations are local CLI operations.
func Handler(s *Store) http.Handler {
	const base = "/v1/projects/{project}/documents/{document}"
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+base+"/revisions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Expected *int64  `json:"expected_revision"`
			Content  *string `json:"content"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Expected == nil || *body.Expected < 0 || body.Content == nil {
			reply(w, 0, Revision{}, ErrInvalid)
			return
		}
		rev, err := s.Publish(r.Context(), bearer(r), r.PathValue("project"), r.PathValue("document"), *body.Expected, *body.Content)
		reply(w, http.StatusCreated, rev, err)
	})
	mux.HandleFunc("POST "+base+"/approvals", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Revision *int64 `json:"revision"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Revision == nil || *body.Revision <= 0 {
			reply(w, 0, Revision{}, ErrInvalid)
			return
		}
		rev, err := s.Approve(r.Context(), bearer(r), r.PathValue("project"), r.PathValue("document"), *body.Revision)
		reply(w, http.StatusOK, rev, err)
	})
	mux.HandleFunc("GET "+base+"/revisions/{revision}", func(w http.ResponseWriter, r *http.Request) {
		number, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
		if err != nil || number <= 0 {
			reply(w, 0, Revision{}, ErrInvalid)
			return
		}
		rev, err := s.Read(r.Context(), bearer(r), r.PathValue("project"), r.PathValue("document"), number)
		reply(w, http.StatusOK, rev, err)
	})
	mux.HandleFunc("GET "+base+"/approved", func(w http.ResponseWriter, r *http.Request) {
		rev, err := s.Read(r.Context(), bearer(r), r.PathValue("project"), r.PathValue("document"), 0)
		reply(w, http.StatusOK, rev, err)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if bearer(r) == "" {
			reply(w, 0, Revision{}, ErrUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return ""
	}
	scheme, value, ok := strings.Cut(headers[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.ContainsAny(value, " \t\r\n,") {
		return ""
	}
	return value
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	// A JSON Unicode escape can take six bytes per byte of decoded ASCII text.
	const maxWireBytes = 6*(256<<10) + 4096
	return jsonv2.UnmarshalRead(http.MaxBytesReader(w, r.Body, maxWireBytes), dst, jsonv2.RejectUnknownMembers(true))
}

func reply(w http.ResponseWriter, success int, rev Revision, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err == nil {
		w.WriteHeader(success)
		_ = json.NewEncoder(w).Encode(rev)
		return
	}
	var status int
	switch {
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", `Bearer realm="projects"`)
		status = http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
	default:
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": http.StatusText(status)})
}
