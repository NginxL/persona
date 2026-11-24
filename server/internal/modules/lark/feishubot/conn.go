package feishubot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The long connection (Open Platform "Receive events through a persistent connection"): the app asks for a WebSocket address with its id and secret, and
// events and card callbacks arrive on it as protobuf frames. The hub needs no public address; Feishu keeps the
// connection's events for the app, so one lost while reconnecting is delivered again (by event id: handlers dedupe).
//
// Protocol (as the official SDKs speak it):
//   - POST <domain>/callback/ws/endpoint {"AppID","AppSecret"} → {code, msg, data: {URL, ClientConfig}}; the URL's
//     query names the connection (device_id) and the service id pings carry.
//   - The client pings every ClientConfig.PingInterval seconds (a control frame, header type=ping); the pong may carry
//     a new ClientConfig.
//   - A data frame's headers say its type (event / card), message id and, for a message split in parts, sum and seq.
//     The client answers each with the same frame, the payload replaced by {"code":200,"data":<base64 JSON>}; a card
//     callback's data is the toast / card to show at once. Feishu waits about 3 s for it and then retries.

// eventTurnWait is how long an event waits for the one before it: the handler returns quickly, so only a stuck one
// is waited for that long.
const eventTurnWait = 3 * time.Second

type clientConfig struct {
	ReconnectCount    int `json:"ReconnectCount,omitempty"`
	ReconnectInterval int `json:"ReconnectInterval,omitempty"`
	ReconnectNonce    int `json:"ReconnectNonce,omitempty"`
	PingInterval      int `json:"PingInterval,omitempty"`
}

const (
	defaultPing   = 2 * time.Minute
	pongGrace     = 5 * time.Second
	minBackoff    = 2 * time.Second
	maxBackoff    = time.Minute
	partsLifetime = 10 * time.Second
)

// Conn is one app's long connection.
type Conn struct {
	api     *Client
	handler Handler
	logf    func(string, ...any)

	mu    sync.Mutex
	state ConnState
	ping  time.Duration
	parts map[string]*partial
	now   func() time.Time // the current moment (time.Now; overridable in tests)
}

type partial struct {
	bufs [][]byte
	at   time.Time
}

func NewConn(api *Client, handler Handler, logf func(string, ...any)) *Conn {
	return &Conn{api: api, handler: handler, logf: logf, ping: defaultPing, parts: map[string]*partial{}, now: time.Now}
}

