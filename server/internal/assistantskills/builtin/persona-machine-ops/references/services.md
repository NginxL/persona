# Services, containers, and bounded logs (#12353)

Inspect user systemd/launchd services and Docker containers, and read bounded tail logs.

## Commands and MCP tools

- **List services and containers:**
  - CLI: `ops.sh services`
  - MCP: `machine_ops_services` (no parameters)
  - Returns `managers[]` (`kind`, `available`, `unavailable`), `services[]` (user `systemd` on Linux or `launchd` on macOS), and `containers[]` (`docker`). Each item includes `id`, `name`, `state`, `sub_state`, `status`, `description`, `pid`, and `protected` / `protect_note`.

- **Bounded log tail:**
  - CLI: `ops.sh service-logs --kind systemd|launchd|docker --id <id> [--lines N]`
  - MCP: `machine_ops_service_logs` (`{kind: "systemd" | "launchd" | "docker", id: string, lines?: int}`)
  - `id` is strictly validated (alphanumeric start, no `..`, no shell metacharacters).
  - `lines` defaults to 100 and is clamped to `[1, 500]`; output is capped at 64 KiB (`truncated: true` when trimmed).

## Boundary

Read-only inspection only. Service start/stop/restart actions are never exposed through `ops` CLI or `ops mcp`.
