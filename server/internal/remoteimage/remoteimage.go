// Package remoteimage fetches the pictures a reply names by address (Markdown `![alt](https://…)`) for the apps, so
// they only ever talk to their own hub (#9473, docs/protocol/protocol.md "Remote images"). The third party sees the
// hub's address, never a device's; the apps get the size they draw instead of the original.
//
// What it fetches: an http or https address on the open internet. The host is resolved once and every answer must be
// a public unicast address (Public); only those numbers are dialled (pinneddial), on every redirect too, so a second
// DNS answer cannot move a fetch onto this machine, the LAN or a cloud's metadata service. The request carries no
// cookies, no credentials and no Referer, except one retry with the image's own origin when its host refuses a request
// without one (hotlink protection; it names nothing the host does not already know).
//
// What it answers: JPEG, PNG, GIF (its first frame) and WebP are decoded, turned upright (EXIF), scaled to fit the size
// asked (never enlarged) and encoded again (JPEG, or PNG where transparent), which also drops their metadata. HEIC and
// AVIF, which Go cannot decode, pass as they are under a smaller byte limit. Anything else, SVG included (it can carry
// scripts), is refused: the type is read from the bytes, never from Content-Type.
//
// Limits: bytes and pixels per source, a time per fetch, fetches and decodes at once, fetches per caller (cache hits
// are free), a short memory of failed addresses, and a disk cache with a total cap that drops the least recently used.
package remoteimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Sizes are the long sides (px) the apps may ask for; Snap rounds a request up to one of them.
var Sizes = []int{256, 512, 768, 1024, 1536, 2048}

// DefaultSize is the size served when none is asked for.
const DefaultSize = 1024

// The limits (docs/protocol/protocol.md "Remote images").
const (
	maxURL         = 4096
	maxSource      = 20 << 20 // bytes of a source the hub decodes
	maxPassThrough = 8 << 20  // bytes of a HEIC or AVIF passed as it is
	maxPixels      = 40_000_000
	maxRedirects   = 3
	fetchTimeout   = 15 * time.Second
	dialTimeout    = 5 * time.Second
	fetchSlots     = 6 // fetches at once, for every caller
	decodeSlots    = 2 // decodes at once: each may hold a large picture in memory
	cacheMax       = 512 << 20
	failedFor      = 2 * time.Minute // a failed address answers its error this long without being fetched again
	failedMost     = 1024
	// perCaller is how many fetches (cache misses) one caller may start in a minute, all of them at once at most.
	perCaller = 60
	// processing changes the cache keys when what is made from a source changes.
	processing = "v1"
)

// Codes of the refusals and failures (Error.Code), the route's JSON error codes.
const (
	CodeBadURL      = "bad_url"
	CodeNotAllowed  = "address_not_allowed"
	CodeTooLarge    = "too_large"
	CodeUnsupported = "unsupported_type"
	CodeBadImage    = "bad_image"
	CodeFetchFailed = "fetch_failed"
	CodeTimeout     = "timeout"
	CodeRateLimited = "rate_limited"
)

// Error is why an image could not be served: Code (above) and the HTTP status the route answers with.
type Error struct {
	Code   string
	Status int
	Detail string
	// RetryAfter is set with CodeRateLimited: when the caller may fetch again.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return "remote image: " + e.Code
	}
	return "remote image: " + e.Code + ": " + e.Detail
}

func fail(code string, status int, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Detail: fmt.Sprintf(format, args...)}
}

// Image is what the apps get: the bytes, their type, and their size when the hub drew them (0 for a passed HEIC/AVIF).
type Image struct {
	Type          string
	Data          []byte
	Width, Height int
}

// Options set up a Fetcher.
type Options struct {
	// Profile is what the Fetcher takes and makes (Pictures when it names no sizes).
	Profile Profile
	// CacheDir is where the pictures are kept (created 0700); "" keeps none on disk.
	CacheDir string
	// CacheMax is the disk cache's cap in bytes (0: 512 MiB).
	CacheMax int64
	// DoH is the DNS-over-HTTPS JSON API asked for a host's real addresses when the resolver answers only a fake-IP
	// proxy's (doh.go), read when one is needed; nil or "" asks none, and such answers are refused.
	DoH  func() string
	Logf func(format string, args ...any)

	// Seams for tests.
	resolve func(ctx context.Context, host string) ([]net.IPAddr, error)
	dial    func(ctx context.Context, network, address string) (net.Conn, error)
	proxy   func(*http.Request) (*url.URL, error)
	now     func() time.Time
	timeout time.Duration // fetchTimeout
	// tlsInsecure lets tests use an httptest TLS server (its own certificate).
	tlsInsecure bool
}

