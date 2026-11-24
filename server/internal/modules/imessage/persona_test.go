package imessage

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// scripted is a Mac's node answering each method with fixed JSON, recording the params it was asked with.
type scripted struct {
	mu      sync.Mutex
	answers map[string]string
	calls   []string
}

func (s *scripted) call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	s.mu.Lock()
	s.calls = append(s.calls, method+" "+string(raw))
	answer := s.answers[method]
	s.mu.Unlock()
	if method == "connector.list" {
		answer = `{"connectors":[{"id":"imessage","status":"ok"}]}`
	}
	return json.Unmarshal([]byte(answer), out)
}

func (s *scripted) sent(method string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if strings.HasPrefix(c, method+" ") {
			out = append(out, strings.TrimPrefix(c, method+" "))
		}
	}
	return out
}

func personaRemote(t *testing.T, answers map[string]string) (*Remote, *scripted) {
	t.Helper()
	n := &scripted{answers: answers}
	c := NewRemote(Config{Machine: "m1"}, "示例 Mac", n.call)
	c.Zone = func() *time.Location { return time.UTC }
	c.Check(context.Background())
	return c, n
}

const zhangJSON = `{"handle":"+15550100","name":"张三"}`

func TestChangesGoToTheKnowledgeBase(t *testing.T) {
	ctx := context.Background()
	c, n := personaRemote(t, map[string]string{"connector.changes": `{"next":"42","more":true,"batches":[
 {"chat":{"id":"imessage-0123456789ab","name":"张三","members":[` + zhangJSON + `],"services":["SMS"],"last":"2026-10-30T08:00:00Z"},
  "messages":[
   {"id":"7","at":"2026-10-30T07:00:00Z","kind":"message","sender":` + zhangJSON + `,"text":"示例：周五见","service":"SMS"},
   {"id":"8","at":"2026-10-30T07:10:00Z","kind":"message","from_me":true,"text":"好","reply_to":"示例：周五见"},
   {"id":"9","at":"2026-10-30T07:20:00Z","kind":"message","sender":` + zhangJSON + `,"attachments":[{"kind":"image","name":"IMG_0001.HEIC"},{"kind":"file","name":"路线.pdf"}]},
   {"id":"10","at":"2026-10-30T07:30:00Z","kind":"message","sender":` + zhangJSON + `,"unsent":true},
   {"id":"11","at":"2026-10-30T07:40:00Z","kind":"message","sender":` + zhangJSON + `,"undecoded":true}]},
 {"chat":{"id":"imessage-ba9876543210","name":"周末徒步","group":true,"members":[],"services":["iMessage"],"last":"2026-10-30T08:00:00Z"},
  "messages":[{"id":"12","at":"2026-10-30T07:50:00Z","kind":"event","from_me":true,"text":"你把群名改成「周末徒步」"}]}]}`})

	batches, next, more, err := c.Changes(ctx, "30", 20)
	if err != nil || next != "42" || !more || len(batches) != 2 {
		t.Fatalf("%d batches, next %q, more %v, %v", len(batches), next, more, err)
	}
	if got := n.sent("connector.changes"); len(got) != 1 || got[0] != `{"cursor":"30","days":30,"id":"imessage","keep":"contacts","limit":300}` {
		t.Errorf("asked %v", got)
	}
	dm := batches[0]
	if dm.Chat.ID != "0123456789ab" || dm.Chat.Kind != "dm" || dm.Chat.Name != "张三" || batches[1].Chat.Kind != "group" {
		t.Errorf("chat %+v", dm.Chat)
	}
	var lines []string
	for _, m := range dm.Messages {
		lines = append(lines, m.SenderName+"|"+m.Text)
		if !m.Mine && (m.SenderID != PersonKey("+15550100") || strings.Contains(m.SenderID, "555")) {
			t.Errorf("sender id %q", m.SenderID)
		}
	}
	want := "张三|示例：周五见\n|回复「示例：周五见」：好\n张三|[图片] [文件 路线.pdf]\n张三|（撤回了一条消息）\n张三|[无法显示的消息]"
	if strings.Join(lines, "\n") != want {
		t.Errorf("lines:\n%s", strings.Join(lines, "\n"))
	}
	if ev := batches[1].Messages[0]; ev.Type != "SYSTEM" || !ev.Mine {
		t.Errorf("event %+v", ev)
	}

	// Off: nothing is asked, the cursor stays.
	c.SetAI(false)
	batches, next, more, err = c.Changes(ctx, "42", 20)
	if err != nil || len(batches) != 0 || next != "42" || more || len(n.sent("connector.changes")) != 1 {
		t.Errorf("off: %d batches, next %q, %v", len(batches), next, err)
	}
}

