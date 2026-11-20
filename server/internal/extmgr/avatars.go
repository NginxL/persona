package extmgr

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/remoteimage"
)

// The avatars route (#10923, docs/protocol/protocol.md "Avatars"): every picture of a person or a conversation the
// apps show comes from here, by the hub path the lists name (extwire.AvatarURL). Its value is the source's own media
// ref, whose bytes its connector gives (Threads.Media), or an https address fetched on the open internet only
// (remoteimage's Avatars profile). Either way the picture is decoded and drawn again at the size asked, kept in
// cache/avatars/<source>/, and answered as a picture nothing may run: an extension's bytes are never served under the
// hub's origin as they came. Full devices only: no extension scope opens it (it would be a way out past an extension's
// own network permissions), and a limited device draws no such rows.

// avatarCacheMost is how many bytes of avatars are kept for every source together; the least recently used go first.
const avatarCacheMost = 256 << 20

// newAvatars is the avatars' fetcher, kept under dir's cache/avatars ("" keeps none on disk: tests).
func newAvatars(dir string, doh func() string, logf func(string, ...any)) *remoteimage.Fetcher {
	o := remoteimage.Options{Profile: remoteimage.Avatars, CacheMax: avatarCacheMost, DoH: doh, Logf: logf}
	if dir != "" {
		o.CacheDir = filepath.Join(dir, "cache", "avatars")
	}
	return remoteimage.New(o)
}

// avatarDoH is manager.json's remote_images.doh, as /remote-image reads it: the DNS-over-HTTPS service asked for a
// host's real addresses behind a fake-IP proxy (remoteimage.DefaultDoH when it names none; "" asks none).
func (h *Manager) avatarDoH() string {
	if d := h.cfg.RemoteImages.DoH; d != nil {
		return strings.TrimSpace(*d)
	}
	return remoteimage.DefaultDoH
}

func (h *Manager) avatarRoutes(api *http.ServeMux) {
	api.HandleFunc(extwire.AvatarsRoute, h.getAvatar)
}

func (h *Manager) getAvatar(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p.Role != roleFull || p.Extension != "" {
		writeErrCode(w, http.StatusForbidden, "forbidden", errors.New("avatars are for the user's own devices"))
		return
	}
	source := r.PathValue("source")
	value, ok := extwire.AvatarValue(r.PathValue("value"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, remoteimage.CodeBadURL, errors.New("not an avatar"))
		return
	}
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("size"))
	caller := cmp.Or(p.ID, p.Name, "hub")
	if source == "lark" && q.Has("lark_account") {
		img, err := h.larkAvatar(r.Context(), caller, value, q.Get("lark_account"), size)
		h.answerAvatar(w, source, img, err)
		return
	}
	c := h.connectors.Get(source)
	var messaging *hubExtension
	var item wire.MessagingItem
	fromMessaging := q.Get("messaging") == "1"
	if c == nil || fromMessaging {
		messaging, item = h.extensions.messagingOwner(source)
	}
	if (c == nil || fromMessaging) && messaging == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no source %q", source))
		return
	}
	var img *remoteimage.Image
	var err error
	cache := source
	if messaging != nil {
		cache = messagingAvatarCache(source)
	}
	if extwire.AvatarAddress(value) {
		img, err = h.avatars.GetIn(r.Context(), caller, cache, value, size)
	} else if messaging != nil {
		media, valid := messagingAvatarMedia(value)
		if !valid {
			writeErrCode(w, http.StatusNotFound, remoteimage.CodeBadURL, errors.New("not a messaging avatar"))
			return
		}
		// Foundation's URLComponents sends a literal plus as "+". The manager
		// emits spaces as "%20", so both opaque account IDs round-trip.
		avatarQuery, _ := url.ParseQuery(strings.ReplaceAll(r.URL.RawQuery, "+", "%2B"))
		account := avatarQuery.Get("account")
		if account == "" && slices.Contains(item.Capabilities, "accounts") {
			writeErrCode(w, http.StatusNotFound, remoteimage.CodeBadURL, errors.New("no account for this messaging avatar"))
			return
		}
		// A media key names bytes within one source account.
		name := messaging.id + "\x00" + url.QueryEscape(account) + "\x00" + value
		img, err = h.avatars.Make(r.Context(), caller, cache, name, size, func(ctx context.Context) ([]byte, error) {
			return messagingAvatar(ctx, messaging, source, media, account)
		})
	} else {
		th, ok := connector.As[connector.Threads](c)
		if !ok {
			writeErrCode(w, http.StatusNotFound, remoteimage.CodeBadURL, fmt.Errorf("%s has no pictures of its own", c.Name()))
			return
		}
		img, err = h.avatars.Make(r.Context(), caller, source, value, size, func(ctx context.Context) ([]byte, error) {
			return h.connectorAvatar(ctx, c, th, value)
		})
	}
	h.answerAvatar(w, source, img, err)
}

func (h *Manager) answerAvatar(w http.ResponseWriter, source string, img *remoteimage.Image, err error) {
	if err != nil {
		var e *remoteimage.Error
		if !errors.As(err, &e) {
			return // the caller went away
		}
		if e.Status >= http.StatusInternalServerError {
			h.logf("avatar %s: %s", source, e)
		}
		if e.Code == remoteimage.CodeRateLimited {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(e.RetryAfter.Seconds())))))
		}
		writeErrCode(w, e.Status, e.Code, e)
		return
	}
	writeAvatar(w, img)
}

// connectorAvatar is the bytes of c's media ref: kept from its conversation page when it was opened (the same ref
// always names the same bytes), else asked of the connector while it can be read.
func (h *Manager) connectorAvatar(ctx context.Context, c connector.Connector, th connector.Threads, ref string) ([]byte, error) {
	if m, ok := h.media.get(h.mediaDir(), c.ID(), ref); ok {
		return m.Data, nil
	}
	if st := connector.StatusOf(ctx, c); st.State != connector.StateOK {
		return nil, fmt.Errorf("%s: %s", c.Name(), cmp.Or(st.Error, st.State))
	}
	m, err := h.media.fetch(ctx, c.ID()+"\x00"+ref, func(ctx context.Context) (connector.Media, error) { return th.Media(ctx, ref) })
	return m.Data, err
}

// writeAvatar answers the picture as a picture of its own type that nothing may run or sniff as something else, kept
// by the app for good: its path names a value, and a value always names the same picture.
func writeAvatar(w http.ResponseWriter, img *remoteimage.Image) {
	hd := w.Header()
	hd.Set("Content-Type", img.Type)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("Cache-Control", "private, max-age=31536000, immutable")
	hd.Set("Content-Length", strconv.Itoa(len(img.Data)))
	_, _ = w.Write(img.Data)
}
