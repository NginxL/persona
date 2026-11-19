package node

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// larkDecision is the gate's call on one call: its command gate's rules (the user's first, the hub put them there),
// or for a lark-cli command the configured rules and then lark-cli's own risk level (a rule for one:
// `{"method":"CLI","path":"im/+messages-send","action":"allow"}`).
func larkDecision(cfg proto.AssistantLark, c larkCall) string {
	if c.CLI && c.Method == "CLI" {
		if action, ok := matchLark(cfg.Rules, c); ok {
			return action
		}
		if larkCLIReads(c.Risk) {
			return "allow"
		}
		return "confirm"
	}
	if c.CLI { // lark-cli api METHOD PATH: the configured rules, then reads go
		if action, ok := matchLark(cfg.Rules, c); ok {
			return action
		}
		if c.Method == "GET" || c.Method == "HEAD" {
			return "allow"
		}
		return "confirm"
	}
	if c.Gate < 0 || c.Gate >= len(cfg.Gates) {
		return "confirm"
	}
	return decideLark(cfg.Gates[c.Gate].Rules, c)
}

// larkRead: a call that only reads (lark-cli's own read risk or a command it does not have, a GET / HEAD).
func larkRead(c larkCall) bool {
	if c.CLI && c.Method == "CLI" {
		return larkCLIReads(c.Risk)
	}
	return c.Method == "GET" || c.Method == "HEAD"
}

// decideLark: the action for a call, from the rules in order (the first match); none matching is confirmed.

func decideLark(rules []proto.LarkRule, c larkCall) string {
	if action, ok := matchLark(rules, c); ok {
		return action
	}
	return "confirm"
}

// matchLark is the action of the first rule that matches the call.
func matchLark(rules []proto.LarkRule, c larkCall) (string, bool) {
	r, ok := firstLark(rules, c)
	return r.Action, ok
}

// firstLark is the first rule that matches the call.
func firstLark(rules []proto.LarkRule, c larkCall) (proto.LarkRule, bool) {
	for _, r := range rules {
		if (r.Method == "*" || strings.EqualFold(r.Method, c.Method)) && pathMatches(r.Path, c.Path) && bodyMatches(r.Body, c.Body) {
			return r, true
		}
	}
	return proto.LarkRule{}, false
}

func pathMatches(pattern, path string) bool {
	p := strings.Split(strings.Trim(pattern, "/"), "/")
	s := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range p {
		if seg == "**" {
			return true
		}
		if i >= len(s) || (seg != "*" && seg != s[i]) {
			return false
		}
	}
	return len(p) == len(s)
}

