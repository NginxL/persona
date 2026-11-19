package persona.core.contracts

private val agyPersonaMcpTool = Regex(
    "^mcp_(?:${ProtocolCompatibility.personaServers.joinToString("|")})(?:_[0-9a-fA-F]{8,16})?_([a-z][a-z0-9_]*)$"
)

/** Normalizes Antigravity's single-underscore Persona MCP names (`mcp_persona_<tool>`, `mcp_persona_<hex>_<tool>`) to `mcp__persona__<tool>`. */
fun canonicalMcpToolName(name: String): String =
    if (name.startsWith("mcp_") && !name.startsWith("mcp__")) {
        agyPersonaMcpTool.matchEntire(name)?.let { "mcp__persona__${it.groupValues[1]}" } ?: name
    } else name

/** The MCP server's name as shown: Persona's own server keeps its old id `persona` (allow lists and hooks match it). */
fun mcpServerLabel(server: String): String = server.removePrefix("plugin_").let { if (it in ProtocolCompatibility.personaServers) "Persona" else it }

/**
 * A tool's name for a sentence (a permission card): an MCP tool as "Persona · mail send", others as written; null
 * without a name (the card says "tool", [CoreText.Key.ToolGeneric]).
 */
fun toolDisplayName(rawName: String?): String? {
    if (rawName.isNullOrEmpty()) return null
    val name = canonicalMcpToolName(rawName)
    if (!name.startsWith("mcp__")) return name
    val rest = name.removePrefix("mcp__")
    val i = rest.lastIndexOf("__")
    if (i <= 0) return if (rest.isEmpty()) name else mcpServerLabel(rest)
    val tool = rest.substring(i + 2).replace('_', ' ')
    return if (tool.isEmpty()) mcpServerLabel(rest.substring(0, i)) else "${mcpServerLabel(rest.substring(0, i))} · $tool"
}
