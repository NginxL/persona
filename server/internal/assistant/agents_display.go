package assistant

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The default manager keeps the paired hub's display preference, like navigation and
// triage. This never changes the devices' per-machine inventory or seeBotmux.
const agentsShowBotmuxKey = "agents.show_botmux"
const agentsUseHerdrTabNameKey = "agents.use_herdr_tab_name"
const agentsSortByActivityKey = "agents.sort_by_activity"
const agentsShowFilterKey = "agents.show_filter"

// agentsDisplay is the answer of GET / PUT display-settings. SortByActivity and ShowFilter are the effective values: the
// saved choice, else manager.json's agents.sort_by_activity / agents.show_filter, else off.
type agentsDisplay struct {
	ShowBotmux      bool `json:"show_botmux"`
	Configured      bool `json:"configured"`
	UseHerdrTabName bool `json:"use_herdr_tab_name"`
	SortByActivity  bool `json:"sort_by_activity"`
	ShowFilter      bool `json:"show_filter"`
}

func (s *Service) readAgentsDisplay(ctx context.Context) (agentsDisplay, error) {
	agents := s.hello(ctx).Agents
	out := agentsDisplay{ShowBotmux: true, SortByActivity: agents.SortByActivity != nil && *agents.SortByActivity,
		ShowFilter: agents.ShowFilter != nil && *agents.ShowFilter}
	st := s.store()
	if st == nil {
		return out, errors.New("Agents display settings need a database")
	}
	var err error
	out.Configured, err = st.Setting(ctx, agentsShowBotmuxKey, &out.ShowBotmux)
	if err == nil {
		_, err = st.Setting(ctx, agentsUseHerdrTabNameKey, &out.UseHerdrTabName)
	}
	if err == nil {
		_, err = st.Setting(ctx, agentsSortByActivityKey, &out.SortByActivity)
	}
	if err == nil {
		_, err = st.Setting(ctx, agentsShowFilterKey, &out.ShowFilter)
	}
	return out, err
}

func (s *Service) useHerdrTabName(ctx context.Context) bool {
	if s.fleetOn() {
		// An Agent's manager may differ from the one that stores this hub-wide choice.
		var shared struct {
			UseHerdrTabName *bool `json:"use_herdr_tab_name"`
		}
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.appCall(readCtx, assistantwire.AppSettings, assistantwire.AgentsDisplayGet, nil, &shared)
		cancel()
		if err == nil && shared.UseHerdrTabName != nil {
			return *shared.UseHerdrTabName
		}
	}
	var on bool
	if st := s.store(); st != nil {
		_, err := st.Setting(ctx, agentsUseHerdrTabNameKey, &on)
		return err == nil && on
	}
	return false
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
			ShowBotmux      *bool `json:"show_botmux"`
			UseHerdrTabName *bool `json:"use_herdr_tab_name"`
			SortByActivity  *bool `json:"sort_by_activity"`
			ShowFilter      *bool `json:"show_filter"`
			OnlyIfUnset     bool  `json:"only_if_unset"`
		}
		err := decodeBody(w, r, &in, 1<<16)
		sent := 0
		for _, p := range []*bool{in.ShowBotmux, in.UseHerdrTabName, in.SortByActivity, in.ShowFilter} {
			if p != nil {
				sent++
			}
		}
		if err != nil || sent != 1 || in.OnlyIfUnset && in.ShowBotmux == nil {
			writeBodyErr(w, err, errors.New("send one of show_botmux, use_herdr_tab_name, sort_by_activity or show_filter, with only_if_unset allowed for show_botmux"))
			return
		}
		st := s.store()
		if st == nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("Agents display settings need a database"))
			return
		}
		if in.OnlyIfUnset {
			atomic, ok := st.(interface {
				PutSettingIfUnset(context.Context, string, any) error
			})
			if !ok {
				apierr.Write(w, http.StatusServiceUnavailable, errors.New("atomic settings migration unavailable"))
				return
			}
			err = atomic.PutSettingIfUnset(r.Context(), agentsShowBotmuxKey, *in.ShowBotmux)
		} else if in.ShowBotmux != nil {
			err = st.PutSetting(r.Context(), agentsShowBotmuxKey, *in.ShowBotmux)
		} else if in.UseHerdrTabName != nil {
			err = st.PutSetting(r.Context(), agentsUseHerdrTabNameKey, *in.UseHerdrTabName)
		} else if in.SortByActivity != nil {
			err = st.PutSetting(r.Context(), agentsSortByActivityKey, *in.SortByActivity)
		} else {
			err = st.PutSetting(r.Context(), agentsShowFilterKey, *in.ShowFilter)
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
