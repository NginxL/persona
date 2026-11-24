package extmgr

import (
	"context"

	"github.com/nwverse/persona/server/internal/mailwire"
)

// Store is the hub's Postgres as this code keeps its state in it (internal/db; the same keys and tables as when
// they were the hub's: plugins, plugin_secrets, plugin_audit, the markets, connector_secrets, mail_accounts, the
// settings of the connectors, bots and channels). The registry's own tables are extension.Store's.
type Store interface {
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
}

// secretStore keeps the connectors' credentials (connector_secrets).
type secretStore interface {
	ConnectorSecret(ctx context.Context, connector string) (string, bool, error)
	PutConnectorSecret(ctx context.Context, connector, sealed string) error
	DeleteConnectorSecret(ctx context.Context, connector string) error
}

// sendStore is the once-only guard of sends (sent_messages).
type sendStore interface {
	ClaimSend(ctx context.Context, id, machine, session string) (string, error)
	SettleSend(ctx context.Context, id, state string) error
}

// mailStore keeps the mail accounts (mail_accounts; mail seals their secrets itself).
type mailStore = mailwire.Store

// Mail is the mail module (internal/modules/mail) as this code uses it; nil when the build has none (mailport).
type Mail = mailwire.Service
