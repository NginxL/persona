package imessage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// multiChatFixture copies only the repository's synthetic databases before adding associations to the copy.
func multiChatFixture(t *testing.T) *Reader {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{
		filepath.Join("Library", "Messages", "chat.db"),
		filepath.Join("Library", "Application Support", "AddressBook", "Sources", "S1", "AddressBook-v22.abcddb"),
	} {
		src := filepath.Join("testdata", "home", name)
		dst := filepath.Join(home, name)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := &Reader{Home: home, SQLite: sqlite3WithJSON(t), Via: "ssh"}
	modifyMultiChatFixture(t, r, `
INSERT INTO chat_message_join(chat_id, message_id, message_date)
  SELECT 2, ROWID, date FROM message WHERE ROWID IN (3, 5, 6, 9);
INSERT INTO chat_message_join(chat_id, message_id, message_date)
  SELECT 3, ROWID, date FROM message WHERE ROWID = 9;
INSERT INTO chat(ROWID, guid, style, chat_identifier, service_name)
  VALUES (7, 'fixture-short-code', 45, '12345', 'iMessage');
INSERT INTO chat_message_join(chat_id, message_id, message_date)
  SELECT 7, ROWID, date FROM message WHERE ROWID = 22;
UPDATE message SET text = 'Example code 123456' WHERE ROWID = 3;
UPDATE message SET text = 'Reply code 654321' WHERE ROWID = 5;
UPDATE message SET text = 'shared needle old' WHERE ROWID = 7;
UPDATE message SET text = 'shared needle new' WHERE ROWID = 9;
`)
	return r
}

func modifyMultiChatFixture(t *testing.T, r *Reader, sql string) {
	t.Helper()
	if out, err := exec.Command(r.SQLite, r.chatDB(), sql).CombinedOutput(); err != nil {
		t.Fatalf("synthetic fixture setup: %v: %s", err, out)
	}
}

func messageIDs(messages []Message) []string {
	var ids []string
	for _, m := range messages {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestMultiChatMessagesPagesAndUnread(t *testing.T) {
	r := multiChatFixture(t)
	ctx := context.Background()
	a := convID("person:+15550100")
	b := convID("person:alice@example.com")
	want := []string{"1", "2", "3", "5", "6", "7", "8", "9"}
	full, err := r.Messages(ctx, a, 50, "", KeepContacts)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(full.Messages); !reflect.DeepEqual(got, want) || full.More || full.Before != "" {
		t.Fatalf("full page = %v, more %v, before %q", got, full.More, full.Before)
	}
	if full.Messages[2].Text != "Example code 123456" || full.Messages[3].ReplyTo != "Example code 123456" ||
		len(full.Messages[4].Attachments) != 1 || full.Messages[4].Attachments[0].Name != "IMG_0001.HEIC" {
		t.Fatalf("message, reply or attachment changed: %+v", full.Messages[2:5])
	}
	other, err := r.Messages(ctx, b, 50, "", KeepAll)
	if err != nil || !reflect.DeepEqual(messageIDs(other.Messages), []string{"10", "11", "12", "9"}) {
		t.Fatalf("distinct conversation = %+v, %v", other, err)
	}

	for _, limit := range []int{1, 3, 8} {
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			var got []string
			before := ""
			for n := 0; n < 12; n++ {
				page, err := r.Messages(ctx, a, limit, before, KeepContacts)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Messages) == 0 || len(page.Messages) > limit {
					t.Fatalf("page has %d messages for limit %d", len(page.Messages), limit)
				}
				got = append(messageIDs(page.Messages), got...)
				if page.More != (len(got) < len(want)) {
					t.Fatalf("more = %v after %v", page.More, got)
				}
				if !page.More {
					if page.Before != "" {
						t.Fatalf("terminal page has cursor %q", page.Before)
					}
					break
				}
				if page.Before == "" || page.Before == before {
					t.Fatalf("cursor did not advance: %+v", page)
				}
				before = page.Before
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("paged messages = %v, want %v", got, want)
			}
		})
	}
	chats, err := r.Chats(ctx, 50, "", KeepAll)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, chat := range chats.Chats {
		counts[chat.ID] = chat.Unread
	}
	if counts[a] != 1 || counts[b] != 1 {
		t.Fatalf("unread counts by conversation = %v, want 1 in each", counts)
	}
}