func chatsPage(now time.Time) string {
	at := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	return `{"chats":[
 {"id":"imessage-000000000001","name":"张三","members":[` + zhangJSON + `],"services":["iMessage"],"last":"` + at(time.Minute) + `","preview":"示例：验证码 123456 别告诉别人","unread":2},
 {"id":"imessage-000000000002","name":"周末徒步","group":true,"members":[` + zhangJSON + `],"services":["iMessage"],"last":"` + at(time.Hour) + `","preview":"张三：我也去","unread":1},
 {"id":"imessage-000000000003","name":"Alice Example","members":[{"handle":"alice@example.com","name":"Alice Example"}],"services":["iMessage"],"last":"` + at(2*time.Hour) + `","preview":"好的","from_me":true},
 {"id":"imessage-000000000004","name":"李四","members":[{"handle":"+15550101","name":"李四"}],"services":["SMS"],"last":"` + at(3*time.Hour) + `","preview":"收到"},
 {"id":"imessage-000000000005","name":"王五","members":[{"handle":"+15550103","name":"王五"}],"services":["SMS"],"last":"` + at(10*24*time.Hour) + `","preview":"很久以前","unread":1}]}`
}

const olderPage = `{"chat":{"id":"imessage-000000000001","name":"张三","members":[],"services":[],"last":"2026-10-30T08:00:00Z"},"messages":[
 {"id":"20","at":"2026-10-30T07:58:00Z","kind":"message","from_me":true,"text":"在吗"},
 {"id":"21","at":"2026-10-30T07:59:00Z","kind":"message","sender":` + zhangJSON + `,"text":"示例：验证码 123456 别告诉别人"}]}`

func TestTheInboxSeesUnreadConversations(t *testing.T) {
	ctx := context.Background()
	c, n := personaRemote(t, map[string]string{"connector.chats": chatsPage(time.Now()), "connector.older": olderPage})
	var scopes []string
	changed := func(s connectorwire.Scope) { scopes = append(scopes, string(s)) }
	c.look(ctx, changed)
	things, _ := c.Collect(ctx)
	if len(things) != 4 || strings.Join(scopes, ",") != "chats,inbox" {
		t.Fatalf("%d things, scopes %v", len(things), scopes)
	}
	zhang, group, alice, li := things[0], things[1], things[2], things[3]
	if zhang.Kind != "dm" || zhang.Tier != "action" || zhang.OpenURL != "sms:+15550100" || zhang.CanReply || zhang.Read || zhang.Done != "" ||
		zhang.Preview != "示例：验证码 •••• 别告诉别人" || zhang.Context != "我（10-30 07:58）：在吗\n张三（10-30 07:59）：示例：验证码 •••• 别告诉别人" {
		t.Errorf("张三 = %+v", zhang)
	}
	if group.Kind != "group" || group.Label != "群聊" || group.Tier != "fyi" || group.OpenURL != "" || group.Sender != "张三" {
		t.Errorf("group = %+v", group)
	}
	if alice.Done != connectorwire.DoneReplied || alice.Context != "" || !li.Read || li.Context != "" {
		t.Errorf("answered / read = %+v / %+v", alice, li)
	}
	// Each unread conversation's last messages are read once for its newest message.
	if got := n.sent("connector.older"); len(got) != 2 || !strings.Contains(got[0], `"keep":"contacts"`) {
		t.Errorf("older %v", got)
	}
	scopes = nil
	c.look(ctx, changed)
	if len(scopes) != 0 || len(n.sent("connector.older")) != 2 {
		t.Errorf("nothing new: scopes %v, %d reads", scopes, len(n.sent("connector.older")))
	}

	c.SetAI(false)
	if things, _ := c.Collect(ctx); len(things) != 0 {
		t.Errorf("off: %d things", len(things))
	}
	if err := c.Reply(ctx, connectorwire.InboxReply{Thread: "imessage-000000000001", Text: "好"}); err == nil {
		t.Error("Persona sent a message")
	}
}

