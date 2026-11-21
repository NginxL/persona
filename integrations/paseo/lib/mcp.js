// Persona's agent MCP tools for the agents the bridge creates (docs/integrations/paseo.md "Create"). Paseo takes
// stdio MCP servers in an agent's config.mcpServers and keeps them across reloads, so this reads the node's mcp.json
// and gives each agent its own copy, naming the Persona session the agent belongs to.

import { readFileSync } from 'node:fs'

/**
 * The servers of the Claude-style config file at path, as Paseo's config.mcpServers: stdio, each with
 * PERSONA_SESSION_ID in its environment (the agent's Persona session, not the provider's own session id).
 * Nothing usable (no path, no file, no stdio server): undefined, with the reason logged; creating the agent goes on.
 */
export function personaMcpServers(path, sessionId, log = () => {}, a2aToken) {
  if (!path) {
    log('paseo: no MCP config given; the new agent gets no Persona tools')
    return undefined
  }
  let servers
  try {
    servers = JSON.parse(readFileSync(path, 'utf8'))?.mcpServers
  } catch (err) {
    log(`paseo: cannot read the MCP config ${path} (${err.message}); the new agent gets no Persona tools`)
    return undefined
  }
  const out = {}
  for (const [name, s] of Object.entries(servers && typeof servers === 'object' ? servers : {})) {
    if (!s || typeof s.command !== 'string' || !s.command || (s.type && s.type !== 'stdio')) continue
    const env = {}
    for (const [k, v] of Object.entries(s.env ?? {})) if (typeof v === 'string') env[k] = v
    out[name] = {
      type: 'stdio',
      command: s.command,
      ...(Array.isArray(s.args) ? { args: s.args.filter((a) => typeof a === 'string') } : {}),
      env: { ...env, PERSONA_SESSION_ID: sessionId },
    }
  }
  if (!Object.keys(out).length) {
    log(`paseo: the MCP config ${path} has no stdio server; the new agent gets no Persona tools`)
    return undefined
  }
  const persona = out.persona ?? out.jarvis
  if (a2aToken && persona) {
    out.persona_a2a = { ...persona, env: { ...persona.env, PERSONA_A2A_TOKEN: a2aToken } }
  }
  return out
}
