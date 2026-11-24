package mail

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset" // register GBK etc. charset decoders
)

const (
	// networkTimeout bounds every single network operation (dial, command).
	networkTimeout = 20 * time.Second
	// idleTimeout closes a cached IMAP connection that has been unused this long. It is longer than the mail rounds
	// (the inbox's every 3 minutes, the knowledge base's every 6), so a round reuses the last one's login instead of
	// a fresh TCP + TLS + LOGIN per account; RFC 3501 servers keep an idle session at least 30 minutes.
	idleTimeout = 10 * time.Minute
	// sweepInterval is how often idle connections are checked.
	sweepInterval = 30 * time.Second
)

// Service is the mail module. One Service per hub process.
type Service struct {
	dir   string
	store *store

	// accountMu serializes candidate capture, persistence and publication. Network
	// verification happens before taking it; readers see only committed accounts.
	accountMu accountCommitMutex
	mu        sync.Mutex
	accts     map[string]*MailAccount // by ID, as loaded from disk
	conns     map[string]*conn        // by account ID
	// loginErr is why an account's last login was refused, by ID (account_health.go); in memory only.
	loginErr map[string]string
	opMu     map[string]accountLock // per-account IMAP serialization (imap.go)

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// now is swappable in tests.
	now func() time.Time
	// imapDial and sendMail are swappable in tests; nil means the real network.
	imapDial func(a *MailAccount) (*imapclient.Client, error)
	// googleClientID is the iOS OAuth client used for "Sign in with Google" (hub.json google.client_id).
	googleClientID string
	// Outlook sign-in (microsoft.go): hub.json's default client ID and redirect, and the one the user entered
	// (loaded from the settings once).
	msDefaultClientID, msRedirect string
	msClientID                    string
	msClientLoaded                bool
	usedStates                    map[string]time.Time    // OAuth states already finished, against replays
	googleProxy                   *url.URL                // hub.json google.proxy for Gmail traffic (proxy.go), nil = direct
	googleHTTP                    *http.Client            // talks to Google's OAuth and Gmail API endpoints
	access                        map[string]*accessCache // access tokens, by refresh token
	// verify replaces the IMAP + SMTP login check of new accounts in tests.
	verify func(ctx context.Context, a *MailAccount) error
	// scan bounds one page of a local header search, fetch a page's summary fetch; tests shrink them.
	scan  localScan
	fetch pageFetch
	// probe is when a cached connection is checked before it is reused; tests shrink it.
	probe connProbe
	// refreshAccess replaces the Google token endpoint in tests.
	refreshAccess func(*OAuthCreds) (string, error)
	// sendMail delivers a rendered message over SMTP; nil uses the real network.
	sendMail func(ctx context.Context, a *MailAccount, from string, to []string, raw []byte) error
}

// accountCommitMutex has a usable zero value, including for Services without a
// store. A channel lets concurrency tests wait for blocked writers deterministically.
type accountCommitMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *accountCommitMutex) Lock() {
	m.once.Do(func() { m.ch = make(chan struct{}, 1) })
	m.ch <- struct{}{}
}

func (m *accountCommitMutex) Unlock() { <-m.ch }

// New creates a Service. Accounts live in recs; dataDir holds the encryption key (secret.key).
func New(dataDir string, recs Records) (*Service, error) {
	return NewWithKey(dataDir, filepath.Join(dataDir, "secret.key"), recs)
}

// NewWithKey is New with the encryption key at keyPath (a manager's own secret.key, not its data directory's).
func NewWithKey(dataDir, keyPath string, recs Records) (*Service, error) {
	st, err := openStore(dataDir, keyPath, recs)
	if err != nil {
		return nil, err
	}
	s := &Service{
		dir:        dataDir,
		store:      st,
		accts:      make(map[string]*MailAccount),
		conns:      make(map[string]*conn),
		loginErr:   make(map[string]string),
		usedStates: make(map[string]time.Time),
		googleHTTP: httpClient,
		access:     make(map[string]*accessCache),
		stop:       make(chan struct{}),
		now:        time.Now,
		scan:       defaultLocalScan,
		fetch:      defaultPageFetch,
		probe:      defaultConnProbe,
	}
	accounts, err := st.load()
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		s.accts[a.ID] = a
	}
	s.wg.Add(1)
	go s.sweep()
	return s, nil
}

// SetGoogleClientID enables "Sign in with Google" for Gmail with an iOS OAuth client ID.
func (s *Service) SetGoogleClientID(id string) { s.googleClientID = strings.TrimSpace(id) }

// Close closes every cached connection and stops the idle sweeper.
func (s *Service) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
	s.mu.Lock()
	conns := s.conns
	s.conns = make(map[string]*conn)
	s.mu.Unlock()
	for _, c := range conns {
		c.c.Close()
	}
	return nil
}

// sweep closes IMAP connections that have been idle for too long.
func (s *Service) sweep() {
	defer s.wg.Done()
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		s.sweepOnce()
	}
}

// sweepOnce closes every idle connection.
func (s *Service) sweepOnce() {
	now := s.now()
	s.mu.Lock()
	var stale []*conn
	for id, c := range s.conns {
		if now.Sub(c.lastUsed) > idleTimeout {
			stale = append(stale, c)
			delete(s.conns, id)
		}
	}
	s.mu.Unlock()
	for _, c := range stale {
		c.c.Close()
	}
}

// logf is a thin indirection so tests can silence output.
var logf = func(format string, args ...any) { log.Printf(format, args...) }
