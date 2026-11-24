package imessage

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealMessagesCounts reads a real Mac's Messages through the same code, printing only numbers (conversations,
// messages, what did not decode, how long it took): never a name, a number or a word of a message. Opt-in, on the
// Mac whose data it is:
//
//	PERSONA_IMESSAGE_HOME=$HOME ./imessage.test -test.run RealMessagesCounts -test.v
func TestRealMessagesCounts(t *testing.T) {
	home := os.Getenv("PERSONA_IMESSAGE_HOME")
	if home == "" {
		t.Skip("set PERSONA_IMESSAGE_HOME to a Mac home to count its messages")
	}
	ctx := context.Background()
	r := &Reader{Home: home, SQLite: sqlite3WithJSON(t)}
	if st := r.Status(ctx); st.State != "ok" {
		t.Fatalf("status %s", st.State)
	}
	start := time.Now()
	var convs, groups, named, previews int
	var firstPage time.Duration
	var ids []string
	before := ""
	for {
		p, err := r.Chats(ctx, 50, before, "")
		if err != nil {
			t.Fatal(err)
		}
		if firstPage == 0 {
			firstPage = time.Since(start)
		}
		for _, c := range p.Chats {
			convs++
			ids = append(ids, c.ID)
			if c.Group {
				groups++
			}
			if c.Preview != "" {
				previews++
			}
			for _, m := range c.Members {
				if m.Name != "" {
					named++
					break
				}
			}
		}
		if !p.More {
			break
		}
		before = p.Before
	}
	t.Logf("conversations %d (groups %d, with a contact's name %d, with a preview %d); first page %s, all %s",
		convs, groups, named, previews, firstPage.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))

	// The newest page of the 30 newest conversations.
	start = time.Now()
	var msgs, texts, undecoded, events, files, unsent, slowest int
	var slow time.Duration
	for i, id := range ids[:min(30, len(ids))] {
		t0 := time.Now()
		p, err := r.Messages(ctx, id, 50, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(t0); d > slow {
			slow, slowest = d, i
		}
		for _, m := range p.Messages {
			msgs++
			switch {
			case m.Kind == "event":
				events++
			case m.Undecoded:
				undecoded++
			case m.Unsent:
				unsent++
			case m.Text != "":
				texts++
			}
			files += len(m.Attachments)
		}
	}
	t.Logf("messages %d in %d conversations: with text %d, undecoded %d, events %d, unsent %d, attachments %d; %s, slowest #%d %s",
		msgs, min(30, len(ids)), texts, undecoded, events, unsent, files, time.Since(start).Round(time.Millisecond), slowest, slow.Round(time.Millisecond))

	// What would go to Persona: 30 days of changes, kept (contacts or written in) and all, and a search.
	for _, keep := range []string{KeepContacts, KeepAll} {
		start = time.Now()
		var pages, batches, kept, masked int
		cursor := ""
		for {
			p, err := r.Changes(ctx, cursor, 500, time.Now().AddDate(0, 0, -30), keep)
			if err != nil {
				t.Fatal(err)
			}
			pages++
			batches += len(p.Batches)
			for _, b := range p.Batches {
				kept += len(b.Messages)
				for _, m := range b.Messages {
					if strings.Contains(m.Text, "••••") {
						masked++
					}
				}
			}
			cursor = p.Next
			if !p.More {
				break
			}
		}
		t.Logf("changes (%s, 30 days): %d page(s), %d conversation batch(es), %d message(s), %d with a masked code; %s",
			keep, pages, batches, kept, masked, time.Since(start).Round(time.Millisecond))
	}
	start = time.Now()
	hits, err := r.Search(ctx, "的", 20, time.Now().AddDate(-1, 0, 0), KeepContacts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("search over a year: %d hit(s); %s", len(hits), time.Since(start).Round(time.Millisecond))
}
