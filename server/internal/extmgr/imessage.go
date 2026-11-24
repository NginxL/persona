package extmgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extmgr/imessageport"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/imessagewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Messages (modules/imessage, docs/plans/plan-connectors.md "5"): the app reads one Mac's Messages through that Mac's node;
// the hub passes the pages along and keeps none of them. With "Hand to Persona" on (the user's switch, on by default,
// imessageAIKey) the real conversations also go to the knowledge base, the inbox and the assistant's tools
// (modules/imessage/persona.go). No number or message is logged (only the connector, the Mac and the kind of failure).

// imessageAIKey is the settings key of "Hand to Persona": absent means on.
const imessageAIKey = "imessage.ai"

// errNoIMessageModule: this build has no iMessage module (imessageport), so there is no Messages connector to make.
var errNoIMessageModule = errors.New("this build has no iMessage module")

func (h *Manager) addIMessage(cfg imessagewire.Config) error {
	newRemote := imessageport.Factory()
	if newRemote == nil {
		return errNoIMessageModule
	}
	name := cfg.Machine
	if m := h.localMachine(cfg.Machine); m != nil {
		name = m.name()
	}
	c := newRemote(cfg, name, h.imessageCall(cfg.Machine))
	c.SetZone(h.userLocation)
	on, err := h.imessageAI(context.Background())
	if err != nil {
		on = false
		h.imessageAIPending.Store(true)
	}
	h.imessageArchive.init(on, h.store)
	c.SetAI(on)
	return h.addConnector(c)
}

// imessageAI is the user's "Hand to Persona" choice: on unless they turned it off.
func (h *Manager) imessageAI(ctx context.Context) (bool, error) {
	if h.store == nil {
		return true, nil
	}
	var on bool
	ok, err := h.store.Setting(ctx, imessageAIKey, &on)
	if err != nil {
		return false, err
	}
	return !ok || on, nil
}

// A failed startup read or an uncertain settings write stays off until the
// persisted choice can be read again. Serialize recovery with a user's PUT.
func (h *Manager) reconcileIMessageAI(ctx context.Context) {
	if !h.imessageAIPending.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := h.imessageSettingsMu.lock(ctx); err != nil {
		return
	}
	defer h.imessageSettingsMu.unlock()
	if !h.imessageAIPending.Load() {
		return
	}
	c := h.imessage()
	if c == nil {
		return
	}
	on, err := h.imessageAI(ctx)
	if err != nil {
		return
	}
	if on {
		if err := h.imessageArchive.enable(ctx); err != nil {
			return
		}
		c.SetAI(true)
	} else {
		c.SetAI(false)
		if err := h.imessageArchive.disable(ctx); err != nil {
			return
		}
	}
	h.imessageAIPending.Store(false)
	if on {
		h.kbKick(c.ID(), connector.ScopeChats)
		h.kickInbox()
	}
}

var errNotAMachine = errors.New("信息's Mac is not a machine of this hub")

// imessageCall asks the Mac's node, turning its failures into the connector's errors.
func (h *Manager) imessageCall(machine string) imessagewire.Caller {
	return func(ctx context.Context, method string, params, out any) error {
		m := h.localMachine(machine)
		if m == nil {
			return errNotAMachine
		}
		err := m.callBuiltinProgram(ctx, imessageExtensionID, method, params, out)
		var re *rpc.Error
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errOffline):
			return imessagewire.ErrOffline
		case errors.As(err, &re) && strings.HasPrefix(re.Message, "unknown method"):
			return imessagewire.ErrNodeTooOld
		case errors.As(err, &re) && re.Code == proto.CodeNoPermission:
			return imessagewire.ErrNoPermission
		case errors.As(err, &re) && re.Code == proto.CodeNoChat:
			return imessagewire.ErrNoChat
		}
		return fmt.Errorf("%s: %w", method, err)
	}
}

func (h *Manager) imessage() imessagewire.Remote {
	c, _ := h.connectors.Get(imessagewire.ID).(imessagewire.Remote)
	return c
}

