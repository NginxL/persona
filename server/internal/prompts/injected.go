package prompts

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The hub types some messages into conversations as the user would, for the model: a quiet task's notice
// (assistant/task_notice), a scheduled task's run (assistant/task_run), a wake-up (agents/wake), the continue after a
// usage limit reset (agents/limit_continue) or on a backup launcher (agents/limit_fallback_continue), the background tasks
// a node restart stopped (assistant/lost_tasks), news of an inbox item for a conversation it was handed to
// (inbox/handoff_note), how a question asked without waiting ended (assistant/ask_later). The model reads them as they are; the apps show one line instead
// (docs/protocol/protocol.md "Messages injected by Persona"). ParseInjected reads them back off the transcript's text, so old lines are
// known too; its tests render every template and parse the result, which keeps the two in step.

var (
	noticeHeadRe   = lazyre.New(`^🔔 任务「(.+)」(做完了|在等用户|被模型服务出错打断了|没开起来)(?:（(.+)）)?$`)
	followupHeadRe = lazyre.New(`^⏰ (.+)（(你之前安排的跟进|定时任务(?:，每 [^）]+)?)）$`)
	wakeHeadRe     = lazyre.New(`^⏰（Persona 叫醒你，第 \d+ 次）你上一轮停下时说在等「(.*?)」`)
	superviseRe    = lazyre.New(`^📋 督办：你派出去的任务有 (\d+) 个要你处理$`)
	inboxHeadRe    = lazyre.New(`^📥 收件箱事件「(.+)」有了下文$`)
	askLaterHeadRe = lazyre.New(`^🔔 你 .+ 用 ask_user 问过用户（wait: false，当时没有等回答）：$`)
	leadContextRe  = lazyre.New(`^(?s)\s*<persona-context\b[^>]*>.*?</persona-context>`)
)

// noticeMarker is in every task notice (assistant/task_notice): a first line alone could be the user's.
const noticeMarker = "这是 Persona 的任务通知"

// superviseMarker is in every supervise (follow-up) message (assistant/supervise, #3124).
const superviseMarker = "这是 Persona 的督办消息"

// SuperviseRoundMarker is on its own line in a supervision message that is a round of the background supervisor starting
// from a clean context (assistant/supervise, Fresh, #7315). The manager sends such a message as a round (IsSuperviseRound),
// and the transcript reader keeps every round in view though they all hang from the conversation's first turn
// (transcript/branch.go).
const SuperviseRoundMarker = "<persona-supervision-round />"

// IsSuperviseRound says whether text is a supervision message rendered as a round (SuperviseRoundMarker).
func IsSuperviseRound(text string) bool {
	if !strings.Contains(text, "\n"+SuperviseRoundMarker+"\n") {
		return false
	}
	in := ParseInjected(text)
	return in != nil && in.State == "supervise"
}

// inboxMarker is in every inbox note for a conversation an item was handed to (inbox/handoff_note, #1194).
const inboxMarker = "这是 Persona 的收件箱通知"

// askLaterMarker ends every assistant/ask_later message (#4928): a first line alone could be the user's.
const askLaterMarker = "（这是 Persona 的通知，不是用户在这里发的消息。）"

// askAnswered, askExpired and askSpoke start ask_later's sections for an answered card, one that ran out and made a
// to-do or took the defaults, and one that ran out after the user wrote in the conversation; any other ended unanswered.
const (
	askAnswered = "用户在卡片上回答了："
	askExpired  = "卡片到点没人回："
	askSpoke    = "卡片到点了，没人在卡片上回答。"
)

// askAnswerCap is how many characters of the answers the line keeps.
const askAnswerCap = 60

// limitContinueHead starts agents/limit_continue: the manager continues an agent once its usage limit reset.
const limitContinueHead = "⏰（Persona 叫你接着做）订阅的用量额度已经重置了"

// lostTasksHead starts assistant/lost_tasks.
const lostTasksHead = "⚠️ 运行你的 node 刚重启过"

// kbHintHead starts assistant/routine_kb_hint, which a daily run's prompt may end with.
const kbHintHead = "（知识库"

var noticeStates = map[string]string{"做完了": "done", "在等用户": "needs", "被模型服务出错打断了": "interrupted", "没开起来": "failed"}

var noticeLines = map[string]string{"done": "做完了", "needs": "在等你", "interrupted": "被打断了", "failed": "没开起来"}

// ContinuesOwnWork says whether in is Persona going on with the conversation's own work: the continuation once a
// usage limit reset (agents/limit_continue) or on a backup launcher (agents/limit_fallback_continue), or a wake for
// what it waits on (agents/wake). Such a message starts neither another task nor a background round.
func ContinuesOwnWork(in *proto.Injected) bool {
	return in != nil && (in.State == "limit_continue" || in.State == "limit_fallback" || in.State == "wake")
}

