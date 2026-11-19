package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Claude Code's peer inbox (read from its 2.1.308 bundle; 2.1.291 is the same; docs/integrations/claude-integration.md
// "The peer inbox"): every interactive claude since 2.1.29x listens on a local socket for messages other Claude
// sessions send it (SendMessage), and names it in its sessions/<pid>.json as messagingSocketPath, with peerProtocol 1.
// On Windows it is a named pipe, \\.\pipe\LOCAL\cc-msg-<32 hex>; on macOS and Linux a unix socket,
// $XDG_RUNTIME_DIR/cc-socks/<pid>.sock or /tmp/cc-socks-<uid>/<pid>.sock.
//
// The wire is one JSON object per line, written by the sender and never answered on the connection: the sender writes
// its lines and closes. The first line may be {"type":"auth","token":"<peerToken>"}; on Windows it must be (an inbox
// there drops a connection whose first line is not a good auth line). The token is in <config dir>/sessions/
// <pid>.<sha256 of the canonical socket path>.key, a small JSON file only this user may read: {"peerToken": 32 hex,
// "procStartFt" / "procStart", "pidDomain"}. A message is {"type":"user","message":{"role":"user","content":"…"}}
// with optional msgV (1), msg_id (a uuid), priority ("next", the default; "now" or "later"), from ("uds:<socket>",
// the sender's own inbox) and session_id (when set and not the session's, the line is dropped).
//
// The content is what the model reads, after Claude Code's own line "Another Claude session sent a message:" and its
// warning that a peer is not the user. A Claude sender wraps the body in <cross-session-message from=… from-session=…
// hop-chain=… from-name=… from-mode=… from-plugin=…> (attributes in that order, each optional; 2.1.326); the session
// takes the sender's name, its mode and the body from that envelope into the transcript line's origin, and a message
// without one is written with neither (transcript/peer.go peerMessage then has nothing to show). So the node wraps it
// too, named "Persona" (peerSender), with no from (it has no inbox of its own) and with from-mode only as peerAttest says:
// the class of whoever's words it carries, never one chosen to suit the receiver.
//
// What the session does with it is its own business, and the sender is not told: it may take it (queued: run as a
// turn when the session rests, absorbed at the next tool boundary while it works), hold it for the person at the
// computer to approve (from-mode below; Claude Code's crossSessionInbound setting can say accept, hold or refuse
// instead), refuse it, or drop it (rate limit, the same body as the last within 30 s, a full queue). Receipts go back
// only to a sender's own inbox. So a send is
// confirmed from the transcript: the session writes a queue-operation (or the message's line) when it takes one
// (peerTaken); a message that does not show there in peerConfirmWait is reported as not confirmed, never as sent.
//
// from-mode is the sender's permission-mode class, "bypass" or "prompting" (2.1.326, read from its bundle). The session
// puts itself in a class too: bypass in bypassPermissions (and in plan mode entered with bypass available), prompting
// otherwise. A message whose sender states the session's own class is taken; one that states the other class is held
// (mode-mismatch); one that states none is taken by a prompting session and held by a bypass one (no-mode-asserted).

const (
	// peerProtocol is the only messagingSocketPath protocol the node speaks (sessions/<pid>.json peerProtocol).
	peerProtocol = 1
	// peerLineMax is the inbox's line cap (1 MiB, characters): a longer line drops the connection.
	peerLineMax = 1 << 20
	// peerDialWait bounds connecting and the inbox reading the line.
	peerDialWait = 5 * time.Second
	// peerKeyMax: a key file is a few hundred bytes; Claude Code reads at most 4 KiB of one.
	peerKeyMax = 4 << 10
	// peerSender is the name the node signs its messages with (the envelope's from-name, the chat's "From"); the
	// transcript reader takes the user's words out of a message signed so.
	peerSender = transcript.PersonaPeer
	// peerTag is Claude Code's envelope for a message from another session.
	peerTag = "cross-session-message"
	// from-mode's classes: a sender that runs with no permission prompts, and one that prompts (peerAttest).
	peerModeBypass    = "bypass"
	peerModePrompting = "prompting"
)

