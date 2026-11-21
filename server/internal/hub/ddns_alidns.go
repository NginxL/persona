package hub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	alidnsAPIVersion    = "2015-01-09"
	alidnsSignAlgorithm = "ACS3-HMAC-SHA256"
	alidnsDateLayout    = "2006-01-02T15:04:05Z"
	alidnsNonceBytes    = 16
	alidnsQueryPageSize = "2"
)

type alidnsErrorEnvelope struct {
	Code string `json:"Code"`
}

type alidnsSubDomainRecordsResponse struct {
	TotalCount    int `json:"TotalCount"`
	DomainRecords struct {
		Record []alidnsRecord `json:"Record"`
	} `json:"DomainRecords"`
}

type alidnsRecord struct {
	RecordID string `json:"RecordId"`
	Value    string `json:"Value"`
	Line     string `json:"Line"`
	TTL      int    `json:"TTL"`
}

func randomAlidnsNonce() (string, error) {
	var b [alidnsNonceBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("cannot generate Alibaba Cloud DNS request nonce")
	}
	return hex.EncodeToString(b[:]), nil
}

func alidnsCredentials(token string) (string, string, error) {
	accessKeyID, accessKeySecret, ok := strings.Cut(token, ":")
	if !ok || accessKeyID == "" || accessKeySecret == "" || strings.Contains(accessKeySecret, ":") {
		return "", "", errors.New("DDNS token file for alidns must contain access_key_id:access_key_secret")
	}
	return accessKeyID, accessKeySecret, nil
}

func splitAlidnsRecord(record, domain string) (string, error) {
	if domain == "" || !strings.Contains(domain, ".") || hasEmptyDNSLabel(domain) {
		return "", errors.New("public.ddns.domain must be a full domain name for alidns")
	}
	if hasEmptyDNSLabel(record) {
		return "", errors.New("public.ddns.record must be a full domain name")
	}
	if record == domain {
		// Alibaba Cloud DNS uses "@" as the host record for the zone apex.
		return "@", nil
	}
	suffix := "." + domain
	if !strings.HasSuffix(record, suffix) {
		return "", errors.New("public.ddns.record must be within public.ddns.domain")
	}
	return strings.TrimSuffix(record, suffix), nil
}

func hasEmptyDNSLabel(name string) bool {
	if name == "" {
		return true
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return true
		}
	}
	return false
}

// alidnsPercentEncode encodes s according to RFC 3986, as required by Alibaba Cloud OpenAPI V3 signing
// (space as %20, asterisk as %2A, tilde unencoded).
func alidnsPercentEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

func alidnsCanonicalURI(path string) string {
	if path == "" || path == "/" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = alidnsPercentEncode(part)
	}
	uri := strings.Join(parts, "/")
	if !strings.HasPrefix(uri, "/") {
		return "/" + uri
	}
	return uri
}

func alidnsCanonicalQuery(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(alidnsPercentEncode(k))
		b.WriteByte('=')
		b.WriteString(alidnsPercentEncode(params[k]))
	}
	return b.String()
}

