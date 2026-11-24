package imessage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var fixtureSince = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// Everything after a cursor, by ROWID: the real conversations only (someone in the contacts, or the user wrote),
// tapbacks left out; the cursor moves past what was left out too.
func TestChangesFromTheStart(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	page, err := r.Changes(ctx, "", 100, fixtureSince, KeepContacts)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range page.Batches {
		var ids []string
		for _, m := range b.Messages {
			ids = append(ids, m.ID)
		}
		got = append(got, b.Chat.Name+":"+strings.Join(ids, ","))
	}
	// Zhang San over SMS then iMessage (4 is a tapback), Alice, the named group, the unnamed one (Li Si is a contact); the
	// short code nobody saved is left out.
	want := "张三:1,2,3,5,6,7,8,9|Alice Example:10,11,12|周末徒步:13,14,15,16,17|李四、+15550102:18,19"
	if strings.Join(got, "|") != want || page.Next != "22" || page.More {
		t.Fatalf("batches %v next %q more %v", got, page.Next, page.More)
	}
	zhang := page.Batches[0]
	if zhang.Messages[2].Text != "示例消息：周五下午三点见" || zhang.Messages[3].ReplyTo != "示例消息：周五下午三点见" ||
		zhang.Messages[0].Service != "SMS" || zhang.Messages[0].Sender == nil || zhang.Messages[0].Sender.Name != "张三" {
		t.Errorf("张三 = %+v", zhang.Messages[:4])
	}
	if group := page.Batches[2]; !group.Chat.Group || group.Messages[0].Kind != "event" || group.Messages[2].Attachments[0].Name != "路线.pdf" {
		t.Errorf("group = %+v", group)
	}

	// Nothing new after the last one.
	again, err := r.Changes(ctx, page.Next, 100, fixtureSince, KeepContacts)
	if err != nil || len(again.Batches) != 0 || again.Next != "22" {
		t.Fatalf("again: %+v %v", again, err)
	}

	// Everything, codes masked; the 2017 message came in late (a high ROWID), so it is new here.
	all, err := r.Changes(ctx, "", 100, fixtureSince, KeepAll)
	if err != nil {
		t.Fatal(err)
	}
	last := all.Batches[len(all.Batches)-1]
	if last.Chat.Name != "12345" || len(last.Messages) != 3 || last.Messages[2].Text != "【示例】验证码 ••••，5 分钟内有效" || !last.Messages[1].Undecoded {
		t.Errorf("short code = %+v", last)
	}
}

// Five at a time from the start: the same messages, none twice, and the cursor where the last page ended.
func TestChangesPage(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	var ids []string
	cursor := ""
	for range 10 {
		page, err := r.Changes(ctx, cursor, 5, fixtureSince, KeepContacts)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range page.Batches {
			for _, m := range b.Messages {
				ids = append(ids, m.ID)
			}
		}
		cursor = page.Next
		if !page.More {
			break
		}
	}
	if strings.Join(ids, ",") != "1,2,3,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19" || cursor != "22" {
		t.Errorf("ids %v cursor %q", ids, cursor)
	}
	// A start after the newest message: nothing yet, and the cursor stays at the newest.
	future, err := r.Changes(ctx, "", 5, time.Now().AddDate(1, 0, 0), KeepContacts)
	if err != nil || len(future.Batches) != 0 || future.Next != "22" {
		t.Errorf("future %+v %v", future, err)
	}
	if _, err := r.Changes(ctx, "x", 5, fixtureSince, KeepContacts); err == nil {
		t.Error("bad cursor accepted")
	}
}

func TestSearch(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	since := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	hits, err := r.Search(ctx, "周五", 10, since, KeepContacts)
	if err != nil {
		t.Fatal(err)
	}
	// Only in attributedBody: decoded to be found. The tapback that quotes it is not a message.
	if len(hits) != 1 || hits[0].Chat.Name != "张三" || hits[0].Message.Text != "示例消息：周五下午三点见" {
		t.Fatalf("hits %+v", hits)
	}
	if hits, _ := r.Search(ctx, "验证码", 10, since, KeepContacts); len(hits) != 0 {
		t.Errorf("a short code's message found: %+v", hits)
	}
	hits, _ = r.Search(ctx, "验证码", 10, since, KeepAll)
	if len(hits) != 1 || hits[0].Message.Text != "【示例】验证码 ••••，5 分钟内有效" {
		t.Errorf("all: %+v", hits)
	}
	hits, _ = r.Search(ctx, "EXAMPLE.COM", 10, since, KeepContacts)
	if len(hits) != 1 || hits[0].Chat.Name != "Alice Example" {
		t.Errorf("any case: %+v", hits)
	}
	hits, _ = r.Search(ctx, "示例", 2, since, KeepContacts)
	if len(hits) != 2 || !hits[0].Message.At.After(hits[1].Message.At) {
		t.Errorf("newest first, two: %+v", hits)
	}
	if _, err := r.Search(ctx, " ", 10, since, KeepContacts); err == nil {
		t.Error("empty query")
	}
}

func TestMaskCodes(t *testing.T) {
	for in, want := range map[string]string{
		"【示例】验证码 123456，5 分钟内有效":            "【示例】验证码 ••••，5 分钟内有效",
		"Your code is 8841 for example.com": "Your code is •••• for example.com",
		"动态密码 00912345":                     "动态密码 ••••",
		"示例：明天下午 1530 在 2 号会议室":             "示例：明天下午 1530 在 2 号会议室", // no code word: as it is
		"PIN: 4321":      "PIN: ••••",
		"barcode 123456": "barcode 123456", // a word that only contains "code"
	} {
		if got := MaskCodes(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

// The assistant and the inbox list and read only what goes to Persona; the app's own view lists everything.
func TestChatsAndMessagesKept(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	all, err := r.Chats(ctx, 50, "", "")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := r.Chats(ctx, 50, "", KeepContacts)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	code := ""
	for _, c := range kept.Chats {
		names = append(names, c.Name)
	}
	for _, c := range all.Chats {
		if c.Name == "12345" {
			code = c.ID
		}
	}
	if len(all.Chats) != 5 || strings.Join(names, "|") != "张三|李四、+15550102|周末徒步|Alice Example" {
		t.Fatalf("all %d, kept %v", len(all.Chats), names)
	}
	if _, err := r.Messages(ctx, code, 10, "", KeepContacts); !errors.Is(err, ErrNoChat) {
		t.Errorf("the short code's conversation for Persona: %v", err)
	}
	if p, err := r.Messages(ctx, code, 10, "", ""); err != nil || len(p.Messages) != 3 {
		t.Errorf("the short code's conversation in the app: %v", err)
	}
}
