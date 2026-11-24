package voice

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The function tools the voice model may call (live dialect with tools): what Persona knows and the agents on the
// user's machines, through the hub's API. Their descriptions are prompts (prompts/mcp/voice), and so is what they
// answer (prompts/voice/tool_result.md, prompts/voice/refused.md).
var toolNames = []string{"search_knowledge", "list_agents", "send_to_agent", "ask_persona"}

// boundTools are a call bound to a conversation's (protocol 2): the two that only read, and delegate_task, which hands
// work to that conversation (docs/assistant/voice.md "Sending, gates, and 409").
var boundTools = []string{"search_knowledge", "list_agents", "delegate_task"}

// toolLabels are what the app shows while a tool runs.
var toolLabels = map[string]string{
	"search_knowledge": "查知识库",
	"list_agents":      "看 Agents",
	"send_to_agent":    "发给 Agent",
	"ask_persona":      "交给 Persona",
	"delegate_task":    "交给对话",
}

// toolDecls declares the named tools, in toolNames' order (delegate_task last). meeting: a meeting call's, whose
// delegate_task is described as asking the meeting conversation (mcp/voice/meeting_delegate_task: none of the bound
// call's marks, which a meeting call does not read, meeting.go).
func toolDecls(names []string, meeting bool, language ...string) []toolDecl {
	out := make([]toolDecl, 0, len(names))
	for _, n := range append(slices.Clone(toolNames), "delegate_task") {
		if !slices.Contains(names, n) {
			continue
		}
		t := prompts.Tool(n, language...)
		if meeting && n == "delegate_task" {
			t = prompts.Tool("meeting_delegate_task", language...)
		}
		out = append(out, toolDecl{Type: "function", Name: n, Description: t.Description, Parameters: t.InputSchema})
	}
	return out
}

// runTool runs one of the hub tools (ask_persona is the conversation's: it answers later) and returns what the model
// is told, in language l.
func runTool(ctx context.Context, hub *hubClient, l i18n.Lang, name, args string) string {
	res := prompts.VoiceToolResult{Lang: string(l), Tool: name}
	var in struct {
		Query     string `json:"query"`
		Kind      string `json:"kind"` // search_knowledge: one kind only, as kb_search takes it (recordings for meetings)
		MachineID string `json:"machine_id"`
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
	}
	if strings.TrimSpace(args) != "" && json.Unmarshal([]byte(args), &in) != nil {
		res.Code = "bad_args"
		return prompts.RenderVoiceToolResult(res)
	}
	switch name {
	case "search_knowledge":
		if strings.TrimSpace(in.Query) == "" {
			res.Code = "bad_args"
			break
		}
		hits, err := hub.searchKnowledge(ctx, in.Query, strings.TrimSpace(in.Kind))
		if err != nil {
			res.Code, res.Reason = "failed", hubReason(err)
			break
		}
		for i, h := range hits {
			if i == 20 {
				break
			}
			res.Hits = append(res.Hits, prompts.VoiceHit{Path: h.Path, Line: clipRunes(oneLine(h.Line), 300)})
		}
	case "list_agents":
		agents, err := hub.agents(ctx)
		if err != nil {
			res.Code, res.Reason = "failed", hubReason(err)
			break
		}
		for _, a := range agents {
			if a.State == "offline" {
				continue
			}
			res.Agents = append(res.Agents, prompts.VoiceAgent{ID: a.MachineID + "/" + a.SessionID, Title: firstNonEmpty(a.Title, a.Cwd),
				State: a.State, Last: clipRunes(oneLine(a.LastText), 240)})
		}
	case "send_to_agent":
		task := clipRunes(in.Text, taskMost)
		if in.MachineID == "" || in.SessionID == "" || strings.TrimSpace(in.Text) == "" {
			res.Code = "bad_args"
			break
		}
		// As long as a send to the main conversation may take (sendTimeout): an agent that is offline is resumed first.
		sess, _ := hub.session(ctx, in.MachineID, in.SessionID)
		sctx, cancel := context.WithTimeout(ctx, sendTimeout(sess))
		err := hub.send(sctx, in.MachineID, in.SessionID, in.Text, voiceMessageID())
		cancel()
		if err != nil {
			code, reason := sendFailure(maybeSent(err))
			return prompts.RenderVoiceRefused(prompts.VoiceRefused{Lang: string(l), Code: code, Task: task, Reason: reason, Agent: true})
		}
	default:
		res.Code = "unknown_tool"
	}
	return prompts.RenderVoiceToolResult(res)
}

// oneLine is s with its runs of white space (newlines too) as one space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
