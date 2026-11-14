package mail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// localScan bounds one page of a local header search (searchLocally).
type localScan struct {
	batch  int // envelopes per FETCH: small, since one batch of old mail can take seconds (NetEase 163)
	budget int // messages looked at per page
	// time is how long a page may search, counted from the start of its IMAP work (SELECT and the server's own
	// search included), so the matches can still be fetched inside networkTimeout.
	time time.Duration
}

var defaultLocalScan = localScan{batch: 100, budget: 2000, time: 6 * time.Second}

// pageFetch bounds the summary fetch of one page (listOne).
type pageFetch struct {
	batch   int           // summaries per FETCH
	reserve time.Duration // no further batch once the operation has less than this left
}

var defaultPageFetch = pageFetch{batch: 50, reserve: 6 * time.Second}

// pageFetchLimits is s.fetch with the defaults filled in (a zero Service has none).
func (s *Service) pageFetchLimits() pageFetch {
	l := s.fetch
	if l.batch <= 0 {
		l.batch = defaultPageFetch.batch
	}
	if l.reserve <= 0 {
		l.reserve = defaultPageFetch.reserve
	}
	return l
}

// ListMessages lists messages of one mailbox, or of every account's inbox when
// opts.AccountID is empty.
func (s *Service) ListMessages(ctx context.Context, opts ListOptions) (*ListResult, error) {
	limit := opts.Limit
	switch {
	case limit <= 0:
		limit = defaultListLimit
	case limit > maxListLimit:
		limit = maxListLimit
	}
	mailbox := strings.TrimSpace(opts.Mailbox)
	if mailbox == "" {
		mailbox = "INBOX"
	}

	if opts.AccountID != "" {
		a, err := s.account(opts.AccountID)
		if err != nil {
			return nil, err
		}
		return s.listOne(ctx, a, mailbox, limit, opts.BeforeUID, opts.Query)
	}

	s.mu.Lock()
	accounts := s.snapshotLocked()
	s.mu.Unlock()

	type result struct {
		res *ListResult
		err error
	}
	results := make([]result, len(accounts))
	var wg sync.WaitGroup
	for i, a := range accounts {
		wg.Add(1)
		go func(i int, a *MailAccount) {
			defer wg.Done()
			res, err := s.listOne(ctx, a, "INBOX", limit, opts.BeforeUID, opts.Query)
			results[i] = result{res, err}
		}(i, a)
	}
	wg.Wait()
	// A caller that gave up must not receive another account's completed page as a success.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var all []MailSummary
	hasMore, headersOnly := false, false
	for _, r := range results {
		if r.err != nil {
			// Never expose an incomplete merged page as a success, including to older clients which do not
			// understand partial-result metadata. Upstream error text can contain account or credential data.
			return nil, &IncompleteSearchError{}
		}
		all = append(all, r.res.Messages...)
		hasMore = hasMore || r.res.HasMore
		headersOnly = headersOnly || r.res.HeadersOnly
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date > all[j].Date // RFC3339 sorts lexicographically
		}
		return all[i].UID > all[j].UID
	})
	if len(all) > limit {
		all = all[:limit]
		hasMore = true
	}
	// No NextBeforeUID here: UIDs belong to one mailbox, so there is no single place to continue.
	return &ListResult{Messages: all, HasMore: hasMore, HeadersOnly: headersOnly}, nil
}

// uidSearch is what a search found: the matching UIDs, ascending.
type uidSearch struct {
	uids []imap.UID
	// scannedTo is set when a local search stopped before the oldest message: it is the lowest UID looked at, and
	// the messages below it have not been looked at yet.
	scannedTo imap.UID
	// headersOnly: the server found nothing, so the headers were matched here (searchLocally).
	headersOnly bool
}

