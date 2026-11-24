package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// Forking a conversation ("Branch to New Chat", #2137): a new Claude Code session that knows the conversation up to one of its
// replies, the original untouched. The transcript is copied under a new id up to the end of the turn that reply is in
// (so no tool call is cut from its result) and the copy is an offline session like any other: the next message to it
// resumes it (claude --resume <new id>) in a new tab, or in the assistant's process for an assistant conversation.
// `claude --resume --fork-session` copies the whole conversation only, and --resume-session-at, which would cut it,
// is not honoured by an interactive claude (tried 2026-11-05), so the node makes the copy itself.

// forkable checks for a native fork or a local Claude transcript that the node can copy.
func (n *Node) forkable(s proto.Session) bool {
	if !forkTarget(s) {
		return false
	}
	if s.SourceKind == proto.SourceRuntime {
		if s.Capabilities != nil && s.Capabilities.Fork {
			return true
		}
		t := n.runtimes.transcriptOf(s.SessionID)
		return t != "" && (s.Agent == "" || s.Agent == "claude") &&
			!n.runtimeAssistant(s.SessionID) && n.claudeTranscript(t, false) != ""
	}
	if _, codex := n.codexSource(s.SessionID); codex || s.SourceKind != "" || (s.Agent != "" && s.Agent != "claude") || !validID(s.SessionID) || n.runtimeAssistant(s.SessionID) {
		return false
	}
	return n.claudeTranscript(s.SessionID, false) != ""
}

func forkTarget(s proto.Session) bool {
	return !s.Starting && !s.Archived && !s.IsSubagent && !strings.Contains(s.SessionID, subagentSep) && s.Agent != "codex"
}

// claudeTranscript is session id's own Claude transcript (<id>.jsonl), or "". look: find it when it is not known yet;
// the session list does not (it has looked for every row already, and a glob per row on every rebuild is costly).
func (n *Node) claudeTranscript(id string, look bool) string {
	var p string
	if look {
		p = n.transcriptPath(id)
	} else {
		n.mu.Lock()
		p = n.paths[id]
		n.mu.Unlock()
	}
	if p == "" || filepath.Base(p) != id+".jsonl" {
		return ""
	}
	return p
}

