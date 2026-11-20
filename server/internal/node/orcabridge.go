package node

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Orca's structured sessions (docs/plans/plan-orca-support.md "Structured sessions"): Orca's own chats around Claude Code, with no
// terminal, so not the Orca host's (hostorca.go lists terminals). While the machine's engine is Orca the node runs a
// bridge for them in its own process: a runtime (runtimes.go) on one end of a pipe into the runtime host, Orca's
// agentSession.* on the other. A session's content is its Claude transcript here (runtimetranscript.go); the bridge
// lists the sessions from worktree.ps, sends, interrupts, closes, sets modes, and puts Orca's pending approvals and
// questions on the phone as cards. Codex chats are not listed yet.

// orcaRuntimeID is the bridge's runtime id: its sessions' space kind, the Orca host's too, so a worktree's chats and
// terminals share one space.
const orcaRuntimeID = proto.EngineOrca

var (
	// orcaBridgePoll is how often the bridge reads worktree.ps (unchanged answers are a few bytes) and, for sessions
	// waiting for an answer, their pending requests.
	orcaBridgePoll = 1500 * time.Millisecond
	// orcaBridgeRestart is the wait before the bridge runs again after its connection ended.
	orcaBridgeRestart = 2 * time.Second
	// orcaHeadRetry: how long a session whose history could not be read waits before the next try.
	orcaHeadRetry = 10 * time.Second
	// orcaOptionsMaxAge: a session's options are read again at most this often, and only after it changed.
	orcaOptionsMaxAge = 10 * time.Second
	// orcaAskRetry: a card the node could not take (no hub) is offered again after this.
	orcaAskRetry = 5 * time.Second
)

// orcaBridgeState is the bridge the runtime host runs (setOrcaBridge).
type orcaBridgeState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// setOrcaBridge runs the bridge (on) or stops it (off). Called whenever a hub connection says which engine the
// machine uses.
func (rh *RuntimeHost) setOrcaBridge(on bool) {
	b := &rh.orca
	b.mu.Lock()
	defer b.mu.Unlock()
	if on == (b.cancel != nil) {
		return
	}
	if !on {
		b.cancel()
		b.cancel = nil
		done := b.done
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		rh.logger.Printf("orca bridge: stopped (the engine is not Orca any more)")
		return
	}
	base := rh.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	b.cancel, b.done = cancel, make(chan struct{})
	go rh.orcaBridgeLoop(ctx, b.done)
}

// orcaBridgeLoop runs the bridge until ctx ends, again after its connection ends.
func (rh *RuntimeHost) orcaBridgeLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	c := orcaClient()
	for ctx.Err() == nil {
		err := rh.runOrcaBridge(ctx, c)
		if ctx.Err() != nil {
			return
		}
		rh.logger.Printf("orca bridge: %v (again in %s)", err, orcaBridgeRestart)
		select {
		case <-ctx.Done():
		case <-time.After(orcaBridgeRestart):
		}
	}
}

// runOrcaBridge connects one bridge to the runtime host through a pipe and runs it until either end goes.
func (rh *RuntimeHost) runOrcaBridge(ctx context.Context, c *orcart.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hostEnd, bridgeEnd := net.Pipe()
	go func() {
		rh.serveConn(ctx, hostEnd, "")
		cancel()
	}()
	b := newOrcaBridge(c, bridgeEnd, rh.logger.Printf)
	return b.run(ctx, rh.version)
}

// orcaBridge is one connection of the bridge.
type orcaBridge struct {
	c    *orcart.Client
	conn io.ReadWriteCloser
	peer *rpc.Peer
	logf func(format string, args ...any)

	mu      sync.Mutex
	treesID string
	trees   []orcart.Worktree
	orcaOf  map[string]string    // Persona session id (its Claude session) → Orca's session id
	heads   map[string]*orcaHead // by Orca session id
	asks    map[string]*orcaAsk  // by orcaPromptKey
	sends   map[string]time.Time // client_msg_id → when it was first sent (its operation id's time: a retry replays)
	pushed  string               // the rows last pushed
}

// orcaHead is what the bridge knows of one Orca session beyond its worktree.ps row.
type orcaHead struct {
	provider string // Claude's session id: the transcript, and the session's id in Persona
	fence    int64
	failedAt time.Time
	opts     *orcart.Options
	optsAt   time.Time
	optsRev  int64 // the row's updatedAt when opts was read
}

