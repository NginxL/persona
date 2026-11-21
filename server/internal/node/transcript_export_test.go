package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestStrictExportRefusesCachedTranscriptWhenTheSourceDisappears(t *testing.T) {
	n, home := testNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(home, "fixture.jsonl")
	body := strings.Repeat("long answer ", 3000)
	line, err := json.Marshal(map[string]any{
		"type": "assistant", "uuid": "answer", "timestamp": "2020-01-01T00:00:00Z",
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": body}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	n.setPath(id, path)
	request := func(method string, values map[string]any) (map[string]any, error) {
		values["session_id"] = id
		params, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		result, err := n.handle(context.Background(), method, params)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out, nil
	}
	page, err := request("session.items", map[string]any{"strict_read": true})
	if err != nil || page["strict_read"] != true || page["from_seq"] != float64(0) || page["to_seq"] != float64(1) {
		t.Fatalf("expected fresh page: %+v %v", page, err)
	}
	items := page["items"].([]any)
	idOnPage := items[0].(map[string]any)["id"]
	full, err := request("session.item", map[string]any{"strict_read": true, "item_id": idOnPage})
	if err != nil || full["strict_read"] != true || full["source_text_bytes"] != float64(len(body)) || full["text"] != body {
		t.Fatalf("fresh full read: %v %v %v", full["strict_read"], full["source_text_bytes"], err)
	}
	// An in-place edit can keep the inode and size. The strict read notices
	// the new modification time while preserving normal fast-path behavior.
	rewritten := strings.Replace(string(line), "long answer ", "next answer ", 1)
	if err := os.WriteFile(path, []byte(rewritten+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	changed, err := request("session.items", map[string]any{"strict_read": true})
	if err != nil || changed["history"] == page["history"] || !strings.Contains(changed["items"].([]any)[0].(map[string]any)["text"].(string), "next answer") {
		t.Fatalf("equal-size rewrite was not observed: %v %v", changed["history"], err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	legacy, err := request("session.items", map[string]any{})
	if err != nil || len(legacy["items"].([]any)) != 1 || legacy["strict_read"] != nil {
		t.Fatalf("ordinary paging no longer shows the cached item: %v %v", len(legacy), err)
	}
	for _, target := range []struct {
		method string
		params map[string]any
	}{
		{"session.items", map[string]any{"strict_read": true}},
		{"session.items", map[string]any{"strict_read": true, "before_seq": 1}},
		{"session.item", map[string]any{"strict_read": true, "item_id": idOnPage}},
	} {
		if _, err := request(target.method, target.params); err == nil {
			t.Errorf("%s acknowledged an unreadable cached source", target.method)
		}
	}
}

func TestStrictExportRefusesCachedDatabaseProjectionAfterRefreshFailure(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Fixture chat", time.Now(), 0))
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0,
		items: [][2]string{userItem("u0", "original projected text")}}})
	id, text := readChat(t, n, appChat)
	if id == "" || !strings.Contains(text, "original projected text") {
		t.Fatal("did not build the test projection")
	}
	db := filepath.Join(home, "thread_history_1.sqlite")
	if err := os.WriteFile(db, []byte("unreadable database fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := n.refreshCodexAppHistory(id); err == nil {
		t.Fatal("test did not force a projection refresh failure")
	}
	for _, method := range []string{"session.items", "session.item"} {
		params, _ := json.Marshal(map[string]any{"session_id": id, "strict_read": true, "item_id": "u0"})
		if _, err := n.handle(context.Background(), method, params); refusalCode(err) != proto.CodeNodeOutdated {
			t.Errorf("%s did not explicitly refuse a stale SQLite projection: %v", method, err)
		}
	}
}
