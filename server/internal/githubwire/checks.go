package githubwire

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// PRCheckSnapshot is a PR head's check runs and commit statuses. An empty
// snapshot is not evidence that CI has finished.
type PRCheckSnapshot struct {
	Head        string
	Fingerprint string
	Pending     bool
	Failures    []string
	Count       int
	identities  []string
}

// AddRun and AddStatus collect the same check semantics from either source.
// Callers validate their own input and completeness before adding it.
func (s *PRCheckSnapshot) AddRun(id int64, name, status, conclusion string) {
	s.identities = append(s.identities, fmt.Sprintf("run:%d:%q:%q:%q", id, name, status, conclusion))
	s.Count++
	if name == "" || status != "completed" || conclusion == "" {
		s.Pending = true
		return
	}
	switch conclusion {
	case "success", "neutral", "skipped":
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
		s.Failures = append(s.Failures, name)
	default:
		s.Pending = true
	}
}

func (s *PRCheckSnapshot) AddStatus(context, state string) {
	s.identities = append(s.identities, fmt.Sprintf("status:%q:%q", context, state))
	s.Count++
	if context == "" {
		s.Pending = true
		return
	}
	switch state {
	case "success":
	case "failure", "error":
		s.Failures = append(s.Failures, context)
	default:
		s.Pending = true
	}
}

// Finish seals a snapshot with a stable identity for its head and checks.
func (s *PRCheckSnapshot) Finish() {
	sort.Strings(s.Failures)
	sort.Strings(s.identities)
	hash := sha256.Sum256([]byte(s.Head + "\x00" + strings.Join(s.identities, "\x00")))
	s.Fingerprint = fmt.Sprintf("%x", hash)
	s.identities = nil
}

// ValidRepo reports whether r is a safe GitHub owner/name pair: "owner/name" of letters, digits, "_", "." and "-", and
// nothing that changes the API path ("..").
func ValidRepo(r string) bool {
	owner, name, ok := strings.Cut(r, "/")
	return ok && repoPart(owner) && repoPart(name) && owner != "." && owner != ".." && name != "." && name != ".."
}

func repoPart(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}