// peerConfirmWait is how long a send waits for the session to write that it took the message.
var peerConfirmWait = 6 * time.Second

// Refusals of a send through the peer inbox (nothing reached the session; the app shows why).
const (
	peerUnsupported = "peer_unsupported" // no inbox, or not one the node can speak to (protocol, platform)
	peerUnreachable = "peer_unreachable" // the inbox is not there, or its key is missing
	peerTooLong     = "peer_too_long"    // longer than the inbox takes in one line
	peerUnconfirmed = "peer_unconfirmed" // written, but the session has not taken it (held, dropped): delivery unknown
	peerHeld        = "peer_held"        // written, and the session wrote that it holds it for approval at the computer (peerHeldSeen): delivery unknown
	peerCommand     = "peer_command"     // a slash command, which the inbox does not run
)

// claudePeer is where a running claude's inbox is and how to reach it, from its sessions/<pid>.json.
type claudePeer struct {
	pid      int
	socket   string // messagingSocketPath
	protocol int    // peerProtocol
	keyDir   string // the sessions directory its key file is in
	fromMode string // the class the node states in the envelope's from-mode (peerAttest); "" states none
}

// peerOf is c's inbox, ok false when its file names none.
func peerOf(c claudeProc) (claudePeer, bool) {
	if c.MessagingSocketPath == "" {
		return claudePeer{}, false
	}
	return claudePeer{pid: c.pid, socket: c.MessagingSocketPath, protocol: c.PeerProtocol, keyDir: c.dir}, true
}

// errPeer is why a send through the inbox was refused before anything reached the session: code (peer_*) and what
// the node says, in Chinese as the node's other refusals (the hub localizes by code, rpc-errors.peer_*).
type errPeer struct {
	code, msg string
	cause     error
}

func (e *errPeer) Error() string {
	if e.cause != nil {
		return e.msg + "（" + e.cause.Error() + "）"
	}
	return e.msg
}

func (e *errPeer) Unwrap() error { return e.cause }

const (
	peerNoInboxMsg     = "这个 Claude 没有开放会话之间的消息通道（它的版本太旧，或这项功能在那边关着）：在电脑上接着用它。这次没有发送。"
	peerProtocolMsg    = "这个 Claude 的消息通道版本 Persona 不认识：在电脑上接着用它，或更新 Persona。这次没有发送。"
	peerPlatformMsg    = "这台电脑上连不了这个 Claude 的消息通道：在电脑上接着用它。这次没有发送。"
	peerKeyMsg         = "找不到这个 Claude 的消息通道钥匙（它可能刚重启或正在退出）：稍后再发，或在电脑上接着用它。这次没有发送。"
	peerUnreachMsg     = "连不上这个 Claude 的消息通道（它可能刚退出）：稍后再发，或在电脑上接着用它。这次没有发送。"
	peerTooLongMsg     = "消息太长，会话之间的消息通道一次放不下：分成几条发。这次没有发送。"
	peerWriteBrokeMsg  = "给这个 Claude 的消息写到一半连接断了，不知道它收到没有：先在电脑上看一眼再决定要不要重发。"
	peerUnconfirmedMsg = "消息已交给这个 Claude，但它还没有收下：可能在等电脑上批准（权限模式是跳过确认的会话会先扣下其他会话发来的消息），也可能和上一条重复被丢掉了。先在电脑上看一眼再决定要不要重发。"
	peerHeldMsg        = "消息已交给这个 Claude，但它扣下了这条消息，在等电脑上批准：在电脑上允许后它才会收下。不用重发。"
	peerCommandMsg     = "会话之间的消息通道只送普通消息，不执行斜杠命令：命令请在电脑上输入。这次没有发送。"
)

