package mail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The MIME layouts mail clients write when a file or photo sits in the middle of the text. Apple Mail (iPhone and
// Mac) splits the body around it into several inline parts; Gmail keeps one multipart/alternative body and puts the
// files after it.

const (
	bodyPlainPart = "Content-Transfer-Encoding: 7bit\r\nContent-Type: text/plain;\r\n\tcharset=us-ascii\r\n\r\n"
	bodyHTMLPart  = "Content-Transfer-Encoding: quoted-printable\r\nContent-Type: text/html;\r\n\tcharset=utf-8\r\n\r\n"
	bodyPhotoPart = "Content-Type: image/jpeg;\r\n\tname=IMG_0412.jpeg;\r\n\tx-apple-part-url=4D1C9E2B-1F3A-4B5C-8D6E-7F8091A2B3C4\r\n" +
		"Content-Disposition: inline;\r\n\tfilename=IMG_0412.jpeg\r\nContent-Transfer-Encoding: base64\r\n\r\n/9j/4AAQSkZJRgABAQ=="
	bodyMacPhotoPart = "Content-Transfer-Encoding: base64\r\nContent-Disposition: inline;\r\n\tfilename=IMG_0412.jpeg\r\n" +
		"Content-Type: image/jpeg;\r\n\tx-unix-mode=0644;\r\n\tname=\"IMG_0412.jpeg\"\r\n" +
		"Content-Id: <4D1C9E2B-1F3A-4B5C-8D6E-7F8091A2B3C4>\r\n\r\n/9j/4AAQSkZJRgABAQ=="
	bodyPDFPart = "Content-Disposition: inline;\r\n\tfilename=contract.pdf\r\nContent-Type: application/pdf;\r\n\tx-unix-mode=0644;\r\n" +
		"\tname=\"contract.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQ="

	// Apple Mail's rich text halves, quoted-printable as it sends them, and what they decode to.
	bodyMacHTMLBefore = "<html><head><meta http-equiv=3D\"content-type\" content=3D\"text/html; charset=3Dutf-8\"></head>" +
		"<body style=3D\"overflow-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;\"><div>H=\r\n" +
		"ere is the receipt:</div><div><img apple-inline=3D\"yes\" src=3D\"cid:4D1C9E2B-1F3A-4B5C-8D6E-7F8091A2B3C4\"></div>"
	bodyMacHTMLBeforeDecoded = "<html><head><meta http-equiv=\"content-type\" content=\"text/html; charset=utf-8\"></head>" +
		"<body style=\"overflow-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;\"><div>" +
		"Here is the receipt:</div><div><img apple-inline=\"yes\" src=\"cid:4D1C9E2B-1F3A-4B5C-8D6E-7F8091A2B3C4\"></div>"
	bodyMacHTMLAfter = "<div><br></div><div>Can you file it under travel by Monday?</div></body></html>" // nothing to escape
)

// mimeTree joins parts into a multipart body with the given boundary, closing delimiter included.
func mimeTree(boundary string, parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString("--" + boundary + "\r\n" + part + "\r\n")
	}
	return b.String() + "--" + boundary + "--\r\n"
}

// multipartPart is a nested multipart entity.
func multipartPart(contentType, boundary string, parts ...string) string {
	return "Content-Type: " + contentType + ";\r\n\tboundary=\"" + boundary + "\"\r\n\r\n" + mimeTree(boundary, parts...)
}

// bodyMessage is a whole message whose top-level entity is contentType with the given parts.
func bodyMessage(mailer, contentType, boundary string, parts ...string) string {
	return "From: Sender <sender@example.com>\r\nTo: me@example.com\r\nSubject: Receipt\r\n" +
		"Date: Thu, 12 Nov 2026 09:41:00 +0800\r\nMessage-Id: <receipt@example.com>\r\nMime-Version: 1.0 (1.0)\r\n" +
		"X-Mailer: " + mailer + "\r\n" + multipartPart(contentType, boundary, parts...)
}

type bodyPartsCase struct {
	name        string
	raw         string
	text, html  string
	attachments []string // file names, in order
}

