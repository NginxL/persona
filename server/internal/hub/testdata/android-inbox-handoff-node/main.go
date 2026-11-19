// This node stands in for the Agent's computer in the live Android Inbox handoff test.
// The hub, its two managers and the Android transports run their ordinary implementations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const sessionID = "00000000-0000-4000-8000-000000008422"

func main() {
	if len(os.Args) == 4 && os.Args[1] == "observe" {
		if err := observe(os.Args[2], os.Args[3]); err != nil {
			panic(err)
		}
		return
	}
	host := ""
	for _, arg := range os.Args[1:] {
		if arg == "fixture-named" || arg == "fixture-default" {
			host = arg
		}
	}
	if host == "" {
		os.Exit(1)
	}
	command := os.Args[len(os.Args)-1]
	if command == "true" {
		return
	}
	if !strings.HasSuffix(command, "node --stdio") {
		os.Exit(1)
	}
	row := proto.Session{
		SessionID: sessionID, Agent: "claude", Status: proto.StatusIdle, Title: "Fixture Agent",
		StartedByPersona: true, Cwd: "/fixture", PaneID: "w1:p1", UpdatedAt: time.Now().UTC(), Rev: 1,
	}
	peer := rpc.New("fixture", os.Stdout)
	peer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "node.info":
			return proto.NodeInfo{
				Protocol: nodeproto.Version, ProtocolMin: nodeproto.MinManagerVersion,
				Capabilities: append(slices.Clone(nodeproto.Required), nodeproto.MachineUID),
				Hostname:     host, OS: "linux", Arch: "amd64", Home: "/fixture", Version: "dev",
				Program: nodeproto.ProgramPersonad, MachineUID: "android-handoff-" + host,
			}, nil
		case "sessions.list":
			return map[string]any{"sessions": []proto.Session{row}}, nil
		case "session.get", "session.watch":
			return map[string]any{"session": row, "items": []any{}}, nil
		case "session.items":
			return map[string]any{"items": []any{}}, nil
		case "session.send", "session.send_once":
			var input struct {
				SessionID string `json:"session_id"`
				ClientID  string `json:"client_msg_id"`
			}
			if err := json.Unmarshal(params, &input); err != nil || input.SessionID != sessionID || input.ClientID == "" {
				return nil, errors.New("expected the fixture session and client message id")
			}
			exe, err := os.Executable()
			if err != nil {
				return nil, err
			}
			output, err := os.OpenFile(filepath.Join(filepath.Dir(exe), host+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				return nil, err
			}
			defer output.Close()
			err = json.NewEncoder(output).Encode(map[string]string{"method": method, "session_id": input.SessionID, "client_msg_id": input.ClientID})
			return map[string]any{"ok": true}, err
		}
		return map[string]any{}, nil
	}
	_ = peer.Serve(context.Background(), os.Stdin)
}
