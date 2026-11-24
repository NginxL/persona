package feishubot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client makes the app's REST calls with a tenant access token it keeps fresh.
type Client struct {
	appID, appSecret string
	domain           string
	http             *http.Client

	mu         sync.Mutex
	token      string
	console    string // the developer console this client links out to, when not what the domain says (tests)
	expires    time.Time
	refresh    chan struct{}
	generation uint64
}

func New(appID, appSecret, domain string) *Client {
	return &Client{appID: appID, appSecret: appSecret, domain: ResolveDomain(domain), http: &http.Client{Timeout: 30 * time.Second}}
}

// WithHTTP replaces the HTTP client (tests).
func (c *Client) WithHTTP(h *http.Client) *Client {
	c.http = h
	return c
}

func (c *Client) AppID() string { return c.appID }

func (c *Client) Domain() string { return c.domain }

func (c *Client) tenantToken(ctx context.Context, fresh bool) (string, error) {
	var waited bool
	var generation uint64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		c.mu.Lock()
		if waited {
			// A successful exchange since we joined satisfies even a forced refresh.
			// Failure leaves the generation unchanged, so try with our own context.
			fresh = c.generation == generation
		}
		if pending := c.refresh; pending != nil {
			waited, generation = true, c.generation
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-pending:
				// Recheck the current exchange and cache; this result may be superseded.
				continue
			}
		}
		if !fresh && c.token != "" && time.Now().Before(c.expires) {
			token := c.token
			c.mu.Unlock()
			return token, nil
		}
		pending := make(chan struct{})
		c.refresh = pending
		c.mu.Unlock()

		token, expires, err := c.fetchTenantToken(ctx)
		c.mu.Lock()
		if err == nil {
			c.token, c.expires = token, expires
			c.generation++
		}
		c.refresh = nil
		close(pending)
		c.mu.Unlock()
		return token, err
	}
}

// fetchTenantToken performs HTTP outside the cache lock using the refreshing caller's context.
func (c *Client) fetchTenantToken(ctx context.Context) (string, time.Time, error) {
	body, _ := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.domain+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, fmt.Errorf("feishu token: HTTP %d: %w", resp.StatusCode, err)
	}
	if out.Code != 0 || out.Token == "" {
		return "", time.Time{}, &APIError{Op: "token", Code: out.Code, Msg: out.Msg}
	}
	return out.Token, time.Now().Add(time.Duration(max(out.Expire-300, 60)) * time.Second), nil
}

const (
	codeRateLimited     = 99991400         // the open platform's rate-limit body code
	codeChatRateLimited = 230020           // 5 messages a second into one chat or to one user; also a message updated too often
	chatLimitWindow     = time.Second      // 230020's window: a retry waits at least this long
	callMaxAttempts     = 3                // includes the first attempt; callSleep is swapped in tests, so no t.Parallel here
	callMaxRetryWait    = 60 * time.Second // a server-requested wait longer than this ends the retries instead of hanging on it
)

var (
	callBackoff = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond}
	callSleep   = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

// call sends one JSON request and decodes {code, msg, data} into out. Non-POST is safe to repeat; POST retries through callRetry only with an idempotency uuid.
func (c *Client) call(ctx context.Context, op, method, path string, in, out any) error {
	return c.callRetry(ctx, op, method, path, in, out, method != http.MethodPost)
}

func (c *Client) callRetry(ctx context.Context, op, method, path string, in, out any, idempotent bool) error {
	var payload []byte
	if in != nil {
		payload, _ = json.Marshal(in)
	}
	return c.callBody(ctx, op, method, path, payload, "application/json; charset=utf-8", out, idempotent)
}

