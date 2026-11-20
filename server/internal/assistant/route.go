package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/kb"
)

// Older managers offered to turn new chats into directions. Their stored offers can still be
// read, accepted or dismissed. New chats become directions when their owner chooses Pin.
const routeOffersKey = "assistant.route_offers"

// routeTimeout is all one routing may take (the proposal's "about 2 s"); tests change it.
var routeTimeout = 2 * time.Second

const (
	// routeMaxTokens: the answer is one small JSON object.
	routeMaxTokens = 400
	// routeOfferKeep: an offer nobody answered in this long is dropped.
	routeOfferKeep = 7 * 24 * time.Hour
	// leadersMost is the hard limit on leaders, the same for every way one is made (setLeaders, the owner's taps).
	leadersMost = 20
)

// routeLeader is a current leader as the model and the rules see it.
type routeLeader struct{ ID, Title, Project string }

// routeOffer is a direction offered for a New chat, waiting for the owner's Start direction or Just a quick chat.
type routeOffer struct {
	MachineID string `json:"machine_id"`
	// Title: the direction's suggested name.
	Title string `json:"title"`
	// Project: the slug of the existing knowledge-base project the sentence names; NewProject: else the name of the
	// project Start direction makes ("" both: none).
	Project    string `json:"project,omitempty"`
	NewProject string `json:"new_project,omitempty"`
	// Closest is the existing leader suggested by the manager that made the offer.
	Closest string    `json:"closest,omitempty"`
	At      time.Time `json:"at"`
}

func (o *routeOffer) current() bool {
	return o != nil && time.Since(o.At) <= routeOfferKeep
}

type routeBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*routeOffer // by conversation id
	// ask is the model call (nil: assistant.route's model); tests set it.
	ask func(ctx context.Context, system, input string, out any) error
}

// askRouteModel asks the model (or the test's stand-in).
func (s *Service) askRouteModel(ctx context.Context, system, input string, out any) error {
	if ask := s.offers.ask; ask != nil {
		return ask(ctx, system, input, out)
	}
	return s.purposeModel("assistant.route").JSONMax(ctx, system, input, routeMaxTokens, out)
}

// routeLeaders are the current leaders with their titles and projects.
func (s *Service) routeLeaders(ctx context.Context) []routeLeader {
	cfg := s.settings(ctx)
	titles, _ := s.conversationTitles()
	views, _ := s.directionViews(ctx)
	var out []routeLeader
	for _, id := range cfg.Leaders {
		if id == "" || id == cfg.Main {
			continue
		}
		ld := routeLeader{ID: id, Title: titles[id].title}
		if d := views[id]; d != nil {
			ld.Project = d.Project
		}
		out = append(out, ld)
	}
	return out
}

// ---- the offers ----

func (s *Service) routeOffersLocked(ctx context.Context) bool {
	b := &s.offers
	if b.loaded {
		return true
	}
	m := map[string]*routeOffer{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, routeOffersKey, &m); err != nil {
			s.logf("load route offers: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*routeOffer{}
	}
	b.m, b.loaded = m, true
	return true
}

func (s *Service) saveRouteOffersLocked(ctx context.Context) {
	for id, o := range s.offers.m {
		if !o.current() {
			delete(s.offers.m, id)
		}
	}
	st := s.store()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, routeOffersKey, s.offers.m); err != nil {
		s.logf("save route offers: %v", err)
	}
}

// takeRouteOffer removes conversation id's offer and returns it (nil: none). told: the apps hear it is gone.
func (s *Service) takeRouteOffer(ctx context.Context, id string) (*routeOffer, error) {
	b := &s.offers
	b.mu.Lock()
	if !s.routeOffersLocked(ctx) {
		b.mu.Unlock()
		return nil, errors.New("the direction offers could not be read")
	}
	o := b.m[id]
	if !o.current() {
		b.mu.Unlock()
		return nil, nil
	}
	delete(b.m, id)
	s.saveRouteOffersLocked(ctx)
	b.mu.Unlock()
	s.event(context.WithoutCancel(ctx), map[string]any{"type": "direction_offer", "conversation": id, "offer": nil})
	return o, nil
}

