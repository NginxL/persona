package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestChannelTestRouteCannotSendMapsTo409(t *testing.T) {
	t.Run("preserves channel explanation", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{"channels": []channel.View{{
				ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{Format: channel.FormatPlain, Push: true},
			}}}, nil
		})
		const reason = "This channel only replies to incoming triggers."
		f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: CodeCannotSend, Message: reason}
		})
		s := newTestService(t, f)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/examplechat/test", nil)
		req.Header.Set("Accept-Language", "en")
		s.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != channel.CodeCannotSend {
			t.Fatalf("code = %v, want %q", body["code"], channel.CodeCannotSend)
		}
		if body["error"] != reason {
			t.Fatalf("error = %v, want %q", body["error"], reason)
		}
	})

	t.Run("uses localized fallback when explanation is generic", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{"channels": []channel.View{{
				ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{Format: channel.FormatPlain, Push: true},
			}}}, nil
		})
		f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: CodeCannotSend, Message: errCannotSend.Error()}
		})
		s := newTestService(t, f)

		for _, tc := range []struct {
			lang string
			want string
		}{
			{lang: "en", want: "Example Chat can't send a standalone test message"},
			{lang: "zh-Hans", want: "Example Chat 暂时无法主动发送测试消息"},
			{lang: "ja", want: "Example Chatは現在テストメッセージを単独で送信できません"},
		} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/examplechat/test", nil)
			req.Header.Set("Accept-Language", tc.lang)
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusConflict {
				t.Fatalf("lang=%s status=%d want 409; body=%s", tc.lang, rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != channel.CodeCannotSend {
				t.Fatalf("lang=%s code=%v want %q", tc.lang, body["code"], channel.CodeCannotSend)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, tc.want) {
				t.Fatalf("lang=%s error=%q want substring %q", tc.lang, msg, tc.want)
			}
		}
	})
}

func TestChannelTestRouteReplyOnlyConnectivityCheck(t *testing.T) {
	t.Run("healthy reply-only channel succeeds without proactive send", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{"channels": []channel.View{{
				ID: "replychat", Name: "Reply Chat", Kind: channel.KindMention, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{Format: channel.FormatPlain, ReplyOnly: true},
			}}}, nil
		})
		sendCalls := 0
		f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
			sendCalls++
			return nil, errors.New("channel.send must not be called for reply_only channels")
		})
		s := newTestService(t, f)

		for _, tc := range []struct {
			lang string
			want string
		}{
			{lang: "en", want: "Reply Chat is connected and ready"},
			{lang: "zh-Hans", want: "Reply Chat 已连接并就绪"},
			{lang: "ja", want: "Reply Chatは接続済みで準備ができています"},
		} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/replychat/test", nil)
			req.Header.Set("Accept-Language", tc.lang)
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("lang=%s status=%d want 200; body=%s", tc.lang, rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["ok"] != true || body["reply_only"] != true {
				t.Fatalf("lang=%s body=%v", tc.lang, body)
			}
			if note, _ := body["note"].(string); !strings.Contains(note, tc.want) {
				t.Fatalf("lang=%s note=%q want substring %q", tc.lang, note, tc.want)
			}
		}
		if sendCalls != 0 {
			t.Fatalf("channels.send called %d times, want 0", sendCalls)
		}
	})

	t.Run("offline or error reply-only channel fails cleanly with 409", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			view     channel.View
			wantText string
		}{
			{
				name: "not_connected",
				view: channel.View{
					ID: "replychat", Name: "Reply Chat", Kind: channel.KindMention, Source: channel.SourcePlugin,
					Installed: true, Enabled: true, State: channel.StateNotConnected,
					Capabilities: channel.Capabilities{ReplyOnly: true},
				},
				wantText: "Reply Chat isn't connected yet",
			},
			{
				name: "disabled",
				view: channel.View{
					ID: "replychat", Name: "Reply Chat", Kind: channel.KindMention, Source: channel.SourcePlugin,
					Installed: true, Enabled: false, State: channel.StateDisabled,
					Capabilities: channel.Capabilities{ReplyOnly: true},
				},
				wantText: "Reply Chat isn't connected yet",
			},
			{
				name: "error_with_reason",
				view: channel.View{
					ID: "replychat", Name: "Reply Chat", Kind: channel.KindMention, Source: channel.SourcePlugin,
					Installed: true, Enabled: true, State: channel.StateError, Error: "extension process stopped",
					Capabilities: channel.Capabilities{ReplyOnly: true},
				},
				wantText: "extension process stopped",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newFakeHub().withMachine()
				f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
					return map[string]any{"channels": []channel.View{tc.view}}, nil
				})
				sendCalls := 0
				f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
					sendCalls++
					return map[string]any{}, nil
				})
				s := newTestService(t, f)
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/replychat/test", nil)
				req.Header.Set("Accept-Language", "en")
				s.ServeHTTP(rec, req)

				if rec.Code != http.StatusConflict {
					t.Fatalf("status=%d want 409; body=%s", rec.Code, rec.Body.String())
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["code"] != "not_connected" {
					t.Fatalf("code=%v want not_connected", body["code"])
				}
				if msg, _ := body["error"].(string); !strings.Contains(msg, tc.wantText) {
					t.Fatalf("error=%q want %q", msg, tc.wantText)
				}
				if sendCalls != 0 {
					t.Fatalf("channels.send called %d times, want 0", sendCalls)
				}
			})
		}
	})
}

func TestChannelTestRouteProactiveChannelUnchanged(t *testing.T) {
	t.Run("sends proactive test message and returns plain ok", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{"channels": []channel.View{{
				ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{Format: channel.FormatPlain, Push: true},
			}}}, nil
		})
		var sent map[string]any
		f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
			_ = json.Unmarshal(p, &sent)
			return map[string]any{}, nil
		})
		s := newTestService(t, f)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/examplechat/test", nil)
		req.Header.Set("Accept-Language", "en")
		s.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["ok"] != true {
			t.Fatalf("body=%v", body)
		}
		if _, hasReplyOnly := body["reply_only"]; hasReplyOnly {
			t.Fatalf("unexpected reply_only in proactive test body: %v", body)
		}
		if _, hasNote := body["note"]; hasNote {
			t.Fatalf("unexpected note in proactive test body: %v", body)
		}
		if sent["channel"] != "examplechat" || sent["kind"] != channel.OutNote {
			t.Fatalf("sent=%v", sent)
		}
	})

	t.Run("transport error remains HTTP 502", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{"channels": []channel.View{{
				ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{Format: channel.FormatPlain, Push: true},
			}}}, nil
		})
		f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
			return nil, errors.New("upstream gateway unreachable")
		})
		s := newTestService(t, f)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/examplechat/test", nil)
		s.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status=%d want 502; body=%s", rec.Code, rec.Body.String())
		}
	})
}
