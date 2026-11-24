package feishubot

import (
	"context"
	"net/http"
	"net/url"
)

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
