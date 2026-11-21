package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Official DeepSeek launchers report a prepaid balance at the provider's own origin (#11825). The manager reads it
// with the launcher's sealed key and hands on an amount and currency, never the key.
func TestLauncherBalancesReadsTheOfficialDeepSeekBalance(t *testing.T) {
	s, mux := launcherTestService(t)
	base := "https://api.deepseek.com/anthropic"
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"`+base+`","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
		t.Fatal(code, raw)
	}
	var requests []*http.Request
	var bodies []string
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Clone(context.Background()))
		return probeResponse(r, 200, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"12.34","granted_balance":"0.00","topped_up_balance":"12.34"}]}`), nil
	})
	accounts, known := s.LauncherBalances(context.Background())
	if !known || len(accounts) != 1 {
		t.Fatalf("known=%v accounts=%+v", known, accounts)
	}
	a := accounts[0]
	if a.Provider != "deepseek" || a.Name != "deepseek" || a.Problem != "" || a.Balance == nil {
		t.Fatalf("account: %+v", a)
	}
	if a.Balance.Amount != "12.34" || a.Balance.Currency != "CNY" {
		t.Fatalf("balance: %+v", a.Balance)
	}
	if len(requests) != 1 {
		t.Fatalf("requests: %d", len(requests))
	}
	r := requests[0]
	if r.Method != http.MethodGet || r.URL.String() != "https://api.deepseek.com/user/balance" {
		t.Fatalf("request: %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeKey || r.Header.Get("x-api-key") != fakeKey {
		t.Fatal("the launcher's key was not sent to its own provider")
	}
	raw, _ := json.Marshal(accounts)
	bodies = append(bodies, string(raw))
	for _, body := range bodies {
		if strings.Contains(body, fakeKey) {
			t.Fatalf("the key leaked into a sample: %s", body)
		}
	}
}

// A genuine zero is a balance the sidebar can show, not the absence of one.
func TestLauncherBalancesKeepsAGenuineZero(t *testing.T) {
	s, mux := launcherTestService(t)
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
		t.Fatal(code, raw)
	}
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		return probeResponse(r, 200, `{"is_available":false,"balance_infos":[{"currency":"cny","total_balance":"0.00"}]}`), nil
	})
	accounts, _ := s.LauncherBalances(context.Background())
	if len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "0.00" || accounts[0].Balance.Currency != "CNY" || accounts[0].Problem != "" {
		t.Fatalf("zero balance: %+v", accounts)
	}
}

// A failed read is a problem code, never a made-up zero.
func TestLauncherBalancesReportsFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		netErr bool
		want   string
	}{
		{name: "refused key", status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`, want: "rejected"},
		{name: "forbidden key", status: http.StatusForbidden, body: `{}`, want: "rejected"},
		{name: "server error", status: http.StatusInternalServerError, body: `{}`, want: "failed"},
		{name: "redirect", status: http.StatusFound, body: ``, want: "failed"},
		{name: "not json", status: http.StatusOK, body: `not json`, want: "failed"},
		{name: "no amount", status: http.StatusOK, body: `{"balance_infos":[]}`, want: "failed"},
		{name: "bad amount", status: http.StatusOK, body: `{"balance_infos":[{"currency":"CNY","total_balance":"twelve"}]}`, want: "failed"},
		{name: "unreachable", netErr: true, want: "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mux := launcherTestService(t)
			if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
				`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
				t.Fatal(code, raw)
			}
			interceptProbe(t, func(r *http.Request) (*http.Response, error) {
				if tc.netErr {
					return nil, io.ErrUnexpectedEOF
				}
				return probeResponse(r, tc.status, tc.body), nil
			})
			accounts, known := s.LauncherBalances(context.Background())
			if !known || len(accounts) != 1 || accounts[0].Problem != tc.want || accounts[0].Balance != nil {
				t.Fatalf("%s: known=%v %+v", tc.name, known, accounts)
			}
		})
	}
}

// Only the provider's own origin is asked: a gateway or a lookalike host never receives the launcher's key here.
func TestLauncherBalancesIgnoresOtherOrigins(t *testing.T) {
	for _, base := range []string{
		"https://gateway.example.com/anthropic",
		"https://api.deepseek.com.example.com/anthropic",
		"https://api.deepseek.com:8443/anthropic",
		"http://api.deepseek.com/anthropic",
		"https://api.deepseek.com/team/anthropic",
	} {
		s, mux := launcherTestService(t)
		if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
			`{"name":"gw","base_url":"`+base+`","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
			t.Fatal(code, raw)
		}
		asked := false
		interceptProbe(t, func(r *http.Request) (*http.Response, error) {
			asked = true
			return probeResponse(r, 200, `{}`), nil
		})
		if accounts, known := s.LauncherBalances(context.Background()); !known || len(accounts) != 0 || asked {
			t.Fatalf("%s: known=%v accounts=%+v asked=%v", base, known, accounts, asked)
		}
	}
}

// A launcher with no key of its own may use the key one of its computers keeps (#11825: the launcher's credential
// mechanism, not a second one).
func TestLauncherBalancesUsesAComputersKey(t *testing.T) {
	s, mux := launcherTestService(t)
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro"}`); code != 200 {
		t.Fatal(code, raw)
	}
	sealed, err := s.sealKey(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{{Launcher: "deepseek", Machine: "m1", KeySealed: sealed}}); err != nil {
		t.Fatal(err)
	}
	var key string
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		key = r.Header.Get("Authorization")
		return probeResponse(r, 200, `{"balance_infos":[{"currency":"CNY","total_balance":"3.00"}]}`), nil
	})
	accounts, _ := s.LauncherBalances(context.Background())
	if len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "3.00" || key != "Bearer "+fakeKey {
		t.Fatalf("accounts=%+v key=%q", accounts, key)
	}
}

// Two launchers that hold one key are one account: one row, under the first launcher's name.
func TestLauncherBalancesDeduplicatesOneKey(t *testing.T) {
	s, mux := launcherTestService(t)
	for _, name := range []string{"deepseek", "deepseek-flash"} {
		if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
			`{"name":"`+name+`","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
			t.Fatal(code, raw)
		}
	}
	requests := 0
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		requests++
		return probeResponse(r, 200, `{"balance_infos":[{"currency":"CNY","total_balance":"1.00"}]}`), nil
	})
	accounts, _ := s.LauncherBalances(context.Background())
	if len(accounts) != 1 || requests != 1 {
		t.Fatalf("accounts=%+v requests=%d", accounts, requests)
	}
}
