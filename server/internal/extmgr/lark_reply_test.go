package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestLarkReplyAppCallUsesBoundReadOnlyProvider(t *testing.T) {
	needsLarkCLI(t)
	needsLarkModule(t)
	now := time.Now().UTC()
	request := map[string]any{
		"message_id": "om_request", "msg_type": "text", "create_time": now.Add(-time.Hour).Format(time.RFC3339),
		"sender":  map[string]any{"id": "ou_account", "id_type": "open_id", "sender_type": "user"},
		"chat_id": "oc_bound", "thread_id": "omt_bound",
		"message_app_link": "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_bound&open_thread_id=omt_bound",
	}
	reply := map[string]any{
		"message_id": "om_reply", "msg_type": "text", "create_time": now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000Z"),
		"sender":  map[string]any{"id": "ou_person", "id_type": "open_id", "sender_type": "user"},
		"chat_id": "oc_bound", "thread_id": "omt_bound",
	}
	cases := []struct {
		name            string
		mutate          func(map[string]any, map[string]any) map[string]any
		drift           bool
		brand           string
		page            map[string]any
		wantReply       string
		sameMinuteTwins bool
		disabled        bool
		noRequest       bool
		wantErr         bool
		params          string
		requestAppID    string
		replyPrecision  time.Duration
	}{
		{name: "verified reply", wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "lark-branded request link", brand: "lark", wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "topic link verifies without RootID inference", mutate: func(_ map[string]any, r map[string]any) map[string]any {
			delete(r, "thread_id")
			r["parent_id"], r["root_id"] = "om_parent", "om_root"
			r["message_app_link"] = "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_bound&open_thread_id=omt_bound"
			return r
		}, wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "two replies in same minute", sameMinuteTwins: true, wantReply: "om_reply_z", replyPrecision: time.Minute},
		{name: "minute precision is retained", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["create_time"] = now.Add(-73 * time.Hour).Format(time.RFC3339)
			r["create_time"] = now.Add(-72 * time.Hour).In(time.Local).Format("2006-01-02 15:04")
			return r
		}, wantReply: "om_reply", replyPrecision: time.Minute},
		{name: "app-authored request identity", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["sender"] = map[string]any{"id": "app_sender_1", "id_type": "open_id", "sender_type": "app", "app_id": "app_bound"}
			return r
		}, wantReply: "om_reply", requestAppID: "app_bound", replyPrecision: time.Millisecond},
		{name: "app ID typed request identity", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["sender"] = map[string]any{"id": "app_bound", "id_type": "app_id", "sender_type": "app"}
			return r
		}, wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "wrong person sender", mutate: func(_ map[string]any, r map[string]any) map[string]any {
			r["sender"].(map[string]any)["id"] = "ou_other"
			return r
		}},
		{name: "own bot is not a person reply", mutate: func(_ map[string]any, r map[string]any) map[string]any {
			r["sender"] = map[string]any{"id": "app_bound", "id_type": "app_id", "sender_type": "app", "app_id": "app_bound"}
			return r
		}},
		{name: "human reply with app ID is not a person receipt", mutate: func(_ map[string]any, r map[string]any) map[string]any {
			r["sender"].(map[string]any)["app_id"] = "app_bound"
			return r
		}},
		{name: "wrong request sender", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["sender"].(map[string]any)["id"] = "ou_other"
			return r
		}, wantErr: true},
		{name: "different request id", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["message_id"] = "om_other"
			return r
		}, noRequest: true, wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "request app link scope mismatch", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["message_app_link"] = "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_other&open_thread_id=omt_bound"
			return r
		}, noRequest: true, wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "request URL port rejected", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["message_app_link"] = "https://applink.feishu.cn:443/client/thread/open?open_chat_id=oc_bound&open_thread_id=omt_bound"
			return r
		}, noRequest: true, wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "wrong brand host rejected", mutate: func(req map[string]any, r map[string]any) map[string]any {
			req["message_app_link"] = "https://applink.larksuite.com/client/thread/open?open_chat_id=oc_bound&open_thread_id=omt_bound"
			return r
		}, noRequest: true, wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "conflicting topic", mutate: func(_ map[string]any, r map[string]any) map[string]any { r["thread_id"] = "omt_other"; return r }},
		{name: "deleted reply", mutate: func(_ map[string]any, r map[string]any) map[string]any { r["deleted"] = true; return r }},
		{name: "forwarded reply", mutate: func(_ map[string]any, r map[string]any) map[string]any { r["msg_type"] = "merge_forward"; return r }},
		{name: "encoded forwarded content", mutate: func(_ map[string]any, r map[string]any) map[string]any {
			r["content"] = `{"forwarded_message":{"id":"om_elsewhere"}}`
			return r
		}},
		{name: "encoded ordinary text", mutate: func(_ map[string]any, r map[string]any) map[string]any {
			r["content"] = `{"text":"An ordinary reply"}`
			return r
		}, wantReply: "om_reply", replyPrecision: time.Millisecond},
		{name: "unknown reply time", mutate: func(_ map[string]any, r map[string]any) map[string]any { r["create_time"] = "tomorrow maybe"; return r }, wantErr: true},
		{name: "invalid reply receipt ID", mutate: func(_ map[string]any, r map[string]any) map[string]any { r["message_id"] = "om reply"; return r }, wantErr: true},
		{name: "identity changed during read", drift: true, wantErr: true},
		{name: "disabled source is not read", disabled: true, wantErr: true},
		{name: "reader app differs from binding", params: `{"source":"larkcli","brand":"feishu","app_id":"app_other","account_open_id":"ou_account","person_open_id":"ou_person","chat_id":"oc_bound","thread_id":"omt_bound","request_message_id":"om_request"}`, wantErr: true},
		{name: "pagination repeats", page: map[string]any{"messages": []any{}, "has_more": true, "page_token": "same"}, wantErr: true},
		{name: "binding ID with whitespace rejected", params: `{"source":"larkcli","brand":"feishu","app_id":"app bound","account_open_id":"ou_account","person_open_id":"ou_person","chat_id":"oc_bound","thread_id":"omt_bound","request_message_id":"om_request"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, rep := cloneMap(request), cloneMap(reply)
			if tc.mutate != nil {
				rep = tc.mutate(req, rep)
			}
			brand := tc.brand
			if brand == "" {
				brand = "feishu"
			}
			if brand == "lark" {
				req["message_app_link"] = "https://applink.larksuite.com/client/thread/open?open_chat_id=oc_bound&open_thread_id=omt_bound"
				rep["message_app_link"] = "https://applink.larksuite.com/client/thread/open?open_chat_id=oc_bound&open_thread_id=omt_bound"
			}
			messages := []any{req, rep}
			if tc.sameMinuteTwins {
				req["create_time"] = now.Add(-73 * time.Hour).Format(time.RFC3339)
				rep["create_time"] = now.Add(-72 * time.Hour).In(time.Local).Format("2006-01-02 15:04")
				twin := cloneMap(rep)
				twin["message_id"] = "om_reply_z"
				messages = []any{req, rep, twin}
			}
			page := tc.page
			if page == nil {
				page = map[string]any{"messages": messages, "has_more": false}
			}
			bin := makeReplyFixtureCLI(t, page, tc.drift, brand)
			h := newTestManager(t)
			h.connectors = h.newConnectors(nil)
			c := cliport.NewConnector(cliwire.Config{Bin: bin})
			if err := h.connectors.Add(c); err != nil {
				t.Fatal(err)
			}
			if tc.disabled {
				if err := h.connectors.SetEnabled(cliwire.ConnectorID, false); err != nil {
					t.Fatal(err)
				}
			}
			params := tc.params
			if params == "" {
				params = `{"source":"larkcli","brand":"feishu","app_id":"app_bound","account_open_id":"ou_account","person_open_id":"ou_person","chat_id":"oc_bound","thread_id":"omt_bound","request_message_id":"om_request"}`
				if brand == "lark" {
					params = strings.Replace(params, `"brand":"feishu"`, `"brand":"lark"`, 1)
				}
			}
			raw, handled, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: "connectors.lark_reply", Params: json.RawMessage(params)})
			if !handled {
				t.Fatal("reply app call was not handled")
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected fail-closed result, got %s", raw)
				}
				if tc.disabled {
					if _, err := os.Stat(filepath.Join(filepath.Dir(bin), "count")); !os.IsNotExist(err) {
						t.Fatalf("a disabled source executed the CLI: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			requestReceipt, _ := got["request"].(map[string]any)
			replyReceipt, _ := got["reply"].(map[string]any)
			if !tc.noRequest && requestReceipt["id"] != "om_request" {
				t.Fatalf("request receipt = %v", requestReceipt)
			} else if tc.noRequest && requestReceipt["id"] != nil && requestReceipt["id"] != "" {
				t.Fatalf("unexpected request receipt = %v", requestReceipt)
			}
			gotRequestAppID, _ := requestReceipt["app_id"].(string)
			if gotRequestAppID != tc.requestAppID {
				t.Fatalf("request sender app_id = %q, want %q", gotRequestAppID, tc.requestAppID)
			}
			if tc.wantReply == "" {
				if replyReceipt["id"] != nil && replyReceipt["id"] != "" {
					t.Fatalf("unexpected reply receipt = %v", replyReceipt)
				}
			} else if replyReceipt["id"] != tc.wantReply {
				t.Fatalf("reply receipt = %v", replyReceipt)
			}
			if tc.wantReply != "" {
				at, _ := time.Parse(time.RFC3339Nano, replyReceipt["at"].(string))
				before, _ := time.Parse(time.RFC3339Nano, replyReceipt["before"].(string))
				if before.Sub(at) != tc.replyPrecision {
					t.Fatalf("timestamp precision interval = %s", before.Sub(at))
				}
			}
			if strings.Contains(string(raw), "message text") {
				t.Fatalf("message content escaped: %s", raw)
			}
		})
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		if nested, ok := value.(map[string]any); ok {
			out[key] = cloneMap(nested)
		} else {
			out[key] = value
		}
	}
	return out
}

func makeReplyFixtureCLI(t *testing.T, page map[string]any, drift bool, brand string) string {
	t.Helper()
	dir := t.TempDir()
	pageRaw, err := json.Marshal(map[string]any{"ok": true, "data": page})
	if err != nil {
		t.Fatal(err)
	}
	auth := func(account string) []byte {
		return []byte(fmt.Sprintf(`{"appId":"app_bound","brand":%q,"identities":{"user":{"status":"ready","openId":%q,"tokenStatus":"valid"}}}`, brand, account))
	}
	if err := os.WriteFile(filepath.Join(dir, "auth1.json"), auth("ou_account"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth2 := "ou_account"
	if drift {
		auth2 = "ou_rebound"
	}
	if err := os.WriteFile(filepath.Join(dir, "auth2.json"), auth(auth2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.json"), pageRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(dir, "count")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "${1:-}" = "--profile" ]; then shift 2; fi
case "${1:-}" in
  auth)
    n=$(cat %s 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > %s
    if [ "$n" -eq 1 ]; then cat %s; else cat %s; fi
    ;;
  im) cat %s ;;
  *) exit 2 ;;
esac
`, shQuote(count), shQuote(count), shQuote(filepath.Join(dir, "auth1.json")), shQuote(filepath.Join(dir, "auth2.json")), shQuote(filepath.Join(dir, "page.json")))
	bin := filepath.Join(dir, "lark-cli")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