// Fetcher serves remote images. One per manager; safe for concurrent use.
type Fetcher struct {
	o         Options
	transport *http.Transport
	fetches   chan struct{}
	decodes   chan struct{}
	cache     *diskCache
	limits    *limiter
	dohClient *http.Client

	mu      sync.Mutex
	doh     map[string]dohEntry
	calls   map[string]*call
	failed  map[string]failure
	sources map[string]*call
}

type call struct {
	done chan struct{}
	img  *Image
	src  []byte
	err  error
}

type failure struct {
	err   *Error
	until time.Time
}

// New makes a Fetcher.
func New(o Options) *Fetcher {
	if o.CacheMax <= 0 {
		o.CacheMax = cacheMax
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.now == nil {
		o.now = time.Now
	}
	if len(o.Profile.Sizes) == 0 {
		o.Profile = Pictures
	}
	if o.timeout <= 0 {
		o.timeout = o.Profile.Timeout
	}
	if o.resolve == nil {
		o.resolve = net.DefaultResolver.LookupIPAddr
	}
	if o.dial == nil {
		o.dial = (&net.Dialer{Timeout: dialTimeout}).DialContext
	}
	if o.proxy == nil {
		o.proxy = http.ProxyFromEnvironment
	}
	f := &Fetcher{
		o:       o,
		fetches: make(chan struct{}, fetchSlots),
		decodes: make(chan struct{}, decodeSlots),
		limits:  newLimiter(o.Profile.PerCaller, time.Minute, o.now),
		calls:   map[string]*call{},
		failed:  map[string]failure{},
		sources: map[string]*call{},
		doh:     map[string]dohEntry{},
		// The DoH service is the operator's: reached as the machine reaches the internet (its proxy too).
		dohClient: &http.Client{Timeout: dohTimeout},
	}
	f.transport = f.newTransport()
	if o.CacheDir != "" {
		f.cache = &diskCache{dir: o.CacheDir, max: o.CacheMax, logf: o.Logf}
	}
	return f
}

// Snap is the size Pictures serves for a requested one (Profile.Snap).
func Snap(size int) int { return Pictures.Snap(size) }

func key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// version is what every cache key starts with: what is made from a source, and by which profile (Pictures' keys are
// as they were before profiles).
func (f *Fetcher) version() string {
	if f.o.Profile.Name == "" {
		return processing
	}
	return processing + "-" + f.o.Profile.Name
}

// Get is the image at rawURL scaled to fit size × size, for caller (who is asking, for the per-caller limit).
func (f *Fetcher) Get(ctx context.Context, caller, rawURL string, size int) (*Image, error) {
	return f.GetIn(ctx, caller, "", rawURL, size)
}

// GetIn is Get kept in sub, one subdirectory of the cache (a source's own, "" none).
func (f *Fetcher) GetIn(ctx context.Context, caller, sub, rawURL string, size int) (*Image, error) {
	if !validSub(sub) {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "bad cache directory %q", sub)
	}
	u, err := f.check(rawURL)
	if err != nil {
		return nil, err
	}
	return f.serve(ctx, caller, sub, u.String(), size, func(ctx context.Context) ([]byte, error) { return f.fetch(ctx, u) })
}

// Make is the picture whose bytes get gives (a source's own: an extension's media ref), scaled to fit size × size and
// kept in sub under name, a value that always names the same bytes. Its bytes are checked and drawn exactly as a
// fetched address's: types from the bytes, the profile's limits, one get however many ask, within caller's limit
// and the fetch slots, a failure answered from memory for a while.
func (f *Fetcher) Make(ctx context.Context, caller, sub, name string, size int, get func(context.Context) ([]byte, error)) (*Image, error) {
	if !validSub(sub) {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "bad cache directory %q", sub)
	}
	if name == "" {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "no name")
	}
	id := "\x00" + sub + "\x00" + name // never an address: those start with a scheme
	return f.serve(ctx, caller, sub, id, size, func(ctx context.Context) ([]byte, error) { return f.given(ctx, get) })
}

