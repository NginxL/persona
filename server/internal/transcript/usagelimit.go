package transcript

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Subscription usage limits in Claude Code's words (docs/assistant/agent-work.md "Usage limits"). Its own record
// says more (the assistant line's quotaLimits, read by the parser); these read the words for what an older version
// wrote, a launcher check's output and a node that does not send the record's fields yet.

// UsageLimitText: text is Claude Code's line for a subscription usage limit (only as the start of the text: a reply
// may quote one). The pattern lives in assistantwire, which CutOff shares; a contract package cannot import this one.
func UsageLimitText(text string) bool {
	return assistantwire.UsageLimitText(text)
}

// usageLimitReset is how Claude Code says when a limit resets: "resets 4:20pm (Area/City)", "resets at 4pm (UTC)"; past
// a day "resets Sep 28, 4pm (…)" ("Sep 28 at 4pm" in some locales' data), another year "resets Jan 2, 2027, 4pm (…)".
// The time zone is the one Claude Code's machine runs in, as an IANA name.
var usageLimitReset = lazyre.New(`(?i)\bresets (?:at )?(?:(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]* (\d{1,2})(?:, (\d{4}))?(?:,| at) )?` +
	`(\d{1,2})(?::(\d{2}))?\s?([ap]m) \(([A-Za-z]+(?:/[A-Za-z0-9_+\-]+)*)\)`)

// fiveHourLimit is how Claude Code names its 5-hour window on the line before the reset ("You've hit your 5-hour
// limit", "session limit" in older versions). The weekly, Opus and Sonnet limits have their own names.
var fiveHourLimit = lazyre.New(`(?i)\b(?:5-hour|session) limit\b`)

// fiveHourWindow: a 5-hour limit resets at most this long after its line was written.
const fiveHourWindow = 5 * time.Hour

var months = map[string]time.Month{"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August, "sep": time.September, "oct": time.October,
	"nov": time.November, "dec": time.December}

// UsageLimitReset is when the limit text speaks of resets, read off its words (usageLimitReset), as the first such
// moment after at (when the text was written). A clock time without a time zone is not read: it is the local time of
// a machine the reader may not be on. False when the words name no time this can place.
//
// Claude Code writes the time alone when the reset is at most 24 hours off, whichever limit it is, and the date with it
// further off. A time alone that is not after at is tomorrow's, except on a 5-hour limit's line: it resets within
// fiveHourWindow, so when tomorrow's is further off the time is the reset that has just passed (the line was read a
// little after it, #9386), returned as it is: past, the limit is over.
func UsageLimitReset(text string, at time.Time) (time.Time, bool) {
	if assistantwire.GatewayQuotaText(text) {
		return GatewayAvailableAgain(text, at)
	}
	m := usageLimitReset.FindStringSubmatch(text)
	if m == nil || at.IsZero() {
		return time.Time{}, false
	}
	loc, ok := zone(m[7])
	if !ok {
		return time.Time{}, false
	}
	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5]) // none: 0
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}, false
	}
	hour %= 12
	if strings.EqualFold(m[6], "pm") {
		hour += 12
	}
	now := at.In(loc)
	if m[1] == "" { // a time of day: today's, else tomorrow's (a 5-hour limit's: the one just past, see above)
		t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if !t.After(now.Add(-time.Minute)) {
			next := time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, loc)
			if !next.After(now.Add(fiveHourWindow)) || !namesFiveHourLimit(text, m[0]) {
				t = next
			}
		}
		return t.UTC(), true
	}
	day, _ := strconv.Atoi(m[2])
	month := months[strings.ToLower(m[1])]
	if day < 1 || day > 31 {
		return time.Time{}, false
	}
	year := now.Year()
	if m[3] != "" {
		year, _ = strconv.Atoi(m[3])
	}
	t := time.Date(year, month, day, hour, minute, 0, 0, loc)
	if t.Day() != day {
		return time.Time{}, false // Feb 30
	}
	if m[3] == "" && t.Before(now.Add(-time.Hour)) {
		t = time.Date(year+1, month, day, hour, minute, 0, 0, loc) // "Jan 2" written on Dec 30
	}
	return t.UTC(), true
}

var gatewayAvailable = lazyre.New(`(?i)available again at (?:(\d{4}-\d{2}-\d{2})\s+)?(\d{2}:\d{2})\s*UTC\b`)

// GatewayAvailableAgain reads the UTC reset given by a gateway. Undated clocks are anchored to the error's
// timestamp, never the time a stopped session happens to be listed after its reset.
func GatewayAvailableAgain(text string, at time.Time) (time.Time, bool) {
	m := gatewayAvailable.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	date := m[1]
	if date == "" {
		if at.IsZero() {
			return time.Time{}, false
		}
		date = at.UTC().Format("2006-01-02")
	}
	t, err := time.Parse("2006-01-02 15:04", date+" "+m[2])
	if err != nil {
		return time.Time{}, false
	}
	if m[1] == "" && !t.After(at) {
		t = t.Add(24 * time.Hour)
	}
	return t, true
}

// GatewayErrorKind classifies only the two known gateway 429 shapes. Other rate limits keep their existing policy.
func GatewayErrorKind(text string, at time.Time) (string, time.Time) {
	if assistantwire.GatewayQuotaText(text) {
		reset, _ := GatewayAvailableAgain(text, at)
		return proto.TurnErrorUsageLimit, reset
	}
	if assistantwire.GatewayCapacityText(text) {
		return proto.TurnErrorTransient, time.Time{}
	}
	return "", time.Time{}
}

// namesFiveHourLimit: the line of text that holds the reset words match names the 5-hour limit before them.
func namesFiveHourLimit(text, match string) bool {
	before := text[:strings.Index(text, match)] // the leftmost match is the words' first occurrence
	if i := strings.LastIndexByte(before, '\n'); i >= 0 {
		before = before[i+1:]
	}
	return fiveHourLimit.MatchString(before)
}

// zones keeps the time zones UsageLimitReset loaded (name → *time.Location): the session lists read the same limit's
// words each time they are built, and time.LoadLocation reads the zone file every call. Only real zones are kept,
// so the words cannot grow it past the zone database.
var zones sync.Map

func zone(name string) (*time.Location, bool) {
	if name == "Local" {
		return nil, false // the reader's own zone, not the writer's
	}
	if v, ok := zones.Load(name); ok {
		return v.(*time.Location), true
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	zones.Store(name, loc)
	return loc, true
}
