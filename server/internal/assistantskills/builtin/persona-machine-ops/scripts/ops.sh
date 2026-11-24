#!/bin/sh
# Runs `ops …` (docs/server/machines.md) with this machine's Persona program: the one the node names (PERSONA_BIN), else
# where Persona installs it (personad, or persona-node: both carry the read-only ops CLI and MCP server), else on PATH.
# Exit 3 when there is none.
for b in "$PERSONA_BIN" "$HOME/.persona/bin/personad" "$HOME/.persona/bin/persona-node" /opt/persona/bin/personad \
	"$(command -v personad 2>/dev/null)" "$(command -v persona-node 2>/dev/null)"; do
	if [ -n "$b" ] && [ -x "$b" ]; then
		exec "$b" ops "$@"
	fi
done
echo "persona-machine-ops: no personad or persona-node on this machine (looked in ~/.persona/bin and PATH)" >&2
exit 3
