package nodeclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDirectionNodeExactWirePayloads(t *testing.T) {
	tests := []struct {
		name string
		want string
		call func(Dialer) (DirectionReply, error)
	}{
		{
			name: "create with an explicit session",
			want: `{"kind":"direction.create","input":{"name":"Release","project":"release","machine_id":"machine-example","session_id":"session-example"}}`,
			call: func(dial Dialer) (DirectionReply, error) {
				return CreateDirectionNode(context.Background(), dial, DirectionCreateRequest{
					Name: "Release", Project: "release", MachineID: "machine-example", SessionID: "session-example",
				})
			},
		},
		{
			name: "create without a session",
			want: `{"kind":"direction.create","input":{"name":"Release","project":"release"}}`,
			call: func(dial Dialer) (DirectionReply, error) {
				return CreateDirectionNode(context.Background(), dial, DirectionCreateRequest{Name: "Release", Project: "release"})
			},
		},
		{
			name: "progress replaces every text field and preserves links",
			want: `{"kind":"direction.progress","input":{"id":"direction_1","reached":"","doing":"Working","needs_you":"","links":["https://example.com/a,b","https://example.com/c"]}}`,
			call: func(dial Dialer) (DirectionReply, error) {
				return WriteDirectionProgressNode(context.Background(), dial, DirectionProgressRequest{
					ID: "direction_1", Doing: "Working", Links: []string{"https://example.com/a,b", "https://example.com/c"},
				})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			dial := func(_ context.Context, value any) ([]byte, error) {
				calls++
				req, ok := value.(Request)
				if !ok {
					t.Fatalf("request type %T, want nodeclient.Request", value)
				}
				wire, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				if string(wire) != tc.want {
					t.Fatalf("wire %s, want exactly %s", wire, tc.want)
				}
				return []byte("{\"status\":\"ok\",\"leader\":{\"id\":\"direction_1\"}}\n"), nil
			}
			rep, err := tc.call(dial)
			if err != nil || rep.Status != DirectionOK || string(rep.Leader) != `{"id":"direction_1"}` || calls != 1 {
				t.Fatalf("reply %+v, error %v, calls %d", rep, err, calls)
			}
		})
	}
}

func TestDirectionNodeReplyValidation(t *testing.T) {
	tests := []struct {
		name       string
		reply      string
		wantStatus string
		wantError  string
	}{
		{name: "old node is unavailable", reply: `{"error":"unknown kind"}`, wantStatus: DirectionUnavailable, wantError: "unknown kind"},
		{name: "invalid", reply: `{"status":"invalid","error":"bad project"}`, wantStatus: DirectionInvalid, wantError: "bad project"},
		{name: "failed", reply: `{"status":"failed","error":"write failed"}`, wantStatus: DirectionFailed, wantError: "write failed"},
		{name: "unavailable", reply: `{"status":"unavailable","error":"offline"}`, wantStatus: DirectionUnavailable, wantError: "offline"},
		{name: "empty object", reply: `{}`, wantError: "bad answer"},
		{name: "malformed", reply: `not json`, wantError: "bad answer"},
		{name: "unknown status", reply: `{"status":"later"}`, wantError: `unknown status "later"`},
		{name: "missing leader", reply: `{"status":"ok"}`, wantError: "no leader object"},
		{name: "null leader", reply: `{"status":"ok","leader":null}`, wantError: "no leader object"},
		{name: "array leader", reply: `{"status":"ok","leader":[]}`, wantError: "no leader object"},
		{name: "string leader", reply: `{"status":"ok","leader":"direction_1"}`, wantError: "no leader object"},
		{name: "empty leader object is an object", reply: `{"status":"ok","leader":{}}`, wantStatus: DirectionOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			dial := func(context.Context, any) ([]byte, error) {
				calls++
				return []byte(tc.reply + "\n"), nil
			}
			rep, err := CreateDirectionNode(context.Background(), dial, DirectionCreateRequest{Name: "Release", Project: "release"})
			if calls != 1 {
				t.Fatalf("calls %d, want one", calls)
			}
			if tc.wantStatus != "" && (err != nil || rep.Status != tc.wantStatus) {
				t.Fatalf("reply %+v, error %v; want status %q", rep, err, tc.wantStatus)
			}
			if tc.wantError != "" {
				if tc.wantStatus != "" {
					if rep.Error != tc.wantError {
						t.Fatalf("reply error %q, want %q (transport error %v)", rep.Error, tc.wantError, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error %v, want it to contain %q", err, tc.wantError)
				}
			}
		})
	}
}

func TestDirectionNodeTransportFailureIsNotRetried(t *testing.T) {
	calls := 0
	dial := func(context.Context, any) ([]byte, error) {
		calls++
		return nil, errors.New("connection lost after write")
	}
	_, err := WriteDirectionProgressNode(context.Background(), dial, DirectionProgressRequest{ID: "direction_1", Doing: "work"})
	if err == nil || err.Error() != "connection lost after write" || calls != 1 {
		t.Fatalf("error %v, calls %d; want the original error after one request", err, calls)
	}
}