// fork copies session id's transcript up to the turn of item at (a transcript item id, "<uuid>#<n>"; empty: all of
// it) into a new session and returns the new session's id.
func (n *Node) fork(ctx context.Context, id, at string) (string, error) {
	if h, _, _ := n.runtimes.belongs(id); h != nil {
		s, ok := h.row(id)
		if !ok {
			h.mu.Lock()
			s = proto.Session{SessionID: id, SourceKind: proto.SourceRuntime, Agent: h.hello.Agent}
			h.mu.Unlock()
		}
		return n.forkRuntime(ctx, s, at)
	}
	if !validID(id) {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	a, live := n.agent(id)
	src := n.claudeTranscript(id, true)
	if src == "" || !n.forkable(proto.Session{SessionID: id, Agent: a.Agent}) {
		return "", refusal("fork_unsupported", "这个会话不能分支：只支持 Claude Code 的会话", nil)
	}
	uuid, _, _ := strings.Cut(at, "#")
	newID := newUUID()
	dst := filepath.Join(filepath.Dir(src), newID+".jsonl")
	var title, first string
	if st, err := os.Stat(src); err == nil {
		m := n.meta(src, st)
		title, first = m.title, m.first
	}
	whole, err := copyTranscriptUntil(src, dst, id, newID, uuid, forkTitle(title, first))
	if err != nil {
		return "", err
	}
	if whole && (live && a.AgentStatus == proto.StatusWorking || n.assistantBusy(id)) {
		// The turn is still being written: the copy could end on a tool call without its result.
		_ = os.Remove(dst)
		return "", refusal("fork_turn_running", "这一轮还没做完，等它停下再分支", nil)
	}
	if st, ok := n.state.get(id); ok { // resumed with what the original ran on: launcher, directory, model
		// A guest conversation's branch is one too (assistant_guest.go); an assistant branch keeps its origin/profile metadata.
		next := sessionState{Launcher: st.Launcher, Cwd: st.Cwd, Model: st.Model, ModelFingerprint: st.ModelFingerprint, ModelEffort: st.ModelEffort, Guest: st.Guest}
		if identity.IsAssistantDir(st.Cwd) || n.isAssistantSession(id) {
			next.Origin, next.OriginKnown = st.Origin, st.OriginKnown
			next.SupervisionInstructions = st.SupervisionInstructions
			next.ConfigDir = st.ConfigDir
			next.FollowsSetting = st.FollowsSetting
		}
		n.state.put(newID, next)
	}
	n.forkMade(newID, dst)
	return newID, nil
}

// forkRuntime delegates to a runtime that can fork, or copies the Claude transcript it names (#6298). The copy is of that
// transcript, under a new Claude session id, named after the session. Like any branch it is an offline Claude session;
// its first message continues it with the machine's engine (on a Paseo machine Paseo takes it over, runtimecontrol.go).
// The node keeps state for it (its directory, model and launcher): the copy reads like the runtime's own runs of Claude
// Code's command line, which the offline list leaves out, and a session the node keeps state for is listed
// (claude_oneshot.go). The launcher is the one the session ran on (Paseo: its provider), which the takeover asks for:
// without it Paseo went on with whichever Claude provider ran last, maybe another account's (#8130).
func (n *Node) forkRuntime(ctx context.Context, s proto.Session, at string) (string, error) {
	if h, _, _ := n.runtimes.belongs(s.SessionID); h != nil {
		h.mu.Lock()
		caps := h.hello.Caps
		h.mu.Unlock()
		if caps.Fork {
			if !forkTarget(s) {
				return "", refusal("fork_unsupported", "This session cannot be forked", nil)
			}
			return n.forkOnRuntime(ctx, h, s, at)
		}
	}
	t := n.runtimes.transcriptOf(s.SessionID)
	src := ""
	if t != "" {
		src = n.claudeTranscript(t, true)
	}
	if src == "" || !n.forkable(s) {
		return "", refusal("fork_unsupported", "这个会话不能分支：只支持 Claude Code 的会话", nil)
	}
	uuid, _, _ := strings.Cut(at, "#")
	newID := newUUID()
	dst := filepath.Join(filepath.Dir(src), newID+".jsonl")
	var first string
	if st, err := os.Stat(src); err == nil {
		first = n.meta(src, st).first
	}
	whole, err := copyTranscriptUntil(src, dst, t, newID, uuid, forkTitle(s.Title, first))
	if err != nil {
		return "", err
	}
	if whole && (s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked) {
		// The turn is still being written: the copy could end on a tool call without its result.
		_ = os.Remove(dst)
		return "", refusal("fork_turn_running", "这一轮还没做完，等它停下再分支", nil)
	}
	n.state.put(newID, sessionState{Launcher: s.Launcher, Cwd: s.Cwd, Model: s.Model})
	n.forkMade(newID, dst)
	return newID, nil
}

func (n *Node) forkOnRuntime(ctx context.Context, h *runtimeConn, source proto.Session, at string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	var out struct {
		ID      string         `json:"session_id"`
		Session *proto.Session `json:"session"`
	}
	assistant := n.runtimeAssistant(source.SessionID)
	params := map[string]any{"session_id": source.SessionID, "at_item_id": at, "assistant": assistant}
	if assistant {
		st, _ := n.state.get(source.SessionID)
		if env := n.assistantEnvForRuntime(h, n.runtimeAssistantCwd(source.SessionID, source.Cwd, st)); env != nil {
			params["env"] = env
		}
	}
	if err := h.peer.Call(ctx, "session.fork", params, &out); err != nil {
		return "", err
	}
	if !runtimeIDRe.MatchString(out.ID) || out.ID == source.SessionID || out.Session != nil && out.Session.SessionID != out.ID {
		return "", errors.New("runtime returned an invalid forked session")
	}
	n.runtimes.claim(h, out.ID)
	if assistant || out.Session != nil && identity.IsAssistantDir(out.Session.Cwd) {
		st, _ := n.state.get(source.SessionID)
		st.Cwd = firstNonEmpty(st.Cwd, source.Cwd)
		st.Launcher = firstNonEmpty(st.Launcher, source.Launcher)
		st.Model = firstNonEmpty(st.Model, source.Model)
		if out.Session != nil {
			st.Cwd = firstNonEmpty(st.Cwd, out.Session.Cwd)
			st.Launcher = firstNonEmpty(st.Launcher, out.Session.Launcher)
		}
		n.state.put(out.ID, sessionState{Cwd: st.Cwd, Launcher: st.Launcher, Model: st.Model,
			ModelFingerprint: st.ModelFingerprint, ModelEffort: st.ModelEffort, AppMode: st.AppMode, AppModeExplicit: st.AppModeExplicit, AppEffort: st.AppEffort,
			Origin: st.Origin, OriginKnown: true, StartedAt: time.Now().UTC()})
	}
	if out.Session != nil {
		if _, err := n.listStarted(h, *out.Session, ""); err != nil {
			return "", err
		}
	}
	return out.ID, nil
}

// forkMade lists a new branch at once: its transcript, however short, and its first message starting it.
func (n *Node) forkMade(newID, dst string) {
	n.setPath(newID, dst)
	n.mu.Lock()
	if n.forked == nil {
		n.forked = map[string]bool{}
	}
	n.forked[newID] = true // listed however short it is (scanOffline skips files under 2 KB)
	if n.freshForks == nil {
		n.freshForks = map[string]bool{}
	}
	n.freshForks[newID] = true // its first message starts it (stopped.go)
	n.mu.Unlock()
	offlineCache.Lock()
	offlineCache.at = time.Time{} // listed at once, not with the next scan
	offlineCache.Unlock()
	n.notifySessions()
}

// forkTitle is the copy's name: the original's (else its first prompt, cut to 40 characters) with the Chinese "Branch · " prefix in front,
// once.
func forkTitle(title, first string) string {
	title = strings.TrimSpace(title)
	if title == "" || title == "Claude Code" {
		title = snippet(first)
		if r := []rune(title); len(r) > 40 {
			title = string(r[:40]) + "…"
		}
	}
	if title == "" {
		return ""
	}
	if strings.HasPrefix(title, "分支 · ") {
		return title
	}
	return "分支 · " + title
}

// copyTranscriptUntil writes dst (a new file) with src's complete lines, oldID's sessionId replaced by newID, up to the
// end of the turn the line with uuid is in: the line before the next prompt the user typed. An empty uuid copies every
// line. A title adds a custom-title line, the name /rename would give it. whole: the copy ran to the end of src (the
// turn may still be going on). Lines are copied as they are, only the id swapped, so nothing else in them changes.
func copyTranscriptUntil(src, dst, oldID, newID, uuid, title string) (whole bool, err error) {
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			_ = os.Remove(dst)
		}
	}()
	w := bufio.NewWriterSize(out, 1<<16)
	r := bufio.NewReaderSize(in, 1<<16)
	found := uuid == ""
	lines := 0
	oldKey, newKey := []byte(`"sessionId":"`+oldID+`"`), []byte(`"sessionId":"`+newID+`"`)
	whole = true
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if !bytes.HasSuffix(line, []byte("\n")) { // the end of the file, or a line still being written
			break
		}
		var e map[string]json.RawMessage
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if found && uuid != "" && typedPrompt(e) {
			whole = false
			break // the next turn starts here
		}
		if !found && string(e["uuid"]) == `"`+uuid+`"` {
			found = true
		}
		line = bytes.ReplaceAll(line, oldKey, newKey)
		if _, err := w.Write(line); err != nil {
			return false, err
		}
		lines++
	}
	if !found {
		return false, refusal("fork_item_missing", "找不到要分支的那条消息", nil)
	}
	if lines == 0 {
		return false, errors.New("the conversation is empty")
	}
	if title != "" {
		b, _ := json.Marshal(map[string]string{"type": "custom-title", "customTitle": title, "sessionId": newID})
		if _, err := w.Write(append(b, '\n')); err != nil {
			return false, err
		}
	}
	if err := w.Flush(); err != nil {
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	ok = true
	return whole, nil
}

// assistantBusy: id is an assistant conversation answering right now.
func (n *Node) assistantBusy(id string) bool {
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p == nil || !p.alive() {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.busy
}

// typedPrompt: the transcript line is a message the user sent (a turn's start), not a tool's result, a hook's note or
// a subagent's line.
func typedPrompt(e map[string]json.RawMessage) bool {
	if string(e["type"]) != `"user"` || string(e["isMeta"]) == "true" || string(e["isSidechain"]) == "true" {
		return false
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(e["message"], &m) != nil {
		return false
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return true
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return true
		}
	}
	return false
}
