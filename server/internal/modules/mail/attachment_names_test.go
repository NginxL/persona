package mail

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Attachment names Go's mime.ParseMediaType drops: RFC 2231 values in charsets other than us-ascii and utf-8, and
// headers with an unquoted value that is not a token. The name must still come out, and the type must not carry the
// raw parameters.

const cjkName = "报告.pdf" // i18n-ignore: a Chinese file name as Chinese mailers send it

func gb18030Name(t *testing.T) string {
	t.Helper()
	b, err := simplifiedchinese.GB18030.NewEncoder().String(cjkName)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// namedAttachmentMessage is a mail with a text body and one base64 PDF part ("abc") carrying headers.
func namedAttachmentMessage(headers ...string) string {
	raw := "From: sender@example.com\r\nTo: me@example.com\r\nSubject: names\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=parts\r\n\r\n--parts\r\nContent-Type: text/plain\r\n\r\nbody\r\n"
	for _, h := range headers {
		raw += "--parts\r\n" + h + "Content-Transfer-Encoding: base64\r\n\r\nYWJj\r\n"
	}
	return raw + "--parts--\r\n"
}

func TestParseMessageAttachmentNames(t *testing.T) {
	gb := gb18030Name(t)
	gbPct := url.PathEscape(gb)
	gbHead, gbTail := url.PathEscape(gb[:2]), url.PathEscape(gb[2:])
	utf8Pct := url.PathEscape(cjkName)
	utf8Word := mime.BEncoding.Encode("UTF-8", cjkName)
	gbWord := mime.BEncoding.Encode("GB2312", gb)
	const pdf = "Content-Type: application/pdf\r\n"
	for _, tc := range []struct {
		name, headers, want, mime string
	}{
		// Failing before: Go's mime package drops these.
		{"rfc2231-gb18030", pdf + "Content-Disposition: attachment; filename*=gb18030''" + gbPct + "\r\n", cjkName, ""},
		{"rfc2231-gbk", pdf + "Content-Disposition: attachment; filename*=GBK'zh'" + gbPct + "\r\n", cjkName, ""},
		{"rfc2231-latin1", pdf + "Content-Disposition: attachment; filename*=iso-8859-1''caf%E9.pdf\r\n", "caf\u00e9.pdf", ""},
		{"rfc2231-gb18030-continued", pdf + "Content-Disposition: attachment;\r\n filename*0*=gb18030''" + gbHead + ";\r\n filename*1*=" + gbTail + "\r\n", cjkName, ""},
		// Go keeps the plain second section only and reports no error.
		{"rfc2231-latin1-continued", pdf + "Content-Disposition: attachment; filename*0*=iso-8859-1''caf%E9; filename*1=.pdf\r\n", "caf\u00e9.pdf", ""},
		{"rfc2231-type-name", "Content-Type: application/pdf; name*=gb18030''" + gbPct + "\r\nContent-Disposition: attachment\r\n", cjkName, ""},
		{"rfc2047-unquoted", "Content-Type: application/pdf; name=" + utf8Word + "\r\nContent-Disposition: attachment; filename=" + utf8Word + "\r\n", cjkName, ""},
		{"rfc2047-unquoted-type-only", "Content-Type: Application/PDF; name=" + utf8Word + "\r\nContent-Disposition: attachment\r\n", cjkName, ""},
		{"rfc2047-gb2312-unquoted", pdf + "Content-Disposition: attachment; filename=" + gbWord + "\r\n", cjkName, ""},
		{"utf8-unquoted", pdf + "Content-Disposition: attachment; filename=" + cjkName + "\r\n", cjkName, ""},
		{"space-unquoted", pdf + "Content-Disposition: attachment; filename=Q3 report.pdf\r\n", "Q3 report.pdf", ""},
		{"unknown-charset-kept", pdf + "Content-Disposition: attachment; filename*=x-unknown''%B1%A8.pdf\r\n", "%B1%A8.pdf", ""},
		{"plain-fallback-for-unknown-charset", pdf + "Content-Disposition: attachment; filename=report.pdf; filename*=x-unknown''%B1.pdf\r\n", "report.pdf", ""},
		{"unknown-charset-type-name", "Content-Type: application/pdf; name=report.pdf\r\nContent-Disposition: attachment; filename*=x-unknown''%B1.pdf\r\n", "report.pdf", ""},
		{"bad-type", "Content-Type: application/pdf name=x.pdf\r\nContent-Disposition: attachment; filename=x.pdf\r\n", "x.pdf", "application/octet-stream"},
		// Controls that already passed.
		{"rfc2231-utf8", pdf + "Content-Disposition: attachment; filename*=UTF-8''" + utf8Pct + "\r\n", cjkName, ""},
		{"rfc2231-utf8-continued", pdf + "Content-Disposition: attachment; filename*0*=UTF-8''" + url.PathEscape("\u62a5") + "; filename*1*=" + url.PathEscape("\u544a.pdf") + "\r\n", cjkName, ""},
		{"rfc2047-quoted", pdf + "Content-Disposition: attachment; filename=\"" + gbWord + "\"\r\n", cjkName, ""},
		{"utf8-quoted", pdf + "Content-Disposition: attachment; filename=\"" + cjkName + "\"\r\n", cjkName, ""},
		{"quoted-escapes", pdf + "Content-Disposition: attachment; filename=\"a \\\"b\\\"; c.pdf\"\r\n", `a "b"; c.pdf`, ""},
		{"type-name", "Content-Type: application/pdf; name=report.pdf\r\n", "report.pdf", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &MailMessage{}
			parseMessage([]byte(namedAttachmentMessage(tc.headers)), msg)
			wantMime := tc.mime
			if wantMime == "" {
				wantMime = "application/pdf"
			}
			if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != tc.want || msg.Attachments[0].Mime != wantMime || msg.Attachments[0].Size != 3 {
				t.Fatalf("attachments = %+v, want %q (%s)", msg.Attachments, tc.want, wantMime)
			}
		})
	}
}