// signAlidnsRequest computes the x-acs-content-sha256 digest and ACS3-HMAC-SHA256 Authorization header.
func signAlidnsRequest(method, host, canonicalURI, canonicalQuery string, payload []byte, action, version, date, nonce, accessKeyID, accessKeySecret string) (string, string) {
	payloadHash := sha256.Sum256(payload)
	contentSHA256 := hex.EncodeToString(payloadHash[:])

	// Header names are strictly sorted ascending to match CanonicalHeaders and SignedHeaders.
	headers := [6]struct{ name, value string }{
		{"host", strings.TrimSpace(host)},
		{"x-acs-action", strings.TrimSpace(action)},
		{"x-acs-content-sha256", contentSHA256},
		{"x-acs-date", strings.TrimSpace(date)},
		{"x-acs-signature-nonce", strings.TrimSpace(nonce)},
		{"x-acs-version", strings.TrimSpace(version)},
	}
	var canonicalHeaders, signedHeaders strings.Builder
	for i, h := range headers {
		canonicalHeaders.WriteString(h.name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(h.value)
		canonicalHeaders.WriteByte('\n')
		if i > 0 {
			signedHeaders.WriteByte(';')
		}
		signedHeaders.WriteString(h.name)
	}

	// CanonicalHeaders ends with '\n', so joining with '\n' inserts the required blank line before SignedHeaders.
	canonicalRequest := strings.Join([]string{
		strings.ToUpper(method),
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders.String(),
		contentSHA256,
	}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := alidnsSignAlgorithm + "\n" + hex.EncodeToString(canonicalHash[:])

	mac := hmac.New(sha256.New, []byte(accessKeySecret))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	auth := fmt.Sprintf("%s Credential=%s,SignedHeaders=%s,Signature=%s",
		alidnsSignAlgorithm, accessKeyID, signedHeaders.String(), signature)
	return contentSHA256, auth
}

func (d *ddnsUpdater) alidns(ctx context.Context, accessKeyID, accessKeySecret, rr, kind, address string) error {
	var listed alidnsSubDomainRecordsResponse
	queryParams := map[string]string{
		"SubDomain":  d.cfg.Record,
		"DomainName": d.cfg.Domain,
		"Type":       kind,
		"PageSize":   alidnsQueryPageSize,
	}
	if err := d.alidnsCall(ctx, "DescribeSubDomainRecords", queryParams, accessKeyID, accessKeySecret, &listed); err != nil {
		return err
	}
	records := listed.DomainRecords.Record
	if len(records) > 1 || listed.TotalCount > 1 {
		return errors.New("multiple Alibaba Cloud DNS records match the DDNS name and type")
	}
	if len(records) == 0 {
		params := map[string]string{
			"DomainName": d.cfg.Domain,
			"RR":         rr,
			"Type":       kind,
			"Value":      address,
		}
		if d.cfg.TTL > 0 {
			params["TTL"] = strconv.Itoa(d.cfg.TTL)
		}
		return d.alidnsCall(ctx, "AddDomainRecord", params, accessKeyID, accessKeySecret, nil)
	}
	existing := records[0]
	if ip, err := netip.ParseAddr(existing.Value); err == nil && ip.String() == address {
		return nil
	}
	if existing.RecordID == "" {
		return errors.New("missing Alibaba Cloud DNS record ID")
	}
	params := map[string]string{
		"RecordId": existing.RecordID,
		"RR":       rr,
		"Type":     kind,
		"Value":    address,
	}
	if existing.Line != "" {
		params["Line"] = existing.Line
	}
	if d.cfg.TTL > 0 {
		params["TTL"] = strconv.Itoa(d.cfg.TTL)
	} else if existing.TTL > 0 {
		// Preserve the existing record TTL when public.ddns.ttl is omitted.
		params["TTL"] = strconv.Itoa(existing.TTL)
	}
	return d.alidnsCall(ctx, "UpdateDomainRecord", params, accessKeyID, accessKeySecret, nil)
}

func (d *ddnsUpdater) alidnsCall(ctx context.Context, action string, params map[string]string, accessKeyID, accessKeySecret string, out any) error {
	u, err := url.Parse(d.api)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("invalid DDNS provider URL")
	}
	canonicalURI := alidnsCanonicalURI(u.Path)
	canonicalQuery := alidnsCanonicalQuery(params)
	date := d.now().UTC().Format(alidnsDateLayout)
	nonce, err := d.nonce()
	if err != nil {
		return err
	}
	contentSHA256, auth := signAlidnsRequest(
		http.MethodPost, u.Host, canonicalURI, canonicalQuery, nil,
		action, alidnsAPIVersion, date, nonce, accessKeyID, accessKeySecret,
	)

	target := u.Scheme + "://" + u.Host + canonicalURI
	if canonicalQuery != "" {
		target += "?" + canonicalQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return errors.New("cannot create DDNS request")
	}
	req.Header.Set("x-acs-action", action)
	req.Header.Set("x-acs-version", alidnsAPIVersion)
	req.Header.Set("x-acs-date", date)
	req.Header.Set("x-acs-signature-nonce", nonce)
	req.Header.Set("x-acs-content-sha256", contentSHA256)
	req.Header.Set("Authorization", auth)

	res, err := d.client.Do(req)
	if err != nil {
		return errors.New("DDNS provider request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("DDNS provider returned HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, ddnsMaxResponseBytes+1))
	if err != nil || len(raw) > ddnsMaxResponseBytes {
		return errors.New("invalid Alibaba Cloud DNS response")
	}
	var envelope alidnsErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return errors.New("invalid Alibaba Cloud DNS response")
	}
	if envelope.Code != "" {
		return errors.New("Alibaba Cloud DNS rejected the DDNS request")
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("invalid Alibaba Cloud DNS response")
		}
	}
	return nil
}
