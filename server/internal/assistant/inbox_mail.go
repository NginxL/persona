package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// ---- mail ----

// collectMail applies a round's mail: each folder's latest page, then the full text of the new mail (the round's
// new items too: they are triaged on it) and of items collected before their text was read.
func (s *Service) collectMail(ctx context.Context, read *assistantwire.InboxMailRead) []*InboxItem {
	b := &s.inbox
	b.readSource("mail", "邮箱")
	var changed []*InboxItem
	for _, a := range read.Accounts {
		now := time.Now()
		changed = append(changed, b.applyMail(a, a.Messages, now)...)
		changed = append(changed, s.settleMailOffPage(ctx, a, now)...)
		readBoxes := map[string]bool{}
		for f := range a.Mailboxes() {
			readBoxes[assistantwire.MailFolderKey(a.ID, f.Name)] = true
		}
		b.mu.Lock()
		var unread []*InboxItem
		b.each(func(it *InboxItem) {
			if it.Mail != nil && it.Mail.AccountID == a.ID && readBoxes[it.Mail.folderKey()] &&
				it.open() && !it.Read && it.BodyOf != it.Last {
				unread = append(unread, it)
			}
		})
		b.mu.Unlock()
		for _, it := range unread {
			if s.readMailBody(ctx, a, it) {
				changed = append(changed, it)
			}
		}
	}
	s.saveMailBaselines(ctx)
	return changed
}

// readMailBody replaces the list's 2 KB snippet with the newest message's text (HTML-only newsletters have nothing
// but CSS in their first bytes). The hub reads it peeking: the mail stays unread.
func (s *Service) readMailBody(ctx context.Context, a assistantwire.InboxMailAccount, it *InboxItem) bool {
	b := &s.inbox
	b.mu.Lock()
	if it.Mail == nil || len(it.Mail.UIDs) == 0 {
		b.mu.Unlock()
		return false
	}
	uid, mailbox, last := it.Mail.UIDs[len(it.Mail.UIDs)-1], it.Mail.Mailbox, it.Last
	b.mu.Unlock()
	m, err := s.inboxMailBody(ctx, assistantwire.InboxMailRef{AccountID: a.ID, UID: uid, Mailbox: mailbox})
	if err != nil {
		s.logf("inbox: read mail %s/%d: %v", a.Email, uid, err)
		return false
	}
	text := strings.TrimSpace(m.Text)
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	b.roundWrite(it)
	it.BodyOf = last
	if text == "" {
		return true
	}
	it.Preview = clip(oneLine(text), 200)
	var to []string
	for _, t := range m.To {
		to = append(to, t.Email)
	}
	var cc []string
	for _, t := range m.Cc {
		cc = append(cc, t.Email)
	}
	it.Context = fmt.Sprintf("邮箱 %s 收到的未读邮件（共 %d 封，下面是最新一封）：\n发件人：%s <%s>\n收件人：%s\n抄送：%s\n时间：%s\n主题：%s\n\n%s",
		a.Email, len(it.Mail.UIDs), m.From.Name, m.From.Email, strings.Join(to, ", "), strings.Join(cc, ", "), m.Date, m.Subject, clip(text, 3000))
	return true
}

// mailReadElsewhere is an open mail item whose mail is all read in another client, gone from the mailbox or past the
// window, under b.mu: read, and done unless it needs the user. An FYI conclusion is still waiting for Got it: the
// source is read, but the card is never filed before that explicit confirmation (#5471).
func (b *inboxBook) mailReadElsewhere(it *InboxItem, now time.Time) {
	b.roundWrite(it)
	it.Read = true
	g := currentSuggestion(it)
	if it.Tier != TierAction && it.Tier != "" && (g == nil || g.Kind != SuggestConclusion) {
		b.file(it, now, "read")
	}
}

