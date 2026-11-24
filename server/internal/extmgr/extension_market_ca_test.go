package extmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestMarketClientCAFile(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/persona-extensions.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"market_version":1,"name":"Private test","extensions":[]}`))
	}))
	caPEM := marketTestTLSCertificate(t, srv)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	spec, err := extension.ParseMarket(srv.URL + "/persona-extensions.json")
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(client *http.Client) error {
		mk := &extension.Market{Spec: spec, Client: client}
		index, _, err := mk.Fetch(context.Background())
		if err == nil && index.Name != "Private test" {
			t.Fatalf("catalog name %q", index.Name)
		}
		return err
	}

	t.Run("unset keeps the system-only client", func(t *testing.T) {
		var baseRoots *x509.CertPool
		if tlsConfig := http.DefaultTransport.(*http.Transport).TLSClientConfig; tlsConfig != nil {
			baseRoots = tlsConfig.RootCAs
		}
		client, err := marketCAClient("")
		if err != nil {
			t.Fatal(err)
		}
		transport := client.Transport.(*http.Transport)
		var gotRoots *x509.CertPool
		if transport.TLSClientConfig != nil {
			if transport.TLSClientConfig.InsecureSkipVerify {
				t.Fatal("certificate verification is disabled")
			}
			gotRoots = transport.TLSClientConfig.RootCAs
		}
		if gotRoots != baseRoots {
			t.Fatal("unset changed the default root pool")
		}
		err = marketClientGetError(t, client, srv.URL+"/persona-extensions.json")
		var unknownAuthority x509.UnknownAuthorityError
		if !errors.As(err, &unknownAuthority) {
			t.Fatalf("unset trust failed for an unexpected reason: %T %v", err, err)
		}
		if _, err := extension.ParseMarket("http://market.example/persona-extensions.json"); err == nil {
			t.Fatal("plaintext marketplace accepted")
		}
	})

	t.Run("configured appends the CA without weakening verification", func(t *testing.T) {
		caFile := filepath.Join(t.TempDir(), "market-ca.pem")
		if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := x509.SystemCertPool()
		if err != nil {
			t.Fatalf("system roots: %v", err)
		}
		block, _ := pem.Decode(caPEM)
		if block == nil {
			t.Fatal("test CA is not PEM")
		}
		added, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		client, err := marketCAClient(caFile)
		if err != nil {
			t.Fatal(err)
		}
		transport := client.Transport.(*http.Transport)
		if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
			t.Fatal("the configured CA pool is missing")
		}
		if transport.TLSClientConfig.InsecureSkipVerify {
			t.Fatal("certificate verification is disabled")
		}
		wantRoots := before.Clone()
		wantRoots.AddCert(added)
		if !transport.TLSClientConfig.RootCAs.Equal(wantRoots) {
			t.Fatal("the configured pool is not the system roots plus the private test CA")
		}
		if err := fetch(client); err != nil {
			t.Fatalf("configured private CA fetch: %v", err)
		}
		t.Log("configured private CA fetch succeeded with verification enabled")

		wrongHostURL := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/persona-extensions.json"
		err = marketClientGetError(t, client, wrongHostURL)
		var hostname x509.HostnameError
		if !errors.As(err, &hostname) {
			t.Fatalf("a certificate without the requested hostname passed: %T %v", err, err)
		}

		other, err := marketCAClient("")
		if err != nil {
			t.Fatal(err)
		}
		err = marketClientGetError(t, other, srv.URL+"/persona-extensions.json")
		var otherUnknownAuthority x509.UnknownAuthorityError
		if !errors.As(err, &otherUnknownAuthority) {
			t.Fatalf("the other client failed for an unexpected reason: %T %v", err, err)
		}
	})

	for _, tc := range []struct {
		name, contents, want string
		create, directory    bool
	}{
		{name: "missing file", want: "read"},
		{name: "directory", directory: true, want: "not a regular file"},
		{name: "empty file", create: true, want: "no PEM certificates"},
		{name: "malformed PEM", create: true, contents: "not a certificate", want: "no PEM certificates"},
		{name: "private key only", create: true, contents: "-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n", want: "no PEM certificates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caFile := filepath.Join(t.TempDir(), "market-ca.pem")
			switch {
			case tc.directory:
				if err := os.Mkdir(caFile, 0o700); err != nil {
					t.Fatal(err)
				}
			case tc.create:
				if err := os.WriteFile(caFile, []byte(tc.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				client, err := marketCAClient(caFile)
				if client != nil || err == nil {
					t.Fatalf("attempt %d: invalid CA file fell back to a client: %v, %v", attempt+1, client, err)
				}
				if !strings.Contains(err.Error(), "extension_market.ca_file") || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("attempt %d: unclear CA error %q; want setting and %q", attempt+1, err, tc.want)
				}
			}
		})
	}

	t.Run("oversized file", func(t *testing.T) {
		caFile := filepath.Join(t.TempDir(), "market-ca.pem")
		if err := os.WriteFile(caFile, make([]byte, marketCAFileMost+1), 0o600); err != nil {
			t.Fatal(err)
		}
		client, err := marketCAClient(caFile)
		if client != nil || err == nil {
			t.Fatalf("oversized CA file fell back to a client: %v, %v", client, err)
		}
		if !strings.Contains(err.Error(), "extension_market.ca_file") || !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("unclear oversized CA error: %v", err)
		}
	})

	t.Run("device file", func(t *testing.T) {
		const caFile = "/dev/zero"
		if _, err := os.Stat(caFile); err != nil {
			t.Skipf("no device fixture: %v", err)
		}
		client, err := marketCAClient(caFile)
		if client != nil || err == nil {
			t.Fatalf("device CA file fell back to a client: %v, %v", client, err)
		}
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("device CA file did not fail clearly: %v", err)
		}
	})
}

func TestMarketClientCAFileWithProxy(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	caPEM := marketTestTLSCertificate(t, srv)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caFile := filepath.Join(t.TempDir(), "market-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("test CA is not PEM")
	}
	added, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	before, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	wantRoots := before.Clone()
	wantRoots.AddCert(added)

	for _, tc := range []struct {
		name, raw string
		invalid   bool
	}{
		{name: "HTTP", raw: "http://proxy.example:3128"},
		{name: "SOCKS5", raw: "socks5://proxy.example:1080"},
		{name: "invalid proxy", raw: "https://proxy.example:3128", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Manager{cfg: &Config{ExtensionMarket: ExtensionMarketConfig{CAFile: caFile, Proxy: tc.raw}}, extensions: &extensions{}}
			client, err := h.marketClient()
			if err != nil {
				t.Fatal(err)
			}
			tr := client.Transport.(*http.Transport)
			if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil || !tr.TLSClientConfig.RootCAs.Equal(wantRoots) {
				t.Fatal("configuring a proxy lost the system roots or additional CA")
			}
			if tr.TLSClientConfig.InsecureSkipVerify {
				t.Fatal("certificate verification is disabled")
			}
			req, _ := http.NewRequest("GET", "https://market.example.com/catalog.json", nil)
			u, proxyErr := tr.Proxy(req)
			if tc.invalid {
				if proxyErr == nil || u != nil {
					t.Fatalf("invalid proxy with a valid CA fell back to a direct connection: %v, %v", u, proxyErr)
				}
			} else if proxyErr != nil || u == nil || u.String() != tc.raw {
				t.Fatalf("CA configuration lost the explicit proxy: %v, %v; want %s", u, proxyErr, tc.raw)
			}
		})
	}
}

func TestMarketCAFileErrorDoesNotFallBackAtCallSites(t *testing.T) {
	catalog := marketIndex(t, "Private test")
	for _, tc := range []struct {
		name string
		call func(context.Context, *Manager, string) error
	}{
		{name: "Market client", call: func(ctx context.Context, h *Manager, specText string) error {
			mk, err := h.market(extension.MarketRecord{ID: "private", Spec: specText})
			if err != nil {
				return err
			}
			_, _, err = mk.Fetch(ctx)
			return err
		}},
		{name: "market add", call: func(ctx context.Context, h *Manager, specText string) error {
			_, err := h.marketAdd(ctx, specText, "")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(catalog)
			}))
			t.Cleanup(srv.Close)
			h, _, _ := marketHub(t)
			previousDefault := http.DefaultClient
			http.DefaultClient = srv.Client()
			t.Cleanup(func() { http.DefaultClient = previousDefault })

			h.cfg.ExtensionMarket.CAFile = filepath.Join(t.TempDir(), "missing-ca.pem")
			ms := h.marketsState()
			ms.loaded = true
			ms.client = nil
			ms.byID = map[string]*marketState{}
			err := tc.call(context.Background(), h, srv.URL+"/persona-extensions.json")
			if err == nil {
				t.Fatalf("invalid CA silently used http.DefaultClient; marketplace requests = %d", requests.Load())
			}
			if !strings.Contains(err.Error(), "extension_market.ca_file") {
				t.Fatalf("call site hid the CA error: %v", err)
			}
			if n := requests.Load(); n != 0 {
				t.Fatalf("invalid CA reached the marketplace through a fallback client %d time(s)", n)
			}
		})
	}
}

func marketClientGetError(t *testing.T, client *http.Client, url string) error {
	t.Helper()
	resp, err := client.Get(url)
	if err == nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("request unexpectedly succeeded: %s", url)
	}
	return err
}

func marketCAClient(caFile string) (*http.Client, error) {
	h := &Manager{cfg: &Config{ExtensionMarket: ExtensionMarketConfig{CAFile: caFile}}, extensions: &extensions{}}
	return h.marketClient()
}

// marketTestTLSCertificate prepares a server with a leaf signed by a test-only self-signed CA. The listener already
// exists, so the leaf can carry its actual IP address without a fixed host or address in the fixture.
func marketTestTLSCertificate(t *testing.T, srv *httptest.Server) []byte {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Persona test marketplace CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ip := srv.Listener.Addr().(*net.TCPAddr).IP
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Persona test marketplace"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{ip},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}