func (h *Manager) imessageRoutes(api *http.ServeMux) {
	// GET /api/v1/imessage: the status with the steps to fix it (Settings › Connectors › Messages), asked of the Mac
	// now: the page is opened (or "Check again" is tapped) right after the permission was given.
	api.HandleFunc("GET /api/v1/imessage", func(w http.ResponseWriter, r *http.Request) {
		h.reconcileIMessageAI(r.Context())
		c := h.imessage()
		if c == nil {
			writeCode(w, http.StatusNotFound, "not_configured", i18n.Localize(errNoIMessageMac, h.requestLang(r)), nil)
			return
		}
		c.Check(r.Context())
		writeJSON(w, c.Detail(r.Context()))
	})
	// PUT /api/v1/imessage/settings {"ai": bool}: a successful off waits for admitted archive commits; retained data
	// stays until DELETE /api/v1/imessage/data.
	api.HandleFunc("PUT /api/v1/imessage/settings", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := h.imessageSettingsMu.lock(ctx); err != nil {
			writeErr(w, http.StatusRequestTimeout, errors.New(http.StatusText(http.StatusRequestTimeout)))
			return
		}
		defer h.imessageSettingsMu.unlock()
		c := h.imessage()
		if c == nil {
			writeCode(w, http.StatusNotFound, "not_configured", i18n.T(h.requestLang(r), "hub-imessage.not_configured"), nil)
			return
		}
		var in struct {
			AI *bool `json:"ai"`
		}
		if err := decodeJSONBody(w, r, &in, 4<<10); err != nil || in.AI == nil {
			writeBodyErr(w, err, errors.New("ai: true or false"))
			return
		}
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		h.imessageAIPending.Store(true)
		if *in.AI {
			if err := h.store.PutSetting(ctx, imessageAIKey, true); err != nil {
				c.SetAI(false)
				retireCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_, _ = h.imessageArchive.retire(retireCtx)
				cancel()
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			if err := h.imessageArchive.enable(ctx); err != nil {
				writeErr(w, http.StatusServiceUnavailable, errors.New(http.StatusText(http.StatusServiceUnavailable)))
				return
			}
			c.SetAI(true)
		} else {
			c.SetAI(false)
			// Retire even an off write whose database acknowledgement is lost.
			drained, err := h.imessageArchive.retire(ctx)
			if putErr := h.store.PutSetting(ctx, imessageAIKey, false); putErr != nil {
				writeErr(w, http.StatusInternalServerError, putErr)
				return
			}
			if err == nil {
				err = waitIMessageArchive(ctx, drained)
			}
			if err != nil {
				h.logf("imessage: archive disable did not finish: %v", err)
				writeErrCode(w, http.StatusServiceUnavailable, "imessage_archive_pending", i18n.Err("hub-imessage.archive_pending"))
				return
			}
		}
		h.imessageAIPending.Store(false)
		h.logf("imessage: 交给 Persona %v", *in.AI)
		if *in.AI {
			h.kbKick(c.ID(), connector.ScopeChats)
			h.kickInbox()
		}
		writeJSON(w, c.Detail(r.Context()))
	})
	// DELETE /api/v1/imessage/data: what the hub kept of iMessage (the knowledge base's chats and people, inbox items),
	// once "Hand to Persona" is off — on, the next sync would bring it back.
	api.HandleFunc("DELETE /api/v1/imessage/data", func(w http.ResponseWriter, r *http.Request) {
		c := h.imessage()
		if c == nil {
			writeCode(w, http.StatusNotFound, "not_configured", i18n.T(h.requestLang(r), "hub-imessage.not_configured"), nil)
			return
		}
		if c.AI() {
			writeCode(w, http.StatusConflict, "ai_on", i18n.T(h.requestLang(r), "hub-imessage.ai_on"), nil)
			return
		}
		res, err := h.forgetIMessage(r.Context())
		if err != nil {
			h.logf("imessage: delete: %v", err)
			writeForgetErr(w, err)
			return
		}
		h.logf("imessage: deleted %d chat(s), %d person(s), %d inbox item(s)", res.Chats, res.People, res.Inbox)
		writeJSON(w, res)
	})
	api.HandleFunc("GET /api/v1/imessage/chats", func(w http.ResponseWriter, r *http.Request) {
		c := h.imessage()
		if c == nil {
			writeCode(w, http.StatusNotFound, "not_configured", i18n.T(h.requestLang(r), "hub-imessage.not_configured"), nil)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, err := c.ChatsPage(r.Context(), limit, r.URL.Query().Get("before"))
		if err != nil {
			h.imessageFailed(w, c, "chats", err)
			return
		}
		writeJSON(w, page)
	})
	api.HandleFunc("GET /api/v1/imessage/chats/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		c := h.imessage()
		if c == nil {
			writeCode(w, http.StatusNotFound, "not_configured", i18n.T(h.requestLang(r), "hub-imessage.not_configured"), nil)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, err := c.MessagesPage(r.Context(), r.PathValue("id"), limit, r.URL.Query().Get("before"))
		if err != nil {
			h.imessageFailed(w, c, "messages", err)
			return
		}
		writeJSON(w, page)
	})
}

// forgetIMessage removes what the hub kept of iMessage (forgetSource).
func (h *Manager) forgetIMessage(ctx context.Context) (sourceForgotten, error) {
	return h.forgetSource(ctx, imessagewire.ID)
}

// imessageFailed answers a read that failed, with a code the app acts on; only the kind of failure is logged.
func (h *Manager) imessageFailed(w http.ResponseWriter, c imessagewire.Remote, what string, err error) {
	msg := c.Explain(err)
	switch {
	case errors.Is(err, imessagewire.ErrNoChat):
		writeCode(w, http.StatusNotFound, proto.CodeNoChat, msg, nil)
	case errors.Is(err, imessagewire.ErrNoPermission):
		writeCode(w, http.StatusForbidden, proto.CodeNoPermission, msg, nil)
	case errors.Is(err, imessagewire.ErrNodeTooOld):
		writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, msg, nil)
	case errors.Is(err, imessagewire.ErrOffline):
		writeCode(w, http.StatusServiceUnavailable, "offline", msg, nil)
	default:
		h.logf("imessage: %s: %v", what, err)
		writeErr(w, http.StatusBadGateway, errors.New(msg))
	}
}
