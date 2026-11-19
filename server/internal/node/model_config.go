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
	Model  string
	Effort string
	// ServiceTier is a one-shot native speed request for a new Codex conversation. It is consumed before the first turn,
	// never kept as process state or included in the execution fingerprint.
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
	// Origin is the conversation's manager-recorded source. It is authorization metadata, not a model choice, so it
	// never changes the execution fingerprint; the node carries it into session state and model tool calls.
	Origin string `json:"-"`
	// Interject: a message this one carries, sent while the conversation answers, moves its foreground work to the
	// background (assistant_interject.go). Per message, never part of the fingerprint.
	Interject bool `json:"-"`
	// Interactive: run a Claude conversation as interactive Claude Code instead of claude -p (assistant_tui.go), the
	// assistant's claude_interactive setting. Part of the fingerprint, so a change resumes an idle conversation in a
	// new process; omitted when off, so fingerprints from before it stay the same.
	Interactive bool `json:",omitempty"`
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

// executionFingerprint also detects a managed provider changed before the next turn (any of its fields, the #2455 ones
// included): another process, resumed on the same conversation.
func (n *Node) executionFingerprint(launcher string, m modelExecution) string {
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
// would be), for a message the node sends it by itself (recoverAssistantTasks). Started on the model it last ran, the
// conversation's next message from the phone would need another process (#1554). Nothing when the hub can't say (an
// older hub): the conversation's own then.
func (n *Node) assistantModel(ctx context.Context, id string) (string, modelExecution) {
	var res struct {
		Launcher                string                   `json:"launcher"`
		Model                   string                   `json:"model"`
		Effort                  string                   `json:"effort"`
		ModelFingerprint        string                   `json:"model_fingerprint"`
		Runtime                 string                   `json:"runtime"`
		IsMain                  bool                     `json:"is_main"`
		MainCompact             int                      `json:"main_compact_window"`
		External                *proto.ExternalExecution `json:"external"`
		SupervisionInstructions *string                  `json:"supervision_instructions"`
		ClaudeInteractive       bool                     `json:"claude_interactive"` // #10058: a send's own flag, here for starts no send makes
	}
	if n.peer == nil || n.peer.Call(ctx, "assistant.model", map[string]any{"session_id": id}, &res) != nil {
		return "", modelExecution{}
	}
	return res.Launcher, (modelExecution{Model: res.Model, Effort: res.Effort, UseEffort: res.ModelFingerprint != "", Fingerprint: res.ModelFingerprint, Runtime: n.externalRuntime(id, res.Launcher, res.Runtime), IsMain: res.IsMain, MainCompactWindow: res.MainCompact, External: res.External, Interactive: res.ClaudeInteractive}).withSupervision(res.SupervisionInstructions)
}
