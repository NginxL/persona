package cliwire

import "context"

// ConnectorID is the id of the lark-cli connector (Lark via lark-cli).
const ConnectorID = "larkcli"

// StateStore is the small part of the manager's Postgres-backed settings store
// needed by lark-cli Inbox state. It intentionally has the same method set as
// extmgr.Store, so the connector never writes a file under ~/.persona.
type StateStore interface {
	Setting(context.Context, string, any) (bool, error)
	PutSetting(context.Context, string, any) error
}