// peerSend writes text to the claude's inbox as one message to session sessionID (the inbox drops it when the
// claude has moved on to another session). nil says the inbox read the line, not that the session took it
// (peerTaken). Refusals come back as *errPeer (nothing sent); any other error came after the line was written (the
// inbox may have it): the caller looks at the transcript before calling it unknown.
func peerSend(ctx context.Context, p claudePeer, sessionID, text string) error {
	return peerSendAt(ctx, p, sessionID, text, peerNext)
}

// The priorities a message to the inbox goes at. peerNext queues it behind a running turn, or runs it when the session
// rests. peerNow cuts the running turn — and a question or permission prompt the session waits on (their tool call
// ends "Tool permission request aborted") — and runs it at once: Claude Code's own "send now", its queue aborting the
// turn for a command of priority now (measured with Claude Code 2.1.311 in the desktop app, 2026-11-12: a reply cut
// at its 481st character, the new message answered 2 s later).
const (
	peerNext = "next"
	peerNow  = "now"
)

// peerSendAt is peerSend at priority (peerNext, peerNow).
func peerSendAt(ctx context.Context, p claudePeer, sessionID, text, priority string) error {
	if p.socket == "" {
		return &errPeer{code: peerUnsupported, msg: peerNoInboxMsg}
	}
	if p.protocol != peerProtocol {
		return &errPeer{code: peerUnsupported, msg: peerProtocolMsg, cause: fmt.Errorf("peerProtocol %d", p.protocol)}
	}
	if !peerPathUsable(p.socket) {
		return &errPeer{code: peerUnsupported, msg: peerPlatformMsg}
	}
	token, err := peerToken(p)
	if err != nil {
		if peerAuthRequired {
			return &errPeer{code: peerUnreachable, msg: peerKeyMsg, cause: err}
		}
		token = "" // auth is optional on this platform: an inbox whose key file is missing takes the line without it
	}
	msg, err := peerMessageIn(sessionID, text, priority, p.fromMode, agentPromptLanguage(ctx))
	if err != nil {
		return &errPeer{code: peerUnsupported, msg: peerNoInboxMsg, cause: err}
	}
	var buf []byte
	if token != "" {
		auth, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
		buf = append(append(buf, auth...), '\n')
	}
	buf = append(append(buf, msg...), '\n')
	if len(buf) > peerLineMax {
		return &errPeer{code: peerTooLong, msg: peerTooLongMsg}
	}
	dctx, cancel := context.WithTimeout(ctx, peerDialWait)
	defer cancel()
	conn, err := dialPeer(dctx, p.socket)
	if err != nil {
		return &errPeer{code: peerUnreachable, msg: peerUnreachMsg, cause: err}
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(peerDialWait))
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("%s: %w", peerWriteBrokeMsg, err)
	}
	// Until the inbox read it: on a pipe, a flush returns once the other end read everything; on a socket, its end
	// closing after ours (peerFinish).
	if err := peerFinish(conn, peerDialWait); err != nil {
		return fmt.Errorf("%s: %w", peerWriteBrokeMsg, err)
	}
	return nil
}

// peerMessage is the message line for text: the user's words with what the model is told of them
// (prompts/agents/peer_message: the user's own, from the phone, to be answered here), in Claude Code's envelope signed
// peerSender, for sessionID; msgV 1, a fresh msg_id, priority next (queued behind a running turn, or run when the
// session rests), stating no from-mode.
func peerMessage(sessionID, text string) ([]byte, error) {
	return peerMessageAt(sessionID, text, peerNext, "")
}

// peerMessageAt is peerMessage at priority, stating fromMode in the envelope ("" for none).
func peerMessageAt(sessionID, text, priority, fromMode string) ([]byte, error) {
	return peerMessageIn(sessionID, text, priority, fromMode, "")
}

func peerMessageIn(sessionID, text, priority, fromMode, language string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("empty message")
	}
	return marshalNoEscape(map[string]any{
		"type":       "user",
		"message":    map[string]string{"role": "user", "content": peerEnvelope(prompts.RenderPeerMessage(prompts.PeerMessage{Language: language, Text: text}), fromMode)},
		"priority":   priority,
		"msgV":       1,
		"msg_id":     peerMsgID(),
		"session_id": sessionID,
	})
}

