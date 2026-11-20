package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// modelExecution is non-secret per-turn execution metadata supplied by the hub.
type modelExecution struct {
	Strict           bool   `json:",omitempty"` // scheduled execution never substitutes another model or agent
	TaskInstructions string `json:"-"`
	TaskAttachments  string `json:"-"`
	Model            string
	Effort           string
	// Speed is a one-shot Agent start choice, resolved only against the actual terminal dispatch. FastMode is the
	// Claude Code form. Neither is kept as process state or included in the execution fingerprint.
	Speed    string `json:"-"`
	FastMode bool   `json:"-"`
	// ServiceTier is a one-shot native speed request for a new Codex conversation or Agent start. It is consumed before
	// the first turn, never kept as process state or included in the execution fingerprint.
	ServiceTier string `json:"-"`
	UseEffort   bool
	Fingerprint string
	Ultracode   *bool `json:"-"` // explicit TUI start request; never a default or execution fingerprint
	// PlanThen: the permission mode a start in plan mode goes on in once its plan is approved (planthen.go); never
	// part of a fingerprint.
	PlanThen string `json:"-"`
	// Runtime preserves an external request's raw value for admission only. Ordinary conversations always use the CLI.
	Runtime string `json:"-"`
	// StartupContext belongs to the current external send only and is cleared before recording a process model.
	StartupContext context.Context `json:"-"`
	IsMain         bool            `json:",omitempty"`
	// FollowsSetting: a leader/background supervisor conversation the manager sends with the main chat's model
	// setting (#7824). With IsMain it identifies the protected conversations whose recovery is visible and reversible;
	// worker Agent conversations have neither flag.
	FollowsSetting bool   `json:",omitempty"`
	ConfigDir      string `json:"-"` // a conversation's directory, not its model; retained across process restarts
	// MainCompactWindow: the compaction window the manager holds the main conversation to (assistant.main_compact_window,
	// #7314), sent with is_main; 0 (another conversation, a launcher Persona does not manage, an older manager, the
	// setting at 0) leaves the launcher's (assistantCompactionPolicy). Omitted when 0, so a fingerprint from before it
	// stays the same.
	MainCompactWindow int `json:",omitempty"`
	// SupervisionInstructions is manager-authorized role policy, never task or event data.
	// It changes the execution fingerprint so a role change resumes in a fresh process.
	SupervisionInstructions string `json:",omitempty"`
	SupervisionResolved     bool   `json:"-"`
	// Origin is the conversation's manager-recorded source. It is authorization metadata, not a model choice, but its
	// Inbox/ordinary class changes Claude's built-in tool list. The fingerprint uses that class so a real policy change
	// gets a new process while ordinary aliases (for example user and empty) do not restart an identical tool policy.
	Origin string `json:",omitempty"`
	// OriginKnown distinguishes a manager's explicit ordinary (empty-origin) decision from a pre-upgrade record whose
	// origin is unknown. It is likewise authorization metadata and never changes the execution fingerprint.
	OriginKnown bool `json:"-"`
	// OriginDecisionRequired marks the RPC and autonomous-resume entries that must fail closed on a legacy unknown
	// origin. Trusted in-process model plumbing may carry an existing execution without setting this per-entry gate.
	OriginDecisionRequired bool `json:"-"`
	// Public: this message answers in someone else's chat. Per message, never persisted or included in the execution
	// fingerprint; assistantSend leaves the owner's live snapshot out of this input.
	Public bool `json:"-"`
	// Interject: a message this one carries, sent while the conversation answers, moves its foreground work to the
	// background (assistant_interject.go). Per message, never part of the fingerprint.
	Interject bool `json:"-"`
	// Interactive: run a Claude conversation as interactive Claude Code instead of claude -p (assistant_tui.go), the
	// assistant's claude_interactive setting. Part of the fingerprint, so a change resumes an idle conversation in a
	// new process; omitted when off, so fingerprints from before it stay the same.
	Interactive bool `json:",omitempty"`
	// NewModelChoice classifies this request against the previous recovery chain. It is per-turn provenance, never
	// persisted or included in the execution fingerprint, and is committed only when the input is delivered.
	NewModelChoice bool `json:"-"`
	// SecretTicket: the hand-off a new Claude Code start puts in its environment (secretenv.go). Per start, never
	// part of a fingerprint, never kept for a resume.
	SecretTicket string `json:"-"`
	// External is a manager-resolved, explicit main-chat pick. Nil preserves older fingerprints.
	External *proto.ExternalExecution `json:",omitempty"`
	// Goal: a /goal typed after the first message of a new Claude Code or Codex start is in (startgoal.go,
	// codex_start_goal.go). Per start, never part of a fingerprint, never kept for a resume.
	Goal string `json:"-"`
	// InPane is session.start's in_pane: a failed start's kept tab to start in (startremedy.go).
	InPane string `json:"-"`
	// taskSpace carries task placement into an asynchronous start; it is not execution or model state.
	taskSpace *taskSpace `json:"-"`
}

// supervisionDeliveryKey marks a send that is the manager's own supervision message (message_source "supervision").
type supervisionDeliveryKey struct{}

