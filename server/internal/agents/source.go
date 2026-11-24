// Package agents is Persona's single view of the agents it can reach. Sources are running plugins' agent_sources
// contributions (extmgr/extension_agents.go) and the manager's explicitly registered bot endpoints (bot.go).
// A session's public id is "<source>:<sessionKey>" (the Lark gateway's own keys already start with "agent:").
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Unified models: the bridge's shapes are already the right ones, so they are reused, with the public id added.
type (
	Block   = AgentBlock
	Message = AgentMessage
)

type Session struct {
	ID     string `json:"id"` // <source>:<sessionKey>
	Source string `json:"source"`
	AgentSession
	// AvatarURL is the gateway's avatar (data: or https URL); only its presence and version go to the apps, which load
	// the image from /api/v1/agents/sessions/{id}/avatar.
	AvatarURL     string `json:"-"`
	HasAvatar     bool   `json:"hasAvatar,omitempty"`
	AvatarVersion string `json:"avatarVersion,omitempty"`
}

type MessagePage struct {
	Session    *Session  `json:"session,omitempty"`
	Messages   []Message `json:"messages"`
	HasMore    bool      `json:"hasMore"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type SearchHit struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	AgentSearchHit
}

// Status is one source's health and the agents it serves.
type Status struct {
	Source  string `json:"source"`
	Enabled bool   `json:"enabled"`
	Error   string `json:"error,omitempty"`
	// Setup is why a reachable source has nothing to list yet (SetupMissing / SetupDisabled / SetupNoAccounts); the
	// apps say what to do about it.
	Setup     string          `json:"setup,omitempty"`
	Providers []AgentProvider `json:"providers"`
	Accounts  []AgentAccount  `json:"accounts"`
}

// Status.Setup values.
const (
	SetupMissing    = "missing"     // the backend has no agent support (a Lark gateway from before agent-bridge)
	SetupDisabled   = "disabled"    // supported but turned off (by the extension's own settings)
	SetupNoAccounts = "no_accounts" // on, but no agent account is connected yet
)

// Source is one backend of agents and their sessions; keys are the source's own session keys.
type Source interface {
	Name() string
	Status(ctx context.Context) (Status, error)
	Sessions(ctx context.Context) ([]Session, error)
	Search(ctx context.Context, q string) ([]SearchHit, error)
	Messages(ctx context.Context, key, cursor string, limit int) (*MessagePage, error)
	Send(ctx context.Context, key, clientID, text string) (json.RawMessage, error)
	DeleteMessage(ctx context.Context, key, clientID string) error
	MarkRead(ctx context.Context, key string) error
}

var ErrBadID = errors.New("agents: session id must be <source>:<sessionKey>")

// SplitID splits a public id into source name and session key.
func SplitID(id string) (source, key string, err error) {
	source, key, ok := strings.Cut(id, ":")
	if !ok || source == "" || key == "" {
		return "", "", ErrBadID
	}
	return source, key, nil
}

// Wrap is a session of source as the apps see it: its id <source>:<sessionKey>, and its avatar when there is one.
func Wrap(source string, s AgentSession, avatarURL string) Session {
	return withAvatar(Session{ID: source + ":" + s.SessionKey, Source: source, AgentSession: s}, avatarURL)
}