func TestMultiChatMessagesEqualTimestampCursor(t *testing.T) {
	r := multiChatFixture(t)
	// Distinct messages in both chats share one timestamp, including messages joined to both chats.
	modifyMultiChatFixture(t, r, `
UPDATE message SET date = (SELECT date FROM message WHERE ROWID = 9)
  WHERE ROWID IN (1, 2, 3, 5, 6, 7, 8, 9);
UPDATE chat_message_join SET message_date = (SELECT date FROM message WHERE ROWID = message_id)
  WHERE message_id IN (1, 2, 3, 5, 6, 7, 8, 9);`)
	ctx := context.Background()
	conversation := convID("person:+15550100")
	at := appleNanos(time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC))
	tests := []struct {
		limit int
		pages [][]string
	}{
		{1, [][]string{{"9"}, {"8"}, {"7"}, {"6"}, {"5"}, {"3"}, {"2"}, {"1"}}},
		{3, [][]string{{"7", "8", "9"}, {"3", "5", "6"}, {"1", "2"}}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("limit-%d", tt.limit), func(t *testing.T) {
			before := ""
			for i, want := range tt.pages {
				page, err := r.Messages(ctx, conversation, tt.limit, before, KeepContacts)
				if err != nil {
					t.Fatal(err)
				}
				if got := messageIDs(page.Messages); !reflect.DeepEqual(got, want) {
					t.Fatalf("page %d = %v, want %v", i, got, want)
				}
				more := i+1 < len(tt.pages)
				cursor := ""
				if more {
					cursor = fmt.Sprintf("%d.%s", at, want[0])
				}
				if page.More != more || page.Before != cursor {
					t.Fatalf("page %d: more %v, cursor %q; want %v, %q", i, page.More, page.Before, more, cursor)
				}
				before = page.Before
			}
		})
	}
	// A saved date.ROWID cursor remains valid after restarting the reader, with ROWID breaking ties.
	restarted := &Reader{Home: r.Home, SQLite: r.SQLite, Via: r.Via}
	legacy := fmt.Sprintf("%d.6", at)
	page, err := restarted.Messages(ctx, conversation, 3, legacy, KeepContacts)
	if err != nil || !reflect.DeepEqual(messageIDs(page.Messages), []string{"2", "3", "5"}) ||
		!page.More || page.Before != fmt.Sprintf("%d.2", at) {
		t.Fatalf("legacy cursor %s = %+v, %v", legacy, page, err)
	}
}