func bodyMatches(want map[string]any, body string) bool {
	if len(want) == 0 {
		return true
	}
	var got map[string]any
	if json.Unmarshal([]byte(body), &got) != nil {
		return false
	}
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// larkPattern: the call's route with its ids blanked, what "Always allow for this session" remembers.
func larkPattern(c larkCall) string {
	segs := strings.Split(strings.Trim(c.Path, "/"), "/")
	for i, s := range segs {
		if strings.ContainsAny(s, "0123456789") || len(s) > 24 {
			segs[i] = "*"
		}
	}
	return c.Method + " /" + strings.Join(segs, "/")
}

type larkBoundary uint8

const (
	larkOrdinary larkBoundary = iota
	larkOutbound
	larkDeletion
)

var larkActions = []struct {
	re       *lazyre.Regexp
	what     string
	boundary larkBoundary // classified alongside the phone-card action
}{
	{lazyre.New(`^CLI /im/messages/delete`), "撤回消息", larkDeletion},
	{lazyre.New(`^DELETE `), "删除", larkDeletion},
	{lazyre.New(`^(?:GET|HEAD|POST|PUT|PATCH|OPTIONS) /.*(?:delete|remove|trash|recall)`), "删除", larkDeletion},
	{lazyre.New(`^CLI /.*(?:/|\+|-)(?:delete|remove|trash|recall)(?:$|/|-)`), "删除", larkDeletion},
	{lazyre.New(`^CLI /.*(?:comments|replies)/(?:create|reply)$`), "发表评论", larkOutbound},
	{lazyre.New(`^CLI /mail/.*(?:send|reply|forward)$`), "发邮件", larkOutbound},
	{lazyre.New(`^CLI /mail/\+.*(?:send|reply|forward)`), "发邮件", larkOutbound},
	{lazyre.New(`^(POST|PUT|PATCH) /(?:open-apis/)?im/(?:v[0-9]+/)?(?:messages|batch_messages|threads)(?:/|$)`), "发消息", larkOutbound},
	{lazyre.New(`^POST /(?:conversations/[^/]+/(?:messages|files|images)|messages/[^/]+/(?:reply|forward|recall|reactions))`), "发消息", larkOutbound},
	{lazyre.New(`^(POST|PUT|PATCH) /.*(?:comments|replies)(?:/|$)`), "发表评论", larkOutbound},
	{lazyre.New(`^POST /.*(?:mail|mailboxes).*/(?:send|reply|forward)$`), "发邮件", larkOutbound},
	{lazyre.New(`^CLI /im/(?:messages/(?:create|reply|patch|update|send)|threads/forward)(?:/|$)`), "发消息", larkOutbound},
	// lark-cli commands (larkcligate.go): "CLI /<domain>/<command>"
	{lazyre.New(`^CLI /im/\+messages-send`), "发消息", larkOutbound},
	{lazyre.New(`^CLI /im/\+messages-reply`), "回复消息", larkOutbound},
	{lazyre.New(`^CLI /im/reactions/`), "表情回复", larkOutbound},
	{lazyre.New(`^CLI /im/(messages/(forward|merge_forward)|threads/forward)`), "转发消息", larkOutbound},
	{lazyre.New(`^CLI /im/messages/urgent`), "加急", larkOutbound},
	{lazyre.New(`^CLI /im/`), "改群 / 会话", larkOrdinary},
	{lazyre.New(`^CLI /(markdown|slides|whiteboard|mindnotes)`), "写文档", larkOrdinary},
	{lazyre.New(`^CLI /task`), "改待办", larkOrdinary},
	{lazyre.New(`^CLI /approval`), "处理审批", larkOrdinary},
	{lazyre.New(`^CLI /calendar`), "建 / 改日程", larkOrdinary},
	{lazyre.New(`^CLI /(docs|sheets|wiki|drive|base)`), "写文档", larkOrdinary},
	{lazyre.New(`^CLI /mail`), "发 / 改邮件", larkOrdinary},
}

// larkSummary is the phone card's text: what each write does, then its method, path and body.
func larkSummary(gates []proto.CommandGate, calls []larkCall) string {
	return larkSummaryWithContext(gates, calls, nil, i18n.Default)
}

// larkSummaryWithContext uses the readable message context when it was fetched; a failed lookup leaves that call's
// old summary untouched. Other gated calls keep their existing text.
func larkSummaryWithContext(gates []proto.CommandGate, calls []larkCall, contexts []larkCardContext, lang i18n.Lang) string {
	var b strings.Builder
	for i, c := range calls {
		if i > 0 {
			b.WriteString("\n\n")
		}
		what := "写操作"
		for _, a := range larkActions {
			if a.re.MatchString(c.Method + " " + c.Path) {
				what = a.what
				break
			}
		}
		if c.Gate >= 0 && c.Gate < len(gates) {
			for _, a := range gates[c.Gate].Actions {
				if re, err := regexp.Compile(a.Pattern); err == nil && re.MatchString(c.Method+" "+c.Path) {
					what = a.What
					break
				}
			}
		}
		if c.CLI {
			b.WriteString(larkCLIActor(c.Args) + "在飞书上" + what + larkRiskNote(c) + "\n" + prettyBody("lark-cli "+shellJoin(c.Args)))
			continue
		}
		if i < len(contexts) {
			if summary, ok := larkMessageSummary(c, contexts[i], lang); ok {
				b.WriteString(summary)
				continue
			}
		}
		b.WriteString("以你的身份在" + callTitle(gates, c) + "上" + what + "\n" + c.Method + " " + c.Path)
		if c.Query != "" {
			b.WriteString("?" + c.Query)
		}
		if body := prettyBody(c.Body); body != "" {
			b.WriteString("\n" + body)
		}
	}
	return b.String()
}

func prettyBody(body string) string {
	var v any
	if json.Unmarshal([]byte(body), &v) == nil {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			body = string(b)
		}
	}
	if r := []rune(body); len(r) > 2000 {
		body = string(r[:2000]) + "…"
	}
	return body
}

// Only the message send/reply shortcuts are documented to use the bot (plan-lark-cli.md).
// Raw API calls, mail and comments cannot claim this exemption by passing --as bot.
var larkBotOutbound = map[string]bool{
	"/im/+messages-send":  true,
	"/im/+messages-reply": true,
}

// larkBypassSafe uses the same action classification as the card. Unknown CLI risk is not an ordinary write.
func larkBypassSafe(c larkCall) bool {
	if c.CLI && c.Method == "CLI" && larkRead(c) {
		return true
	}
	for _, action := range larkActions {
		if action.boundary != larkOrdinary && action.re.MatchString(c.Method+" "+c.Path) {
			return action.boundary == larkOutbound && c.CLI && c.Method == "CLI" && larkBotOutbound[c.Path] && flagValue(c.Args, "--as") == "bot"
		}
	}
	if c.CLI && c.Method == "CLI" {
		return c.Risk == "write"
	}
	return httpMethod.MatchString(c.Method) && !strings.Contains(c.Path, "$")
}
