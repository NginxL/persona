package feishubot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the production client against a local gateway; credentials and responses are synthetic.
func TestTokenRefreshAfterTransient(t *testing.T) {
	type answer struct{ status, code int }
	ok := answer{http.StatusOK, 0}
	gone := answer{http.StatusBadRequest, 99991663}
	busy := answer{http.StatusServiceUnavailable, 503}
	limited := answer{http.StatusTooManyRequests, 99991400}
	refused := answer{http.StatusForbidden, 230002}
	for _, mode := range []string{"get", "send", "reply", "unkeyed send", "unkeyed reply", "resource"} {
		for _, tc := range []struct {
			name    string
			answers []answer
			fetches int32
			want    int
		}{
			{"success", []answer{ok}, 1, 0},
			{"503 then expired", []answer{busy, gone, ok}, 2, 0},
			{"429 then expired", []answer{limited, gone, ok}, 2, 0},
			{"503 then invalid token", []answer{busy, {http.StatusBadRequest, 99991664}, ok}, 2, 0},
			{"503 then revoked token", []answer{busy, {http.StatusBadRequest, 99991661}, ok}, 2, 0},
			{"first refresh", []answer{gone, ok}, 2, 0},
			{"repeated expiry", []answer{gone, gone}, 2, gone.code},
			{"expiry after transient and refresh", []answer{busy, gone, gone}, 2, gone.code},
			{"expiry on final attempt", []answer{busy, busy, gone}, 1, gone.code},
			{"transient after refresh", []answer{gone, busy, ok}, 2, 0},
			{"transient exhaustion", []answer{busy, busy, busy}, 1, busy.code},
			{"permanent rejection", []answer{refused}, 1, refused.code},
			{"rejection after transient", []answer{busy, refused}, 1, refused.code},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				answers, wantFetches, wantCode := tc.answers, tc.fetches, tc.want
				if mode == "unkeyed send" || mode == "unkeyed reply" {
					// An unknown POST result cannot be replayed, even if a later answer would expire its token.
					for i, a := range answers {
						if a == busy || a == limited {
							answers, wantCode = answers[:i+1], a.code
							wantFetches = 1
							if i > 0 {
								wantFetches = 2
							}
							break
						}
					}
				}
				var hits, fetches, sleeps atomic.Int32
				var payload []byte
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
						n := fetches.Add(1)
						token := "synthetic-old"
						if n > 1 {
							token = "synthetic-new"
						}
						okJSON(w, map[string]any{"code": 0, "tenant_access_token": token, "expire": 7200})
						return
					}
					n := int(hits.Add(1))
					expectedAuth := "Bearer synthetic-old"
					if fetches.Load() > 1 {
						expectedAuth = "Bearer synthetic-new"
					}
					if r.Header.Get("Authorization") != expectedAuth {
						t.Error("request did not use the current token")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error("cannot read request body")
					}
					if n == 1 {
						payload = body
					} else if !bytes.Equal(body, payload) {
						t.Error("retry changed the original request payload")
					}
					if mode == "send" || mode == "reply" {
						var in struct {
							UUID string `json:"uuid"`
						}
						if json.Unmarshal(body, &in) != nil || in.UUID != "stable-request" {
							t.Error("request lost its original UUID")
						}
					}
					if n > len(answers) {
						t.Error("request exceeded the expected attempt count")
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					a := answers[n-1]
					w.WriteHeader(a.status)
					if a == ok && mode == "resource" {
						_, _ = io.WriteString(w, "resource")
						return
					}
					okJSON(w, map[string]any{"code": a.code, "data": map[string]any{"message_id": "receipt", "bot": map[string]any{"open_id": "bot"}}})
				}))
				defer srv.Close()
				prev := callSleep
				callSleep = func(context.Context, time.Duration) error { sleeps.Add(1); return nil }
				defer func() { callSleep = prev }()
				c := New("synthetic-app", "synthetic-secret", srv.URL).WithHTTP(srv.Client())
				var result string
				var err error
				uuid := "stable-request"
				if mode == "unkeyed send" || mode == "unkeyed reply" {
					uuid = ""
				}
				switch mode {
				case "get":
					var info BotInfo
					info, err = c.BotInfo(context.Background())
					result = info.OpenID
				case "send", "unkeyed send":
					result, err = c.Send(context.Background(), "chat_id", "chat", TypeText, TextContent("hello"), uuid)
				case "reply", "unkeyed reply":
					result, err = c.Reply(context.Background(), "message", TypeText, TextContent("hello"), uuid, false)
				case "resource":
					var b []byte
					b, _, err = c.Resource(context.Background(), "message", "file", "file", 1024)
					result = string(b)
				}
				if wantCode == 0 {
					expectedResult := "receipt"
					if mode == "get" {
						expectedResult = "bot"
					}
					if mode == "resource" {
						expectedResult = "resource"
					}
					if err != nil || result != expectedResult {
						t.Error("expected a successful response")
					}
				} else {
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.Code != wantCode {
						t.Errorf("expected API error code %d", wantCode)
					}
				}
				if hits.Load() != int32(len(answers)) || fetches.Load() != wantFetches || hits.Load() > callMaxAttempts {
					t.Errorf("requests=%d token fetches=%d; want %d and %d", hits.Load(), fetches.Load(), len(answers), wantFetches)
				}
				wantSleeps := int32(0)
				for _, a := range answers[:len(answers)-1] {
					if a == busy || a == limited {
						wantSleeps++
					}
				}
				if sleeps.Load() != wantSleeps {
					t.Errorf("backoff waits=%d; want %d", sleeps.Load(), wantSleeps)
				}
			})
		}
	}
}

// Once a UUID-less POST has an uncertain outcome, even a preceding confirmed auth rejection grants no further replay.
func TestUnkeyedTokenRefreshStopsOnUnknownResult(t *testing.T) {
	for _, reply := range []bool{false, true} {
		for _, readFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("reply=%t/readFailure=%t", reply, readFailure), func(t *testing.T) {
				c, srv, hits, fetches := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
					okJSON(w, map[string]any{"code": 99991663})
				})
				failure := errors.New("synthetic unknown result")
				c.WithHTTP(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" || hits.Load() == 0 {
						return srv.Client().Transport.RoundTrip(r)
					}
					hits.Add(1)
					if readFailure {
						return &http.Response{StatusCode: http.StatusOK, Body: errBody{failure}}, nil
					}
					return nil, failure
				})})
				var sleeps int
				callSleep = func(context.Context, time.Duration) error { sleeps++; return nil }
				var err error
				if reply {
					_, err = c.Reply(context.Background(), "message", TypeText, TextContent("hello"), "", false)
				} else {
					_, err = c.Send(context.Background(), "chat_id", "chat", TypeText, TextContent("hello"), "")
				}
				if !errors.Is(err, failure) || hits.Load() != 2 || fetches.Load() != 2 || sleeps != 0 {
					t.Errorf("unknown result was not preserved: requests=%d token fetches=%d waits=%d", hits.Load(), fetches.Load(), sleeps)
				}
			})
		}
	}
}