// withSupervisionDelivery marks ctx for a send from message source src when that is a supervision message.
func withSupervisionDelivery(ctx context.Context, src *proto.MessageSource) context.Context {
	if src != nil && src.Kind == proto.SourceSupervision {
		return context.WithValue(ctx, supervisionDeliveryKey{}, true)
	}
	return ctx
}

// runtimePolicyDropped decides what a runtime conversation does with a supervision policy it cannot carry: a runtime has no
// separate policy channel. The manager's own supervision message is refused, since delivered without its policy it would
// leave a supervisor unsupervised; anything else (a person's message the hub could not tell was for a runtime
// conversation) goes through without the policy rather than shutting that conversation's owner out.
func (n *Node) runtimePolicyDropped(ctx context.Context, id string) error {
	if ctx.Value(supervisionDeliveryKey{}) == true {
		return supervisionRuntimeRefusal(ctx)
	}
	n.logf("assistant %s: runs on a runtime, which has no channel for supervision policy: sending the message without it", short(id))
	return nil
}

func supervisionRuntimeRefusal(ctx context.Context) error {
	return refusal(assistantwire.CodeSupervisionRuntime, i18n.T(i18n.From(ctx), "rpc-errors.assistant_supervision_runtime"), nil)
}

// check refuses a model that is not a model id (proto.ValidModelID, #1644) before anything runs; the error's text is
// what the manager reads as a refused request (assistant startRequestErr), so it stays "invalid model". The effort is
// checked against the launcher's own levels: a codex one takes ultra (validModesFor).
func (m modelExecution) check(launcher string) error {
	if err := m.checkModel(); err != nil {
		return err
	}
	return validModesFor(launcher, "", m.Effort)
}

func (m modelExecution) checkModel() error {
	if m.Model != "" && !proto.ValidModelID(m.Model) {
		return errors.New("invalid model")
	}
	return nil
}

// assistantOriginToolPolicy is the only origin distinction that changes a process's built-in tools. Other origin values
// remain useful audit metadata, but user, channel, spawned and an explicitly ordinary empty origin all run the same tools.
func assistantOriginToolPolicy(origin string) string {
	if origin == originInbox {
		return originInbox
	}
	return ""
}

// executionFingerprint also detects a managed provider changed before the next turn (any of its fields, the #2455 ones
// included): another process, resumed on the same conversation.
func (n *Node) executionFingerprint(launcher string, m modelExecution) string {
	m.Origin = assistantOriginToolPolicy(m.Origin)
	raw, _ := json.Marshal(m)
	if l, ok := n.managedCatalog(launcher); ok { // as named: the same before and after a withheld one's pull (its Rev)
		v, _ := json.Marshal(l)
		raw = append(raw, v...)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func executionOption(options []modelExecution) modelExecution {
	if len(options) > 0 {
		return options[0]
	}
	return modelExecution{}
}

func (m modelExecution) withSupervision(policy *string) modelExecution {
	if policy != nil {
		m.SupervisionInstructions, m.SupervisionResolved = *policy, true
	}
	return m
}

// assistantModel is the launcher and model the hub runs conversation id's next message on (as a message from the phone
// would be), for a message the node sends it by itself. Started on the model it last ran, the conversation's next message
// from the phone would need another process (#1554). Nothing when the hub can't say (an older hub): the conversation's own.
func (n *Node) assistantModel(ctx context.Context, id string) (string, modelExecution) {
	launcher, execution, _ := n.assistantModelDecision(ctx, id)
	return launcher, execution
}

// assistantModelDecision also re-attests origin for background-task recovery. Its boolean says whether the hub answered
// at all. Only the manager's explicit origin_known attests an ordinary empty origin: a successful empty response, an older
// hub and an unavailable answer all leave a pre-upgrade record unknown and must not start it with ordinary tools.
func (n *Node) assistantModelDecision(ctx context.Context, id string) (string, modelExecution, bool) {
	var res struct {
		Launcher                string                   `json:"launcher"`
		Model                   string                   `json:"model"`
		Effort                  string                   `json:"effort"`
		ModelFingerprint        string                   `json:"model_fingerprint"`
		Runtime                 string                   `json:"runtime"`
		IsMain                  bool                     `json:"is_main"`
		MainCompact             int                      `json:"main_compact_window"`
		External                *proto.ExternalExecution `json:"external"`
		Origin                  string                   `json:"origin"`
		OriginKnown             bool                     `json:"origin_known"`
		SupervisionInstructions *string                  `json:"supervision_instructions"`
		ClaudeInteractive       bool                     `json:"claude_interactive"` // #10058: a send's own flag, here for starts no send makes
	}
	if n.peer == nil || n.peer.Call(ctx, "assistant.model", map[string]any{"session_id": id}, &res) != nil {
		return "", modelExecution{}, false
	}
	return res.Launcher, (modelExecution{Model: res.Model, Effort: res.Effort, UseEffort: res.ModelFingerprint != "", Fingerprint: res.ModelFingerprint, Runtime: n.externalRuntime(id, res.Launcher, res.Runtime), IsMain: res.IsMain, MainCompactWindow: res.MainCompact, External: res.External, Origin: res.Origin, OriginKnown: res.OriginKnown, Interactive: res.ClaudeInteractive}).withSupervision(res.SupervisionInstructions), true
}
