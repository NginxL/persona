package node

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Switching the model of a running Claude Code session from the app (issue #2434): the model picker in the composer
// sends {"model": id} with session.modes, and the node types `/model <id>` into the agent's box like a person would,
// through the same guards as a message (sendsafe.go): a dialog with the keyboard or a draft in the box refuses the
// switch with nothing typed. The screen is read again afterwards, so a rejected or unknown model is reported instead
// of claimed.
//
// Side effect to know: recent Claude Code saves the model a typed `/model <id>` picks as the default for new sessions
// too ("Set model to `Opus 5.5` and saved as your default for new sessions", seen in local transcripts); there is no
// session-only form of the command, and the popover says so.
//
// Only Claude Code's TUI sessions: a Codex thread on its app-server keeps its effort-only control (codex_effort.go),
// a headless Persona conversation switches model in its own chip, and every other agent has no command to type.

// modelCommand is the line typed into Claude Code to switch the session to model: an id from the agent's own list
// (aliases such as opus, a full id, a bracketed variant like opus[1m], a gateway's provider/model) or one the user
// typed, held to the rule every model is (proto.ValidModelID, #1644). An empty model would open the picker, a model
// with spaces or a leading dash would be more than one word, so none of them is built.
func modelCommand(model string) (string, error) {
	model = strings.TrimSpace(model)
	if !proto.ValidModelID(model) {
		return "", fmt.Errorf("invalid model %q", model)
	}
	return "/model " + model, nil
}

// modelOutcome reads what Claude Code printed after `/model <model>`: "Set model to …" is the change; a refusal is
// returned with its words; anything else (the command still in the box, a picker) is not claimed as done.
func modelOutcome(screen, model string) error {
	flat := strings.Join(strings.Fields(screen), " ")
	// Only what follows the command we typed counts: an earlier switch's line stays on screen.
	if i := strings.LastIndex(flat, "/model "+model); i >= 0 {
		flat = flat[i:]
	}
	if strings.Contains(flat, "Set model to") {
		return nil
	}
	for _, bad := range []string{"not found", "Invalid model", "invalid model", "Unknown model", "not available", "does not exist"} {
		if strings.Contains(flat, bad) {
			return fmt.Errorf("Claude Code 没接受这个模型（%s），请在终端里确认", model)
		}
	}
	return modelUnconfirmed{model}
}

// modelUnconfirmed is modelOutcome's answer when the screen shows neither the change nor a refusal.
type modelUnconfirmed struct{ model string }

func (e modelUnconfirmed) Error() string {
	return fmt.Sprintf("模型可能没切到 %s，请在终端里确认", e.model)
}

// modelLook is the pause between two looks at the screen after typing (a test shortens it).
var modelLook = 200 * time.Millisecond

// modelRecordMost is how much of the transcript past the typing is read for the switch's record: more than that
// written since and the record is not looked for any more.
const modelRecordMost = 4 << 20

// modelRecorded: Claude Code wrote this switch into the transcript at path since offset: the command with its
// argument, then its output line "Set model to …", which it writes when it runs the command, whatever the screen
// shows. read is false when the transcript could not be read (looked at again), or holds more than modelRecordMost
// since offset.
func modelRecorded(path string, offset int64, model string) (recorded, read bool) {
	if path == "" {
		return false, false
	}
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false, false
	}
	added, err := io.ReadAll(io.LimitReader(f, modelRecordMost+1))
	if err != nil || len(added) > modelRecordMost {
		return false, false
	}
	return modelRecordIn(added, model), true
}

// modelRecordIn: added, what a transcript got since a `/model <model>` was typed, records that command as carried
// out. The argument ties the record to this switch, and the output must be that command's own: the first one after
// it, before any other command (a refused switch followed by another the user typed is not this one made).
func modelRecordIn(added []byte, model string) bool {
	i := bytes.LastIndex(added, []byte("<command-args>"+model+"</command-args>"))
	if i < 0 {
		return false
	}
	rest := added[i+1:]
	out := bytes.Index(rest, []byte("<local-command-std"))
	if out < 0 {
		return false
	}
	if next := bytes.Index(rest, []byte("<command-name>")); next >= 0 && next < out {
		return false
	}
	return bytes.HasPrefix(rest[out:], []byte("<local-command-stdout>Set model to"))
}

