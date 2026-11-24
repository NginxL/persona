package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// The account the connector reads as, and what was read as it (#7832).
//
// A token does not say whose it is: GitHub does (/user). So a token new to the connector — the first one, one a
// computer hands over after GitHub refused the last, one from another source — reads or answers nothing that
// belongs to an account until GitHub has named its account (credential), and the connector counts the accounts it
// has read as: the epoch. What is read as one account (the inbox's things, the notifications cursor, the answers
// kept for If-None-Match) belongs to the epoch it was read under. A change of account retires and resets all of it
// in one step (adopt); a read or a reply that began under the epoch before is refused before it is sent (asEpoch)
// and writes nothing back. What belongs to no account (a tool's read, a write the user allowed) goes out with
// the token in use as before.
//
// The hub keeps the inbox's items longer than this process lives, and gives back only an item's thread and the
// move it holds (InboxThing.Last). So the login and the epoch are kept across restarts (accountRecord), and every
// move read after the first change of account carries its epoch (markLast): a reply is admitted by the epoch of
// the move it answers, whether or not the connector still holds the thing, and the items of the account before
// are closed through Settle (inboxState.unsettled).

// errAccountChanged: the connector reads as another account than the one a read or a reply began under.
var errAccountChanged = errors.New("the GitHub account changed: read it again")

// account is whose the token in memory is. c.mu guards it.
type account struct {
	// login is the account GitHub last named for a token ("" until it has, here or before a restart); known says
	// it is the token in memory's.
	login string
	known bool
	// epoch counts the changes of login, across restarts: what account-scoped state is read under.
	epoch uint64
	// cred counts the credentials: every token put in memory or dropped, every source chosen. Answers come back in
	// any order: one to a credential since replaced says nothing of the account now.
	cred uint64
	// restored: what was kept before this process started has been read (restore); kept is what the store holds.
	restored bool
	kept     accountRecord
}

// accountRecord is what is kept of the account across restarts, with the sign-in (deviceLogin).
type accountRecord struct {
	Login string `json:"login"`
	Epoch uint64 `json:"epoch,omitempty"`
	// Unsettled: the hub may still hold open items of the account before (inboxState.unsettled).
	Unsettled bool `json:"unsettled,omitempty"`
}

