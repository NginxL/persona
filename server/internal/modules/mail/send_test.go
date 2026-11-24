package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func TestRenderMessageChineseSubject(t *testing.T) {
	req := SendRequest{
		To:      []Address{{Name: "李四", Email: "li@example.com"}},
		Cc:      []Address{{Email: "cc@example.com"}},
		Subject: "关于下周的会议",
		Text:    "你好，这是正文。",
	}
	raw, err := RenderMessage(req, Address{Name: "张三", Email: "me@example.com"})
	if err != nil {
		t.Fatalf("RenderMessage: %v", err)
	}

	s := string(raw)
	if strings.Contains(s, "Subject: 关于") {
		t.Error("subject was written as raw UTF-8 instead of encoded-words")
	}
	if !strings.Contains(s, "Subject: =?utf-8?") {
		t.Errorf("subject is not RFC 2047 encoded:\n%s", s)
	}
	if !strings.Contains(s, "From: =?utf-8?") {
		t.Errorf("from display name is not RFC 2047 encoded:\n%s", s)
	}
	// Every line must be CRLF terminated for SMTP.
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Error("message contains a bare LF")
	}

	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("CreateReader: %v", err)
	}
	defer r.Close()

	subject, err := r.Header.Subject()
	if err != nil {
		t.Fatalf("Subject: %v", err)
	}
	if subject != req.Subject {
		t.Errorf("decoded subject = %q, want %q", subject, req.Subject)
	}
	from, err := r.Header.AddressList("From")
	if err != nil || len(from) != 1 {
		t.Fatalf("From = %v, %v", from, err)
	}
	if from[0].Name != "张三" || from[0].Address != "me@example.com" {
		t.Errorf("from = %+v", from[0])
	}
	to, err := r.Header.AddressList("To")
	if err != nil || len(to) != 1 || to[0].Name != "李四" {
		t.Fatalf("to = %v, %v", to, err)
	}
	cc, err := r.Header.AddressList("Cc")
	if err != nil || len(cc) != 1 || cc[0].Address != "cc@example.com" {
		t.Fatalf("cc = %v, %v", cc, err)
	}
	// The Message-ID names the sender's domain, not the machine the hub runs on.
	if id, err := r.Header.MessageID(); err != nil || !strings.HasSuffix(id, "@example.com") {
		t.Errorf("message id = %q, %v; want it to end in @example.com", id, err)
	}

	var text string
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		b, _ := io.ReadAll(p.Body)
		if h, ok := p.Header.(*gomail.InlineHeader); ok {
			if ct, _, _ := h.ContentType(); strings.HasPrefix(ct, "text/plain") {
				text = string(b)
			}
		}
	}
	if text != req.Text {
		t.Errorf("text = %q, want %q", text, req.Text)
	}
}

// Text-only sends must name their charset; the default is US-ASCII.
func TestRenderMessageTextOnlyDeclaresUTF8(t *testing.T) {
	raw, err := RenderMessage(SendRequest{
		To: []Address{{Email: "li@example.com"}}, Subject: "x", Text: "你好",
	}, Address{Email: "me@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ct := r.Header.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != "text/plain" || !strings.EqualFold(params["charset"], "utf-8") {
		t.Fatalf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	p, err := r.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(p.Body)
	if err != nil || string(b) != "你好" {
		t.Fatalf("body = %q %v, want 你好", b, err)
	}
}

func TestRenderMessageHTMLAlternativeAndThreading(t *testing.T) {
	req := SendRequest{
		To:        []Address{{Email: "li@example.com"}},
		Subject:   "Re: hi",
		Text:      "plain",
		HTML:      "<p>html</p>",
		InReplyTo: "<parent@example.com>",
	}
	raw, err := RenderMessage(req, Address{Email: "me@example.com"})
	if err != nil {
		t.Fatalf("RenderMessage: %v", err)
	}

	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("CreateReader: %v", err)
	}
	defer r.Close()

	inReplyTo, err := r.Header.MsgIDList("In-Reply-To")
	if err != nil || len(inReplyTo) != 1 || inReplyTo[0] != "parent@example.com" {
		t.Fatalf("In-Reply-To = %v, %v", inReplyTo, err)
	}
	refs, err := r.Header.MsgIDList("References")
	if err != nil || len(refs) != 1 || refs[0] != "parent@example.com" {
		t.Fatalf("References = %v, %v", refs, err)
	}

	var parts []string
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		h, ok := p.Header.(*gomail.InlineHeader)
		if !ok {
			continue
		}
		ct, _, _ := h.ContentType()
		b, _ := io.ReadAll(p.Body)
		parts = append(parts, ct+"="+string(b))
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %v, want text/plain and text/html", parts)
	}
	if parts[0] != "text/plain=plain" || parts[1] != "text/html=<p>html</p>" {
		t.Fatalf("parts = %v", parts)
	}
}

