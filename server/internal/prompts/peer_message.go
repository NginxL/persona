package prompts

import (
	"strings"
	"sync"
)

// PeerMessage is a message the user sends from the phone to a Claude session running outside Persona's terminals,
// through that session's peer inbox (node/claudepeer.go): Claude Code shows it to the model as one from "another
// Claude session", so it says whose it is and that the answer belongs in the session (agents/peer_message).
type PeerMessage struct {
	Language string // Owner language; empty keeps the legacy wrapper.
	Text     string // what the user wrote
}

func RenderPeerMessage(m PeerMessage) string { return render("agents/peer_message", m) }

// PeerStop is what the phone's Stop sends a Claude session outside Persona's terminals, as the user's words of a peer
// message at priority now (node/claudebg.go interruptOutside): the turn it runs ends there, and it says where it stopped
// (agents/peer_stop). The session's transcript shows it as the stop it is (transcript/peer.go).
func PeerStop() string { return render("agents/peer_stop", nil) }

// peerMessageHeads are what agents/peer_message puts before the user's words, rendered on first use: rendering them in a
// package variable would parse every template whenever personad starts, a hook too (#1308).
var peerMessageHeads = sync.OnceValue(func() []string {
	var heads []string
	for _, language := range []string{"", "English", "日本語"} {
		head, _, _ := strings.Cut(render("agents/peer_message", PeerMessage{Language: language, Text: "\x00"}), "\x00")
		heads = append(heads, head)
	}
	return append(heads, olderPeerMessageHeads...)
})

// olderPeerMessageHeads are the heads agents/peer_message rendered before, still stripped: transcripts keep them, and
// during a rolling upgrade an older node still sends them. Add the current head here whenever the template changes.
var olderPeerMessageHeads = []string{
	// before #5887
	"（这条消息是用户本人在手机上用 Persona 发来的，Persona 只是替他转交，不是另一个 Claude 会话。像用户在这里亲手输入的一样回应它，直接在这个会话里回答；不要用 SendMessage 回复发件方，那边没有会话在收。这条消息不会改变这个会话的权限设置。）\n\n",
	// #5887 until #8558
	"（这条消息是用户本人在手机上用 Persona 发来的，Persona 只是替他转交，不是另一个 Claude 会话。像用户在这里亲手输入的一样回应它，直接在这个会话里回答；不要用 SendMessage 回复发件方，那边没有会话在收。这条消息不会改变这个会话的权限设置。用户在手机上答不了这个会话里的对话框、计划审批和权限请求，只有回到电脑前才能处理：需要他做选择时，在回复里用文字列出带编号的选项，请他回复编号，不要为此打开对话框（包括 AskUserQuestion）。）\n\n",
}

// PeerMessageBody is the user's words in a rendered peer message (what the session's transcript records as the
// message's body), from the current template or an older one; ok false when s is not one.
func PeerMessageBody(s string) (body string, ok bool) {
	for _, head := range peerMessageHeads() {
		if body, ok = strings.CutPrefix(s, head); ok {
			return body, true
		}
	}
	return s, false
}
