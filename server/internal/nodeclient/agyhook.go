package nodeclient

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// AgyFlag marks the entries Persona puts in the Antigravity CLI's own hooks file (node/agyhooks.go):
// `<binary> hook --agy <Event>`. The hook only reports; the TUI stays the one writer of its conversation.
const AgyFlag = "--agy"

// AgyHookKind is the node socket request that carries one report (node/agyhooks.go).
const AgyHookKind = "agy.hook"

// AgyEvents are the agy hook events Persona listens to: PreInvocation names the conversation a pane is in, Stop ends a
// turn.
var AgyEvents = []string{"PreInvocation", "Stop"}

// AgyReport is what one agy hook tells the node about the pane it runs in.
type AgyReport struct {
	Event          string `json:"event"`
	Conversation   string `json:"conversation"`
	TranscriptPath string `json:"transcript_path,omitempty"`
}

var agyUUID = lazyre.New(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// agyInputLimit bounds what the hook reads from agy: the fields it needs are a few hundred bytes.
const agyInputLimit = 1 << 20

// agyHookWait bounds the whole hook (reading agy's input, the node's answer): agy waits for it before its turn goes on.
const agyHookWait = 2 * time.Second

// ParseAgyReport reads agy's hook input (a JSON object with conversationId and transcriptPath) for event. False when
// the event is not one Persona listens to or the input names no conversation id in UUID form.
func ParseAgyReport(event string, input []byte) (AgyReport, bool) {
	known := false
	for _, e := range AgyEvents {
		known = known || e == event
	}
	var in struct {
		Conversation string `json:"conversationId"`
		Transcript   string `json:"transcriptPath"`
	}
	if !known || json.Unmarshal(input, &in) != nil || !agyUUID.MatchString(in.Conversation) {
		return AgyReport{}, false
	}
	return AgyReport{Event: event, Conversation: strings.ToLower(in.Conversation), TranscriptPath: in.Transcript}, true
}

// RunAgyHook is `<binary> hook --agy <Event>`: agy's hook input on stdin, `{}` on stdout whatever happens, so agy goes
// on at once (no node, a pane no engine knows, input that is not agy's: nothing is reported).
func RunAgyHook(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) {
	defer func() { _, _ = io.WriteString(stdout, "{}\n") }()
	if len(args) == 0 {
		return
	}
	pane := HookPane(getenv)
	if pane == "" {
		return
	}
	if _, err := os.Stat(SocketPath()); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, agyHookWait)
	defer cancel()
	read := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(stdin, agyInputLimit))
		read <- b
	}()
	var input []byte
	select {
	case input = <-read:
	case <-ctx.Done():
		return
	}
	rep, ok := ParseAgyReport(args[0], input)
	if !ok {
		return
	}
	body, err := json.Marshal(rep)
	if err != nil {
		return
	}
	_, _ = Dial(ctx, Request{Kind: AgyHookKind, Input: body, Pane: pane})
}