// mailAnswered files an open mail item as replied when one of its mails on the page carries IMAP \Answered (the owner
// replied from a mail client) and no unread mail of the thread came after that one, whatever its tier, under b.mu. An
// item its processing conversation handles or that has a suggestion is left to that. Reports whether it filed the item.
func (b *inboxBook) mailAnswered(it *InboxItem, list, unseen []assistantwire.InboxMailSummary, now time.Time) bool {
	if it.Mail == nil || !it.open() || it.handled() || currentSuggestion(it) != nil {
		return false
	}
	var answered uint32
	for _, m := range list {
		if m.Answered && slices.Contains(it.Mail.UIDs, m.UID) {
			answered = max(answered, m.UID)
		}
	}
	evidence := "mail:" + strconv.FormatUint(uint64(answered), 10)
	if answered == 0 || it.SettledOf == evidence || slices.ContainsFunc(unseen, func(m assistantwire.InboxMailSummary) bool { return m.UID > answered }) {
		return false // nothing answered, reopened by hand after being filed on this answer, or newer mail came since
	}
	b.roundWrite(it)
	b.noteSelfReplied(it, now)
	it.Read = true
	it.done(now, "replied")
	it.SettledOf = evidence
	return true
}

var replyPrefix = lazyre.New(`(?i)^\s*((re|fw|fwd|aw|回复|答复|转发)\s*[:：]\s*|\[[^\]]{1,30}\]\s*)+`)

// mailThread is what keeps replies in one item: the subject without Re: / Fwd: / [list] prefixes. A subject that is
// empty once they are gone says nothing about which mails go together: those are grouped by who sent them too, so a
// subject-less mail from one person is not folded under another's (#4852).
func mailThread(subject string, from ...string) string {
	s := strings.ToLower(strings.Join(strings.Fields(replyPrefix.ReplaceAllString(subject, "")), " "))
	if s == "" && len(from) > 0 && strings.TrimSpace(from[0]) != "" {
		s = "\x00from:" + strings.ToLower(strings.TrimSpace(from[0]))
	}
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// applyMail turns one account's latest pages into items: unseen messages grouped by folder and thread, and mail that
// was already read when the inbox first saw it as items read and done (mailReadOnArrival).
func (b *inboxBook) applyMail(a assistantwire.InboxMailAccount, msgs []assistantwire.InboxMailSummary, now time.Time) []*InboxItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	byFolder := map[string][]*InboxItem{}
	b.each(func(it *InboxItem) {
		if it.Mail != nil && it.Mail.AccountID == a.ID {
			key := it.Mail.folderKey()
			byFolder[key] = append(byFolder[key], it)
		}
	})
	var changed []*InboxItem
	a.Messages = msgs
	for f := range a.Mailboxes() {
		key := assistantwire.MailFolderKey(a.ID, f.Name)
		changed = append(changed, b.applyMailFolder(a, f, key, byFolder[key], now)...)
	}
	return changed
}

