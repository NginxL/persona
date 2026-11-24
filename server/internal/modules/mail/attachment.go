package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

// attachmentMetadata binds IDs to MIME order, metadata and decoded content.
func attachmentMetadata(h *gomail.AttachmentHeader, body io.Reader, index int, dst io.Writer) (Attachment, error) {
	name, ct := partName(h), partType(h)
	sum := sha256.New()
	fmt.Fprintf(sum, "%d\x00%s\x00%s\x00", index, name, ct)
	n, err := io.Copy(io.MultiWriter(sum, dst), body)
	return Attachment{ID: strconv.Itoa(index) + "-" + hex.EncodeToString(sum.Sum(nil)), Filename: name, Mime: ct, Size: n}, err
}

// nextPart is r.NextPart past the parts go-message can only partly decode. A part in an unknown charset comes back
// with its raw body and is used as is; a part in an unknown transfer encoding comes back without a header and is
// skipped. The message list and the attachment download both read parts through it, so they count attachment
// ordinals the same way. io.EOF and every other error are returned. Parts that are files come back as attachments
// (fileAsAttachment).
func nextPart(r *gomail.Reader) (*gomail.Part, error) {
	for {
		part, err := r.NextPart()
		switch {
		case gomessage.IsUnknownEncoding(err):
			continue
		case gomessage.IsUnknownCharset(err) && part != nil:
			return fileAsAttachment(part), nil
		}
		return fileAsAttachment(part), err
	}
}

// fileAsAttachment turns an inline part that is really a file into an attachment. go-message treats every part marked
// inline as body, so a named PDF sent inline (the Apple Mail and iOS Mail default) would vanish; and it reads a
// Content-Disposition whose parameters it cannot parse as no disposition, so a text part marked attachment with a
// malformed filename would become body. Inline text and inline images stay body.
func fileAsAttachment(part *gomail.Part) *gomail.Part {
	if part == nil {
		return nil
	}
	switch h := part.Header.(type) {
	case *gomail.InlineHeader:
		if isFilePart(h.Header) {
			part.Header = &gomail.AttachmentHeader{Header: h.Header}
		}
	case *gomail.AttachmentHeader:
		disp := headerToken(h.Get("Content-Disposition"))
		if (disp == "inline" && !isFilePart(h.Header)) || (disp != "attachment" && strings.HasPrefix(contentMediaType(h.Header), "text/")) {
			part.Header = &gomail.InlineHeader{Header: h.Header}
		}
	}
	return part
}

// isFilePart: the part is marked attachment, or it is marked inline, named, and neither text nor an image.
func isFilePart(h gomessage.Header) bool {
	disposition, contentType := h.Get("Content-Disposition"), h.Get("Content-Type")
	switch headerToken(disposition) {
	case "attachment":
		return true
	case "inline":
		return !staysInline(headerToken(contentType)) && (hasParam(disposition, "filename") || hasParam(contentType, "name"))
	}
	return false
}

// staysInline: inline parts of these media types are body, never attachments. No type means text/plain (RFC 2045).
func staysInline(mediaType string) bool {
	return mediaType == "" || strings.HasPrefix(mediaType, "text/") || strings.HasPrefix(mediaType, "image/") ||
		strings.HasPrefix(mediaType, "multipart/")
}

// headerToken is a raw header value's leading token (the media type or disposition), lowercased, even when the
// parameters after it do not parse.
func headerToken(value string) string {
	token, _, _ := strings.Cut(value, ";")
	return strings.ToLower(strings.TrimSpace(token))
}

// contentMediaType is h's lowercased media type, falling back to headerToken when a parameter fails to parse.
func contentMediaType(h gomessage.Header) string {
	if mt, _, err := h.ContentType(); err == nil {
		return mt
	}
	return headerToken(h.Get("Content-Type"))
}

