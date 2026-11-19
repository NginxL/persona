package assistant

import (
	"context"
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
)

// The default manager keeps the paired hub's display preference, like navigation and
// triage. This never changes the devices' per-machine inventory or seeBotmux.
const agentsShowBotmuxKey = "agents.show_botmux"

type agentsDisplay struct {
	ShowBotmux bool `json:"show_botmux"`
	Configured bool `json:"configured"`
}

func (s *Service) readAgentsDisplay(ctx context.Context) (agentsDisplay, error) {
	out := agentsDisplay{ShowBotmux: true}
	st := s.store()
	if st == nil {
		return out, errors.New("Agents display settings need a database")
	}
	var err error
	out.Configured, err = st.Setting(ctx, agentsShowBotmuxKey, &out.ShowBotmux)
	return out, err
}

func (s *Service) agentsDisplayRoutes() {
	s.handle("GET /api/v1/agents/display-settings", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.readAgentsDisplay(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("PUT /api/v1/agents/display-settings", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("only full access may change Agents display settings"))
			return
		}
		var in struct {
			ShowBotmux  *bool `json:"show_botmux"`
			OnlyIfUnset bool  `json:"only_if_unset"`
		}
		if err := decodeBody(w, r, &in, 1<<16); err != nil || in.ShowBotmux == nil {
			writeBodyErr(w, err, errors.New("show_botmux is required"))
			return
		}
		st := s.store()
		if st == nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("Agents display settings need a database"))
			return
		}
		var err error
		if in.OnlyIfUnset {
			atomic, ok := st.(interface {
				PutSettingIfUnset(context.Context, string, any) error
			})
			if !ok {
				apierr.Write(w, http.StatusServiceUnavailable, errors.New("atomic settings migration unavailable"))
				return
			}
			err = atomic.PutSettingIfUnset(r.Context(), agentsShowBotmuxKey, *in.ShowBotmux)
		} else {
			err = st.PutSetting(r.Context(), agentsShowBotmuxKey, *in.ShowBotmux)
		}
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		out, err := s.readAgentsDisplay(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, out)
	})
}
