package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	ExternalAdapterProtocol = 1
	ExternalLauncherSource  = "external"
	ExternalLauncherPrefix  = "persona_ext_"
	ExternalMainChatScope   = "main-chat-pick"
)

// ExternalLauncherName reserves a shell-safe identity independently of display names.
func ExternalLauncherName(adapter, target string) string {
	sum := sha256.Sum256([]byte(adapter + "\x00" + target))
	return ExternalLauncherPrefix + hex.EncodeToString(sum[:16])
}

// IsExternalLauncher also recognizes a disabled or missing adapter's old bindings.
func IsExternalLauncher(name string) bool {
	id, ok := strings.CutPrefix(name, ExternalLauncherPrefix)
	if !ok || len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

// ExternalInventory is the sanitized output of a locally configured adapter.
// It contains no executable paths, endpoint URLs, environment values or credentials.
type ExternalInventory struct {
	Protocol int              `json:"protocol"`
	Targets  []ExternalTarget `json:"targets"`
}

type ExternalCapabilities struct {
	Contexts  []string `json:"contexts"`
	Runtimes  []string `json:"runtimes"`
	Platforms []string `json:"platforms"`
}

type ExternalTarget struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Engine          string               `json:"engine"`
	Revision        string               `json:"revision"`
	Capabilities    ExternalCapabilities `json:"capabilities"`
	CredentialState string               `json:"credential_state"`
	Models          []AgentModel         `json:"models"`
	UnavailableCode string               `json:"unavailable_code,omitempty"`
}

// ExternalLauncher is a machine's advertisement, not permission to execute it.
// The node resolves every command exclusively through its own local registry.
type ExternalLauncher struct {
	AdapterID string `json:"adapter_id"`
	ExternalTarget
	Stale bool `json:"stale,omitempty"`
}

// ExternalExecution is created by the manager only for an explicit main-chat pick.
// A client cannot obtain execution by supplying its own source, scope or command.
type ExternalExecution struct {
	Scope     string `json:"scope"`
	SessionID string `json:"session_id"`
	AdapterID string `json:"adapter_id"`
	TargetID  string `json:"target_id"`
	Revision  string `json:"revision"`
}
