package connector

import (
	"github.com/nwverse/persona/server/internal/connectorwire"
)

// Defined in the contract package connectorwire (docs/server/architecture.md "Contract packages"); the old names stay.
type (
	SnapshotChat    = connectorwire.SnapshotChat
	SnapshotChats   = connectorwire.SnapshotChats
	SnapshotData    = connectorwire.SnapshotData
	SnapshotMeeting = connectorwire.SnapshotMeeting
)
