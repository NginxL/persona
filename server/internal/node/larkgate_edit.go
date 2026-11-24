package node

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"mvdan.cc/sh/v3/syntax"
)

// larkGateWithEdit keeps the ordinary gate untouched for ineligible commands. An eligible Claude Code Lark
// send/reply gets editable_text; after the answer, the original grant is consumed before a new command is classified
// and granted. That keeps an old Claude unable to execute the original command when the user changed the text.
func (n *Node) larkGateWithEdit(ctx context.Context, in hookInput, ix proto.Interaction) (any, error) {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in.ToolInput, &a) != nil || a.Command == "" {
		return nil, nil
	}
	cfg := n.assistantLark(ctx)
	call, span, original, ok := n.editableLarkCall(cfg, a.Command, in.Cwd)
	if !ok {
		return n.larkGate(ctx, in, ix)
	}
	ix.EditableText = original
	decision, reason, err := n.larkCheck(ctx, in.SessionID, in.Cwd, a.Command, ix)
	if err != nil || decision == "" {
		return nil, err
	}
	edited, _ := n.editedAnswers.LoadAndDelete(ix.ID)
	editedText, _ := edited.(string)
	if editedText == "" || editedText == original {
		return preToolDecision(decision, reason), nil
	}
	if decision != "allow" {
		return preToolDecision(decision, reason), nil
	}
	updated, err := n.applyLarkEdit(ctx, cfg, in.SessionID, ix.ID, a.Command, in.Cwd, call, span, editedText)
	if err != nil {
		return preToolDecision("deny", err.Error()), nil
	}
	var input map[string]any
	if err := json.Unmarshal(in.ToolInput, &input); err != nil {
		return preToolDecision("deny", "edited command input could not be read"), nil
	}
	input["command"] = updated
	out := preToolDecision("allow", reason).(map[string]any)
	out["hookSpecificOutput"].(map[string]any)["updatedInput"] = input
	return out, nil
}

type larkBodySpan struct{ start, end int }

func (n *Node) editableLarkCall(cfg proto.AssistantLark, command, cwd string) (larkCall, larkBodySpan, string, bool) {
	if !larkLoneGateCall(command, cfg.Gates) {
		return larkCall{}, larkBodySpan{}, "", false
	}
	v := classifyLarkWith(command, cfg.Gates, cwd, n.larkCLIRisk(cfg), n.larkLoginPATH)
	if v.deny != "" || v.other || v.unparsed || v.unknown || v.unsure || len(v.calls) != 1 {
		return larkCall{}, larkBodySpan{}, "", false
	}
	c := v.calls[0]
	if c.CLI || c.Gate < 0 || c.Gate >= len(cfg.Gates) || cfg.Gates[c.Gate].Auth == nil || c.relFile {
		return larkCall{}, larkBodySpan{}, "", false
	}
	if kind, _, _ := larkMessageTarget(c); kind == "" {
		return larkCall{}, larkBodySpan{}, "", false
	}
	_, original, ok := editableTextFields(c.Body)
	if !ok {
		return larkCall{}, larkBodySpan{}, "", false
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(tree.Stmts) != 1 {
		return larkCall{}, larkBodySpan{}, "", false
	}
	var spans []larkBodySpan
	syntax.Walk(tree.Stmts[0], func(node syntax.Node) bool {
		if w, ok := node.(*syntax.Word); ok {
			value, dynamic := executionWord(w)
			if !dynamic && value == c.Body {
				spans = append(spans, larkBodySpan{int(w.Pos().Offset()), int(w.End().Offset())})
			}
		}
		return true
	})
	if len(spans) != 1 {
		return larkCall{}, larkBodySpan{}, "", false
	}
	return c, spans[0], original, true
}

// editableTextFields accepts only a JSON object with one top-level text field. Every other field stays as raw JSON so
// mentions and ids are not reserialized or converted through float64. Duplicate keys are not an editable object.
func editableTextFields(body string) (map[string]json.RawMessage, string, bool) {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, "", false
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, "", false
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return nil, "", false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, "", false
		}
		fields[name] = raw
	}
	if _, err := dec.Token(); err != nil || dec.More() {
		return nil, "", false
	}
	var text string
	if err := json.Unmarshal(fields["text"], &text); err != nil || strings.TrimSpace(text) == "" {
		return nil, "", false
	}
	return fields, text, true
}