func TestRenderMessageRequiresRecipients(t *testing.T) {
	if _, err := RenderMessage(SendRequest{Subject: "x", Text: "y"}, Address{Email: "me@example.com"}); err == nil {
		t.Fatal("expected an error without recipients")
	}
}

// testCertPEM and testKeyPEM are a throwaway self-signed certificate for
// localhost, used only to exercise implicit TLS against a test server.
// fakeSMTPServer records the delivered message.
type fakeSMTPServer struct {
	addr    string
	mu      sync.Mutex
	helo    string
	from    string
	rcpts   []string
	data    []byte
	user    string
	pass    string
	backend *smtp.Server
	// XOAUTH2: the access token accepted, who authenticated with it, and an error to refuse with instead.
	token    string
	authUser string
	authErr  *smtp.SMTPError
}

type fakeSession struct {
	srv  *fakeSMTPServer
	from string
	utf8 bool
}

func (s *fakeSession) Reset()        {}
func (s *fakeSession) Logout() error { return nil }
func (s *fakeSession) Mail(from string, opts *smtp.MailOptions) error {
	s.from = from
	s.utf8 = opts != nil && opts.UTF8
	return nil
}
func (s *fakeSession) Rcpt(to string, opts *smtp.RcptOptions) error {
	// As RFC 6531 servers do: a non-ASCII address only after MAIL FROM declared SMTPUTF8.
	if !s.utf8 && !isASCII(to) {
		return &smtp.SMTPError{Code: 553, EnhancedCode: smtp.EnhancedCode{5, 6, 7}, Message: "SMTPUTF8 required"}
	}
	s.srv.mu.Lock()
	s.srv.rcpts = append(s.srv.rcpts, to)
	s.srv.mu.Unlock()
	return nil
}
func (s *fakeSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.srv.mu.Lock()
	s.srv.from = s.from
	s.srv.data = b
	s.srv.mu.Unlock()
	return nil
}
func (s *fakeSession) AuthMechanisms() []string { return []string{"PLAIN", "XOAUTH2"} }
func (s *fakeSession) Auth(mech string) (sasl.Server, error) {
	if mech == "XOAUTH2" {
		return xoauth2Server(func(user, token string) error {
			s.srv.mu.Lock()
			defer s.srv.mu.Unlock()
			if s.srv.authErr != nil {
				return s.srv.authErr
			}
			if token != s.srv.token {
				return smtp.ErrAuthFailed
			}
			s.srv.authUser = user
			return nil
		}), nil
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if username != s.srv.user || password != s.srv.pass {
			return smtp.ErrAuthFailed
		}
		return nil
	}), nil
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	fs := &fakeSMTPServer{user: "me@example.com", pass: "secret"}
	srv := smtp.NewServer(smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		fs.mu.Lock()
		fs.helo = c.Hostname()
		fs.mu.Unlock()
		return &fakeSession{srv: fs}, nil
	}))
	srv.Domain = "example.com"
	srv.AllowInsecureAuth = true
	srv.EnableSMTPUTF8 = true
	srv.ReadTimeout = 5e9
	srv.WriteTimeout = 5e9
	fs.backend = srv

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fs.addr = ln.Addr().String()
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return fs
}