func (c *Client) callBody(ctx context.Context, op, method, path string, payload []byte, contentType string, out any, idempotent bool) error {
	var lastErr error
	giveUp := func() error {
		if e := ctx.Err(); e != nil {
			return e // a cancel during the waits wins over the stored error
		}
		return lastErr
	}
	refreshed := false             // a forced refresh is allowed once, independently of transient attempts
	forcedFresh, waits := false, 0 // forcedFresh makes the next token fetch bypass the cache (after a tokenGone answer); waits counts backoff slots used
	for attempt := 0; attempt < callMaxAttempts; attempt++ {
		tok, err := c.tenantToken(ctx, forcedFresh)
		if err != nil {
			// Idempotent endpoint and unsent body: keep forcedFresh on failure so a forced fetch retries, never replays the stale token.
			lastErr = err
			if !c.waitRetry(ctx, attempt, waits, 0) {
				return giveUp()
			}
			waits++
			continue
		}
		forcedFresh = false // the fresh token is cached; a later transient retry reuses it
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.domain+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if payload != nil {
			req.Header.Set("Content-Type", contentType)
		}
		retry, after := false, time.Duration(0)
		resp, err := c.http.Do(req)
		switch {
		case err != nil:
			if ctx.Err() != nil { // a done context: retrying cannot help
				return err
			}
			lastErr, retry = err, true
		default:
			after = retryWait(resp.Header, resp.StatusCode, 0)
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			switch {
			case readErr != nil:
				if ctx.Err() != nil {
					return readErr
				}
				lastErr, retry = readErr, true
			default:
				var env struct {
					Code int             `json:"code"`
					Msg  string          `json:"msg"`
					Data json.RawMessage `json:"data"`
				}
				jerr := json.Unmarshal(raw, &env)
				after = retryWait(resp.Header, resp.StatusCode, env.Code)
				switch {
				case jerr != nil: // a gateway's HTML page on 429/5xx is transient; other non-JSON answers fail at once
					lastErr = fmt.Errorf("feishu %s: HTTP %d: %w", op, resp.StatusCode, jerr)
					retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
				case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
					if env.Code != 0 { // a parseable body on 429/5xx still means the request did not land
						lastErr = &APIError{Op: op, Code: env.Code, Msg: env.Msg}
					} else {
						lastErr = fmt.Errorf("feishu %s: HTTP %d", op, resp.StatusCode)
					}
					retry = true
				case env.Code != 0:
					if tokenGone(env.Code) && !refreshed && attempt+1 < callMaxAttempts {
						refreshed, forcedFresh = true, true
						continue // one fresh token, one immediate retry, no backoff slot spent
					}
					lastErr = &APIError{Op: op, Code: env.Code, Msg: env.Msg}
					// The per-chat limit is waited out only on a POST that gets here idempotent: a send or reply with a uuid,
					// which lands once. A card update that meets it is its caller's to slow down (it sends a newer card).
					retry = env.Code == codeRateLimited || env.Code == codeChatRateLimited && method == http.MethodPost
				case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
					// A gateway's JSON may omit code or report zero despite rejecting the request.
					// Keep its body out of the error and never decode it as a successful receipt.
					lastErr = fmt.Errorf("feishu %s: HTTP %d", op, resp.StatusCode)
				case out == nil:
					return nil
				case len(env.Data) > 0 && string(env.Data) != "null":
					return json.Unmarshal(env.Data, out)
				default:
					return json.Unmarshal(raw, out)
				}
			}
		}
		if updatesMessage(method) {
			after = 0 // the short backoff only: a longer wait the server names is the caller's (updatesMessage)
		}
		if !idempotent || !retry || !c.waitRetry(ctx, attempt, waits, after) {
			return giveUp()
		}
		waits++
	}
	return lastErr
}

// updatesMessage: a card update (PATCH) or an agent message's append (PUT). Such a call is retried on the short backoff
// only and does not sit out a longer wait the server names (x-ogw-ratelimit-reset, Retry-After): once the tries are
// used up, its caller gets the rate-limit error, backs off on RateLimited and sends what is newest by then. The
// progress tick makes these calls while it holds the run's progress lock, so waiting out a throttle window here would
// hold back the run's answer.
func updatesMessage(method string) bool {
	return method == http.MethodPatch || method == http.MethodPut
}

// waitRetry applies +/-25% jittered backoff (or a longer wait the server asked for) and reports whether to try once more.
// A wait that would outlast ctx's deadline is not started: the caller gets the last answer's error (a rate limit it can
// back off on) now, rather than a bare deadline error once the wait has used up its time.
func (c *Client) waitRetry(ctx context.Context, attempt, n int, after time.Duration) bool {
	if attempt+1 >= callMaxAttempts || n >= len(callBackoff) || after > callMaxRetryWait {
		return false
	}
	base := callBackoff[n]
	d := max(base*3/4+time.Duration(rand.Int64N(int64(base/2))), after) // the server's throttle window wins over the short backoff
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
		return false
	}
	return callSleep(ctx, d) == nil
}

