package configuration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Resolver authenticates/authorizes a request and binds a provider to its resource.
// The mounting module chooses the URL and authorization policy. No URL, storage key
// or resource selector from a configuration document is executed by this handler.
type Resolver func(*http.Request) (Provider, error)

// NewHandler adapts an externally supplied provider to GET Document / PATCH Update.
// It is mounted by the owning module inside that module's existing authenticated
// routes. It does not register a global endpoint or create any server-side settings.
func NewHandler(resolve Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodPatch {
			w.Header().Set("Allow", "GET, PATCH")
			apierr.Write(w, http.StatusMethodNotAllowed, errors.New(http.StatusText(http.StatusMethodNotAllowed)))
			return
		}
		p, err := resolve(r)
		if err != nil {
			writeError(w, err)
			return
		}
		if p == nil {
			writeError(w, errors.New("configuration resolver returned no provider"))
			return
		}
		var result Document
		if r.Method == http.MethodGet {
			result, err = p.Read(r.Context())
		} else {
			var u Update
			u, err = decodeUpdate(w, r)
			if err != nil {
				writeError(w, err)
				return
			}
			// The provider validates against the snapshot in its own transaction. A
			// separate read/check/write here would have a race with another writer.
			result, err = p.Update(r.Context(), u)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		if err = CheckDocument(result); err != nil {
			writeError(w, err)
			return
		}
		apierr.WriteJSON(w, result)
	})
}

func decodeUpdate(w http.ResponseWriter, r *http.Request) (Update, error) {
	var u Update
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBytes))
	if err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			return u, &Error{Code: "configuration_too_large"}
		}
		return u, &Error{Code: "configuration_bad_request"}
	}
	// Go's struct decoder also accepts case variants and null for certain
	// members. Check the exact wire names and reject null before decoding them.
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return u, &Error{Code: "configuration_bad_request"}
	}
	for key, v := range object {
		switch key {
		case "schema_version", "id", "revision", "changes", "reset":
		default:
			return u, &Error{Code: "configuration_bad_request"}
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return u, &Error{Code: "configuration_bad_request"}
		}
	}
	if json.Unmarshal(raw, &u) != nil {
		return u, &Error{Code: "configuration_bad_request"}
	}
	if u.SchemaVersion != Version {
		return u, &Error{Code: "configuration_version"}
	}
	if !named(u.ID) || !named(u.Revision) || len(u.Changes)+len(u.Reset) == 0 {
		return u, &Error{Code: "configuration_bad_request"}
	}
	return u, nil
}

func writeError(w http.ResponseWriter, err error) {
	status, code, key := http.StatusServiceUnavailable, "configuration_unavailable", "configuration.unavailable"
	var e *Error
	var fields []FieldError
	if errors.As(err, &e) {
		fields = e.FieldErrors
		switch e.Code {
		case "configuration_bad_request":
			status, code, key = http.StatusBadRequest, e.Code, "configuration.bad_request"
		case "configuration_version":
			status, code, key = http.StatusBadRequest, e.Code, "configuration.version"
		case "configuration_too_large":
			status, code, key = http.StatusRequestEntityTooLarge, e.Code, "configuration.too_large"
		case "configuration_conflict":
			status, code, key = http.StatusConflict, e.Code, "configuration.conflict"
		case "configuration_invalid":
			status, code, key = http.StatusUnprocessableEntity, e.Code, "configuration.invalid"
		case "configuration_forbidden":
			status, code, key = http.StatusForbidden, e.Code, "configuration.forbidden"
		case "configuration_not_found":
			status, code, key = http.StatusNotFound, e.Code, "configuration.not_found"
		}
	}
	body := map[string]any{"code": code, "error": i18n.T(i18n.OfWriter(w), key)}
	if len(fields) > 0 {
		body["field_errors"] = fields
	}
	apierr.WriteBody(w, status, body)
}
