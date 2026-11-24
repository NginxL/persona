package mail

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestSaveAttachmentBoundsSpoolAndKeepsIMAPSynchronized(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.ignoreListReturn(t)
	service := newServiceWithAccount(t, srv)
	body := strings.Repeat("A", 1<<20)
	raw := "From: sender@example.com\r\nTo: me@example.com\r\nSubject: file\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=parts\r\n\r\n" +
		"--parts\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=test.bin\r\n\r\n" + body + "\r\n--parts--\r\n"
	uid := uint32(srv.appendMessage(t, "INBOX", raw))
	ctx := context.Background()
	msg, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
	if err != nil || len(msg.Attachments) != 1 {
		t.Fatalf("message: %+v, %v", msg, err)
	}
	a := msg.Attachments[0]
	for _, limit := range []int64{0, 64, int64(len(body))} {
		file, got, err := service.attachment(ctx, "acc_test", uid, "INBOX", a.ID, limit)
		if err != nil {
			t.Fatal(err)
		}
		stat, statErr := file.Stat()
		contents, readErr := io.ReadAll(file)
		file.Close()
		os.Remove(file.Name())
		if statErr != nil || readErr != nil || stat.Size() != limit || got != a || string(contents) != body[:int(limit)] {
			t.Fatalf("limit %d: stat %v, read %v, metadata %+v", limit, statErr, readErr, got)
		}
	}
	if _, data, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", a.ID, 64); !errors.Is(err, ErrAttachmentTooLarge) || data != nil {
		t.Fatalf("oversized save: %d bytes, %v", len(data), err)
	}
	if _, _, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", "1-"+strings.Repeat("0", 64), 64); err == nil || errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("wrong identity must still be checked: %v", err)
	}
	got, data, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", a.ID, int64(len(body)))
	if err != nil || got != a || string(data) != body {
		t.Fatalf("exact limit: %+v, %d bytes, %v", got, len(data), err)
	}
	again, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
	if err != nil || again.Seen || len(again.Attachments) != 1 || again.Attachments[0] != a {
		t.Fatalf("after rejected save: %+v, %v", again, err)
	}
}

type failingSpool struct{ err error }

func (w failingSpool) Write([]byte) (int, error) { return 0, w.err }

func TestCappedSpoolReportsWriteFailures(t *testing.T) {
	want := errors.New("disk full")
	w := &cappedSpool{file: failingSpool{want}, left: 4}
	if _, err := w.Write([]byte("hello")); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