// ---- the owner's taps ----

// errNoOffer: Start direction on a conversation with no offer.
var errNoOffer = errors.New("no direction is offered for this conversation: make it one with …/make")

// makeDirection makes assistant conversation id a leader as the owner chose: filed under project (a slug of an existing
// knowledge-base project), or a new project named newProject, or none; title, when given, names it unless the owner
// named it already. The record says by "user"; leader.changed follows (directionsSync).
func (s *Service) makeDirection(ctx context.Context, id, project, newProject, title string) (*leaderView, error) {
	cfg := s.settings(ctx)
	if id == cfg.Main {
		return nil, errors.New("the main conversation supervises already: a direction is a side conversation")
	}
	if id == cfg.BackgroundSupervisor {
		return nil, errors.New("the background supervisor is built in: a direction is another side conversation")
	}
	c, ok := s.session(ctx, cfg.MachineID, id)
	if !ok || !c.Assistant {
		return nil, fmt.Errorf("%s is not a Persona conversation on the assistant's machine", id)
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	already := slices.Contains(cfg.Leaders, id)
	if !already && len(cfg.Leaders) >= leadersMost {
		return nil, fmt.Errorf("at most %d directions: end one first", leadersMost)
	}
	if project != "" { // checked before anything changes
		slug, err := s.todoProject(ctx, project)
		if err != nil {
			return nil, err
		}
		project = slug
	}
	// The leaders first (the limit is checked again there, against a tap at the same moment): what follows only adds to
	// a direction that exists.
	if !already {
		if _, err := s.updateSettings(context.WithoutCancel(ctx), func(v *Settings) error {
			if !slices.Contains(v.Leaders, id) {
				if len(v.Leaders) >= leadersMost {
					return fmt.Errorf("at most %d directions: end one first", leadersMost)
				}
				v.Leaders = append(slices.Clone(v.Leaders), id)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		s.settingsChanged(ctx)
	}
	if project == "" && newProject != "" {
		if e, err := s.kbGet(ctx); err == nil && e != nil {
			slug, err := e.Apply(kb.KindProject, newProject, kb.Update{Name: newProject, Kind: "long"})
			if err != nil {
				s.logf("direction %s: project %q: %v (filed under none)", short(id), newProject, err)
			} else {
				project = slug
			}
		}
	}
	now := time.Now().UTC()
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		b.mu.Unlock()
		return nil, errors.New("the directions could not be read")
	}
	d := b.m[id]
	if d == nil {
		d = &direction{MachineID: cfg.MachineID, At: now}
		b.m[id] = d
	} else if d.EndedAt != nil { // a direction again: since now, keeping its name, project and last progress
		d.At = now
	}
	d.By, d.EndedAt = directionByUser, nil
	if project != "" {
		d.Project = project
	}
	if t := clip(oneLine(title), directionNameMost); t != "" && d.Name == "" { // the name the owner accepted or gave (#3679)
		d.Name, d.NameBy = t, directionByUser
	}
	s.saveDirectionsLocked(ctx)
	view := leaderView{By: d.By, At: d.At}
	view.ID, view.Project = id, d.Project
	b.mu.Unlock()
	if title = strings.TrimSpace(title); title != "" {
		if by, _ := s.titleOf(id); by != titleByUser {
			s.titleSet(c.MachineID, id, title, titleByAssistant)
		}
	}
	if titles, ok := s.conversationTitles(); ok {
		view.Title = titles[id].title
	}
	if d := s.directionOf(id); d != nil {
		pn, pok := s.projectNames(ctx)
		view.Name = nameOf(d, view.Title, pn, pok)
	}
	_, _ = s.takeRouteOffer(ctx, id)
	s.directionsSoon()
	return &view, nil
}

// endDirection takes leader id out of assistant.leaders as the owner chose: its record is ended (directionsSync), its
// open tasks keep it as their supervisor on the ledger while their signals and terminal questions go back to the main
// conversation's supervision route (handedBack), nothing is deleted. One that is no leader is a no-op.
func (s *Service) endDirection(ctx context.Context, id string) (bool, error) {
	cfg := s.settings(ctx)
	if !slices.Contains(cfg.Leaders, id) {
		return false, nil
	}
	if s.store() == nil {
		return false, errors.New("no database on this hub")
	}
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		v.Leaders = slices.DeleteFunc(slices.Clone(v.Leaders), func(l string) bool { return l == id })
		return nil
	}); err != nil {
		return false, err
	}
	b := &s.directions
	b.mu.Lock()
	if s.directionsLocked(ctx) {
		if d := b.m[id]; d != nil {
			d.By = directionByUser
			s.saveDirectionsLocked(ctx)
		}
	}
	b.mu.Unlock()
	s.settingsChanged(ctx)
	s.directionsSoon()
	return true, nil
}

// routeRoutes are the owner's taps on a direction (docs/protocol/protocol.md "REST (App → hub)").
func (s *Service) routeRoutes() {
	s.handle("GET /api/v1/assistant/directions/offers", func(w http.ResponseWriter, r *http.Request) {
		type offerView struct {
			Conversation string `json:"conversation"`
			routeOffer
		}
		b := &s.offers
		b.mu.Lock()
		if !s.routeOffersLocked(r.Context()) {
			b.mu.Unlock()
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("the direction offers could not be read"))
			return
		}
		list := make([]offerView, 0, len(b.m))
		for id, o := range b.m {
			if o.current() {
				list = append(list, offerView{id, *o})
			}
		}
		b.mu.Unlock()
		slices.SortFunc(list, func(a, b offerView) int { return b.At.Compare(a.At) })
		apierr.WriteJSON(w, map[string]any{"offers": list})
	})
	s.handle("DELETE /api/v1/assistant/directions/offers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change directions"))
			return
		}
		o, err := s.takeRouteOffer(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"dropped": o != nil})
	})
	direction := func(start bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change directions"))
				return
			}
			var in struct {
				Project string `json:"project"` // an existing project's name or slug, over the offer's
				Title   string `json:"title"`   // over the offer's
			}
			if r.ContentLength != 0 {
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
					apierr.Write(w, http.StatusBadRequest, err)
					return
				}
			}
			id := r.PathValue("id")
			ctx := r.Context()
			project, newProject, title := strings.TrimSpace(in.Project), "", strings.TrimSpace(in.Title)
			if start {
				b := &s.offers
				b.mu.Lock()
				ok := s.routeOffersLocked(ctx)
				var o *routeOffer
				if ok && b.m[id].current() {
					cp := *b.m[id]
					o = &cp
				}
				b.mu.Unlock()
				if !ok {
					apierr.Write(w, http.StatusServiceUnavailable, errors.New("the direction offers could not be read"))
					return
				}
				if o == nil {
					apierr.Write(w, http.StatusNotFound, errNoOffer)
					return
				}
				if project == "" {
					project, newProject = o.Project, o.NewProject
				}
				if title == "" {
					title = o.Title
				}
			}
			v, err := s.makeDirection(ctx, id, project, newProject, title)
			if err != nil {
				apierr.Write(w, http.StatusConflict, err)
				return
			}
			apierr.WriteJSON(w, map[string]any{"leader": v})
		}
	}
	s.handle("POST /api/v1/assistant/directions/{id}/start", direction(true))
	s.handle("POST /api/v1/assistant/directions/{id}/make", direction(false))
	s.handle("POST /api/v1/assistant/directions/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change directions"))
			return
		}
		ended, err := s.endDirection(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ended": ended})
	})
}
