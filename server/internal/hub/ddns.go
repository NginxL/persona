package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/netaddr"
)

const (
	cloudflareDefaultAPI = "https://api.cloudflare.com/client/v4"
	alidnsDefaultAPI     = "https://alidns.aliyuncs.com"
	cloudflareAutoTTL    = 1
	cloudflareMinTTL     = 30
	ddnsMaxTTL           = 86400
	ddnsMaxResponseBytes = 1 << 20
)

// DDNSConfig optionally publishes the hub's stable addresses.
// TODO: Add DNSPod provider later.
type DDNSConfig struct {
	Provider   string `json:"provider"`
	Record     string `json:"record"`
	Domain     string `json:"domain,omitempty"`
	ZoneID     string `json:"zone_id,omitempty"`
	TokenFile  string `json:"token_file,omitempty"`
	WebhookURL string `json:"webhook_url,omitempty"`
	TTL        int    `json:"ttl,omitempty"`
}

type ddnsAddresses struct {
	Record string `json:"record"`
	IPv6   string `json:"ipv6"`
	IPv4   string `json:"ipv4"`
}

// All mutable updater state belongs to run's goroutine.
type ddnsUpdater struct {
	cfg       DDNSConfig
	advertise []string
	client    *http.Client
	api       string
	list      func() ([]netaddr.Addr, error)
	logf      func(string, ...any)
	now       func() time.Time
	nonce     func() (string, error)
	wake      chan struct{}
	retryMin  time.Duration
	retryMax  time.Duration
	selected  netaddr.Addr
	published ddnsAddresses
}

func defaultDDNSAPI(provider string) string {
	switch provider {
	case "alidns":
		return alidnsDefaultAPI
	default:
		return cloudflareDefaultAPI
	}
}

func newDDNSUpdater(cfg DDNSConfig, advertise []string, logf func(string, ...any)) *ddnsUpdater {
	cfg.Record = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cfg.Record)), ".")
	cfg.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cfg.Domain)), ".")
	return &ddnsUpdater{
		cfg: cfg, advertise: append([]string(nil), advertise...), logf: logf,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		api: defaultDDNSAPI(cfg.Provider), list: netaddr.List,
		now: time.Now, nonce: randomAlidnsNonce,
		wake: make(chan struct{}, 1), retryMin: time.Second, retryMax: 5 * time.Minute,
	}
}

func (h *Hub) startDDNS(ctx context.Context) {
	if h.cfg.Public.DDNS == nil {
		return
	}
	h.public.ddns = newDDNSUpdater(*h.cfg.Public.DDNS, h.cfg.Public.Advertise, h.logf)
	go h.public.ddns.run(ctx)
}

func (h *Hub) updateDDNS() {
	if d := h.public.ddns; d != nil {
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
}

func (d *ddnsUpdater) run(ctx context.Context) {
	// The initial attempt also covers configured endpoints that did not change.
	timer := time.NewTimer(0)
	defer timer.Stop()
	retry := timer.C
	delay := d.retryMin
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			if retry != nil {
				continue
			}
		case <-retry:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		retry = nil
		if ctx.Err() != nil {
			return
		}
		if err := d.update(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			// Errors never include credentials, URLs, response bodies or raw transport errors.
			if d.logf != nil {
				d.logf("DDNS update failed: %v; retry scheduled", err)
			}
			timer.Reset(delay)
			retry = timer.C
			delay = min(delay*2, d.retryMax)
		} else {
			delay = d.retryMin
		}
	}
}