// mail_read and the download see the decoded name, and the download's Content-Type is the bare type.
func TestAttachmentNamesReadAndDownload(t *testing.T) {
	srv := newTestIMAP(t)
	service := newServiceWithAccount(t, srv)
	ctx := context.Background()
	word := mime.BEncoding.Encode("UTF-8", cjkName)
	raw := namedAttachmentMessage(
		"Content-Type: application/pdf; name="+word+"\r\nContent-Disposition: attachment\r\n",
		"Content-Type: application/pdf\r\nContent-Disposition: attachment; filename*=gb18030''"+url.PathEscape(gb18030Name(t))+"\r\n",
	)
	uid := uint32(srv.appendMessage(t, "INBOX", raw))
	msg, err := service.GetMessage(ctx, "acc_test", uid, "INBOX")
	if err != nil || len(msg.Attachments) != 2 {
		t.Fatalf("attachments: %+v %v", msg, err)
	}
	args, _ := json.Marshal(map[string]any{"account_id": "acc_test", "uid": uid})
	read := service.Call(ctx, ToolRead, args, nil)
	if read.IsError || len(read.Content) != 1 {
		t.Fatalf("mail_read: %+v", read)
	}
	for _, a := range msg.Attachments {
		if line := "- " + cjkName + " (application/pdf, 3 bytes) id=" + a.ID + "\n"; !strings.Contains(read.Content[0].Text, line) {
			t.Fatalf("mail_read lacks %q:\n%s", line, read.Content[0].Text)
		}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/acc_test/"+itoa(uid)+"/attachments/"+a.ID, nil))
		_, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
		if response.Code != http.StatusOK || response.Body.String() != "abc" || err != nil || params["filename"] != cjkName ||
			response.Header().Get("Content-Type") != "application/pdf" {
			t.Fatalf("download %s: %d %q %v %v", a.ID, response.Code, response.Body.String(), response.Header(), err)
		}
	}
}