// ParseInjected says what text is when it is one of the messages Persona writes for the model; nil for anything
// else (what the user wrote stays theirs: only the templates' exact shapes are taken).
func ParseInjected(text string) *proto.Injected {
	if UserAuthoredContext(text) {
		return nil
	}
	for {
		loc := leadContextRe.FindStringIndex(text)
		if loc == nil {
			break
		}
		text = text[loc[1]:]
	}
	text = strings.TrimSpace(text)
	head, rest, _ := strings.Cut(text, "\n")
	switch {
	case strings.HasPrefix(head, "<persona-inbox"):
		return parseInboxMarker(head, rest)
	case head == "<persona-answer-again />" && strings.TrimSpace(rest) != "":
		return &proto.Injected{Kind: proto.InjectedSystem, State: "answer_again", Line: "Persona 正在补问"}
	case strings.HasPrefix(head, "<persona-wake"):
		return parseWakeMarker(head, rest)
	case strings.HasPrefix(head, "🔔 任务「"):
		m := noticeHeadRe.FindStringSubmatch(head)
		if m == nil || !strings.Contains(rest, noticeMarker) {
			return nil
		}
		state := noticeStates[m[2]]
		return &proto.Injected{Kind: proto.InjectedTaskNotice, State: state, Title: m[1], Machine: m[3],
			Line: "任务「" + m[1] + "」" + noticeLines[state]}
	case head == "<persona-limit-continue />" && strings.TrimSpace(rest) != "":
		return &proto.Injected{Kind: proto.InjectedFollowup, State: "limit_continue", Line: "用量额度重置了，Persona 让它接着做"}
	case strings.HasPrefix(head, "<persona-limit-continue"):
		return nil
	case head == LimitFallbackMarker && strings.TrimSpace(rest) != "":
		return &proto.Injected{Kind: proto.InjectedFollowup, State: "limit_fallback", Line: "用量额度用完了，Persona 换了个启动器让它接着做"}
	case strings.HasPrefix(head, "<persona-limit-fallback"):
		return nil
	case strings.HasPrefix(head, limitContinueHead):
		// Only hubs that write it mark it: it is not in the shared fixtures the apps' own recognition runs.
		return &proto.Injected{Kind: proto.InjectedFollowup, State: "limit_continue", Line: "用量额度重置了，Persona 让它接着做"}
	case strings.HasPrefix(head, "⏰（"):
		m := wakeHeadRe.FindStringSubmatch(head)
		if m == nil {
			return nil
		}
		return &proto.Injected{Kind: proto.InjectedFollowup, State: "wake", Title: m[1], Line: "Persona 叫醒了它：看看「" + m[1] + "」好了没有"}
	case strings.HasPrefix(head, "⏰ "):
		m := followupHeadRe.FindStringSubmatch(head)
		if m == nil {
			return nil
		}
		in := &proto.Injected{Kind: proto.InjectedFollowup, Title: m[1], Detail: followupDetail(rest)}
		switch {
		case m[2] == "你之前安排的跟进":
			in.State, in.Line = "once", "跟进「"+m[1]+"」到时间了"
		case strings.HasPrefix(m[2], "定时任务，每 "):
			in.State, in.Line = "interval", "定时任务「"+m[1]+"」开始运行"
		default:
			in.State, in.Line = "daily", "定时任务「"+m[1]+"」开始运行"
		}
		return in
	case strings.HasPrefix(head, "📋 督办："):
		// Only hubs that write it mark it: it is not in the shared fixtures the apps' own recognition runs.
		m := superviseRe.FindStringSubmatch(head)
		if m == nil || !strings.Contains(rest, superviseMarker) {
			return nil
		}
		level := "triage" // Messages from before levels were added are routine triage too.
		for _, candidate := range []string{"info", "triage", "user"} {
			if strings.Contains(rest, `<persona-supervision level="`+candidate+`" />`) {
				level = candidate
				break
			}
		}
		return &proto.Injected{Kind: proto.InjectedSystem, State: "supervise", Level: level, Line: "督办：" + m[1] + " 个派出去的任务要处理"}
	case strings.HasPrefix(head, "📥 收件箱事件「"):
		// Only hubs that write it mark it: it is not in the shared fixtures the apps' own recognition runs.
		m := inboxHeadRe.FindStringSubmatch(head)
		if m == nil || !strings.Contains(rest, inboxMarker) {
			return nil
		}
		return &proto.Injected{Kind: proto.InjectedSystem, State: "inbox", Title: m[1], Line: "收件箱「" + m[1] + "」有了下文"}
	case strings.HasPrefix(head, "🔔 你 "):
		// Only hubs that write it mark it: it is not in the shared fixtures the apps' own recognition runs.
		if !askLaterHeadRe.MatchString(head) || !strings.HasSuffix(rest, askLaterMarker) {
			return nil
		}
		return askLaterInjected(rest)
	case strings.HasPrefix(head, lostTasksHead):
		n := 0
		for _, l := range strings.Split(rest, "\n") {
			if strings.HasPrefix(l, "- ") {
				n++
			}
		}
		line := "node 重启过，后台任务停了"
		if n > 0 {
			line = fmt.Sprintf("node 重启过，%d 个后台任务停了", n)
		}
		return &proto.Injected{Kind: proto.InjectedSystem, State: "lost_tasks", Line: line}
	}
	return nil
}