// UIDs, read-on-arrival baselines and thread comparisons belong to one IMAP mailbox.
func (b *inboxBook) applyMailFolder(a assistantwire.InboxMailAccount, f assistantwire.InboxMailFolder, key string, items []*InboxItem, now time.Time) []*InboxItem {
	baseline, since, generation := b.mailBaseline(key, f)
	source := InboxMail{AccountID: a.ID, Mailbox: f.Name, Generation: generation}
	index := &threadIndex{by: map[string][]*InboxItem{}, key: func(it *InboxItem) (string, bool) {
		return it.Mail.Thread, it.Mail.Generation == generation
	}}
	var changed []*InboxItem
	for _, it := range items {
		if it.Mail.Generation < generation && len(it.Mail.UIDs) != 0 {
			// Old UIDs can now target unrelated mail. Preserve the history and invalidate the targets.
			b.roundWrite(it)
			it.Mail.UIDs = []uint32{}
			if it.open() {
				b.mailReadElsewhere(it, now)
			}
			changed = append(changed, it)
		}
		index.file(it)
	}
	msgs := f.Messages
	superseded, stay := b.supersedeSubjectless(index, msgs, now)
	changed = append(changed, superseded...)
	threads := map[string][]assistantwire.InboxMailSummary{}
	var order []string
	for _, m := range msgs {
		t := mailThread(m.Subject, m.From.Email)
		if stay[m.UID] {
			t = subjectlessThread // the old item that keeps its mail (supersedeSubjectless)
		}
		if _, ok := threads[t]; !ok {
			order = append(order, t)
		}
		threads[t] = append(threads[t], m)
	}
	for _, t := range order {
		list := threads[t]
		sort.Slice(list, func(i, j int) bool { return list[i].UID < list[j].UID })
		var unseen, seen []assistantwire.InboxMailSummary
		for _, m := range list {
			d, _ := time.Parse(time.RFC3339, m.Date)
			switch {
			case now.Sub(d) > inboxWindow:
			case !m.Seen:
				unseen = append(unseen, m)
			case since && m.UID > baseline: // arrived since the account's baseline, already read
				seen = append(seen, m)
			}
		}
		open, done := index.openFor(t)
		if open == nil && done != nil {
			last, _ := strconv.ParseUint(done.Last, 10, 32)
			older := func(m assistantwire.InboxMailSummary) bool { return uint64(m.UID) <= last }
			unseen = slices.DeleteFunc(unseen, older)
			seen = slices.DeleteFunc(seen, older)
		}
		// Mail the thread's other items hold (a done one still unread on the server, an older one moved back) is
		// theirs: it does not come into the open one too, counted twice (#3796).
		held := map[uint32]bool{}
		for _, it := range index.by[t] {
			if k, ok := index.key(it); ok && k == t && it != open {
				for _, uid := range it.Mail.UIDs {
					held[uid] = true
				}
			}
		}
		if len(held) > 0 {
			unseen = slices.DeleteFunc(unseen, func(m assistantwire.InboxMailSummary) bool { return held[m.UID] })
			seen = slices.DeleteFunc(seen, func(m assistantwire.InboxMailSummary) bool { return held[m.UID] })
		}
		if open != nil && b.mailAnswered(open, list, unseen, now) {
			changed = append(changed, open)
			continue
		}
		if len(unseen) == 0 {
			// Read in another client (or past the window): done unless it needs the user (mailReadElsewhere).
			if open != nil && !open.Read {
				b.mailReadElsewhere(open, now)
				changed = append(changed, open)
			} else if open == nil && len(seen) > 0 {
				changed = append(changed, b.mailReadOnArrival(a, source, t, seen, index, now))
			}
			continue
		}
		newest := unseen[len(unseen)-1]
		if open != nil && open.Last == strconv.FormatUint(uint64(newest.UID), 10) && len(open.Mail.UIDs) == len(unseen) {
			continue
		}
		it := open
		var prev inboxSeen
		if it != nil {
			prev = seenOf(it)
			b.roundWrite(it)
		}
		if it == nil {
			it = source.item(t, unseen[0].UID)
			it.StartedAt, _ = time.Parse(time.RFC3339, unseen[0].Date)
			b.add("mail", it)
			b.roundNew(it)
			index.note(it)
		}
		fillMail(it, a, unseen)
		it.Read = false
		it.Context = mailContext(a, unseen, false)
		b.noteReceived(it, prev, mailMessages(unseen, prev.uids))
		changed = append(changed, it)
	}
	return changed
}

func (m *InboxMail) folderKey() string { return assistantwire.MailFolderKey(m.AccountID, m.Mailbox) }

func (m InboxMail) item(thread string, first uint32) *InboxItem {
	scope := m.folderKey()
	if m.Generation != 0 {
		scope += fmt.Sprintf(":e:%d", m.Generation)
	}
	m.Thread = thread
	return &InboxItem{ID: fmt.Sprintf("mail:%s:%s:%d", scope, thread, first), Source: "mail", Kind: KindMail,
		Account: m.AccountID, Mail: &m}
}

// fillMail sets what a mail item shows and holds from its mails (oldest first, at least one).
func fillMail(it *InboxItem, a assistantwire.InboxMailAccount, msgs []assistantwire.InboxMailSummary) {
	newest := msgs[len(msgs)-1]
	it.AccountName = a.Email
	it.Title = newest.Subject
	if it.Title == "" {
		it.Title = "（无主题）"
	}
	it.Sender = newest.From.Name
	if it.Sender == "" {
		it.Sender = newest.From.Email
	}
	it.Preview = clip(oneLine(newest.Snippet), 200)
	it.Count = len(msgs)
	it.UpdatedAt, _ = time.Parse(time.RFC3339, newest.Date)
	it.Last = strconv.FormatUint(uint64(newest.UID), 10)
	it.Mail.UIDs = it.Mail.UIDs[:0]
	for _, m := range msgs {
		it.Mail.UIDs = append(it.Mail.UIDs, m.UID)
	}
	it.Mail.MessageID, it.Mail.From = newest.MessageID, newest.From
	it.Mail.Direct = false
	for _, to := range newest.To {
		if strings.EqualFold(to.Email, a.Email) {
			it.Mail.Direct = true
		}
	}
}

