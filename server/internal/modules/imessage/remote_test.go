package imessage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// node answers the connector's calls the way a Mac's node would, from list (connector.list) or with err.
type node struct {
	list  string
	err   error
	calls []string
}

func (n *node) call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	n.calls = append(n.calls, method+" "+string(raw))
	if n.err != nil {
		return n.err
	}
	switch method {
	case "connector.list":
		return json.Unmarshal([]byte(n.list), out)
	case "connector.chats":
		return json.Unmarshal([]byte(`{"chats":[{"id":"imessage-0123456789ab","name":"张三","members":[],"services":["iMessage"],"last":"2026-10-30T08:00:00Z"}]}`), out)
	}
	return json.Unmarshal([]byte(`{"chat":{"id":"imessage-0123456789ab","name":"张三","members":[],"services":[],"last":"2026-10-30T08:00:00Z"},"messages":[]}`), out)
}

func TestRemoteStatus(t *testing.T) {
	ctx := context.Background()
	n := &node{list: `{"connectors":[{"id":"imessage","status":"ok","via":"ssh"}]}`}
	c := NewRemote(Config{Machine: "m1"}, "示例 Mac", n.call)
	if st := c.Status(ctx); st.State == connectorwire.StateOK {
		t.Fatal("ok before asking")
	}
	c.Check(ctx)
	if st := c.Status(ctx); st.State != connectorwire.StateOK || st.Account != "示例 Mac" || st.Via != "示例 Mac 上的「信息」" || st.LastSync.IsZero() {
		t.Errorf("ok: %+v", st)
	}

	n.list = `{"connectors":[{"id":"imessage","status":"no_permission","error":"macOS 不让 Persona 读","guide":["一","二","三"],"via":"launchd"}]}`
	c.Check(ctx)
	if d := c.Detail(ctx); d.Status != connectorwire.StateNoPermission || len(d.Guide) != 3 || d.Via != "launchd" || d.Machine != "示例 Mac" {
		t.Errorf("no permission: %+v", d)
	}

	for err, want := range map[error]string{
		ErrOffline:             "示例 Mac 不在线",
		ErrNodeTooOld:          "示例 Mac 上的 personad 太旧，更新后才能读「信息」",
		errors.New("whatever"): "读不了 示例 Mac 上的「信息」：whatever",
	} {
		n.err = err
		c.Check(ctx)
		if st := c.Status(ctx); st.State != connectorwire.StateError || st.Error != want {
			t.Errorf("%v: %+v", err, st)
		}
		if d := c.Detail(ctx); len(d.Guide) != 0 {
			t.Errorf("%v: guide %v", err, d.Guide)
		}
	}
	n.err, n.list = nil, `{"connectors":[]}`
	c.Check(ctx)
	if st := c.Status(ctx); st.State != connectorwire.StateNotConfigured {
		t.Errorf("not a Mac: %+v", st)
	}
	// The assistant reads the messages only while they are handed to Persona.
	c.SetAI(false)
	if c.Tools() != nil {
		t.Error("tools while 交给 Persona is off")
	}
}

func TestRemoteReads(t *testing.T) {
	ctx := context.Background()
	n := &node{}
	c := NewRemote(Config{Machine: "m1"}, "示例 Mac", n.call)
	page, err := c.Chats(ctx, 20, "123.imessage-0123456789ab")
	if err != nil || len(page.Chats) != 1 || page.Chats[0].Name != "张三" {
		t.Fatalf("chats %+v %v", page, err)
	}
	if _, err := c.Messages(ctx, "imessage-0123456789ab", 30, "5.7"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`connector.chats {"before":"123.imessage-0123456789ab","id":"imessage","limit":20}`,
		`connector.older {"before":"5.7","chat":"imessage-0123456789ab","id":"imessage","limit":30}`,
	}
	if strings.Join(n.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(n.calls, "\n"))
	}

	// Only conversation ids go to the Mac.
	for _, bad := range []string{"", "+15550100", "imessage-0123", "imessage-0123456789AB", "imessage-0123456789ab/../x"} {
		if _, err := c.Messages(ctx, bad, 30, ""); !errors.Is(err, ErrNoChat) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if len(n.calls) != 2 {
		t.Errorf("sent %d calls", len(n.calls))
	}

	// A read refused for want of permission asks the Mac again for the status (and its steps).
	n.err = ErrNoPermission
	if _, err := c.Chats(ctx, 20, ""); !errors.Is(err, ErrNoPermission) {
		t.Fatal(err)
	}
	select {
	case <-c.poke:
	default:
		t.Error("no refresh after a refused read")
	}
}
