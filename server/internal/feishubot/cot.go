package feishubot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// An agent message (the open platform's message_cot): Feishu draws it as its own collapsible process bubble — one
// line ("Task completed >" once it ends) that opens onto the process, each paragraph followed by its folded tool calls. It
// is created empty, then fed AG-UI events (PUT, at most 50 a call), and ends with RUN_FINISHED (or Complete when it
// went wrong). Clients older than PC 7.70 / mobile 7.74 get a plain post Feishu writes itself.

// CotEvent is one AG-UI event of an agent message.
type CotEvent struct {
	Type      string `json:"event_type"`
	Content   string `json:"content"` // the event's JSON
	Timestamp int64  `json:"timestamp"`
}

func cotEvent(typ string, content any) CotEvent {
	b, _ := json.Marshal(content)
	return CotEvent{Type: typ, Content: string(b), Timestamp: time.Now().UnixMilli()}
}

// CotStart opens the run and its process (threadID: the conversation, runID: the turn, first: the first paragraph's id).
func CotStart(threadID, runID, first string) []CotEvent {
	return []CotEvent{
		cotEvent("RUN_STARTED", map[string]string{"threadId": threadID, "runId": runID}),
		cotEvent("REASONING_START", map[string]string{"messageId": first}),
	}
}

// CotParagraph is one paragraph of the process (thinking, or what the agent said between its tool calls).
func CotParagraph(id, text string) []CotEvent {
	return []CotEvent{
		cotEvent("REASONING_MESSAGE_START", map[string]string{"messageId": id, "role": "reasoning"}),
		cotEvent("REASONING_MESSAGE_CONTENT", map[string]string{"messageId": id, "delta": text}),
		cotEvent("REASONING_MESSAGE_END", map[string]string{"messageId": id}),
	}
}

// CotToolCall is a tool call under paragraph parent. Feishu draws only its title (not its arguments), so what it did
// goes there; icon is one of bash, write, read, search, task, default.
func CotToolCall(id, parent, name, title, icon string) []CotEvent {
	start := map[string]string{"toolCallId": id, "toolCallName": name, "title": title, "icon": icon}
	if parent != "" {
		start["parentMessageId"] = parent
	}
	return []CotEvent{cotEvent("TOOL_CALL_START", start), cotEvent("TOOL_CALL_END", map[string]string{"toolCallId": id})}
}

// CotToolResult settles a tool call: its output as code, or a line of text (an empty output still needs one, or the
// call keeps spinning).
func CotToolResult(id, output string, code bool) CotEvent {
	body := map[string]string{"type": "text", "text": output}
	if code {
		body = map[string]string{"type": "code", "code": output}
	}
	b, _ := json.Marshal(body)
	return cotEvent("TOOL_CALL_RESULT", map[string]string{"messageId": "tr-" + id, "toolCallId": id, "role": "tool", "content": string(b)})
}

// CotFinish closes the process (last: the last paragraph's id) and the run, which Feishu then shows as done.
func CotFinish(threadID, runID, last string) []CotEvent {
	return []CotEvent{
		cotEvent("REASONING_END", map[string]string{"messageId": last}),
		cotEvent("RUN_FINISHED", map[string]string{"threadId": threadID, "runId": runID, "status": "done"}),
	}
}

// CotCreate starts an agent message in a chat, under the message replyTo answers (when given), in that
// message's topic when inThread is set; it returns the message's cot id and message id.
func (c *Client) CotCreate(ctx context.Context, chatID, replyTo string, inThread bool) (cotID, messageID string, err error) {
	in := map[string]any{"receive_id": chatID}
	if replyTo != "" {
		in["origin_message_id"] = replyTo
		if inThread {
			in["reply_in_thread"] = true
		}
	}
	var out struct {
		CotID     string `json:"cot_id"`
		MessageID string `json:"message_id"`
	}
	if err := c.call(ctx, "cot create", http.MethodPost, "/open-apis/im/v1/message_cot?receive_id_type=chat_id", in, &out); err != nil {
		return "", "", err
	}
	if out.CotID == "" || out.MessageID == "" {
		return "", "", &APIError{Op: "cot create", Msg: "no cot_id / message_id in the answer"}
	}
	return out.CotID, out.MessageID, nil
}

// CotAppend adds events to an agent message, 50 a call.
func (c *Client) CotAppend(ctx context.Context, cotID, messageID string, events []CotEvent) error {
	for i := 0; i < len(events); i += 50 {
		in := map[string]any{"cot_id": cotID, "message_id": messageID, "events": events[i:min(i+50, len(events))]}
		if err := c.call(ctx, "cot append", http.MethodPut, "/open-apis/im/v1/message_cot", in, nil); err != nil {
			return err
		}
	}
	return nil
}

// CotComplete ends an agent message that will not get its RUN_FINISHED (reason "done" or "error").
func (c *Client) CotComplete(ctx context.Context, cotID, messageID, reason string) error {
	q := url.Values{"message_id": {messageID}, "reason": {reason}}
	return c.call(ctx, "cot complete", http.MethodPost, "/open-apis/im/v1/message_cot/complete/"+url.PathEscape(cotID)+"?"+q.Encode(), nil, nil)
}
