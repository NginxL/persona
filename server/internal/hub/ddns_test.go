package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netaddr"
)

func ddnsTestAddress(last byte) netaddr.Addr {
	// Synthetic global addresses exercise classification without deployment fixtures.
	return netaddr.Addr{
		IP: netip.AddrFrom16([16]byte{0x20, 15: last}), Interface: "eth0",
		InterfaceFlags: net.FlagUp, FlagsKnown: true, ScopeKnown: true,
		Flags: netaddr.FlagPermanent,
	}
}

func TestDDNSCloudflareAddressChange(t *testing.T) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret[:])
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old, next := ddnsTestAddress(1), ddnsTestAddress(2)
	var lists, patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing bearer authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			lists.Add(1)
			if r.URL.Path != "/client/v4/zones/zone/dns_records" || r.URL.Query().Get("type") != "AAAA" || r.URL.Query().Get("name") != "example.com" {
				t.Error("incorrect Cloudflare record lookup")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]string{{"id": "record", "content": old.IP.String()}}})
		case http.MethodPatch:
			patches.Add(1)
			if r.URL.Path != "/client/v4/zones/zone/dns_records/record" {
				t.Error("incorrect Cloudflare PATCH path")
			}
			var body struct {
				Type, Name, Content string
				TTL                 int
				Proxied             bool
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Type != "AAAA" || body.Name != "example.com" || body.Content != next.IP.String() || body.TTL != 300 || body.Proxied {
				t.Error("incorrect Cloudflare PATCH payload")
			}
			_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
		default:
			t.Error("unexpected Cloudflare method")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	d := newDDNSUpdater(DDNSConfig{Provider: "cloudflare", Record: "example.com", ZoneID: "zone", TokenFile: tokenFile, TTL: 300}, nil, nil)
	d.api = server.URL + "/client/v4"
	current := old
	d.list = func() ([]netaddr.Addr, error) {
		return []netaddr.Addr{current, {IP: netip.AddrFrom4([4]byte{0xc0, 0, 3, 1})}}, nil
	}
	if err := d.update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if patches.Load() != 0 {
		t.Fatal("an already matching DNS address was patched")
	}
	current = next
	for i := 0; i < 2; i++ {
		if err := d.update(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if lists.Load() != 2 || patches.Load() != 1 {
		t.Fatalf("got %d lookups and %d patches, want 2 and 1", lists.Load(), patches.Load())
	}
	if err := os.Chmod(tokenFile, 0644); err != nil {
		t.Fatal(err)
	}
	current = ddnsTestAddress(3)
	if err := d.update(context.Background()); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatal("insecure token file was accepted")
	}
	if lists.Load() != 2 || patches.Load() != 1 {
		t.Fatal("insecure token file reached the provider")
	}
}

func TestDDNSWebhookRetryAndSelection(t *testing.T) {
	stable := ddnsTestAddress(5)
	unknown, temporary, deprecated := ddnsTestAddress(1), ddnsTestAddress(2), ddnsTestAddress(3)
	unknown.FlagsKnown = false
	temporary.Flags |= netaddr.FlagTemporary
	deprecated.Flags |= netaddr.FlagDeprecated
	private := netip.AddrFrom4([4]byte{10, 0, 0, 1})
	public := netip.AddrFrom4([4]byte{0xc0, 0, 3, 1})
	var calls atomic.Int32
	received := make(chan ddnsAddresses, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect webhook method or content type")
		}
		var body ddnsAddresses
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("response body must not be logged"))
		}
		received <- body
	}))
	defer server.Close()
	var logs bytes.Buffer
	d := newDDNSUpdater(DDNSConfig{Provider: "webhook", Record: "example.com", WebhookURL: server.URL}, []string{private.String(), "example.com", public.String()}, log.New(&logs, "", 0).Printf)
	d.list = func() ([]netaddr.Addr, error) {
		return []netaddr.Addr{unknown, temporary, deprecated, stable}, nil
	}
	d.retryMin, d.retryMax = time.Millisecond, time.Millisecond
	d.wake = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.run(ctx)
	}()
	defer func() { cancel(); <-done }()
	for i := 0; i < 2; i++ {
		select {
		case got := <-received:
			if got != (ddnsAddresses{Record: "example.com", IPv6: stable.IP.String(), IPv4: public.String()}) {
				t.Fatal("webhook published an unexpected address")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("webhook retry did not arrive")
		}
	}
	// An unbuffered wake joins the successful attempt before cancellation.
	select {
	case d.wake <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("DDNS worker did not finish reconciliation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DDNS worker did not stop")
	}
	if err := d.update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("unchanged webhook addresses were republished")
	}
	if !strings.Contains(logs.String(), "HTTP 503") || strings.Contains(logs.String(), "response body must not be logged") {
		t.Fatal("DDNS failure logging did not redact the response")
	}
}

