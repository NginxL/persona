package extmgr

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/mail"
	"github.com/nwverse/persona/server/internal/rpc"
)

// readInbox reads one round's sources: mail on a full round, the connectors but on a round of Feishu chats.
func (h *Manager) readInbox(ctx context.Context, round inboxRound) assistantwire.InboxRound {
	r := assistantwire.InboxRound{Round: round.wire(), Sources: []assistantwire.InboxSource{}, Names: map[string]string{}}
	if h.connectors == nil {
		return r
	}
	for _, c := range h.connectors.All() {
		r.Names[c.ID()] = connector.DisplayName(ctx, c)
	}
	if round == roundFull {
		r.Mail = h.readMail(ctx)
	}
	if round != roundChats {
		r.Sources = h.readConnectors(ctx)
	}
	h.readSubscriptions(ctx, &r, round != roundChats)
	return r
}

// readMail is each mail account's latest inbox page, read up to inboxMailParallel accounts at once and kept in the
// accounts' order; an account that can't be read is left out and named in Failed with why (the manager keeps what it
// had and the apps say it is behind). nil without mail or when the accounts can't be listed.
func (h *Manager) readMail(ctx context.Context) *assistantwire.InboxMailRead {
	// Add to Inbox off (#5307): no mail round, so the manager keeps the mail items it has; search, replies and the
	// knowledge base go on.
	if h.mail == nil || h.connectors.Get("mail") == nil || !h.connectors.Feeds("mail", connector.FeedInbox) {
		return nil
	}
	accounts, err := h.mail.ListAccounts()
	if err != nil {
		h.logf("inbox: mail accounts: %v", err)
		return nil
	}
	read := make([]*assistantwire.InboxMailAccount, len(accounts))
	errs := make([]error, len(accounts))
	slots := make(chan struct{}, inboxMailParallel)
	var wg sync.WaitGroup
	for i, a := range accounts {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			read[i], errs[i] = h.readMailAccount(ctx, a)
		})
	}
	wg.Wait()
	out := &assistantwire.InboxMailRead{Accounts: []assistantwire.InboxMailAccount{}}
	var health map[string]string // the accounts' status after the round (mail settings'), read once one failed
	checked := false
	now := time.Now()
	for i, acc := range read {
		if acc != nil {
			out.Accounts = append(out.Accounts, *acc)
			h.inboxReads.note("mail:"+accounts[i].ID, now, "")
			continue
		}
		code := mailReadErrorOf(errs[i])
		if code != assistantwire.ReadTimeout {
			if !checked {
				health, checked = h.mailHealth(), true
			}
			if health != nil {
				status, there := health[accounts[i].ID]
				if !there {
					continue // deleted meanwhile: nothing to say of it
				}
				if status == "error" {
					code = assistantwire.ReadLoginRefused // mail settings show why
				}
			}
		}
		out.Failed = append(out.Failed, assistantwire.InboxMailFailed{ID: accounts[i].ID, Email: accounts[i].Email, ReadError: code})
		h.inboxReads.note("mail:"+accounts[i].ID, now, code)
	}
	return out
}

// readMailAccount is one account's latest inbox page; nil and why when it can't be read.
func (h *Manager) readMailAccount(ctx context.Context, a mail.MailAccount) (*assistantwire.InboxMailAccount, error) {
	res, err := h.mail.ListMessages(ctx, mail.ListOptions{AccountID: a.ID, Limit: 40})
	if err != nil {
		h.logf("inbox: mail %s: %v", a.Email, err)
		return nil, err
	}
	acc := &assistantwire.InboxMailAccount{ID: a.ID, Email: a.Email, Messages: []assistantwire.InboxMailSummary{}}
	for _, m := range res.Messages {
		acc.Messages = append(acc.Messages, assistantwire.InboxMailSummary{Mailbox: m.Mailbox, UID: m.UID, MessageID: m.MessageID,
			Subject: m.Subject, From: mailAddress(m.From), To: mailAddresses(m.To), Date: m.Date, Snippet: m.Snippet, Seen: m.Seen, Answered: m.Answered})
	}
	return acc, nil
}

// mailHealth is each mail account's status by id as mail settings show it ("error": its server refuses the login;
// a failed read just now has set it); nil when the accounts can't be listed.
func (h *Manager) mailHealth() map[string]string {
	accounts, err := h.mail.ListAccounts()
	if err != nil {
		h.logf("inbox: mail accounts: %v", err)
		return nil
	}
	out := map[string]string{}
	for _, a := range accounts {
		out[a.ID] = a.Status
	}
	return out
}

// mailReadErrorOf is a failed read of a mail account as a short code, as readErrorOf: the mail service's own
// deadline and the network's are timeouts too.
func mailReadErrorOf(err error) string {
	var ne net.Error
	if errors.Is(err, mail.ErrTimedOut) || errors.As(err, &ne) && ne.Timeout() {
		return assistantwire.ReadTimeout
	}
	return readErrorOf(err)
}