func TestSendOverSMTP(t *testing.T) {
	srv := newTestIMAP(t)
	fs := newFakeSMTPServer(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()

	host, portStr, _ := net.SplitHostPort(fs.addr)
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	a := srv.testAccount()
	a.SMTPHost = host
	a.SMTPPort = port
	a.SMTPSecurity = "none"
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()

	err := s.Send(ctx, SendRequest{
		AccountID: "acc_test",
		To:        []Address{{Name: "李四", Email: "li@example.com"}},
		Cc:        []Address{{Email: "cc@example.com"}},
		Subject:   "你好",
		Text:      "正文",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.helo != "[127.0.0.1]" {
		t.Errorf("EHLO = %q, want the address literal [127.0.0.1], not the machine's host name", fs.helo)
	}
	if fs.from != "me@example.com" {
		t.Errorf("MAIL FROM = %q", fs.from)
	}
	if strings.Join(fs.rcpts, ",") != "li@example.com,cc@example.com" {
		t.Errorf("RCPT TO = %v", fs.rcpts)
	}
	h, err := mail.ReadMessage(bytes.NewReader(fs.data))
	if err != nil {
		t.Fatalf("read delivered message: %v\n%s", err, fs.data)
	}
	subject, _ := gomailHeaderSubject(fs.data)
	if subject != "你好" {
		t.Errorf("delivered subject = %q", subject)
	}
	_ = h
	// The delivered body is quoted-printable encoded; go-message decodes it.
	entity, _ := gomessage.Read(bytes.NewReader(fs.data))
	decoded := ""
	if entity != nil {
		b, _ := io.ReadAll(entity.Body)
		decoded = string(b)
	}
	if !strings.Contains(decoded, "正文") {
		t.Errorf("delivered body = %q, want it to contain %q", decoded, "正文")
	}

	// The message must also be appended to Sent.
	res, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Mailbox: "Sent"})
	if err != nil {
		t.Fatalf("list Sent: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("Sent has %d messages, want 1", len(res.Messages))
	}
	if res.Messages[0].Subject != "你好" {
		t.Errorf("Sent subject = %q", res.Messages[0].Subject)
	}
	if !res.Messages[0].Seen {
		t.Error("appended message should be marked seen")
	}
}

// A non-ASCII recipient address (RFC 6531) gets SMTPUTF8 declared on MAIL FROM even when the sender's own address is
// ASCII: a server that offers SMTPUTF8 refuses the recipient otherwise.
func TestSendDeclaresSMTPUTF8ForNonASCIIRecipient(t *testing.T) {
	srv := newTestIMAP(t)
	fs := newFakeSMTPServer(t)
	s := newServiceWithAccount(t, srv)
	useFakeSMTP(t, s, srv, fs)

	err := s.Send(context.Background(), SendRequest{
		AccountID: "acc_test",
		To:        []Address{{Email: "用户@例子.测试"}},
		Cc:        []Address{{Email: "cc@example.com"}},
		Subject:   "x",
		Text:      "y",
	})
	if err != nil {
		t.Fatalf("Send = %v, want the non-ASCII recipient accepted", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if got := strings.Join(fs.rcpts, ","); got != "用户@例子.测试,cc@example.com" {
		t.Errorf("RCPT TO = %q", got)
	}
}

func TestSendAuthFailure(t *testing.T) {
	srv := newTestIMAP(t)
	fs := newFakeSMTPServer(t)
	s := newServiceWithAccount(t, srv)

	host, portStr, _ := net.SplitHostPort(fs.addr)
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	a := srv.testAccount()
	a.SMTPHost = host
	a.SMTPPort = port
	a.SMTPSecurity = "none"
	a.Password = "wrong"
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()

	err := s.Send(context.Background(), SendRequest{
		AccountID: "acc_test",
		To:        []Address{{Email: "li@example.com"}},
		Subject:   "x",
		Text:      "y",
	})
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("err = %v, want an auth error", err)
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	// A TLS listener with a self-signed certificate: the client must connect
	// with implicit TLS on port 465-style settings.
	cert, err := tls.X509KeyPair([]byte(testCertPEM), []byte(testKeyPEM))
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}
	fs := &fakeSMTPServer{user: "me@example.com", pass: "secret"}
	srv := smtp.NewServer(smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		return &fakeSession{srv: fs}, nil
	}))
	srv.Domain = "example.com"
	srv.AllowInsecureAuth = true
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	fs.backend = srv

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	srvIMAP := newTestIMAP(t)
	s := newServiceWithAccount(t, srvIMAP)
	a := srvIMAP.testAccount()
	a.SMTPHost = host
	a.SMTPPort = port
	a.SMTPSecurity = "tls"
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()

	// The self-signed certificate is not trusted, so the expected outcome is a
	// failure that proves the TLS handshake (not plaintext) was attempted.
	err = s.Send(context.Background(), SendRequest{
		AccountID: "acc_test",
		To:        []Address{{Email: "li@example.com"}},
		Subject:   "x",
		Text:      "y",
	})
	if err == nil {
		t.Fatal("Send succeeded against an untrusted certificate")
	}
	fs.mu.Lock()
	delivered := len(fs.data)
	fs.mu.Unlock()
	if delivered != 0 {
		t.Fatal("message was delivered despite the failed handshake")
	}
}

// gomailHeaderSubject decodes the Subject of a raw message.
func gomailHeaderSubject(raw []byte) (string, error) {
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer r.Close()
	return r.Header.Subject()
}

// silence unused-import churn when only parts of the file compile.
var _ = gomessage.Header{}

// xoauth2Server reads a SASL XOAUTH2 initial response ("user=…^Aauth=Bearer …^A^A") and checks it.
type xoauth2Server func(user, token string) error

func (f xoauth2Server) Next(response []byte) ([]byte, bool, error) {
	var user, token string
	for _, part := range strings.Split(string(response), "\x01") {
		if v, ok := strings.CutPrefix(part, "user="); ok {
			user = v
		}
		if v, ok := strings.CutPrefix(part, "auth=Bearer "); ok {
			token = v
		}
	}
	return nil, true, f(user, token)
}

// useFakeSMTP points the test account's SMTP settings at fs.
func useFakeSMTP(t *testing.T, s *Service, srv *testIMAP, fs *fakeSMTPServer) {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(fs.addr)
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("smtp port %q: %v", portStr, err)
	}
	a := srv.testAccount()
	a.SMTPHost = host
	a.SMTPPort = port
	a.SMTPSecurity = "none"
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()
}

// TLS credentials belong only to this test process, not to the repository history.
var testCertPEM, testKeyPEM = newSMTPTestTLSMaterial()

func newSMTPTestTLSMaterial() (string, string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("could not generate SMTP test TLS material")
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic("could not generate SMTP test certificate")
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic("could not encode SMTP test TLS material")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}))
}