// serve is the picture id names at size: from the cache, or made from its source once however many ask.
func (f *Fetcher) serve(ctx context.Context, caller, sub, id string, size int, fetch func(context.Context) ([]byte, error)) (*Image, error) {
	size = f.o.Profile.Snap(size)
	out := inSub(sub, key(f.version(), "out", strconv.Itoa(size), id))
	if img := f.cache.image(out); img != nil {
		return img, nil
	}
	if e := f.failedLately(id); e != nil {
		return nil, e
	}
	f.mu.Lock()
	c, joined := f.calls[out]
	if !joined {
		c = &call{done: make(chan struct{})}
		f.calls[out] = c
	}
	f.mu.Unlock()
	if !joined {
		// Not tied to the first asker's request: those who joined it still wait for the answer.
		go func() {
			c.img, c.err = f.make(context.WithoutCancel(ctx), caller, sub, id, size, out, fetch)
			f.mu.Lock()
			delete(f.calls, out)
			f.mu.Unlock()
			close(c.done)
		}()
	}
	select {
	case <-c.done:
		return c.img, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Fit is fit (render.go) within the decode slots the fetches use: decoding may hold a large picture in memory, so a
// burst of pictures sent to the channels waits its turn rather than decoding all at once. A wait ends with ctx.
func (f *Fetcher) Fit(ctx context.Context, src []byte, most int) (*Image, error) {
	select {
	case f.decodes <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-f.decodes }()
	return fit(src, most)
}

// make fetches (or finds) the source, draws the size and keeps it.
func (f *Fetcher) make(ctx context.Context, caller, sub, id string, size int, out string, fetch func(context.Context) ([]byte, error)) (*Image, error) {
	src, err := f.source(ctx, caller, sub, id, fetch)
	if err != nil {
		return nil, err
	}
	select {
	case f.decodes <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	img, err := f.o.Profile.render(src, size)
	<-f.decodes
	if err != nil {
		f.remember(id, err)
		return nil, err
	}
	f.cache.put(out, img)
	return img, nil
}

// source is the bytes id names: from the disk cache, or fetched once however many sizes wait for them.
func (f *Fetcher) source(ctx context.Context, caller, sub, id string, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	k := inSub(sub, key(f.version(), "src", id))
	if img := f.cache.image(k); img != nil {
		return img.Data, nil
	}
	f.mu.Lock()
	c, joined := f.sources[k]
	if !joined {
		c = &call{done: make(chan struct{})}
		f.sources[k] = c
	}
	f.mu.Unlock()
	if !joined {
		c.src, c.err = f.fetchLimited(ctx, caller, fetch)
		if c.err == nil {
			f.cache.put(k, &Image{Type: "application/octet-stream", Data: c.src})
		} else {
			f.remember(id, c.err)
		}
		f.mu.Lock()
		delete(f.sources, k)
		f.mu.Unlock()
		close(c.done)
	}
	<-c.done
	return c.src, c.err
}

// fetchLimited is fetch within the caller's limit and the fetch slots: the only place a picture costs the caller.
func (f *Fetcher) fetchLimited(ctx context.Context, caller string, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	if wait, ok := f.limits.take(caller); !ok {
		return nil, &Error{Code: CodeRateLimited, Status: http.StatusTooManyRequests, Detail: "too many images fetched", RetryAfter: wait}
	}
	select {
	case f.fetches <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-f.fetches }()
	return fetch(ctx)
}

// given is the bytes get gives, held to what a fetch is: the profile's time and bytes, a type it serves. A failure of
// get's own is a failed fetch.
func (f *Fetcher) given(ctx context.Context, get func(context.Context) ([]byte, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, f.o.timeout)
	defer cancel()
	data, err := get(ctx)
	if err != nil {
		var e *Error
		switch {
		case errors.As(err, &e):
			return nil, e
		case ctx.Err() != nil || isTimeout(err):
			return nil, fail(CodeTimeout, http.StatusGatewayTimeout, "took longer than %s", f.o.timeout)
		}
		return nil, fail(CodeFetchFailed, http.StatusBadGateway, "%v", err)
	}
	if len(data) > f.o.Profile.MaxSource {
		return nil, fail(CodeTooLarge, http.StatusRequestEntityTooLarge, "%d bytes, more than %d", len(data), f.o.Profile.MaxSource)
	}
	if kind, _ := sniff(data); !f.o.Profile.served(kind) {
		return nil, f.o.Profile.unsupported()
	}
	return data, nil
}

// Drop forgets everything kept in sub (a source uninstalled, or its data deleted).
func (f *Fetcher) Drop(sub string) { f.cache.drop(sub) }

// failedLately is the error an address failed with in the last failedFor, if any.
func (f *Fetcher) failedLately(addr string) *Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x, ok := f.failed[addr]; ok {
		if f.o.now().Before(x.until) {
			return x.err
		}
		delete(f.failed, addr)
	}
	return nil
}

// remember keeps a failure for failedFor, except a caller's limit (another caller may fetch it) or a cancel.
func (f *Fetcher) remember(addr string, err error) {
	var e *Error
	if !errors.As(err, &e) || e.Code == CodeRateLimited {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.failed) >= failedMost {
		clear(f.failed)
	}
	f.failed[addr] = failure{err: e, until: f.o.now().Add(failedFor)}
}