// peerEnvelope wraps text as a Claude sender does: the tag on its own lines around the body, with the body's own
// closing tags made inert (</… becomes <\/…, as Claude Code's stt does), which Claude Code checks by writing the
// envelope again; from-mode follows from-name when fromMode is set, the order Claude Code writes and parses them in.
func peerEnvelope(text, fromMode string) string {
	attrs := ` from-name="` + peerSender + `"`
	if fromMode != "" {
		attrs += ` from-mode="` + fromMode + `"`
	}
	return "<" + peerTag + attrs + ">" + "\n" + peerCloseRe.ReplaceAllString(text, `<\$1`) + "\n</" + peerTag + ">"
}

var peerCloseRe = lazyre.New(`(?i)<(/` + peerTag + `)`)

// marshalNoEscape is json.Marshal without HTML escaping (<, >, & as they are, as JavaScript writes them).
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// peerMsgID is a random uuid (version 4), the shape Claude Code checks msg_id against.
func peerMsgID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// peerPipeRe is a Windows named pipe path as Claude Code takes one: \\.\pipe\<name> or \\.\pipe\LOCAL\<name>.
var peerPipeRe = lazyre.New(`(?i)^[\\/]{2}[.?][\\/]pipe[\\/](?:(LOCAL)[\\/])?([^\\/]+)$`)