// wakeMetadata is the language-independent identity of newly localized wakes.
// Legacy Chinese wakes remain readable above, including historical transcripts.
type wakeMetadata struct {
	Title string `json:"title"`
}

func parseWakeMarker(head, body string) *proto.Injected {
	const start, end = "<persona-wake>", "</persona-wake>"
	if !strings.HasPrefix(head, start) || !strings.HasSuffix(head, end) || strings.TrimSpace(body) == "" {
		return nil
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(head, start), end)
	var meta wakeMetadata
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&meta) != nil || d.Decode(new(any)) != io.EOF {
		return nil
	}
	// Require our canonical JSON: rejects null, absent/duplicate keys and literal
	// closing tags. A malformed marker must never become guessed provenance.
	canonical, _ := json.Marshal(meta)
	if raw != string(canonical) {
		return nil
	}
	return &proto.Injected{Kind: proto.InjectedFollowup, State: "wake", Title: meta.Title,
		Line: "Persona 叫醒了它：看看「" + meta.Title + "」好了没有"}
}

// inboxMetadata is the language-independent identity of an Inbox followup.
type inboxMetadata struct {
	Title string `json:"title"`
}

func parseInboxMarker(head, body string) *proto.Injected {
	const start, end = "<persona-inbox>", "</persona-inbox>"
	if !strings.HasPrefix(head, start) || !strings.HasSuffix(head, end) || strings.TrimSpace(body) == "" {
		return nil
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(head, start), end)
	var meta inboxMetadata
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&meta) != nil || d.Decode(new(any)) != io.EOF {
		return nil
	}
	// Require our canonical JSON: rejects null, absent/duplicate keys and literal
	// closing tags. A malformed marker must never become guessed provenance.
	canonical, _ := json.Marshal(meta)
	if raw != string(canonical) {
		return nil
	}
	return &proto.Injected{Kind: proto.InjectedSystem, State: "inbox", Title: meta.Title,
		Line: "收件箱「" + meta.Title + "」有了下文"}
}

// askLaterInjected is how the card of an ask_later message (its text after the first line) ended (#6421): the
// answers, as one clipped line, when the user answered on it; that it ran out; or that it closed unanswered.
func askLaterInjected(rest string) *proto.Injected {
	lines := strings.Split(rest, "\n")
	for i, l := range lines {
		switch {
		case l == askAnswered:
			var answers []string
			for _, l := range lines[i+1:] {
				if strings.HasPrefix(l, "- ") {
					answers = append(answers, l[len("- "):])
				} else if strings.HasPrefix(l, " ") && len(answers) > 0 { // a question or an answer of several lines
					answers[len(answers)-1] += " " + strings.TrimSpace(l)
				} else {
					break
				}
			}
			for j, a := range answers {
				if k := strings.LastIndex(a, " → "); k >= 0 {
					answers[j] = strings.TrimSpace(a[k+len(" → "):])
				}
			}
			title := clipRunes(strings.Join(answers, " · "), askAnswerCap)
			return &proto.Injected{Kind: proto.InjectedSystem, State: "ask_answered", Title: title, Line: "你回答了：" + title}
		case strings.HasPrefix(l, askExpired), strings.HasPrefix(l, askSpoke):
			return &proto.Injected{Kind: proto.InjectedSystem, State: "ask_expired", Line: "问题到时间了，卡片上没有回答"}
		}
	}
	return &proto.Injected{Kind: proto.InjectedSystem, State: "ask_closed", Line: "问题卡片关掉了，没有回答"}
}

// clipRunes is s cut to at most n characters, with "…" when cut.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// followupDetail is a run's prompt: after the notes under its first line, without the knowledge base hint.
func followupDetail(rest string) string {
	_, body, ok := strings.Cut("\n"+rest, "\n\n")
	if !ok {
		return ""
	}
	body = strings.TrimSpace(body)
	if i := strings.LastIndex(body, "\n\n"+kbHintHead); i >= 0 {
		body = strings.TrimSpace(body[:i])
	} else if strings.HasPrefix(body, kbHintHead) {
		body = ""
	}
	return body
}
