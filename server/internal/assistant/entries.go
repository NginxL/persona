package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type entriesStore interface {
	AppendAssistantEntry(context.Context, proto.AssistantEntry) (proto.AssistantEntry, error)
	ListAssistantEntries(context.Context, proto.AssistantEntriesParams, bool) (proto.AssistantEntriesResult, error)
	AssistantEntry(context.Context, string) (proto.AssistantEntry, error)
}

func (s *Service) entriesDB() entriesStore {
	if s.store == nil {
		return nil
	}
	st, _ := s.store().(entriesStore)
	return st
}

// localEntriesReader proves physical co-location through the same-user node
// socket. An old/offline node or an unknown owner machine never grants access.
func (s *Service) localEntriesReader(ctx context.Context) bool {
	owner := s.settings(ctx).MachineID
	if owner == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	dial := s.entriesDial
	if dial == nil {
		dial = nodeclient.DialInterruptible
	}
	raw, err := dial(ctx, nodeclient.Request{Kind: "ping"})
	var pong struct {
		Machine string `json:"machine_id"`
		OK      bool   `json:"ok"`
	}
	return err == nil && json.Unmarshal(raw, &pong) == nil && pong.OK && pong.Machine == owner
}

func (s *Service) listEntries(ctx context.Context, p proto.AssistantEntriesParams, personal bool) (proto.AssistantEntriesResult, error) {
	if err := db.ValidateAssistantEntriesParams(&p); err != nil {
		return proto.AssistantEntriesResult{}, conversationsInvalid(assistantwire.MethodAssistantEntriesList, err.Error())
	}
	st := s.entriesDB()
	if st == nil {
		return proto.AssistantEntriesResult{}, errors.New("entries require the manager database")
	}
	return st.ListAssistantEntries(ctx, p, personal)
}

func (s *Service) entriesRoutes() {
	s.handle("GET /api/v1/assistant/entries", func(w http.ResponseWriter, r *http.Request) {
		p, err := entriesQuery(r)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		personal := true
		if r.Header.Get(RoleHeader) == roleExtension {
			allowed, granted := s.entriesReadAccess(r.Header.Get(ExtensionHeader))
			if !allowed {
				body := apierr.CodeBody(w, http.StatusForbidden, "permission_denied", errors.New("assistant.entries.list requires assistant.entries.read"))
				body["scope"] = extwire.ScopeAssistantEntriesRead
				apierr.WriteBody(w, http.StatusForbidden, body)
				return
			}
			personal = granted || s.localEntriesReader(r.Context())
		}
		out, err := s.listEntries(r.Context(), p, personal)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("POST /api/v1/assistant/entries/{id}/wrong", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("only the owner can mark an entry wrong"))
			return
		}
		e, err := s.markEntryWrong(r.Context(), r.PathValue("id"))
		if errors.Is(err, db.ErrEntryNotFound) {
			apierr.Write(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, db.ErrEntrySuperseded) {
			apierr.Write(w, http.StatusConflict, err)
			return
		}
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"entry": e})
	})
}

func entriesQuery(r *http.Request) (proto.AssistantEntriesParams, error) {
	var p proto.AssistantEntriesParams
	q := r.URL.Query()
	for k, values := range q {
		if len(values) != 1 {
			return p, errors.New("ledger parameters must occur once")
		}
		v := values[0]
		switch k {
		case "after":
			p.After = v
		case "limit":
			n, err := strconv.Atoi(v)
			if err != nil {
				return p, errors.New("invalid limit")
			}
			p.Limit = n
		case "by":
			p.By = v
		case "trust":
			p.Trust = v
		case "status":
			p.Status = v
		case "tag":
			p.Tag = v
		case "type":
			p.Type = v
		case "scope":
			if err := strictParams("scope", json.RawMessage(v), &p.Scope); err != nil {
				return p, err
			}
		default:
			return p, errors.New("unknown ledger parameter")
		}
	}
	return p, db.ValidateAssistantEntriesParams(&p)
}

// A wrong mark is an immutable copy pointing to its predecessor. The event names
// the rejected predecessor as well as the new record so consumers retain both.
func (s *Service) markEntryWrong(ctx context.Context, id string) (proto.AssistantEntry, error) {
	st := s.entriesDB()
	if st == nil {
		return proto.AssistantEntry{}, errors.New("entries require the manager database")
	}
	e, err := st.AssistantEntry(ctx, id)
	if err != nil {
		return e, err
	}
	e.ID = "entry_wrong_" + id
	e.Supersedes = id
	e.Status = "wrong"
	e.At = time.Time{}
	e, err = st.AppendAssistantEntry(ctx, e)
	if err != nil {
		return e, err
	}
	if publisher, ok := s.hub.(entriesEventPublisher); ok {
		if err := publisher.EntriesEvent(assistantwire.EventAssistantEntriesChanged, assistantwire.AssistantEntriesChanged{ID: id, RecordID: e.ID, Status: "wrong", Personal: e.Scope.Personal}); err != nil {
			s.logf("entries: change notification could not be delivered")
		}
	}
	return e, nil
}

type entriesEventPublisher interface{ EntriesEvent(string, any) error }

func (b *BusHub) EntriesEvent(event string, params any) error { return b.notifyHub(event, params) }

// The hub supplies the authenticated extension ID; this live registry lookup is
// deliberately separate from the token's initial scope snapshot.
type entriesAuthorizer interface{ EntriesReadAccess(string) (bool, bool) }

func (s *Service) entriesReadAccess(id string) (bool, bool) {
	if id == "" {
		return false, false
	}
	if a, ok := s.hub.(entriesAuthorizer); ok {
		return a.EntriesReadAccess(id)
	}
	return false, false
}
func (b *BusHub) EntriesReadAccess(id string) (bool, bool) {
	if a, ok := b.opts.Extensions.(entriesAuthorizer); ok {
		return a.EntriesReadAccess(id)
	}
	return false, false
}
func (s *Service) extensionEntriesPersonal(ctx context.Context) (bool, error) {
	id := assistantwire.EntriesReader(ctx)
	if id == "" {
		return s.localEntriesReader(ctx), nil
	}
	allowed, personal := s.entriesReadAccess(id)
	if !allowed {
		data, _ := json.Marshal(map[string]string{"scope": extwire.ScopeAssistantEntriesRead})
		return false, &rpc.Error{Code: "permission_denied", Message: "assistant.entries.list requires assistant.entries.read", Data: data}
	}
	return personal || s.localEntriesReader(ctx), nil
}