func mailAddress(a mail.Address) assistantwire.MailAddress {
	return assistantwire.MailAddress{Name: a.Name, Email: a.Email}
}

func mailAddresses(list []mail.Address) []assistantwire.MailAddress {
	out := []assistantwire.MailAddress{}
	for _, a := range list {
		out = append(out, mailAddress(a))
	}
	return out
}

// readConnectors reads every connector with an inbox: its things and what it can do with them. One the user turned "Add
// to Inbox" off for, not signed in, or a hub.json plugin's waiting for the triage model is read with nothing (nothing
// of it waits); one whose Collect failed is left out (it keeps what it had). One whose Inbox feed was switched to its
// default subscription is not read at all: the subscription hands its rows under its id (#6265).
func (h *Manager) readConnectors(ctx context.Context) []assistantwire.InboxSource {
	out := []assistantwire.InboxSource{}
	h.reconcileIMessageAI(ctx)
	migrated := map[string]bool{}
	if subs := h.subsLive.Load(); subs != nil {
		var err error
		if migrated, err = subs.Migrated(ctx); err != nil {
			h.logf("inbox: migrated sources: %v", err)
			return out // which built-ins to read is not known: none this round, rather than one twice
		}
	}
	for _, c := range h.connectors.All() {
		if migrated[c.ID()] {
			continue
		}
		in, ok := connector.As[connector.Inbox](c)
		if !ok {
			continue
		}
		src := assistantwire.InboxSource{ID: c.ID(), Name: connector.DisplayName(ctx, c), Extension: fromExtension(c.ID()), Migrated: isMigrated(c.ID()),
			Things: []connector.InboxThing{}}
		state := connector.StatusOf(ctx, c).State
		if !h.connectors.Feeds(c.ID(), connector.FeedInbox) || state != connector.StateOK && state != connector.StateRateLimited ||
			h.waitsForTriageModel(ctx, c) { // a hub.json plugin's that needs the triage model (extension_inbox.go)
			src.Off = true
			out = append(out, src)
			continue
		}
		things, err := in.Collect(ctx)
		if err != nil {
			h.logf("inbox: %s: %v", c.ID(), err)
			src.ReadError = readErrorOf(err) // the manager keeps what it had and the apps say it is behind
			h.inboxReads.note(c.ID(), time.Now(), src.ReadError)
			out = append(out, src)
			continue
		}
		h.inboxReads.note(c.ID(), time.Now(), "")
		h.vetThings(c, things)
		src.Things = things
		withAbilities(&src, c)
		out = append(out, src)
	}
	return out
}

// withAbilities gives a connector's round entry what the Inbox does with its things besides taking them: grouping
// (group, meta), settling, and its kinds as it describes them. A built-in read through its default subscription
// gets the same (#6381), so nothing it does is lost at the switch.
func withAbilities(src *assistantwire.InboxSource, c connector.Connector) {
	_, src.Group = connector.As[connector.Grouper](c)
	_, src.Meta = connector.As[connector.EventWriter](c)
	src.MetaDescription = strings.TrimSpace(c.Manifest().MetaDescription)
	_, src.Settles = connector.As[connector.InboxSettler](c)
	for _, kind := range c.Manifest().Kinds {
		if d, ok := extensionKinds.Load(c.ID() + "/" + kind); ok {
			if src.Kinds == nil {
				src.Kinds = map[string]string{}
			}
			src.Kinds[kind] = d.(string)
		}
	}
}

// readErrorOf is a failed Collect as a short code: its text may hold an address or a token, the code does not.
func readErrorOf(err error) string {
	var re *rpc.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return assistantwire.ReadTimeout
	case errors.As(err, &re) && re.Code == "rate_limited":
		return assistantwire.ReadRateLimited
	}
	return assistantwire.ReadFailed
}

func isMigrated(id string) bool { return slices.Contains(connector.MigratableConnectors, id) }

// thingRulesMost is the most of a thing's rules the model gets (runes; the rest is cut): what the convention's
// triage_rules setting allows (docs/extensions/extensions.md "Conventional settings").
const thingRulesMost = 4000

// vetThings holds a connector's things to its manifest: a tier the inbox does not know is dropped (the model
// decides), a kind it did not declare is logged once (prompts/inbox/kinds/<id>.md may not say what it means); rules
// are cut to thingRulesMost.
func (h *Manager) vetThings(c connector.Connector, things []connector.InboxThing) {
	kinds := c.Manifest().Kinds
	for i := range things {
		t := &things[i]
		t.Rules = clip(strings.TrimSpace(t.Rules), thingRulesMost)
		if t.Tier != "" && !t.Tier.Known() {
			h.logf("inbox: %s: unknown tier %q", c.ID(), t.Tier)
			t.Tier = ""
		}
		if t.Kind != "" && !slices.Contains(kinds, t.Kind) {
			if _, logged := undeclaredKinds.LoadOrStore(c.ID()+"\x00"+t.Kind, true); !logged {
				h.logf("inbox: %s: kind %q is not in its manifest", c.ID(), t.Kind)
			}
		}
	}
}

var undeclaredKinds sync.Map