// State is the connection's state now.
func (c *Conn) State() ConnState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Run keeps the connection up until ctx ends, reconnecting with backoff; the same failure is logged once.
func (c *Conn) Run(ctx context.Context) {
	backoff := minBackoff
	lastErr := ""
	for ctx.Err() == nil {
		start := time.Now()
		err := c.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = minBackoff
		}
		c.mu.Lock()
		c.state.Connected, c.state.Since, c.state.Error = false, time.Now(), err.Error()
		c.mu.Unlock()
		if msg := err.Error(); msg != lastErr {
			c.logf("feishu bot: %v; reconnecting (backoff up to %s)", err, maxBackoff)
			lastErr = msg
		}
		// Feishu asks for some jitter so that a fleet of clients does not come back at once.
		wait := backoff + time.Duration(rand.Int64N(int64(time.Second)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// endpoint asks for the connection's address.
func (c *Conn) endpoint(ctx context.Context) (string, *clientConfig, error) {
	body, _ := json.Marshal(map[string]string{"AppID": c.api.appID, "AppSecret": c.api.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api.domain+"/callback/ws/endpoint", bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("locale", "zh")
	resp, err := c.api.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			URL          string        `json:"URL"`
			ClientConfig *clientConfig `json:"ClientConfig"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil, fmt.Errorf("endpoint: HTTP %d: %w", resp.StatusCode, err)
	}
	if out.Code != 0 || out.Data == nil || out.Data.URL == "" {
		return "", nil, &APIError{Code: out.Code, Msg: out.Msg, Op: "endpoint"}
	}
	return out.Data.URL, out.Data.ClientConfig, nil
}

func (c *Conn) configure(cfg *clientConfig) {
	if cfg == nil || cfg.PingInterval <= 0 {
		return
	}
	c.mu.Lock()
	c.ping = time.Duration(cfg.PingInterval) * time.Second
	c.mu.Unlock()
}

func (c *Conn) pingEvery() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ping
}

// once runs one connection until it fails.
func (c *Conn) once(ctx context.Context) error {
	ectx, cancel := context.WithTimeout(ctx, 15*time.Second)
	raw, cfg, err := c.endpoint(ectx)
	cancel()
	if err != nil {
		return err
	}
	c.configure(cfg)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") {
		return fmt.Errorf("endpoint: bad address")
	}
	service, _ := strconv.ParseInt(u.Query().Get("service_id"), 10, 32)

	dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
	ws, resp, err := websocket.Dial(dctx, raw, &websocket.DialOptions{HTTPClient: c.api.http})
	dcancel()
	if err != nil {
		if resp != nil && resp.Header.Get("Handshake-Status") != "" {
			return fmt.Errorf("handshake: %s %s", resp.Header.Get("Handshake-Status"), resp.Header.Get("Handshake-Msg"))
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 << 20)
	c.mu.Lock()
	c.state.Connected, c.state.Since, c.state.Error = true, time.Now(), ""
	c.mu.Unlock()
	c.logf("feishu bot: connected")

	cctx, stop := context.WithCancel(ctx)
	defer stop()
	var wmu sync.Mutex
	write := func(f *Frame) error {
		wmu.Lock()
		defer wmu.Unlock()
		wctx, cancel := context.WithTimeout(cctx, 10*time.Second)
		defer cancel()
		return ws.Write(wctx, websocket.MessageBinary, f.Marshal())
	}
	go func() {
		for {
			ping := &Frame{Service: int32(service), Method: frameControl, Headers: []Header{{"type", "ping"}}}
			if err := write(ping); err != nil {
				stop()
				return
			}
			// A split message whose rest never arrives leaves a partial (up to the read limit in
			// size); piggyback the sweep on the ping so it is reaped even with no more data frames.
			c.sweepParts()
			select {
			case <-cctx.Done():
				return
			case <-time.After(c.pingEvery()):
			}
		}
	}()
	var lastEvent chan struct{} // closed once the last event frame read is handled
	for {
		// A peer gone without closing (sleep, NAT, network change) would leave Read waiting forever: Feishu answers
		// every ping, so silence longer than two of them means the connection is dead.
		rctx, cancel := context.WithTimeout(cctx, 2*c.pingEvery()+pongGrace)
		typ, data, err := ws.Read(rctx)
		cancel()
		if err != nil {
			if cctx.Err() != nil && ctx.Err() == nil {
				return errors.New("ping failed")
			}
			return fmt.Errorf("read: %w", err)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		f, err := UnmarshalFrame(data)
		if err != nil {
			continue
		}
		switch f.Method {
		case frameControl:
			if f.Header("type") == "pong" && len(f.Payload) > 0 {
				var cfg clientConfig
				if json.Unmarshal(f.Payload, &cfg) == nil {
					c.configure(&cfg)
				}
			}
		case frameData:
			if f.Header("type") != "event" {
				go c.data(cctx, f, write) // a card press: answered at once, not behind the events
				continue
			}
			// Events are handled in the order they came: each in its own goroutine, one sent right after another
			// could reach the handler first (two messages to the bot taken the wrong way round). One stuck in the
			// handler holds the next for eventTurnWait at most.
			prev, done := lastEvent, make(chan struct{})
			lastEvent = done
			go func() {
				defer close(done)
				if prev != nil {
					t := time.NewTimer(eventTurnWait)
					select {
					case <-prev:
					case <-t.C:
					}
					t.Stop()
				}
				c.data(cctx, f, write)
			}()
		}
	}
}

// data handles one data frame (or a part of one) and answers it.
func (c *Conn) data(ctx context.Context, f *Frame, write func(*Frame) error) {
	payload := f.Payload
	if sum := f.headerInt("sum"); sum > 1 {
		if payload = c.combine(f.Header("message_id"), sum, f.headerInt("seq"), payload); payload == nil {
			return
		}
	}
	typ := f.Header("type")
	if typ != "event" && typ != "card" {
		return
	}
	c.mu.Lock()
	c.state.LastEvent = time.Now()
	c.mu.Unlock()
	start := time.Now()
	var res any
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("handler panicked: %v", r)
			}
		}()
		res, err = c.handler(ctx, payload)
	}()
	code := http.StatusOK
	var body []byte
	if err != nil {
		c.logf("feishu bot: event: %v", err)
		code = http.StatusInternalServerError
	} else if res != nil {
		body, _ = json.Marshal(res)
	}
	resp, _ := json.Marshal(struct {
		Code    int               `json:"code"`
		Headers map[string]string `json:"headers"`
		Data    []byte            `json:"data"`
	}{code, nil, body})
	out := *f
	out.Headers = append(append([]Header{}, f.Headers...), Header{"biz_rt", strconv.FormatInt(time.Since(start).Milliseconds(), 10)})
	out.Payload = resp
	if err := write(&out); err != nil {
		c.logf("feishu bot: answer an event: %v", err)
	}
}

// combine collects the parts of a split message; it returns the whole payload once every part is in.
// The expiry clock restarts with every arriving part: a part proves the sender is still sending the
// message, so a slow link must not discard a message whose parts arrive more than partsLifetime apart.
func (c *Conn) combine(id string, sum, seq int, b []byte) []byte {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq < 0 || seq >= sum {
		return nil
	}
	p := c.parts[id]
	if p == nil || len(p.bufs) != sum {
		p = &partial{bufs: make([][]byte, sum)}
		c.parts[id] = p
	}
	p.at = now
	p.bufs[seq] = b
	// Other messages' partials expire against the clock of their own last arriving part; the id a
	// part just arrived for is live by definition and is deleted below when the message is complete.
	for k, q := range c.parts {
		if k != id && now.Sub(q.at) > partsLifetime {
			delete(c.parts, k)
		}
	}
	var all []byte
	for _, part := range p.bufs {
		if part == nil {
			return nil
		}
		all = append(all, part...)
	}
	delete(c.parts, id)
	return all
}

// sweepParts drops partials whose last part arrived more than partsLifetime ago. The ping tick
// calls it so abandoned partials are reaped even when no more split frames arrive.
func (c *Conn) sweepParts() {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, p := range c.parts {
		if now.Sub(p.at) > partsLifetime {
			delete(c.parts, k)
		}
	}
}
