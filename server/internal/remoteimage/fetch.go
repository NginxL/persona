package remoteimage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/netaddr"
	"github.com/nwverse/persona/server/internal/pinneddial"
)

// CheckURL is rawURL if the hub may fetch it: absolute http or https, a host, no user info, at most 4 KiB. Whether
// the host is on the open internet is decided when it is dialled (Public), on every redirect too.
func CheckURL(rawURL string) (*url.URL, error) {
	if len(rawURL) > maxURL {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "address longer than %d bytes", maxURL)
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "not an address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "not an http or https address")
	}
	if u.User != nil {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "address with user info")
	}
	if u.Hostname() == "" {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "address without a host")
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// check is CheckURL under the profile: an https address only, when it says so.
func (f *Fetcher) check(rawURL string) (*url.URL, error) {
	u, err := CheckURL(rawURL)
	if err != nil {
		return nil, err
	}
	if f.o.Profile.HTTPSOnly && u.Scheme != "https" {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "not an https address")
	}
	return u, nil
}

// notPublic are numbers the classes below call public that are no picture's host: cloud platforms' own services.
var notPublic = []netip.Addr{netip.MustParseAddr("168.63.129.16")}

// specialIPv6 is the IETF's special-purpose block inside the global range (Teredo, benchmarking, ORCHID and others).
var specialIPv6 = netip.MustParsePrefix("2001::/23")

// Public says whether ip is an ordinary public unicast address: a public IPv4 or global IPv6 one (netaddr.Classify),
// not one of the special-purpose ones within them. Loopback, private, shared (CGNAT), link-local, ULA, multicast,
// documentation, transition (6to4, Teredo), NAT64 and reserved addresses are not, nor the metadata services' (all of
// those are in the classes refused, or listed here).
func Public(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if slices.Contains(notPublic, ip) || specialIPv6.Contains(ip) {
		return false
	}
	switch netaddr.Classify(ip) {
	case netaddr.PublicIPv4, netaddr.GlobalIPv6:
		return true
	}
	return false
}

// newTransport dials only public addresses: each connection resolves its host once, refuses it if any answer is not
// Public, and dials the checked numbers (through the operator's proxy with a CONNECT to one of them, if the
// environment names one for it). No proxy of the transport's own, no compression (no decompression bombs), short
// header limits.
func (f *Fetcher) newTransport() *http.Transport {
	t := &http.Transport{
		Proxy:                  nil, // proxying happens in DialContext, after the check, to a number
		DialContext:            f.dialPublic,
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
	}
	if f.o.tlsInsecure {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // tests only (httptest's certificate)
	}
	return t
}

func (f *Fetcher) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fail(CodeBadURL, http.StatusBadRequest, "bad address")
	}
	var ips []net.IPAddr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []net.IPAddr{{IP: net.IP(ip.AsSlice()), Zone: ip.Zone()}}
	} else {
		ips, err = f.o.resolve(ctx, host)
		if err != nil {
			return nil, fail(CodeFetchFailed, http.StatusBadGateway, "%s does not resolve", host)
		}
	}
	if len(ips) == 0 || len(ips) > 64 {
		return nil, fail(CodeFetchFailed, http.StatusBadGateway, "%s has no usable address", host)
	}
	// Behind a fake-IP proxy every answer is the proxy's: the real ones come from DoH, or the picture is refused (doh.go).
	if fakeOnly(ips) {
		real, err := f.realAddrs(ctx, host)
		if err != nil {
			return nil, fail(CodeNotAllowed, http.StatusForbidden, "%s resolves only to a proxy's fake addresses, and DNS-over-HTTPS gave none: %v", host, err)
		}
		ips = real
	}
	// One answer that is not public refuses the name: a mixed answer is how a rebinding host looks.
	for _, a := range ips {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok || a.Zone != "" || !Public(ip.Unmap()) {
			return nil, fail(CodeNotAllowed, http.StatusForbidden, "%s is not on the open internet", host)
		}
	}
	scheme := "https"
	if port == "80" {
		scheme = "http"
	}
	upstream, err := f.o.proxy(&http.Request{URL: &url.URL{Scheme: scheme, Host: address}})
	if err != nil {
		return nil, fail(CodeFetchFailed, http.StatusBadGateway, "proxy: %v", err)
	}
	return pinneddial.Pinned(ctx, network, port, ips, func(ctx context.Context, network, target string) (net.Conn, error) {
		return pinneddial.ThroughProxy(ctx, upstream, network, target, f.o.dial)
	})
}

// userAgent is generic: it says what fetches, not who or for which device.
const userAgent = "Mozilla/5.0 (compatible; Persona image fetch)"

// fetch is the bytes at u, sniffed to a type the hub serves.
func (f *Fetcher) fetch(ctx context.Context, u *url.URL) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, f.o.timeout)
	defer cancel()
	data, status, err := f.get(ctx, u, false)
	if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		// Hotlink protection: the host refuses a request that names no page. Ask once more naming its own origin,
		// which tells it nothing it does not know.
		data, status, err = f.get(ctx, u, true)
	}
	if err != nil {
		var e *Error
		switch {
		case errors.As(err, &e):
			return nil, e
		case ctx.Err() != nil || isTimeout(err):
			return nil, fail(CodeTimeout, http.StatusGatewayTimeout, "%s took longer than %s", u.Hostname(), f.o.timeout)
		}
		if inner := errors.Unwrap(err); inner != nil {
			err = inner
		}
		return nil, fail(CodeFetchFailed, http.StatusBadGateway, "%s: %v", u.Hostname(), err)
	}
	if status != http.StatusOK {
		return nil, fail(CodeFetchFailed, http.StatusBadGateway, "%s answered %d", u.Hostname(), status)
	}
	if kind, _ := sniff(data); !f.o.Profile.served(kind) {
		return nil, f.o.Profile.unsupported()
	}
	return data, nil
}

// get makes one request (and its redirects): the body when the answer is 200, else only the status. With
// ownReferer every hop names its own origin as the Referer; without, none does (the client adds one on redirects,
// removed here).
func (f *Fetcher) get(ctx context.Context, u *url.URL, ownReferer bool) ([]byte, int, error) {
	referer := func(r *http.Request) {
		r.Header.Del("Referer")
		if ownReferer {
			r.Header.Set("Referer", r.URL.Scheme+"://"+r.URL.Host+"/")
		}
	}
	client := &http.Client{
		Transport: f.transport,
		Jar:       nil, // no cookies, ever
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fail(CodeFetchFailed, http.StatusBadGateway, "more than %d redirects", maxRedirects)
			}
			if _, err := f.check(r.URL.String()); err != nil {
				return err
			}
			referer(r)
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, fail(CodeBadURL, http.StatusBadRequest, "not an address")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg,image/gif,image/*;q=0.5")
	referer(req)
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.CopyN(io.Discard, res.Body, 4<<10) // lets the connection be kept for the next picture
		return nil, res.StatusCode, nil
	}
	most := int64(f.o.Profile.MaxSource)
	if res.ContentLength > most {
		return nil, 0, fail(CodeTooLarge, http.StatusRequestEntityTooLarge, "%d bytes, more than %d", res.ContentLength, most)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, most+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) > most {
		return nil, 0, fail(CodeTooLarge, http.StatusRequestEntityTooLarge, "more than %d bytes", most)
	}
	return data, http.StatusOK, nil
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// String for logs: the code and detail.
func (e *Error) String() string { return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Detail) }
