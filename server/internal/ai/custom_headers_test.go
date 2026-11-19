package ai

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Streamed errors are constructed after the 200, so they must pass through the same provider-secret redaction as HTTP
// errors. The short value also proves that no minimum length lets a header value escape.
func TestCustomHeaderSecretsRedactedFromStreamErrorsAndLogs(t *testing.T) {
	for _, tc := range []struct {
		api          string
		genericEvent string
		limitEvent   string
	}{
		{
			api:          "messages",
			genericEvent: `{"type":"error","error":{"type":"api_error","message":"echo abc and key-secret"}}`,
			limitEvent:   `{"type":"error","error":{"type":"rate_limit_error","message":"echo abc and key-secret"}}`,
		},
		{
			api:          "responses",
			genericEvent: `{"type":"response.failed","response":{"error":{"code":"server_error","message":"echo abc and key-secret"}}}`,
			limitEvent:   `{"type":"response.failed","response":{"error":{"code":"account_quota_exhausted","message":"echo abc and key-secret"}}}`,
		},
		{
			api:          "chat",
			genericEvent: `{"error":{"code":"server_error","message":"echo abc and key-secret"}}`,
			limitEvent:   `{"error":{"code":"account_quota_exhausted","message":"echo abc and key-secret"}}`,
		},
	} {
		for _, limited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/limited=%v", tc.api, limited), func(t *testing.T) {
				event := tc.genericEvent
				if limited {
					event = tc.limitEvent
				}
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", event)
				}))
				defer gw.Close()
				var logs bytes.Buffer
				oldWriter := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(oldWriter) })

				client := New(Config{API: tc.api, BaseURL: gw.URL, APIKey: "key-secret", Model: "m", Headers: map[string]string{"X-Custom-Header": "abc"}})
				_, err := client.Complete(context.Background(), "system", "hello", 8)
				if err == nil {
					t.Fatal("streamed gateway error was ignored")
				}
				for _, secret := range []string{"abc", "key-secret"} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("error exposed %q: %v", secret, err)
					}
					if strings.Contains(logs.String(), secret) {
						t.Errorf("logs exposed %q: %s", secret, logs.String())
					}
				}
				if limited && logs.Len() == 0 {
					t.Fatal("rate-limit failure did not exercise the gate log")
				}
			})
		}
	}
}