// modelPicker: Claude Code's /model picker is on screen (the command was taken as a bare /model, or a pick is open).
func modelPicker(screen string) bool {
	return strings.Contains(screen, "Select model") || (strings.Contains(screen, "Enter to confirm") && strings.Contains(screen, "Esc to exit"))
}

// pendingModel is a model the app switched to and the transcript has not shown yet.
type pendingModel struct {
	Model string
	// From is the model the transcript showed when it was switched: while it still shows that one, no reply has been
	// written since, so the app's pick stands.
	From string
	At   time.Time
}

// pendingModelTTL bounds how long a pick stands in for the transcript (a switch followed by no reply at all).
const pendingModelTTL = time.Hour

// awaitedModel is a switch typed into Claude Code that the wait after typing did not confirm. Claude Code can take
// longer than that wait to run a typed command (twelve seconds were seen on a long session, #10810): the switch is
// reported as unconfirmed, as before, and when Claude Code's own record of it shows in the transcript it is noted as
// the pick it is, so the app shows the model the session really runs.
type awaitedModel struct {
	Model, From string
	Path        string // the transcript
	Offset      int64  // where it ended when the command was typed: only what follows is this command's record
	Checked     int64  // its size at the last look: looked at again only once it grew
	At          time.Time
}

// awaitedModelTTL is how long a typed switch is still looked for.
const awaitedModelTTL = 5 * time.Minute

func (n *Node) awaitModel(id string, a awaitedModel) {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.modes[id]
	m.Awaited = &a
	n.modes[id] = m
}

