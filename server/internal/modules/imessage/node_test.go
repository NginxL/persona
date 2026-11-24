package imessage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const messagesHome = "testdata/home" // synthetic (testdata/gen/make.py)

func TestLocalConnectorsOnlyOnAMac(t *testing.T) {
	ctx := context.Background()
	res, err := serveLocalConnector(ctx, &Reader{Home: messagesHome}, false, "connector.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(res); string(raw) != `{"connectors":[]}` {
		t.Errorf("list = %s", raw)
	}
	if _, err := serveLocalConnector(ctx, &Reader{Home: messagesHome}, false, "connector.chats", json.RawMessage(`{"id":"imessage"}`)); err == nil {
		t.Error("chats off a Mac")
	}
	if _, err := serveLocalConnector(ctx, &Reader{Home: messagesHome}, true, "connector.chats", json.RawMessage(`{"id":"github"}`)); err == nil {
		t.Error("a connector this node does not have")
	}
}

// Without Full Disk Access the list says so with the steps, and a read fails with no_permission.
func TestLocalConnectorWithoutPermission(t *testing.T) {
	ctx := context.Background()
	denied := filepath.Join(t.TempDir(), "sqlite3")
	_ = os.WriteFile(denied, []byte("#!/bin/sh\necho 'Error: in prepare, authorization denied (23)' >&2\nexit 1\n"), 0o755)
	r := &Reader{Home: messagesHome, SQLite: denied, Via: "ssh"}
	res, err := serveLocalConnector(ctx, r, true, "connector.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	var list struct {
		Connectors []struct {
			ID     string   `json:"id"`
			Status string   `json:"status"`
			Guide  []string `json:"guide"`
			Via    string   `json:"via"`
		} `json:"connectors"`
	}
	_ = json.Unmarshal(raw, &list)
	if len(list.Connectors) != 1 || list.Connectors[0].ID != "imessage" || list.Connectors[0].Status != "no_permission" ||
		len(list.Connectors[0].Guide) != 3 || list.Connectors[0].Via != "ssh" {
		t.Errorf("list = %s", raw)
	}
	_, err = serveLocalConnector(ctx, r, true, "connector.older", json.RawMessage(`{"id":"imessage","chat":"imessage-000000000000"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeNoPermission {
		t.Errorf("older = %v", err)
	}
}

func TestLocalConnectorReadsMessages(t *testing.T) {
	ctx := context.Background()
	r := &Reader{Home: messagesHome, SQLite: sqlite3WithJSON(t)}
	res, err := serveLocalConnector(ctx, r, true, "connector.chats", json.RawMessage(`{"id":"imessage","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	chats := res.(ChatsPage)
	if len(chats.Chats) != 2 || !chats.More || chats.Before == "" {
		t.Fatalf("chats = %+v", chats)
	}
	res, err = serveLocalConnector(ctx, r, true, "connector.older", json.RawMessage(`{"id":"imessage","chat":"`+chats.Chats[0].ID+`","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if page := res.(MessagesPage); len(page.Messages) != 3 || !page.More || page.Chat.ID != chats.Chats[0].ID {
		t.Errorf("older = %+v", page)
	}
	_, err = serveLocalConnector(ctx, r, true, "connector.older", json.RawMessage(`{"id":"imessage","chat":"imessage-000000000000"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeNoChat {
		t.Errorf("unknown chat = %v", err)
	}

	// What goes to Persona: changes after a cursor (the first from days back), and a search.
	res, err = serveLocalConnector(ctx, r, true, "connector.changes", json.RawMessage(`{"id":"imessage","days":4000,"limit":100}`))
	if err != nil {
		t.Fatal(err)
	}
	if changes := res.(ChangesPage); len(changes.Batches) != 4 || changes.Next != "22" {
		t.Errorf("changes = %d batches, next %q", len(changes.Batches), changes.Next)
	}
	res, err = serveLocalConnector(ctx, r, true, "connector.search", json.RawMessage(`{"id":"imessage","query":"周五","days":4000}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(res); !strings.Contains(string(raw), "示例消息：周五下午三点见") {
		t.Errorf("search = %s", raw)
	}
}
