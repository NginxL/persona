package imessage

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector/conformance"
	"github.com/nwverse/persona/server/internal/connectorwire"
)

// iMessage is one implementation of the connector protocol: what it gives directly, it gives through the wire.
func TestConformance(t *testing.T) {
	c, _ := personaRemote(t, map[string]string{"connector.chats": chatsPage(time.Now()), "connector.older": olderPage,
		"connector.changes": `{"next":"42","batches":[{"chat":{"id":"imessage-0123456789ab","name":"张三","members":[` + zhangJSON + `],"last":"2026-10-30T08:00:00Z"},
		"messages":[{"id":"7","at":"2026-10-30T07:00:00Z","kind":"message","sender":` + zhangJSON + `,"text":"示例：周五见"}]}]}`})
	c.look(context.Background(), func(connectorwire.Scope) {})
	conformance.Check(t, c)
}