// searchUIDs finds the messages below beforeUID that match query (all of them for an empty query). want is how many
// matches the caller can use (a page and one more); only a local search stops early on it, or at the page's
// deadline (localScan.time after started, when the page's IMAP work began).
func (s *Service) searchUIDs(ctx context.Context, c *imapclient.Client, a *MailAccount, mailbox string, beforeUID uint32, query string, want int, started time.Time) (uidSearch, error) {
	if query != "" && a.Provider == ProviderGmail {
		// Gmail query syntax needs X-GM-RAW, which the IMAP client library
		// cannot express; use a short-lived raw connection.
		if uids, err := s.gmailSearch(ctx, a, mailbox, query, beforeUID); err == nil {
			return uidSearch{uids: uids}, nil
		} else {
			logf("mail: X-GM-RAW search failed, falling back to TEXT search: %v", err)
		}
	}
	criteria := belowUID(beforeUID)
	if query != "" {
		criteria.Text = append(criteria.Text, query)
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if query != "" {
		// Not every server searches on its side: NetEase 163 answers every keyword SEARCH (TEXT, BODY, SUBJECT,
		// FROM, any language) with nothing, even for a word in the subject; others refuse some queries with NO.
		// Match the headers here then. A server that does search and found nothing costs one bounded scan.
		var ie *imap.Error
		switch {
		case err == nil && len(data.AllUIDs()) == 0:
			return s.searchLocally(c, beforeUID, query, want, started)
		case errors.As(err, &ie):
			logf("mail: keyword search refused, matching headers locally: %v", err)
			return s.searchLocally(c, beforeUID, query, want, started)
		}
	}
	if err != nil {
		return uidSearch{}, err
	}
	uids := data.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uidSearch{uids: uids}, nil
}

// belowUID selects the messages with a UID lower than beforeUID (all of them for 0).
func belowUID(beforeUID uint32) *imap.SearchCriteria {
	criteria := &imap.SearchCriteria{}
	if beforeUID > 1 {
		var set imap.UIDSet
		set.AddRange(1, imap.UID(beforeUID-1))
		criteria.UID = append(criteria.UID, set)
	}
	return criteria
}

// localScanLimits is s.scan with the defaults filled in (a zero Service has none).
func (s *Service) localScanLimits() localScan {
	l := s.scan
	if l.batch <= 0 {
		l.batch = defaultLocalScan.batch
	}
	if l.budget <= 0 {
		l.budget = defaultLocalScan.budget
	}
	if l.time <= 0 {
		l.time = defaultLocalScan.time
	}
	return l
}

// searchLocally matches query against the envelopes of the messages below beforeUID, newest first, in batches. It
// stops once it has want matches or when the page's budget (messages looked at, time) is spent; scannedTo then says
// where the next page continues.
func (s *Service) searchLocally(c *imapclient.Client, beforeUID uint32, query string, want int, started time.Time) (uidSearch, error) {
	data, err := c.UIDSearch(belowUID(beforeUID), nil).Wait()
	if err != nil {
		return uidSearch{}, err
	}
	all := data.AllUIDs()
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	limits := s.localScanLimits()
	terms := strings.Fields(strings.ToLower(query))
	out := uidSearch{headersOnly: true}
	deadline := started.Add(limits.time)
	looked := 0
	end := len(all) // all[:end] is still to look at
	// The budget is checked after each batch, so every page looks at one batch at least and moves on.
	for end > 0 && len(out.uids) < want {
		n := min(limits.batch, limits.budget-looked, end)
		var set imap.UIDSet
		set.AddNum(all[end-n : end]...)
		msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true}).Collect()
		if err != nil {
			return uidSearch{}, fmt.Errorf("fetch envelopes: %w", err)
		}
		for _, m := range msgs {
			if headerMatch(m.Envelope, terms) {
				out.uids = append(out.uids, m.UID)
			}
		}
		looked += n
		end -= n
		if looked >= limits.budget || time.Now().After(deadline) {
			break
		}
	}
	if end > 0 {
		out.scannedTo = all[end] // the lowest UID looked at
	}
	sort.Slice(out.uids, func(i, j int) bool { return out.uids[i] < out.uids[j] })
	return out, nil
}

