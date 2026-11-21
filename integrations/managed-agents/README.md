# Managed Agents runtime

Managed Agents sessions (Volcengine Ark's hosted endpoint, or one you run yourself) in Persona's Agents, with your own
key. What it does, how to configure it and what it assumes: `docs/integrations/managed-agents.md`.

```sh
node integrations/managed-agents/companion.mjs            # next to the node, on its runtime.sock
node integrations/managed-agents/companion.mjs --check    # read-only: each profile's key and list, counts only
PERSONA_DAEMON_TOKEN=<token> node integrations/managed-agents/companion.mjs --listen <port>   # as a daemon
```

Node 22.19+, no dependencies. Tests: `npm test`; end to end: `PERSONAD=<personad> node e2e/run.mjs`.