func TestDDNSCloudflareDefaultTTL(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("cf-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var gotTTL atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
		case http.MethodPost:
			var body struct {
				TTL int `json:"ttl"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			gotTTL.Store(int32(body.TTL))
			_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
		}
	}))
	defer server.Close()
	d := newDDNSUpdater(DDNSConfig{Provider: "cloudflare", Record: "hub.example.com", ZoneID: "zone", TokenFile: tokenFile}, nil, nil)
	d.api = server.URL + "/client/v4"
	d.list = func() ([]netaddr.Addr, error) { return []netaddr.Addr{ddnsTestAddress(9)}, nil }
	if err := d.update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotTTL.Load() != cloudflareAutoTTL {
		t.Fatalf("got default Cloudflare TTL %d, want %d", gotTTL.Load(), cloudflareAutoTTL)
	}
}

func TestAlidnsSignatureOfficialVector(t *testing.T) {
	// Official Alibaba Cloud OpenAPI V3 ACS3-HMAC-SHA256 verification vector.
	query := alidnsCanonicalQuery(map[string]string{
		"RegionId": "cn-shanghai",
		"ImageId":  "win2019_1809_x64_dtc_zh-cn_40G_alibase_20230811.vhd",
	})
	wantQuery := "ImageId=win2019_1809_x64_dtc_zh-cn_40G_alibase_20230811.vhd&RegionId=cn-shanghai"
	if query != wantQuery {
		t.Fatalf("canonical query = %q, want %q", query, wantQuery)
	}
	contentSHA256, auth := signAlidnsRequest(
		http.MethodPost,
		"ecs.cn-shanghai.aliyuncs.com",
		"/",
		query,
		nil,
		"RunInstances",
		"2014-05-26",
		"2023-10-26T10:22:32Z",
		"3156853299f313e23d1673dc12e1703d",
		"YourAccessKeyId",
		"YourAccessKeySecret",
	)
	wantHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if contentSHA256 != wantHash {
		t.Fatalf("content sha256 = %q, want %q", contentSHA256, wantHash)
	}
	wantAuth := "ACS3-HMAC-SHA256 Credential=YourAccessKeyId,SignedHeaders=host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-signature-nonce;x-acs-version,Signature=06563a9e1b43f5dfe96b81484da74bceab24a1d853912eee15083a6f0f3283c0"
	if auth != wantAuth {
		t.Fatalf("authorization = %q, want %q", auth, wantAuth)
	}
	if got := alidnsPercentEncode("2001:db8::1 @*~ "); got != "2001%3Adb8%3A%3A1%20%40%2A~%20" {
		t.Fatalf("percent encode = %q", got)
	}
	if rr, err := splitAlidnsRecord("a.b.example.co.uk", "example.co.uk"); err != nil || rr != "a.b" {
		t.Fatalf("split multi-level subdomain = %q, %v; want %q", rr, err, "a.b")
	}
}

func TestDDNSAlidnsAddressChangeCreateAndApex(t *testing.T) {
	var idRaw, secretRaw [16]byte
	if _, err := rand.Read(idRaw[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(secretRaw[:]); err != nil {
		t.Fatal(err)
	}
	accessKeyID := "ak" + hex.EncodeToString(idRaw[:])
	accessKeySecret := hex.EncodeToString(secretRaw[:])
	tokenFile := filepath.Join(t.TempDir(), "alidns-token")
	if err := os.WriteFile(tokenFile, []byte(accessKeyID+":"+accessKeySecret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	oldV6, nextV6 := ddnsTestAddress(1), ddnsTestAddress(2)
	publicV4 := netip.AddrFrom4([4]byte{0xc0, 0, 3, 1})
	fixedTime := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	var describes, updates, adds atomic.Int32
	var failAddA atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		action := r.Header.Get("x-acs-action")
		version := r.Header.Get("x-acs-version")
		date := r.Header.Get("x-acs-date")
		nonce := r.Header.Get("x-acs-signature-nonce")
		if version != alidnsAPIVersion || date != fixedTime.Format(alidnsDateLayout) || nonce == "" {
			t.Errorf("invalid ACS3 headers: version=%q date=%q nonce=%q", version, date, nonce)
		}
		// Recompute signature from the received query to verify wire-level compliance.
		params := map[string]string{}
		for k, vals := range r.URL.Query() {
			if len(vals) > 0 {
				params[k] = vals[0]
			}
		}
		wantHash, wantAuth := signAlidnsRequest(r.Method, r.Host, alidnsCanonicalURI(r.URL.Path), alidnsCanonicalQuery(params), nil, action, version, date, nonce, accessKeyID, accessKeySecret)
		if r.Header.Get("x-acs-content-sha256") != wantHash || r.Header.Get("Authorization") != wantAuth {
			t.Errorf("signature mismatch for action %s", action)
		}

		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch action {
		case "DescribeSubDomainRecords":
			describes.Add(1)
			if q.Get("DomainName") != "example.com" || q.Get("PageSize") != "2" {
				t.Errorf("unexpected DescribeSubDomainRecords query: %s", r.URL.RawQuery)
			}
			switch q.Get("Type") {
			case "AAAA":
				if q.Get("SubDomain") == "example.com" {
					_, _ = w.Write([]byte(`{"TotalCount":0,"DomainRecords":{"Record":[]}}`))
					return
				}
				if q.Get("SubDomain") != "hub.example.com" {
					t.Errorf("unexpected SubDomain %q", q.Get("SubDomain"))
				}
				// Use a non-canonical IPv6 form on the first lookup to verify IP equality comparison.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"TotalCount": 1,
					"DomainRecords": map[string]any{
						"Record": []map[string]any{{
							"RecordId": "rec-v6",
							"Value":    "2000:0000:0000:0000:0000:0000:0000:0001",
							"Line":     "oversea",
							"TTL":      600,
						}},
					},
				})
			case "A":
				_, _ = w.Write([]byte(`{"TotalCount":0,"DomainRecords":{"Record":[]}}`))
			default:
				t.Errorf("unexpected record type %q", q.Get("Type"))
			}
		case "UpdateDomainRecord":
			updates.Add(1)
			if q.Get("RecordId") != "rec-v6" || q.Get("RR") != "hub" || q.Get("Type") != "AAAA" ||
				q.Get("Value") != nextV6.IP.String() || q.Get("Line") != "oversea" || q.Get("TTL") != "600" {
				t.Errorf("unexpected UpdateDomainRecord query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"RequestId":"req-1","RecordId":"rec-v6"}`))
		case "AddDomainRecord":
			if failAddA.Load() && q.Get("Type") == "A" {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("secret-body-must-not-leak"))
				return
			}
			adds.Add(1)
			if q.Get("Type") == "A" {
				if q.Get("DomainName") != "example.com" || q.Get("RR") != "hub" || q.Get("Value") != publicV4.String() || q.Get("TTL") != "" {
					t.Errorf("unexpected AddDomainRecord A query: %s", r.URL.RawQuery)
				}
			} else if q.Get("Type") == "AAAA" {
				if q.Get("DomainName") != "example.com" || q.Get("RR") != "@" || q.Get("Value") != nextV6.IP.String() || q.Get("TTL") != "120" {
					t.Errorf("unexpected AddDomainRecord apex query: %s", r.URL.RawQuery)
				}
			}
			_, _ = w.Write([]byte(`{"RequestId":"req-2","RecordId":"rec-new"}`))
		default:
			t.Errorf("unexpected action %q", action)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	// 1. Existing matching AAAA is a no-op; missing A fails once and retries without re-updating AAAA.
	d := newDDNSUpdater(DDNSConfig{
		Provider:  "alidns",
		Record:    "Hub.Example.COM.",
		Domain:    "Example.COM.",
		TokenFile: tokenFile,
	}, []string{"https://" + publicV4.String() + ":18662"}, nil)
	d.api = server.URL
	d.now = func() time.Time { return fixedTime }
	current := oldV6
	d.list = func() ([]netaddr.Addr, error) { return []netaddr.Addr{current}, nil }

	failAddA.Store(true)
	err := d.update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("got error %v, want HTTP 503", err)
	}
	if strings.Contains(err.Error(), "secret-body-must-not-leak") || strings.Contains(err.Error(), accessKeyID) || strings.Contains(err.Error(), accessKeySecret) {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
	if updates.Load() != 0 || adds.Load() != 0 {
		t.Fatalf("got %d updates and %d adds after matching AAAA + failing A, want 0 and 0", updates.Load(), adds.Load())
	}

	// Retry: AAAA was already remembered as published, so only A is queried and created.
	failAddA.Store(false)
	if err := d.update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if describes.Load() != 3 || updates.Load() != 0 || adds.Load() != 1 {
		t.Fatalf("after A retry: got %d describes, %d updates, %d adds; want 3, 0, 1", describes.Load(), updates.Load(), adds.Load())
	}

	// 2. IPv6 changes -> updates AAAA, preserves Line and TTL, and skips duplicate call.
	current = nextV6
	for i := 0; i < 2; i++ {
		if err := d.update(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if describes.Load() != 4 || updates.Load() != 1 || adds.Load() != 1 {
		t.Fatalf("after AAAA change: got %d describes, %d updates, %d adds; want 4, 1, 1", describes.Load(), updates.Load(), adds.Load())
	}

	// 3. Apex domain creates record with RR="@" and explicit TTL.
	apex := newDDNSUpdater(DDNSConfig{
		Provider:  "alidns",
		Record:    "example.com",
		Domain:    "example.com",
		TokenFile: tokenFile,
		TTL:       120,
	}, nil, nil)
	apex.api = server.URL
	apex.now = func() time.Time { return fixedTime }
	apex.list = func() ([]netaddr.Addr, error) { return []netaddr.Addr{nextV6}, nil }
	if err := apex.update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adds.Load() != 2 {
		t.Fatalf("apex add count = %d, want 2", adds.Load())
	}
}

func TestDDNSAlidnsValidationAndErrors(t *testing.T) {
	dir := t.TempDir()
	goodToken := filepath.Join(dir, "good")
	if err := os.WriteFile(goodToken, []byte("akid:aksecret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	badToken := filepath.Join(dir, "bad")
	if err := os.WriteFile(badToken, []byte("missing-colon\n"), 0600); err != nil {
		t.Fatal(err)
	}
	insecureToken := filepath.Join(dir, "insecure")
	if err := os.WriteFile(insecureToken, []byte("akid:aksecret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecureToken, 0644); err != nil { // WriteFile's mode is masked by the umask (077 leaves 0600)
		t.Fatal(err)
	}

	var responseBody atomic.Value
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody.Load().(string)))
	}))
	defer server.Close()

	newTestUpdater := func(cfg DDNSConfig) *ddnsUpdater {
		d := newDDNSUpdater(cfg, nil, nil)
		d.api = server.URL
		d.list = func() ([]netaddr.Addr, error) { return []netaddr.Addr{ddnsTestAddress(7)}, nil }
		return d
	}

	// Pre-flight validation rejects bad configs without calling the provider.
	invalidCases := []struct {
		name string
		cfg  DDNSConfig
		want string
	}{
		{"insecure_mode", DDNSConfig{Provider: "alidns", Record: "hub.example.com", Domain: "example.com", TokenFile: insecureToken}, "0600"},
		{"bad_credentials", DDNSConfig{Provider: "alidns", Record: "hub.example.com", Domain: "example.com", TokenFile: badToken}, "access_key_id:access_key_secret"},
		{"missing_domain", DDNSConfig{Provider: "alidns", Record: "hub.example.com", TokenFile: goodToken}, "public.ddns.domain"},
		{"record_outside_domain", DDNSConfig{Provider: "alidns", Record: "notexample.com", Domain: "example.com", TokenFile: goodToken}, "within public.ddns.domain"},
		{"empty_label", DDNSConfig{Provider: "alidns", Record: "a..example.com", Domain: "example.com", TokenFile: goodToken}, "full domain name"},
		{"negative_ttl", DDNSConfig{Provider: "alidns", Record: "hub.example.com", Domain: "example.com", TokenFile: goodToken, TTL: -1}, "ttl"},
		{"ttl_too_large", DDNSConfig{Provider: "alidns", Record: "hub.example.com", Domain: "example.com", TokenFile: goodToken, TTL: 86401}, "ttl"},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			err := newTestUpdater(tc.cfg).update(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got error %v, want substring %q", err, tc.want)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid configs made %d provider calls, want 0", calls.Load())
	}

	// Multiple matching records are refused without mutation.
	responseBody.Store(`{"TotalCount":2,"DomainRecords":{"Record":[{"RecordId":"1","Value":"2000::1"},{"RecordId":"2","Value":"2000::2"}]}}`)
	err := newTestUpdater(DDNSConfig{Provider: "alidns", Record: "hub.example.com", Domain: "example.com", TokenFile: goodToken}).update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "multiple Alibaba Cloud DNS records") {
		t.Fatalf("multiple records error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("multiple records caused %d calls, want 1", calls.Load())
	}

	// HTTP 200 with a JSON error Code is rejected without leaking the provider message.
	responseBody.Store(`{"Code":"InvalidAccessKeyId.NotFound","Message":"sensitive-provider-detail"}`)
	err = newTestUpdater(DDNSConfig{Provider: "alidns", Record: "hub.example.com", Domain: "example.com", TokenFile: goodToken}).update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Alibaba Cloud DNS rejected") || strings.Contains(err.Error(), "sensitive-provider-detail") {
		t.Fatalf("API error code handling = %v", err)
	}
}
