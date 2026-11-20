// This test-only executable builds against both the frozen baseline and the
// current modules. It uses their actual wire structs, never starts a resident
// node, scans processes, launches an agent or contacts an external service.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

var ownedDB = lazyre.New(`^persona_compat_[0-9a-f]{16}(?:_manager)?$`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "compat fixture failed:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "db-") {
		if len(os.Args) != 3 || !ownedDB.MatchString(os.Args[2]) {
			return fmt.Errorf("refusing a non-fixture database")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, os.Getenv("PERSONA_COMPAT_DATABASE_URL"))
		if err != nil {
			return fmt.Errorf("test Postgres is unavailable")
		}
		defer c.Close(ctx)
		name := pgx.Identifier{os.Args[2]}.Sanitize()
		switch os.Args[1] {
		case "db-create":
			_, err = c.Exec(ctx, "CREATE DATABASE "+name)
		case "db-drop":
			_, err = c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		default:
			return fmt.Errorf("unknown fixture database operation")
		}
		if err != nil {
			return fmt.Errorf("fixture database operation failed")
		}
		return nil
	}
	var session proto.Session
	if err := json.Unmarshal([]byte(`{"session_id":"compat-session","machine_id":"compat-machine","title":"Compatibility fixture","cwd":"/workspace/example","agent":"claude","status":"idle","state":"done","started_by_jarvis":true,"started_by_persona":true,"permission_mode":"default"}`), &session); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	writer := bufio.NewWriter(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		result := any(map[string]any{"ok": true})
		switch req.Method {
		case "node.info":
			result = map[string]any{"version": "compat-fixture", "protocol": nodeproto.Version, "protocol_min": nodeproto.MinManagerVersion, "capabilities": linesOnly(nodeproto.Announced), "os": "linux", "arch": "amd64", "home": "/home/example", "hostname": "fixture", "engine": "herdr", "model_configuration": true}
		case "sessions.list":
			result = map[string]any{"sessions": []proto.Session{session}}
		case "sessions.revs":
			result = map[string]int{"compat-session": 1}
		case "session.info":
			result = session
		case "session.items", "session.read":
			result = map[string]any{"items": []map[string]any{{"seq": 1, "kind": "text", "role": "assistant", "text": "Fixture answer"}}, "backlog": []any{}, "has_more": false}
		case "session.send":
			if path := os.Getenv("PERSONA_COMPAT_SEND_LOG"); path != "" {
				f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					return err
				}
				fmt.Fprintln(f, "send")
				f.Close()
			}
			result = map[string]any{"ok": true, "session_id": "compat-session"}
		case "launchers.list":
			result = []any{}
		case "mcp.tools":
			result = map[string]any{"tools": []any{}}
		}
		if req.ID == nil {
			continue
		}
		b, err := json.Marshal(map[string]any{"id": req.ID, "result": result})
		if err != nil {
			return err
		}
		if _, err = writer.Write(append(b, '\n')); err != nil {
			return err
		}
		if err = writer.Flush(); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// linesOnly is caps without lane framing (docs/protocol/protocol.md "Lanes"): this fixture reads and writes JSON lines
// only. By name, so the baseline's nodeproto, which has no constant for it, builds it too.
func linesOnly(caps []string) []string {
	var out []string
	for _, c := range caps {
		if c != "node_lanes_v1" {
			out = append(out, c)
		}
	}
	return out
}