// retryWait is the least wait before an answer is tried again. A throttled answer (HTTP 429, or code 99991400 on the
// older APIs' 400) names it in x-ogw-ratelimit-reset, the seconds until the gateway's window resets; a standard
// Retry-After counts when it is longer. The per-chat send limit's window is a second.
// https://open.feishu.cn/document/server-docs/api-call-guide/frequency-control
func retryWait(h http.Header, status, code int) time.Duration {
	after := retryAfterDelay(h.Get("Retry-After"))
	if status == http.StatusTooManyRequests || code == codeRateLimited {
		if secs, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Ogw-Ratelimit-Reset"))); err == nil && secs > 0 {
			after = max(after, time.Duration(secs)*time.Second)
		}
	}
	if code == codeChatRateLimited {
		after = max(after, chatLimitWindow)
	}
	return after
}

// retryAfterDelay parses a standard Retry-After header: delta seconds or an HTTP date.
func retryAfterDelay(v string) time.Duration {
	v = strings.TrimSpace(v)
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && time.Until(t) > 0 {
		return time.Until(t)
	}
	return 0
}

func (c *Client) BotInfo(ctx context.Context) (BotInfo, error) {
	var out struct {
		Bot BotInfo `json:"bot"`
	}
	err := c.call(ctx, "bot info", http.MethodGet, "/open-apis/bot/v3/info", nil, &out)
	return out.Bot, err
}

type sent struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id"`
}

// Send posts a message to a user (idType "open_id") or into a chat ("chat_id"); uuid (≤ 50 chars, may be "") makes
// a retried send land once.
func (c *Client) Send(ctx context.Context, idType, id, msgType, content, uuid string) (string, error) {
	var out sent
	in := map[string]any{"receive_id": id, "msg_type": msgType, "content": content}
	if uuid != "" {
		in["uuid"] = uuid
	}
	// uuid makes a retried send land once: with it, transient failures are retried; without it, a POST is not.
	err := c.callRetry(ctx, "send", http.MethodPost, "/open-apis/im/v1/messages?receive_id_type="+url.QueryEscape(idType), in, &out, uuid != "")
	return out.MessageID, err
}