// mailReadOnArrival files a thread's mail that was already read when the inbox first saw it (read in another client,
// or marked read by a forwarding rule, between two rounds) as one item, read and done (#8298): Done lists it, so the
// mail is never missing from the inbox, but it is no push, no unread and nothing in "Needs you". No model is asked
// about it: it has its rule tier, as filed. Under b.mu.
func (b *inboxBook) mailReadOnArrival(a assistantwire.InboxMailAccount, source InboxMail, t string, seen []assistantwire.InboxMailSummary,
	index *threadIndex, now time.Time) *InboxItem {
	it := source.item(t, seen[0].UID)
	it.StartedAt, _ = time.Parse(time.RFC3339, seen[0].Date)
	b.add("mail", it)
	b.roundNew(it)
	index.note(it)
	fillMail(it, a, seen)
	it.Read = true
	it.Context = mailContext(a, seen, true)
	it.Tier, it.Reason = b.ruleTier(it), b.byRules()
	it.TriagedAt, it.TriageRev = it.Last, inboxTriageRev
	b.noteReceived(it, inboxSeen{}, mailMessages(seen, nil))
	b.file(it, now, "read")
	return it
}

// inboxMailBaselinesKey keeps each mailbox's first-page baseline, last known UIDVALIDITY (#v) and generation
// after renumbering (#e). An old account-only entry remains its INBOX baseline.
const inboxMailBaselinesKey = "inbox_mail_baselines"

// mailBaseline records an empty folder too: read mail delivered by a rule after that first visit should be filed.
func (b *inboxBook) mailBaseline(key string, f assistantwire.InboxMailFolder) (baseline uint32, since bool, generation uint32) {
	if b.mailBaselines == nil {
		b.mailBaselines = map[string]uint32{}
	}
	validity := b.mailBaselines[key+"#v"]
	renumbered := false
	if f.UIDValidity != 0 && validity != f.UIDValidity {
		b.mailBaselines[key+"#v"] = f.UIDValidity
		b.mailBaselinesDirty = true
		if validity != 0 {
			renumbered = true
			b.mailBaselines[key+"#e"]++
		}
	}
	generation = b.mailBaselines[key+"#e"]
	var newest uint32
	for _, m := range f.Messages {
		newest = max(newest, m.UID)
	}
	baseline, since = b.mailBaselines[key]
	if since && !renumbered && (newest >= baseline || validity != 0 && f.UIDValidity != 0) {
		return baseline, true, generation
	}
	// When either validity is unknown (including an upgrade), retain the original lower-page heuristic.
	b.mailBaselines[key] = newest
	b.mailBaselinesDirty = true
	return newest, false, generation
}

