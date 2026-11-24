package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/runtimepkg"
)

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func envKeyCount(env []string, key string) int {
	prefix := key + "="
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			n++
		}
	}
	return n
}

// unsetEnv removes keys from the process environment for the duration of the test and restores prior values/presence.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	type saved struct {
		val string
		ok  bool
	}
	prev := make(map[string]saved, len(keys))
	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		prev[k] = saved{val: v, ok: ok}
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv(%s): %v", k, err)
		}
	}
	t.Cleanup(func() {
		for _, k := range keys {
			s := prev[k]
			if s.ok {
				_ = os.Setenv(k, s.val)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	})
}

func TestParseScutilProxy(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want systemProxy
	}{
		{
			name: "http https socks with exceptions and pac disabled",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : 192.0.2.0/24
    2 : 198.51.100/24
    3 : localhost
    4 : *.local
    5 : *.example.com
    6 : <local>
  }
  ExcludeSimpleHostnames : 1
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  ProxyAutoDiscoveryEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 7891
  SOCKSProxy : 127.0.0.1
}
`,
			want: systemProxy{
				HTTP:                   "http://127.0.0.1:7890",
				HTTPS:                  "http://127.0.0.1:7890",
				SOCKS:                  "socks5h://127.0.0.1:7891",
				Exceptions:             []string{"127.0.0.1", "192.0.2.0/24", "198.51.100.0/24", "localhost", ".local", ".example.com"},
				ExcludeSimpleHostnames: true,
			},
		},
		{
			name: "socks only ignores disabled http and https with stale host and port",
			out: `<dictionary> {
  HTTPEnable : 0
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : proxy.example.com
}
`,
			want: systemProxy{
				SOCKS: "socks5h://proxy.example.com:1080",
			},
		},
		{
			name: "ipv6 proxies including bare bracketed and link-local zone",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : [::1]
    1 : 2001:db8::/32
    2 : [2001:db8::5]:8080
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : ::1
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : [2001:db8::1]
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : fe80::1%en0
}
`,
			want: systemProxy{
				HTTP:       "http://[::1]:7890",
				HTTPS:      "http://[2001:db8::1]:8443",
				SOCKS:      "socks5h://[fe80::1%25en0]:1080",
				Exceptions: []string{"::1", "2001:db8::/32", "[2001:db8::5]:8080"},
			},
		},
		{
			name: "all disabled returns no active proxy",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPEnable : 0
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  ProxyAutoDiscoveryEnable : 0
  SOCKSEnable : 0
  SOCKSPort : 7890
  SOCKSProxy : 127.0.0.1
}
`,
			want: systemProxy{
				Exceptions: []string{".local"},
			},
		},
		{
			name: "pac enabled without static proxies returns empty",
			out: `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 0
  ProxyAutoConfigEnable : 1
  ProxyAutoConfigURLString : http://proxy.example.com/proxy.pac
}
`,
			want: systemProxy{},
		},
		{
			name: "pac enabled alongside manual proxies skips manual injection",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7890
  SOCKSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 1
  ProxyAutoConfigURLString : http://proxy.example.com/proxy.pac
}
`,
			want: systemProxy{},
		},
		{
			name: "wpad enabled alongside manual proxies skips manual injection",
			out: `<dictionary> {
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  ProxyAutoDiscoveryEnable : 1
}
`,
			want: systemProxy{},
		},
		{
			name: "drops bare exact domains while keeping wildcard suffixes ips cidrs and localhost",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : example.org
    1 : api.example.com
    2 : example.org:8443
    3 : intranet
    4 : *.example.org
    5 : .example.net
    6 : *.example.org:8443
    7 : localhost
    8 : localhost:8080
    9 : 127.0.0.1:9000
    10 : 192.0.2.0/24
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
}
`,
			want: systemProxy{
				HTTP:       "http://127.0.0.1:7890",
				Exceptions: []string{".example.org", ".example.net", ".example.org:8443", "localhost", "localhost:8080", "127.0.0.1:9000", "192.0.2.0/24"},
			},
		},
		{
			name: "ignores nested scoped dictionary and unrelated arrays",
			out: `<dictionary> {
  OtherList : <array> {
    0 : *.unrelated.example.com
  }
  ExceptionsList : <array> {
    0 : *.example.org
  }
  __SCOPED__ : <dictionary> {
    en0 : <dictionary> {
      HTTPEnable : 0
      HTTPPort : 9999
      HTTPProxy : 203.0.113.9
      ExceptionsList : <array> {
        0 : *.scoped.example.com
      }
    }
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
}
`,
			want: systemProxy{
				HTTP:       "http://127.0.0.1:7890",
				Exceptions: []string{".example.org"},
			},
		},
		{
			name: "filters invalid ports hosts and malformed exceptions without dropping valid peers",
			out: "<dictionary> {\r\n" +
				"  ExceptionsList : <array> {\r\n" +
				"    0 : *.local\r\n" +
				"    1 : <local>\r\n" +
				"    2 : *.\r\n" +
				"    3 : foo*bar\r\n" +
				"    4 : bad host\r\n" +
				"    5 : a,b\r\n" +
				"    6 : example.com/path\r\n" +
				"    7 : 192.0.2.0/99\r\n" +
				"    8 : ctrl\x07host\r\n" +
				"    9 : localhost:8080\r\n" +
				"  }\r\n" +
				"  HTTPEnable : 1\r\n" +
				"  HTTPPort : 0\r\n" +
				"  HTTPProxy : 127.0.0.1\r\n" +
				"  HTTPSEnable : 1\r\n" +
				"  HTTPSPort : 8080\r\n" +
				"  HTTPSProxy : 127.0.0.1\r\n" +
				"  SOCKSEnable : 1\r\n" +
				"  SOCKSPort : 65536\r\n" +
				"  SOCKSProxy : 127.0.0.1\r\n" +
				"}\r\n",
			want: systemProxy{
				HTTPS:      "http://127.0.0.1:8080",
				Exceptions: []string{".local", "localhost:8080"},
			},
		},
		{
			name: "empty or garbage output returns empty",
			out:  "not a scutil dictionary\nHTTPEnable : 1\nHTTPProxy : 127.0.0.1\nHTTPPort : 7890\n",
			want: systemProxy{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseScutilProxy(tc.out)
			if got.HTTP != tc.want.HTTP ||
				got.HTTPS != tc.want.HTTPS ||
				got.SOCKS != tc.want.SOCKS ||
				got.ExcludeSimpleHostnames != tc.want.ExcludeSimpleHostnames ||
				!slices.Equal(got.Exceptions, tc.want.Exceptions) {
				t.Fatalf("parseScutilProxy() =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

func TestFormatProxyURLValidation(t *testing.T) {
	tests := []struct {
		name   string
		scheme string
		host   string
		port   string
		want   string
	}{
		{"ipv4", "http", "127.0.0.1", "7890", "http://127.0.0.1:7890"},
		{"hostname", "http", "proxy-1.example.com", "8080", "http://proxy-1.example.com:8080"},
		{"fqdn trailing dot", "http", "proxy.local.", "8080", "http://proxy.local.:8080"},
		{"ipv6 bare", "socks5h", "::1", "1080", "socks5h://[::1]:1080"},
		{"ipv6 bracketed", "http", "[2001:db8::1]", "7890", "http://[2001:db8::1]:7890"},
		{"ipv6 zone", "http", "fe80::1%en0", "7890", "http://[fe80::1%25en0]:7890"},
		{"port min", "http", "127.0.0.1", "1", "http://127.0.0.1:1"},
		{"port max", "http", "127.0.0.1", "65535", "http://127.0.0.1:65535"},
		{"port zero", "http", "127.0.0.1", "0", ""},
		{"port overflow", "http", "127.0.0.1", "65536", ""},
		{"port negative", "http", "127.0.0.1", "-1", ""},
		{"port plus sign", "http", "127.0.0.1", "+7890", ""},
		{"port leading zero", "http", "127.0.0.1", "07890", ""},
		{"port non-numeric", "http", "127.0.0.1", "abc", ""},
		{"empty host", "http", "", "7890", ""},
		{"host with scheme", "http", "http://127.0.0.1", "7890", ""},
		{"host with userinfo", "http", "user:pass@127.0.0.1", "7890", ""},
		{"host with path", "http", "127.0.0.1/path", "7890", ""},
		{"host with whitespace", "http", "proxy example.com", "7890", ""},
		{"host with control char", "http", "127.0.0.1\x00", "7890", ""},
		{"host with crlf", "http", "127.0.0.1\r\nX=1", "7890", ""},
		{"bracketed ipv4 rejected", "http", "[127.0.0.1]", "7890", ""},
		{"malformed ipv6", "http", "2001:db8:::1", "7890", ""},
		{"invalid ipv6 zone", "http", "fe80::1%en0/bad", "7890", ""},
		{"bogus numeric host", "http", "999.999.999.999", "7890", ""},
		{"leading hyphen host", "http", "-bad.example.com", "7890", ""},
		{"empty label host", "http", "bad..example.com", "7890", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatProxyURL(tc.scheme, tc.host, tc.port); got != tc.want {
				t.Fatalf("formatProxyURL(%q, %q, %q) = %q, want %q", tc.scheme, tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestWithSystemProxyEnvRespectsExplicitEnv(t *testing.T) {
	oldDetect := detectSystemProxy
	t.Cleanup(func() { detectSystemProxy = oldDetect })

	for _, key := range proxyVars {
		t.Run(key+" non-empty", func(t *testing.T) {
			called := false
			detectSystemProxy = func(context.Context) systemProxy {
				called = true
				return systemProxy{
					HTTP:  "http://127.0.0.1:7890",
					HTTPS: "http://127.0.0.1:7890",
					SOCKS: "socks5h://127.0.0.1:7890",
				}
			}
			base := []string{"PATH=/usr/bin", key + "=http://proxy.example.com:8888"}
			got := withSystemProxyEnv(context.Background(), base)
			if called {
				t.Fatalf("detectSystemProxy was called despite explicit %s", key)
			}
			if !slices.Equal(got, base) {
				t.Fatalf("env changed despite explicit %s: got %v, want %v", key, got, base)
			}
		})

		t.Run(key+" explicit empty disables fallback", func(t *testing.T) {
			called := false
			detectSystemProxy = func(context.Context) systemProxy {
				called = true
				return systemProxy{
					HTTP:  "http://127.0.0.1:7890",
					HTTPS: "http://127.0.0.1:7890",
					SOCKS: "socks5h://127.0.0.1:7890",
				}
			}
			base := []string{"PATH=/usr/bin", key + "="}
			got := withSystemProxyEnv(context.Background(), base)
			if called {
				t.Fatalf("detectSystemProxy was called despite explicit empty %s=", key)
			}
			if !slices.Equal(got, base) {
				t.Fatalf("env changed despite explicit empty %s=: got %v, want %v", key, got, base)
			}
		})
	}

	t.Run("unset proxy vars falls back to system proxy", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return systemProxy{HTTPS: "http://127.0.0.1:7890"}
		}
		base := []string{"PATH=/usr/bin"}
		got := withSystemProxyEnv(context.Background(), base)
		if !called {
			t.Fatal("detectSystemProxy was not called when proxy vars were unset")
		}
		m := envMap(got)
		if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("system proxy not injected: %v", m)
		}
	})
}

func TestApplySystemProxyEnvMergesNoProxyAndSyncsCase(t *testing.T) {
	fullProxy := systemProxy{
		HTTP:       "http://127.0.0.1:7890",
		HTTPS:      "http://127.0.0.1:7890",
		SOCKS:      "socks5h://127.0.0.1:7890",
		Exceptions: []string{"127.0.0.1", "localhost", "192.0.2.0/24", ".local"},
	}

	t.Run("injects all enabled proxies and loopback-safe no_proxy", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		orig := slices.Clone(base)
		got := applySystemProxyEnv(base, fullProxy)
		if !slices.Equal(base, orig) {
			t.Fatalf("input slice mutated: %v", base)
		}
		m := envMap(got)
		for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
			if m[k] != "http://127.0.0.1:7890" {
				t.Fatalf("%s = %q, want http://127.0.0.1:7890", k, m[k])
			}
			if envKeyCount(got, k) != 1 {
				t.Fatalf("%s appeared %d times in %v", k, envKeyCount(got, k), got)
			}
		}
		for _, k := range []string{"ALL_PROXY", "all_proxy"} {
			if m[k] != "socks5h://127.0.0.1:7890" {
				t.Fatalf("%s = %q, want socks5h://127.0.0.1:7890", k, m[k])
			}
		}
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("merges existing uppercase and lowercase NO_PROXY without losing loopback", func(t *testing.T) {
		base := []string{"NO_PROXY=corp.example.com,127.0.0.1", "no_proxy=internal.example.org"}
		got := applySystemProxyEnv(base, fullProxy)
		m := envMap(got)
		wantNoProxy := "corp.example.com,127.0.0.1,internal.example.org,localhost,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("preserves wildcard NO_PROXY", func(t *testing.T) {
		base := []string{"NO_PROXY=*"}
		got := applySystemProxyEnv(base, fullProxy)
		m := envMap(got)
		if m["NO_PROXY"] != "*" || m["no_proxy"] != "*" {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want *", m["NO_PROXY"], m["no_proxy"])
		}
	})

	t.Run("exclude simple hostnames adds single-label gateway host to NO_PROXY", func(t *testing.T) {
		p := fullProxy
		p.ExcludeSimpleHostnames = true
		base := []string{"PATH=/usr/bin", "GOOGLE_GEMINI_BASE_URL=http://gateway:8080/v1beta"}
		got := applySystemProxyEnv(base, p)
		m := envMap(got)
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local,gateway"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("exclude simple hostnames ignores multi-label or ip gateway hosts", func(t *testing.T) {
		p := fullProxy
		p.ExcludeSimpleHostnames = true
		for _, baseURL := range []string{"https://gateway.example.com", "http://192.0.2.10:8080"} {
			got := applySystemProxyEnv([]string{"GOOGLE_GEMINI_BASE_URL=" + baseURL}, p)
			m := envMap(got)
			wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
			if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
				t.Fatalf("baseURL=%q: NO_PROXY=%q no_proxy=%q, want %q", baseURL, m["NO_PROXY"], m["no_proxy"], wantNoProxy)
			}
		}
	})

	t.Run("exclude simple hostnames off does not add single-label gateway host", func(t *testing.T) {
		base := []string{"PATH=/usr/bin", "GOOGLE_GEMINI_BASE_URL=http://gateway"}
		got := applySystemProxyEnv(base, fullProxy)
		m := envMap(got)
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("socks only does not set http or https proxy", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		got := applySystemProxyEnv(base, systemProxy{SOCKS: "socks5h://127.0.0.1:1080"})
		m := envMap(got)
		if m["ALL_PROXY"] != "socks5h://127.0.0.1:1080" || m["all_proxy"] != "socks5h://127.0.0.1:1080" {
			t.Fatalf("ALL_PROXY=%q all_proxy=%q", m["ALL_PROXY"], m["all_proxy"])
		}
		for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
			if _, ok := m[k]; ok {
				t.Fatalf("unexpected %s=%q in socks-only proxy env", k, m[k])
			}
		}
		if m["NO_PROXY"] != loopbackNoProxy || m["no_proxy"] != loopbackNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], loopbackNoProxy)
		}
	})

	t.Run("disabled system proxy leaves env untouched", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		got := applySystemProxyEnv(base, systemProxy{Exceptions: []string{".local"}})
		if !slices.Equal(got, base) {
			t.Fatalf("disabled system proxy modified env: got %v, want %v", got, base)
		}
	})
}

func TestAgyBridgeEnvSystemProxyIntegration(t *testing.T) {
	oldDetect := detectSystemProxy
	t.Cleanup(func() { detectSystemProxy = oldDetect })

	detected := systemProxy{
		HTTP:                   "http://127.0.0.1:7890",
		HTTPS:                  "http://127.0.0.1:7890",
		SOCKS:                  "socks5h://127.0.0.1:7890",
		Exceptions:             []string{"192.0.2.0/24", ".local"},
		ExcludeSimpleHostnames: true,
	}

	t.Run("injects system proxy when launchd env has no proxy", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy { return detected }
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		rh.setAgyProviderEnv([]string{"GEMINI_API_KEY=test-key", "PERSONA_AGY_MODEL_PROVIDER=gemini"})
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		m := envMap(got)
		if m["PERSONA_RUNTIME_SOCKET"] != "/tmp/persona-runtime.sock" {
			t.Fatalf("PERSONA_RUNTIME_SOCKET = %q", m["PERSONA_RUNTIME_SOCKET"])
		}
		if m["GEMINI_API_KEY"] != "test-key" || m["PERSONA_AGY_MODEL_PROVIDER"] != "gemini" {
			t.Fatalf("provider env missing: %v", m)
		}
		if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("HTTPS proxy not injected: %v", m)
		}
		if m["ALL_PROXY"] != "socks5h://127.0.0.1:7890" || m["all_proxy"] != "socks5h://127.0.0.1:7890" {
			t.Fatalf("ALL proxy not injected: %v", m)
		}
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("adds single-label provider gateway host to NO_PROXY when ExcludeSimpleHostnames is on", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy { return detected }
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		rh.setAgyProviderEnv([]string{"GOOGLE_GEMINI_BASE_URL=http://gateway", "GEMINI_API_KEY=test-key", "PERSONA_AGY_MODEL_PROVIDER=gemini"})
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		m := envMap(got)
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local,gateway"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("merges provider NO_PROXY with loopback and system exceptions", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy { return detected }
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		rh.setAgyProviderEnv([]string{"GEMINI_API_KEY=test-key", "NO_PROXY=gateway.example.com"})
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		m := envMap(got)
		wantNoProxy := "gateway.example.com,localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("provider explicit proxy suppresses system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return detected
		}
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		rh.setAgyProviderEnv([]string{"HTTPS_PROXY=http://proxy.example.com:3128"})
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		if called {
			t.Fatal("detectSystemProxy should not be called when provider explicitly sets HTTPS_PROXY")
		}
		m := envMap(got)
		if m["HTTPS_PROXY"] != "http://proxy.example.com:3128" {
			t.Fatalf("HTTPS_PROXY = %q", m["HTTPS_PROXY"])
		}
		if _, ok := m["ALL_PROXY"]; ok {
			t.Fatalf("unexpected ALL_PROXY injected alongside explicit provider proxy: %v", m)
		}
	})

	t.Run("provider explicit empty proxy disables system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return detected
		}
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		rh.setAgyProviderEnv([]string{"HTTP_PROXY="})
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		if called {
			t.Fatal("detectSystemProxy should not be called when provider explicitly clears HTTP_PROXY")
		}
		m := envMap(got)
		if v, ok := m["HTTP_PROXY"]; !ok || v != "" {
			t.Fatalf("HTTP_PROXY = %q (ok=%v), want explicit empty", v, ok)
		}
		if _, ok := m["HTTPS_PROXY"]; ok {
			t.Fatalf("unexpected HTTPS_PROXY injected: %v", m)
		}
	})

	t.Run("process explicit empty proxy disables system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return detected
		}
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "HTTPS_PROXY="})
		if called {
			t.Fatal("detectSystemProxy should not be called when process env explicitly sets HTTPS_PROXY=")
		}
		m := envMap(got)
		if v, ok := m["HTTPS_PROXY"]; !ok || v != "" {
			t.Fatalf("HTTPS_PROXY = %q (ok=%v), want explicit empty", v, ok)
		}
		if _, ok := m["HTTP_PROXY"]; ok {
			t.Fatalf("unexpected HTTP_PROXY injected: %v", m)
		}
	})
}

func TestAgyBridgeCommandInjectsSystemProxyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	pkgRoot := filepath.Join(dir, "pkgs")
	companion := filepath.Join(pkgRoot, agyPackage, "antigravity", "companion.mjs")
	if err := os.MkdirAll(filepath.Dir(companion), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companion, []byte("// test companion\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimepkg.OverrideEnv, pkgRoot)

	fakeNode := filepath.Join(dir, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nprintf 22.14.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_NODEJS", fakeNode)

	oldDetect := detectSystemProxy
	t.Cleanup(func() { detectSystemProxy = oldDetect })

	allProxyAndBypass := append(slices.Clone(proxyVars), "NO_PROXY", "no_proxy")

	t.Run("inherits system proxy when proxy env vars are unset", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy {
			return systemProxy{
				HTTP:       "http://127.0.0.1:7890",
				HTTPS:      "http://127.0.0.1:7890",
				SOCKS:      "socks5h://127.0.0.1:7890",
				Exceptions: []string{".local"},
			}
		}
		unsetEnv(t, allProxyAndBypass...)

		rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
		cmd, err := agyBridgeCommand(context.Background(), rh, "/opt/bin/agy", "builtin-test")
		if err != nil {
			t.Fatalf("agyBridgeCommand: %v", err)
		}
		m := envMap(cmd.Env)
		if m["PERSONA_RUNTIME_SOCKET"] != rh.path {
			t.Fatalf("PERSONA_RUNTIME_SOCKET = %q, want %q", m["PERSONA_RUNTIME_SOCKET"], rh.path)
		}
		if m["HTTP_PROXY"] != "http://127.0.0.1:7890" || m["http_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("HTTP proxy missing from cmd.Env: HTTP_PROXY=%q http_proxy=%q", m["HTTP_PROXY"], m["http_proxy"])
		}
		if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("HTTPS proxy missing from cmd.Env: HTTPS_PROXY=%q https_proxy=%q", m["HTTPS_PROXY"], m["https_proxy"])
		}
		if m["ALL_PROXY"] != "socks5h://127.0.0.1:7890" || m["all_proxy"] != "socks5h://127.0.0.1:7890" {
			t.Fatalf("ALL proxy missing from cmd.Env: ALL_PROXY=%q all_proxy=%q", m["ALL_PROXY"], m["all_proxy"])
		}
		wantNoProxy := "localhost,127.0.0.1,::1,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("explicit empty HTTPS_PROXY= in process env suppresses system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return systemProxy{HTTP: "http://127.0.0.1:7890", HTTPS: "http://127.0.0.1:7890"}
		}
		unsetEnv(t, allProxyAndBypass...)
		t.Setenv("HTTPS_PROXY", "")

		rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
		cmd, err := agyBridgeCommand(context.Background(), rh, "/opt/bin/agy", "builtin-test")
		if err != nil {
			t.Fatalf("agyBridgeCommand: %v", err)
		}
		if called {
			t.Fatal("detectSystemProxy should not be called when HTTPS_PROXY= is explicitly set")
		}
		m := envMap(cmd.Env)
		if v, ok := m["HTTPS_PROXY"]; !ok || v != "" {
			t.Fatalf("HTTPS_PROXY = %q (ok=%v), want explicit empty", v, ok)
		}
		if _, ok := m["HTTP_PROXY"]; ok {
			t.Fatalf("unexpected HTTP_PROXY=%q when HTTPS_PROXY= was explicitly set", m["HTTP_PROXY"])
		}
	})

	t.Run("preserves explicit process proxy without probing system proxy", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return systemProxy{HTTP: "http://127.0.0.1:7890", HTTPS: "http://127.0.0.1:7890"}
		}
		unsetEnv(t, allProxyAndBypass...)
		t.Setenv("HTTPS_PROXY", "http://proxy.example.com:8888")

		rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
		cmd, err := agyBridgeCommand(context.Background(), rh, "/opt/bin/agy", "builtin-test")
		if err != nil {
			t.Fatalf("agyBridgeCommand: %v", err)
		}
		if called {
			t.Fatal("detectSystemProxy should not be called when HTTPS_PROXY is explicitly set")
		}
		m := envMap(cmd.Env)
		if m["HTTPS_PROXY"] != "http://proxy.example.com:8888" {
			t.Fatalf("HTTPS_PROXY = %q, want http://proxy.example.com:8888", m["HTTPS_PROXY"])
		}
		if _, ok := m["HTTP_PROXY"]; ok {
			t.Fatalf("unexpected HTTP_PROXY=%q when explicit HTTPS_PROXY was set", m["HTTP_PROXY"])
		}
	})
}

func TestReadSystemProxyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := readSystemProxy(ctx); got.hasProxy() || len(got.Exceptions) != 0 {
		t.Fatalf("readSystemProxy(canceled) = %+v, want empty", got)
	}
}
