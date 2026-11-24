package botcard

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// APIError is Feishu's {code, msg} for a failed call.
type APIError struct {
	Op   string
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("feishu %s: %d %s", e.Op, e.Code, e.Msg)
}

// Token-related codes: the token expired or is not valid any more; one fresh token and one retry fix it.
func TokenGone(code int) bool { return code == 99991663 || code == 99991664 || code == 99991661 }

// Refused: Feishu will not take this message however often it is sent (the card's content refused, the bot not in
// the chat or not allowed to reach the user, the app missing a permission); sending it again cannot help.
func Refused(err error) bool {
	switch ErrorCode(err) {
	case 230099, 230025, 200621, 200830, 230002, 230006, 230013, 99991672, 99991679:
		return true
	}
	return false
}

// RateLimited: Feishu refused the call for coming too often (230020: too many messages into one chat, or a message
// updated too often; 230429: the operation's frequency limit; 99991400: the app's rate limit; HTTP 429).
func RateLimited(err error) bool {
	switch ErrorCode(err) {
	case 230020, 230429, 99991400, http.StatusTooManyRequests:
		return true
	}
	return err != nil && strings.Contains(err.Error(), "HTTP 429")
}

// MessageGone: the message to update was withdrawn (230011) or deleted (230110).
func MessageGone(err error) bool {
	c := ErrorCode(err)
	return c == 230011 || c == 230110
}

// Transient: the call failed for a reason that may pass, so trying again later can work: Feishu gave no answer at all
// (the network, a timeout, the connection to the hub), it limited the rate, a server failed (5xx) or the token had to be
// renewed. Any other answer is Feishu's word on that message or chat (deleted, past its edit period, not a card, not
// allowed) and will not change.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	c := ErrorCode(err)
	return c == 0 || RateLimited(err) || c >= 500 && c < 600 || TokenGone(c)
}

// ChatRefused: Feishu said the bot cannot send into that chat: it is not in it (230002) or the chat was dissolved
// (232009). Nothing was sent. Also when the error came over the bus as text.
func ChatRefused(err error) bool {
	c := ErrorCode(err)
	return c == 230002 || c == 232009
}

// ErrorCode is an APIError's code, also when it came over the bus as text ("feishu <op>: <code> <msg>"); 0 if none.
func ErrorCode(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return 0
	}
	msg := err.Error()
	i := strings.Index(msg, "feishu ")
	if i < 0 {
		return 0
	}
	j := strings.Index(msg[i:], ": ")
	if j < 0 {
		return 0
	}
	f := strings.Fields(msg[i+j+2:])
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(f[0])
	return n
}

// CardRejected: Feishu refused a card's content (a field it does not take, Markdown it cannot read, too large); the
// hub sends the text as rich text instead.
func CardRejected(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case 230099, 230025, 200621, 200830:
		return true
	}
	return strings.Contains(strings.ToLower(e.Msg), "card")
}
