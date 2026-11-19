package compat

import "strings"

// EnvironmentMap mirrors only the process-owned values supplied by the caller.
// Canonical presence wins over a legacy alias, even for an empty value.
func EnvironmentMap(input map[string]string) map[string]string {
	out := make(map[string]string, len(input)*2)
	for key, value := range input {
		out[key] = value
	}
	for key, value := range input {
		current, legacy := EnvNames(key)
		if canonical, ok := input[current]; ok {
			value = canonical
		}
		out[current], out[legacy] = value, value
	}
	return out
}

// AddMCPAliases registers the identical built-in handler under both namespaces.
// Extension namespaces are not cloned, and their permission scopes never change.
func AddMCPAliases(servers map[string]any) map[string]any {
	return MCPRegistrationServers(servers, true)
}

// MCPRegistrationServers prepares the built-in handler for a new process. Legacy
// peers retain the alias; negotiated assistant starts, including resumes, use only
// Persona. Extension namespaces and their permission scopes never change.
func MCPRegistrationServers(servers map[string]any, legacy bool) map[string]any {
	builtin := servers["persona"]
	if builtin == nil {
		return servers
	}
	if config, ok := builtin.(map[string]any); ok {
		if env, ok := config["env"].(map[string]string); ok {
			config["env"] = EnvironmentMap(env)
		}
	}
	if legacy {
		servers[MCPServer] = builtin
	}
	return servers
}

// MCPToolNames recognizes both namespaces in historical transcripts, regardless
// of which names a newly started process registers or permits.
func MCPToolNames(tools []string) []string {
	return MCPAllowedToolNames(tools, true)
}

// MCPAllowedToolNames returns exactly the tool names permitted by the process's
// registration policy. Use MCPToolNames instead when reading historical calls.
func MCPAllowedToolNames(tools []string, legacy bool) []string {
	out := make([]string, 0, len(tools)*2)
	for _, name := range tools {
		out = append(out, "mcp__persona__"+name)
		if legacy {
			out = append(out, "mcp__"+MCPServer+"__"+name)
		}
	}
	return out
}

var agyPersonaMCPTool = lazy(`^mcp_(?:persona|` + MCPServer + `)(?:_[0-9a-fA-F]{8,16})?_([a-z][a-z0-9_]*)$`)

// CanonicalMCPToolName normalizes Antigravity's single-underscore Persona MCP names
// (mcp_persona_<tool>, mcp_persona_<hex>_<tool> and their legacy alias) to mcp__persona__<tool>.
func CanonicalMCPToolName(name string) string {
	if strings.HasPrefix(name, "mcp_") && !strings.HasPrefix(name, "mcp__") {
		if m := agyPersonaMCPTool().FindStringSubmatch(name); m != nil {
			return "mcp__persona__" + m[1]
		}
	}
	return name
}

func CodexMCPAliases(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && strings.HasPrefix(args[i+1], "mcp_servers.persona.") {
			out = append(out, "-c", strings.Replace(args[i+1], "mcp_servers.persona.", "mcp_servers."+MCPServer+".", 1))
		}
	}
	return out
}