func (d *ddnsUpdater) update(ctx context.Context) error {
	if d.cfg.Record == "" || !strings.Contains(d.cfg.Record, ".") {
		return errors.New("public.ddns.record must be a full domain name")
	}
	addrs, err := d.list()
	if err != nil {
		return errors.New("cannot discover stable DDNS addresses")
	}
	next := ddnsAddresses{Record: d.cfg.Record}
	if selected, ok := netaddr.Select(addrs, d.selected); ok {
		d.selected = selected
		next.IPv6 = selected.IP.String()
	} else {
		d.selected = netaddr.Addr{}
	}
	// Neither public_url nor an automatically discovered IPv4 proves public ingress.
	for _, raw := range d.advertise {
		endpoint, ok := configuredEndpoint(raw, "https", 443, time.Time{})
		if !ok {
			continue
		}
		u, _ := url.Parse(endpoint.URL)
		ip, err := netip.ParseAddr(u.Hostname())
		if err == nil && ip.Zone() == "" && netaddr.Classify(ip) == netaddr.PublicIPv4 {
			next.IPv4 = ip.Unmap().String()
			break
		}
	}
	if next == d.published || (next.IPv6 == "" && next.IPv4 == "") {
		return nil
	}
	token, err := ddnsToken(d.cfg.TokenFile, d.cfg.Provider == "cloudflare" || d.cfg.Provider == "alidns")
	if err != nil {
		return err
	}
	switch d.cfg.Provider {
	case "webhook":
		if err := d.request(ctx, http.MethodPost, d.cfg.WebhookURL, token, next, nil); err != nil {
			return err
		}
	case "cloudflare":
		ttl := d.cfg.TTL
		if ttl == 0 {
			ttl = cloudflareAutoTTL // Cloudflare's automatic TTL.
		}
		if d.cfg.ZoneID == "" || (ttl != cloudflareAutoTTL && (ttl < cloudflareMinTTL || ttl > ddnsMaxTTL)) {
			return errors.New("invalid Cloudflare zone_id or ttl")
		}
		if err := d.publishFamilies(next, func(kind, address string) error {
			return d.cloudflare(ctx, token, kind, address, ttl)
		}); err != nil {
			return err
		}
	case "alidns":
		accessKeyID, accessKeySecret, err := alidnsCredentials(token)
		if err != nil {
			return err
		}
		rr, err := splitAlidnsRecord(d.cfg.Record, d.cfg.Domain)
		if err != nil {
			return err
		}
		if d.cfg.TTL < 0 || d.cfg.TTL > ddnsMaxTTL {
			return errors.New("invalid Alibaba Cloud DNS ttl")
		}
		if err := d.publishFamilies(next, func(kind, address string) error {
			return d.alidns(ctx, accessKeyID, accessKeySecret, rr, kind, address)
		}); err != nil {
			return err
		}
	default:
		return errors.New("unsupported DDNS provider")
	}
	d.published = next
	return nil
}

// publishFamilies remembers each successful family separately so a partial failure is not republished.
func (d *ddnsUpdater) publishFamilies(next ddnsAddresses, publish func(kind, address string) error) error {
	for _, record := range []struct {
		kind, address string
		published     *string
	}{
		{"AAAA", next.IPv6, &d.published.IPv6},
		{"A", next.IPv4, &d.published.IPv4},
	} {
		if record.address != "" && record.address != *record.published {
			if err := publish(record.kind, record.address); err != nil {
				return err
			}
			*record.published = record.address
		}
	}
	return nil
}

func ddnsToken(path string, required bool) (string, error) {
	if path == "" {
		if required {
			return "", errors.New("public.ddns.token_file is required")
		}
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open DDNS token file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("DDNS token file must be a regular file with mode 0600")
	}
	b, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(b) > 16384 {
		return "", errors.New("cannot read DDNS token file")
	}
	token := strings.TrimSpace(string(b))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return "", errors.New("DDNS token file contains an invalid token")
	}
	return token, nil
}

type ddnsCloudflareResponse struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
}

func (d *ddnsUpdater) cloudflare(ctx context.Context, token, kind, address string, ttl int) error {
	base := d.api + "/zones/" + url.PathEscape(d.cfg.ZoneID) + "/dns_records"
	query := url.Values{"type": {kind}, "name": {d.cfg.Record}, "per_page": {"2"}}
	var response ddnsCloudflareResponse
	if err := d.request(ctx, http.MethodGet, base+"?"+query.Encode(), token, nil, &response); err != nil {
		return err
	}
	var records []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(response.Result, &records); err != nil {
		return errors.New("invalid Cloudflare record list")
	}
	if len(records) > 1 {
		return errors.New("multiple Cloudflare records match the DDNS name and type")
	}
	method, target := http.MethodPost, base
	if len(records) == 1 {
		if ip, err := netip.ParseAddr(records[0].Content); err == nil && ip.String() == address {
			return nil
		}
		if records[0].ID == "" {
			return errors.New("missing Cloudflare record ID")
		}
		method, target = http.MethodPatch, base+"/"+url.PathEscape(records[0].ID)
	}
	payload := struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		TTL     int    `json:"ttl"`
		Proxied bool   `json:"proxied"`
	}{kind, d.cfg.Record, address, ttl, false}
	return d.request(ctx, method, target, token, payload, &response)
}

func (d *ddnsUpdater) request(ctx context.Context, method, target, token string, body any, response *ddnsCloudflareResponse) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("invalid DDNS provider URL")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("cannot encode DDNS request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return errors.New("cannot create DDNS request")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return errors.New("DDNS provider request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("DDNS provider returned HTTP %d", res.StatusCode)
	}
	if response != nil {
		*response = ddnsCloudflareResponse{}
		if err := json.NewDecoder(io.LimitReader(res.Body, ddnsMaxResponseBytes)).Decode(response); err != nil {
			return errors.New("invalid Cloudflare response")
		}
		if !response.Success {
			return errors.New("Cloudflare rejected the DDNS request")
		}
	}
	return nil
}
