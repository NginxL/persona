package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/managerconf"
)

type mailReceiptSource struct{}

func (mailReceiptSource) Name() string                                        { return "mail" }
func (mailReceiptSource) Chats(context.Context, int) ([]kb.SourceChat, error) { return nil, nil }
func (mailReceiptSource) Messages(context.Context, kb.SourceChat, string, int) ([]kb.SourceMessage, string, bool, error) {
	return nil, "", false, nil
}
func (mailReceiptSource) Download(context.Context, string, string) error {
	return errors.New("no attachments")
}

func receiptMail(account string, uid uint32, at time.Time, text string) mail.MailSummary {
	return mail.MailSummary{AccountID: account, Mailbox: "INBOX", UID: uid,
		From: mail.Address{Email: "sender@example.com"}, Subject: text, Snippet: text,
		Date: at.Format(time.RFC3339)}
}

func receiptChanges(t *testing.T, c mailConnector, cursor string, want int) ([]connector.ChatBatch, string) {
	t.Helper()
	batches, next, more, err := c.Changes(context.Background(), cursor, 20)
	if err != nil || more || len(batches) != want {
		t.Fatalf("Changes: got %d batches, cursor %q, more %v, err %v; want %d", len(batches), next, more, err, want)
	}
	return batches, next
}

func ingestMail(t *testing.T, e *kb.Engine, b connector.ChatBatch, want int) {
	t.Helper()
	if n, err := e.Ingest(context.Background(), mailReceiptSource{}, b.Chat, b.Messages); err != nil || n != want {
		t.Fatalf("Ingest ID %q: got %d, %v; want %d", b.Messages[0].ID, n, err, want)
	}
}

