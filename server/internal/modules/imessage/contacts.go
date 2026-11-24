package imessage

import (
	"context"
	"path/filepath"
	"strings"
	"unicode"
)

// contacts are the names in this Mac's address book (the same Apple ID as Messages), by number and address. Numbers
// are saved in every form ("+1 (555) 010-0", "555 0101"): they are matched on their last ten digits, or else on
// their last seven when only one contact has those.
type contacts struct {
	byNumber map[string]string // last ten digits (or all, when fewer)
	bySeven  map[string]string // last seven digits; "" when two contacts share them
	byEmail  map[string]string
}

func (r *Reader) addressBooks() []string {
	dir := filepath.Join(r.Home, "Library", "Application Support", "AddressBook")
	books, _ := filepath.Glob(filepath.Join(dir, "Sources", "*", "AddressBook-v22.abcddb"))
	return append([]string{filepath.Join(dir, "AddressBook-v22.abcddb")}, books...)
}

type contactRow struct {
	First *string `json:"first"`
	Last  *string `json:"last"`
	Nick  *string `json:"nick"`
	Org   *string `json:"org"`
	Value *string `json:"value"`
}

const contactName = `r.ZFIRSTNAME AS first, r.ZLASTNAME AS last, r.ZNICKNAME AS nick, r.ZORGANIZATION AS org`

func (r *Reader) loadContacts(ctx context.Context) (*contacts, error) {
	c := &contacts{byNumber: map[string]string{}, bySeven: map[string]string{}, byEmail: map[string]string{}}
	var lastErr error
	read := 0
	for _, book := range r.addressBooks() {
		var phones, emails []contactRow
		err := r.query(ctx, book, "SELECT "+contactName+", p.ZFULLNUMBER AS value FROM ZABCDPHONENUMBER p JOIN ZABCDRECORD r ON r.Z_PK = p.ZOWNER", &phones)
		if err == nil {
			err = r.query(ctx, book, "SELECT "+contactName+", e.ZADDRESS AS value FROM ZABCDEMAILADDRESS e JOIN ZABCDRECORD r ON r.Z_PK = e.ZOWNER", &emails)
		}
		if err != nil {
			lastErr = err // a book that is missing (the top-level one often is) or unreadable: the others still count
			continue
		}
		read++
		for _, row := range phones {
			if name := row.name(); name != "" {
				c.addNumber(str(row.Value), name)
			}
		}
		for _, row := range emails {
			if name := row.name(); name != "" && str(row.Value) != "" {
				c.byEmail[strings.ToLower(strings.TrimSpace(str(row.Value)))] = name
			}
		}
	}
	if read == 0 {
		return c, lastErr
	}
	return c, nil
}

// name is a contact's name as Contacts shows it: a Chinese name (family name first, together) or Alice Example; else the
// nickname or the company.
func (row contactRow) name() string {
	first, last := strings.TrimSpace(str(row.First)), strings.TrimSpace(str(row.Last))
	switch {
	case first != "" || last != "":
		if hasHan(first + last) {
			return last + first
		}
		return strings.TrimSpace(first + " " + last)
	case strings.TrimSpace(str(row.Nick)) != "":
		return strings.TrimSpace(str(row.Nick))
	}
	return strings.TrimSpace(str(row.Org))
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func lastN(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (c *contacts) addNumber(number, name string) {
	d := digits(number)
	if len(d) < 3 {
		return
	}
	c.byNumber[lastN(d, 10)] = name
	if len(d) >= 7 {
		seven := lastN(d, 7)
		if other, ok := c.bySeven[seven]; ok && other != name {
			c.bySeven[seven] = ""
		} else {
			c.bySeven[seven] = name
		}
	}
}

// name is the contact's name for a handle (a number or an address), "" when nobody has it.
func (c *contacts) name(handle string) string {
	if c == nil {
		return ""
	}
	if strings.Contains(handle, "@") {
		return c.byEmail[strings.ToLower(strings.TrimSpace(handle))]
	}
	d := digits(handle)
	if d == "" {
		return ""
	}
	if n, ok := c.byNumber[lastN(d, 10)]; ok {
		return n
	}
	if len(d) >= 7 {
		return c.bySeven[lastN(d, 7)]
	}
	return ""
}
