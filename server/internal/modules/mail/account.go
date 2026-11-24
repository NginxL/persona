package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ListAccounts returns every configured account without secrets.
func (s *Service) ListAccounts() ([]MailAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MailAccount, 0, len(s.accts))
	for _, a := range s.accts {
		out = append(out, *s.withHealthLocked(a))
	}
	sortAccounts(out)
	return out, nil
}

func sortAccounts(a []MailAccount) {
	// Stable by email so the app shows a predictable order.
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && strings.ToLower(a[j].Email) < strings.ToLower(a[j-1].Email); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// account returns a copy of the account with the given ID.
func (s *Service) account(id string) (*MailAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accts[id]
	if !ok {
		return nil, &UnknownAccountError{Account: id}
	}
	return cloneAccount(a), nil
}

func cloneAccount(a *MailAccount) *MailAccount {
	cp := *a
	if a.OAuth != nil {
		creds := *a.OAuth
		cp.OAuth = &creds
	}
	return &cp
}

// publicAccount returns a copy without secrets.
func publicAccount(a *MailAccount) *MailAccount {
	cp := *a
	cp.Password, cp.OAuth = "", nil
	return &cp
}

func accountExists(email string) error {
	return fmt.Errorf("%s %w；要换服务器或授权码，先删掉原来的账号再添加", email, ErrAccountExists)
}

// hasAddress reports whether an account with this address is already there.
func (s *Service) hasAddress(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.accts {
		if strings.EqualFold(existing.Email, email) {
			return true
		}
	}
	return false
}

// AddAccount validates the account (by testing the connections) and saves it.
func (s *Service) AddAccount(req AddAccountRequest) (*MailAccount, error) {
	if req.Auth == AuthOAuth2 {
		return nil, errors.New("invalid auth \"oauth2\": use Sign in with Google")
	}
	a, err := buildAccount(req)
	if err != nil {
		return nil, err
	}
	// A duplicate is refused before the connection test, which takes seconds and up to 40 on a slow server, twice for
	// Feishu's two server sets (#1766).
	if s.hasAddress(a.Email) {
		return nil, accountExists(a.Email)
	}
	if a.Provider == ProviderFeishu {
		err = s.pickFeishuServers(context.Background(), a)
	} else {
		err = s.testAccount(context.Background(), a)
	}
	if err != nil {
		return nil, err
	}

	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	s.mu.Lock()
	for _, existing := range s.accts {
		if strings.EqualFold(existing.Email, a.Email) { // added by another request while this one was testing
			s.mu.Unlock()
			return nil, accountExists(a.Email)
		}
	}
	s.mu.Unlock()

	if err := s.commitAccount(a.ID, a, false, nil); err != nil {
		return nil, err
	}
	return publicAccount(a), nil
}

// upsertAccount saves a verified account; an existing account with the same address keeps its ID
// and name but takes the new credentials (signing in with Google again repairs a revoked token).
func (s *Service) upsertAccount(a *MailAccount) (*MailAccount, error) {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	a = cloneAccount(a)
	s.mu.Lock()
	var prev *MailAccount
	for _, existing := range s.accts {
		if strings.EqualFold(existing.Email, a.Email) {
			prev = existing
			break
		}
	}
	if prev != nil {
		a.ID, a.Name = prev.ID, prev.Name
	}
	s.mu.Unlock()

	if err := s.commitAccount(a.ID, a, true, nil); err != nil {
		return nil, err
	}
	return publicAccount(a), nil
}

// DeleteAccount removes an account and closes its connection.
func (s *Service) DeleteAccount(id string) error {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	s.mu.Lock()
	if _, ok := s.accts[id]; !ok {
		s.mu.Unlock()
		return &UnknownAccountError{Account: id}
	}
	s.mu.Unlock()
	return s.commitAccount(id, nil, true, nil)
}

// commitAccount persists a detached candidate, then publishes it and retires
// health/connections together when credentials changed or the account was removed.
// A nil account removes id; publish updates associated caches under s.mu.
// The caller holds accountMu, never across network verification.
func (s *Service) commitAccount(id string, a *MailAccount, retire bool, publish func()) error {
	s.mu.Lock()
	accounts := s.snapshotLocked()
	s.mu.Unlock()
	candidate := make([]*MailAccount, 0, len(accounts)+1)
	for _, existing := range accounts {
		if existing.ID != id {
			candidate = append(candidate, existing)
		}
	}
	if a != nil {
		a = cloneAccount(a)
		candidate = append(candidate, a)
	}
	if err := s.store.save(candidate); err != nil {
		return err
	}
	s.mu.Lock()
	if a == nil {
		delete(s.accts, id)
	} else {
		s.accts[id] = a
	}
	var c *conn
	if retire {
		delete(s.loginErr, id)
		c = s.conns[id]
		delete(s.conns, id)
	}
	if publish != nil {
		publish()
	}
	s.mu.Unlock()
	if c != nil {
		c.c.Close()
	}
	return nil
}

// snapshotLocked copies accounts and their credentials. The caller holds s.mu.
func (s *Service) snapshotLocked() []*MailAccount {
	out := make([]*MailAccount, 0, len(s.accts))
	for _, a := range s.accts {
		out = append(out, cloneAccount(a))
	}
	return out
}

// buildAccount normalizes a request, applying the domain preset for any field
// the request leaves empty.
func buildAccount(req AddAccountRequest) (*MailAccount, error) {
	email := normalizeEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, errors.New("email is required")
	}
	a := &MailAccount{
		ID:           newAccountID(),
		Name:         strings.TrimSpace(req.Name),
		Email:        email,
		Username:     strings.TrimSpace(req.Username),
		IMAPHost:     strings.TrimSpace(req.IMAPHost),
		IMAPPort:     req.IMAPPort,
		IMAPSecurity: strings.ToLower(strings.TrimSpace(req.IMAPSecurity)),
		SMTPHost:     strings.TrimSpace(req.SMTPHost),
		SMTPPort:     req.SMTPPort,
		SMTPSecurity: strings.ToLower(strings.TrimSpace(req.SMTPSecurity)),
		Password:     req.Password,
		Status:       "ok",
	}
	if a.Name == "" {
		a.Name = email
	}
	if a.Username == "" {
		a.Username = email
	}

	preset := presetFor(email)
	// Providers with fixed servers (Feishu) win over the email domain, which is the company's own.
	if p := providerPresets[req.Provider]; p != nil {
		preset = p
		a.IMAPHost, a.IMAPPort, a.SMTPHost, a.SMTPPort, a.SMTPSecurity = "", 0, "", 0, ""
	}
	// A Microsoft sign-in decides the provider: the presets for outlook.com and friends are the IMAP ones.
	if req.Auth == AuthOAuth2 && req.Provider == ProviderOutlook {
		a.Provider = ProviderOutlook
	}
	if preset != nil {
		if a.Provider == "" {
			a.Provider = preset.Provider
		}
		if a.IMAPHost == "" {
			a.IMAPHost = preset.IMAPHost
		}
		if a.IMAPPort == 0 {
			a.IMAPPort = preset.IMAPPort
		}
		if a.SMTPHost == "" {
			a.SMTPHost = preset.SMTPHost
		}
		if a.SMTPPort == 0 {
			a.SMTPPort = preset.SMTPPort
		}
		if a.SMTPSecurity == "" {
			a.SMTPSecurity = preset.SMTPSecurity
		}
	}
	if a.Provider == "" {
		a.Provider = req.Provider
	}
	if a.Provider == "" {
		a.Provider = ProviderIMAP
	}
	if a.Provider != ProviderGmail && a.Provider != ProviderIMAP && a.Provider != ProviderFeishu && a.Provider != ProviderOutlook {
		return nil, fmt.Errorf("invalid provider %q", a.Provider)
	}

	switch a.Auth = req.Auth; a.Auth {
	case "", AuthPassword:
		a.Auth = AuthPassword
		if a.Password == "" {
			return nil, errors.New("password is required")
		}
	case AuthOAuth2:
		if a.Provider != ProviderGmail && a.Provider != ProviderOutlook {
			return nil, errors.New("invalid auth \"oauth2\": only Gmail and Outlook sign in with their accounts")
		}
	default:
		return nil, fmt.Errorf("invalid auth %q", a.Auth)
	}

	if a.IMAPHost == "" || a.IMAPPort == 0 {
		return nil, fmt.Errorf("unknown mail provider for %s: provide imap_host and imap_port", email)
	}
	if a.SMTPHost == "" || a.SMTPPort == 0 {
		return nil, fmt.Errorf("unknown mail provider for %s: provide smtp_host and smtp_port", email)
	}
	if a.SMTPSecurity == "" {
		switch a.SMTPPort {
		case 465:
			a.SMTPSecurity = "tls"
		case 587, 25:
			a.SMTPSecurity = "starttls"
		default:
			a.SMTPSecurity = "tls"
		}
	}
	return a, nil
}
