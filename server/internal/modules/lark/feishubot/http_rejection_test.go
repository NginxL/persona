package feishubot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise public operations after a successful token fetch, including a nil-output
// update and replies whose untrusted response must not supply a message receipt.
func TestCallRejectsGatewayJSON(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client) (string, error)
	}{
		{"send", func(c *Client) (string, error) {
			return c.Send(context.Background(), "chat_id", "synthetic-chat", TypeText, TextContent("synthetic text"), "synthetic-uuid")
		}},
		{"reply", func(c *Client) (string, error) {
			return c.Reply(context.Background(), "synthetic-message", TypeText, TextContent("synthetic text"), "synthetic-uuid", true)
		}},
		{"update card", func(c *Client) (string, error) {
			return "", c.UpdateCard(context.Background(), "synthetic-message", `{}`)
		}},
		{"bot info", func(c *Client) (string, error) {
			info, err := c.BotInfo(context.Background())
			return info.OpenID, err
		}},
	}
	for _, op := range operations {
		for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity} {
			for bodyIndex, body := range []string{
				`{"error":"synthetic-private-detail","data":{"message_id":"synthetic-untrusted-id","bot":{"open_id":"synthetic-untrusted-id"}}}`,
				`{"code":0,"msg":"synthetic-private-detail","data":{"message_id":"synthetic-untrusted-id","bot":{"open_id":"synthetic-untrusted-id"}}}`,
				`null`,
			} {
				t.Run(fmt.Sprintf("%s/%d/body-%d", op.name, status, bodyIndex), func(t *testing.T) {
					c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, _ *atomic.Int32) {
						if r.Header.Get("Authorization") != "Bearer tok" {
							t.Error("missing fetched token")
						}
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Retry-After", "30")
						w.WriteHeader(status)
						_, _ = w.Write([]byte(body))
					})
					slept := recordSleeps()
					id, err := op.call(c)
					want := fmt.Sprintf("feishu %s: HTTP %d", op.name, status)
					// The exact error also proves that response details and credentials stay out.
					if err == nil || err.Error() != want || id != "" {
						t.Fatalf("id=%q err=%v, want empty id and %q", id, err, want)
					}
					if hits.Load() != 1 || tokens.Load() != 1 || len(*slept) != 0 {
						t.Fatalf("hits=%d tokens=%d waits=%v, want one request, one token and no retry", hits.Load(), tokens.Load(), *slept)
					}
				})
			}
		}
	}
}

func TestCallSuccessfulHTTPResponses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		for bodyIndex, body := range []string{
			`{"code":0,"data":{"message_id":"synthetic-message"}}`,
			`{"data":{"message_id":"synthetic-message"}}`,
			`{"message_id":"synthetic-message"}`,
		} {
			t.Run(fmt.Sprintf("%d/body-%d", status, bodyIndex), func(t *testing.T) {
				c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, _ *http.Request, _ *atomic.Int32) {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(body))
				})
				id, err := c.Send(context.Background(), "chat_id", "synthetic-chat", TypeText, TextContent("synthetic text"), "")
				if err != nil || id != "synthetic-message" || hits.Load() != 1 || tokens.Load() != 1 {
					t.Fatalf("id=%q err=%v hits=%d tokens=%d", id, err, hits.Load(), tokens.Load())
				}
			})
		}
	}
}

func TestCallGatewayTransientJSONRetryPolicy(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway} {
		for _, uuid := range []string{"", "synthetic-uuid"} {
			t.Run(fmt.Sprintf("%d/uuid=%s", status, uuid), func(t *testing.T) {
				c, _, hits, tokens := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, _ *atomic.Int32) {
					var in struct {
						UUID string `json:"uuid"`
					}
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.UUID != uuid {
						t.Errorf("uuid=%q err=%v, want %q", in.UUID, err, uuid)
					}
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"synthetic-private-detail"}`))
				})
				slept := recordSleeps()
				id, err := c.Send(context.Background(), "chat_id", "synthetic-chat", TypeText, TextContent("synthetic text"), uuid)
				want := fmt.Sprintf("feishu send: HTTP %d", status)
				if err == nil || err.Error() != want || id != "" || tokens.Load() != 1 {
					t.Fatalf("id=%q err=%v tokens=%d, want %q and one token", id, err, tokens.Load(), want)
				}
				wantHits := int32(1)
				if uuid != "" {
					wantHits = callMaxAttempts
				}
				if hits.Load() != wantHits || len(*slept) != int(wantHits)-1 {
					t.Fatalf("hits=%d waits=%v, want %d attempts", hits.Load(), *slept, wantHits)
				}
				for _, wait := range *slept {
					if wait != 2*time.Second {
						t.Errorf("wait=%v, want Retry-After 2s", wait)
					}
				}
			})
		}
	}
}
