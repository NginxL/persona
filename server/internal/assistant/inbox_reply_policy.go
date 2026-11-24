package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Server-side reply policy gates for Inbox owner drafts (docs/assistant/inbox.md "Reply policy", #11901).
//
// Opt-in via manager.json inbox.reply_policy (disabled by default). When disabled or empty, existing queue,
// suggestion, and confirmation behavior is preserved unchanged. When enabled, a classifier outputs only a category
// name, the server maps that category to owner_draft, bot, owner_only, or no_reply, and fail-closed gates run at
// three independent boundaries:
//   1. Queue gate (before AI handling / escalation / agent start)
//   2. Suggestion gate (when accepting an owner reply suggestion in inbox_suggest / inbox_draft)
//   3. Send gate (immediately before the confirmed outward sender call, including a final-text veto)

// Default reply-policy category names mapped on the server when inbox.reply_policy.categories is omitted.
const (
	// Normal owner-draftable category.
	ReplyCategoryRoutineReply = "routine_reply"

	// Four owner-only categories: must never produce an AI owner draft or confirmed suggestion send.
	ReplyCategoryApprovalCommitment = "approval_commitment"
	ReplyCategorySensitivePersonal  = "sensitive_personal"
	ReplyCategoryExternalPublic     = "external_public"
	ReplyCategoryEscalationDecision = "escalation_decision"

	// Five bot categories: must never produce an AI owner draft or confirmed suggestion send.
	ReplyCategoryBotNotification = "bot_notification"
	ReplyCategorySystemAlert     = "system_alert"
	ReplyCategoryWorkflowNotice  = "workflow_notice"
	ReplyCategoryAutomatedDigest = "automated_digest"
	ReplyCategoryBotCommand      = "bot_command"

	// No-reply category.
	ReplyCategoryNoReply = "no_reply"
)

const defaultReplyPolicyTimeout = 30 * time.Second

var (
	errReplyPolicyBlocked         = i18n.ErrEnglish("inbox-errors.reply_policy_blocked")
	errReplyPolicyUnknownCategory = errors.New("unknown reply policy category")
	errReplyPolicyInvalidRoute    = errors.New("invalid reply policy route")
	errReplyPolicyStaleSnapshot   = errors.New("stale reply policy snapshot")
	errReplyPolicyMissingSnapshot = errors.New("missing reply policy snapshot")
)

// OwnerOnlyReplyPolicyCategories returns the four default owner-only categories.
func OwnerOnlyReplyPolicyCategories() []string {
	return []string{
		ReplyCategoryApprovalCommitment,
		ReplyCategorySensitivePersonal,
		ReplyCategoryExternalPublic,
		ReplyCategoryEscalationDecision,
	}
}

// BotReplyPolicyCategories returns the five default bot categories.
func BotReplyPolicyCategories() []string {
	return []string{
		ReplyCategoryBotNotification,
		ReplyCategorySystemAlert,
		ReplyCategoryWorkflowNotice,
		ReplyCategoryAutomatedDigest,
		ReplyCategoryBotCommand,
	}
}

// DefaultReplyPolicyCategories returns a copy of the built-in server-side category-to-route table.
func DefaultReplyPolicyCategories() map[string]string {
	return map[string]string{
		ReplyCategoryRoutineReply:       assistantwire.ReplyRouteOwnerDraft,
		ReplyCategoryApprovalCommitment: assistantwire.ReplyRouteOwnerOnly,
		ReplyCategorySensitivePersonal:  assistantwire.ReplyRouteOwnerOnly,
		ReplyCategoryExternalPublic:     assistantwire.ReplyRouteOwnerOnly,
		ReplyCategoryEscalationDecision: assistantwire.ReplyRouteOwnerOnly,
		ReplyCategoryBotNotification:    assistantwire.ReplyRouteBot,
		ReplyCategorySystemAlert:        assistantwire.ReplyRouteBot,
		ReplyCategoryWorkflowNotice:     assistantwire.ReplyRouteBot,
		ReplyCategoryAutomatedDigest:    assistantwire.ReplyRouteBot,
		ReplyCategoryBotCommand:         assistantwire.ReplyRouteBot,
		ReplyCategoryNoReply:            assistantwire.ReplyRouteNoReply,
	}
}

// InboxReplyPolicySnapshot records a server-side reply-policy decision for one content version and policy revision.
// Persisted in InboxItem / InboxSuggestion JSON; stripped from listed client copies.
type InboxReplyPolicySnapshot struct {
	Category       string    `json:"category,omitempty"`
	Route          string    `json:"route,omitempty"`
	ContentVersion string    `json:"content_version,omitempty"`
	PolicyRevision string    `json:"policy_revision,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Error          string    `json:"error,omitempty"`
	At             time.Time `json:"at,omitempty"`
}

func cloneReplyPolicySnapshot(s *InboxReplyPolicySnapshot) *InboxReplyPolicySnapshot {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

func sameReplyPolicyBinding(a, b *InboxReplyPolicySnapshot) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Category == b.Category &&
		a.Route == b.Route &&
		a.ContentVersion == b.ContentVersion &&
		a.PolicyRevision == b.PolicyRevision
}

// replyPolicySpec is the compiled, immutable server-side reply policy for one evaluation.
type replyPolicySpec struct {
	enabled    bool
	revision   string
	categories map[string]string // normalized category -> raw route ("" marks conflicting duplicate keys invalid)
	names      []string          // sorted category names offered to the classifier
	rules      string
	timeout    time.Duration
}

func validReplyPolicyRoute(route string) bool {
	switch route {
	case assistantwire.ReplyRouteOwnerDraft,
		assistantwire.ReplyRouteBot,
		assistantwire.ReplyRouteOwnerOnly,
		assistantwire.ReplyRouteNoReply:
		return true
	default:
		return false
	}
}

// compileReplyPolicy normalizes inbox.reply_policy and derives a deterministic effective policy revision.
// Disabled or zero-value configuration returns an inactive spec so existing behavior stays byte-for-byte identical.
func compileReplyPolicy(cfg assistantwire.InboxReplyPolicyConfig) replyPolicySpec {
	if !cfg.Enabled {
		return replyPolicySpec{}
	}
	rawCats := cfg.Categories
	if len(rawCats) == 0 {
		rawCats = DefaultReplyPolicyCategories()
	}
	cats := make(map[string]string, len(rawCats))
	// Sort raw keys so duplicate case/whitespace collisions resolve deterministically to invalid (fail closed).
	rawKeys := slices.Sorted(maps.Keys(rawCats))
	for _, k := range rawKeys {
		normKey := strings.ToLower(strings.TrimSpace(k))
		if normKey == "" {
			continue
		}
		normVal := strings.ToLower(strings.TrimSpace(rawCats[k]))
		if prev, exists := cats[normKey]; exists && prev != normVal {
			cats[normKey] = "" // ambiguous config fails closed
			continue
		}
		cats[normKey] = normVal
	}
	names := slices.Sorted(maps.Keys(cats))
	rules := strings.TrimSpace(cfg.Rules)
	timeout := defaultReplyPolicyTimeout
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	h := sha256.New()
	explicitRev := strings.TrimSpace(cfg.Revision)
	fmt.Fprintf(h, "rev=%s\nrules=%s\ntimeout=%d\n", explicitRev, rules, int(timeout/time.Second))
	for _, name := range names {
		fmt.Fprintf(h, "%s=%s\n", name, cats[name])
	}
	digest := hex.EncodeToString(h.Sum(nil))
	rev := "sha256:" + digest[:16]
	if explicitRev != "" {
		rev = explicitRev + "@" + digest[:12]
	}
	return replyPolicySpec{
		enabled:    true,
		revision:   rev,
		categories: cats,
		names:      names,
		rules:      rules,
		timeout:    timeout,
	}
}

// resolve maps a classifier-produced category name to a validated server route.
// Unknown categories and invalid routes always fail closed.
func (p replyPolicySpec) resolve(category string) (normCat, route string, err error) {
	normCat = strings.ToLower(strings.TrimSpace(category))
	if normCat == "" {
		return "", "", errReplyPolicyUnknownCategory
	}
	raw, ok := p.categories[normCat]
	if !ok {
		return normCat, "", errReplyPolicyUnknownCategory
	}
	if !validReplyPolicyRoute(raw) {
		return normCat, "", errReplyPolicyInvalidRoute
	}
	return normCat, raw, nil
}

// replyPolicyContentVersion hashes the item's message content and reply target so any content update
// invalidates prior policy snapshots. Caller holds b.mu (or owns a copy of it).
func replyPolicyContentVersion(it *InboxItem) string {
	if it == nil {
		return ""
	}
	payload := struct {
		ID, Source, Kind, Last, Title, Sender, Preview, Context, Target string
	}{
		ID:      it.ID,
		Source:  it.Source,
		Kind:    it.Kind,
		Last:    it.Last,
		Title:   it.Title,
		Sender:  it.Sender,
		Preview: it.Preview,
		Context: it.Context,
		Target:  suggestionReplyTarget(it),
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// replyPolicyAllowsOwnerDraft checks whether snap authorizes owner-draft handling for the given content version
// under the live policy. Every missing, errored, stale, unknown, invalid, or non-owner_draft state fails closed.
func (p replyPolicySpec) allowsOwnerDraft(snap *InboxReplyPolicySnapshot, contentVersion string) error {
	if !p.enabled {
		return nil
	}
	if snap == nil {
		return errReplyPolicyMissingSnapshot
	}
	if snap.Error != "" {
		return fmt.Errorf("%w: %s", errReplyPolicyBlocked, snap.Error)
	}
	if contentVersion == "" || snap.ContentVersion != contentVersion || snap.PolicyRevision != p.revision {
		return errReplyPolicyStaleSnapshot
	}
	normCat, route, err := p.resolve(snap.Category)
	if err != nil {
		return err
	}
	if snap.Category != normCat || snap.Route != route || route != assistantwire.ReplyRouteOwnerDraft {
		return fmt.Errorf("%w: category %q routes to %q", errReplyPolicyBlocked, normCat, route)
	}
	return nil
}

// replyPolicyItemData holds the lock-free snapshot of an item passed to the classifier.
type replyPolicyItemData struct {
	id             string
	title          string
	sender         string
	source         string
	kind           string
	context        string
	contentVersion string
}

func snapshotItemForReplyPolicyLocked(it *InboxItem) replyPolicyItemData {
	return replyPolicyItemData{
		id:             it.ID,
		title:          it.Title,
		sender:         it.Sender,
		source:         it.Source,
		kind:           it.Kind,
		context:        it.Context,
		contentVersion: replyPolicyContentVersion(it),
	}
}

// classifyReplyPolicy invokes the small-task classifier model to obtain a category name and maps it to a route
// strictly on the server. Any model-supplied route field is ignored. Errors, timeouts, unknown categories, and
// invalid routes fail closed.
func (s *Service) classifyReplyPolicy(ctx context.Context, policy replyPolicySpec, item replyPolicyItemData, replyText string) (*InboxReplyPolicySnapshot, error) {
	now := time.Now().UTC()
	snap := &InboxReplyPolicySnapshot{
		ContentVersion: item.contentVersion,
		PolicyRevision: policy.revision,
		At:             now,
	}
	if !policy.enabled {
		return nil, nil
	}
	if len(policy.names) == 0 {
		snap.Error = "no categories configured"
		return snap, errReplyPolicyUnknownCategory
	}
	hello := s.hello(ctx)
	language := answerLanguage(hello.Locale)
	system := prompts.RenderInboxReplyPolicy(prompts.InboxReplyPolicy{
		Language:   language,
		Owner:      inboxPromptOwner(hello),
		Categories: policy.names,
		Rules:      policy.rules,
	})
	input := prompts.RenderInboxReplyPolicyInput(prompts.InboxReplyPolicyInput{
		Language:  language,
		Title:     item.title,
		Sender:    item.sender,
		Source:    item.source,
		Kind:      item.kind,
		Context:   item.context,
		ReplyText: strings.TrimSpace(replyText),
	})
	// Only Category and Reason are decoded; any model-supplied "route" field is deliberately ignored so the model
	// cannot choose its own route.
	var out struct {
		Category string `json:"category"`
		Reason   string `json:"reason"`
	}
	cctx, cancel := context.WithTimeout(ctx, policy.timeout)
	err := s.inboxModelJSON(cctx, purposeTriage, system, input, 0, &out)
	timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	if err != nil {
		if timedOut {
			snap.Error = "classification timed out"
		} else {
			snap.Error = "classification failed"
		}
		return snap, fmt.Errorf("%w: %s", errReplyPolicyBlocked, snap.Error)
	}
	normCat, route, err := policy.resolve(out.Category)
	snap.Category = normCat
	snap.Reason = clip(oneLine(out.Reason), 80)
	if err != nil {
		snap.Error = err.Error()
		return snap, fmt.Errorf("%w: %v", errReplyPolicyBlocked, err)
	}
	snap.Route = route
	return snap, nil
}

// ---- Gate 1: Queue gate (before AI handling) ----

// gateReplyPolicyQueue classifies open items eligible for AI handling (and already-queued items without a session)
// whose reply-policy snapshot is missing, errored, or stale for the current content version or policy revision.
// Called before escalateWithPolicy and startInboxAgents in handPass.
func (s *Service) gateReplyPolicyQueue(ctx context.Context, policy replyPolicySpec, since time.Time) {
	if !policy.enabled {
		return
	}
	b := &s.inbox
	b.mu.Lock()
	var todo []replyPolicyItemData
	for _, it := range b.items {
		if !b.needsReplyPolicyQueueCheckLocked(it, policy, since) {
			continue
		}
		todo = append(todo, snapshotItemForReplyPolicyLocked(it))
	}
	b.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	var writes []*inboxWrite
	for _, data := range todo {
		snap, _ := s.classifyReplyPolicy(ctx, policy, data, "")
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		it := b.items[data.id]
		if it != nil && it.open() && replyPolicyContentVersion(it) == data.contentVersion {
			w := beginWrite(it)
			it.ReplyPolicy = snap
			b.settle(w)
			writes = append(writes, w)
		}
		b.mu.Unlock()
	}
	s.saveWrites(ctx, writes, "its reply policy classification")
}

// needsReplyPolicyQueueCheckLocked reports whether it is an escalation or queued-start candidate that needs a fresh
// reply-policy classification. Caller holds b.mu.
func (b *inboxBook) needsReplyPolicyQueueCheckLocked(it *InboxItem, policy replyPolicySpec, since time.Time) bool {
	if !policy.enabled || !it.open() || it.Context == "" {
		return false
	}
	cv := replyPolicyContentVersion(it)
	if it.ReplyPolicy != nil && it.ReplyPolicy.Error == "" &&
		it.ReplyPolicy.ContentVersion == cv && it.ReplyPolicy.PolicyRevision == policy.revision {
		return false // already has a non-error classification for this exact content and policy revision
	}
	a := it.Agent
	// Already queued without a session: must be classified before startInboxAgents decides whether to start or release.
	if a != nil && a.State == AgentQueued && a.SessionID == "" && !a.savingRetry {
		return true
	}
	if a != nil && (a.Of == it.Last && !a.InterruptedByDisable || a.savingRetry) {
		return false
	}
	handled := it.handled()
	twin := b.policyTwin(it)
	if !handled && (twin == TwinNever || it.Tier != TierAction && twin != TwinFirst || !it.tiered()) {
		return false
	}
	if (a == nil || a.Of == "" && a.StartedAt == nil && a.SessionID == "") && !it.UpdatedAt.After(since) {
		return false
	}
	return true
}

// replyPolicyAllowsQueueLocked is Gate 1's enforcement check in escalateWithPolicy and startInboxAgents.
// Caller holds b.mu.
func replyPolicyAllowsQueueLocked(it *InboxItem, policy replyPolicySpec) bool {
	if !policy.enabled {
		return true
	}
	return policy.allowsOwnerDraft(it.ReplyPolicy, replyPolicyContentVersion(it)) == nil
}

// ---- Gate 2: Suggestion gate (when accepting an owner reply suggestion) ----

// gateReplyPolicySuggestion enforces inbox.reply_policy before accepting a reply suggestion (inbox_suggest /
// inbox_draft). Fails closed on any non-owner_draft category, unknown category, invalid route, classification
// failure/timeout, or stale snapshot.
func (s *Service) gateReplyPolicySuggestion(ctx context.Context, policy replyPolicySpec, item replyPolicyItemData, existing *InboxReplyPolicySnapshot, replyText string) (*InboxReplyPolicySnapshot, error) {
	if !policy.enabled {
		return nil, nil
	}
	// If the incoming item already has a fresh non-error classification that disallows owner drafts, fail closed at once.
	if existing != nil && existing.Error == "" &&
		existing.ContentVersion == item.contentVersion && existing.PolicyRevision == policy.revision {
		if err := policy.allowsOwnerDraft(existing, item.contentVersion); err != nil {
			return existing, err
		}
	}
	// Always classify the item together with the proposed reply text under the live policy so neither an unchecked
	// incoming item (e.g. handed-off or pre-policy session) nor an unsafe proposed reply can create an owner draft.
	snap, err := s.classifyReplyPolicy(ctx, policy, item, replyText)
	if err != nil {
		return snap, err
	}
	if err := policy.allowsOwnerDraft(snap, item.contentVersion); err != nil {
		return snap, err
	}
	return snap, nil
}

func replyPolicySuggestionErr(snap *InboxReplyPolicySnapshot, err error) error {
	if snap != nil && snap.Route != "" && snap.Route != assistantwire.ReplyRouteOwnerDraft {
		return errSuggestion{fmt.Sprintf("reply policy routed category %q to %q; an owner reply suggestion is not allowed", snap.Category, snap.Route)}
	}
	if err != nil {
		return errSuggestion{fmt.Sprintf("reply policy blocked this owner reply suggestion: %v", err)}
	}
	return errSuggestion{"reply policy blocked this owner reply suggestion"}
}

// ---- Gate 3: Send gate (immediately before the confirmed sender call) ----

// verifyReplyPolicySuggestionLocked checks that g has a valid, fresh, owner_draft reply-policy snapshot matching
// the item's current content version and the live policy revision. Caller holds b.mu.
func verifyReplyPolicySuggestionLocked(it *InboxItem, g *InboxSuggestion, a *InboxSuggestionAttempt, policy replyPolicySpec) error {
	if !policy.enabled {
		return nil
	}
	cv := replyPolicyContentVersion(it)
	if err := policy.allowsOwnerDraft(g.ReplyPolicy, cv); err != nil {
		return err
	}
	if a != nil {
		attemptSnap := &InboxReplyPolicySnapshot{
			Category:       a.Category,
			Route:          a.Route,
			ContentVersion: a.ContentVersion,
			PolicyRevision: a.PolicyRevision,
		}
		if err := policy.allowsOwnerDraft(attemptSnap, cv); err != nil {
			return err
		}
	}
	return nil
}

// gateReplyPolicySend performs Gate 3's final-text veto immediately before confirmation proceeds to sending.
// It classifies the actual final text (original or owner-edited) under the live policy and fails closed on any
// non-owner_draft route, unknown category, invalid route, error, or timeout.
func (s *Service) gateReplyPolicySend(ctx context.Context, policy replyPolicySpec, item replyPolicyItemData, finalText string) (*InboxReplyPolicySnapshot, error) {
	if !policy.enabled {
		return nil, nil
	}
	snap, err := s.classifyReplyPolicy(ctx, policy, item, finalText)
	if err != nil {
		return snap, err
	}
	if err := policy.allowsOwnerDraft(snap, item.contentVersion); err != nil {
		return snap, err
	}
	return snap, nil
}

// verifyReplyPolicySendAttemptLocked is the synchronous pre-dispatch recheck immediately before the sender call
// (and again under once.mu in replyInboxDoMode) to prevent stale policy revisions or content changes from sending.
// Caller holds b.mu.
func verifyReplyPolicySendAttemptLocked(it *InboxItem, a *InboxSuggestionAttempt, policy replyPolicySpec) error {
	if !policy.enabled {
		return nil
	}
	if a == nil {
		return errReplyPolicyMissingSnapshot
	}
	cv := replyPolicyContentVersion(it)
	attemptSnap := &InboxReplyPolicySnapshot{
		Category:       a.Category,
		Route:          a.Route,
		ContentVersion: a.ContentVersion,
		PolicyRevision: a.PolicyRevision,
	}
	return policy.allowsOwnerDraft(attemptSnap, cv)
}
