package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The pushed web read first asks for this cheap validator (#13804). It covers the to-do write revision and the stable
// view classes, not the full response bytes: formatting every row, its brief, lineage and runs just to answer 304 was
// the costly part of #9590's full-list ETag. The old GET /todos response is unchanged.
//
// A full read emits a token only when its write revision and the classes stored by todoViewsSeen still match the
// projection it is sending. A concurrent write or an old-generation projection therefore costs a later full read,
// never lets a stale body claim to be current. The per-process instance prevents a restarted manager reusing a token.
type todoRevisionBook struct {
	writes   atomic.Uint64
	once     sync.Once
	instance string
}

func (b *todoRevisionBook) changed() { b.writes.Add(1) }

func (b *todoRevisionBook) id() string {
	b.once.Do(func() { b.instance = randomHex(16) })
	return b.instance
}

// todoRevisionView is the comparable part of a row's view, with its id, in a JSON-encodable form.
type todoRevisionView struct {
	ID       string                      `json:"id"`
	Phase    string                      `json:"phase"`
	Section  string                      `json:"section"`
	Reason   string                      `json:"reason"`
	Asks     string                      `json:"asks,omitempty"`
	Inactive int64                       `json:"inactive,omitempty"`
	CanWait  bool                        `json:"can_wait,omitempty"`
	Seen     bool                        `json:"seen,omitempty"`
	Steps    assistantwire.TodoStepCount `json:"steps"`
}

func todoRevisionViewOf(id string, c todoClass) todoRevisionView {
	return todoRevisionView{ID: id, Phase: c.phase, Section: c.section, Reason: c.reason, Asks: c.asks,
		Inactive: c.inactive, CanWait: c.canWait, Seen: c.seen, Steps: c.steps}
}

func (s *Service) todoRevisionFor(writes uint64, views []todoRevisionView, known bool) string {
	slices.SortFunc(views, func(a, b todoRevisionView) int { return strings.Compare(a.ID, b.ID) })
	raw, _ := json.Marshal(struct {
		Instance string             `json:"instance"`
		Writes   uint64             `json:"writes"`
		Known    bool               `json:"known"`
		Views    []todoRevisionView `json:"views"`
	}{Instance: s.trevision.id(), Writes: writes, Known: known, Views: views})
	sum := sha256.Sum256(raw)
	return `W/"tr1-` + hex.EncodeToString(sum[:16]) + `"`
}

// todoRevisionCurrent reads only the counters and stable classes already held in memory. It never loads, formats or
// projects the to-do list.
func (s *Service) todoRevisionCurrent() string {
	b := &s.tview
	b.mu.Lock()
	views := make([]todoRevisionView, 0, len(b.listed))
	for id, class := range b.listed {
		views = append(views, todoRevisionViewOf(id, class))
	}
	known := b.listed != nil
	b.mu.Unlock()
	return s.todoRevisionFor(s.trevision.writes.Load(), views, known)
}

// todoRevisionForProjection is the token of exactly this full answer. It is attached only if it is also the current
// token, which rules out a write or view recheck racing the projection.
func (s *Service) todoRevisionForProjection(writes uint64, p todoProjection) string {
	views := make([]todoRevisionView, 0, len(p.facts))
	for i := range p.facts {
		views = append(views, todoRevisionViewOf(p.facts[i].ID, todoClassOf(p.views[i])))
	}
	return s.todoRevisionFor(writes, views, true)
}

func (s *Service) tagTodoRevision(w http.ResponseWriter, writes uint64, p todoProjection) {
	if writes != s.trevision.writes.Load() {
		return
	}
	tag := s.todoRevisionForProjection(writes, p)
	if tag == s.todoRevisionCurrent() {
		w.Header().Set(assistantwire.TodoRevisionHeader, tag)
	}
}

func todoRevisionMatches(lines []string, tag string) bool {
	for _, line := range lines {
		for _, candidate := range strings.Split(line, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || candidate == tag {
				return true
			}
		}
	}
	return false
}

func (s *Service) todoRevisionRead(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(RoleHeader) == roleExtension {
		// extensionRows has its own scoped projection, which this internal validator does not describe.
		apierr.WriteCode(w, http.StatusConflict, "todo_revision_unavailable", errors.New("revision unavailable for extension rows"))
		return
	}
	tag := s.todoRevisionCurrent()
	w.Header().Set("ETag", tag)
	w.Header().Set(assistantwire.TodoRevisionHeader, tag)
	if todoRevisionMatches(r.Header.Values("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	apierr.WriteJSON(w, struct {
		Revision string `json:"revision"`
	}{Revision: tag})
}
