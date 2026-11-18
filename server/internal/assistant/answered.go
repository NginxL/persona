package assistant

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// answeredBody is the conversation's record of a task's card the user dealt with (feed kind "answered"): a line for
// the collapsed row, and every question with its answer in full for when the row is opened (#1231). Question and
// Answer stay one clipped line each: older apps draw only them, and older records have nothing else.
type answeredBody struct {
	Question string         `json:"question"`
	Answer   string         `json:"answer"`
	Items    []answeredItem `json:"items,omitempty"`
	// By is "assistant" when the assistant answered for the user (delegate.go), with Reason; Answer says so too, for
	// the apps that draw only it.
	By     string `json:"by,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// answeredItem is one question of a card and what the user answered, newlines kept. Answer is empty when there is
// nothing to say beyond the summary (a dismissed card's questions say "ignored" themselves).
type answeredItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
}

const (
	answeredLineMax  = 120   // the summary line
	answeredTextMax  = 4000  // one question or answer
	answeredTotalMax = 12000 // all of a record's items together
	answeredItemsMax = 20
)

// answeredSecret is what stands for a value typed into a password field: the hub keeps those nowhere.
func answeredSecret(l i18n.Lang) string { return i18n.T(l, "assistant-answered.secret") }

// answeredT is one of this file's texts in l.
func answeredT(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-answered."+name, args...)
}

// taskAnswered: the user dealt with a card of one of the task's agents; the conversation keeps a record of it.
func (s *Service) taskAnswered(ctx context.Context, ix proto.Interaction, r proto.Resolution, status string) {
	t, ok := s.taskOf(ctx, ix.MachineID, ix.SessionID)
	if !ok || status == "failed" {
		return
	}
	l := s.lang(ctx) // the record stays in the language the user had when they answered
	body := answeredBody{Question: interactionQuestion(l, ix), Answer: resolutionAnswer(l, ix, r), Items: answeredItems(l, ix, r)}
	if r.AnsweredBy == proto.AnsweredByAssistant {
		body.By, body.Reason = r.AnsweredBy, r.Reason
		if r.Reason != "" {
			body.Answer = clip(answeredT(l, "by_assistant_because", i18n.Args{"answer": body.Answer, "reason": oneLine(r.Reason)}), answeredLineMax)
		} else {
			body.Answer = clip(answeredT(l, "by_assistant", i18n.Args{"answer": body.Answer}), answeredLineMax)
		}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.appendFeed(cctx, t.Origin, feedAnswered, t.ID, body); err != nil {
		s.logf("task %s: answered: %v", t.ID, err)
	}
}

// interactionQuestion is what a card asked, in a line.
func interactionQuestion(l i18n.Lang, ix proto.Interaction) string {
	switch {
	case len(ix.Questions) > 0:
		return clip(oneLine(ix.Questions[0].Question), answeredLineMax)
	case ix.Kind == proto.IxPlan:
		return answeredT(l, "approve_plan")
	case ix.Summary != "":
		return clip(oneLine(ix.Summary), answeredLineMax)
	case ix.Kind == proto.IxElicitation && ix.Elicitation != nil && ix.Elicitation.Message != "":
		return clip(oneLine(ix.Elicitation.Message), answeredLineMax)
	case ix.ToolName != "":
		return answeredT(l, "use_tool", i18n.Args{"tool": ixToolName(ix)})
	}
	return answeredT(l, "needs_you")
}

// resolutionAnswer is the user's answer, in a line (never a typed secret: the hub keeps those nowhere).
func resolutionAnswer(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string {
	switch {
	case r.Cancel:
		return answeredT(l, "not_answered")
	case r.Dismiss:
		return answeredT(l, "dismissed")
	case len(r.Answers) > 0:
		var parts []string
		for _, it := range questionAnswers(ix, r) {
			parts = append(parts, it.Answer)
		}
		return clip(oneLine(strings.Join(parts, answeredT(l, "answer_sep"))), answeredLineMax)
	case r.Approve != nil && *r.Approve, r.Allow != nil && *r.Allow:
		if r.Always {
			return answeredT(l, "approved_always")
		}
		return answeredT(l, "approved")
	case r.Approve != nil, r.Allow != nil:
		if r.Feedback != "" || r.Message != "" {
			return clip(oneLine(answeredT(l, "denied_because", i18n.Args{"reason": r.Feedback + r.Message})), answeredLineMax)
		}
		return answeredT(l, "denied")
	case r.Option != "" || len(r.Options) > 0:
		if a := screenAnswerText(l, ix, r); a != answeredT(l, "picked") {
			return clip(oneLine(answeredT(l, "picked_option", i18n.Args{"option": a})), answeredLineMax)
		}
		return answeredT(l, "picked")
	case r.Action != "":
		return elicitationAction(l, r.Action)
	}
	return answeredT(l, "answered")
}

// questionAnswers pairs a question card's questions with the answers, in the card's order (proto.QuestionAnswers).
func questionAnswers(ix proto.Interaction, r proto.Resolution) []answeredItem {
	var out []answeredItem
	for _, qa := range proto.QuestionAnswers(ix, r) {
		out = append(out, answeredItem{Question: qa.Question, Answer: qa.Answer})
	}
	return out
}

// answeredItems is the record in full: each question with its answer, newlines kept, within the length limits.
func answeredItems(l i18n.Lang, ix proto.Interaction, r proto.Resolution) []answeredItem {
	var items []answeredItem
	switch {
	case len(ix.Questions) > 0 || len(r.Answers) > 0:
		answers := questionAnswers(ix, r)
		got := map[string]string{}
		for _, a := range answers {
			got[a.Question] = a.Answer
		}
		for _, q := range ix.Questions {
			a, ok := got[q.Question]
			switch {
			case r.Dismiss:
				a = answeredT(l, "dismissed")
			case !ok:
				a = answeredT(l, "not_answered")
			}
			items = append(items, answeredItem{Question: q.Question, Answer: a})
		}
		for _, a := range answers[min(len(answers), countAnswered(ix, got)):] {
			items = append(items, a) // answers to questions the card does not list
		}
	case ix.Kind == proto.IxPlan:
		q := answeredT(l, "approve_plan")
		if p := strings.TrimSpace(ix.Plan); p != "" {
			q = answeredT(l, "approve_this_plan", i18n.Args{"plan": p})
		}
		items = append(items, answeredItem{Question: q, Answer: decisionAnswer(l, ix, r)})
	case ix.Kind == proto.IxScreen:
		items = append(items, answeredItem{Question: screenQuestion(l, ix), Answer: screenAnswerText(l, ix, r)})
	case ix.Kind == proto.IxElicitation:
		items = append(items, answeredItem{Question: elicitationQuestion(l, ix), Answer: elicitationAnswer(l, ix, r)})
	default: // a permission, or anything else a card asks
		items = append(items, answeredItem{Question: permissionQuestion(l, ix), Answer: decisionAnswer(l, ix, r)})
	}
	return fitAnswered(items)
}

// countAnswered is how many of the card's own questions have an answer (they come first in questionAnswers).
func countAnswered(ix proto.Interaction, got map[string]string) int {
	n := 0
	for _, q := range ix.Questions {
		if _, ok := got[q.Question]; ok {
			n++
		}
	}
	return n
}

// decisionAnswer is an approve / deny in full: the reason given, the plan's mode, the card's fields.
func decisionAnswer(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string {
	if r.Dismiss {
		return answeredT(l, "dismissed")
	}
	var lines []string
	approved := r.Approve != nil && *r.Approve || r.Allow != nil && *r.Allow
	denied := !approved && (r.Approve != nil || r.Allow != nil)
	switch {
	case approved:
		head := answeredT(l, "approved")
		if mode, ok := planModes[r.Mode]; ix.Kind == proto.IxPlan && ok {
			head += " · " + answeredT(l, mode)
		}
		if r.Always {
			head = answeredT(l, "always", i18n.Args{"answer": head})
		}
		lines = append(lines, head)
	case denied:
		lines = append(lines, answeredT(l, "denied"))
	default:
		lines = append(lines, resolutionAnswer(l, ix, r))
	}
	if why := strings.TrimSpace(strings.TrimSpace(r.Feedback) + "\n" + strings.TrimSpace(r.Message)); why != "" {
		if denied {
			lines[0] = answeredT(l, "denied_with_reason")
			lines = append(lines, why)
		} else {
			lines = append(lines, answeredT(l, "note", i18n.Args{"note": why}))
		}
	}
	if len(r.Values) > 0 {
		lines = append(lines, fieldValues(l, ix, r)...)
	}
	return strings.Join(lines, "\n")
}

// planModes names how an approved plan goes on (Claude Code's permission modes): the text's name in this file's
// namespace.
var planModes = map[string]string{
	"acceptEdits":       "mode_accept_edits",
	"default":           "mode_default",
	"bypassPermissions": "mode_bypass",
	"plan":              "mode_plan",
}

// fieldValues are a card's filled-in fields, label: value, a password field only said to be filled in.
func fieldValues(l i18n.Lang, ix proto.Interaction, r proto.Resolution) []string {
	var lines []string
	known := map[string]bool{}
	for _, f := range ix.Fields {
		known[f.Key] = true
		v, ok := r.Values[f.Key]
		if !ok || v == "" {
			continue
		}
		label := f.Label
		if label == "" {
			label = f.Key
		}
		if f.Secret || secretName.MatchString(f.Key) {
			v = answeredSecret(l)
		}
		lines = append(lines, answeredT(l, "field", i18n.Args{"label": label, "value": v}))
	}
	var rest []string
	for k := range r.Values {
		if !known[k] && r.Values[k] != "" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest { // a field the card does not describe: kept only by name
		lines = append(lines, answeredT(l, "field", i18n.Args{"label": k, "value": answeredSecret(l)}))
	}
	return lines
}

// secretName is a field or property name that holds a credential whatever the card says about it.
var secretName = lazyre.New(`(?i)pass|secret|token|credential|api[_-]?key|private|otp|pin\b`)

// ixToolName is the tool a card asks about, for a sentence: the manager's name for people on its own tools' cards
// (proto.Interaction.ToolTitle), else toolDisplayName.
func ixToolName(ix proto.Interaction) string {
	if ix.ToolTitle != "" {
		return ix.ToolTitle
	}
	return toolDisplayName(ix.ToolName)
}

// toolDisplayName is a tool's name for a sentence: an MCP tool as "Persona · mail send" (Persona's own server keeps
// its old id persona, which allow lists and hooks match), others as written.
func toolDisplayName(name string) string {
	name = compat.CanonicalMCPToolName(name)
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return name
	}
	server, tool := rest, ""
	if i := strings.LastIndex(rest, "__"); i > 0 {
		server, tool = rest[:i], strings.ReplaceAll(rest[i+2:], "_", " ")
	}
	if server = strings.TrimPrefix(server, "plugin_"); compat.IsMCPServer(server) {
		server = "Persona"
	}
	if server == "" {
		return name
	}
	if tool == "" {
		return server
	}
	return server + " · " + tool
}

// permissionQuestion is what a permission card asked in full: the tool, what it would do, why the agent asks.
func permissionQuestion(l i18n.Lang, ix proto.Interaction) string {
	var lines []string
	if ix.ToolName != "" {
		lines = append(lines, answeredT(l, "use_tool", i18n.Args{"tool": ixToolName(ix)}))
	}
	if s := strings.TrimSpace(ix.Summary); s != "" {
		lines = append(lines, s)
	}
	if s := strings.TrimSpace(ix.Reason); s != "" {
		lines = append(lines, answeredT(l, "reason", i18n.Args{"reason": s}))
	}
	if len(lines) == 0 {
		return interactionQuestion(l, ix)
	}
	return strings.Join(lines, "\n")
}

// screenQuestion is a terminal dialog's question with the lines that belong to it (the command asked about).
func screenQuestion(l i18n.Lang, ix proto.Interaction) string {
	if d := ix.Dialog; d != nil && strings.TrimSpace(d.Question) != "" {
		var lines []string
		for _, s := range []string{d.Warning, d.Detail, d.Question} {
			if s = strings.TrimSpace(s); s != "" {
				lines = append(lines, s)
			}
		}
		return strings.Join(lines, "\n")
	}
	if s := strings.TrimSpace(ix.Summary); s != "" {
		return s
	}
	return interactionQuestion(l, ix)
}

// screenAnswerText is the options picked on a terminal dialog, with the text typed into one — never what was typed at a
// password prompt, nor keys sent straight to the terminal.
func screenAnswerText(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string {
	switch {
	case r.Dismiss:
		return answeredT(l, "dismissed")
	case r.Option != "" || len(r.Options) > 0:
		lines := []string{}
		if a := pickedOptions(l, ix, r); a != "" {
			lines = append(lines, a)
		}
		if t := strings.TrimSpace(r.Text); t != "" && !ix.Secret {
			lines = append(lines, t)
		}
		if len(lines) == 0 {
			return answeredT(l, "picked")
		}
		return strings.Join(lines, "\n")
	case ix.Secret && (r.Text != "" || len(r.Keys) > 0):
		return answeredT(l, "typed_password")
	}
	return resolutionAnswer(l, ix, r)
}

// pickedOptions names the options a terminal dialog's answer picked, by their labels.
func pickedOptions(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string {
	keys := r.Options
	if r.Option != "" {
		keys = []string{r.Option}
	}
	var labels []string
	for _, k := range keys {
		label := k
		if ix.Dialog != nil {
			for _, o := range ix.Dialog.Options {
				if o.Key == k && o.Label != "" {
					label = o.Label
					if o.Kind == "type_something" {
						label = "" // its text follows on its own line
					}
				}
			}
		}
		if label != "" {
			labels = append(labels, label)
		}
	}
	return strings.Join(labels, answeredT(l, "list_sep"))
}

func elicitationQuestion(l i18n.Lang, ix proto.Interaction) string {
	if e := ix.Elicitation; e != nil {
		var lines []string
		for _, s := range []string{e.Message, e.URL} {
			if s = strings.TrimSpace(s); s != "" {
				lines = append(lines, s)
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}
	}
	return interactionQuestion(l, ix)
}

func elicitationAction(l i18n.Lang, action string) string {
	switch action {
	case "accept":
		return answeredT(l, "submitted")
	case "decline":
		return answeredT(l, "denied")
	case "cancel":
		return answeredT(l, "cancelled")
	}
	return answeredT(l, "answered")
}

// elicitationAnswer is what was submitted to an MCP server's form: its fields by their titles, a password-like one
// only said to be filled in.
func elicitationAnswer(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string {
	if r.Dismiss {
		return answeredT(l, "dismissed")
	}
	lines := []string{elicitationAction(l, r.Action)}
	if r.Action != "accept" || len(r.Content) == 0 {
		return lines[0]
	}
	var schema struct {
		Properties map[string]struct {
			Title     string `json:"title"`
			Format    string `json:"format"`
			WriteOnly bool   `json:"writeOnly"`
		} `json:"properties"`
	}
	if ix.Elicitation != nil {
		_ = json.Unmarshal(ix.Elicitation.RequestedSchema, &schema)
	}
	keys := make([]string, 0, len(r.Content))
	for k := range r.Content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := schema.Properties[k]
		label := k
		if p.Title != "" {
			label = p.Title
		}
		var v string
		switch x := r.Content[k].(type) {
		case string:
			v = x
		case nil:
			continue
		default:
			b, _ := json.Marshal(x)
			v = string(b)
		}
		if p.Format == "password" || p.WriteOnly || secretName.MatchString(k) {
			v = answeredSecret(l)
		}
		lines = append(lines, answeredT(l, "field", i18n.Args{"label": label, "value": v}))
	}
	return strings.Join(lines, "\n")
}

// fitAnswered keeps a record within its limits: each text clipped, the items stopping where the total runs out.
func fitAnswered(items []answeredItem) []answeredItem {
	left := answeredTotalMax
	var out []answeredItem
	for _, it := range items {
		if left <= 0 || len(out) == answeredItemsMax {
			break
		}
		q := clip(strings.TrimSpace(it.Question), min(answeredTextMax, left))
		left -= utf8.RuneCountInString(q)
		a := clip(strings.TrimSpace(it.Answer), max(0, min(answeredTextMax, left)))
		if a == "…" {
			a = ""
		}
		left -= utf8.RuneCountInString(a)
		out = append(out, answeredItem{Question: q, Answer: a})
	}
	return out
}
