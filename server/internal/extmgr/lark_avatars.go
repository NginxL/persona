package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/lark"
	"github.com/nwverse/persona/server/internal/remoteimage"
)

// The legacy gateway owns a registered API prefix, independently of the messaging source named "lark".
func larkAvatarCache(owner string) string { return "_lark-" + owner }

func larkAccountNumber(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > lark.MaxAccount {
		return 0
	}
	return n
}

func (h *Manager) larkAvatar(ctx context.Context, caller, value, account string, size int) (*remoteimage.Image, error) {
	n := larkAccountNumber(account)
	if n == 0 || (!extwire.AvatarAddress(value) && !extwire.LarkMediaKey(value)) {
		return nil, &remoteimage.Error{Status: http.StatusNotFound, Code: remoteimage.CodeBadURL}
	}
	hp, _ := h.extensions.apiRoute("/api/v1/lark/media/")
	if hp == nil {
		return nil, &remoteimage.Error{Status: http.StatusNotFound, Code: remoteimage.CodeBadURL}
	}
	cache := larkAvatarCache(hp.id)
	if extwire.AvatarAddress(value) {
		return h.avatars.GetIn(ctx, caller, cache, value, size)
	}
	// The gateway can return a thumbnail for a multiple of 24 through 624. Keep that input size in the
	// original's cache key: a later 512px drawing must not reuse the first 72px thumbnail.
	edge := (remoteimage.Avatars.Snap(size) + 23) / 24 * 24
	params := url.Values{"ol_account": {strconv.Itoa(n)}, "size": {strconv.Itoa(edge)}}
	name := params.Encode() + "\x00" + value
	return h.avatars.Make(ctx, caller, cache, name, size, func(ctx context.Context) ([]byte, error) {
		end, err := hp.proc.BeginHTTP(ctx)
		if err != nil {
			return nil, err
		}
		defer end()
		res, err := larkAvatarRequest(ctx, hp, "media/"+url.PathEscape(value), params)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Lark media returned HTTP %d", res.StatusCode)
		}
		limit := int64(remoteimage.Avatars.MaxSource)
		if res.ContentLength > limit {
			return nil, &remoteimage.Error{Status: http.StatusRequestEntityTooLarge, Code: remoteimage.CodeTooLarge}
		}
		return io.ReadAll(io.LimitReader(res.Body, limit+1))
	})
}

// A fixed request to the registered plugin. Its transport never follows an upstream redirect.
func larkAvatarRequest(ctx context.Context, hp *hubExtension, path string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://plugin/api/v1/lark/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = q.Encode()
	p := principalOf(ctx)
	req.Header.Set("X-Persona-Role", p.Role)
	req.Header.Set("X-Persona-Device", p.ID)
	return hp.proxy.Transport.RoundTrip(req)
}

// The legacy API uses a numbered gateway slot. The account_id in e.g. a conversation page is a
// signed-in Feishu user's identity and cannot select a gateway account. Read the selected slot from
// this same plugin before requesting the page, so a simultaneous default change cannot rebind media.
func defaultLarkAvatarAccount(ctx context.Context, hp *hubExtension) int {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := larkAvatarRequest(ctx, hp, "accounts", nil)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0
	}
	var list struct {
		Default int `json:"default"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, larkDefaultMost)).Decode(&list) != nil || list.Default < 1 || list.Default > lark.MaxAccount {
		return 0
	}
	return list.Default
}