func bodyPartsCases() []bodyPartsCase {
	return []bodyPartsCase{{
		name: "iPhone plain text with a photo between the paragraphs",
		raw: bodyMessage("iPhone Mail (22B83)", "multipart/mixed", "Apple-Mail-2B1E5F0A-6C7D-4E8F-9A0B-1C2D3E4F5A6B",
			bodyPlainPart+"Here is the receipt:",
			bodyPhotoPart,
			bodyPlainPart+"\r\nCan you file it under travel by Monday?\r\n\r\nSent from my iPhone"),
		text: "Here is the receipt:\r\nCan you file it under travel by Monday?\r\n\r\nSent from my iPhone",
	}, {
		name: "Apple Mail plain text with a file between the paragraphs",
		raw: bodyMessage("Apple Mail (3826.200.121)", "multipart/mixed", "Apple-Mail=_0A1B2C3D-4E5F-6A7B-8C9D-0E1F2A3B4C5D",
			bodyPlainPart+"Hi,\r\n\r\nThe contract is attached.\r\n",
			bodyPDFPart,
			bodyPlainPart+"\r\nPlease sign it by Friday and send it back, otherwise the offer lapses.\r\n",
			bodyPlainPart+"\r\n"),
		text:        "Hi,\r\n\r\nThe contract is attached.\r\n\r\nPlease sign it by Friday and send it back, otherwise the offer lapses.\r\n",
		attachments: []string{"contract.pdf"},
	}, {
		name: "Mac rich text with a photo between the paragraphs",
		raw: bodyMessage("Apple Mail (3826.200.121)", "multipart/alternative", "Apple-Mail=_ALT-5E6F",
			bodyPlainPart+"Here is the receipt:\r\n\r\nCan you file it under travel by Monday?\r\n",
			"Content-Type: multipart/related;\r\n\ttype=\"text/html\";\r\n\tboundary=\"Apple-Mail=_REL-7A8B\"\r\n\r\n"+
				mimeTree("Apple-Mail=_REL-7A8B", bodyHTMLPart+bodyMacHTMLBefore, bodyMacPhotoPart, bodyHTMLPart+bodyMacHTMLAfter)),
		text: "Here is the receipt:\r\n\r\nCan you file it under travel by Monday?\r\n",
		html: bodyMacHTMLBeforeDecoded + bodyMacHTMLAfter,
	}, {
		name: "Mac rich text with a file between the paragraphs",
		raw: bodyMessage("Apple Mail (3826.200.121)", "multipart/alternative", "Apple-Mail=_ALT-9C0D",
			bodyPlainPart+"Hi,\r\n\r\nThe contract is attached.\r\n\r\nPlease sign it by Friday.\r\n",
			multipartPart("multipart/mixed", "Apple-Mail=_MIX-1E2F",
				bodyHTMLPart+"<html><body><div>Hi,</div><div><br></div><div>The contract is attached.</div>",
				bodyPDFPart,
				bodyHTMLPart+"<div><br></div><div>Please sign it by Friday.</div></body></html>")),
		text: "Hi,\r\n\r\nThe contract is attached.\r\n\r\nPlease sign it by Friday.\r\n",
		html: "<html><body><div>Hi,</div><div><br></div><div>The contract is attached.</div>" +
			"<div><br></div><div>Please sign it by Friday.</div></body></html>",
		attachments: []string{"contract.pdf"},
	}, {
		name: "Gmail puts the files after one alternative body",
		raw: bodyMessage("Gmail", "multipart/mixed", "000000000000a1b2c3d4e5f60718",
			multipartPart("multipart/alternative", "000000000000a1b2c3d4e5f60716",
				"Content-Type: text/plain; charset=\"UTF-8\"\r\n\r\nHere is the receipt.\r\nCan you file it?",
				"Content-Type: text/html; charset=\"UTF-8\"\r\n\r\n<div dir=\"ltr\">Here is the receipt.<div>Can you file it?</div></div>"),
			"Content-Type: application/pdf; name=\"receipt.pdf\"\r\nContent-Disposition: attachment; filename=\"receipt.pdf\"\r\n"+
				"Content-Transfer-Encoding: base64\r\nContent-ID: <f_m1a2b3c40>\r\nX-Attachment-Id: f_m1a2b3c40\r\n\r\nJVBERi0xLjQ=",
			"Content-Type: image/png; name=\"scan.png\"\r\nContent-Disposition: attachment; filename=\"scan.png\"\r\n"+
				"Content-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo="),
		text:        "Here is the receipt.\r\nCan you file it?",
		html:        "<div dir=\"ltr\">Here is the receipt.<div>Can you file it?</div></div>",
		attachments: []string{"receipt.pdf", "scan.png"},
	}, {
		// Each alternative is the whole content, so the second rendering is never added to the first.
		name: "an alternative with text in both renderings is not doubled",
		raw: bodyMessage("Example Mailer", "multipart/alternative", "alt",
			bodyPlainPart+"First rendering.",
			multipartPart("multipart/mixed", "mix", bodyPlainPart+"Second rendering, part one.", bodyPDFPart,
				bodyPlainPart+"Second rendering, part two.")),
		text:        "First rendering.",
		attachments: []string{"contract.pdf"},
	}, {
		// Two alternative bodies around a file: each contributes its text and its HTML.
		name: "alternatives on both sides of a file",
		raw: bodyMessage("Example Mailer", "multipart/mixed", "mix",
			multipartPart("multipart/alternative", "alt1", bodyPlainPart+"Before the file.", bodyHTMLPart+"<p>Before the file.</p>"),
			bodyPDFPart,
			multipartPart("multipart/alternative", "alt2", bodyPlainPart+"After the file.", bodyHTMLPart+"<p>After the file.</p>")),
		text:        "Before the file.\nAfter the file.",
		html:        "<p>Before the file.</p><p>After the file.</p>",
		attachments: []string{"contract.pdf"},
	}, {
		// A named text part shown inline is a file, not more of the letter; text marked attachment never is.
		name: "named and attached text parts stay out of the body",
		raw: bodyMessage("Apple Mail (3826.200.121)", "multipart/mixed", "mix",
			bodyPlainPart+"Notes below.",
			"Content-Type: text/plain;\r\n\tname=notes.txt\r\nContent-Disposition: inline;\r\n\tfilename=notes.txt\r\n\r\nnotes",
			"Content-Type: text/csv\r\nContent-Disposition: attachment; filename=table.csv\r\n\r\na,b",
			"Content-Type: text/plain\r\nContent-Disposition: attachment; filename=log.txt\r\n\r\nlog"),
		text:        "Notes below.",
		attachments: []string{"table.csv", "log.txt"},
	}, {
		// With nothing else to show, a named text part is still the body, as before.
		name: "a message that is only a named text part",
		raw: "From: Sender <sender@example.com>\r\nTo: me@example.com\r\nSubject: Notes\r\nMIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=us-ascii; name=notes.txt\r\n\r\nthe notes",
		text: "the notes",
	}}
}

