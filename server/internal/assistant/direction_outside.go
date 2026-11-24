package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
)

// Outside directions (#8236) have an ordinary direction record and current-leader membership, but deliberately no
// Persona conversation. Their optional Agent reference is metadata for opening the work, not task supervision: the
// existing missing-conversation route sends the direction's task signals to the main chat.
var (
	errDirectionInvalid  = errors.New("invalid direction")
	errDirectionConflict = errors.New("direction conflict")
)

type directionCreateInput struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type directionProgressInput struct {
	Reached  string   `json:"reached"`
	Doing    string   `json:"doing"`
	NeedsYou string   `json:"needs_you"`
	Links    []string `json:"links"`
}

func directionInvalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errDirectionInvalid, fmt.Sprintf(format, args...))
}

// createOutsideDirection creates a direction without creating a conversation, title, model turn or to-do. The settings
// membership, ordinary record and outside marker are committed together; caches change only after that commit.
func (s *Service) createOutsideDirection(ctx context.Context, in directionCreateInput) (*leaderView, error) {
	name := clip(oneLine(in.Name), directionNameMost)
	if name == "" {
		return nil, directionInvalidf("name is required")
	}
	if strings.TrimSpace(in.Project) == "" {
		return nil, directionInvalidf("project is required")
	}
	project, err := s.todoProject(ctx, in.Project)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDirectionInvalid, err)
	}
	machine, session := strings.TrimSpace(in.MachineID), strings.TrimSpace(in.SessionID)
	if (machine == "") != (session == "") {
		return nil, directionInvalidf("machine_id and session_id must be supplied together")
	}
	ref := db.OutsideDirection{}
	if machine != "" {
		am, ok := s.device(ctx, machine)
		if !ok {
			return nil, directionInvalidf("no machine %q on this assistant manager", machine)
		}
		c, ok := s.session(ctx, am.ID, session)
		if !ok {
			return nil, directionInvalidf("no session %q on machine %q", session, am.ID)
		}
		if c.Assistant {
			return nil, directionInvalidf("the external session must be a coding Agent, not a Persona conversation")
		}
		ref = db.OutsideDirection{MachineID: am.ID, SessionID: c.SessionID}
	}
	st := s.store()
	creator, ok := st.(outsideDirectionStore)
	if !ok {
		return nil, errors.New("outside directions need the assistant manager database")
	}
	id, now := "dir_"+randomHex(16), time.Now().UTC()
	var view *leaderView
	_, err = s.updateSettingsSave(ctx, func(v *Settings) error {
		if len(v.Leaders) >= leadersMost {
			return fmt.Errorf("%w: at most %d directions: end one first", errDirectionConflict, leadersMost)
		}
		if slices.Contains(v.Leaders, id) {
			return fmt.Errorf("%w: direction id already exists", errDirectionConflict)
		}
		v.Leaders = append(slices.Clone(v.Leaders), id)
		return nil
	}, func(ctx context.Context, _ Store, v Settings) error {
		b := &s.directions
		b.mu.Lock()
		defer b.mu.Unlock()
		if !s.directionsLocked(ctx) {
			return errors.New("the directions could not be read")
		}
		if b.m[id] != nil {
			return fmt.Errorf("%w: direction id already exists", errDirectionConflict)
		}
		if ref.MachineID != "" {
			for other, d := range b.m {
				if d.EndedAt == nil && d.Outside != nil && d.Outside.MachineID == ref.MachineID && d.Outside.SessionID == ref.SessionID {
					return fmt.Errorf("%w: that Agent already stands for direction %s", errDirectionConflict, other)
				}
			}
		}
		next := maps.Clone(b.m)
		d := &direction{Outside: &directionOutside{MachineID: ref.MachineID, SessionID: ref.SessionID}, Project: project,
			By: directionByUser, At: now, Name: name, NameBy: directionByUser}
		next[id] = d
		if err := creator.CreateOutsideDirection(ctx, id, ref, map[string]any{settingsKey: v, directionsKey: next}); err != nil {
			return err
		}
		b.m = next
		view = &leaderView{External: true, MachineID: ref.MachineID, SessionID: ref.SessionID, By: d.By, At: d.At}
		view.ID, view.Title, view.Name, view.Project = id, name, name, project
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.settingsChanged(ctx)
	s.directionsSoon()
	return view, nil
}

// writeDirectionProgress reports on any current direction, conversation-backed or outside. Holding the settings write
// lock keeps an end from slipping between the current-direction check and the durable report.
func (s *Service) writeDirectionProgress(ctx context.Context, id string, in directionProgressInput) (*leaderView, error) {
	p, err := normalizeLeaderProgress(in, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDirectionInvalid, err)
	}
	s.prefs.change.Lock()
	defer s.prefs.change.Unlock()
	cfg, ok := s.savedSettings(ctx)
	if !ok {
		return nil, errors.New("the assistant's settings could not be read")
	}
	if id == "" || id == cfg.Main || !slices.Contains(cfg.Leaders, id) {
		return nil, errNoDirection
	}
	if _, err := s.leaderProgressReport(ctx, id, p); err != nil {
		return nil, err
	}
	d := s.directionOf(id)
	if d == nil {
		// The report is durable and its event is queued. A response-decoration read must not turn that committed write
		// into an apparent failure that a caller might retry; GET leaders will fill in the ordinary metadata again.
		view := leaderView{Progress: &p}
		view.ID, view.Title, view.Name = id, id, id
		return &view, nil
	}
	titles, _ := s.conversationTitles()
	pnames, pok := s.projectNames(ctx)
	view := directionLeaderView(id, d, titles[id].title, pnames, pok)
	s.directionsSoon()
	return &view, nil
}

// directionNodeReply is the local node CLI's outcome. The socket transport stays one-shot: an unavailable result does
// not prove a write was not admitted, so neither the node nor the CLI repeats it automatically.
func directionNodeReply(view *leaderView, err error) map[string]any {
	if err == nil {
		return map[string]any{"status": "ok", "leader": view}
	}
	status := "unavailable"
	switch {
	case errors.Is(err, errDirectionInvalid):
		status = "invalid"
	case errors.Is(err, errNoDirection), errors.Is(err, errDirectionConflict):
		status = "failed"
	}
	return map[string]any{"status": status, "error": err.Error()}
}

func (s *Service) directionOutsideRoutes() {
	writer := func(w http.ResponseWriter, r *http.Request) bool {
		if todoWriter(r) {
			return true
		}
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change directions"))
		return false
	}
	s.handle("POST /api/v1/assistant/directions", func(w http.ResponseWriter, r *http.Request) {
		if !writer(w, r) {
			return
		}
		var in directionCreateInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		view, err := s.createOutsideDirection(r.Context(), in)
		writeDirectionResult(w, view, err)
	})
	s.handle("POST /api/v1/assistant/directions/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		if !writer(w, r) {
			return
		}
		var in directionProgressInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		view, err := s.writeDirectionProgress(r.Context(), r.PathValue("id"), in)
		writeDirectionResult(w, view, err)
	})
}

func writeDirectionResult(w http.ResponseWriter, view *leaderView, err error) {
	switch {
	case err == nil:
		apierr.WriteJSON(w, map[string]any{"leader": view})
	case errors.Is(err, errDirectionInvalid):
		apierr.Write(w, http.StatusBadRequest, err)
	case errors.Is(err, errNoDirection):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errDirectionConflict):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusServiceUnavailable, err)
	}
}
