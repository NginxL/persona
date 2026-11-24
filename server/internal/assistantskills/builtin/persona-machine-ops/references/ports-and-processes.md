# Listening ports and processes (#12352)

Inspect active listening sockets and running processes with command-line secret redaction, protection annotations, and stale/orphan test process heuristics.

## Commands and MCP tools

- **Listening sockets:**
  - CLI: `ops.sh ports [--protocol tcp|udp]`
  - MCP: `machine_ops_ports` (`{protocol?: "tcp" | "udp"}`)
  - Returns `ports[]` with `protocol`, `bind_addr`, `port`, `scope` (`loopback`, `all`, `interface`), `pid`, `user`, `process`, and redacted `cmdline`.

- **Running processes:**
  - CLI: `ops.sh processes [--sort cpu|memory|uptime] [--query TEXT] [--orphan-only] [--limit N]`
  - MCP: `machine_ops_processes` (`{sort?: "cpu" | "memory" | "uptime", query?: string, orphan_only?: bool, limit?: int}`)
  - `limit` defaults to 100 and is capped at 300.
  - Each process includes `owned`, `protected` / `protected_reason`, `likely_orphan` / `orphan_reasons`, `cpu_percent`, `rss_bytes`, `uptime_seconds`, and redacted `cmdline`.

## Boundary

Read-only inspection only. Process termination (`kill`) is never exposed through `ops` CLI or `ops mcp`; do not attempt to kill `protected` or non-owned processes.
