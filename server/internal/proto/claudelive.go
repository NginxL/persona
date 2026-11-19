package proto

// Claude Code sessions the node finds running where Persona has no terminal pane: their source_kind
// (docs/integrations/claude-integration.md, "Claude outside Persona's terminals").
const (
	// SourceClaudeBg: a background session Claude's own daemon runs (/fork, /background).
	SourceClaudeBg = "claude_bg"
	// SourceClaudeDesktop: a session open in Claude's desktop app (entrypoint claude-desktop…).
	SourceClaudeDesktop = "claude_desktop"
	// SourceClaudeTerminal: claude in a terminal herdr, Orca and botmux do not run (entrypoint cli).
	SourceClaudeTerminal = "claude_cli"
)

// What the node says about such a session, in Chinese as it says everything else; the hub rewrites these in the
// app's language (assistant/fleet_text.go claudeLiveText), so the node and the hub share the wording here.
const (
	// ReadOnlyClaudeBg: a background session with no peer inbox the node can use.
	ReadOnlyClaudeBg = "Claude Code 的后台会话：手机上只能看，回复请在电脑上用 claude agents 打开它"
	// ReadOnlyClaudeNoInbox: the claude has no peer inbox (an older Claude Code, or cross-session messages are off).
	ReadOnlyClaudeNoInbox = "这个 Claude 没有开放会话之间的消息通道（版本较旧或功能关着）：手机上只能看，回复请在电脑上"
	// ReadOnlyClaudeInbox: it has one, but not one the node can use (unknown protocol, no key, another platform).
	ReadOnlyClaudeInbox = "这个 Claude 的消息通道 Persona 用不了（版本不认识或缺少钥匙）：手机上只能看，回复请在电脑上"
	// AttentionClaudeComputerBefore5965 is the former computer-only sentence, retained so a new hub localizes old nodes.
	AttentionClaudeComputerBefore5965 = "它在等电脑上的一个问题或权限请求：手机上答不了，请到电脑上处理"
	// AttentionClaudeComputer: it waits on a question or a permission prompt, which only the computer can answer.
	AttentionClaudeComputer = "它在等电脑上的一个问题或权限请求：手机上答不了，请到电脑上处理。下次在 tmux 里或从 Persona 启动 Claude，就能在手机上回答"
	// AttentionClaudeAnswer: it waits on a question it asked, and a message through its inbox answers it: the question's
	// card (node/claudeoutside_ask.go) sends the picks as that message. The hub words it in the app's language; this text
	// is what the node and the hub match on, so it stays as it is.
	AttentionClaudeAnswer = "它在问你一个问题：直接回复就是回答"
)
