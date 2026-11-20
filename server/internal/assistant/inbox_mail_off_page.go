package assistant

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A mail round reads each eligible mailbox's latest page (extmgr readMailAccount) and applyMail settles the threads on
// it. An open item whose mail is on no page any more (archived or deleted in the mail client, or pushed off by newer
// mail) would never be looked at again: neither read elsewhere nor past the window would file it, and the open side of
// the list would only grow (#5993). settleMailOffPage gives those items the same rules:
//
//   - past the window (its newest mail older than inboxWindow): as a mail past the window on the page;
//   - else the hub is asked once a round which of their mails are still in the mailbox, read or not
//     (inbox.mail_flags, at most assistantwire.InboxMailFlagsMost of them, the longest unasked first): an item whose
//     mail is all read or gone is read elsewhere; one with an unread mail still there stays as it is, and so does
//     every item when the hub can't say.
//
// Both end in mailReadElsewhere: an item that needs the user (Needs you, an FYI conclusion waiting for Got it) is only
// marked read, as on the page.

// mailOffPageAsk is one item's mail asked about: the item as it was when asked.
type mailOffPageAsk struct {
	it      *InboxItem
	last    string
	mailbox string
	uids    []uint32
}

// settleMailOffPage settles account a's open mail items off the page a round just applied (applyMail), at now.
func (s *Service) settleMailOffPage(ctx context.Context, a assistantwire.InboxMailAccount, now time.Time) []*InboxItem {
	b := &s.inbox
	changed, asks := b.mailOffPage(a, now)
	if len(asks) == 0 {
		return changed
	}
	byBox := map[string][]uint32{}
	var boxes []string
	for _, ask := range asks {
		if _, ok := byBox[ask.mailbox]; !ok {
			boxes = append(boxes, ask.mailbox)
		}
		byBox[ask.mailbox] = append(byBox[ask.mailbox], ask.uids...)
	}
	state := map[string]map[uint32]bool{} // mailbox → uid → seen; a mailbox the hub could not say about is missing
	for _, box := range boxes {
		var flags assistantwire.InboxMailFlags
		err := s.appCall(ctx, AppInbox, assistantwire.InboxMailFlagsMethod,
			assistantwire.InboxMailFlagsAsk{AccountID: a.ID, Mailbox: box, UIDs: byBox[box]}, &flags)
		if err != nil {
			s.logf("inbox: mail %s off the page: %v", a.Email, err)
			continue
		}
		m := map[uint32]bool{}
		for _, uid := range flags.Seen {
			m[uid] = true
		}
		for _, uid := range flags.Unseen {
			m[uid] = false
		}
		state[box] = m
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	for _, ask := range asks {
		m, ok := state[ask.mailbox]
		it := ask.it
		// Asked about as it was: one that changed meanwhile (done by hand, new mail) is the next round's.
		if !ok || b.items[it.ID] != it || !it.open() || it.Read || it.Last != ask.last || !slices.Equal(it.Mail.UIDs, ask.uids) {
			continue
		}
		if slices.ContainsFunc(ask.uids, func(uid uint32) bool { seen, there := m[uid]; return there && !seen }) {
			continue // still unread in the mailbox: it stays until it is read, gone or past the window
		}
		b.mailReadElsewhere(it, now)
		changed = append(changed, it)
	}
	return changed
}

// mailOffPage is account a's open, unread mail items with no mail on their folder's page (by thread or by mail), under
// b.mu: those past the window are read elsewhere at once (changed), the others are what to ask the hub about, the
// longest unasked first, as many as fit in one ask.
func (b *inboxBook) mailOffPage(a assistantwire.InboxMailAccount, now time.Time) ([]*InboxItem, []mailOffPageAsk) {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	type page struct {
		threads map[string]bool
		uids    map[uint32]bool
	}
	pages := map[string]page{}
	for f := range a.Mailboxes() {
		p := page{threads: map[string]bool{}, uids: map[uint32]bool{}}
		for _, m := range f.Messages {
			p.threads[mailThread(m.Subject, m.From.Email)] = true
			p.uids[m.UID] = true
		}
		pages[assistantwire.MailFolderKey(a.ID, f.Name)] = p
	}
	for id := range b.mailAsked { // asked about before, and settled since
		if it := b.items[id]; it == nil || !it.open() || it.Read {
			delete(b.mailAsked, id)
		}
	}
	var changed []*InboxItem
	var asks []mailOffPageAsk
	for _, it := range b.items {
		if it.Mail == nil || it.Mail.AccountID != a.ID || !it.open() || it.Read || len(it.Mail.UIDs) == 0 {
			continue
		}
		p, ok := pages[it.Mail.folderKey()]
		if !ok || p.threads[it.Mail.Thread] || slices.ContainsFunc(it.Mail.UIDs, func(uid uint32) bool { return p.uids[uid] }) {
			continue // skip unreadable folders and mail already settled on its own page
		}
		if now.Sub(it.UpdatedAt) > inboxWindow {
			b.mailReadElsewhere(it, now)
			changed = append(changed, it)
			continue
		}
		asks = append(asks, mailOffPageAsk{it: it, last: it.Last, mailbox: it.Mail.Mailbox, uids: slices.Clone(it.Mail.UIDs)})
	}
	sort.SliceStable(asks, func(i, j int) bool {
		ai, aj := b.mailAsked[asks[i].it.ID], b.mailAsked[asks[j].it.ID]
		if !ai.Equal(aj) {
			return ai.Before(aj)
		}
		return asks[i].it.ID < asks[j].it.ID
	})
	room := assistantwire.InboxMailFlagsMost
	kept := asks[:0]
	for _, ask := range asks {
		if len(ask.uids) > room {
			continue
		}
		room -= len(ask.uids)
		kept = append(kept, ask)
		if b.mailAsked == nil {
			b.mailAsked = map[string]time.Time{}
		}
		b.mailAsked[ask.it.ID] = now
	}
	return changed, kept
}