func TestMultiChatChangesPagesAndLegacyCursor(t *testing.T) {
	r := multiChatFixture(t)
	ctx := context.Background()
	a := convID("person:+15550100")
	b := convID("person:alice@example.com")

	boundary, err := r.Changes(ctx, "8", 1, fixtureSince, KeepContacts)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, batch := range boundary.Batches {
		got[batch.Chat.ID] = messageIDs(batch.Messages)
	}
	if boundary.Next != "9" || !boundary.More || !reflect.DeepEqual(got[a], []string{"9"}) || !reflect.DeepEqual(got[b], []string{"9"}) || len(got) != 2 {
		t.Fatalf("limit-1 boundary = %+v, ids %v", boundary, got)
	}
	// A numeric cursor saved by an older version remains valid on a fresh Reader.
	restarted := &Reader{Home: r.Home, SQLite: r.SQLite, Via: r.Via}
	again, err := restarted.Changes(ctx, "8", 1, fixtureSince, KeepContacts)
	if err != nil || !reflect.DeepEqual(boundary, again) {
		t.Fatalf("restart at legacy cursor = %+v, %v", again, err)
	}
	next, err := restarted.Changes(ctx, "9", 1, fixtureSince, KeepContacts)
	if err != nil || next.Next != "10" || len(next.Batches) != 1 || !reflect.DeepEqual(messageIDs(next.Batches[0].Messages), []string{"10"}) {
		t.Fatalf("after legacy cursor 9 = %+v, %v", next, err)
	}

	want := map[string][]string{
		a: {"1", "2", "3", "5", "6", "7", "8", "9"},
		b: {"9", "10", "11", "12"},
		convID("group:iMessage;+;chat100000000000000001"): {"13", "14", "15", "16", "17"},
		convID("group:iMessage;+;chat100000000000000002"): {"18", "19"},
	}
	for _, limit := range []int{1, 3, 8, 50} {
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			collected := map[string][]string{}
			cursor := ""
			for n := 0; n < 30; n++ {
				page, err := r.Changes(ctx, cursor, limit, fixtureSince, KeepContacts)
				if err != nil {
					t.Fatal(err)
				}
				for _, batch := range page.Batches {
					collected[batch.Chat.ID] = append(collected[batch.Chat.ID], messageIDs(batch.Messages)...)
				}
				if page.Next == cursor {
					t.Fatalf("cursor did not advance: %+v", page)
				}
				cursor = page.Next
				if !page.More {
					break
				}
			}
			if cursor != "22" || !reflect.DeepEqual(collected, want) {
				t.Fatalf("cursor %q, batches %v, want %v", cursor, collected, want)
			}
		})
	}
	filtered, err := r.Changes(ctx, "19", 3, fixtureSince, KeepContacts)
	if err != nil || len(filtered.Batches) != 0 || filtered.Next != "22" || filtered.More {
		t.Fatalf("filtered page = %+v, %v", filtered, err)
	}
	all, err := r.Changes(ctx, "19", 3, fixtureSince, KeepAll)
	if err != nil || len(all.Batches) != 1 || len(all.Batches[0].Messages) != 3 || !strings.Contains(all.Batches[0].Messages[2].Text, "••••") {
		t.Fatalf("unfiltered code page = %+v, %v", all, err)
	}
	details, err := r.Changes(ctx, "2", 4, fixtureSince, KeepContacts)
	if err != nil || len(details.Batches) != 1 {
		t.Fatalf("details = %+v, %v", details, err)
	}
	ms := details.Batches[0].Messages
	if len(ms) != 4 || ms[0].Text != "Example code ••••" || ms[1].ReplyTo != "Example code ••••" ||
		len(ms[2].Attachments) != 1 || ms[2].Attachments[0].Name != "IMG_0001.HEIC" {
		t.Fatalf("masked message, reply or attachment = %+v", ms)
	}
}