// hasParam reports whether a raw header value carries a non-empty key parameter in any form (key=, or the RFC 2231
// key*= and key*0=), including values in a syntax or charset Go's mime package rejects.
func hasParam(value, key string) bool {
	_, params, _ := strings.Cut(value, ";")
	for _, param := range strings.Split(params, ";") {
		name, v, ok := strings.Cut(param, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if ok && (name == key || strings.HasPrefix(name, key+"*")) && strings.Trim(strings.TrimSpace(v), `"`) != "" {
			return true
		}
	}
	return false
}

// cappedSpool stores at most left bytes but consumes the full stream. Hashing and MIME parsing therefore see
// all the data, so identity checks and the IMAP connection remain correct even when the saved file is too large.
type cappedSpool struct {
	file io.Writer
	left int64
}

func (w *cappedSpool) Write(p []byte) (int, error) {
	keep := min(int64(len(p)), w.left)
	if keep > 0 {
		n, err := w.file.Write(p[:int(keep)])
		w.left -= int64(n)
		if err != nil {
			return n, err
		}
		if n != int(keep) {
			return n, io.ErrShortWrite
		}
	}
	return len(p), nil
}

// spoolLimit < 0 leaves the HTTP download unbounded; bounded saves still scan and hash all decoded bytes.
func (s *Service) attachment(ctx context.Context, accountID string, uid uint32, mailbox, id string, spoolLimit int64) (*os.File, Attachment, error) {
	a, err := s.account(accountID)
	if err != nil {
		return nil, Attachment{}, err
	}
	indexText, digest, ok := strings.Cut(id, "-")
	index, parseErr := strconv.Atoi(indexText)
	if !ok || parseErr != nil || index < 1 || len(digest) != 64 {
		return nil, Attachment{}, fmt.Errorf("invalid attachment id")
	}
	if mailbox == "" {
		mailbox = "INBOX"
	}
	file, err := os.CreateTemp("", "persona-mail-attachment-*")
	if err != nil {
		return nil, Attachment{}, err
	}
	var metadata Attachment
	err = s.withConn(ctx, a, "attachment "+mailbox, func(c *imapclient.Client) error {
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return err
		}
		// BODY.PEEK is available in IMAP4rev1; do not assume BINARY or any LIST extension.
		fetch := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{Peek: true}}})
		defer fetch.Close()
		found := false
		for message := fetch.Next(); message != nil; message = fetch.Next() {
			for item := message.Next(); item != nil; item = message.Next() {
				section, ok := item.(imapclient.FetchItemDataBodySection)
				if !ok || section.Literal == nil {
					continue
				}
				reader, err := gomail.CreateReader(section.Literal)
				if reader == nil {
					return err
				}
				count := 0
				for {
					part, err := nextPart(reader)
					if err == io.EOF {
						break
					}
					if err != nil {
						return err
					}
					header, ok := part.Header.(*gomail.AttachmentHeader)
					if !ok {
						continue
					}
					count++
					if count != index {
						continue
					}
					var dst io.Writer = file
					if spoolLimit >= 0 {
						dst = &cappedSpool{file: file, left: spoolLimit}
					}
					metadata, err = attachmentMetadata(header, part.Body, count, dst)
					if err != nil {
						return err
					}
					if metadata.ID != id {
						return fmt.Errorf("attachment not found")
					}
					found = true
					break
				}
			}
		}
		if err := fetch.Close(); err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("attachment not found")
		}
		return nil
	})
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, Attachment{}, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, Attachment{}, err
	}
	return file, metadata, nil
}

// ErrAttachmentTooLarge: the attachment is over FetchAttachment's limit.
var ErrAttachmentTooLarge = errors.New("attachment too large")

// FetchAttachment is one attachment's metadata and decoded bytes (the id mail_read lists and the HTTP route takes),
// at most max bytes. Like the download, it does not mark the message as read.
func (s *Service) FetchAttachment(ctx context.Context, accountID string, uid uint32, mailbox, id string, max int64) (Attachment, []byte, error) {
	if uid == 0 {
		return Attachment{}, nil, fmt.Errorf("invalid uid")
	}
	spoolLimit := max
	if spoolLimit < 0 {
		spoolLimit = 0
	}
	file, metadata, err := s.attachment(ctx, accountID, uid, mailbox, id, spoolLimit)
	if err != nil {
		return Attachment{}, nil, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if metadata.Size > max {
		return metadata, nil, fmt.Errorf("%w: %d bytes, the limit is %d", ErrAttachmentTooLarge, metadata.Size, max)
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return Attachment{}, nil, err
	}
	if int64(len(data)) != metadata.Size {
		return Attachment{}, nil, fmt.Errorf("attachment changed while reading")
	}
	return metadata, data, nil
}

func (s *Service) handleAttachment(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil || uid == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid uid"))
		return
	}
	file, metadata, err := s.attachment(r.Context(), r.PathValue("aid"), uint32(uid), r.URL.Query().Get("mailbox"), r.PathValue("attachment"), -1)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	ct := metadata.Mime
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": metadata.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+metadata.ID+`"`)
	// ServeContent handles Range, If-Range, Content-Range and 416 over decoded bytes.
	http.ServeContent(w, r, metadata.Filename, time.Time{}, file)
}
