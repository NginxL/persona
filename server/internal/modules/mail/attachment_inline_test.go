package mail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

type inlineFile struct {
	filename string // "" leaves the name unchecked: recovering malformed names is attachmentMetadata's job
	mime     string
	data     string
}

type inlineCase struct {
	name        string
	parts       []string
	text        string
	attachments []inlineFile
}

// inlineRaw wraps parts in a multipart/mixed message with an Apple Mail style boundary.
func inlineRaw(parts ...string) string {
	raw := "From: Sender <sender@example.com>\r\nTo: recipient@example.com\r\nSubject: Files\r\n" +
		"Message-ID: <files@example.com>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed;\r\n\tboundary=\"Apple-Mail=_0A1B\"\r\n\r\n"
	for _, part := range parts {
		raw += "--Apple-Mail=_0A1B\r\n" + part + "\r\n"
	}
	return raw + "--Apple-Mail=_0A1B--\r\n"
}

func inlineCases() []inlineCase {
	body := func(text string) string {
		return "Content-Transfer-Encoding: 7bit\r\nContent-Type: text/plain;\r\n\tcharset=us-ascii\r\n\r\n" + text
	}
	return []inlineCase{{
		// What Apple Mail and iOS Mail send by default: the file is marked inline, with a trailing text part.
		name: "apple mail inline pdf",
		parts: []string{body("See attached."),
			"Content-Disposition: inline;\r\n\tfilename=report.pdf\r\nContent-Type: application/pdf;\r\n\tx-unix-mode=0644;\r\n" +
				"\tname=\"report.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQ=",
			body("\r\n")},
		text:        "See attached.",
		attachments: []inlineFile{{"report.pdf", "application/pdf", "%PDF-1.4"}},
	}, {
		// Inline images and inline text stay part of the body (#1571), and so does an inline file without a name.
		name: "inline images, text and unnamed files stay body",
		parts: []string{body("Photo below."),
			"Content-Type: image/png\r\nContent-Disposition: inline; filename=inline.png\r\nContent-Transfer-Encoding: base64\r\n\r\nYWJj",
			"Content-Type: image/jpeg; name=\"IMG_0001.jpeg\"\r\nContent-Disposition: inline\r\nContent-Transfer-Encoding: base64\r\n\r\nYWJj",
			"Content-Type: text/plain; name=notes.txt\r\nContent-Disposition: inline; filename=notes.txt\r\n\r\nnotes",
			"Content-Type: application/pdf\r\nContent-Disposition: inline\r\n\r\nunnamed"},
		text: "Photo below.",
	}, {
		// Parameters go-message cannot parse: an unquoted UTF-8 name and an RFC 2047 word right after the '='. Marked
		// attachment all the same, so neither text part may become the body.
		name: "attachment with malformed parameters",
		parts: []string{"Content-Type: text/plain; charset=utf-8\r\nContent-Disposition: attachment; filename=résumé.txt\r\n\r\nsummary",
			body("See the table."),
			"Content-Type: text/csv\r\nContent-Disposition: attachment; filename==?UTF-8?B?w6TDtsO8LmNzdg==?=\r\n\r\na,b"},
		text:        "See the table.",
		attachments: []inlineFile{{"", "text/plain", "summary"}, {"", "text/csv", "a,b"}},
	}, {
		// RFC 2231 names, including a charset Go's mime package drops, and an upper-case disposition.
		name: "inline files with rfc 2231 names",
		parts: []string{body("Two files."),
			"Content-Type: application/pdf\r\nContent-Disposition: inline; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf\r\n\r\npdf",
			"Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document\r\n" +
				"Content-Disposition: inline;\r\n\tfilename*0*=iso-8859-1''r%E9sum%E9;\r\n\tfilename*1*=.docx\r\n\r\ndocx",
			"Content-Type: application/zip\r\nContent-Disposition: INLINE; filename=\"archive.zip\"\r\n\r\nzip"},
		text: "Two files.",
		attachments: []inlineFile{{"résumé.pdf", "application/pdf", "pdf"},
			{"", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"},
			{"archive.zip", "application/zip", "zip"}},
	}}
}

func checkInlineFiles(t *testing.T, c inlineCase, msg *MailMessage) {
	t.Helper()
	if msg.Text != c.text {
		t.Fatalf("text = %q, want %q", msg.Text, c.text)
	}
	if len(msg.Attachments) != len(c.attachments) {
		t.Fatalf("attachments = %+v, want %+v", msg.Attachments, c.attachments)
	}
	for i, want := range c.attachments {
		a := msg.Attachments[i]
		if !strings.HasPrefix(a.ID, strconv.Itoa(i+1)+"-") || a.Mime != want.mime || a.Size != int64(len(want.data)) ||
			(want.filename != "" && a.Filename != want.filename) {
			t.Errorf("attachment %d = %+v, want %+v", i+1, a, want)
		}
	}
}

func TestParseMessageInlineNamedFiles(t *testing.T) {
	for _, c := range inlineCases() {
		t.Run(c.name, func(t *testing.T) {
			msg := &MailMessage{}
			parseMessage([]byte(inlineRaw(c.parts...)), msg)
			checkInlineFiles(t, c, msg)
		})
	}
}

// The list's paperclip follows the same rule over BODYSTRUCTURE, where servers may pass RFC 2231 keys undecoded.
func TestBodyStructureInlineNamedFiles(t *testing.T) {
	part := func(mediaType string, params map[string]string, disposition string, dispParams map[string]string) imap.BodyStructure {
		typ, subtype, _ := strings.Cut(mediaType, "/")
		single := &imap.BodyStructureSinglePart{Type: typ, Subtype: subtype, Params: params}
		if disposition != "" {
			single.Extended = &imap.BodyStructureSinglePartExt{
				Disposition: &imap.BodyStructureDisposition{Value: disposition, Params: dispParams}}
		}
		return &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{
			&imap.BodyStructureSinglePart{Type: "TEXT", Subtype: "PLAIN", Params: map[string]string{"charset": "us-ascii"}}, single}}
	}
	for _, c := range []struct {
		name string
		bs   imap.BodyStructure
		want bool
	}{
		{"inline pdf with filename", part("APPLICATION/PDF", map[string]string{"x-unix-mode": "0644", "name": "report.pdf"}, "INLINE", map[string]string{"filename": "report.pdf"}), true},
		{"inline pdf with content type name", part("application/pdf", map[string]string{"name": "report.pdf"}, "inline", nil), true},
		{"inline pdf with rfc 2231 filename", part("application/pdf", nil, "inline", map[string]string{"filename*": "UTF-8''r%C3%A9sum%C3%A9.pdf"}), true},
		{"inline docx with rfc 2231 continuations", part("application/msword", nil, "inline", map[string]string{"filename*0*": "iso-8859-1''r%E9sum%E9", "filename*1*": ".doc"}), true},
		{"unmarked pdf with rfc 2231 name", part("application/pdf", map[string]string{"name*": "UTF-8''x.pdf"}, "", nil), true},
		{"attachment with unparsed parameters", part("text/csv", nil, "attachment; filename==?UTF-8?B?w6TDtsO8LmNzdg==?=", nil), true},
		{"inline pdf without a name", part("application/pdf", nil, "inline", nil), false},
		{"inline pdf with an empty name", part("application/pdf", map[string]string{"name": ""}, "inline", map[string]string{"filename": ""}), false},
		{"inline image", part("image/png", nil, "inline", map[string]string{"filename": "inline.png"}), false},
		{"inline text", part("text/plain", nil, "inline", map[string]string{"filename": "notes.txt"}), false},
		{"unmarked named text", part("text/plain", map[string]string{"name": "notes.txt"}, "", nil), false},
	} {
		if got := bodyStructureHasAttachments(c.bs); got != c.want {
			t.Errorf("%s: bodyStructureHasAttachments = %t, want %t", c.name, got, c.want)
		}
	}
}

