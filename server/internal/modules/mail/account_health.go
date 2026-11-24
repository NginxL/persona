package mail

import (
	"context"
	"errors"

	"github.com/emersion/go-imap/v2"
)

// errGoogleRevoked is Google's token endpoint refusing the refresh token (invalid_grant): the user revoked the
// grant, changed the password, or it lapsed.
var errGoogleRevoked = errors.New("Google authorization expired or was revoked: sign in with Google again")

// loginRefused reports whether err, from connecting and logging in, means the account's credentials no longer work
// (wrong password, revoked OAuth grant) rather than a passing failure (timeout, refused connection, DNS, the server
// busy, the caller giving up). Only the former is the user's to fix and marks the account as broken.
func loginRefused(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || unreachable(err) {
		return false
	}
	if errors.Is(err, errGoogleRevoked) {
		return true
	}
	if _, ok := errors.AsType[userError](err); ok {
		// loginRejected's NO about the credentials or the account, microsoftError's lapsed or refused grant.
		return true
	}
	var ie *imap.Error
	if errors.As(err, &ie) {
		// XOAUTH2 answered NO; UNAVAILABLE is the server being busy.
		return ie.Type == imap.StatusResponseTypeNo && ie.Code != imap.ResponseCodeUnavailable
	}
	return false
}

// maxLoginErrLen bounds the reason kept for a broken account (runes), so a chatty server cannot fill the settings.
const maxLoginErrLen = 300

// noteLogin records how connecting to the account went. A refused login marks it broken with the reason; a working
// connection marks it healthy again; anything else (a timeout, the network) leaves it as it was. Kept in memory only:
// after a restart the account shows healthy until its next read fails again, a few minutes at most. It reports
// whether the record changed (a refusal that starts or says something new, a refused account working again).
func (s *Service) noteLogin(id string, err error) bool {
	if err != nil && !loginRefused(err) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		_, had := s.loginErr[id]
		delete(s.loginErr, id)
		return had
	}
	if _, ok := s.accts[id]; !ok {
		return false // deleted meanwhile
	}
	msg := []rune(err.Error())
	if len(msg) > maxLoginErrLen {
		msg = append(msg[:maxLoginErrLen-1], '…')
	}
	if s.loginErr == nil {
		s.loginErr = make(map[string]string)
	}
	before, had := s.loginErr[id]
	s.loginErr[id] = string(msg)
	return !had || before != string(msg)
}

// withHealthLocked is a's public copy with the last login's outcome as its status. The caller holds s.mu.
func (s *Service) withHealthLocked(a *MailAccount) *MailAccount {
	cp := publicAccount(a)
	if msg, ok := s.loginErr[a.ID]; ok {
		cp.Status, cp.Error = "error", msg
	}
	return cp
}
