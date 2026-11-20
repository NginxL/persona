package devicewire

import (
	"context"
	"time"
)

// Caller is installed only by the authenticated hub transport. A display name,
// native transcript role, or client-supplied header never establishes ownership.
type Caller struct {
	Kind            string `json:"kind"`
	Issuer          string `json:"issuer,omitempty"`
	CredentialID    string `json:"credential_id,omitempty"`
	OwnerSubject    string `json:"owner_subject,omitempty"`
	BindingRevision int64  `json:"binding_revision,omitempty"`
	AdmissionID     string `json:"admission_id,omitempty"`
}
type Submission struct {
	ID           string `json:"submission_id"`
	Kind         string `json:"kind"`
	OriginalText string `json:"original_text"`
}

// SubmissionApprovalClarification is created only by the hub after consuming a
// one-time proof bound to an authenticated question on a prepared approval.
const SubmissionApprovalClarification = "approval_clarification"

type ShareAuthority struct {
	Issuer     string `json:"issuing_authority"`
	Machine    string `json:"machine_reference"`
	Generation string `json:"assignment_generation"`
}
type callerContextKey struct{}
type shareAuthorityKey struct{}
type ownerAnswerKey struct{}

// WithOwnerAnswer carries a card answer's provenance from the authenticated hub bus to Devices.
func WithOwnerAnswer(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerAnswerKey{}, true)
}

func IsOwnerAnswer(ctx context.Context) bool {
	ok, _ := ctx.Value(ownerAnswerKey{}).(bool)
	return ok
}

func WithShareAuthority(ctx context.Context, a ShareAuthority) context.Context {
	return context.WithValue(ctx, shareAuthorityKey{}, a)
}
func ShareAuthorityOf(ctx context.Context) ShareAuthority {
	a, _ := ctx.Value(shareAuthorityKey{}).(ShareAuthority)
	return a
}

// ShareAuthorityReporter keeps private authority outside JSON and HTTP headers.
type ShareAuthorityReporter interface{ SetShareAuthority(ShareAuthority) }

func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerContextKey{}, c)
}
func CallerOf(ctx context.Context) Caller { c, _ := ctx.Value(callerContextKey{}).(Caller); return c }

// ShareRoutes belong to the devices manager, not the hub's business storage.
var ShareRoutes = []string{
	"GET /api/v1/sessions/{mid}/{sid}/share-catalog",
	"POST /api/v1/sessions/{mid}/{sid}/share-preview",
	"GET /api/v1/sessions/{mid}/{sid}/share-preview/{preview_id}",
	"POST /api/v1/agent-shares",
	"GET /api/v1/agent-shares",
	"GET /api/v1/agent-shares/{share_id}",
	"GET /api/v1/agent-shares/{share_id}/messages",
	"PATCH /api/v1/agent-shares/{share_id}",
	"DELETE /api/v1/agent-shares/{share_id}",
}

const SharingCapability = "agent_sharing.v1"
const ProjectionMethod = "session.share_projection"
const ProjectionCapability = "share_projection.v1"
const CreationCapability = "creation_receipt.v1"

type ShareSelection struct {
	Mode          string   `json:"mode"`
	FromMessageID string   `json:"from_message_id,omitempty"`
	MessageIDs    []string `json:"message_ids,omitempty"`
}
type ShareExecution struct {
	Kind      string        `json:"kind"`
	Text      string        `json:"text,omitempty"`
	ToolID    string        `json:"tool_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Results   []ShareResult `json:"results,omitempty"`
}
type ShareResult struct {
	Text string `json:"text"`
}
type PublicShareMessage struct {
	ID        string           `json:"message_id"`
	Role      string           `json:"role"`
	Text      string           `json:"text"`
	Execution []ShareExecution `json:"execution,omitempty"`
}

// Frontier captures physical EOF separately from the last complete record. A row
// already partially present at enable time remains pre-existing when completed.
type ShareFrontier struct {
	Incarnation        string           `json:"incarnation"`
	Adapter            string           `json:"adapter"`
	Source             string           `json:"source_identity"`
	Branch             string           `json:"branch"`
	Boundary           int64            `json:"complete_record_boundary"`
	EOF                int64            `json:"observed_eof"`
	PrefixHash         string           `json:"accepted_prefix_sha256"`
	PartialHash        string           `json:"partial_prefix_sha256"`
	SubmissionSequence int64            `json:"submission_sequence"`
	FirstSeen          map[string]int64 `json:"first_seen"`
}
type ShareProjectionRequest struct {
	Inspect     bool           `json:"inspect,omitempty"`
	SessionID   string         `json:"session_id"`
	Incarnation string         `json:"incarnation"`
	Previous    *ShareFrontier `json:"previous,omitempty"`
}
type ShareProjectionMessage struct {
	Submission bool   `json:"submission,omitempty"`
	Identity   string `json:"identity"`
	FirstSeen  int64  `json:"first_seen"`
	// Timestamp orders trusted submitted input alongside native replies; it is
	// never included in PublicShareMessage and never establishes eligibility.
	Timestamp time.Time          `json:"timestamp,omitempty"`
	Message   PublicShareMessage `json:"message"`
}
type ShareProjection struct {
	Complete  bool                     `json:"complete"`
	Reason    string                   `json:"reason,omitempty"`
	Frontier  ShareFrontier            `json:"frontier"`
	Messages  []ShareProjectionMessage `json:"messages"`
	Omitted   int                      `json:"omitted_messages"`
	Omissions []string                 `json:"omissions"`
}

type modeActorKey struct{}

// WithModeActor carries display-only provenance from a trusted bus request.
func WithModeActor(ctx context.Context, a *Actor) context.Context {
	return context.WithValue(ctx, modeActorKey{}, a)
}

func ModeActorOf(ctx context.Context) *Actor {
	a, _ := ctx.Value(modeActorKey{}).(*Actor)
	return a
}

// AssistantModeActor identifies an internal assistant request in existing timeline clients.
func AssistantModeActor() *Actor { return &Actor{Kind: "assistant", DeviceName: "Persona"} }