// The list, mail_read, the HTTP download and FetchAttachment agree on these parts through a real IMAP server.
func TestInlineNamedFilesListReadAndDownload(t *testing.T) {
	srv := newTestIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	service := newServiceWithAccount(t, srv)
	ctx := context.Background()
	for _, c := range inlineCases() {
		t.Run(c.name, func(t *testing.T) {
			uid := uint32(srv.appendMessage(t, "INBOX", inlineRaw(c.parts...)))
			list, err := service.ListMessages(ctx, ListOptions{AccountID: "acc_test", Limit: 1})
			if err != nil || len(list.Messages) != 1 || list.Messages[0].UID != uid {
				t.Fatalf("list = %+v, %v", list, err)
			}
			if got := list.Messages[0].HasAttach; got != (len(c.attachments) > 0) {
				t.Fatalf("list paperclip = %t, want %t", got, len(c.attachments) > 0)
			}
			msg, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
			if err != nil {
				t.Fatal(err)
			}
			checkInlineFiles(t, c, msg)
			if msg.HasAttach != (len(c.attachments) > 0) {
				t.Fatalf("read paperclip = %t", msg.HasAttach)
			}
			path := "/api/v1/mail/messages/acc_test/" + itoa(uid) + "/attachments/"
			for i, a := range msg.Attachments {
				response := httptest.NewRecorder()
				service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+a.ID, nil))
				if response.Code != http.StatusOK || response.Body.String() != c.attachments[i].data ||
					response.Header().Get("Content-Type") != a.Mime || response.Header().Get("ETag") != `"`+a.ID+`"` {
					t.Fatalf("download %s: %d %q %v", a.ID, response.Code, response.Body.String(), response.Header())
				}
				metadata, data, err := service.FetchAttachment(ctx, "acc_test", uid, "INBOX", a.ID, MaxSavedAttachment)
				if err != nil || string(data) != c.attachments[i].data || metadata != a {
					t.Fatalf("save handoff %s: %+v %q %v", a.ID, metadata, data, err)
				}
			}
		})
	}
}