// SendUser posts a message to a user by open id, and gives the chat it landed in: the bot's private chat with them,
// which the platform makes on the first message.
func (c *Client) SendUser(ctx context.Context, openID, msgType, content, uuid string) (messageID, chatID string, err error) {
	var out sent
	in := map[string]any{"receive_id": openID, "msg_type": msgType, "content": content}
	if uuid != "" {
		in["uuid"] = uuid
	}
	err = c.callRetry(ctx, "send", http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=open_id", in, &out, uuid != "")
	return out.MessageID, out.ChatID, err
}

// Reply answers a message (quoting it); inThread puts the answer in the message's thread.
func (c *Client) Reply(ctx context.Context, messageID, msgType, content, uuid string, inThread bool) (string, error) {
	var out sent
	in := map[string]any{"msg_type": msgType, "content": content}
	if uuid != "" {
		in["uuid"] = uuid
	}
	if inThread {
		in["reply_in_thread"] = true
	}
	err := c.callRetry(ctx, "reply", http.MethodPost, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reply", in, &out, uuid != "")
	return out.MessageID, err
}

// React puts an emoji under a message (emoji_type: OK, DONE, OnIt…) and returns the reaction's id.
func (c *Client) React(ctx context.Context, messageID, emoji string) (string, error) {
	var out struct {
		ReactionID string `json:"reaction_id"`
	}
	err := c.call(ctx, "react", http.MethodPost, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reactions",
		map[string]any{"reaction_type": map[string]string{"emoji_type": emoji}}, &out)
	return out.ReactionID, err
}

// Unreact takes a reaction away.
func (c *Client) Unreact(ctx context.Context, messageID, reactionID string) error {
	return c.call(ctx, "unreact", http.MethodDelete, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reactions/"+url.PathEscape(reactionID), nil, nil)
}

// Pin pins a message in its chat (needs im:message.pins:write_only).
func (c *Client) Pin(ctx context.Context, messageID string) error {
	return c.call(ctx, "pin", http.MethodPost, "/open-apis/im/v1/pins", map[string]any{"message_id": messageID}, nil)
}

// Unpin takes a message off its chat's pins.
func (c *Client) Unpin(ctx context.Context, messageID string) error {
	return c.call(ctx, "unpin", http.MethodDelete, "/open-apis/im/v1/pins/"+url.PathEscape(messageID), nil, nil)
}

// pinsPagesMost bounds how many pages of a chat's pins are read (50 each).
const pinsPagesMost = 4

// Pins are a chat's pins, newest first (needs im:message.pins:read).
func (c *Client) Pins(ctx context.Context, chatID string) ([]Pin, error) {
	var all []Pin
	token := ""
	for range pinsPagesMost {
		var out struct {
			Items     []Pin  `json:"items"`
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		}
		path := "/open-apis/im/v1/pins?page_size=50&chat_id=" + url.QueryEscape(chatID)
		if token != "" {
			path += "&page_token=" + url.QueryEscape(token)
		}
		if err := c.call(ctx, "pins", http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if !out.HasMore || out.PageToken == "" {
			break
		}
		token = out.PageToken
	}
	return all, nil
}

// Recall withdraws a message the bot sent.
func (c *Client) Recall(ctx context.Context, messageID string) error {
	return c.call(ctx, "recall", http.MethodDelete, "/open-apis/im/v1/messages/"+url.PathEscape(messageID), nil, nil)
}

// UpdateCard replaces a card the bot sent (shared cards: update_multi).
func (c *Client) UpdateCard(ctx context.Context, messageID, card string) error {
	return c.call(ctx, "update card", http.MethodPatch, "/open-apis/im/v1/messages/"+url.PathEscape(messageID), map[string]any{"content": card}, nil)
}

func (c *Client) Message(ctx context.Context, messageID string) ([]Item, error) {
	var out struct {
		Items []Item `json:"items"`
	}
	err := c.call(ctx, "read message", http.MethodGet, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"?user_id_type=open_id", nil, &out)
	return out.Items, err
}

// UserName is a user's name (contact API; needs a contact permission, so "" with an error when the app lacks it).
func (c *Client) UserName(ctx context.Context, openID string) (string, error) {
	var out struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	err := c.call(ctx, "user", http.MethodGet, "/open-apis/contact/v3/users/"+url.PathEscape(openID)+"?user_id_type=open_id", nil, &out)
	return out.User.Name, err
}

// Resource downloads an image or file of a message the bot received (kind "image" or "file"); at most limit bytes.
// It is a GET, so like call it tries again on a throttled answer, a server failure or a dropped connection, and renews
// a stale token once. https://open.feishu.cn/document/server-docs/im-v1/message/get-2
func (c *Client) Resource(ctx context.Context, messageID, key, kind string, limit int64) ([]byte, string, error) {
	u := c.domain + "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/resources/" + url.PathEscape(key) + "?type=" + url.QueryEscape(kind)
	var lastErr error
	refreshed := false
	forcedFresh, waits := false, 0
	for attempt := 0; attempt < callMaxAttempts; attempt++ {
		tok, err := c.tenantToken(ctx, forcedFresh)
		if err != nil {
			return nil, "", err
		}
		forcedFresh = false
		b, ct, retry, after, err := c.resourceOnce(ctx, u, tok, limit)
		var apiErr *APIError
		switch {
		case err == nil:
			return b, ct, nil
		case errors.As(err, &apiErr) && tokenGone(apiErr.Code) && !refreshed && attempt+1 < callMaxAttempts:
			refreshed, forcedFresh = true, true
			continue // one fresh token, one immediate retry, no backoff slot spent
		case !retry:
			return nil, "", err
		}
		lastErr = err
		if !c.waitRetry(ctx, attempt, waits, after) {
			if e := ctx.Err(); e != nil {
				return nil, "", e // a cancel during the wait wins over the stored error
			}
			return nil, "", err
		}
		waits++
	}
	return nil, "", lastErr
}

// resourceOnce makes one download. retry: the failure may pass (a throttle, a server failure, the connection); after:
// the wait the server asked for first.
func (c *Client) resourceOnce(ctx context.Context, u, tok string, limit int64) (b []byte, ct string, retry bool, after time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", ctx.Err() == nil, 0, err // a done context: retrying cannot help
	}
	defer resp.Body.Close()
	// Only a non-200 answer is an error. A 200 is the file whatever its type (a .json file the user sent comes
	// labeled application/json), as in the official SDK, which never reads a 200 download as an error.
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env)
		retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || env.Code == codeRateLimited
		after = retryWait(resp.Header, resp.StatusCode, env.Code)
		if env.Code == 0 {
			env.Code, env.Msg = resp.StatusCode, http.StatusText(resp.StatusCode)
		}
		return nil, "", retry, after, &APIError{Op: "download", Code: env.Code, Msg: env.Msg}
	}
	if resp.ContentLength > limit { // said up front: refused without downloading it
		return nil, "", false, 0, fmt.Errorf("%w: larger than %d MB", ErrTooLarge, limit>>20)
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", ctx.Err() == nil, 0, err
	}
	if int64(len(b)) > limit {
		return nil, "", false, 0, fmt.Errorf("%w: larger than %d MB", ErrTooLarge, limit>>20)
	}
	return b, resp.Header.Get("Content-Type"), false, 0, nil
}
