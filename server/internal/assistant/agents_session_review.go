package assistant

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// An explicit independent review of one Agent session's work (#1808). The source is read only: the reviewer starts in
// plan mode in a disposable snapshot, so even an engine recovering it without that mode cannot edit the source tree.
// The manager's acknowledged review book, never node state.put, owns identity, recovery and the result card.

const (
	sessionReviewSetting = "assistant.session_reviews"
	sessionReviewTool    = "persona_session_review"
	sessionReviewPrefix  = "review_"

	sessionReviewRunning   = "running"
	sessionReviewPending   = "pending"
	sessionReviewFailed    = "failed"
	sessionReviewSent      = "sent"
	sessionReviewDismissed = "dismissed"
	sessionReviewExpired   = "expired"
	sessionReviewReplaced  = "replaced"

	sessionReviewDeliverySent = "sent"
	sessionReviewAnswerSend   = "send"
	sessionReviewAnswerResume = "resume"

	sessionReviewTimeout   = 30 * time.Minute
	sessionReviewCardTTL   = 7 * 24 * time.Hour
	sessionReviewStartWait = 3 * time.Minute
	sessionReviewSendWait  = 5 * time.Minute
	sessionReviewKeepDone  = 50
	sessionReviewAskTurns  = 40
	sessionReviewAskMax    = 2_000
	sessionReviewAsksMax   = 12
)