func TestMultiChatSearch(t *testing.T) {
	r := multiChatFixture(t)
	ctx := context.Background()
	a := convID("person:+15550100")
	b := convID("person:alice@example.com")
	hits, err := r.Search(ctx, "shared needle", 3, fixtureSince, KeepContacts)
	if err != nil || len(hits) != 3 {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	if hits[0].Message.ID != "9" || hits[1].Message.ID != "9" || hits[0].Chat.ID != a || hits[1].Chat.ID != b ||
		hits[2].Message.ID != "7" || hits[2].Chat.ID != a {
		t.Fatalf("distinct conversation hits = %+v", hits)
	}
	code, err := r.Search(ctx, "code", 2, fixtureSince, KeepContacts)
	if err != nil || len(code) != 2 || code[0].Message.ID != "5" || code[1].Message.ID != "3" ||
		code[0].Message.Text != "Reply code ••••" || code[1].Message.Text != "Example code ••••" {
		t.Fatalf("deduplicated, masked results = %+v, %v", code, err)
	}
	// A small scan window proves that associations do not consume the distinct-message scan cap.
	modifyMultiChatFixture(t, r, `UPDATE message SET item_type = 1 WHERE ROWID NOT IN (7, 9);`)
	scanned, err := r.search(ctx, "shared needle", 10, fixtureSince, KeepContacts, 2)
	if err != nil || len(scanned) != 3 || scanned[0].Message.ID != "9" || scanned[1].Message.ID != "9" || scanned[2].Message.ID != "7" {
		t.Fatalf("two-message scan = %+v, %v", scanned, err)
	}
	one, err := r.search(ctx, "shared needle", 10, fixtureSince, KeepContacts, 1)
	if err != nil || len(one) != 2 || one[0].Message.ID != "9" || one[1].Message.ID != "9" {
		t.Fatalf("one-message scan = %+v, %v", one, err)
	}
}

func TestMultiChatAssociationLimitDoesNotAdvance(t *testing.T) {
	r := multiChatFixture(t)
	// Each extra association has a synthetic chat. The boundary exceeds the maximum by one or more.
	modifyMultiChatFixture(t, r, fmt.Sprintf(`
WITH RECURSIVE n(i) AS (VALUES(10000) UNION ALL SELECT i + 1 FROM n WHERE i < %d)
INSERT INTO chat(ROWID, guid, style, chat_identifier, service_name)
  SELECT i, printf('fixture-%%d', i), 45, '+15550100', 'SMS' FROM n;
INSERT INTO chat_message_join(chat_id, message_id, message_date)
  SELECT c.ROWID, m.ROWID, m.date FROM chat c CROSS JOIN message m
	  WHERE c.ROWID >= 10000 AND m.ROWID = 9;`, 10000+maxSearchAssociations))
	for _, cursor := range []string{"8", "8"} {
		page, err := r.Changes(context.Background(), cursor, 1, fixtureSince, KeepAll)
		if !errors.Is(err, ErrTooManyAssociations) || page.Next != "" || page.More || len(page.Batches) != 0 {
			t.Fatalf("cursor %s: page %+v, error %v", cursor, page, err)
		}
	}
	history, err := r.Messages(context.Background(), convID("person:+15550100"), 1, "", KeepAll)
	if !errors.Is(err, ErrTooManyAssociations) || history.Before != "" || history.More || len(history.Messages) != 0 {
		t.Fatalf("history over chat limit = %+v, %v", history, err)
	}
	if hits, err := r.Search(context.Background(), "shared needle", 1, fixtureSince, KeepAll); !errors.Is(err, ErrTooManyAssociations) || len(hits) != 0 {
		t.Fatalf("search over association limit = %+v, %v", hits, err)
	}
	// A malformed cursor is still rejected and cannot be confused with an expansion error.
	if _, err := r.Changes(context.Background(), strconv.Itoa(-1), 1, time.Time{}, KeepAll); err == nil || errors.Is(err, ErrTooManyAssociations) {
		t.Fatalf("bad cursor error = %v", err)
	}
}

func TestMultiChatChangesDoesNotAdvanceIfChatDisappears(t *testing.T) {
	r := multiChatFixture(t)
	t.Setenv("ISSUE4440_TEST_SQLITE", r.SQLite)
	t.Setenv("ISSUE4440_TEST_DB", r.chatDB())
	wrapper := filepath.Join(t.TempDir(), "sqlite-wrapper")
	script := `#!/bin/sh
sql=$(cat)
printf '%s' "$sql" | "$ISSUE4440_TEST_SQLITE" "$@"
status=$?
if [ "$status" -ne 0 ]; then exit "$status"; fi
case "$sql" in
  *'AS chat_present'*) "$ISSUE4440_TEST_SQLITE" "$ISSUE4440_TEST_DB" 'DELETE FROM chat WHERE ROWID = 3;' ;;
esac
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r.SQLite = wrapper
	page, err := r.Changes(context.Background(), "8", 1, fixtureSince, KeepAll)
	if err == nil || !strings.Contains(err.Error(), "chat changed") || page.Next != "" || page.More || len(page.Batches) != 0 {
		t.Fatalf("chat disappeared between reads: page %+v, error %v", page, err)
	}
	// A later snapshot sees an already-orphaned join row; keep the existing behavior of filtering it out.
	r.SQLite = os.Getenv("ISSUE4440_TEST_SQLITE")
	page, err = r.Changes(context.Background(), "8", 1, fixtureSince, KeepAll)
	if err != nil || page.Next != "9" || len(page.Batches) != 1 || !reflect.DeepEqual(messageIDs(page.Batches[0].Messages), []string{"9"}) {
		t.Fatalf("retry with an orphan join = %+v, %v", page, err)
	}
}