// loadMailBaselines reads the mail accounts' baselines kept; false when the database could not be read.
func (s *Service) loadMailBaselines(ctx context.Context) bool {
	st := s.store()
	if st == nil {
		return true
	}
	var kept map[string]uint32
	if _, err := st.Setting(ctx, inboxMailBaselinesKey, &kept); err != nil {
		s.logf("inbox: mail baselines: %v", err)
		return false
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	if kept == nil {
		kept = map[string]uint32{}
	}
	maps.Copy(kept, b.mailBaselines) // set by a round before the load: memory is newer
	b.mailBaselines = kept
	return true
}

// saveMailBaselines stores the baselines when a round set one; one that could not be written is tried again after the
// next round (until then a restart sets it again, a little later).
func (s *Service) saveMailBaselines(ctx context.Context) {
	b := &s.inbox
	b.mu.Lock()
	if !b.mailBaselinesDirty {
		b.mu.Unlock()
		return
	}
	b.mailBaselinesDirty = false
	kept := maps.Clone(b.mailBaselines)
	b.mu.Unlock()
	st := s.store()
	if st == nil {
		return
	}
	if err := st.PutSetting(ctx, inboxMailBaselinesKey, kept); err != nil {
		s.logf("inbox: mail baselines: %v", err)
		b.mu.Lock()
		b.mailBaselinesDirty = true
		b.mu.Unlock()
	}
}

// subjectlessThread is the key every subject-less mail had before #4852 grouped them by sender too.
var subjectlessThread = mailThread("")

// supersedeSubjectless settles an open item from before #4852 that holds subject-less mail of several senders under
// the empty subject's key, under b.mu: no round matches it by its senders any more, so each mail is listed once
// (#3796, #4920).
//
//   - One the owner has to act on (Needs you) or the AI twin worked on keeps its mail as it was (stay: those UIDs stay under
//     the empty subject's key), with its tier and draft; it is read and filed like any item. A sender item a round
//     since #4852 opened with only that mail is filed as superseded.
//   - Any other is filed as superseded once every unread mail of it on the page has a sender (each goes into its
//     sender's item); while some have none it stays open for those, and the round keeps them in it.
//   - When all of its mail on the page is read elsewhere it is read like any item; while none is on the page it is
//     left alone.
//
// An item the owner moved back (Move back) is filed as superseded again the same way.
func (b *inboxBook) supersedeSubjectless(index *threadIndex, msgs []assistantwire.InboxMailSummary, now time.Time) ([]*InboxItem, map[uint32]bool) {
	open, _ := index.openFor(subjectlessThread)
	if open == nil {
		return nil, nil
	}
	byUID := make(map[uint32]assistantwire.InboxMailSummary, len(msgs))
	for _, m := range msgs {
		byUID[m.UID] = m
	}
	if open.Tier == TierAction || open.Agent != nil {
		stay := map[uint32]bool{}
		for _, uid := range open.Mail.UIDs {
			stay[uid] = true
		}
		// A hub that already ran #4852 opened sender items for the same mail: one holding only mail the old item
		// keeps would list it a second time and no round touches it again, so it is filed. One with other mail too
		// keeps only that (the round lists its thread's mail, without these); one the owner has to act on, or with a
		// draft, is left as it is.
		var filed []*InboxItem
		for _, it := range b.items {
			if it == open || !it.open() || it.Mail == nil || it.Mail.folderKey() != open.Mail.folderKey() || it.Mail.Generation != open.Mail.Generation || it.Mail.Thread == subjectlessThread ||
				it.Tier == TierAction || it.Agent != nil || len(it.Mail.UIDs) == 0 ||
				slices.ContainsFunc(it.Mail.UIDs, func(uid uint32) bool { return !stay[uid] }) {
				continue
			}
			b.roundWrite(it)
			it.done(now, "superseded")
			filed = append(filed, it)
		}
		return filed, stay
	}
	onPage, seen, moved, remain := 0, 0, 0, 0
	for _, uid := range open.Mail.UIDs {
		m, ok := byUID[uid]
		switch {
		case !ok:
			continue
		case m.Seen:
			seen++
		case strings.TrimSpace(m.From.Email) != "" && mailThread(m.Subject, m.From.Email) != subjectlessThread:
			moved++
		default:
			remain++
		}
		onPage++
	}
	switch {
	case moved > 0 && remain == 0:
		b.roundWrite(open)
		open.done(now, "superseded")
	case onPage > 0 && seen == onPage && !open.Read:
		b.roundWrite(open)
		open.Read = true
		if open.Tier != "" {
			open.done(now, "read")
		}
	default:
		return nil, nil // nothing on the page, or mail without a sender that the round keeps in it
	}
	return []*InboxItem{open}, nil
}

// mailMessages are the mails of a batch (those not in the item before) for its received entry.
func mailMessages(unseen []assistantwire.InboxMailSummary, had []uint32) []InboxChainMessage {
	var out []InboxChainMessage
	for _, m := range unseen {
		if slices.Contains(had, m.UID) {
			continue
		}
		sender := m.From.Name
		if sender == "" {
			sender = m.From.Email
		}
		msg := InboxChainMessage{Sender: sender, Text: clip(oneLine(m.Snippet), chainMessageText)}
		if d, err := time.Parse(time.RFC3339, m.Date); err == nil {
			d = d.UTC()
			msg.At = &d
		}
		out = append(out, msg)
	}
	return out
}

// mailContext is the item's mails as the models read them; read: they were already read when they came.
func mailContext(a assistantwire.InboxMailAccount, msgs []assistantwire.InboxMailSummary, read bool) string {
	var b strings.Builder
	head := "邮箱 %s 收到的未读邮件：\n"
	if read {
		head = "邮箱 %s 收到的邮件（来时已读）：\n" // i18n-ignore: text for a model
	}
	fmt.Fprintf(&b, head, a.Email)
	for _, m := range msgs {
		var to []string
		for _, t := range m.To {
			to = append(to, t.Email)
		}
		fmt.Fprintf(&b, "---\n发件人：%s <%s>\n收件人：%s\n时间：%s\n主题：%s\n%s\n", m.From.Name, m.From.Email, strings.Join(to, ", "),
			m.Date, m.Subject, clip(oneLine(m.Snippet), inboxText))
	}
	return strings.TrimSpace(b.String())
}
