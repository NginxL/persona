package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"

	"github.com/nwverse/persona/server/internal/i18n"
)

// connectFailure is err told in words for people when it means the mail server could not be reached or trusted
// (as opposed to a login the server answered and rejected, which loginRejected explains); nil when err is
// something else. It names the machine that failed to connect: the hub's, not the phone's, since the hub opens
// every IMAP / SMTP connection. Written in the hub's default language like loginRejected's.
func connectFailure(host string, port int, viaProxy bool, err error) error {
	target := host + ":" + strconv.Itoa(port)
	var (
		pe  *proxyError
		dns *net.DNSError
		ne  net.Error
		rh  tls.RecordHeaderError
		ua  x509.UnknownAuthorityError
		hn  x509.HostnameError
		ci  x509.CertificateInvalidError
	)
	msg := func(key string) error {
		return userError{cause: err, msg: i18n.T(i18n.Default, "mail-connect."+key,
			i18n.Args{"proxy": proxyHost(pe), "reason": proxyReason(pe), "target": target, "host": host})}
	}
	switch {
	case errors.As(err, &pe):
		return msg("proxy")
	case errors.As(err, &dns):
		return msg("dns")
	case errors.As(err, &rh), errors.As(err, &ua), errors.As(err, &hn), errors.As(err, &ci), strings.Contains(err.Error(), "tls:"):
		return msg("tls")
	case errors.Is(err, syscall.ECONNREFUSED):
		return msg("refused")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne):
		if viaProxy {
			return msg("timeout_proxy")
		}
		return msg("timeout")
	}
	return nil
}

func proxyHost(pe *proxyError) string {
	if pe == nil {
		return ""
	}
	return pe.proxy
}

func proxyReason(pe *proxyError) string {
	if pe == nil {
		return ""
	}
	return pe.err.Error()
}