type sessionReview struct {
	ID            string     `json:"id"`
	MachineID     string     `json:"machine_id"`
	SessionID     string     `json:"session_id"`
	ReviewerID    string     `json:"reviewer_session_id"`
	SessionTitle  string     `json:"session_title,omitempty"`
	Cwd           string     `json:"cwd,omitempty"`
	ReviewCwd     string     `json:"review_cwd,omitempty"`
	Worktree      string     `json:"worktree,omitempty"`
	Launcher      string     `json:"launcher,omitempty"`
	Language      string     `json:"language,omitempty"`
	Scope         string     `json:"scope,omitempty"`
	State         string     `json:"state"`
	Findings      string     `json:"findings,omitempty"`
	FindingsCount *int       `json:"findings_count,omitempty"`
	Delivery      string     `json:"delivery,omitempty"`
	AnswerMode    string     `json:"answer_mode,omitempty"`
	Error         string     `json:"error,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	CleanedAt     *time.Time `json:"cleaned_at,omitempty"`
}

type sessionReviewDocument struct {
	Revision int64           `json:"revision"`
	Reviews  []sessionReview `json:"reviews"`
}

type sessionReviewBook struct {
	mu     sync.Mutex
	loaded bool
	doc    sessionReviewDocument
	taking map[string]bool
}

var (
	errSessionReviewRunning  = errors.New("an independent review is already running")
	errSessionReviewConflict = errors.New("the independent review book changed concurrently")
	errSessionReviewStore    = errors.New("the independent review book is unavailable")
)

func sessionReviewT(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-session_review."+name, args...)
}

func sessionReviewErr(l i18n.Lang, name string, args ...i18n.Args) error {
	return errors.New(sessionReviewT(l, name, args...))
}

func newSessionReviewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func cloneSessionReviewDocument(doc sessionReviewDocument) sessionReviewDocument {
	doc.Reviews = slices.Clone(doc.Reviews)
	return doc
}

// sessionReviewRecords reads the book once. A failed read never becomes an empty book that a later write could erase.
func (s *Service) sessionReviewRecords(ctx context.Context) ([]sessionReview, error) {
	if s.store == nil {
		return nil, errSessionReviewStore
	}
	b := &s.sessionReviews
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		st := s.store()
		if st == nil {
			return nil, errSessionReviewStore
		}
		var doc sessionReviewDocument
		if _, err := st.Setting(ctx, sessionReviewSetting, &doc); err != nil {
			// The last acknowledged snapshot still backs cards already shown to the user; callers must honor the error.
			return slices.Clone(b.doc.Reviews), err
		}
		doc.Reviews = slices.Clone(doc.Reviews)
		b.doc, b.loaded = doc, true
	}
	return slices.Clone(b.doc.Reviews), nil
}

// changeSessionReviews serializes local writers, uses acknowledged CAS on the real store, and updates memory only
// after the write succeeds. The in-memory test store uses its acknowledged PutSetting; production always has CAS.
func (s *Service) changeSessionReviews(ctx context.Context, change func(*sessionReviewDocument) error) error {
	if s.store == nil {
		return errSessionReviewStore
	}
	b := &s.sessionReviews
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		st := s.store()
		if st == nil {
			return errSessionReviewStore
		}
		var doc sessionReviewDocument
		if _, err := st.Setting(ctx, sessionReviewSetting, &doc); err != nil {
			return err
		}
		b.doc, b.loaded = doc, true
	}
	doc := cloneSessionReviewDocument(b.doc)
	if err := change(&doc); err != nil {
		return err
	}
	// Never evict live work. Keep only the newest terminal records when the history exceeds its bound.
	if len(doc.Reviews) > sessionReviewKeepDone*2 {
		kept, terminal := make([]sessionReview, 0, len(doc.Reviews)), 0
		for i := len(doc.Reviews) - 1; i >= 0; i-- {
			e := doc.Reviews[i]
			active := e.State == sessionReviewRunning || e.State == sessionReviewPending
			if !active {
				terminal++
			}
			if active || terminal <= sessionReviewKeepDone {
				kept = append(kept, e)
			}
		}
		slices.Reverse(kept)
		doc.Reviews = kept
	}
	expected := doc.Revision
	doc.Revision++
	st := s.store()
	if st == nil {
		return errSessionReviewStore
	}
	if cas, ok := st.(standingCASStore); ok {
		saved, err := cas.CompareSetting(ctx, sessionReviewSetting, expected, doc)
		if err != nil {
			return err
		}
		if !saved {
			b.loaded = false // the next writer reloads the winning revision instead of conflicting forever
			return errSessionReviewConflict
		}
	} else if err := st.PutSetting(ctx, sessionReviewSetting, doc); err != nil {
		return err
	}
	b.doc = doc
	return nil
}

func sessionReviewByID(reviews []sessionReview, id string) (sessionReview, bool) {
	for _, e := range reviews {
		if e.ID == id {
			return e, true
		}
	}
	return sessionReview{}, false
}

func sessionReviewRunningOn(reviews []sessionReview, machine, sid string) (sessionReview, bool) {
	for _, e := range reviews {
		if e.MachineID == machine && e.SessionID == sid && e.State == sessionReviewRunning {
			return e, true
		}
	}
	return sessionReview{}, false
}

func (s *Service) mutateSessionReview(ctx context.Context, id string, change func(*sessionReview) error) error {
	return s.changeSessionReviews(ctx, func(doc *sessionReviewDocument) error {
		for i := range doc.Reviews {
			if doc.Reviews[i].ID == id {
				return change(&doc.Reviews[i])
			}
		}
		return errGone(id)
	})
}

// ---- explicit start and lightweight menu state ----

func (s *Service) sessionReviewRoutes() {
	s.handle("POST /api/v1/sessions/{mid}/{sid}/review", s.startSessionReview)
	s.handle("GET /api/v1/sessions/{mid}/{sid}/review", s.sessionReviewState)
}

func (s *Service) startSessionReview(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot start a review"))
		return
	}
	m, ok := s.fleetMachine(r.PathValue("mid"))
	if !ok {
		apierr.Write(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
		return
	}
	ctx := r.Context()
	c, ok := s.session(ctx, m.ID, r.PathValue("sid"))
	if !ok || c.Assistant {
		apierr.WriteCode(w, http.StatusNotFound, "no_session", sessionReviewErr(s.requestCtxLang(ctx), "no_session"))
		return
	}
	if c.Starting || c.Status == proto.StatusWorking || c.Status == proto.StatusBlocked {
		apierr.WriteCode(w, http.StatusConflict, "session_busy", sessionReviewErr(s.requestCtxLang(ctx), "session_busy"))
		return
	}
	if c.Agent != "" && c.Agent != "claude" {
		apierr.WriteCode(w, http.StatusConflict, "read_only_unsupported",
			sessionReviewErr(s.requestCtxLang(ctx), "read_only_unsupported", i18n.Args{"agent": agentLabel(c)}))
		return
	}
	cwd := cmp.Or(c.WorkspaceCwd, c.Cwd)
	if cwd == "" {
		apierr.WriteCode(w, http.StatusConflict, "no_change", sessionReviewErr(s.requestCtxLang(ctx), "no_change_not_git"))
		return
	}
	now := time.Now().UTC()
	lang := s.requestCtxLang(ctx)
	e := sessionReview{ID: sessionReviewPrefix + randomHex(8), MachineID: m.ID, SessionID: c.SessionID,
		ReviewerID: newSessionReviewUUID(), SessionTitle: agentLabel(c), Cwd: c.Cwd, ReviewCwd: cwd,
		Launcher: cmp.Or(c.Launcher, "claude"), Language: string(lang), StartedAt: now, State: sessionReviewRunning}
	if err := s.changeSessionReviews(ctx, func(doc *sessionReviewDocument) error {
		if _, running := sessionReviewRunningOn(doc.Reviews, m.ID, c.SessionID); running {
			return errSessionReviewRunning
		}
		doc.Reviews = append(doc.Reviews, e)
		return nil
	}); err != nil {
		if errors.Is(err, errSessionReviewRunning) {
			apierr.WriteCode(w, http.StatusConflict, "review_running", sessionReviewErr(s.requestCtxLang(ctx), "review_running"))
		} else {
			apierr.Write(w, http.StatusServiceUnavailable, sessionReviewErr(s.requestCtxLang(ctx), "store_failed"))
		}
		return
	}
	rev, scope, err := s.reviewChange(ctx, m.ID, cwd)
	if err != nil {
		s.abortSessionReview(ctx, e, "no_diff")
		apierr.Call(w, err)
		return
	}
	if len(rev.Files) == 0 && !rev.Truncated {
		s.abortSessionReview(ctx, e, "no_change")
		apierr.WriteCode(w, http.StatusConflict, "no_change", sessionReviewErr(s.requestCtxLang(ctx), "no_change"))
		return
	}
	diff, omitted := reviewDiff(*rev)
	ask := s.sessionReviewAsks(ctx, m.ID, c.SessionID)
	e.Scope = scope
	var snap proto.WorktreeSnapshot
	qctx, cancel := context.WithTimeout(ctx, sessionReviewStartWait)
	err = s.call(qctx, m.ID, "worktree.snapshot", map[string]any{"cwd": cwd, "name_hint": c.Title}, &snap)
	cancel()
	if err != nil || snap.Path == "" {
		s.abortSessionReview(ctx, e, "snapshot")
		if err != nil {
			apierr.Call(w, err)
		} else {
			apierr.WriteCode(w, http.StatusConflict, "not_supported", sessionReviewErr(s.requestCtxLang(ctx), "not_supported"))
		}
		return
	}
	e.Worktree = snap.Path
	if err := s.mutateSessionReview(ctx, e.ID, func(v *sessionReview) error {
		if v.State != sessionReviewRunning {
			return errGone(v.ID)
		}
		v.Worktree, v.Scope = e.Worktree, e.Scope
		return nil
	}); err != nil {
		s.abortSessionReview(ctx, e, "store")
		apierr.Write(w, http.StatusServiceUnavailable, sessionReviewErr(s.requestCtxLang(ctx), "store_failed"))
		return
	}
	prompt := prompts.RenderAgentReview(prompts.AgentReview{Language: answerLanguage(string(lang)), Title: c.Title,
		Ask: ask, Scope: scope, Diff: diff, Omitted: omitted})
	params := map[string]any{"cwd": snap.Path, "launcher": e.Launcher, "permission_mode": "plan", "prompt": prompt,
		"tab_name": "review-" + short(c.SessionID), "async": true, "session_id": e.ReviewerID}
	var res struct {
		Session proto.Session `json:"session"`
	}
	sctx, cancel := context.WithTimeout(ctx, sessionReviewStartWait)
	err = s.call(sctx, m.ID, DeviceSessionStart, params, &res)
	cancel()
	if err == nil && (res.Session.SessionID == "" || res.Session.StartError != "") {
		err = errors.New(cmp.Or(res.Session.StartError, "no session"))
	}
	if err != nil {
		s.abortSessionReview(ctx, e, "start")
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == "readonly_unsupported" {
			apierr.WriteCode(w, http.StatusConflict, "read_only_unsupported", sessionReviewErr(s.requestCtxLang(ctx), "read_only_unsupported", i18n.Args{"agent": agentLabel(c)}))
		} else {
			apierr.WriteCode(w, http.StatusConflict, "not_supported", sessionReviewErr(s.requestCtxLang(ctx), "start_failed", i18n.Args{"error": err.Error()}))
		}
		return
	}
	e.ReviewerID = res.Session.SessionID // a node that assigned its own id is correlated by this acknowledged answer
	if err := s.mutateSessionReview(ctx, e.ID, func(v *sessionReview) error {
		if v.State != sessionReviewRunning {
			return errGone(v.ID)
		}
		v.ReviewerID = e.ReviewerID
		return nil
	}); err != nil {
		s.abortSessionReview(ctx, e, "store")
		apierr.Write(w, http.StatusServiceUnavailable, sessionReviewErr(s.requestCtxLang(ctx), "store_failed"))
		return
	}
	s.publishSoon()
	apierr.WriteJSON(w, map[string]any{"running": true, "reviewer_session_id": e.ReviewerID, "started_at": now.Format(time.RFC3339)})
}

func (s *Service) sessionReviewState(w http.ResponseWriter, r *http.Request) {
	m, ok := s.fleetMachine(r.PathValue("mid"))
	if !ok {
		apierr.Write(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
		return
	}
	ctx := r.Context()
	c, ok := s.session(ctx, m.ID, r.PathValue("sid"))
	if !ok || c.Assistant {
		apierr.WriteCode(w, http.StatusNotFound, "no_session", sessionReviewErr(s.requestCtxLang(ctx), "no_session"))
		return
	}
	reviews, err := s.sessionReviewRecords(ctx)
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, sessionReviewErr(s.requestCtxLang(ctx), "store_failed"))
		return
	}
	out := map[string]any{"running": false, "has_changes": true, "started_at": ""}
	if running, ok := sessionReviewRunningOn(reviews, m.ID, c.SessionID); ok {
		out["running"], out["started_at"] = true, running.StartedAt.Format(time.RFC3339)
		apierr.WriteJSON(w, out)
		return
	}
	if c.Starting || c.Status == proto.StatusWorking || c.Status == proto.StatusBlocked {
		apierr.WriteJSON(w, out)
		return
	}
	rev, _, err := s.reviewChange(ctx, m.ID, cmp.Or(c.WorkspaceCwd, c.Cwd))
	if err != nil {
		apierr.Call(w, err)
		return
	}
	out["has_changes"] = len(rev.Files) > 0 || rev.Truncated
	apierr.WriteJSON(w, out)
}

// sessionReviewAsks returns only genuine user inputs, never assistant text, reasoning, injected follow-ups or peers.
func (s *Service) sessionReviewAsks(ctx context.Context, machine, sid string) string {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(qctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": sessionReviewAskTurns, "lazy_images": true}, &res); err != nil {
		s.logf("session review %s/%s: user asks could not be read: %v", machine, short(sid), err)
		return ""
	}
	items := slices.Clone(res.Items)
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = cmp.Or(items[i].ClientMsgID, fmt.Sprintf("seq:%d:%d", items[i].Seq, i))
		}
	}
	allowed := map[string]bool{}
	for _, p := range transcript.UserInputPositions(items) {
		allowed[p.ID] = true
	}
	var asks []string
	for _, it := range items {
		if it.Kind == proto.KindText && it.Role == "user" && allowed[it.ID] && strings.TrimSpace(it.Text) != "" {
			asks = append(asks, clip(strings.TrimSpace(it.Text), sessionReviewAskMax))
		}
	}
	if len(asks) > sessionReviewAsksMax {
		asks = asks[len(asks)-sessionReviewAsksMax:]
	}
	total := 0
	for i := len(asks) - 1; i >= 0; i-- {
		total += len([]rune(asks[i]))
		if total > reviewAskBudget {
			asks = asks[i+1:]
			break
		}
	}
	return strings.Join(asks, "\n\n")
}

func (s *Service) abortSessionReview(ctx context.Context, e sessionReview, why string) {
	now := time.Now().UTC()
	_ = s.mutateSessionReview(ctx, e.ID, func(v *sessionReview) error {
		if v.State == sessionReviewRunning {
			v.State, v.Error, v.EndedAt = sessionReviewFailed, why, &now
		}
		return nil
	})
	// Cleanup must run even when the failed state could not be persisted. The snapshot path and reviewer ID are known here.
	e.State = sessionReviewFailed
	s.publishSoon()
	s.cleanSessionReview(ctx, e)
}

func (s *Service) cleanSessionReview(ctx context.Context, e sessionReview) {
	if e.State == sessionReviewRunning || e.CleanedAt != nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	stopFailed := false
	if e.ReviewerID != "" {
		if err := s.call(cctx, e.MachineID, deviceSessionStop, map[string]any{"session_id": e.ReviewerID}, nil); err != nil {
			stopFailed = true
			s.logf("session review %s: stop reviewer: %v", e.ID, err)
		}
	}
	if e.Worktree != "" {
		err := s.call(cctx, e.MachineID, "worktree.remove", map[string]any{"path": e.Worktree, "force": true}, nil)
		var re *rpc.Error
		if err != nil && !(errors.As(err, &re) && re.Code == "not_our_worktree") {
			s.logf("session review %s: remove snapshot: %v", e.ID, err)
			return
		}
	}
	if stopFailed {
		return // the next tick retries the stop; a removed snapshot alone is not a completed cleanup
	}
	now := time.Now().UTC()
	_ = s.mutateSessionReview(context.Background(), e.ID, func(v *sessionReview) error {
		v.CleanedAt = &now
		return nil
	})
}