// headerMatch reports whether every term occurs, ignoring case, in the subject or in the name or address of a
// sender or recipient.
func headerMatch(env *imap.Envelope, terms []string) bool {
	if env == nil || len(terms) == 0 {
		return false
	}
	var b strings.Builder
	b.WriteString(env.Subject)
	for _, list := range [][]imap.Address{env.From, env.Sender, env.ReplyTo, env.To, env.Cc} {
		for _, addr := range list {
			b.WriteByte('\n')
			b.WriteString(addr.Name)
			b.WriteByte(' ')
			b.WriteString(addr.Addr())
		}
	}
	hay := strings.ToLower(b.String())
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// listOne pages one mailbox of one account.
func (s *Service) listOne(ctx context.Context, a *MailAccount, mailbox string, limit int, beforeUID uint32, query string) (*ListResult, error) {
	var out *ListResult
	err := s.withConnAt(ctx, a, func(c *imapclient.Client, deadline time.Time) error {
		out = nil
		// When the operation began, connecting included (a cold login to a slow server takes seconds), and not again
		// on a retry: the page's time counts from here. A sooner context deadline makes it earlier, which is safe.
		started := deadline.Add(-networkTimeout)
		sel, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("select %s: %w", mailbox, err)
		}
		found, err := s.searchUIDs(ctx, c, a, mailbox, beforeUID, query, limit+1, started)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}
		uids := found.uids
		res := &ListResult{Messages: []MailSummary{}, HeadersOnly: found.headersOnly, UIDValidity: sel.UIDValidity}
		switch {
		case len(uids) > limit:
			uids = uids[len(uids)-limit:]
			res.HasMore, res.NextBeforeUID = true, uint32(uids[0])
		case found.scannedTo > 0:
			// A local search ran out of its page's budget: carry on below the last message it looked at.
			res.HasMore, res.NextBeforeUID = true, uint32(found.scannedTo)
		}
		if len(uids) == 0 {
			out = res
			return nil
		}

		// Newest first, a batch at a time: a slow server (NetEase 163 takes seconds for a batch of old mail) could
		// otherwise run the page past its deadline. Once little time is left the page ends here and the rest comes
		// with the next one.
		fetch := s.pageFetchLimits()
		sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
		for i := 0; i < len(uids); i += fetch.batch {
			if i > 0 && time.Now().After(deadline.Add(-fetch.reserve)) {
				res.HasMore, res.NextBeforeUID = true, uint32(uids[i-1])
				break
			}
			var set imap.UIDSet
			set.AddNum(uids[i:min(i+fetch.batch, len(uids))]...)
			msgs, err := c.Fetch(set, &imap.FetchOptions{
				UID:           true,
				Envelope:      true,
				Flags:         true,
				InternalDate:  true,
				RFC822Size:    true,
				BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
				BodySection: []*imap.FetchItemBodySection{
					{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Content-Type", "Content-Transfer-Encoding"}, Peek: true},
					{Specifier: imap.PartSpecifierText, Partial: &imap.SectionPartial{Offset: 0, Size: snippetBytes}, Peek: true},
				},
			}).Collect()
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			for _, m := range msgs {
				res.Messages = append(res.Messages, s.summaryFrom(a, mailbox, m))
			}
		}
		sort.SliceStable(res.Messages, func(i, j int) bool {
			return res.Messages[i].UID > res.Messages[j].UID
		})
		out = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// summaryFrom converts one fetched message into a summary row.
func (s *Service) summaryFrom(a *MailAccount, mailbox string, m *imapclient.FetchMessageBuffer) MailSummary {
	sum := MailSummary{
		AccountID: a.ID,
		Mailbox:   mailbox,
		UID:       uint32(m.UID),
		Seen:      hasFlag(m.Flags, imap.FlagSeen),
		Answered:  hasFlag(m.Flags, imap.FlagAnswered),
		Flagged:   hasFlag(m.Flags, imap.FlagFlagged),
	}
	if m.Envelope != nil {
		sum.Subject = m.Envelope.Subject
		sum.MessageID = m.Envelope.MessageID
		sum.From = firstAddress(m.Envelope.From)
		for _, addr := range m.Envelope.To {
			if e := addr.Addr(); e != "" {
				sum.To = append(sum.To, Address{Name: addr.Name, Email: e})
			}
		}
		date := m.Envelope.Date
		if date.IsZero() {
			date = m.InternalDate
		}
		if !date.IsZero() {
			sum.Date = date.UTC().Format(time.RFC3339)
		}
	} else if !m.InternalDate.IsZero() {
		sum.Date = m.InternalDate.UTC().Format(time.RFC3339)
	}
	sum.HasAttach = bodyStructureHasAttachments(m.BodyStructure)
	sum.Snippet = snippetOf(m.BodySection)
	return sum
}

func firstAddress(addrs []imap.Address) Address {
	for _, addr := range addrs {
		if e := addr.Addr(); e != "" {
			return Address{Name: addr.Name, Email: e}
		}
	}
	return Address{}
}

func hasFlag(flags []imap.Flag, want imap.Flag) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

// bodyStructureHasAttachments reports whether any part is an attachment: marked attachment, marked inline but a named
// file that is neither text nor an image (as nextPart reads it), or unmarked, named and not text.
func bodyStructureHasAttachments(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	found := false
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if found {
			return false
		}
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		mediaType := single.MediaType()
		named := hasNamedParam(single.Params, "name")
		if disp := single.Disposition(); disp != nil {
			named = named || hasNamedParam(disp.Params, "filename")
			switch headerToken(disp.Value) {
			case "attachment":
				found = true
				return false
			case "inline":
				found = named && !staysInline(mediaType)
				return !found
			}
		}
		if named && !strings.HasPrefix(mediaType, "text/") {
			found = true
			return false
		}
		return true
	})
	return found
}

// hasNamedParam reports whether BODYSTRUCTURE parameters carry a non-empty key in any form: servers may pass the
// RFC 2231 key*= and key*0= forms through undecoded.
func hasNamedParam(params map[string]string, key string) bool {
	for name, v := range params {
		name = strings.ToLower(name)
		if v != "" && (name == key || strings.HasPrefix(name, key+"*")) {
			return true
		}
	}
	return false
}