func (n *Node) applyLarkEdit(ctx context.Context, cfg proto.AssistantLark, session, id, command, cwd string, old larkCall, span larkBodySpan, edited string) (string, error) {
	if err := n.takeOriginalGrant(cfg, session, id, old); err != nil {
		return "", err
	}
	_, originalText, ok := editableTextFields(old.Body)
	if !ok || strings.TrimSpace(edited) == "" {
		return "", fmt.Errorf("edited text must not be empty")
	}
	oldFields, _, _ := editableTextFields(old.Body)
	oldFields["text"], _ = json.Marshal(edited)
	body, err := json.Marshal(oldFields)
	if err != nil {
		return "", err
	}
	// Replace the one literal body word, never the route, query, mentions or another occurrence of the same text.
	// The caller already consumed the original grant, so a parse/validation failure cannot execute the original.
	updated, ok := editableSplice(command, old, span, string(body))
	if !ok {
		return "", fmt.Errorf("the edited command could not be rebuilt")
	}
	command = updated
	v := classifyLarkWith(command, cfg.Gates, cwd, n.larkCLIRisk(cfg), n.larkLoginPATH)
	if v.deny != "" || v.other || v.unparsed || v.unknown || v.unsure || len(v.calls) != 1 {
		return "", fmt.Errorf("the edited command is no longer one standalone gated call")
	}
	c := v.calls[0]
	if c.Gate != old.Gate || c.Method != old.Method || c.Path != old.Path || c.Query != old.Query || c.CLI || c.relFile {
		return "", fmt.Errorf("the edited command changed its tool, method, path or query")
	}
	if kind, _, _ := larkMessageTarget(c); larkDecision(cfg, c) == "deny" || kind == "" {
		return "", fmt.Errorf("the edited text is denied by the same gate")
	}
	newFields, newText, ok := editableTextFields(c.Body)
	if !ok || newText != edited {
		return "", fmt.Errorf("the edited body does not carry the requested text")
	}
	for key, before := range oldFields {
		if key == "text" {
			continue
		}
		after, present := newFields[key]
		if !present || string(before) != string(after) {
			return "", fmt.Errorf("the edited body changed a field other than text")
		}
	}
	if err := n.grantEdited(cfg, session, c, proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: id, EditedFrom: originalText}); err != nil {
		return "", err
	}
	return command, nil
}

func editableSplice(command string, old larkCall, span larkBodySpan, body string) (string, bool) {
	if span.start < 0 || span.end < span.start || span.end > len(command) {
		return "", false
	}
	segment := command[span.start:span.end]
	tree, err := syntax.NewParser().Parse(strings.NewReader(segment), "")
	if err != nil || len(tree.Stmts) != 1 {
		return "", false
	}
	stmt := tree.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Negated || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) != 1 {
		return "", false
	}
	value, dynamic := executionWord(call.Args[0])
	if dynamic || value != old.Body {
		return "", false
	}
	return command[:span.start] + shellQuote(body) + command[span.end:], true
}

func (n *Node) takeOriginalGrant(cfg proto.AssistantLark, session, id string, c larkCall) error {
	if c.Gate < 0 || c.Gate >= len(cfg.Gates) || cfg.Gates[c.Gate].Auth == nil {
		return fmt.Errorf("the original command has no authenticated gate")
	}
	var body any
	parsed := json.Unmarshal([]byte(c.Body), &body) == nil
	p := n.gates()
	key := gateKey{command: cfg.Gates[c.Gate].Command, session: session, space: p.spaceSession(session)}
	query := gateQueryKey(c.Query)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	for i, g := range p.grants {
		// The interaction id distinguishes this approval from another outstanding card for the same route and body.
		if g.through.Kind != proto.LarkThroughCard || g.through.InteractionID != id ||
			g.command != key.command || g.method != strings.ToUpper(c.Method) || g.path != c.Path || g.query != query {
			continue
		}
		if key.space && (key.session == "" || g.session != key.session) {
			continue
		}
		if g.session != "" && key.session != "" && g.session != key.session {
			continue
		}
		if g.bodyBound && (!parsed || !reflect.DeepEqual(g.body, body)) {
			continue
		}
		p.grants = slices.Delete(p.grants, i, i+1)
		return nil
	}
	return fmt.Errorf("the original grant could not be retired")
}

// grantEdited always pins the exact JSON body, including when the edited text contains '$'. The ordinary grant path's
// dynamic-body wildcard is deliberately not used here.
func (n *Node) grantEdited(cfg proto.AssistantLark, session string, c larkCall, through proto.LarkLetThrough) error {
	if c.Gate < 0 || c.Gate >= len(cfg.Gates) || cfg.Gates[c.Gate].Auth == nil {
		return fmt.Errorf("the edited command has no authenticated gate")
	}
	var body any
	dec := json.NewDecoder(strings.NewReader(c.Body))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return err
	}
	g := gateGrant{command: cfg.Gates[c.Gate].Command, method: strings.ToUpper(c.Method), path: c.Path, query: gateQueryKey(c.Query), body: body, bodyBound: true, bodyUseNumber: true,
		until: time.Now().Add(gateGrantFor), session: session, through: through}
	p := n.gates()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	p.grants = append(p.grants, g)
	if len(p.grants) > gateGrantsMax {
		p.grants = p.grants[len(p.grants)-gateGrantsMax:]
	}
	return nil
}
