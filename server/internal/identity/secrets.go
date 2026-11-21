package identity

// The data directory's secrets: one list for the three things that keep them from what is asked for, the node's path
// guard (node/pathguard.go: files a client names by path), the agent sandbox (extension/sandbox/agent.go: files an
// agent's own processes open) and `personad hub doctor` (hub/doctor.go: the modes the credential files are kept in).
// The guard and the sandbox match them where they are written, at the top of the data directory (the path guard also
// at the top of a manager's folder, managers/<id>/, which the sandbox closes whole, and of a manager's folder
// anywhere, one holding a manager's manager.json); docs/server/agent-sandbox.md "The rules close names at the top of the data directory" says where the two
// differ.

import (
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// DataSecrets are the credential files in the data directory by the start of their name, so a backup beside one is
// matched too: hub.json (the token, the database password), Persona Link's credential, the tunnel identities (a
// private key and the Cloudflare tunnel token), the daemons' tokens, a manager's bus token, the APNs key.
var DataSecrets = []string{"hub.json", "link.json", "tailcat.json", "tailcat-hub.json", "daemons.json", "manager.json", "apns",
	"relay.json", "relay.secret", "relay-tunnel.token", "devices.json", "mail_accounts.json"}

// dataCredentialExtensions is the single extension source for the regular expression and its glob equivalent.
var dataCredentialExtensions = []string{"key", "p8", "pem", "crt", "token", "secret", "password"}

// DataCredentialPattern matches the end of a credential file's name, backups included (secret.key, secret.key.bak,
// handles.key~, the APNs .p8, relay-tunnel.token, a .password file): a regular expression both Go's regexp and
// sandbox-exec's read the same way.
var DataCredentialPattern = `\.(` + strings.Join(dataCredentialExtensions, "|") + `)([.~-][^/]*)?$`

// DataCredentialGlobs is DataCredentialPattern expressed as basename globs for permission-rule engines. The three
// backup forms mirror the pattern's dot, tilde and dash suffix; a final * may be empty.
func DataCredentialGlobs() []string {
	var globs []string
	for _, ext := range dataCredentialExtensions {
		globs = append(globs, "*."+ext, "*."+ext+".*", "*."+ext+"~*", "*."+ext+"-*")
	}
	return globs
}

// credentialExt compiles the shared pattern when a name first needs matching.
var credentialExt = lazyre.New(DataCredentialPattern)

// DataSecretName says whether name, a file directly in a data directory (or in a manager's folder), is one of its
// credential files: a known one by the start of its name (a backup beside it included), or any file whose name ends
// in a key or credential suffix. The node's path guard and `hub doctor` classify a name with this, the same lists the
// agent sandbox writes into its profile.
func DataSecretName(name string) bool {
	for _, secret := range DataSecrets {
		if strings.HasPrefix(name, secret) {
			return true
		}
	}
	return credentialExt.MatchString(name)
}

// DataSecretDirs are folders in the data directory that are secret whole, by their path in it: the launchers' merged
// settings hold their tokens, tls/ the hub's TLS key (hub/public.go), link-tls/ Persona Link's (link/tlsid.go),
// docker/ the Postgres password the container entrypoint writes (docs/operations/docker.md), extensions/data/ the
// extensions' own data, their sign-ins and tokens among it, and extensions/staging/ what an uninstall or an upgrade
// sets aside of it (extension/registry_install.go, plan.go); plugins/, whole, is the extensions' folder of a hub from
// before #1844 (or the link to extensions/ it left, extmgr/extension_compat.go) and a node's from before #1921;
// node-plugins/, whole, the programs extensions run on this computer and their data (node/plugins.go). Only Persona's
// own processes use these.
var DataSecretDirs = []string{"launcher-settings", "tls", "link-tls", "docker", "extensions/data", "extensions/staging",
	"plugins", "node-plugins"}
