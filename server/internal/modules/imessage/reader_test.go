package imessage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// sqlite3WithJSON is the sqlite3 on PATH when it can print JSON rows: the reader asks it for "-json", which SQLite
// has since 3.33 (macOS's own from macOS 12 on). Elsewhere — an old install, a container — the tests that read the
// fixture skip instead of failing on an option it does not know.
func sqlite3WithJSON(t *testing.T) string {
	t.Helper()
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("no sqlite3 here (macOS has one): run these on a Mac")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, sqlite, "-json", ":memory:", "SELECT 1 AS n").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"n"`) {
		t.Skipf("sqlite3 at %s has no -json (SQLite 3.33 or newer; macOS's own has it): %s", sqlite, strings.TrimSpace(string(out)))
	}
	return sqlite
}

// fixture reads testdata/home: a made-up Mac home with Messages' chat.db and an address book (testdata/gen/make.py),
// every row synthetic. It needs the sqlite3 command line, which macOS has; elsewhere the test is skipped.
func fixture(t *testing.T) *Reader {
	t.Helper()
	return &Reader{Home: "testdata/home", SQLite: sqlite3WithJSON(t), Via: "ssh"}
}

func TestChatsAreConversationsNewestFirst(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	page, err := r.Chats(ctx, 50, "", "")
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ name, preview string }
	var got []row
	for _, c := range page.Chats {
		got = append(got, row{c.Name, c.Preview})
	}
	want := []row{
		{"张三", "👍 好的 🎉"},                    // iMessage and SMS chats of one person: one conversation
		{"12345", "【示例】验证码 123456，5 分钟内有效"}, // not in the contacts: the number
		{"李四、+15550102", "收到"},              // a group without a name: its members (the second contact saved without +1)
		{"周末徒步", "你把+15550102加进了群聊"},        // the newest line is the group's: not the tapback-free message before it
		{"Alice Example", "[语音]"},
	}
	if len(got) != len(want) || page.More {
		t.Fatalf("chats = %+v (more %v)", got, page.More)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chat %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	zhang, hike := page.Chats[0], page.Chats[3]
	if strings.Join(zhang.Services, ",") != "SMS,iMessage" || zhang.Unread != 1 || zhang.Group || len(zhang.Members) != 1 ||
		zhang.Members[0] != (Person{Handle: "+15550100", Name: "张三"}) {
		t.Errorf("张三 = %+v", zhang)
	}
	if !hike.Group || hike.Unread != 1 || len(hike.Members) != 3 || !page.Chats[2].FromMe {
		t.Errorf("周末徒步 = %+v", hike)
	}
	if !zhang.Last.Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("last = %s", zhang.Last)
	}
	for _, c := range page.Chats {
		if !strings.HasPrefix(c.ID, "imessage-") || len(c.ID) != len("imessage-")+12 || strings.ContainsAny(c.ID[9:], "+@") {
			t.Errorf("id %q", c.ID)
		}
	}

	// Two at a time: the same order, nothing twice, nothing missed.
	var paged []string
	before := ""
	for range 5 {
		p, err := r.Chats(ctx, 2, before, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Chats {
			paged = append(paged, c.Name)
		}
		if !p.More {
			break
		}
		before = p.Before
	}
	if strings.Join(paged, "|") != "张三|12345|李四、+15550102|周末徒步|Alice Example" {
		t.Errorf("paged = %v", paged)
	}
}

func TestMessagesOfAConversation(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	chats, err := r.Chats(ctx, 50, "", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, c := range chats.Chats {
		byName[c.Name] = c.ID
	}

	page, err := r.Messages(ctx, byName["张三"], 50, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Chat.Name != "张三" || page.More {
		t.Fatalf("chat %+v more %v", page.Chat, page.More)
	}
	var texts []string
	for _, m := range page.Messages {
		texts = append(texts, m.Text)
	}
	// Oldest first, over SMS and iMessage; the tapback is not a message.
	want := []string{"示例：今天的快递到了吗", "到了，谢谢", "示例消息：周五下午三点见", "好的，我准时到", "", "改一下：三点半", "", "👍 好的 🎉"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("texts = %q", texts)
	}
	m := page.Messages
	if m[0].Service != "SMS" || m[0].Sender == nil || m[0].Sender.Name != "张三" || m[0].FromMe || !m[1].FromMe || m[1].Sender != nil {
		t.Errorf("first two = %+v / %+v", m[0], m[1])
	}
	if m[3].ReplyTo != "示例消息：周五下午三点见" {
		t.Errorf("reply = %+v", m[3])
	}
	if len(m[4].Attachments) != 1 || m[4].Attachments[0] != (Attachment{Kind: "image", Name: "IMG_0001.HEIC", Mime: "image/heic", Size: 1234567}) {
		t.Errorf("photo = %+v", m[4])
	}
	if !m[5].Edited || !m[6].Unsent || !m[7].Unread || m[6].Unread {
		t.Errorf("edited / unsent / unread = %+v / %+v / %+v", m[5], m[6], m[7])
	}

	// Three at a time, back to the start.
	var paged []string
	before := ""
	for range 5 {
		p, err := r.Messages(ctx, byName["张三"], 3, before, "")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range p.Messages {
			ids = append(ids, m.ID)
		}
		paged = append([]string{strings.Join(ids, ",")}, paged...)
		if !p.More {
			break
		}
		before = p.Before
	}
	if strings.Join(paged, "|") != "1,2|3,5,6|7,8,9" {
		t.Errorf("pages = %v", paged)
	}

	group, err := r.Messages(ctx, byName["周末徒步"], 50, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, m := range group.Messages {
		who := "我"
		if m.Sender != nil {
			who = m.Sender.shown()
		}
		line := m.Kind + " " + who + "：" + m.Text
		for _, a := range m.Attachments {
			line += " [" + a.Kind + " " + a.Name + "]"
		}
		lines = append(lines, line)
	}
	wantLines := []string{
		"event 我：你把群名改成「周末徒步」",
		"message 李四：@张三 明天的示例会议改到十点",
		"message Alice Example：示例：带水和零食 [file 路线.pdf]",
		"message 张三：我也去",
		"event 我：你把+15550102加进了群聊",
	}
	if strings.Join(lines, "\n") != strings.Join(wantLines, "\n") {
		t.Errorf("group:\n%s", strings.Join(lines, "\n"))
	}

	code, err := r.Messages(ctx, byName["12345"], 50, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(code.Messages) != 3 || !code.Messages[0].At.Equal(time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!code.Messages[1].Undecoded || code.Messages[1].Text != "" || code.Messages[2].Undecoded {
		t.Errorf("short code = %+v", code.Messages)
	}

	if _, err := r.Messages(ctx, "imessage-000000000000", 50, "", ""); !errors.Is(err, ErrNoChat) {
		t.Errorf("unknown id: %v", err)
	}
	for _, bad := range []string{"x", "1.x", "1;DROP TABLE message"} {
		if _, err := r.Messages(ctx, byName["张三"], 50, bad, ""); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	if st := (&Reader{Home: t.TempDir()}).Status(ctx); st.State != connectorwire.StateNotConfigured {
		t.Errorf("no Messages: %+v", st)
	}

	// What macOS's sqlite3 says when the process has no Full Disk Access.
	dir := t.TempDir()
	denied := filepath.Join(dir, "sqlite3")
	_ = os.WriteFile(denied, []byte("#!/bin/sh\necho 'Error: unable to open database \"chat.db\": unable to open database file' >&2\nexit 1\n"), 0o755)
	ssh := (&Reader{Home: "testdata/home", SQLite: denied, Via: "ssh"}).Status(ctx)
	if ssh.State != connectorwire.StateNoPermission || len(ssh.Guide) != 3 || !strings.Contains(ssh.Guide[1], "允许远程用户完全访问磁盘") {
		t.Errorf("ssh: %+v", ssh)
	}
	launchd := (&Reader{Home: "testdata/home", SQLite: denied, Via: "launchd", Executable: "/opt/persona/personad"}).Status(ctx)
	if launchd.State != connectorwire.StateNoPermission || !strings.Contains(launchd.Guide[1], "/opt/persona/personad") {
		t.Errorf("launchd: %+v", launchd)
	}
	if _, err := (&Reader{Home: "testdata/home", SQLite: denied}).Chats(ctx, 10, "", ""); !errors.Is(err, ErrNoPermission) {
		t.Errorf("chats without permission: %v", err)
	}

	if st := fixture(t).Status(ctx); st.State != connectorwire.StateOK || st.Error != "" {
		t.Errorf("fixture: %+v", st)
	}
}

func TestContactNames(t *testing.T) {
	c := &contacts{byNumber: map[string]string{}, bySeven: map[string]string{}, byEmail: map[string]string{"alice@example.com": "Alice"}}
	c.addNumber("+86 138 0000 0000", "示例甲")
	c.addNumber("(415) 555-0100", "Example B")
	c.addNumber("555 0101", "示例乙")
	c.addNumber("+1 555 0199", "示例丙")
	c.addNumber("+44 555 0199", "示例丁") // the same last seven digits as the contact above: neither by seven
	for handle, want := range map[string]string{
		"+8613800000000":    "示例甲",
		"+14155550100":      "Example B",
		"+15550101":         "示例乙",
		"ALICE@example.com": "Alice",
		"+15550199":         "示例丙", // its own ten digits
		"+15550198":         "",
		"12345":             "",
		"":                  "",
	} {
		if got := c.name(handle); got != want {
			t.Errorf("%q = %q, want %q", handle, got, want)
		}
	}
	for row, want := range map[contactRow]string{
		{First: p("三"), Last: p("张")}:           "张三",
		{First: p("Alice"), Last: p("Example")}: "Alice Example",
		{Nick: p("小王")}:                         "小王",
		{Org: p("示例公司")}:                        "示例公司",
		{First: p(" "), Org: p("示例公司")}:         "示例公司",
		{First: p("Bob")}:                       "Bob",
	} {
		if got := row.name(); got != want {
			t.Errorf("%+v = %q, want %q", row, got, want)
		}
	}
}

func p(s string) *string { return &s }

func TestAppleTime(t *testing.T) {
	ns := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if got := appleTime((ns.Unix() - appleEpoch) * 1e9); !got.Equal(ns) {
		t.Errorf("nanoseconds: %s", got)
	}
	old := time.Date(2016, 5, 1, 0, 0, 0, 0, time.UTC)
	if got := appleTime(old.Unix() - appleEpoch); !got.Equal(old) {
		t.Errorf("seconds: %s", got)
	}
	if !appleTime(0).IsZero() {
		t.Error("zero")
	}
}