// restore reads the account kept before this process started, before anything is read or answered as an account:
// the first login GitHub names is compared with it, so a login that changed while the manager was down is a change
// like any other. It reads again after Reload, and after a read that failed: until the store can be read the
// connector goes on with what it has in memory, as it does without a store (a computer's gh login must not stop
// working because the hub's secret no longer opens). A record ahead of what this process has (read late, or saved
// by whoever held the id meanwhile) replaces it, and GitHub is asked again whose the token is.
func (c *Connector) restore(ctx context.Context) {
	c.mu.Lock()
	done := c.acct.restored
	c.mu.Unlock()
	if done {
		return
	}
	c.deviceState(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.acct.restored || c.secrets != nil && !c.devLoaded {
		return
	}
	c.acct.restored = true
	r := c.dev.Account
	if r == nil || r.Login == "" {
		return // nothing kept: keepAccount writes what is in use
	}
	c.acct.kept = *r
	switch {
	case c.acct.login == "": // nothing named in this process yet
		c.acct.login, c.acct.epoch = r.Login, r.Epoch
		c.inbox.reset(r.Epoch, r.Unsettled)
		c.etags.reset(r.Epoch)
	case r.Epoch > c.acct.epoch:
		c.acct.login, c.acct.epoch, c.acct.known = r.Login, r.Epoch, false
		c.inbox.reset(r.Epoch, r.Unsettled)
		c.etags.reset(r.Epoch)
	case r.Epoch == c.acct.epoch && !strings.EqualFold(r.Login, c.acct.login):
		// This process named an account without knowing the one kept: asked again, GitHub's answer is a change.
		c.acct.login, c.acct.known = r.Login, false
	}
}

// keepAccount saves the account for the next start when it is not what the store holds. A save that fails is
// tried again the next time (every check).
func (c *Connector) keepAccount(ctx context.Context) {
	now := func() accountRecord { // c.mu is held
		return accountRecord{Login: c.acct.login, Epoch: c.acct.epoch, Unsettled: c.inbox.isUnsettled()}
	}
	c.mu.Lock()
	same := c.secrets == nil || !c.acct.restored || c.acct.login == "" || c.acct.kept == now()
	c.mu.Unlock()
	if same {
		return
	}
	var saved accountRecord
	if err := c.saveDevice(ctx, func(d *deviceLogin) {
		saved = now()
		d.Account = &saved
	}); err != nil {
		return
	}
	c.mu.Lock()
	c.acct.kept = saved
	c.mu.Unlock()
}

// keepSettled saves, before the hub is told to close the items of the account before (Settle), that this is done
// for epoch, and says whether it is saved: a restart that still found them unsettled would close, by the same
// exclusion, items of the account in use that the new process holds no thing of yet. Without a store there is
// nothing a restart could find. A store that cannot be written keeps the old items open (and unanswerable).
func (c *Connector) keepSettled(ctx context.Context, epoch uint64) bool {
	c.mu.Lock()
	none, restored := c.secrets == nil, c.acct.restored
	c.mu.Unlock()
	if none {
		return true
	}
	if !restored {
		return false
	}
	var saved accountRecord
	err := c.saveDevice(ctx, func(d *deviceLogin) {
		if c.acct.epoch != epoch {
			return // the account changed again meanwhile: that change's own save says what holds
		}
		saved = accountRecord{Login: c.acct.login, Epoch: epoch}
		d.Account = &saved
	})
	if err != nil || saved.Login == "" {
		return false
	}
	c.mu.Lock()
	c.acct.kept = saved
	c.mu.Unlock()
	return true
}

// newCredential puts tok in memory ("" drops the one there): nobody knows whose it is until GitHub says. c.mu is
// held.
func (c *Connector) newCredential(tok string) uint64 {
	c.token = tok
	c.acct.cred++
	c.acct.known = false
	return c.acct.cred
}

// How often credential looks again when the credential is replaced under it (a source chosen while a computer or
// GitHub was answering).
const credentialTries = 4

// tokenNow is the token in memory and its credential, fetching one only when there is none or GitHub refused the
// one there (refused): a machine that went offline leaves the one in memory in use. What was fetched for a
// credential since replaced is dropped: the source it came from may no longer be the one chosen.
func (c *Connector) tokenNow(ctx context.Context, refused string) (string, uint64, error) {
	for range credentialTries {
		c.mu.Lock()
		tok, cred, source, fetch := c.token, c.acct.cred, c.source, c.fetch
		c.mu.Unlock()
		if tok != "" && tok != refused {
			return tok, cred, nil
		}
		var fresh string
		switch {
		case source == SourceDevice:
			if fresh = c.deviceState(ctx).Token; fresh == "" {
				return "", cred, errNoDeviceLogin
			}
		case fetch == nil:
			return "", cred, ErrNoMachine
		default:
			var err error
			if fresh, err = fetch(ctx, c.cfg.Host()); err != nil {
				return "", cred, err
			}
		}
		c.mu.Lock()
		if c.acct.cred != cred {
			c.mu.Unlock()
			continue
		}
		replaced := tok != "" && fresh != tok
		if fresh != tok {
			cred = c.newCredential(fresh)
		}
		c.mu.Unlock()
		if replaced {
			// Another token than the one GitHub refused may be another account's: Run asks now, without waiting
			// for the next read that belongs to an account to.
			c.kickCheck()
		}
		return fresh, cred, nil
	}
	return "", 0, errAccountChanged
}

// How much credential must know of whose the token is.
const (
	tokenAsIs  = iota // the token in use, whoever's: what belongs to no account
	tokenKnown        // GitHub has named its account: what is read or answered as an account (asEpoch)
	tokenAsked        // GitHub is asked now, even for a known one (check)
)

// credential is the token to send and the account epoch it reads as. For tokenKnown a token GitHub has not named
// the account of is asked about first, and nothing is sent with it before. refused is a token GitHub just answered
// 401 to: the machine may have signed in again since, so it is asked once more. The last result is the credential
// the answer or the error belongs to.
func (c *Connector) credential(ctx context.Context, refused string, need int) (tok string, epoch, cred uint64, err error) {
	if need != tokenAsIs {
		c.restore(ctx)
	}
	again := refused != ""
	for range credentialTries {
		if tok, cred, err = c.tokenNow(ctx, refused); err != nil {
			return "", 0, cred, err
		}
		c.mu.Lock()
		known := c.acct.known && c.acct.cred == cred
		epoch = c.acct.epoch
		c.mu.Unlock()
		if need == tokenAsIs || need == tokenKnown && known {
			return tok, epoch, cred, nil
		}
		var u struct {
			Login string `json:"login"`
		}
		code, h, askErr := c.roundTrip(ctx, http.MethodGet, "/user", nil, nil, &u, tok)
		if code == http.StatusUnauthorized && !again {
			refused, again = tok, true
			continue
		}
		if askErr == nil && u.Login == "" {
			askErr = errors.New("GitHub GET /user: no login in the answer")
		}
		if askErr != nil {
			return "", 0, cred, askErr
		}
		c.mu.Lock()
		taken := c.adopt(cred, u.Login, h)
		epoch = c.acct.epoch
		c.mu.Unlock()
		if taken {
			c.keepAccount(ctx)
			return tok, epoch, cred, nil
		}
		refused = "" // the credential was replaced while GitHub answered: look at the one there now
	}
	return "", 0, cred, errAccountChanged
}

// adopt records GitHub's answer to whose the credential cred is: login, and h, the answer's headers (the token's
// scopes). An answer to a credential since replaced is dropped (false): it would put the account before back.
// A login other than the last is another account: everything read as the last one is retired and reset here, in
// the same step that makes the new one the account in use, so nothing read as one is ever served or answered as
// the other. The same login again (a token refreshed, the same account from another source) keeps everything.
// c.mu is held; the inbox's and the cache's own locks are taken under it, never the other way round.
func (c *Connector) adopt(cred uint64, login string, h http.Header) bool {
	if cred != c.acct.cred {
		return false
	}
	now := time.Now()
	if c.acct.login != "" && !strings.EqualFold(c.acct.login, login) {
		c.acct.epoch++
		c.inbox.retire(c.acct.epoch)
		c.etags.reset(c.acct.epoch)
		select {
		case c.repoll <- struct{}{}: // Watch tells the hub now, and reads the new account's inbox
		default:
		}
	}
	c.acct.login, c.acct.known = login, true
	c.scopes, c.scopesKnown = scopesOf(h)
	c.status = connectorwire.Status{State: connectorwire.StateOK, Account: login, LastSync: now.UTC(), LastEvent: c.status.LastEvent}
	return true
}

// accountKnown says whether GitHub has named the account of the token in memory. Until it has, nothing kept for
// the account before is served: the read is sent, which asks first (tokenKnown).
func (c *Connector) accountKnown(ctx context.Context) bool {
	c.restore(ctx) // a read that binds itself to an epoch does so to the one kept
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acct.known
}

// epochNow is the epoch of the account the connector reads as.
func (c *Connector) epochNow(ctx context.Context) uint64 {
	c.restore(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acct.epoch
}

// markLast is the move stamp as the hub keeps it (InboxThing.Last), read under epoch: as it is while the connector
// has only ever read as one account (nothing changes for a hub that never changes account), and with the epoch
// after it from the first change on. The hub hands a reply the move its item holds: lastEpoch reads whose it is.
func markLast(stamp string, epoch uint64) string {
	if epoch == 0 {
		return stamp
	}
	return stamp + "#" + strconv.FormatUint(epoch, 10)
}

// lastEpoch is the epoch a move was read under (markLast); a move with no mark is the first account's.
func lastEpoch(last string) uint64 {
	if i := strings.LastIndexByte(last, '#'); i >= 0 {
		if epoch, err := strconv.ParseUint(last[i+1:], 10, 64); err == nil {
			return epoch
		}
	}
	return 0
}

type epochKey struct{}

// asEpoch binds everything asked under the returned context to the account epoch: a request is refused
// (errAccountChanged) before it is sent once the connector reads as another account, so one poll, one thread page
// or one reply is all one account's or fails. A context bound already keeps its epoch.
func asEpoch(ctx context.Context, epoch uint64) context.Context {
	if _, bound := boundEpoch(ctx); bound {
		return ctx
	}
	return context.WithValue(ctx, epochKey{}, epoch)
}

func boundEpoch(ctx context.Context) (uint64, bool) {
	epoch, ok := ctx.Value(epochKey{}).(uint64)
	return epoch, ok
}
