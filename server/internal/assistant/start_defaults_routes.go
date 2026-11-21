package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// startDefaultsRoutes serves GET/PUT /api/v1/assistant/start-defaults. Full devices read and write it like any
// assistant setting; an extension whose token grants the assistant.start_defaults hub scope reaches it too, and a
// policy it writes must expire within startDefaultsMaxExpiry, so a stopped or uninstalled extension leaves nothing in
// force. Empty rules clear it.
func (s *Service) startDefaultsRoutes() {
	s.startDefaultsPreviewRoutes() // start_defaults_preview.go
	s.handle("GET /api/v1/assistant/start-defaults", func(w http.ResponseWriter, r *http.Request) {
		policy := s.activeStartDefaults(r.Context())
		if policy == nil {
			policy = &StartDefaultsPolicy{Rules: []StartDefaultsRule{}}
		}
		apierr.WriteJSON(w, map[string]any{"start_defaults": policy})
	})

	s.handle("PUT /api/v1/assistant/start-defaults", func(w http.ResponseWriter, r *http.Request) {
		// The hub relays an extension's request only to routes its granted scopes open, so an extension role here
		// already holds assistant.start_defaults; read-only devices are refused.
		role := r.Header.Get(assistantwire.RoleHeader)
		if role != "" && role != roleFull && role != roleExtension {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change the start defaults"))
			return
		}
		var in struct {
			StartDefaults *StartDefaultsPolicy `json:"start_defaults"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, startDefaultsBodyLimit))
		dec.DisallowUnknownFields() // a misspelt field must not silently do nothing
		if err := dec.Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if in.StartDefaults == nil {
			apierr.Write(w, http.StatusBadRequest, errors.New("the body must name start_defaults"))
			return
		}
		fromExtension := role == roleExtension
		ext := strings.TrimSpace(r.Header.Get(ExtensionHeader))
		if fromExtension && ext == "" { // the hub names the extension of every request it relays for one
			apierr.Write(w, http.StatusForbidden, errors.New("the extension of this request is unknown"))
			return
		}
		clearing := len(in.StartDefaults.Rules) == 0
		if !clearing {
			if err := in.StartDefaults.validate(!fromExtension, time.Now().UTC()); err != nil {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
			in.StartDefaults.By = "owner"
			if fromExtension {
				in.StartDefaults.By = ext
			}
		}
		var saved StartDefaultsPolicy
		_, err := s.updateSettings(r.Context(), func(v *Settings) error {
			if clearing {
				v.StartDefaults = nil
				saved = StartDefaultsPolicy{Rules: []StartDefaultsRule{}}
				return nil
			}
			v.StartDefaults = in.StartDefaults
			saved = *in.StartDefaults
			return nil
		})
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		s.settingsChanged(r.Context())
		apierr.WriteJSON(w, map[string]any{"start_defaults": saved})
	})
}