func TestParseMessageJoinsBodyPartsAroundFiles(t *testing.T) {
	for _, c := range bodyPartsCases() {
		t.Run(c.name, func(t *testing.T) {
			msg := &MailMessage{}
			parseMessage([]byte(c.raw), msg)
			if msg.Text != c.text {
				t.Errorf("text = %q, want %q", msg.Text, c.text)
			}
			if msg.HTML != c.html {
				t.Errorf("html = %q, want %q", msg.HTML, c.html)
			}
			var names []string
			for _, a := range msg.Attachments {
				names = append(names, a.Filename)
			}
			if strings.Join(names, ",") != strings.Join(c.attachments, ",") {
				t.Errorf("attachments = %+v, want %v", msg.Attachments, c.attachments)
			}
		})
	}
}

// mail_read, what the assistant reads, carries every line of the text, including what was written after a photo or file.
func TestMailReadShowsTextAfterAnInlineFile(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()
	for _, c := range bodyPartsCases() {
		t.Run(c.name, func(t *testing.T) {
			uid := srv.appendMessage(t, "INBOX", c.raw)
			args, _ := json.Marshal(map[string]any{"account_id": "acc_test", "uid": uid})
			res := s.Call(ctx, ToolRead, args, nil)
			if res.IsError {
				t.Fatalf("mail_read: %v", res.Content)
			}
			out := resultText(t, res)
			for _, line := range strings.Split(c.text, "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.Contains(out, line) {
					t.Errorf("mail_read output missing %q:\n%s", line, out)
				}
			}
		})
	}
}