// peerKeyName is the key file's name for the inbox at socket, as Claude Code derives it: the sha256 (hex) of the
// canonical path — for a pipe \\.\pipe\ and its name (with LOCAL\ when it has it) in lower case, for a socket the
// absolute path — after the pid. "" for a path Claude Code would not name a key for.
func peerKeyName(pid int, socket string) string {
	var canon string
	if m := peerPipeRe.FindStringSubmatch(socket); m != nil {
		name := m[2]
		if name == "." || name == ".." || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
			return ""
		}
		if m[1] != "" {
			name = "LOCAL\\" + name
		}
		canon = `\\.\pipe\` + strings.ToLower(name)
	} else {
		if slices.Contains(strings.Split(filepath.ToSlash(socket), "/"), "..") || !filepath.IsAbs(socket) {
			return ""
		}
		canon = filepath.Clean(socket)
	}
	sum := sha256.Sum256([]byte(canon))
	return strconv.Itoa(pid) + "." + hex.EncodeToString(sum[:]) + ".key"
}

// peerToken reads the inbox's token from its key file next to the claude's sessions/<pid>.json.
func peerToken(p claudePeer) (string, error) {
	name := peerKeyName(p.pid, p.socket)
	if name == "" {
		return "", errors.New("no key name for this socket path")
	}
	f, err := os.Open(filepath.Join(p.keyDir, name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, peerKeyMax+1))
	if err != nil {
		return "", err
	}
	if len(b) > peerKeyMax {
		return "", errors.New("key file too large")
	}
	var k struct {
		PeerToken string `json:"peerToken"`
	}
	if err := json.Unmarshal(b, &k); err != nil {
		return "", err
	}
	if !peerTokenRe.MatchString(k.PeerToken) {
		return "", errors.New("key file has no token")
	}
	return k.PeerToken, nil
}

var peerTokenRe = lazyre.New(`^[0-9a-f]{32}$`)

// peerReachable: the claude's inbox looks usable from here — a path this platform dials, the protocol the node
// speaks, and (where the inbox requires it) a key file. It opens nothing: a session's capabilities say "send" only
// when this holds, and a send still reports what actually happened.
func peerReachable(p claudePeer) bool {
	if p.socket == "" || p.protocol != peerProtocol || !peerPathUsable(p.socket) {
		return false
	}
	if !peerAuthRequired {
		return true
	}
	_, err := peerToken(p)
	return err == nil
}

// peerTaken waits up to wait for the session's transcript (find: its path, "" while it has none — a session gets one
// with its first message) to show, past offset, that the session took a message with this text: the queue-operation
// it writes when it queues one, or the message's own line (a peer origin). A line's strings are compared decoded
// (JSON may escape <, > and & or not), on the body's start, so a long body still counts.
func peerTaken(ctx context.Context, find func() string, offset int64, text string, wait time.Duration) bool {
	needle := peerNeedle(text)
	if needle == "" {
		return false
	}
	deadline := time.Now().Add(wait)
	for {
		if path := find(); path != "" && peerSeen(path, offset, needle) {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// peerNeedle is the first words of text as the envelope carries them (closing tags made inert, peerEnvelope): at
// most 40 runes, to the end of its first line; "" when there is nothing to look for.
func peerNeedle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(peerCloseRe.ReplaceAllString(text, `<\$1`)), "\n")
	r := []rune(strings.TrimSpace(line))
	if len(r) > 40 {
		r = r[:40]
	}
	return string(r)
}

// peerSeen: a line past offset is a queue-operation, or a line with a peer origin, whose text has needle in it.
func peerSeen(path string, offset int64, needle string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		l, err := peerLine(br)
		if peerLineSeen(l, needle) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// peerHeldSeen: a line past offset is the notice Claude Code writes when it holds a peer message for approval at the
// computer, for one signed Persona: {"type":"system", "content":"Held peer message — from … (peer claims name:
// Persona) …"} (2.1.326). It is the evidence a message waits there (peerHeld); one held, refused or dropped without such
// a line is only not confirmed. The notice quotes only the start of the message, the same for every Persona message,
// so it tells that one of Persona's messages since offset was held, not which.
func peerHeldSeen(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		l, err := peerLine(br)
		if bytes.Contains(l, []byte(peerHeldNotice)) {
			var e struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			}
			if json.Unmarshal(l, &e) == nil && e.Type == "system" && strings.HasPrefix(e.Content, peerHeldNotice) &&
				strings.Contains(e.Content, "(peer claims name: "+peerSender+")") {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// peerHeldNotice starts the transcript notice of a held peer message (peerHeldSeen).
const peerHeldNotice = "Held peer message"

// peerScanMax bounds a transcript line peerSeen decodes: a queue-operation or a peer's line carries one inbox message
// (peerLineMax, JSON-escaped). A longer line (several pasted images inline, a tool result repeating a large output)
// cannot be one; it is read past without being kept, and the lines after it are still read.
const peerScanMax = 8 << 20

// peerLine is br's next line without its newline: nil when it is longer than peerScanMax. err is the last read's;
// io.EOF with a line is a last line without a newline (possibly still being written).
func peerLine(br *bufio.Reader) ([]byte, error) {
	b, err := br.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return bytes.TrimSuffix(b, []byte("\n")), err
	}
	l := append([]byte(nil), b...) // copied: the next read reuses b
	for errors.Is(err, bufio.ErrBufferFull) {
		b, err = br.ReadSlice('\n')
		if l != nil && len(l)+len(b) <= peerScanMax {
			l = append(l, b...)
		} else {
			l = nil
		}
	}
	return bytes.TrimSuffix(l, []byte("\n")), err
}

// peerLineSeen: l is a queue-operation, or a line with a peer origin, whose text has needle in it.
func peerLineSeen(l []byte, needle string) bool {
	if !bytes.Contains(l, []byte(`"queue-operation"`)) && !bytes.Contains(l, []byte(`"peer"`)) {
		return false
	}
	var e struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Origin *struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"origin"`
	}
	if json.Unmarshal(l, &e) != nil {
		return false
	}
	var said string
	_ = json.Unmarshal(e.Message.Content, &said)
	switch {
	case e.Type == "queue-operation" && strings.Contains(e.Content, needle):
		return true
	case e.Origin != nil && e.Origin.Kind == "peer" && (strings.Contains(e.Origin.Body, needle) || strings.Contains(said, needle)):
		return true
	}
	return false
}