func TestToolsReadTheMac(t *testing.T) {
	ctx := context.Background()
	c, n := personaRemote(t, map[string]string{"connector.chats": chatsPage(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)),
		"connector.older": olderPage,
		"connector.search": `{"hits":[{"chat":{"id":"imessage-000000000001","name":"张三","members":[],"services":[],"last":"2026-10-30T08:00:00Z"},
		  "message":{"id":"21","at":"2026-10-30T07:59:00Z","kind":"message","sender":` + zhangJSON + `,"text":"示例：周五见"}}]}`})
	var names []string
	for _, tool := range c.Tools() {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "imessage_chats,imessage_read,imessage_search" {
		t.Fatalf("tools %v", names)
	}

	out, err := c.Call(ctx, "imessage_chats", nil)
	if err != nil || !strings.Contains(out, "- 张三 [imessage-000000000001] · 私聊 +15550100 · 2026-10-30 08:59 · 未读 2：示例：验证码 •••• 别告诉别人") ||
		!strings.Contains(out, "周末徒步 [imessage-000000000002] · 群聊（张三）") || !strings.Contains(out, "：我：好的") {
		t.Errorf("chats:\n%s\n%v", out, err)
	}
	out, err = c.Call(ctx, "imessage_read", json.RawMessage(`{"chat":"imessage-000000000001","limit":5}`))
	if err != nil || !strings.Contains(out, "[2026-10-30 07:59] 张三：示例：验证码 •••• 别告诉别人") || !strings.Contains(out, "[2026-10-30 07:58] 我：在吗") {
		t.Errorf("read:\n%s\n%v", out, err)
	}
	if got := n.sent("connector.older"); len(got) != 1 || !strings.Contains(got[0], `"limit":5`) || !strings.Contains(got[0], `"keep":"contacts"`) {
		t.Errorf("older %v", got)
	}
	out, err = c.Call(ctx, "imessage_search", json.RawMessage(`{"query":"周五"}`))
	if err != nil || !strings.Contains(out, "- [2026-10-30 07:59] 「张三」[imessage-000000000001] 张三：示例：周五见") {
		t.Errorf("search:\n%s\n%v", out, err)
	}
	if got := n.sent("connector.search"); len(got) != 1 || got[0] != `{"days":365,"id":"imessage","keep":"contacts","limit":20,"query":"周五"}` {
		t.Errorf("search params %v", got)
	}
	for _, bad := range []string{`{}`, `{"chat":"+15550100"}`} {
		if _, err := c.Call(ctx, "imessage_read", json.RawMessage(bad)); err == nil {
			t.Errorf("read %s", bad)
		}
	}
	if _, err := c.Call(ctx, "imessage_search", json.RawMessage(`{"query":" "}`)); err == nil {
		t.Error("empty search")
	}

	c.SetAI(false)
	if c.Tools() != nil {
		t.Error("tools while off")
	}
	if _, err := c.Call(ctx, "imessage_chats", nil); err == nil {
		t.Error("a tool ran while off")
	}
}