// orcaAsk is one of Orca's prompts put on the phone.
type orcaAsk struct {
	orca    string
	cancel  context.CancelFunc
	retryAt time.Time // the node could not take it: offer it again then
}

func newOrcaBridge(c *orcart.Client, conn io.ReadWriteCloser, logf func(string, ...any)) *orcaBridge {
	return &orcaBridge{c: c, conn: conn, logf: logf, orcaOf: map[string]string{}, heads: map[string]*orcaHead{},
		asks: map[string]*orcaAsk{}, sends: map[string]time.Time{}}
}

// run says hello and keeps the list current until the connection or ctx ends.
func (b *orcaBridge) run(ctx context.Context, version string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer b.conn.Close()
	b.peer = rpc.New("o", b.conn)
	b.peer.OnRequest = b.request
	served := make(chan error, 1)
	go func() { served <- b.peer.Serve(ctx, b.conn) }()
	hello := map[string]any{"runtime": orcaRuntimeID, "name": proto.EngineNames[proto.EngineOrca], "agent": "claude",
		"version": version, "instance": newUUID(),
		"caps": map[string]any{"send": true, "interrupt": true, "close": true, "modes": true}}
	if err := b.peer.Call(ctx, "runtime.hello", hello, nil); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	t := time.NewTicker(orcaBridgePoll)
	defer t.Stop()
	for {
		b.refresh(ctx)
		select {
		case err := <-served:
			if err == nil {
				err = errors.New("the runtime host closed the connection")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// refresh reads worktree.ps, pushes the rows when they changed, and keeps the cards in step with Orca's prompts.
func (b *orcaBridge) refresh(ctx context.Context) {
	b.mu.Lock()
	after := b.treesID
	b.mu.Unlock()
	trees, id, unchanged, err := b.c.Worktrees(ctx, after)
	b.mu.Lock()
	switch {
	case errors.Is(err, orcart.ErrNotRunning):
		b.trees, b.treesID = nil, "" // Orca quit: its chats leave the list
	case err != nil:
		b.treesID = "" // keep the last rows; ask for everything next time
	case unchanged:
	default:
		b.trees, b.treesID = trees, id
	}
	trees = b.trees
	b.mu.Unlock()

	rows := []map[string]any{}
	orcaOf := map[string]string{}
	waiting := map[string]string{} // Orca session id → Persona session id, sessions with a prompt pending
	for _, w := range trees {
		if w.HostID != "" && w.HostID != "local" {
			continue // a worktree on an SSH target: that machine's node lists it
		}
		for _, a := range w.Agents {
			orca, ok := orcart.SessionOf(a.PaneKey)
			if !ok || a.AgentType != "claude" {
				continue
			}
			h := b.head(ctx, orca, a.UpdatedAt)
			if h == nil {
				continue
			}
			row := orcaRow(w, a, h)
			rows = append(rows, row)
			orcaOf[h.provider] = orca
			if row["status"] == proto.StatusBlocked {
				waiting[orca] = h.provider
			}
		}
	}
	b.mu.Lock()
	b.orcaOf = orcaOf
	b.mu.Unlock()
	if raw, err := json.Marshal(rows); err == nil && string(raw) != b.pushed {
		if b.peer.Notify("runtime.sessions", map[string]any{"sessions": rows}) == nil {
			b.pushed = string(raw)
		}
	}
	b.prompts(ctx, waiting)
}

// head is a session's Claude session id and fence (read once from its history) and options (read again after the
// row changed); nil while not known.
func (b *orcaBridge) head(ctx context.Context, orca string, rev int64) *orcaHead {
	var h orcaHead // a copy: the stored one is replaced, never changed
	b.mu.Lock()
	if old := b.heads[orca]; old != nil {
		h = *old
	}
	b.mu.Unlock()
	if h.provider == "" {
		if !h.failedAt.IsZero() && time.Since(h.failedAt) < orcaHeadRetry {
			return nil
		}
		hd, err := b.c.Head(ctx, orca)
		if err != nil || hd.Provider == "" {
			if err != nil && ctx.Err() == nil {
				b.logf("orca bridge: history of %s: %v", orca, err)
			}
			b.mu.Lock()
			b.heads[orca] = &orcaHead{failedAt: time.Now()}
			b.mu.Unlock()
			return nil
		}
		h = orcaHead{provider: hd.Provider, fence: hd.Fence}
	}
	if h.opts == nil || h.optsRev != rev && time.Since(h.optsAt) > orcaOptionsMaxAge {
		if o, err := b.c.Options(ctx, orca); err == nil {
			h.opts = o
		}
		h.optsAt, h.optsRev = time.Now(), rev
	}
	b.mu.Lock()
	b.heads[orca] = &h
	b.mu.Unlock()
	return &h
}

// orcaModes are Persona's permission modes in Orca's words (Claude Code's); Orca has no plan mode.
var orcaModes = map[string]string{"default": "ask", "acceptEdits": "accept-edits", "auto": "auto",
	proto.ModeBypass: "full-access"}

// orcaRow is the runtime.sessions row of a chat (docs/integrations/runtimes.md).
func orcaRow(w orcart.Worktree, a orcart.AgentRow, h *orcaHead) map[string]any {
	title := firstOf(a.ConversationName, a.DisplayName, a.TaskTitle, firstLine(a.Prompt), filepath.Base(w.Path))
	updated := time.Now().UTC()
	if a.UpdatedAt > 0 {
		updated = time.UnixMilli(a.UpdatedAt).UTC()
	}
	row := map[string]any{"session_id": h.provider, "title": title, "cwd": w.Path, "status": orcaChatStatus(a),
		"updated_at": updated, "rev": a.UpdatedAt, "space_id": w.WorktreeID, "space_name": orcaSpaceLabel(w.Branch, w.Path),
		"agent": "claude", "transcript": h.provider}
	if parent := orcaParent(w.WorktreeID, w.ParentWorktreeID); parent != "" {
		row["parent_space_id"] = parent // folds under its parent worktree like the terminals' sessions (#2073)
	}
	if a.LastAssistantMessage != "" {
		row["last_text"] = snippet(a.LastAssistantMessage)
	}
	if o := h.opts; o != nil {
		for mode, theirs := range orcaModes {
			if theirs == o.PermissionMode {
				row["permission_mode"] = mode
			}
		}
		if o.Effort != "" {
			row["effort"] = o.Effort
		}
		if o.Model != "" {
			row["model"] = o.Model
		}
	}
	return row
}

// orcaChatStatus is a chat's status from its row (AgentRow.PaneState): its main agent's own state (the row's folds in
// its subagents), blocked while one of its subagents waits on the user.
func orcaChatStatus(a orcart.AgentRow) string {
	switch a.PaneState() {
	case "working":
		return proto.StatusWorking
	case "blocked", "waiting":
		return proto.StatusBlocked
	case "":
		return proto.StatusIdle
	}
	return proto.StatusDone
}

// orcaPromptKey names one revision of one prompt.
func orcaPromptKey(orca string, p orcart.Prompt) string {
	return fmt.Sprintf("%s/%s/%d", orca, p.ItemID, p.Revision)
}

// prompts puts the pending prompts of the waiting sessions on the phone and takes the cards of those gone (answered
// in Orca, withdrawn) off it.
func (b *orcaBridge) prompts(ctx context.Context, waiting map[string]string) {
	live := map[string]bool{}
	read := map[string]bool{}
	for orca, persona := range waiting {
		ps, err := b.c.Prompts(ctx, orca)
		if err != nil {
			if ctx.Err() == nil {
				b.logf("orca bridge: requests of %s: %v", orca, err)
			}
			continue // its cards stay as they are
		}
		read[orca] = true
		for _, p := range ps {
			key := orcaPromptKey(orca, p)
			live[key] = true
			b.mu.Lock()
			a := b.asks[key]
			due := a == nil || !a.retryAt.IsZero() && time.Now().After(a.retryAt)
			if due {
				b.asks[key] = &orcaAsk{orca: orca}
			}
			b.mu.Unlock()
			if due {
				b.ask(ctx, key, orca, persona, p)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, a := range b.asks {
		_, stillWaiting := waiting[a.orca]
		if live[key] || stillWaiting && !read[a.orca] {
			continue
		}
		if a.cancel != nil {
			a.cancel() // the goroutine takes the card off the phone
		}
		delete(b.asks, key)
	}
}

// ask puts one prompt on the phone and answers Orca with what comes back.
func (b *orcaBridge) ask(ctx context.Context, key, orca, persona string, p orcart.Prompt) {
	ix := orcaInteraction(persona, p)
	if ix == nil {
		b.logf("orca bridge: a %s request Persona cannot show (left to Orca)", p.Body.Kind)
		return
	}
	sum := sha256.Sum256([]byte(key))
	ix.ID = "orca_" + hex.EncodeToString(sum[:8])
	askCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	if a := b.asks[key]; a != nil {
		a.cancel, a.retryAt = cancel, time.Time{}
	}
	b.mu.Unlock()
	go func() {
		defer cancel()
		var res proto.Resolution
		err := b.peer.Call(askCtx, "interaction.create", map[string]any{"interaction": ix}, &res)
		if askCtx.Err() != nil {
			if ctx.Err() == nil {
				_ = b.peer.Notify("interaction.cancel", map[string]any{"id": ix.ID})
			}
			return
		}
		if err != nil {
			b.mu.Lock()
			if a := b.asks[key]; a != nil {
				a.retryAt = time.Now().Add(orcaAskRetry)
			}
			b.mu.Unlock()
			return
		}
		if res.Dismiss {
			return // handed back to the computer: Orca's own window answers it
		}
		from := ""
		b.mu.Lock()
		if head := b.heads[orca]; head != nil && head.opts != nil {
			for mode, theirs := range orcaModes {
				if theirs == head.opts.PermissionMode {
					from = mode
				}
			}
		}
		b.mu.Unlock()
		if err := b.answer(ctx, orca, p, res); err != nil {
			b.logf("orca bridge: answering %s in %s: %v", p.Body.Kind, orca, err)
		} else if ix.Kind == proto.IxPlan && res.Approve != nil && *res.Approve && orcaModes[res.Mode] != "" && res.ModeActor != nil {
			_ = b.peer.Notify(proto.EventModeSet, proto.ModeSetEvent{SessionID: persona, From: from, To: res.Mode, Actor: res.ModeActor})
		}
	}()
}

// orcaTitleTool reads the tool out of an approval's title ("Allow Bash?").
func orcaTitleTool(title string) string {
	t, ok := strings.CutPrefix(title, "Allow ")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(t), "?")
}

// orcaInteraction is the card of one of Orca's prompts; nil for one Persona has no card for.
func orcaInteraction(persona string, p orcart.Prompt) *proto.Interaction {
	body := p.Body
	ix := &proto.Interaction{SessionID: persona, AgentID: p.AgentID}
	switch body.Kind {
	case "approval":
		if body.Subject != nil && body.Subject.Kind == "plan" {
			ix.Kind, ix.Plan = proto.IxPlan, body.Subject.Text
			return ix
		}
		ix.Kind = proto.IxPermission
		ix.ToolName = orcaTitleTool(body.Title)
		if body.MatchedAskRule != nil && body.MatchedAskRule.ToolName != "" {
			ix.ToolName = body.MatchedAskRule.ToolName
		}
		if body.Detail != nil {
			ix.Summary = *body.Detail
			// Claude's tool calls come as their input in JSON: the card shows it as a hook's card does.
			var input map[string]any
			if d := strings.TrimSpace(*body.Detail); strings.HasPrefix(d, "{") && json.Unmarshal([]byte(d), &input) == nil {
				ix.ToolInput = json.RawMessage(d)
				ix.Summary = toolSummary(ix.ToolName, ix.ToolInput)
			}
		}
		if ix.Summary == "" {
			ix.Summary = firstOf(body.Description, body.Title)
		}
		ix.Reason = body.DecisionReason
		return ix
	case "question":
		for _, q := range orcaQuestions(p) {
			pq := proto.Question{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect}
			for _, o := range q.Options {
				pq.Options = append(pq.Options, proto.QuestionOption{Label: o.Label, Description: o.Description})
			}
			ix.Questions = append(ix.Questions, pq)
		}
		if len(ix.Questions) == 0 {
			return nil
		}
		ix.Kind = proto.IxQuestion
		return ix
	}
	return nil
}

// orcaQuestions are a question prompt's questions; an older one with a single question and no ids is one of them.
func orcaQuestions(p orcart.Prompt) []orcart.PromptQuestion {
	if len(p.Body.Questions) > 0 {
		return p.Body.Questions
	}
	if p.Body.Question == "" {
		return nil
	}
	return []orcart.PromptQuestion{{Question: p.Body.Question, Options: p.Body.Options}}
}

// orcaOption is the id of the first of the prompt's options named in ids ("" when it has none of them).
func orcaOption(p orcart.Prompt, ids ...string) string {
	for _, id := range ids {
		for _, o := range p.Body.Options {
			if o.ID == id {
				return id
			}
		}
	}
	return ""
}

var (
	orcaAllow  = []string{"allow", "accept", "yes"}
	orcaAlways = []string{"allowForSession", "acceptForSession"}
	orcaDeny   = []string{"deny", "decline", "no", "cancel"}
)

// answer gives Orca the phone's answer to a prompt: the option picked, the answers written; a refusal's words go into
// the conversation after it (Orca's approvals take no message).
func (b *orcaBridge) answer(ctx context.Context, orca string, p orcart.Prompt, res proto.Resolution) error {
	w := b.write(orca, "")
	switch p.Body.Kind {
	case "approval":
		plan := p.Body.Subject != nil && p.Body.Subject.Kind == "plan"
		allow := res.Allow
		if plan || allow == nil {
			allow = res.Approve
		}
		if allow == nil {
			return nil
		}
		var option string
		switch {
		case !*allow:
			option = orcaOption(p, orcaDeny...)
		case res.Always:
			option = orcaOption(p, append(append([]string{}, orcaAlways...), orcaAllow...)...)
		default:
			option = orcaOption(p, orcaAllow...)
		}
		if option == "" {
			return fmt.Errorf("no option to answer with among %d", len(p.Body.Options))
		}
		if err := b.c.Approve(ctx, w, p, option); err != nil {
			return err
		}
		if *allow && plan && orcaModes[res.Mode] != "" {
			return b.c.SetOption(ctx, b.write(orca, ""), "permissionMode", orcaModes[res.Mode])
		}
		if words := strings.TrimSpace(firstOf(res.Message, res.Feedback)); !*allow && words != "" {
			return b.c.SendSession(ctx, b.write(orca, ""), words, nil)
		}
		return nil
	case "question":
		qs := orcaQuestions(p)
		var answers []orcart.Answer
		legacy := ""
		for _, q := range qs {
			text := strings.TrimSpace(res.Answers[q.Question])
			if text == "" {
				continue
			}
			ids, other := orcaPicked(q, text)
			if q.ID == "" { // an older prompt: one option id, no written answer
				if len(ids) > 0 {
					legacy = ids[0]
				}
				continue
			}
			answers = append(answers, orcart.Answer{QuestionID: q.ID, OptionIDs: ids, Other: other})
		}
		if len(answers) > 4 {
			answers = answers[:4]
		}
		if len(answers) == 0 && legacy == "" {
			return errors.New("no answer Orca can take")
		}
		if answers == nil {
			return b.c.AnswerQuestion(ctx, w, p, nil, legacy)
		}
		for i := range answers {
			if answers[i].OptionIDs == nil {
				answers[i].OptionIDs = []string{}
			}
		}
		return b.c.AnswerQuestion(ctx, w, p, answers, "")
	}
	return nil
}

// orcaPicked is an answer from the phone as a question's options (the labels picked, a multi-select's joined with
// ", ") and the words that are none of them.
func orcaPicked(q orcart.PromptQuestion, text string) (ids []string, other string) {
	byLabel := map[string]string{}
	for _, o := range q.Options {
		byLabel[o.Label] = o.ID
	}
	if id, ok := byLabel[text]; ok {
		return []string{id}, ""
	}
	if !q.MultiSelect {
		return nil, text
	}
	var rest []string
	for _, part := range strings.Split(text, ", ") {
		if id, ok := byLabel[strings.TrimSpace(part)]; ok {
			ids = append(ids, id)
		} else if part = strings.TrimSpace(part); part != "" {
			rest = append(rest, part)
		}
	}
	return ids, strings.Join(rest, ", ")
}

// write is the envelope of a write to an Orca session: its fence and an operation id (the same one for the same
// key, so a retried send replays; key "" is a fresh one).
func (b *orcaBridge) write(orca, key string) orcart.Write {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := orcart.Write{SessionID: orca}
	if h := b.heads[orca]; h != nil {
		w.Fence = h.fence
	}
	at := time.Now()
	if key != "" {
		if first, ok := b.sends[key]; ok && time.Since(first) < 12*time.Hour {
			at = first
		} else {
			b.sends[key] = at
			if len(b.sends) > 500 {
				for k, t := range b.sends {
					if time.Since(t) > time.Hour {
						delete(b.sends, k)
					}
				}
			}
		}
	}
	w.OperationID = orcart.OperationID(at, key)
	return w
}

// request answers the node's calls about the bridge's sessions.
func (b *orcaBridge) request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p struct {
		SessionID      string        `json:"session_id"`
		Text           string        `json:"text"`
		Images         []proto.Image `json:"images"`
		ClientMsgID    string        `json:"client_msg_id"`
		PermissionMode string        `json:"permission_mode"`
		Effort         string        `json:"effort"`
	}
	_ = json.Unmarshal(params, &p)
	ok := map[string]any{"ok": true}
	switch method {
	case "session.watch", "session.unwatch":
		return ok, nil // the transcript is the node's to follow
	}
	b.mu.Lock()
	orca := b.orcaOf[p.SessionID]
	b.mu.Unlock()
	if method == "session.items" && orca != "" { // before the transcript is on disk: nothing yet
		return map[string]any{"items": []any{}, "to_seq": 0}, nil // no history: it is a string (the file's version) when set
	}
	// Not one of Orca's chats: say so, also to session.items, which is how the node asks who owns a session it does
	// not know yet (runtimeFor). An empty page here claimed every such session for good, a new assistant conversation
	// whose transcript was not on disk yet included, and its next message was refused with the line below.
	if orca == "" {
		return nil, &rpc.Error{Code: "session_gone", Message: "这个会话已经不在 Orca 里了"}
	}
	var err error
	switch method {
	case "session.send":
		var paths []string
		if paths, err = orcaImages(p.Images); err == nil {
			key := p.ClientMsgID
			if key != "" {
				key = orca + "/" + key
			}
			err = b.c.SendSession(ctx, b.write(orca, key), p.Text, paths)
		}
	case "session.interrupt":
		err = b.c.CancelSession(ctx, b.write(orca, ""))
	case "session.close":
		err = b.c.CloseSession(ctx, orca)
	case "session.modes":
		if p.PermissionMode != "" {
			mode := orcaModes[p.PermissionMode]
			if mode == "" {
				return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: Orca 没有这个权限模式（" + p.PermissionMode + "）"}
			}
			err = b.c.SetOption(ctx, b.write(orca, ""), "permissionMode", mode)
		}
		if err == nil && p.Effort != "" {
			err = b.c.SetOption(ctx, b.write(orca, ""), "effort", p.Effort)
		}
		b.mu.Lock()
		if h := b.heads[orca]; h != nil {
			c := *h
			c.opts = nil // read again on the next refresh
			b.heads[orca] = &c
		}
		b.mu.Unlock()
	default:
		return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: " + method}
	}
	if err != nil {
		return nil, orcaWriteErr(err)
	}
	return ok, nil
}

// orcaWriteErr is a failed write in the user's words: Orca turned it down, or it is not known whether it happened.
func orcaWriteErr(err error) error {
	var e *orcart.Error
	switch {
	case errors.Is(err, orcart.ErrNotRunning):
		return &rpc.Error{Code: "runtime_away", Message: "Orca 没有在运行"}
	case errors.As(err, &e) && orcart.Refused(err):
		return &rpc.Error{Code: "refused", Message: "Orca 没有接受：" + firstOf(e.Message, e.Code)}
	case errors.As(err, &e) && e.Code == "agent_session_operation_unknown":
		return &rpc.Error{Code: "unknown", Message: "不确定 Orca 是否已经执行，请先在电脑上看一下再重试"}
	}
	return err
}

// orcaImageKeep is how long an image written for Orca stays: it only has to last until Orca has read it.
const orcaImageKeep = 24 * time.Hour

// orcaImages writes the phone's images out as files for Orca (Orca takes images as files on this machine), named by
// their content, in a folder of this user only; images older than orcaImageKeep go first.
func orcaImages(images []proto.Image) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}
	dir, err := privateCacheDir("orca-images")
	if err != nil {
		return nil, fmt.Errorf("image: %w", err)
	}
	pruneOlder(dir, orcaImageKeep)
	var paths []string
	for _, img := range images {
		data := img.Data
		if _, after, ok := strings.Cut(data, ";base64,"); ok {
			data = after
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("image: %w", err)
		}
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[img.Mime]
		if ext == "" {
			ext = ".png"
		}
		sum := sha256.Sum256(raw)
		p := filepath.Join(dir, hex.EncodeToString(sum[:12])+ext)
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}