func openMailStore(t *testing.T, dir string) *kb.Store {
	t.Helper()
	st, err := kb.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Exercise actual Changes, receipt persistence and the atomic cursor writer, including a failed publication.
func TestMailArchiveReceiptKnownGenerationAndRetry(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	m := &pagedMail{validity: 1, msgs: []mail.MailSummary{receiptMail("acc", 1, at, "first generation")}}
	c := mailConnector{h: &Manager{mail: m}}
	dir := t.TempDir()
	e := &kb.Engine{Store: openMailStore(t, dir)}

	first, cursor := receiptChanges(t, c, "", 1)
	if first[0].Messages[0].ID != "1" {
		t.Fatalf("first generation must retain decimal IDs: %+v", first)
	}
	ingestMail(t, e, first[0], 1)
	m.msgs = append(m.msgs, receiptMail("acc", 2, at.Add(time.Minute), "same generation"))
	same, cursor := receiptChanges(t, c, cursor, 1)
	if same[0].Messages[0].ID != "2" {
		t.Fatalf("unchanged validity must retain decimal IDs: %+v", same)
	}
	ingestMail(t, e, same[0], 1)

	// The previous checkpoint exists, but its name leaves no space for the atomic writer's temporary suffix.
	checkpoint := filepath.Join(dir, strings.Repeat("x", 248)+".json")
	if err := os.WriteFile(checkpoint, []byte(cursor), 0o600); err != nil {
		t.Fatal(err)
	}
	m.validity = 2
	m.msgs = []mail.MailSummary{receiptMail("acc", 1, at.Add(2*time.Minute), "second generation")}
	second, next := receiptChanges(t, c, cursor, 1)
	if first[0].Chat.ID != second[0].Chat.ID || first[0].Messages[0].ID == second[0].Messages[0].ID {
		t.Fatalf("reused UID must be distinct within the same chat: first %+v, second %+v", first, second)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := e.Ingest(cancelled, mailReceiptSource{}, second[0].Chat, second[0].Messages); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ingest: %d, %v", n, err)
	}
	retry, retryNext := receiptChanges(t, c, cursor, 1)
	if !reflect.DeepEqual(second, retry) || next != retryNext {
		t.Fatalf("retry before ingest changed identity or cursor: %+v / %+v", second, retry)
	}
	ingestMail(t, e, retry[0], 1)
	if err := managerconf.WriteFileAtomic(checkpoint, []byte(next), 0o600); err == nil {
		t.Fatal("expected checkpoint replacement to fail")
	}
	old, err := os.ReadFile(checkpoint)
	if err != nil || string(old) != cursor {
		t.Fatalf("failed replacement changed checkpoint: %q, %v", old, err)
	}
	// Reopen the store and regenerate from the last durable cursor, as after a crash.
	e = &kb.Engine{Store: openMailStore(t, dir)}
	replay, replayNext := receiptChanges(t, c, string(old), 1)
	if !reflect.DeepEqual(second, replay) || next != replayNext {
		t.Fatalf("restart changed identity or cursor: %+v / %+v", second, replay)
	}
	ingestMail(t, e, replay[0], 0)
	checkpoint = filepath.Join(dir, "mail.json")
	if err := managerconf.WriteFileAtomic(checkpoint, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	receiptChanges(t, c, string(persisted), 0)

	ids := map[string]bool{first[0].Messages[0].ID: true, second[0].Messages[0].ID: true}
	cursor = string(persisted)
	for i, validity := range []uint32{3, 2, 1} {
		m.validity = validity
		m.msgs = []mail.MailSummary{receiptMail("acc", 1, at.Add(time.Duration(i+3)*time.Minute), "later generation")}
		page, next := receiptChanges(t, c, cursor, 1)
		id := page[0].Messages[0].ID
		if ids[id] {
			t.Fatalf("generation %d reused receipt ID %q", validity, id)
		}
		ids[id] = true
		ingestMail(t, e, page[0], 1)
		ingestMail(t, e, page[0], 0)
		cursor = next
	}
	text := e.Store.Since(kb.ChatID("mail", first[0].Chat.ID), time.Time{}, 1<<20)
	for _, s := range []string{"first generation", "same generation", "second generation"} {
		if strings.Count(text, s) != 2 { // subject and snippet each contain the text
			t.Fatalf("archive missing or duplicated %q: %s", s, text)
		}
	}
	if strings.Count(text, "later generation") != 6 {
		t.Fatalf("archive missing or duplicated a later generation: %s", text)
	}
}

// A missing or zero validity cannot prove that UIDs were renumbered; it must not reset or invent an epoch.
func TestMailArchiveReceiptUnknownValidity(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		name := "zero validity"
		if legacy {
			name = "legacy cursor"
		}
		t.Run(name, func(t *testing.T) {
			at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			m := &pagedMail{validity: 1, msgs: []mail.MailSummary{receiptMail("acc", 1, at, "old")}}
			c := mailConnector{h: &Manager{mail: m}}
			e := &kb.Engine{Store: openMailStore(t, t.TempDir())}
			first, cursor := receiptChanges(t, c, "", 1)
			ingestMail(t, e, first[0], 1)
			if legacy {
				cur := cursorOf(t, cursor)
				delete(cur, "acc#v")
				raw, err := json.Marshal(cur)
				if err != nil {
					t.Fatal(err)
				}
				cursor = string(raw)
				m.validity = 2
			} else {
				m.validity = 0
			}
			m.msgs = []mail.MailSummary{receiptMail("acc", 1, at.Add(time.Minute), "unproven reuse")}
			_, learned := receiptChanges(t, c, cursor, 0)
			cur := cursorOf(t, learned)
			if cur["acc"] != 1 || cur["acc#e"] != 0 || (!legacy && cur["acc#v"] != 1) || (legacy && cur["acc#v"] != 2) {
				t.Fatalf("unknown generation changed cursor unexpectedly: %s", learned)
			}
			// Once advanced, a lost reused UID cannot be reconstructed by later polling.
			receiptChanges(t, c, learned, 0)
			m.msgs = append(m.msgs, receiptMail("acc", 2, at.Add(2*time.Minute), "new UID"))
			newer, next := receiptChanges(t, c, learned, 1)
			if newer[0].Messages[0].ID != "2" || cursorOf(t, next)["acc#e"] != 0 {
				t.Fatalf("unproven generation changed legacy identity: %+v, %s", newer, next)
			}
			ingestMail(t, e, newer[0], 1)
		})
	}
}

type multiReceiptMail struct {
	inboxOnlyMail
	accounts []mail.MailAccount
	inboxes  map[string]*pagedMail
	fail     map[string]bool
}

func (m *multiReceiptMail) ListAccounts() ([]mail.MailAccount, error) { return m.accounts, nil }
func (m *multiReceiptMail) ListMessages(ctx context.Context, opt mail.ListOptions) (*mail.ListResult, error) {
	if m.fail[opt.AccountID] {
		return nil, errors.New("unreadable inbox")
	}
	if opt.Mailbox != "" && opt.Mailbox != "INBOX" {
		return nil, errors.New("unexpected mailbox")
	}
	return m.inboxes[opt.AccountID].ListMessages(ctx, opt)
}
func (m *multiReceiptMail) GetMessage(context.Context, string, uint32, string) (*mail.MailMessage, error) {
	return nil, errors.New("use snippet")
}

func TestMailArchiveReceiptIndependentInboxesAndErrors(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	m := &multiReceiptMail{
		accounts: []mail.MailAccount{{ID: "a", Email: "a@example.com"}, {ID: "b", Email: "b@example.com"}},
		inboxes: map[string]*pagedMail{
			"a": {validity: 1, msgs: []mail.MailSummary{receiptMail("a", 1, at, "a old")}},
			"b": {validity: 1, msgs: []mail.MailSummary{receiptMail("b", 1, at, "b old")}},
		},
		fail: map[string]bool{},
	}
	c := mailConnector{h: &Manager{mail: m}}
	e := &kb.Engine{Store: openMailStore(t, t.TempDir())}
	first, cursor := receiptChanges(t, c, "", 2)
	for _, b := range first {
		ingestMail(t, e, b, 1)
	}
	if first[0].Chat.ID == first[1].Chat.ID {
		t.Fatal("inboxes must use independent chats")
	}
	m.inboxes["a"].validity = 2
	m.inboxes["a"].msgs = []mail.MailSummary{receiptMail("a", 1, at.Add(time.Minute), "a new")}
	m.inboxes["b"].msgs = append(m.inboxes["b"].msgs, receiptMail("b", 2, at.Add(time.Minute), "b new"))
	m.fail["a"] = true
	page, next := receiptChanges(t, c, cursor, 1)
	if page[0].Chat.ID != first[1].Chat.ID || page[0].Messages[0].ID != "2" {
		t.Fatalf("failure in one inbox affected the other: %+v", page)
	}
	ingestMail(t, e, page[0], 1)
	before, after := cursorOf(t, cursor), cursorOf(t, next)
	for _, suffix := range []string{"", "#v", "#t", "#f", "#h", "#e"} {
		if before["a"+suffix] != after["a"+suffix] {
			t.Fatalf("failed inbox cursor changed: %s -> %s", cursor, next)
		}
	}
	m.fail["a"] = false
	page, cursor = receiptChanges(t, c, next, 1)
	if page[0].Chat.ID != first[0].Chat.ID || page[0].Messages[0].ID == first[0].Messages[0].ID {
		t.Fatalf("retry lost first inbox generation: %+v", page)
	}
	ingestMail(t, e, page[0], 1)
	m.inboxes["b"].validity = 2
	m.inboxes["b"].msgs = []mail.MailSummary{receiptMail("b", 1, at.Add(2*time.Minute), "b generation")}
	page, _ = receiptChanges(t, c, cursor, 1)
	if page[0].Chat.ID != first[1].Chat.ID || page[0].Messages[0].ID == first[1].Messages[0].ID {
		t.Fatalf("second inbox lost its own generation: %+v", page)
	}
	ingestMail(t, e, page[0], 1)
}

// A second change before publishing the first cursor still needs a different receipt namespace.
func TestMailArchiveReceiptChangesBeforeCheckpoint(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	m := &pagedMail{validity: 1, msgs: []mail.MailSummary{receiptMail("acc", 1, at, "original")}}
	c := mailConnector{h: &Manager{mail: m}}
	e := &kb.Engine{Store: openMailStore(t, t.TempDir())}
	first, checkpoint := receiptChanges(t, c, "", 1)
	ingestMail(t, e, first[0], 1)
	m.validity = 2
	m.msgs = []mail.MailSummary{receiptMail("acc", 1, at.Add(time.Minute), "unpublished generation")}
	second, _ := receiptChanges(t, c, checkpoint, 1)
	ingestMail(t, e, second[0], 1)
	// Simulate another renumbering before the new checkpoint was published.
	m.validity = 3
	m.msgs = []mail.MailSummary{receiptMail("acc", 1, at.Add(2*time.Minute), "next generation")}
	third, next := receiptChanges(t, c, checkpoint, 1)
	if third[0].Messages[0].ID == second[0].Messages[0].ID || third[0].Messages[0].ID == first[0].Messages[0].ID {
		t.Fatalf("unpublished changes reused receipt: %q, %q, %q", first[0].Messages[0].ID, second[0].Messages[0].ID, third[0].Messages[0].ID)
	}
	ingestMail(t, e, third[0], 1)
	receiptChanges(t, c, next, 0)
}

func TestMailArchiveReceiptRetryAfterIngestWriteFailure(t *testing.T) {
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	m := &pagedMail{validity: 1, msgs: []mail.MailSummary{receiptMail("acc", 1, at, "old day")}}
	c := mailConnector{h: &Manager{mail: m}}
	e := &kb.Engine{Store: openMailStore(t, t.TempDir())}
	first, cursor := receiptChanges(t, c, "", 1)
	ingestMail(t, e, first[0], 1)
	m.validity = 2
	m.msgs = []mail.MailSummary{receiptMail("acc", 1, at.Add(24*time.Hour), "new day")}
	page, next := receiptChanges(t, c, cursor, 1)
	day := at.Add(24 * time.Hour).In(e.Store.Zone()).Format("2006-01-02")
	blocked := e.Store.Path(kb.KindChat, kb.ChatID("mail", first[0].Chat.ID), "messages", day+".md")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Ingest(context.Background(), mailReceiptSource{}, page[0].Chat, page[0].Messages); err == nil {
		t.Fatal("expected archive day write to fail")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	retry, retryNext := receiptChanges(t, c, cursor, 1)
	if !reflect.DeepEqual(page, retry) || next != retryNext {
		t.Fatalf("failed ingest changed the retry: %+v / %+v", page, retry)
	}
	ingestMail(t, e, retry[0], 1)
	ingestMail(t, e, retry[0], 0)
}

func TestMailArchiveReceiptTransientZeroAfterChange(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	m := &pagedMail{validity: 1, msgs: []mail.MailSummary{receiptMail("acc", 1, at, "original")}}
	c := mailConnector{h: &Manager{mail: m}}
	e := &kb.Engine{Store: openMailStore(t, t.TempDir())}
	first, cursor := receiptChanges(t, c, "", 1)
	ingestMail(t, e, first[0], 1)
	m.validity = 2
	m.msgs = []mail.MailSummary{receiptMail("acc", 1, at.Add(time.Minute), "known generation")}
	second, cursor := receiptChanges(t, c, cursor, 1)
	ingestMail(t, e, second[0], 1)
	m.msgs = append(m.msgs, receiptMail("acc", 2, at.Add(2*time.Minute), "transient unknown"))
	m.validity = 0
	unknown, next := receiptChanges(t, c, cursor, 1)
	if cursorOf(t, next)["acc#v"] != 2 {
		t.Fatalf("zero erased last known validity: %s", next)
	}
	m.validity = 2
	known, knownNext := receiptChanges(t, c, cursor, 1)
	if !reflect.DeepEqual(unknown, known) || next != knownNext {
		t.Fatalf("transient unknown changed a retry identity: %+v / %+v", unknown, known)
	}
	ingestMail(t, e, unknown[0], 1)
	ingestMail(t, e, known[0], 0)
}

func TestMailArchiveReceiptEpochExhaustionFailsClosed(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	m := &pagedMail{validity: 2, msgs: []mail.MailSummary{receiptMail("acc", 1, at, "new generation")}}
	c := mailConnector{h: &Manager{mail: m}}
	cursor := `{"acc":1,"acc#v":1,"acc#e":4294967295}`
	batches, next, more, err := c.Changes(context.Background(), cursor, 20)
	if err == nil || len(batches) != 0 || next != cursor || more {
		t.Fatalf("epoch exhaustion must preserve the checkpoint: batches %+v, next %s, more %v, err %v", batches, next, more, err)
	}
}

func TestMailArchiveReceiptEpochWithoutValidityFailsClosed(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	m := &pagedMail{validity: 2, msgs: []mail.MailSummary{receiptMail("acc", 1, at, "unknown generation")}}
	c := mailConnector{h: &Manager{mail: m}}
	cursor := `{"acc":1,"acc#e":1}`
	batches, next, more, err := c.Changes(context.Background(), cursor, 20)
	if err == nil || len(batches) != 0 || next != cursor || more {
		t.Fatalf("epoch without validity must preserve the checkpoint: batches %+v, next %s, more %v, err %v", batches, next, more, err)
	}
}

// A checkpoint written by the new version must not let an old numeric receipt hide a later UID.
// Running this against the original connector also demonstrates why rolling back the writer is unsafe.
func TestMailArchiveReceiptVersionedCheckpoint(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	m := &pagedMail{validity: 1, msgs: []mail.MailSummary{receiptMail("acc", 2, at, "legacy receipt")}}
	c := mailConnector{h: &Manager{mail: m}}
	e := &kb.Engine{Store: openMailStore(t, t.TempDir())}
	legacy, _ := receiptChanges(t, c, "", 1)
	if legacy[0].Messages[0].ID != "2" {
		t.Fatalf("legacy ID: %+v", legacy)
	}
	ingestMail(t, e, legacy[0], 1)
	// A new-version writer has seen 1 -> 2 and published UID 1 under epoch 1.
	m.validity = 2
	m.msgs = []mail.MailSummary{receiptMail("acc", 2, at.Add(time.Minute), "later epoch")}
	cursor := `{"acc":1,"acc#v":2,"acc#e":1}`
	page, next := receiptChanges(t, c, cursor, 1)
	if cursorOf(t, next)["acc#e"] != 1 {
		t.Fatalf("epoch lost: %s", next)
	}
	ingestMail(t, e, page[0], 1)
	ingestMail(t, e, page[0], 0)
}