// takeAwaitedModel notes an awaited switch as made once Claude Code recorded it, and lets go of one too old.
func (n *Node) takeAwaitedModel(id string) {
	n.mu.Lock()
	a := n.modes[id].Awaited
	n.mu.Unlock()
	if a == nil {
		return
	}
	recorded, size := false, a.Checked
	expired := time.Since(a.At) > awaitedModelTTL
	if !expired {
		st, err := os.Stat(a.Path)
		if err != nil || st.Size() == a.Checked {
			return
		}
		read := false
		if recorded, read = modelRecorded(a.Path, a.Offset, a.Model); !read {
			if st.Size()-a.Offset <= modelRecordMost {
				return // not read this time: looked at again
			}
			expired = true // too much written since: its record is not looked for any more
		}
		size = st.Size()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.modes[id]
	if m.Awaited != a { // another switch since
		return
	}
	switch {
	case recorded:
		m.Awaited, m.Model = nil, &pendingModel{Model: a.Model, From: a.From, At: time.Now()}
	case expired:
		m.Awaited = nil
	default:
		next := *a
		next.Checked = size
		m.Awaited = &next
	}
	n.modes[id] = m
}

// shownModel is the model the session list shows: the app's pick while the transcript has not moved on from the model
// it had at the switch.
func (n *Node) shownModel(id, fromTranscript string) string {
	n.takeAwaitedModel(id)
	n.mu.Lock()
	m, ok := n.modes[id]
	n.mu.Unlock()
	if !ok || m.Model == nil {
		return fromTranscript
	}
	if fromTranscript != m.Model.From || time.Since(m.Model.At) > pendingModelTTL {
		return fromTranscript
	}
	return m.Model.Model
}

// transcriptModel is the model the session's transcript last shows, "" when there is none.
func (n *Node) transcriptModel(id string) string {
	p := n.transcriptPath(id)
	if p == "" {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return n.meta(p, st).model
}

func (n *Node) noteModel(id, model, from string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.modes[id]
	m.Model, m.Awaited = &pendingModel{Model: model, From: from, At: time.Now()}, nil
	n.modes[id] = m
}

// setModel switches a running Claude Code session to model.
func (n *Node) setModel(ctx context.Context, id, model string) error {
	if st, ok := n.state.get(id); ok && strings.TrimSpace(model) != "" {
		// A tier alias on a gateway provider is its model for that tier (#10365); one it names none for is left out as a
		// start leaves out its --model: `/model default` puts the session back on the provider's default, and no alias
		// reaches the gateway as a model's name.
		model = cmp.Or(n.claudeModelArg(st.Launcher, strings.TrimSpace(model)), "default")
	}
	cmd, err := modelCommand(model)
	if err != nil {
		return err
	}
	model = strings.TrimSpace(model)
	if n.isAssistantSession(id) {
		return fmt.Errorf("Persona 对话的模型在对话上方的芯片里换")
	}
	if n.externalCodex(id) || n.codexPath(id) != "" || n.traexPath(id) != "" {
		return fmt.Errorf("Codex / TRAEX 的会话不支持在手机上切换模型")
	}
	if err := n.switchingRefusal(id); err != nil { // a launcher switch, maybe on the last connection's Node
		return err
	}
	// One at a time with the messages: a send in flight is typing into the same box.
	defer n.typing.lock(id)()
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		return n.noPaneForModes(id)
	}
	if a, ok := n.agent(id); ok {
		if a.Agent != "" && a.Agent != "claude" {
			return fmt.Errorf("这个 Agent 不支持在手机上切换模型")
		}
		if a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked {
			return fmt.Errorf("Agent 正在工作，这一轮结束后再切换模型")
		}
	}
	// The same checks as a message: a dialog with the keyboard or a draft in the box refuses, nothing typed.
	if err := n.checkPane(ctx, pane, "claude", ""); err != nil {
		return err
	}
	// The transcript as it is now: its model, and where it ends. What Claude Code writes after this is its record of
	// the command typed below.
	record, from, recordFrom := n.transcriptPath(id), "", int64(0)
	if st, err := os.Stat(record); record != "" && err == nil {
		from, recordFrom = n.meta(record, st).model, st.Size()
	} else {
		record = ""
	}
	if err := n.prompt(ctx, pane, cmd); err != nil {
		if r := deliveryRefusal(err); r != nil {
			return r
		}
		return uncertain(err)
	}
	n.typing.typed(pane, cmd)
	var last string
	recorded := false
	for range 15 {
		time.Sleep(modelLook)
		if recorded, _ = modelRecorded(record, recordFrom, model); recorded {
			break // Claude Code's own word, whatever the screen shows by now
		}
		s, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil {
			continue
		}
		last = s
		if modelPicker(s) { // taken as a bare /model: leave it as we found it
			_ = n.sendKeys(ctx, pane, "esc")
			return fmt.Errorf("Claude Code 打开了模型选择器而没有直接切换，已取消")
		}
		if modelOutcome(s, model) == nil {
			break
		}
	}
	if !recorded {
		if err := modelOutcome(last, model); err != nil {
			if errors.As(err, &modelUnconfirmed{}) && record != "" {
				// Typed, and Claude Code may yet run it: its record, when it comes, is taken up (takeAwaitedModel).
				n.awaitModel(id, awaitedModel{Model: model, From: from, Path: record, Offset: recordFrom, Checked: recordFrom, At: time.Now()})
			}
			return err
		}
	}
	n.noteModel(id, model, from)
	n.notifySessions()
	return nil
}

// setModelAndModes applies a request of session.modes: the model first (the efforts a model takes depend on it),
// then the mode and effort.
func (n *Node) setModelAndModes(ctx context.Context, id, model, mode, effort string) error {
	if model != "" {
		if err := n.setModel(ctx, id, model); err != nil {
			return err
		}
	}
	if mode == "" && effort == "" {
		return nil
	}
	return n.setModes(ctx, id, mode, effort)
}
