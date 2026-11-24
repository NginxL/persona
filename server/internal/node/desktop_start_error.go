package node

import (
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Once native creation has happened, a failed import or send must never make a caller retry session.start.
// Include the session in the error because the RPC transport discards a result returned alongside an error.
func desktopCreatedError(s *proto.Session, err error, firstMessageAttempted bool) error {
	delivery := deliveredNone
	if firstMessageAttempted {
		delivery = deliveredUnknown
	}
	data := map[string]any{
		"delivered": deliveredUnknown, "created": true, "session_id": s.SessionID, "session": s,
		"first_message_delivered": delivery,
	}
	var cause *rpc.Error
	if errors.As(err, &cause) {
		data["cause_code"] = cause.Code
		if delivery := deliveredOf(cause); firstMessageAttempted && delivery != "" {
			data["first_message_delivered"] = delivery
		}
	}
	b, _ := json.Marshal(data)
	return &rpc.Error{Code: "desktop_start_incomplete", Message: "The desktop session was created, but setup or the first message did not complete. Continue in the existing session: " + err.Error(), Data: b}
}
