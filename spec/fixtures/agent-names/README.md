# agent-names

What every client calls each coding agent (#10042): the name a person reads next to the agent's mark, in Agents, on the
token usage page's By Agent, wherever an agent is named by its kind. Keys are `proto.Session.Agent`'s names (the
node's agent kinds); values are the product's own spelling (opencode and pi write themselves in lower case), the same
in every language.

Checked by the web (`web/src/lib/agent-kind.test.ts`: `agentName`, from the `agent-mark` catalog) and the iPhone
(`apple/PersonaTests/TokenCalendarTests.swift`: `AgentKind(usageAgent:)`). A new agent kind gets its name here first.
